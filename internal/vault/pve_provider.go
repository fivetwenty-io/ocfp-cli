package vault

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/artifacts"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/providers"
	"github.com/ocfp/ocfp-cli-go/internal/pve/capacity"
	"github.com/ocfp/ocfp-cli-go/internal/state"
	"go.uber.org/zap"
)

// PVEVaultProvider implements vault operations for Proxmox VE.
type PVEVaultProvider struct {
	*providers.BaseVaultProvider

	Safe        SafeInterface
	PathBuilder *PathBuilder
	logger      *zap.SugaredLogger

	// BlobstoreEndpoint is the S3-compatible endpoint URL supplied by the operator via
	// --blobstore-endpoint. When empty and BlobstoreMode is local/empty, ConfigureBlobstores
	// writes only the local-mode marker. When BlobstoreEndpoint is set, the endpoint plus
	// region/path_style flags are written to cf/blobstores/main, and credentials go to a
	// separate cf/blobstores/main/creds path.
	BlobstoreEndpoint string

	// BlobstoreMode is "local" (default) or "external". Determines whether bucket
	// creation runs and what gets written to vault.
	BlobstoreMode string

	// BlobstoreRegion is the S3 region (default "us-east-1" when empty).
	BlobstoreRegion string

	// BlobstoreAccessKey + BlobstoreSecretKey carry external-mode S3 credentials.
	// Written to a separate vault path so the config path stays secret-free.
	BlobstoreAccessKey string
	BlobstoreSecretKey string //nolint:gosec // field name is descriptive

	// blobstoreCACert is the PEM CA cert sourced from artifacts state (internal-ca
	// or self-signed TLS mode). Written to vault alongside endpoint/region so
	// genesis kits can pin it. Populated by ConfigureBlobstores when auto-sourcing
	// from bootstrap state; remains empty when the operator supplies flags. May
	// also be populated later, by blobstoreS3Target recovering the bloc CA from
	// vault when state's copy is missing — see blobstoreTLSMode.
	blobstoreCACert string

	// blobstoreTLSMode is the artifacts tls.mode ("internal-ca", "self-signed",
	// "disabled", or "" when unknown — e.g. a manual --blobstore-endpoint
	// override with no corresponding artifacts state) sourced alongside
	// blobstoreCACert by ConfigureBlobstores. resolveBlobstoreTLSTrust uses it
	// to decide whether a missing CA cert is recoverable from vault
	// (internal-ca) or must fall back to a logged skip-verify (everything
	// else) — it is never silently treated as internal-ca-safe.
	blobstoreTLSMode string

	// blobstoreTLSResolved marks that resolveBlobstoreTLSTrust has already run
	// for this provider instance (single blobstore-populate invocation).
	// Idempotency guard: without it, calling resolveBlobstoreTLSTrust from
	// both configureExternalBlobstore (before the CF config write) and
	// blobstoreS3Target (bucket creation) would re-attempt vault CA recovery
	// and, on the self-signed/legacy fallback path, double-log the
	// skip-verify warning.
	blobstoreTLSResolved bool

	// blobstoreAllowInsecure caches whether resolveBlobstoreTLSTrust fell back
	// to the skip-verify-with-warning path (self-signed/disabled-but-https/
	// unknown tls_mode with no CA available). Read by blobstoreS3Target when
	// building the Endpoint for bucket creation.
	blobstoreAllowInsecure bool

	// bucketEnsurer creates the blobstore buckets a written secret points at, so
	// the secret and its backing bucket are always provisioned together. nil
	// defaults to the live S3 implementation; tests inject a recording stub.
	bucketEnsurer blobstoreBucketEnsurer
}

// blobstoreBucketEnsurer creates blobstore buckets on the external S3 endpoint.
// Seam for tests; the production implementation calls artifacts.EnsureBuckets.
type blobstoreBucketEnsurer interface {
	EnsureBuckets(ctx context.Context, ep artifacts.Endpoint, creds artifacts.Credentials, buckets []artifacts.BucketSpec) error
}

// s3BucketEnsurer is the live blobstoreBucketEnsurer backed by the artifacts
// package's idempotent S3 bucket creation.
type s3BucketEnsurer struct{}

func (s3BucketEnsurer) EnsureBuckets(ctx context.Context, ep artifacts.Endpoint, creds artifacts.Credentials, buckets []artifacts.BucketSpec) error {
	return artifacts.EnsureBuckets(ctx, ep, creds, buckets)
}

// noopBucketEnsurer skips bucket creation entirely. Dry-run populate injects
// it so previewing a plan can never touch the blobstore.
type noopBucketEnsurer struct{}

func (noopBucketEnsurer) EnsureBuckets(_ context.Context, _ artifacts.Endpoint, _ artifacts.Credentials, _ []artifacts.BucketSpec) error {
	return nil
}

// NewPVEVaultProvider creates a new Proxmox VE vault provider.
func NewPVEVaultProvider(cfg *config.Config, safe SafeInterface, blocName string) *PVEVaultProvider {
	return &PVEVaultProvider{
		BaseVaultProvider: providers.NewBaseVaultProvider(cfg, blocName),
		Safe:              safe,
		PathBuilder:       NewPathBuilder(cfg, blocName),
		logger:            logger.Get(),
		bucketEnsurer:     s3BucketEnsurer{},
	}
}

// Configure performs full vault configuration for Proxmox VE.
func (p *PVEVaultProvider) Configure(reporter providers.ProgressReporter) error {
	p.logger.Infow("Starting PVE vault configuration", "bloc", p.BlocName)

	// Track phase numbers across entire configuration (0-based for ReportPhaseStart)
	phaseIndex := 0

	// Total phases: 1 (config) + 7 (mgmt: networks, subnets, security-groups, blobstores, databases, load-balancers, fqdns)
	//               + 7 (ocf: same) + 2 (shared: certificates, public-ips) = 17
	totalPhases := 17

	// Save OCFP configuration to vault first (phase 1)
	err := p.SaveConfigToVault(reporter, phaseIndex, totalPhases)
	if err != nil {
		return fmt.Errorf("failed to save config to vault: %w", err)
	}

	phaseIndex++

	// Configure both management and OCF environments
	for _, envType := range []string{"mgmt", "ocf"} {
		err := p.configureEnvironment(envType, reporter, &phaseIndex, totalPhases)
		if err != nil {
			return err
		}
	}

	// Configure shared components
	err = p.configureSharedComponents(reporter, &phaseIndex, totalPhases)
	if err != nil {
		return err
	}

	// Report final summary
	if reporter != nil {
		reporter.ReportFinalSummary(true, 0, totalPhases, 0)
	}

	p.logger.Infow("PVE vault configuration completed", "bloc", p.BlocName)

	return nil
}

// SaveConfigToVault saves the OCFP configuration to vault.
func (p *PVEVaultProvider) SaveConfigToVault(reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
	phaseName := PhaseConfig
	phaseStart := time.Now()

	if reporter != nil {
		reporter.ReportPhaseStart(phaseName, phaseNum, totalPhases)
	}

	p.logger.Info("Saving OCFP configuration to vault")

	configPath := p.PathBuilder.GetConfigPath()

	configData := map[string]any{
		"provider":        "pve",
		"bloc":            p.BlocName,
		"host":            p.Config.APIEndpoint,
		"node":            p.Config.Region,
		"network_mode":    "bridge",
		"default_bridge":  "vmbr0",
		"default_storage": "local-lvm",
	}

	err := p.Safe.SetMultiple(configPath, configData)
	if err != nil {
		return fmt.Errorf("failed to save config to vault: %w", err)
	}

	if reporter != nil {
		reporter.ReportPhaseComplete(phaseName, time.Since(phaseStart))
	}

	return nil
}

// ConfigureNetworks configures network settings.
func (p *PVEVaultProvider) ConfigureNetworks(_envPath, envType string, reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
	phaseName := "networks-" + envType
	phaseStart := time.Now()

	if reporter != nil {
		reporter.ReportPhaseStart(phaseName, phaseNum, totalPhases)
	}

	p.logger.Infow("Configuring networks", "env_type", envType)

	// Proxmox uses bridges instead of VPCs
	networkPath := p.PathBuilder.GetNetPath(envType)

	cidr := p.Config.VPCCIDRBlock
	if p.Config.Network.CIDR != "" {
		cidr = p.Config.Network.CIDR
	}

	// dns / region are read directly by the bosh kit via
	// meta.ocfp.bosh.{dns,region} (see kits/bosh/ocfp/meta.yml) — must be
	// present even on stateless lab populates.
	dns := pveFirstNonEmpty(pveFirstDNS(p.Config.DNS), pveCIDRGateway(cidr), "1.1.1.1")
	region := p.Config.Region

	networkConfig := map[string]any{
		"type":   "bridge",
		"bridge": pveFirstNonEmpty(p.Config.Network.Name, "vmbr0"),
		"cidr":   cidr,
		"dns":    dns,
		"region": region,
	}

	err := p.Safe.SetMultiple(networkPath, networkConfig)
	if err != nil {
		return fmt.Errorf("failed to set network configuration: %w", err)
	}

	if reporter != nil {
		reporter.ReportPhaseComplete(phaseName, time.Since(phaseStart))
	}

	return nil
}

