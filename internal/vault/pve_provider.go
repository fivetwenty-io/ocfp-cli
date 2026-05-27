package vault

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

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
}

// NewPVEVaultProvider creates a new Proxmox VE vault provider.
func NewPVEVaultProvider(cfg *config.Config, safe SafeInterface, blocName string) *PVEVaultProvider {
	return &PVEVaultProvider{
		BaseVaultProvider: providers.NewBaseVaultProvider(cfg, blocName),
		Safe:              safe,
		PathBuilder:       NewPathBuilder(cfg, blocName),
		logger:            logger.Get(),
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

	configData := map[string]interface{}{
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

	networkConfig := map[string]interface{}{
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
		if err := p.writeFallbackSubnet(envType); err != nil {
			return err
		}

		if reporter != nil {
			reporter.ReportPhaseComplete(phaseName, time.Since(phaseStart))
		}

		return nil
	}

	subnets, err := sm.GetResourcesByType("subnet")
	if err != nil || len(subnets) == 0 {
		if err := p.writeFallbackSubnet(envType); err != nil {
			return err
		}

		if reporter != nil {
			reporter.ReportPhaseComplete(phaseName, time.Since(phaseStart))
		}

		return nil
	}

	subnetsPath := p.PathBuilder.GetSubnetsPath(envType)

	for _, sub := range subnets {
		if !strings.HasPrefix(sub.Name, p.BlocName+"-") {
			continue
		}

		cidr, _ := sub.Properties["cidr"].(string)
		az, _ := sub.Properties["availability_zone"].(string)
		gateway, _ := sub.Properties["gateway"].(string)

		if cidr == "" {
			continue
		}

		// Derive gateway (first host) and dns from the subnet when state omits
		// them; genesis's dynamic-subnet cloud-config builder reads per-subnet
		// dns/gateway directly and emits dns: [null] / a bad gateway otherwise.
		if gateway == "" {
			gateway = pveCIDRGateway(cidr)
		}

		dns := pveFirstNonEmpty(pveFirstDNS(p.Config.DNS), gateway, "1.1.1.1")

		subnetPath := filepath.Join(subnetsPath, sub.Name)
		if err := p.Safe.SetMultiple(subnetPath, map[string]interface{}{
			"cidr":    cidr,
			"az":      az,
			"gateway": gateway,
			"dns":     dns,
		}); err != nil {
			return fmt.Errorf("failed to write subnet %s: %w", sub.Name, err)
		}

		// Reserved IPs — pull each `reserved_{name}_{role}_ip` output.
		if err := p.writeReservedIPs(sm, subnetPath, sub.Name); err != nil {
			return err
		}
	}

	if reporter != nil {
		reporter.ReportPhaseComplete(phaseName, time.Since(phaseStart))
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

	err := p.Safe.SetMultiple(subnetsPath, map[string]interface{}{
		"note": "Proxmox SDN: single vnet subnet; no bootstrap state found",
		"cidr": p.Config.VPCCIDRBlock,
	})
	if err != nil {
		return fmt.Errorf("failed to set subnet configuration: %w", err)
	}

	cidr := p.Config.Network.CIDR
	if cidr == "" {
		cidr = p.Config.VPCCIDRBlock
	}

	gateway := pveCIDRGateway(cidr)
	dns := pveFirstNonEmpty(pveFirstDNS(p.Config.DNS), gateway, "1.1.1.1")
	bridgeID := pveFirstNonEmpty(p.Config.Network.Name, "vmbr0")
	az := pveFirstAZ(p.Config)
	boshIP := pveOffsetIP(gateway, 9)
	jumpboxIP := pveOffsetIP(gateway, 8)

	// PVE single-network mode: populate ocfp-0..ocfp-2 with identical data so
	// the bosh kit's cloud-config-director hook (which hardcodes specific
	// subnet refs like 'ocfp-2' for the compilation network) resolves without
	// special-casing.  All three logical subnets share the same underlying
	// lvnet bridge — this is conventional for single-vnet PVE deployments.
	for i := 0; i < 3; i++ {
		subnetPath := p.PathBuilder.GetSubnetPath(envType, "ocfp", i)
		if err := p.Safe.SetMultiple(subnetPath, map[string]interface{}{
			"cidr":       cidr,
			"cidr_block": cidr,
			"gateway":    gateway,
			"dns":        dns,
			"az":         az,
			"id":         bridgeID,
		}); err != nil {
			return fmt.Errorf("failed to set ocfp-%d subnet entry: %w", i, err)
		}

		if boshIP == "" {
			continue
		}

		reservedPath := p.PathBuilder.GetReservedIPsPath(envType, "ocfp", i)
		if err := p.Safe.SetMultiple(reservedPath, map[string]interface{}{
			"bosh_ip":     boshIP,
			"ip":          boshIP,
			"director_ip": boshIP,
			"jumpbox_ip":  jumpboxIP,
		}); err != nil {
			return fmt.Errorf("failed to set ocfp-%d reserved-ips: %w", i, err)
		}
	}

	return nil
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

// pveOffsetIP returns base IP with its last octet incremented by `off`.
// Caps at 254 to stay below broadcast.  Returns "" on parse failure.
func pveOffsetIP(base string, off int) string {
	last := -1
	for i := len(base) - 1; i >= 0; i-- {
		if base[i] == '.' {
			last = i
			break
		}
	}
	if last <= 0 || last == len(base)-1 {
		return ""
	}
	octet := 0
	for _, c := range base[last+1:] {
		if c < '0' || c > '9' {
			return ""
		}
		octet = octet*10 + int(c-'0')
	}
	result := octet + off
	if result < 0 || result > 254 {
		return ""
	}
	return fmt.Sprintf("%s%d", base[:last+1], result)
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
func (p *PVEVaultProvider) writeReservedIPs(sm *state.Manager, subnetPath, subnetName string) error {
	current := sm.Current()
	if current == nil || len(current.Outputs) == 0 {
		return nil
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

		rolePath := filepath.Join(subnetPath, "reserved-ips", role)
		if err := p.Safe.SetMultiple(rolePath, map[string]interface{}{
			"ip": ip,
		}); err != nil {
			return fmt.Errorf("failed to write reserved IP for %s/%s: %w", subnetName, role, err)
		}
	}

	return nil
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

	sgConfig := map[string]interface{}{
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
	blobstorePath := p.PathBuilder.GetSystemBlobstorePath(envType, "cf", "main")

	switch mode {
	case "external":
		err := p.configureExternalBlobstore(envType, blobstorePath)
		if err != nil {
			return err
		}
	default:
		p.logger.Infow("Configuring local-mode blobstore (no external endpoint)", "env_type", envType)

		err := p.Safe.SetMultiple(blobstorePath, map[string]interface{}{
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
	// this path with real endpoint+creds via configureBOSHBlobstore.
	if envType == "mgmt" && mode != "external" {
		boshBlobPath := p.PathBuilder.GetSystemBlobstorePath(envType, "bosh", "bosh")
		region := pveFirstNonEmpty(p.BlobstoreRegion, "us-east-1")
		bucket := pveFirstNonEmpty(p.BlocName+"-bosh", "bosh-blobs")
		if err := p.Safe.SetMultiple(boshBlobPath, map[string]interface{}{
			"name":   bucket,
			"region": region,
			"mode":   mode,
		}); err != nil {
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
		return fmt.Errorf("pve blobstore external mode requires --blobstore-endpoint")
	}

	region := p.BlobstoreRegion
	if region == "" {
		region = "us-east-1"
	}

	p.logger.Infow("Configuring external blobstore", "env_type", envType, "endpoint", p.BlobstoreEndpoint, "region", region)

	blobstoreConfig := map[string]interface{}{
		"mode":       "external",
		"endpoint":   p.BlobstoreEndpoint,
		"region":     region,
		"path_style": true,
		"status":     "configured",
	}

	err := p.Safe.SetMultiple(blobstorePath, blobstoreConfig)
	if err != nil {
		return fmt.Errorf("failed to set blobstore configuration: %w", err)
	}

	if p.BlobstoreAccessKey != "" || p.BlobstoreSecretKey != "" {
		credsPath := blobstorePath + "/creds"

		credsConfig := map[string]interface{}{
			"access_key": p.BlobstoreAccessKey,
			"secret_key": p.BlobstoreSecretKey,
		}

		err = p.Safe.SetMultiple(credsPath, credsConfig)
		if err != nil {
			return fmt.Errorf("failed to set blobstore credentials: %w", err)
		}
	}

	if envType == "mgmt" {
		err = p.configureBOSHBlobstore(region)
		if err != nil {
			return err
		}
	}

	return nil
}

// configureBOSHBlobstore writes the BOSH director's external blobstore config
// and credentials so the genesis BOSH kit can deploy with an S3-compatible
// blobstore (RustFS/MinIO/Ceph). Path mirrors AWS naming convention.
func (p *PVEVaultProvider) configureBOSHBlobstore(region string) error {
	boshPath := p.PathBuilder.GetSystemBlobstorePath("mgmt", "bosh", "bosh")

	p.logger.Infow("Configuring external BOSH director blobstore", "endpoint", p.BlobstoreEndpoint, "region", region, "path", boshPath)

	boshConfig := map[string]interface{}{
		"mode":       "external",
		"endpoint":   p.BlobstoreEndpoint,
		"region":     region,
		"path_style": true,
		"status":     "configured",
	}

	err := p.Safe.SetMultiple(boshPath, boshConfig)
	if err != nil {
		return fmt.Errorf("failed to set BOSH blobstore configuration: %w", err)
	}

	if p.BlobstoreAccessKey != "" || p.BlobstoreSecretKey != "" {
		err = p.Safe.SetMultiple(boshPath+"/creds", map[string]interface{}{
			"access_key": p.BlobstoreAccessKey,
			"secret_key": p.BlobstoreSecretKey,
		})
		if err != nil {
			return fmt.Errorf("failed to set BOSH blobstore credentials: %w", err)
		}
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

	dbConfig := map[string]interface{}{
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

	lbConfig := map[string]interface{}{
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

	fqdnPath := p.PathBuilder.GetFQDNsPath(envType)

	fqdnConfig := map[string]interface{}{
		"env_type": envType,
	}

	// Add configured FQDNs if available
	if p.Config.FQDNs != nil {
		if p.Config.FQDNs.Base != "" {
			fqdnConfig["base"] = p.Config.FQDNs.Base
		}
	}

	err := p.Safe.SetMultiple(fqdnPath, fqdnConfig)
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

	certConfig := map[string]interface{}{
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
	publicIPConfig := map[string]interface{}{
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
		return fmt.Errorf("pve configureCPI: api_endpoint (host) is required but not set in config")
	}

	node := p.Config.Region
	cpiPath := p.PathBuilder.GetEnvironmentPath(envType) + "/cpi/pve"

	apiTokenMode := p.Config.AuthToken != "" && p.Config.TokenSecret != ""
	userPassMode := p.Config.Username != "" && p.Config.Password != "" && p.Config.AuthToken == ""

	if !apiTokenMode && !userPassMode {
		return fmt.Errorf("pve configureCPI: no complete auth configuration found; set (auth_token + token_secret) for API token auth or (username + password) for user/password auth")
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
	cpiConfig := map[string]interface{}{
		"cf_max_in_flight": fmt.Sprintf("%d", cfMaxInFlight),
		"disk_format":      pveDiskFormat(diskStorage),
		"disk_storage":     diskStorage,
		"host":             pveHostnameOnly(host),
		"iso_storage":      pveFirstNonEmpty(p.Config.IsoStorage, "local"),
		"network_bridge":   pveFirstNonEmpty(p.Config.Network.Name, "lvnet001"),
		"node":             node,
		"port":             fmt.Sprintf("%d", pveCPIPort(host)),
		"status":           "configured",
		"stemcell_storage": pveStorageOrDefault(p.Config, "stemcell", "local"),
		"storage_backend":  pveStorageBackend(diskStorage),
		"user":             pveCPIUser(p.Config.AuthToken, p.Config.Username),
		"verify_ssl":       fmt.Sprintf("%t", p.Config.VerifySSL),
		"vm_storage":       vmStorage,
		"vmid_range_end":   fmt.Sprintf("%d", pveVMIDRangeEnd(p.Config)),
		"vmid_range_start": fmt.Sprintf("%d", pveVMIDRangeStart(p.Config)),
	}

	// password and api_token are mutually exclusive auth modes but the kit
	// reads both via plain (( vault ... )) — the key MUST exist even when its
	// auth mode is inactive, so write the unused one as an empty string.
	// password and api_token are mutually exclusive auth modes but the kit
	// reads both via plain (( vault ... )) — the key MUST exist even when its
	// auth mode is inactive, so write the unused one as an empty string.
	//
	// api_token is rendered in the bosh-pve-cpi-release format
	// "user@realm!tokenid=secret" so the CPI's Proxmox client can authenticate
	// directly with PVE.
	switch {
	case apiTokenMode:
		cpiConfig["token_id"] = p.Config.AuthToken
		cpiConfig["token_secret"] = p.Config.TokenSecret
		cpiConfig["api_token"] = p.Config.AuthToken + "=" + p.Config.TokenSecret
		cpiConfig["password"] = ""
	case userPassMode:
		cpiConfig["username"] = p.Config.Username
		cpiConfig["password"] = p.Config.Password
		cpiConfig["api_token"] = ""
	}

	if err := p.Safe.SetMultiple(cpiPath, cpiConfig); err != nil {
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
// zfspool block devices do NOT support qcow2; they require raw format.
// lvm and lvmthin also require raw for the same reason (block devices).
// All other pool types default to qcow2 (dir, rbd, nfs, cifs, glusterfs, pbs).
//
// Source: R3-10; Wayne-lab storage-pools.yml; bosh-pve-cpi docs.
func pveDiskFormat(poolName string) string {
	switch strings.ToLower(strings.TrimSpace(poolName)) {
	case "zfspool", "lvm", "lvmthin":
		return "raw"
	default:
		return "qcow2"
	}
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

// ConfigureAZs writes Proxmox node names as availability zone entries.
//
// Path pattern: secret/config/{bloc}/{envType}/net/azs/{node}
//
// Proxmox does not have availability zones in the cloud-provider sense. Each
// Proxmox node in the cluster acts as an independent failure domain. This method
// writes one vault entry per node so that BOSH directors can reference them as
// AZ cloud properties.
//
// Node list source (in priority order):
//  1. Config.Nodes — iterated when len > 0; one vault write per node.
//  2. Config.Region — fallback single-node when Nodes is empty.
//  3. Both empty — logs a warning and returns nil (no error).
func (p *PVEVaultProvider) ConfigureAZs(envType string) error {
	p.logger.Infow("Configuring PVE AZs (nodes as AZ entries)", "env_type", envType)

	// Multi-node: iterate Config.Nodes when set; single-node: fall back to Config.Region.
	switch {
	case len(p.Config.Nodes) > 0:
		for _, node := range p.Config.Nodes {
			azPath := p.PathBuilder.GetAZPath(envType, node)

			azData := map[string]interface{}{
				"node_name": node,
				"status":    "configured",
			}

			if err := p.Safe.SetMultiple(azPath, azData); err != nil {
				return fmt.Errorf("failed to set AZ entry for node %s: %w", node, err)
			}

			p.logger.Infow("PVE AZ entry configured", "env_type", envType, "node", node, "path", azPath)
		}

	case p.Config.Region != "":
		node := p.Config.Region
		azPath := p.PathBuilder.GetAZPath(envType, node)

		azData := map[string]interface{}{
			"node_name": node,
			"status":    "configured",
		}

		if err := p.Safe.SetMultiple(azPath, azData); err != nil {
			return fmt.Errorf("failed to set AZ entry for node %s: %w", node, err)
		}

		p.logger.Infow("PVE AZ entry configured", "env_type", envType, "node", node, "path", azPath)

	default:
		p.logger.Warnw("No nodes configured (Nodes slice and Region are both empty), skipping AZ configuration", "env_type", envType)
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