// ConfigureSubnets writes per-subnet entries to vault, sourcing CIDR, gateway,
// and availability zone from bootstrap state's `subnet` resources. Each subnet
// matching the bloc-name prefix is written to:
//
//	{subnetsPath}/{subnetName}                          → cidr, az, gateway
//	{subnetsPath}/{subnetName}/reserved-ips/{role}      → ip
//
// When no bootstrap state is present the method falls back to the legacy
// single-blob write so `populate` does not fail in stateless contexts.
//
//nolint:cyclop // state-driven vs legacy-fallback subnet writer; branches map to distinct vault layouts
func (p *PVEVaultProvider) ConfigureSubnets(_envPath, envType string, reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
	phaseName := "subnets-" + envType
	phaseStart := time.Now()

	if reporter != nil {
		reporter.ReportPhaseStart(phaseName, phaseNum, totalPhases)
	}

	p.logger.Infow("Configuring subnets", "env_type", envType)

	sm := p.loadStateManager()
	if sm == nil {
		// No state? Fall back to old behavior so populate still writes something.
		err := p.writeFallbackSubnet(envType)
		if err != nil {
			return err
		}

		if reporter != nil {
			reporter.ReportPhaseComplete(phaseName, time.Since(phaseStart))
		}

		return nil
	}

	subnets, err := sm.GetResourcesByType("subnet")
	if err != nil || len(subnets) == 0 {
		err := p.writeFallbackSubnet(envType)
		if err != nil {
			return err
		}

		if reporter != nil {
			reporter.ReportPhaseComplete(phaseName, time.Since(phaseStart))
		}

		return nil
	}

	if err := validateWorkloadSubnetCIDRs(p.Config, "pve vault provider: subnets", pveWorkloadSubnetCIDRs(subnets, p.BlocName)); err != nil {
		return err
	}

	subnetsPath := p.PathBuilder.GetSubnetsPath(envType)

	for _, sub := range subnets {
		if !strings.HasPrefix(sub.Name, p.BlocName+"-") {
			continue
		}

		cidr, _ := sub.Properties["cidr"].(string)
		az, _ := sub.Properties["availability_zone"].(string)

		if cidr == "" {
			continue
		}

		// Each PVE subnet's gateway is its OWN first host (per-/22), NOT the
		// parent /18 SDN gateway. BOSH requires a subnet's gateway to be inside
		// its range, and the PVE SDN provisions a real gateway per /22 subnet
		// (infra .64.1, ocfp-0 .68.1, ocfp-1 .72.1, ocfp-2 .76.1). Bootstrap
		// state records the parent /18 gateway (network.go addVirtualSubnetToState),
		// so we recompute from the subnet's own CIDR here — mirroring the Stackit
		// provider (parseSubnetCIDR), which likewise derives the subnet-local
		// gateway rather than the network gateway. The cloud-config builder reads
		// this per-subnet gateway/dns directly.
		gateway := pveCIDRGateway(cidr)
		if gateway == "" {
			// Defensive: malformed cidr (already guarded above) — fall back to
			// whatever bootstrap state recorded.
			gateway, _ = sub.Properties["gateway"].(string)
		}

		dns := pveFirstNonEmpty(pveFirstDNS(p.Config.DNS), gateway, "1.1.1.1")

		// Genesis consumes the bloc-relative subnet name ("ocfp-0", "infra"),
		// not the bloc-prefixed state name ("<bloc>-ocfp-0"). The bosh kit and
		// cf kit reference subnets by these short names (e.g. compilation pins
		// 'ocfp-2'). Strip the bloc prefix so the vault path matches.
		genesisName := strings.TrimPrefix(sub.Name, p.BlocName+"-")

		// network_id is the SDN/bridge identifier the bosh kit reads as the
		// subnet `id`/cloud-property. Fall back to the configured bridge name.
		bridgeID, _ := sub.Properties["network_id"].(string)
		bridgeID = pveFirstNonEmpty(bridgeID, p.Config.Network.Name, "vmbr0")

		subnetPath := filepath.Join(subnetsPath, genesisName)
		if err := p.Safe.SetMultiple(subnetPath, map[string]any{
			"cidr":       cidr,
			"cidr_block": cidr,
			"az":         az,
			"gateway":    gateway,
			"dns":        dns,
			"id":         bridgeID,
		}); err != nil {
			return fmt.Errorf("failed to write subnet %s: %w", genesisName, err)
		}

		if subnetNum, ok := pveWorkloadSubnetIndex(genesisName); ok {
			// Workload (ocfp-*) subnets: reserved-ips computed entirely from
			// this subnet's own CIDR plus the per-tier assignment table
			// (internal/vault/pve_reserved_ips.go), NOT from bootstrap
			// state. mgmt and ocf therefore get DISJOINT reserved-ips on a
			// shared subnet — bootstrap's tier-blind reserved_<name>_* state
			// outputs are no longer consulted here (see
			// plans/pve-tiered-reserved-ip-map.md).
			err := p.writeTieredReservedIPs(cidr, envType, subnetNum, genesisName, subnetPath)
			if err != nil {
				return err
			}
		} else {
			// Infra subnet: unchanged behavior. Role-keyed reserved IPs
			// (reserved_{name}_{role}_ip) for kits that resolve IPs by role
			// name, sourced from bootstrap state as before — the infra
			// subnet's bastion/director/shield/blacksmith placement is out
			// of scope for the tiered-map change (plan: "Infra subnet keeps
			// its existing layout/behavior").
			roleKeys, err := p.writeReservedIPs(sm, subnetPath, sub.Name)
			if err != nil {
				return err
			}

			if len(roleKeys) > 0 {
				reservedPath := filepath.Join(subnetPath, "reserved-ips")

				err := p.Safe.SetMultiple(reservedPath, roleKeys)
				if err != nil {
					return fmt.Errorf("failed to write reserved-ip keys for %s: %w", genesisName, err)
				}
			}
		}
	}

	if reporter != nil {
		reporter.ReportPhaseComplete(phaseName, time.Since(phaseStart))
	}

	return nil
}

// ConfigureReservedIPs writes ONLY the reserved-ips vault paths for both
// mgmt and ocf -- never the subnet skeleton (cidr/az/gateway/dns/id), the
// subnets marker, or any other part of the configuration tree. Reached
// PVE-only, through a *PVEVaultProvider type assertion at the call sites
// (populate's reserved-ips phase, and the reserved-ips migrate/status
// commands) rather than the shared VaultProvider interface, because the
// tiered/role-keyed writers it drives (writeTieredReservedIPs,
// writeReservedIPs, writeFallbackReservedIPs) exist only on this provider.
func (p *PVEVaultProvider) ConfigureReservedIPs(reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
	phaseName := PhaseReservedIPs
	phaseStart := time.Now()

	if reporter != nil {
		reporter.ReportPhaseStart(phaseName, phaseNum, totalPhases)
	}

	p.logger.Infow("Configuring reserved IPs", "bloc", p.BlocName)

	for _, envType := range []string{"mgmt", "ocf"} {
		if err := p.configureReservedIPsForEnv(envType); err != nil {
			return err
		}
	}

	if reporter != nil {
		reporter.ReportPhaseComplete(phaseName, time.Since(phaseStart))
	}

	return nil
}

// configureReservedIPsForEnv writes the reserved-ips paths for one
// environment (mgmt or ocf). Mirrors ConfigureSubnets' per-subnet dispatch
// (state-driven tiered/role-keyed writers, or the stateless fallback) but
// omits every write ConfigureSubnets makes outside reserved-ips.
func (p *PVEVaultProvider) configureReservedIPsForEnv(envType string) error {
	sm := p.loadStateManager()
	if sm == nil {
		return p.writeFallbackReservedIPs(envType)
	}

	subnets, err := sm.GetResourcesByType("subnet")
	if err != nil || len(subnets) == 0 {
		return p.writeFallbackReservedIPs(envType)
	}

	if err := validateWorkloadSubnetCIDRs(p.Config, "pve vault provider: reserved-ips", pveWorkloadSubnetCIDRs(subnets, p.BlocName)); err != nil {
		return err
	}

	subnetsPath := p.PathBuilder.GetSubnetsPath(envType)

	for _, sub := range subnets {
		if !strings.HasPrefix(sub.Name, p.BlocName+"-") {
			continue
		}

		cidr, _ := sub.Properties["cidr"].(string)
		if cidr == "" {
			continue
		}

		genesisName := strings.TrimPrefix(sub.Name, p.BlocName+"-")
		subnetPath := filepath.Join(subnetsPath, genesisName)

		if subnetNum, ok := pveWorkloadSubnetIndex(genesisName); ok {
			if err := p.writeTieredReservedIPs(cidr, envType, subnetNum, genesisName, subnetPath); err != nil {
				return err
			}

			continue
		}

		roleKeys, err := p.writeReservedIPs(sm, subnetPath, sub.Name)
		if err != nil {
			return err
		}

		if len(roleKeys) == 0 {
			continue
		}

		reservedPath := filepath.Join(subnetPath, "reserved-ips")

		if err := p.Safe.SetMultiple(reservedPath, roleKeys); err != nil {
			return fmt.Errorf("failed to write reserved-ip keys for %s: %w", genesisName, err)
		}
	}

	return nil
}

// pveWorkloadSubnetPrefix is the genesis-name prefix identifying a PVE
// workload subnet ("ocfp-0", "ocfp-1", "ocfp-2"), as opposed to the infra
// subnet (bare "infra").
const pveWorkloadSubnetPrefix = "ocfp-"

// pveWorkloadSubnetIndex extracts the AZ index from a workload subnet's
// genesis name ("ocfp-2" -> 2, true). Returns (0, false) for names that
// don't match the "ocfp-N" pattern (e.g. "infra"), so callers can branch on
// the infra-vs-workload distinction with one call.
func pveWorkloadSubnetIndex(genesisName string) (int, bool) {
	if !strings.HasPrefix(genesisName, pveWorkloadSubnetPrefix) {
		return 0, false
	}

	idx, err := strconv.Atoi(strings.TrimPrefix(genesisName, pveWorkloadSubnetPrefix))
	if err != nil || idx < 0 {
		return 0, false
	}

	return idx, true
}

// pveWorkloadSubnetCIDRs extracts the ocfp/workload subnet CIDRs from a
// bloc's bootstrap-state subnet resources, for validateWorkloadSubnetCIDRs.
// Mirrors the same bloc-prefix/genesis-name/workload-index filtering
// ConfigureSubnets and configureReservedIPsForEnv apply in their own
// per-subnet loops, so the pre-loop enforcement check and the loop it
// guards can never disagree about which resources count as workload
// subnets.
func pveWorkloadSubnetCIDRs(subnets []*state.Resource, blocName string) []string {
	cidrs := make([]string, 0, len(subnets))

	for _, sub := range subnets {
		if !strings.HasPrefix(sub.Name, blocName+"-") {
			continue
		}

		genesisName := strings.TrimPrefix(sub.Name, blocName+"-")
		if _, ok := pveWorkloadSubnetIndex(genesisName); !ok {
			continue
		}

		cidr, _ := sub.Properties["cidr"].(string)
		if cidr == "" {
			continue
		}

		cidrs = append(cidrs, cidr)
	}

	return cidrs
}

// writeTieredReservedIPs writes the genesis-consumed reserved-ips block for a
// workload (ocfp-*) subnet, computed entirely from the subnet's own CIDR plus
// the per-tier assignment table (reservedIPsForSubnet) — the STACKIT
// architecture ported to PVE. mgmt and ocf calls against the SAME cidr always
// produce disjoint keys/values (see plans/pve-tiered-reserved-ip-map.md), so
// bootstrap's tier-blind reserved_<name>_* state outputs are never consulted
// for this subnet class. director_ip/ip are written as compatibility
// aliases for bosh_ip: PVE historically exposed all three, and a kit hook
// this change cannot see may still read either alias.
func (p *PVEVaultProvider) writeTieredReservedIPs(cidr, envType string, subnetNum int, genesisName, subnetPath string) error {
	reserved, err := reservedIPsForSubnet(cidr, envType, subnetNum, p.Config, p.logger)
	if err != nil {
		return fmt.Errorf("failed to compute reserved IPs for %s/%s-%d: %w", envType, genesisName, subnetNum, err)
	}

	if boshIP, ok := reserved["bosh_ip"]; ok {
		reserved["ip"] = boshIP
		reserved["director_ip"] = boshIP
	}

	if len(reserved) == 0 {
		return nil
	}

	reservedPath := filepath.Join(subnetPath, "reserved-ips")

	err = p.Safe.SetMultiple(reservedPath, reserved)
	if err != nil {
		return fmt.Errorf("failed to write reserved-ips band for %s: %w", genesisName, err)
	}

	return nil
}

// writeFallbackSubnet preserves the legacy single-blob write so vault populate
// keeps a positive marker at the subnets path when bootstrap state is absent.
//
// Also writes the canonical `subnets/ocfp-0` entry the bosh kit expects
// (cidr_block, gateway, az, id) plus reserved-ips/bosh_ip.  These are the
// keys read by kits/bosh/ocfp/meta.yml via
// `vault meta.ocfp.vault.config "/net/subnets/ocfp-0:..."` —
// without them the manifest hook fails before it can apply env yml overrides.
func (p *PVEVaultProvider) writeFallbackSubnet(envType string) error {
	subnetsPath := p.PathBuilder.GetSubnetsPath(envType)

	marker := map[string]any{
		"note": "Proxmox SDN: single vnet subnet; no bootstrap state found",
	}
	// PVE has no VPC concept, so VPCCIDRBlock is often empty. Only record a
	// parent cidr when one is actually known (prefer the configured subnet
	// CIDR) -- writing an empty cidr leaves an un-entombable placeholder.
	if c := pveFirstNonEmpty(p.Config.Network.CIDR, p.Config.VPCCIDRBlock); c != "" {
		marker["cidr"] = c
	}

	err := p.Safe.SetMultiple(subnetsPath, marker)
	if err != nil {
		return fmt.Errorf("failed to set subnet configuration: %w", err)
	}

	cidr := p.pveFallbackCIDR()

	gateway := pveCIDRGateway(cidr)
	dns := pveFirstNonEmpty(pveFirstDNS(p.Config.DNS), gateway, "1.1.1.1")
	bridgeID := pveFirstNonEmpty(p.Config.Network.Name, "vmbr0")
	az := pveFirstAZ(p.Config)

	// PVE single-network mode: populate ocfp-0..ocfp-2 with identical data so
	// the bosh kit's cloud-config-director hook (which hardcodes specific
	// subnet refs like 'ocfp-2' for the compilation network) resolves without
	// special-casing.  All three logical subnets share the same underlying
	// lvnet bridge — this is conventional for single-vnet PVE deployments.
	for i := range 3 {
		subnetPath := p.PathBuilder.GetSubnetPath(envType, "ocfp", i)

		err := p.Safe.SetMultiple(subnetPath, map[string]any{
			"cidr":       cidr,
			"cidr_block": cidr,
			"gateway":    gateway,
			"dns":        dns,
			"az":         az,
			"id":         bridgeID,
		})
		if err != nil {
			return fmt.Errorf("failed to set ocfp-%d subnet entry: %w", i, err)
		}

		// Named-role statics (bosh_ip, vault_ip, ...) and the tier's default
		// available band, computed from the shared fallback cidr plus the
		// per-tier assignment table — the same engine the state-driven path
		// uses (reservedIPsForSubnet), so mgmt/ocf disjointness holds even
		// in this degraded, no-bootstrap-state mode.
		reserved, err := p.finalizeFallbackReserved(cidr, envType, i)
		if err != nil {
			return err
		}

		reservedPath := p.PathBuilder.GetReservedIPsPath(envType, "ocfp", i)

		err = p.Safe.SetMultiple(reservedPath, reserved)
		if err != nil {
			return fmt.Errorf("failed to set ocfp-%d reserved-ips: %w", i, err)
		}
	}

	return nil
}

// pveFallbackCIDR resolves the CIDR the stateless-fallback subnet writers
// use: Network.CIDR when set, else the legacy VPCCIDRBlock knob.
func (p *PVEVaultProvider) pveFallbackCIDR() string {
	if p.Config.Network.CIDR != "" {
		return p.Config.Network.CIDR
	}

	return p.Config.VPCCIDRBlock
}

// finalizeFallbackReserved computes one stateless-fallback subnet's (index
// i, 0/1/2) full reserved-ips block: the tier's derived assignments, the
// director-IP compatibility aliases, and this subnet's disjoint slice of
// the available band.
//
// On PVE the three logical subnets share a single flat range in this
// stateless-fallback mode, so the tier's shared available band would let
// net-compilation (pinned to ocfp-2) and net-ocf (spanning ocfp-0/1) both
// allocate from the same IPs and collide. Carving a DISJOINT contiguous
// slice per subnet index keeps compilation clear of the workload band even
// without bootstrap state. Network.AvailableIPStart/End (absolute,
// config-driven) overrides the tier default here, applied identically
// regardless of envType — unlike network.bands.mgmt (see
// pve_reserved_ips.go), this knob predates the tiered layout and stays
// tier-blind by design.
func (p *PVEVaultProvider) finalizeFallbackReserved(cidr, envType string, i int) (map[string]any, error) {
	reserved, err := reservedIPsForSubnet(cidr, envType, i, p.Config, p.logger)
	if err != nil {
		return nil, fmt.Errorf("failed to compute fallback reserved IPs for ocfp-%d: %w", i, err)
	}

	if boshIP, ok := reserved["bosh_ip"]; ok {
		reserved["ip"] = boshIP
		reserved["director_ip"] = boshIP
	}

	defaultStart, _ := reserved["available_0"].(string)
	defaultEnd, _ := reserved["available_1"].(string)

	if start, end := pveFallbackSubnetBand(p.Config, i, defaultStart, defaultEnd); start != "" && end != "" {
		reserved["available_0"] = start
		reserved["available_1"] = end
	}

	return reserved, nil
}

// writeFallbackReservedIPs writes only the reserved-ips portion of the
// stateless-fallback subnet layout (see writeFallbackSubnet), for callers
// that must not also write the subnet skeleton (cidr/gateway/dns/az/id) or
// the subnets marker.
func (p *PVEVaultProvider) writeFallbackReservedIPs(envType string) error {
	cidr := p.pveFallbackCIDR()

	for i := range 3 {
		reserved, err := p.finalizeFallbackReserved(cidr, envType, i)
		if err != nil {
			return err
		}

		reservedPath := p.PathBuilder.GetReservedIPsPath(envType, "ocfp", i)

		if err := p.Safe.SetMultiple(reservedPath, reserved); err != nil {
			return fmt.Errorf("failed to set ocfp-%d reserved-ips: %w", i, err)
		}
	}

	return nil
}

// pveFallbackSubnetBand returns the disjoint available [start,end] band for the
// stateless-fallback subnet at index i (0,1,2). Network.AvailableIPStart/End
// (absolute, config-driven, tier-blind by design — see writeFallbackSubnet)
// wins when both are set; otherwise defaultStart/defaultEnd (the calling
// tier's map-derived available band, from reservedIPsForSubnet) is used.
// Either way the resolved band is split into three contiguous, non-
// overlapping slices so each logical subnet on the shared flat range owns a
// distinct slice. This keeps net-compilation (ocfp-2) clear of net-ocf
// (ocfp-0/1) when no bootstrap state is present.
//
// Slicing uses full 32-bit IP arithmetic (see vaultIPToUint32/vaultUint32ToIP),
// so a band spanning an octet boundary (e.g. .64.240 to .65.30) still slices
// into three correct, disjoint, non-overlapping parts rather than silently
// collapsing to a shared band. Only a genuinely unsliceable band (parse
// failure, or an inverted/empty range where end <= start) falls back to
// returning the whole band to every subnet, per the documented graceful-
// degradation contract. Returns empty strings when no band can be resolved at
// all (both the override and the caller-supplied default are unset).
func pveFallbackSubnetBand(cfg *config.Config, i int, defaultStart, defaultEnd string) (string, string) {
	const subnetCount = 3

	// i is always 0/1/2 from writeFallbackSubnet's `for i := range 3`, but
	// guard explicitly since this is a package-level function: a negative or
	// out-of-range i must not reach the uint32(i) conversion below.
	if i < 0 || i >= subnetCount {
		return "", ""
	}

	start, end := cfg.Network.AvailableIPStart, cfg.Network.AvailableIPEnd
	if start == "" || end == "" {
		start, end = defaultStart, defaultEnd
	}

	if start == "" || end == "" {
		return "", ""
	}

	startVal, startOK := vaultIPToUint32(start)
	endVal, endOK := vaultIPToUint32(end)

	if !startOK || !endOK || endVal <= startVal {
		// Cannot reason about the band (parse failure, or an inverted/empty
		// range): fall back to the shared band so we still write something usable.
		return start, end
	}

	span := endVal - startVal + 1

	slice := span / subnetCount
	if slice < 1 {
		// Band too small to split; keep it shared rather than emit an empty slice.
		return start, end
	}

	// Safe conversion: i was validated above to be in [0, subnetCount), well
	// within uint32 range.
	sliceStart := startVal + uint32(i)*slice

	sliceEnd := sliceStart + slice - 1
	if i == subnetCount-1 {
		// Last slice absorbs any remainder so the full band is covered.
		sliceEnd = endVal
	}

	return vaultUint32ToIP(sliceStart), vaultUint32ToIP(sliceEnd)
}

// vaultIPToUint32 parses an IPv4 dotted-quad string into its big-endian
// uint32 representation, returning ok=false for anything that isn't a valid
// IPv4 address. This is a small local copy of internal/bootstrap's
// ipToUint32: internal/bootstrap already imports internal/vault (for
// artifacts/state helpers), so importing bootstrap here would create an
// import cycle.
func vaultIPToUint32(ip string) (uint32, bool) {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return 0, false
	}

	v4 := parsed.To4()
	if v4 == nil {
		return 0, false
	}

	const (
		octetShift24 = 24
		octetShift16 = 16
		octetShift8  = 8
	)

	return uint32(v4[0])<<octetShift24 | uint32(v4[1])<<octetShift16 | uint32(v4[2])<<octetShift8 | uint32(v4[3]), true
}

// vaultUint32ToIP is the inverse of vaultIPToUint32: it renders a big-endian
// uint32 as a dotted-quad IPv4 string.
func vaultUint32ToIP(n uint32) string {
	const (
		octetShift24 = 24
		octetShift16 = 16
		octetShift8  = 8
		octetMask    = 0xFF
	)

	return net.IPv4(byte(n>>octetShift24&octetMask), byte(n>>octetShift16&octetMask), byte(n>>octetShift8&octetMask), byte(n&octetMask)).String()
}

// pveFirstDNS returns the first DNS entry from a slice, or "" if the slice is empty.
func pveFirstDNS(dns []string) string {
	if len(dns) == 0 {
		return ""
	}

	return dns[0]
}

// pveCIDRGateway returns the conventional gateway IP for a CIDR
// ("10.64.64.0/18" -> "10.64.64.1").  Returns "" on parse failure.
func pveCIDRGateway(cidr string) string {
	for i := 0; i < len(cidr); i++ {
		if cidr[i] == '/' {
			cidr = cidr[:i]

			break
		}
	}
	// Replace last octet with .1.
	last := -1

	for i := len(cidr) - 1; i >= 0; i-- {
		if cidr[i] == '.' {
			last = i

			break
		}
	}

	if last <= 0 {
		return ""
	}

	return cidr[:last+1] + "1"
}

// pveFirstAZ returns the first Proxmox node name as the AZ identifier, or "z1"
// when no nodes are configured.  Mirrors the kit's `meta.ocfp.bosh.az` default.
func pveFirstAZ(cfg *config.Config) string {
	if len(cfg.Nodes) > 0 {
		return cfg.Nodes[0]
	}

	if cfg.Region != "" {
		return cfg.Region
	}

	return "z1"
}

// writeReservedIPs iterates bootstrap state outputs looking for keys of the
// form `reserved_{subnetName}_{role}_ip` and writes each non-empty value to
// `{subnetPath}/reserved-ips/{role}` so genesis kits can resolve reserved IPs
// by role name. State outputs are the canonical source — they are populated
// by bootstrap.compute when subnets are reserved.
// writeReservedIPs writes each `reserved_{subnet}_{role}_ip` state output to its
// per-role sub-path (reserved-ips/{role}:ip) for role-lookup consumers, and
// returns the `{role}_ip` KEY map for the caller to merge into the reserved-ips
// secret — the key form (reserved-ips:doomsday_ip, reserved-ips:vault_ip, ...)
// is what the genesis kits actually read. Returns an empty map when state has no
// matching outputs.
func (p *PVEVaultProvider) writeReservedIPs(sm *state.Manager, subnetPath, subnetName string) (map[string]any, error) {
	roleKeys := map[string]any{}

	current := sm.Current()
	if current == nil || len(current.Outputs) == 0 {
		return roleKeys, nil
	}

	prefix := "reserved_" + subnetName + "_"
	suffix := "_ip"

	for key, val := range current.Outputs {
		if !strings.HasPrefix(key, prefix) || !strings.HasSuffix(key, suffix) {
			continue
		}

		role := strings.TrimSuffix(strings.TrimPrefix(key, prefix), suffix)
		if role == "" {
			continue
		}

		ip, ok := val.(string)
		if !ok || ip == "" {
			continue
		}

		roleKeys[role+"_ip"] = ip

		rolePath := filepath.Join(subnetPath, "reserved-ips", role)

		err := p.Safe.SetMultiple(rolePath, map[string]any{
			"ip": ip,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to write reserved IP for %s/%s: %w", subnetName, role, err)
		}
	}

	return roleKeys, nil
}

// loadStateManager loads the bloc's bootstrap state, returning nil on any
// failure so callers can fall through to stateless behavior.
func (p *PVEVaultProvider) loadStateManager() *state.Manager {
	stateDir, err := state.GetStateDir(p.BlocName)
	if err != nil {
		return nil
	}

	sm, err := state.NewManager(stateDir)
	if err != nil {
		return nil
	}

	if _, err := sm.Load(p.BlocName); err != nil {
		return nil
	}

	return sm
}

// ConfigureSecurityGroups configures security group settings.
func (p *PVEVaultProvider) ConfigureSecurityGroups(_envPath, envType string, reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
	phaseName := "security-groups-" + envType
	phaseStart := time.Now()

	if reporter != nil {
		reporter.ReportPhaseStart(phaseName, phaseNum, totalPhases)
	}

	p.logger.Infow("Configuring security groups", "env_type", envType)

	// Proxmox uses firewall groups at cluster level
	sgPath := p.PathBuilder.GetSecurityGroupsPath(envType)

	sgConfig := map[string]any{
		"type":        "firewall_group",
		"description": fmt.Sprintf("Security group for %s environment", envType),
	}

	err := p.Safe.SetMultiple(sgPath, sgConfig)
	if err != nil {
		return fmt.Errorf("failed to set security group configuration: %w", err)
	}

	if reporter != nil {
		reporter.ReportPhaseComplete(phaseName, time.Since(phaseStart))
	}

	return nil
}

// ConfigureBlobstores writes blobstore configuration to vault.
//
// Two modes are honoured:
//
//   - local (default): writes only `mode: local` and `status: configured` to
//     cf/blobstores/main. No endpoint, region, or credentials are written.
//
//   - external: writes mode, endpoint, region, and path_style to
//     cf/blobstores/main. Credentials go to cf/blobstores/main/creds so the
//     config path stays free of secrets. Empty endpoint in external mode
//     surfaces an error rather than writing a half-configured entry.
//
// Path mirrors AWS cf/blobstores/main naming for kit compatibility.
func (p *PVEVaultProvider) ConfigureBlobstores(_envPath, envType string, reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
	phaseName := "blobstores-" + envType
	phaseStart := time.Now()

	if reporter != nil {
		reporter.ReportPhaseStart(phaseName, phaseNum, totalPhases)
	}

	mode := p.resolveBlobstoreMode()

	// src is non-nil only on the auto-sourced-from-state path (below); it
	// gates the writeArtifactsMeta call after configureExternalBlobstore
	// succeeds. Flag-driven external mode (operator passed --blobstore-endpoint
	// directly) has no backing artifacts state to source metadata from, so it
	// intentionally does not write secret/ocfp/{bloc}/artifacts.
	var src *ArtifactsBlobstoreSource

	// When the operator supplied no explicit blobstore flags, try to auto-source
	// the endpoint + credentials + CA from bootstrap state's artifacts resource.
	// This is the common case on a clean run: `ocfp vault populate` (on the
	// bastion) promotes to external mode from state with no flags required.
	if mode == "local" && p.BlobstoreEndpoint == "" {
		var err error

		src, err = ConfigureBlobstoresFromArtifactsState(p.loadStateManager(), p.BlocName)
		if err != nil {
			p.logger.Warnw("Could not load artifacts state for blobstore populate", "error", err)
		}

		if src != nil {
			p.BlobstoreEndpoint = src.Endpoint
			p.BlobstoreRegion = pveFirstNonEmpty(p.BlobstoreRegion, "us-east-1")
			p.BlobstoreAccessKey = src.AccessKey
			p.BlobstoreSecretKey = src.SecretKey
			p.blobstoreCACert = src.CACert
			p.blobstoreTLSMode = src.TLSMode
			mode = "external"

			// Endpoint URL only — credentials and CA are never logged.
			p.logger.Infow("Auto-sourced blobstore config from artifacts state",
				"env_type", envType, "endpoint", p.BlobstoreEndpoint)
		}
	}

	blobstorePath := p.PathBuilder.GetSystemBlobstorePath(envType, "cf", "main")

	switch mode {
	case "external":
		if err := p.configureExternalBlobstore(envType, blobstorePath); err != nil {
			return err
		}

		if src != nil {
			// ocfp vault populate runs ON the bastion against the bastion's own
			// inception vault, which otherwise never receives this path —
			// WriteArtifacts (the other writer of secret/ocfp/{bloc}/artifacts)
			// only runs on the workstation during bootstrap/provision. Without
			// this, scripts/blobstores' `safe get
			// secret/ocfp/<bloc>/artifacts:endpoint` finds nothing on the
			// bastion and the script exits silently.
			p.writeArtifactsMeta(src)
		}
	default:
		p.logger.Infow("Configuring local-mode blobstore (no external endpoint)", "env_type", envType)

		err := p.Safe.SetMultiple(blobstorePath, map[string]any{
			"mode":   "local",
			"status": "configured",
		})
		if err != nil {
			return fmt.Errorf("failed to set blobstore (local mode) configuration: %w", err)
		}
	}

	// In local mode, seed bosh-director blobstore meta so the manifest hook can
	// resolve `:name` / `:region` placeholders (kits/bosh/ocfp/meta.yml).  These
	// are non-functional defaults; a working blobstore still requires either
	// external mode here or an env-yml override.  External mode already writes
	// this path with real endpoint+creds via configureBOSHBlobstoreForScope.
	if envType == "mgmt" && mode != "external" {
		boshBlobPath := p.PathBuilder.GetSystemBlobstorePath(envType, "bosh", "bosh")
		region := pveFirstNonEmpty(p.BlobstoreRegion, "us-east-1")

		bucket := pveFirstNonEmpty(p.BlocName+"-bosh", "bosh-blobs")

		err := p.Safe.SetMultiple(boshBlobPath, map[string]any{
			"name":   bucket,
			"region": region,
			"mode":   mode,
		})
		if err != nil {
			return fmt.Errorf("failed to set bosh blobstore meta: %w", err)
		}
	}

	if reporter != nil {
		reporter.ReportPhaseComplete(phaseName, time.Since(phaseStart))
	}

	return nil
}

// resolveBlobstoreMode picks the active mode, preferring an explicit operator
// choice and falling back to `external` whenever an endpoint was supplied
// (backwards compatible with the old --blobstore-endpoint-only contract).
func (p *PVEVaultProvider) resolveBlobstoreMode() string {
	if p.BlobstoreMode != "" {
		return p.BlobstoreMode
	}

	if p.BlobstoreEndpoint != "" {
		return "external"
	}

	return "local"
}

// configureExternalBlobstore writes the external-mode config + credentials.
//
// For mgmt env, it also writes the BOSH director blobstore entry at
// {bloc}/mgmt/bosh/blobstores/bosh so genesis BOSH kit can consume the same
// S3-compatible endpoint as the CF blobstore. Previously only the CF path
// was written, leaving the BOSH director blobstore unconfigured for PVE.
func (p *PVEVaultProvider) configureExternalBlobstore(envType, blobstorePath string) error {
	if p.BlobstoreEndpoint == "" {
		return errors.New("pve blobstore external mode requires --blobstore-endpoint")
	}

	// Resolve (and, for internal-ca, recover from vault) the CA cert BEFORE
	// writing any blobstore config. This must happen before the CF write
	// below, not lazily inside bucket creation: bucket creation for the CF
	// path runs after the CF config's ca_cert field is already written, so a
	// recovery that happened there instead would repair p.blobstoreCACert too
	// late for the CF entry — the CF blobstore would get no ca_cert while the
	// BOSH entry, written afterward, would get the recovered one.
	if err := p.resolveBlobstoreTLSTrust(); err != nil {
		return err
	}

	region := p.BlobstoreRegion
	if region == "" {
		region = "us-east-1"
	}

	// host is the endpoint's bare host (the artifacts VM private_ip on PVE);
	// the bosh director blobstore job template reads `host` rather than the
	// full endpoint URL.
	host := pveHostnameOnly(p.BlobstoreEndpoint)

	// Bucket name follows the <bloc>-<scope>-cf convention shared with
	// ArtifactsWriter and bootstrap.artifactsBucketList.
	bucketName := fmt.Sprintf("%s-%s-cf", p.BlocName, envType)

	p.logger.Infow("Configuring external blobstore", "env_type", envType, "endpoint", p.BlobstoreEndpoint, "region", region)

	blobstoreConfig := map[string]any{
		"mode":       "external",
		"endpoint":   p.BlobstoreEndpoint,
		"host":       host,
		"region":     region,
		"path_style": true,
		"bucket":     bucketName,
		"name":       bucketName,
		"status":     "configured",
	}
	if p.blobstoreCACert != "" {
		blobstoreConfig["ca_cert"] = p.blobstoreCACert
	}

	err := p.Safe.SetMultiple(blobstorePath, blobstoreConfig)
	if err != nil {
		return fmt.Errorf("failed to set blobstore configuration: %w", err)
	}

	if p.BlobstoreAccessKey != "" || p.BlobstoreSecretKey != "" {
		credsPath := blobstorePath + "/creds"

		credsConfig := map[string]any{
			"access_key": p.BlobstoreAccessKey,
			"secret_key": p.BlobstoreSecretKey,
		}

		err = p.Safe.SetMultiple(credsPath, credsConfig)
		if err != nil {
			return fmt.Errorf("failed to set blobstore credentials: %w", err)
		}
	}

	// Create the CF blobstore bucket so the secret just written has a backing
	// bucket (atomic secret+bucket; see ensureBlobstoreBucket).
	if err := p.ensureBlobstoreBucket(bucketName); err != nil {
		return err
	}

	// The BOSH director blobstore is scoped per env: mgmt-BOSH consumes the
	// mgmt-scoped path, env-BOSH (deployed for ocf) consumes the ocf-scoped
	// path. Write the scope matching this env so both directors get a working
	// S3-compatible blobstore (RustFS/MinIO/Ceph) — previously only mgmt was.
	if err := p.configureBOSHBlobstoreForScope(envType, region); err != nil {
		return err
	}

	return nil
}

// writeArtifactsMeta re-syncs the operator/status metadata at
// secret/ocfp/{bloc}/artifacts (see artifactsMetaPath, and
// vault.ArtifactsWriter.WriteArtifacts — the workstation-side writer of the
// same path during bootstrap/provision) from state auto-sourced blobstore
// config. `ocfp vault populate` runs ON the bastion against the bastion's own
// inception vault, a vault instance WriteArtifacts never writes to — without
// this, that path stays empty there and scripts/blobstores' `safe get
// secret/ocfp/<bloc>/artifacts:endpoint` finds nothing, so the script exits
// silently on the bastion even though the blobstore itself is fully
// configured.
//
// Only called from the auto-sourced-from-state path (src != nil in
// ConfigureBlobstores); flag-driven external mode has no backing artifacts
// state to source this metadata from, so it is skipped there rather than
// written with guessed/partial values.
//
// Deliberately does NOT touch secret/ocfp/{bloc}/ca: artifacts state only
// ever carries the leaf/CA *certificate* (never the private key — see
// resolveArtifactsProvisionTLS), and a cert-only entry at the ca path risks
// being mistaken by LoadOrGenerateBlocCA's found-but-incomplete handling for
// a corrupted CA. Bastion trust in the CA instead comes from the
// bloc_ca_trust init phase installing it into the OS trust store
// (scripts/blobstores' tier-2 CA resolution).
//
// Warn-only on failure: this is status metadata, not required for the
// blobstore config itself to function, so a write failure here must not fail
// `ocfp vault populate`.
func (p *PVEVaultProvider) writeArtifactsMeta(src *ArtifactsBlobstoreSource) {
	meta := map[string]any{
		"endpoint": src.Endpoint,
		"host":     src.Host,
		"port":     src.Port,
		"tls_mode": src.TLSMode,
	}

	if src.FingerprintSHA256 != "" {
		meta["tls_fingerprint_sha256"] = src.FingerprintSHA256
	}

	if src.LeafNotAfter != "" {
		meta["tls_leaf_not_after"] = src.LeafNotAfter
	}

	if err := p.Safe.SetMultiple(artifactsMetaPath(p.BlocName), meta); err != nil {
		p.logger.Warnw("Could not write artifacts operational metadata; bastion-side tooling (scripts/blobstores) may not find the endpoint",
			"bloc", p.BlocName, "error", err)
	}
}

// resolveBlobstoreTLSTrust resolves the CA cert / skip-verify decision for
// the external blobstore endpoint exactly once per provider instance (see
// blobstoreTLSResolved), mutating p.blobstoreCACert in place when a CA is
// recovered from vault. Every reader of p.blobstoreCACert — the CF config
// write in configureExternalBlobstore, the BOSH config write in
// configureBOSHBlobstoreForScope, and the bucket-creation Endpoint built by
// blobstoreS3Target — must run after this has been called at least once, so
// they all observe the same (possibly-recovered) CA. configureExternalBlobstore
// calls it first, before any vault write, which is what makes that guarantee
// hold: without that early call, a CA recovered lazily during bucket creation
// would arrive too late for whichever config entry gets written first.
//
// Only https endpoints need TLS trust material at all; http is left
// unresolved (no CA recovery attempted, no skip-verify decision made) since
// EndpointForLookup treats http as needing no TLS material regardless of
// mode.
//
// Returns an error only for the fatal case: tls.mode=internal-ca with no CA
// cert recoverable from state or vault — run `ocfp artifacts ca` / `ocfp
// artifacts provision`. Every other mode (self-signed, disabled-but-https, or
// unknown/legacy state with no tls_mode recorded) with a missing CA cert
// falls back to blobstoreAllowInsecure=true (skip-verify) with a loud warning
// log — this provider has no operator-facing --insecure flag to gate an
// explicit opt-in on, so the log line is the acknowledgment. It is never
// silently treated as internal-ca-safe.
func (p *PVEVaultProvider) resolveBlobstoreTLSTrust() error {
	if p.blobstoreTLSResolved {
		return nil
	}

	if p.blobstoreCACert == "" && strings.HasPrefix(p.BlobstoreEndpoint, "https://") {
		switch p.blobstoreTLSMode {
		case config.ArtifactsTLSModeInternalCA:
			recovered, err := LoadBlocCA(p.Safe, p.BlocName)
			if err != nil {
				return fmt.Errorf(
					"blobstore %s: tls.mode=internal-ca but no CA cert in state or vault: %w; run `ocfp artifacts ca --bloc %s` to inspect, or `ocfp artifacts provision --bloc %s` to recover/re-mint it",
					p.BlobstoreEndpoint, err, p.BlocName, p.BlocName)
			}

			p.blobstoreCACert = recovered.CertPEM
		default:
			p.blobstoreAllowInsecure = true

			p.logger.Warnw("blobstore endpoint has no CA cert to pin; skipping TLS verification",
				"endpoint", p.BlobstoreEndpoint, "tls_mode", p.blobstoreTLSMode)
		}
	}

	p.blobstoreTLSResolved = true

	return nil
}

// blobstoreS3Target builds the S3 endpoint + credentials used to create
// blobstore buckets from the provider's resolved external-mode fields.
//
// ok is false when the endpoint or credentials are absent — bucket creation
// cannot authenticate, so callers skip it rather than fail. This is the
// normal "external mode not configured" case, not an error.
//
// When ok is true, err distinguishes two further outcomes: err == nil means
// the endpoint + TLS trust material are ready to use; err != nil means the
// endpoint/credentials ARE present but TLS trust could not be safely
// resolved (tls.mode=internal-ca with no CA cert recoverable from state or
// vault, see resolveBlobstoreTLSTrust) — callers must treat this as fatal and
// never fall back to skipping verification for internal-ca.
func (p *PVEVaultProvider) blobstoreS3Target() (artifacts.Endpoint, artifacts.Credentials, bool, error) {
	if p.BlobstoreEndpoint == "" || p.BlobstoreAccessKey == "" || p.BlobstoreSecretKey == "" {
		return artifacts.Endpoint{}, artifacts.Credentials{}, false, nil
	}

	if err := p.resolveBlobstoreTLSTrust(); err != nil {
		return artifacts.Endpoint{}, artifacts.Credentials{}, true, err
	}

	region := p.BlobstoreRegion
	if region == "" {
		region = "us-east-1"
	}

	effectiveMode := p.blobstoreTLSMode
	if p.blobstoreAllowInsecure {
		effectiveMode = config.ArtifactsTLSModeSelfSigned
	}

	ep, err := artifacts.EndpointForLookup(p.BlocName, p.BlobstoreEndpoint, effectiveMode, p.blobstoreCACert, p.blobstoreAllowInsecure)
	if err != nil {
		return artifacts.Endpoint{}, artifacts.Credentials{}, true, err
	}

	ep.Host = pveHostnameOnly(p.BlobstoreEndpoint)
	ep.Region = region

	creds := artifacts.Credentials{AccessKey: p.BlobstoreAccessKey, SecretKey: p.BlobstoreSecretKey}

	return ep, creds, true, nil
}

// ensureBlobstoreBucket creates the named bucket on the external blobstore so a
// written blobstore secret always has a backing bucket. This closes the gap
// where vault populate wrote a <bloc>-<scope>-bosh (or -cf) secret but never
// created the bucket: RustFS then lets the director's CreateMultipartUpload
// succeed against a missing bucket and fails every UploadPart with NoSuchUpload
// during CF deploy. Idempotent (already-owned buckets are fine); a no-op when
// credentials are absent. A creation failure is fatal — a secret pointing at a
// bucket that does not exist is worse than a loud failure at populate time.
func (p *PVEVaultProvider) ensureBlobstoreBucket(bucketName string) error {
	ep, creds, ok, err := p.blobstoreS3Target()
	if err != nil {
		return fmt.Errorf("ensuring blobstore bucket %q: %w", bucketName, err)
	}

	if !ok {
		return nil
	}

	ensurer := p.bucketEnsurer
	if ensurer == nil {
		ensurer = s3BucketEnsurer{}
	}

	if err := ensurer.EnsureBuckets(context.Background(), ep, creds, []artifacts.BucketSpec{{Name: bucketName}}); err != nil {
		return fmt.Errorf("ensuring blobstore bucket %q exists: %w", bucketName, err)
	}

	p.logger.Infow("Ensured blobstore bucket exists", "bucket", bucketName, "endpoint", p.BlobstoreEndpoint)

	return nil
}

// configureBOSHBlobstoreForScope writes the BOSH director's external blobstore
// config and credentials for the given scope (mgmt or ocf) so the genesis BOSH
// kit can deploy with an S3-compatible blobstore. Bucket names follow the
// <bloc>-<scope>-bosh convention. Path mirrors AWS naming convention.
func (p *PVEVaultProvider) configureBOSHBlobstoreForScope(scope, region string) error {
	boshPath := p.PathBuilder.GetSystemBlobstorePath(scope, "bosh", "bosh")
	bucketName := fmt.Sprintf("%s-%s-bosh", p.BlocName, scope)
	host := pveHostnameOnly(p.BlobstoreEndpoint)

	p.logger.Infow("Configuring external BOSH director blobstore",
		"scope", scope, "endpoint", p.BlobstoreEndpoint, "region", region, "path", boshPath)

	boshConfig := map[string]any{
		"mode":       "external",
		"endpoint":   p.BlobstoreEndpoint,
		"host":       host,
		"region":     region,
		"path_style": true,
		"name":       bucketName,
		"bucket":     bucketName,
		"status":     "configured",
	}
	if p.blobstoreCACert != "" {
		boshConfig["ca_cert"] = p.blobstoreCACert
	}

	err := p.Safe.SetMultiple(boshPath, boshConfig)
	if err != nil {
		return fmt.Errorf("failed to set BOSH blobstore configuration: %w", err)
	}

	if p.BlobstoreAccessKey != "" || p.BlobstoreSecretKey != "" {
		err = p.Safe.SetMultiple(boshPath+"/creds", map[string]any{
			"access_key": p.BlobstoreAccessKey,
			"secret_key": p.BlobstoreSecretKey,
		})
		if err != nil {
			return fmt.Errorf("failed to set BOSH blobstore credentials: %w", err)
		}
	}

	// Create the BOSH director blobstore bucket. This is the bucket whose absence
	// silently broke CF deploy (NoSuchUpload on every compiled-release UploadPart).
	if err := p.ensureBlobstoreBucket(bucketName); err != nil {
		return err
	}

	return nil
}

// ConfigureDatabases configures database settings.
func (p *PVEVaultProvider) ConfigureDatabases(_envPath, envType string, reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
	phaseName := "databases-" + envType
	phaseStart := time.Now()

	if reporter != nil {
		reporter.ReportPhaseStart(phaseName, phaseNum, totalPhases)
	}

	p.logger.Infow("Configuring databases", "env_type", envType)

	// Database configuration (typically VM-based for Proxmox)
	dbPath := p.PathBuilder.GetDatabasesPath(envType)

	dbConfig := map[string]any{
		"type": "vm-based",
		"note": "Databases deployed as VMs on Proxmox",
	}

	err := p.Safe.SetMultiple(dbPath, dbConfig)
	if err != nil {
		return fmt.Errorf("failed to set database configuration: %w", err)
	}

	if reporter != nil {
		reporter.ReportPhaseComplete(phaseName, time.Since(phaseStart))
	}

	return nil
}

// ConfigureLoadBalancers configures load balancer settings.
func (p *PVEVaultProvider) ConfigureLoadBalancers(_envPath, envType string, reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
	phaseName := "load-balancers-" + envType
	phaseStart := time.Now()

	if reporter != nil {
		reporter.ReportPhaseStart(phaseName, phaseNum, totalPhases)
	}

	p.logger.Infow("Configuring load balancers", "env_type", envType)

	// Proxmox doesn't have native load balancers - document external requirement
	lbPath := p.PathBuilder.GetLoadBalancersPath(envType)

	lbConfig := map[string]any{
		"note":     "Proxmox requires external load balancer (HAProxy VM, MetalLB, etc.)",
		"provider": "external",
	}

	err := p.Safe.SetMultiple(lbPath, lbConfig)
	if err != nil {
		return fmt.Errorf("failed to set load balancer configuration: %w", err)
	}

	if reporter != nil {
		reporter.ReportPhaseComplete(phaseName, time.Since(phaseStart))
	}

	return nil
}

// ConfigureFQDNs configures FQDN settings.
func (p *PVEVaultProvider) ConfigureFQDNs(_envPath, envType string, reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
	phaseName := "fqdns-" + envType
	phaseStart := time.Now()

	if reporter != nil {
		reporter.ReportPhaseStart(phaseName, phaseNum, totalPhases)
	}

	p.logger.Infow("Configuring FQDNs", "env_type", envType)

	fqdnCfg := p.Config.FQDNs
	if fqdnCfg == nil {
		if reporter != nil {
			reporter.ReportPhaseComplete(phaseName, time.Since(phaseStart))
		}

		return nil
	}

	// Store the base FQDN at the shared path (mgmt pass only), mirroring the
	// other IaaS providers.
	if fqdnCfg.Base != "" && envType == MgmtEnvType {
		err := p.Safe.Set(p.PathBuilder.GetBaseFQDNPath(), "value", fqdnCfg.Base)
		if err != nil {
			return fmt.Errorf("failed to set base FQDN: %w", err)
		}
	}

	explicit := ExplicitFQDNsForEnv(fqdnCfg, envType)

	base := fqdnCfg.Base
	if base == "" {
		base = p.Config.DomainName
	}

	// Infra-service UIs sit behind the *.system wildcard edge when an ingress
	// provider fronts the bloc (cloudflared tunnel or tailscale); derive them
	// as {svc}.system.{base}.
	systemScoped := config.SystemScoped(p.Config)

	fqdnConfig := PopulateFQDNsForEnv(envType, explicit, base, systemScoped)
	if fqdnConfig == nil {
		fqdnConfig = map[string]any{}
	}

	// Preserve the descriptive keys the PVE path has always written.
	fqdnConfig["env_type"] = envType
	if base != "" {
		fqdnConfig["base"] = base
	}

	err := p.Safe.SetMultiple(p.PathBuilder.GetFQDNsPath(envType), fqdnConfig)
	if err != nil {
		return fmt.Errorf("failed to set FQDN configuration: %w", err)
	}

	if reporter != nil {
		reporter.ReportPhaseComplete(phaseName, time.Since(phaseStart))
	}

	return nil
}

// ConfigureCertificates configures TLS certificates.
func (p *PVEVaultProvider) ConfigureCertificates(_envPath, _envType string, reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
	phaseName := PhaseCertificates
	phaseStart := time.Now()

	if reporter != nil {
		reporter.ReportPhaseStart(phaseName, phaseNum, totalPhases)
	}

	p.logger.Info("Configuring certificates")

	certsPath := p.PathBuilder.GetCertsPath()

	certConfig := map[string]any{
		"provider": "letsencrypt",
		"note":     "Certificates managed via Let's Encrypt or manual import",
	}

	err := p.Safe.SetMultiple(certsPath, certConfig)
	if err != nil {
		return fmt.Errorf("failed to set certificate configuration: %w", err)
	}

	if reporter != nil {
		reporter.ReportPhaseComplete(phaseName, time.Since(phaseStart))
	}

	return nil
}

// ConfigurePublicIPs configures public IP settings.
func (p *PVEVaultProvider) ConfigurePublicIPs(reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
	phaseName := PhasePublicIPs
	phaseStart := time.Now()

	if reporter != nil {
		reporter.ReportPhaseStart(phaseName, phaseNum, totalPhases)
	}

	p.logger.Info("Configuring public IPs")

	publicIPPath := p.PathBuilder.GetPublicIPsPath()

	// Write a pending marker so downstream consumers can detect unconfigured state
	// with a single key check ("status" == "pending"). PVE has no IaaS-managed
	// floating IPs; the operator must allocate them externally. The "provider"
	// key records that fact without conflicting with the AWS status semantics.
	publicIPConfig := map[string]any{
		"status":   PublicIPStatusPending,
		"provider": "external",
	}

	err := p.Safe.SetMultiple(publicIPPath, publicIPConfig)
	if err != nil {
		return fmt.Errorf("failed to set public IP configuration: %w", err)
	}

	if reporter != nil {
		reporter.ReportPhaseComplete(phaseName, time.Since(phaseStart))
	}

	return nil
}

// ConfigureIAAS configures IAAS settings (implements VaultProvider interface).
func (p *PVEVaultProvider) ConfigureIAAS(envPath, envType string, reporter providers.ProgressReporter, phaseNum *int, totalPhases int) error {
	// IAAS configuration is handled by ConfigureNetworks for Proxmox VE
	return p.ConfigureNetworks(envPath, envType, reporter, *phaseNum, totalPhases)
}

// GetProviderName returns the provider name.
func (p *PVEVaultProvider) GetProviderName() string {
	return "pve"
}

// configureEnvironment configures vault paths for a specific environment type.
func (p *PVEVaultProvider) configureEnvironment(envType string, reporter providers.ProgressReporter, phaseIndex *int, totalPhases int) error {
	envPath := p.PathBuilder.GetEnvironmentPath(envType)

	// Configure networks
	err := p.ConfigureNetworks(envPath, envType, reporter, *phaseIndex, totalPhases)
	if err != nil {
		return err
	}

	*phaseIndex++

	// Configure subnets (minimal for Proxmox - bridges don't have native subnets)
	err = p.ConfigureSubnets(envPath, envType, reporter, *phaseIndex, totalPhases)
	if err != nil {
		return err
	}

	*phaseIndex++

	// Configure security groups
	err = p.ConfigureSecurityGroups(envPath, envType, reporter, *phaseIndex, totalPhases)
	if err != nil {
		return err
	}

	*phaseIndex++

	// Configure blobstores (external for Proxmox; skipped when BlobstoreEndpoint is empty)
	err = p.ConfigureBlobstores(envPath, envType, reporter, *phaseIndex, totalPhases)
	if err != nil {
		return err
	}

	*phaseIndex++

	// Configure databases
	err = p.ConfigureDatabases(envPath, envType, reporter, *phaseIndex, totalPhases)
	if err != nil {
		return err
	}

	*phaseIndex++

	// Configure load balancers (external for Proxmox)
	err = p.ConfigureLoadBalancers(envPath, envType, reporter, *phaseIndex, totalPhases)
	if err != nil {
		return err
	}

	*phaseIndex++

	// Configure FQDNs
	err = p.ConfigureFQDNs(envPath, envType, reporter, *phaseIndex, totalPhases)
	if err != nil {
		return err
	}

	*phaseIndex++

	// Configure CPI credentials for this environment
	err = p.configureCPI(envType)
	if err != nil {
		return fmt.Errorf("failed to configure CPI for %s: %w", envType, err)
	}

	// Configure availability zones (Proxmox nodes as AZ entries)
	err = p.ConfigureAZs(envType)
	if err != nil {
		return fmt.Errorf("failed to configure AZs for %s: %w", envType, err)
	}

	return nil
}

// configureCPI writes Proxmox VE CPI credentials to vault.
//
// Path: secret/config/{bloc}/{envType}/cpi/pve
//
// Auth mode selection (mutually exclusive):
//   - API token auth: Config.AuthToken (token_id) + Config.TokenSecret (token_secret) both set.
//     Writes token_id and token_secret. Does NOT write username or password.
//   - User/password auth: Config.Username + Config.Password both set, AuthToken empty.
//     Writes username and password. Does NOT write token_id or token_secret.
//
// Common fields written in both modes:
//   - host   — PVE API endpoint (Config.APIEndpoint)
//   - node   — Primary Proxmox node name (Config.Region)
//   - status — literal "configured"
//
// Returns an error when host is empty, or when neither auth mode is fully configured.
// Config.Password is never aliased as token_secret; use Config.TokenSecret explicitly.
func (p *PVEVaultProvider) configureCPI(envType string) error {
	p.logger.Infow("Configuring PVE CPI credentials", "env_type", envType)

	host := p.Config.APIEndpoint
	if host == "" {
		return errors.New("pve configureCPI: api_endpoint (host) is required but not set in config")
	}

	node := p.Config.Region
	cpiPath := p.PathBuilder.GetEnvironmentPath(envType) + "/cpi/pve"

	apiTokenMode := p.Config.AuthToken != "" && p.Config.TokenSecret != ""
	userPassMode := p.Config.Username != "" && p.Config.Password != "" && p.Config.AuthToken == ""

	if !apiTokenMode && !userPassMode {
		return errors.New("pve configureCPI: no complete auth configuration found; set (auth_token + token_secret) for API token auth or (username + password) for user/password auth")
	}

	// All PVE CPI keys read by ~/src/kits/bosh/ocfp/pve/base.yml under
	// `meta.ocfp.cpi.*`.  The kit reads these directly via `(( vault ... ))`
	// (no spruce default), so every key must be present even when the
	// operator wants the env yml's params.* override to win.
	// All values are written as strings because spruce's (( vault ... )) operator
	// requires string-typed leaves; numeric or boolean JSON values surface as
	// "is not a string" during manifest rendering.
	//
	// host is stored as bare hostname (no scheme, no port) because the
	// bosh-pve-cpi-release Go client concatenates "https://" + host + ":" +
	// port internally — a stored URL would double-prefix.
	//
	// Storage classification (R3-10 matrix):
	//   disk_storage candidates determine storage_backend and disk_format.
	//   Shared backends:  rbd, cephfs, nfs, cifs, glusterfs, pbs → storage_backend=shared
	//   Local backends:   lvm, lvmthin, zfspool, dir, btrfs      → storage_backend=block
	//   dir is special:   storage_backend=dir
	//   zfspool requires: disk_format=raw (qcow2 unsupported on block devices)
	//   All others:       disk_format=qcow2
	vmStorage := pveCPIVMStorage(p.Config)
	diskStorage := pveCPIDiskStorage(p.Config)
	// Decision D1: resolve cf_max_in_flight via config → pvesh query → default 12.
	// A nil querier is passed here so the query step is always skipped during
	// vault populate (no PVE API credentials are available at that point; the
	// config value or the default is always sufficient). Operators who want the
	// live-derived value should set cf_max_in_flight in the bloc config explicitly.
	cfMaxInFlight := pveCFMaxInFlight(p.Config)
	cpiConfig := map[string]any{
		"cf_max_in_flight": strconv.Itoa(cfMaxInFlight),
		"disk_format":      pveDiskFormat(diskStorage),
		"disk_storage":     diskStorage,
		"host":             pveHostnameOnly(host),
		"iso_storage":      pveFirstNonEmpty(p.Config.IsoStorage, "local"),
		"network_bridge":   pveFirstNonEmpty(p.Config.Network.Name, "lvnet001"),
		"node":             node,
		"port":             strconv.Itoa(pveCPIPort(host)),
		"status":           "configured",
		"stemcell_storage": pveStorageOrDefault(p.Config, "stemcell", "local"),
		"storage_backend":  pveStorageBackend(diskStorage),
		"user":             pveCPIUser(p.Config.AuthToken, p.Config.Username),
		"verify_ssl":       strconv.FormatBool(p.Config.VerifySSL),
		"vm_storage":       vmStorage,
		"vmid_range_end":   strconv.Itoa(pveVMIDRangeEnd(p.Config)),
		"vmid_range_start": strconv.Itoa(pveVMIDRangeStart(p.Config)),
	}

	// password and api_token are mutually exclusive auth modes. The kit's PVE
	// CPI wiring sources only the ACTIVE mode's key from vault, so write only
	// that key — never an empty placeholder for the inactive mode. An empty
	// vault value cannot be entombed into the director's CredHub, and an unused
	// key is simply absent rather than present-and-empty.
	//
	// api_token is rendered in the bosh-pve-cpi-release format
	// "user@realm!tokenid=secret" so the CPI's Proxmox client can authenticate
	// directly with PVE.
	switch {
	case apiTokenMode:
		cpiConfig["token_id"] = p.Config.AuthToken
		cpiConfig["token_secret"] = p.Config.TokenSecret
		cpiConfig["api_token"] = p.Config.AuthToken + "=" + p.Config.TokenSecret
	case userPassMode:
		cpiConfig["username"] = p.Config.Username
		cpiConfig["password"] = p.Config.Password
	}

	err := p.Safe.SetMultiple(cpiPath, cpiConfig)
	if err != nil {
		return fmt.Errorf("failed to set PVE CPI configuration: %w", err)
	}

	p.logger.Infow("PVE CPI credentials configured", "env_type", envType, "path", cpiPath)

	return nil
}

// pveHostnameOnly strips scheme and trailing :port from a URL-shaped host
// value, returning the bare hostname.  "https://pve.example.com:8006" →
// "pve.example.com".  Leaves bare hostnames unchanged.
func pveHostnameOnly(host string) string {
	if idx := indexOf(host, "://"); idx >= 0 {
		host = host[idx+3:]
	}
	// strip trailing :port if present
	for i := len(host) - 1; i >= 0; i-- {
		if host[i] == ':' {
			port := host[i+1:]

			allDigits := port != ""
			for _, c := range port {
				if c < '0' || c > '9' {
					allDigits = false

					break
				}
			}

			if allDigits {
				host = host[:i]
			}

			break
		}

		if host[i] == '/' {
			break
		}
	}
	// strip any trailing path
	if idx := indexOf(host, "/"); idx >= 0 {
		host = host[:idx]
	}

	return host
}

func indexOf(s, sub string) int {
	if len(sub) == 0 || len(sub) > len(s) {
		return -1
	}

	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}

	return -1
}

// pveCPIPort returns the PVE API port from the host URL or the standard 8006.
func pveCPIPort(host string) int {
	const defaultPort = 8006
	// host is "https://pve.example.com:8006".  Cheap parse — proper URL parsing
	// adds a dependency for one substring extraction.
	for i := len(host) - 1; i >= 0; i-- {
		if host[i] == ':' {
			port := 0

			for _, c := range host[i+1:] {
				if c < '0' || c > '9' {
					return defaultPort
				}

				port = port*10 + int(c-'0')
			}

			if port > 0 {
				return port
			}

			break
		}

		if host[i] == '/' {
			break
		}
	}

	return defaultPort
}

// pveCPIUser extracts the user@realm portion from a PVE API token id
// ("user@realm!tokenName"), or returns the raw username when API tokens
// are not in use.  Defaults to "root@pam" when both are empty so the kit's
// (( vault ... :user )) lookup never resolves to an empty string.
func pveCPIUser(authToken, username string) string {
	if authToken != "" {
		for i, c := range authToken {
			if c == '!' {
				return authToken[:i]
			}
		}

		return authToken
	}

	if username != "" {
		return username
	}

	return "root@pam"
}

// pveFirstNonEmpty returns the first non-empty string from its arguments.
func pveFirstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}

	return ""
}

// pveCPIVMStorage resolves the effective vm_storage vault key value.
//
// Priority:
//  1. Config.VMStorage — explicit operator override (new field).
//  2. Artifacts.Data.StoragePool — legacy fallback kept for backwards compat.
//  3. "local-lvm" — conservative hardcoded default.
func pveCPIVMStorage(cfg *config.Config) string {
	if cfg.VMStorage != "" {
		return cfg.VMStorage
	}

	if cfg.Artifacts.Data.StoragePool != "" {
		return cfg.Artifacts.Data.StoragePool
	}

	return "local-lvm"
}

// pveCPIDiskStorage resolves the effective disk_storage vault key value.
//
// Priority:
//  1. Config.DiskStorage — explicit operator override (new field).
//  2. Artifacts.Data.StoragePool — legacy fallback; maps persistent-disk pool to
//     disk_storage so blocs that already configured artifacts.data.storage_pool
//     continue working without migration.
//  3. "zfs-1" — conservative hardcoded default matching Wayne-lab reference setup.
func pveCPIDiskStorage(cfg *config.Config) string {
	if cfg.DiskStorage != "" {
		return cfg.DiskStorage
	}

	if cfg.Artifacts.Data.StoragePool != "" {
		return cfg.Artifacts.Data.StoragePool
	}

	return "zfs-1"
}

// pveStorageOrDefault honors per-role storage hints from the artifacts
// config when present (artifacts.data.storage_pool maps to stemcell_storage),
// and falls back to the supplied default.
// NOTE: vm_storage and disk_storage are now resolved by pveCPIVMStorage and
// pveCPIDiskStorage respectively; this helper is retained for stemcell_storage.
func pveStorageOrDefault(cfg *config.Config, role, def string) string {
	if cfg.Artifacts.Data.StoragePool != "" && role == "stemcell" {
		return cfg.Artifacts.Data.StoragePool
	}

	return def
}

// pveStorageBackend returns the BOSH-CPI storage_backend classification for a
// PVE storage pool name.
//
// Shared backends (cluster-visible): rbd, cephfs, nfs, cifs, glusterfs, pbs.
// Dir backend: dir (special-cased; CPI uses "dir" not "block" or "shared").
// Local/block backends: lvm, lvmthin, zfspool, btrfs — all return "block".
// Unknown pool names: "block" (conservative default, suitable for most installs).
//
// Source: R3-10 storage matrix; matches bosh-pve-cpi-release _LOCAL_DISK_TYPES.
func pveStorageBackend(poolName string) string {
	switch strings.ToLower(strings.TrimSpace(poolName)) {
	case "rbd", "cephfs", "nfs", "cifs", "glusterfs", "pbs":
		return "shared"
	case "dir":
		return "dir"
	case "lvm", "lvmthin", "zfspool", "btrfs":
		return "block"
	default:
		// Unknown pool: default to "block" (most common local install type).
		// Caller logs a warning via configureCPI summary; no error returned here
		// because the pool name may be a site-specific alias.
		return "block"
	}
}

// pveDiskFormat returns the disk_format required by the given storage pool.
//
// zfspool, lvm, lvmthin, and btrfs block devices do NOT support qcow2; they
// require raw format. Pool names are site-specific aliases rather than type
// keywords (e.g. "zfs-1", "local-lvm-data"), so match by substring: any name
// containing lvm, zfs, or btrfs is treated as a block backend needing raw.
// All other pools default to qcow2 (dir, rbd, nfs, cifs, glusterfs, pbs).
//
// Source: R3-10; Wayne-lab storage-pools.yml; bosh-pve-cpi docs.
func pveDiskFormat(poolName string) string {
	name := strings.ToLower(strings.TrimSpace(poolName))
	for _, marker := range []string{"lvm", "zfs", "btrfs"} {
		if strings.Contains(name, marker) {
			return "raw"
		}
	}

	return "qcow2"
}

// pveVMIDRangeStart returns the configured VMID range start, or the default
// value 100 when the Config field is zero (unset). 100 is the lab-tested lower
// bound: it clears the PVE internal IDs (1-99) and reserved template range
// (9000+) without overlapping the OCFP lab allocation space.
func pveVMIDRangeStart(cfg *config.Config) int {
	const defaultStart = 100
	if cfg == nil || cfg.VmidRangeStart <= 0 {
		return defaultStart
	}

	return cfg.VmidRangeStart
}

// pveVMIDRangeEnd returns the configured VMID range end, or the default value
// 5999 when the Config field is zero (unset). 5999 leaves the range 6000-8999
// free for operator use and keeps PVE template IDs (9000+) unreachable by the
// CPI allocator.
func pveVMIDRangeEnd(cfg *config.Config) int {
	const defaultEnd = 5999
	if cfg == nil || cfg.VmidRangeEnd <= 0 {
		return defaultEnd
	}

	return cfg.VmidRangeEnd
}

// pveCFMaxInFlight resolves the cf_max_in_flight value written to vault.
//
// Resolution order per Decision D1:
//  1. Config.CfMaxInFlight > 0 — explicit operator override; used as-is.
//  2. Live PVE node query — not attempted here (no credentials at vault-populate
//     time); callers that have API access may pass a non-nil Querier to
//     capacity.Resolve directly.
//  3. Default 12 — sized to PVE's default storage worker thread count.
//
// The function always returns a positive integer. A nil Config is safe.
func pveCFMaxInFlight(cfg *config.Config) int {
	if cfg != nil && cfg.CfMaxInFlight > 0 {
		return cfg.CfMaxInFlight
	}

	// No live query during vault populate (no PVE credentials available).
	// Resolve with nil querier; falls straight to the default.
	resolved := capacity.Resolve(context.Background(), nil, "", 0, 0)

	return resolved.MaxInFlight
}

// pveWorkloadAZCount is the number of workload availability zones a PVE bloc
// exposes. It mirrors the ocfp-{0,1,2} workload subnets carved by
// bootstrap.createPVEVirtualSubnets (pveSubnetCount-1). The subnet layer
// assigns those subnets AZ keys "pve"+{a,b,c} (bootstrap.pveAZNamePrefix), and
// the Genesis director cloud-config hook (Director.pm _set_network_azs)
// resolves each ocfp-* subnet's az against the net/azs/<zone> entries written
// here. Keep in sync with bootstrap.pveSubnetCount / pveAZNamePrefix.
const (
	pveWorkloadAZCount = 3
	pveAZKeyPrefix     = "pve"
)

// ConfigureAZs writes the PVE workload availability zones as vault entries,
// keyed by ZONE name (pvea/pveb/pvec) — the same keys the subnet layer assigns
// to the ocfp-{0,1,2} subnets — NOT by node name.
//
// Path pattern: secret/config/{bloc}/{envType}/net/azs/{zone}
//
// Proxmox has no cloud-provider availability zones; each PVE node is an
// independent failure domain. BOSH still needs a stable set of AZ keys that the
// workload subnets reference. Genesis' _set_network_azs resolves every ocfp-*
// subnet's az ("pvea"...) against these keys; keying by node ("pve") instead
// leaves them unresolvable ("AZ pvea not found in the available AZs for the
// network"). Each zone records the node that physically backs it via node_name;
// multi-node blocs spread zones across Config.Nodes round-robin, while a
// single-node bloc backs every zone with the one node.
//
// Node list source (in priority order):
//  1. Config.Nodes — round-robin backing for the zones when len > 0.
//  2. Config.Region — single backing node when Nodes is empty.
//  3. Both empty — logs a warning and returns nil (no error).
func (p *PVEVaultProvider) ConfigureAZs(envType string) error {
	p.logger.Infow("Configuring PVE AZs (workload zones)", "env_type", envType)

	// Resolve the node(s) that physically back the zones.
	nodes := p.Config.Nodes
	if len(nodes) == 0 && p.Config.Region != "" {
		nodes = []string{p.Config.Region}
	}

	if len(nodes) == 0 {
		p.logger.Warnw("No nodes configured (Nodes slice and Region are both empty), skipping AZ configuration", "env_type", envType)

		return nil
	}

	for z := range pveWorkloadAZCount {
		zone := pveAZKeyPrefix + string(rune('a'+z))
		node := nodes[z%len(nodes)]
		azPath := p.PathBuilder.GetAZPath(envType, zone)

		// index (1-based) drives Genesis' AZ naming: name = "<env>-z" . index.
		// Zone letters carry no trailing digit, so the explicit index yields
		// pvea->"<env>-z1", pveb->z2, pvec->z3, matching the kit's
		// default_cf_az and the workload subnets' az assignment.
		azData := map[string]any{
			"node_name": node,
			"index":     z + 1,
			"status":    "configured",
		}

		err := p.Safe.SetMultiple(azPath, azData)
		if err != nil {
			return fmt.Errorf("failed to set AZ entry for zone %s: %w", zone, err)
		}

		p.logger.Infow("PVE AZ entry configured", "env_type", envType, "zone", zone, "node", node, "path", azPath)
	}

	return nil
}

// configureSharedComponents configures shared vault paths.
func (p *PVEVaultProvider) configureSharedComponents(reporter providers.ProgressReporter, phaseIndex *int, totalPhases int) error {
	// Configure certificates
	err := p.ConfigureCertificates("", "", reporter, *phaseIndex, totalPhases)
	if err != nil {
		return err
	}

	*phaseIndex++

	// Configure public IPs
	err = p.ConfigurePublicIPs(reporter, *phaseIndex, totalPhases)
	if err != nil {
		return err
	}

	*phaseIndex++

	return nil
}
