package bootstrap

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/artifacts"
	"github.com/ocfp/ocfp-cli-go/internal/artifacts/provision"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/state"
	"github.com/ocfp/ocfp-cli-go/internal/vault"
)

const (
	// artifactsReadinessTimeout bounds the post-boot wait for RustFS.
	artifactsReadinessTimeout = 8 * time.Minute

	// artifactsReadinessPoll is the poll interval for the readiness probe.
	artifactsReadinessPoll = 6 * time.Second

	// artifactsDataDeviceHint is the device path the attached volume should
	// land on inside the VM. Cloud-init runs `zpool create rpool /dev/sdb`.
	artifactsDataDeviceHint = "/dev/sdb"

	// artifactsResourceType is the state resource Type for the artifacts VM.
	artifactsResourceType = "artifacts"

	// artifactsSkipPathProbeTimeout bounds the re-probe run on the bootstrap
	// skip path (state already records the VM). Short and warning-only: this
	// is a re-run convergence check, not the initial-boot readiness gate
	// (artifactsReadinessTimeout), so a slow RustFS restart must not fail an
	// otherwise no-op bootstrap re-run.
	artifactsSkipPathProbeTimeout = 30 * time.Second
)

// ErrArtifactsVaultUnavailable is the cause wrapped into the actionable
// internal-ca vault error when this bootstrap run never obtained vault
// access at all (SetSafe was never called — see executeBootstrap in
// internal/commands/bootstrap.go), as opposed to a specific dial/auth
// failure from vault.NewManagerFromEnv.
var ErrArtifactsVaultUnavailable = errors.New("vault access unavailable for this bootstrap run")

// CreateArtifacts provisions the ocfp-artifacts VM when the bloc opts into
// Artifacts.Enabled. The step performs:
//
//  1. Resolve networking from the bastion subnet (same infra subnet).
//  2. Resolve credentials + TLS material.
//  3. Render cloud-init.
//  4. Create the VM. On failure, no state recorded.
//  5. Create + attach the data volume. On failure, delete the VM.
//  6. Poll the RustFS S3 endpoint until ready.
//  7. Create the configured buckets directly via the artifacts endpoint.
//  8. Record state.
//
// The step is idempotent: if state already records an artifacts VM and the
// readiness probe succeeds, it returns nil without touching anything.
//
//nolint:cyclop,funlen,maintidx // linear 8-step idempotent provisioning sequence; splitting would obscure the flow
func (m *Manager) CreateArtifacts(ctx context.Context) error {
	if !m.config.Artifacts.Enabled {
		_, _ = fmt.Fprintf(os.Stdout, "    • artifacts feature disabled in config; skipping\n")

		return nil
	}

	vmName := m.options.BlocName + "-artifacts"

	if existing, _ := m.stateManager.GetResource(artifactsResourceType, vmName); existing != nil {
		_, _ = fmt.Fprintf(os.Stdout, "    • Artifacts VM %s already in state; converging\n", vmName)
		logger.Infof("Artifacts VM %s already recorded; converging", vmName)

		m.convergeExistingArtifacts(ctx, existing)

		return nil
	}

	networkID, subnetInfo, err := m.resolveBastionNetworking()
	if err != nil {
		return fmt.Errorf("artifacts: resolve networking: %w", err)
	}

	sgID, err := m.getArtifactsSecurityGroup()
	if err != nil {
		return fmt.Errorf("artifacts: resolve security group: %w", err)
	}

	ipStr, err := CalculateIPFromCIDR(subnetInfo.CIDR, artifactsIPSlot)
	if err != nil {
		return fmt.Errorf("artifacts: compute static IP: %w", err)
	}

	ip := net.ParseIP(ipStr)
	if ip == nil {
		return fmt.Errorf("artifacts: invalid IP %q from CIDR %s", ipStr, subnetInfo.CIDR)
	}

	creds, err := artifacts.ResolveCredentials(artifacts.Credentials{
		AccessKey: m.config.Artifacts.Rustfs.AccessKey,
		SecretKey: m.config.Artifacts.Rustfs.SecretKey,
	})
	if err != nil {
		return fmt.Errorf("artifacts: resolve credentials: %w", err)
	}

	var (
		tlsMat *artifacts.TLSMaterial
		caPEM  string
	)

	switch m.config.Artifacts.TLS.Mode {
	case config.ArtifactsTLSModeSelfSigned:
		cn := m.config.Artifacts.TLS.CommonName
		if cn == "" {
			cn = vmName
		}

		mat, err := artifacts.GenerateSelfSignedTLS(cn, []string{cn, vmName}, artifacts.ArtifactsLeafSANIPs(ip))
		if err != nil {
			return fmt.Errorf("artifacts: generate self-signed TLS: %w", err)
		}

		tlsMat = &mat
		caPEM = mat.CertPEM
	case config.ArtifactsTLSModeInternalCA:
		if m.safe == nil {
			return artifacts.InternalCAVaultError(m.options.BlocName, ErrArtifactsVaultUnavailable)
		}

		ca, caErr := vault.LoadOrGenerateBlocCA(m.safe, m.options.BlocName)
		if caErr != nil {
			return fmt.Errorf("artifacts: load bloc CA: %w", caErr)
		}

		cn := m.config.Artifacts.TLS.CommonName
		if cn == "" {
			cn = vmName
		}

		leaf, leafErr := artifacts.IssueLeafCert(ca, cn, []string{cn, vmName}, artifacts.ArtifactsLeafSANIPs(ip))
		if leafErr != nil {
			return fmt.Errorf("artifacts: issue leaf cert: %w", leafErr)
		}

		tlsMat = &leaf
		// caPEM is the CA cert (what genesis kits pin), NOT the leaf cert.
		caPEM = ca.CertPEM
	case config.ArtifactsTLSModeDisabled:
		// Plain HTTP on :9000; no TLS material.
	default:
		return fmt.Errorf("artifacts: unsupported TLS mode %q", m.config.Artifacts.TLS.Mode)
	}

	ciInputs := provision.ArtifactsCloudInitInputs{
		AccessKey:   creds.AccessKey,
		SecretKey:   creds.SecretKey,
		DownloadURL: m.config.Artifacts.ResolvedDownloadURL(),
		S3Port:      m.config.Artifacts.Rustfs.S3Port,
		ConsolePort: m.config.Artifacts.Rustfs.ConsolePort,
		Mountpoint:  m.config.Artifacts.Data.Mountpoint,
		Filesystem:  m.config.Artifacts.ResolvedFilesystem(),
		ZFSDataset:  m.config.Artifacts.ResolvedDataset(m.options.BlocName),
		TLSEnabled:  tlsMat != nil,
		CertPEM:     pemOrEmpty(tlsMat, true),
		KeyPEM:      pemOrEmpty(tlsMat, false),
		// CAPEM is the VM's own on-box trust anchor (installed into the OS
		// trust store by the provisioning script/cloud-init) — the bloc CA
		// for internal-ca, or the leaf itself for self-signed (it is its own
		// trust anchor). Empty when TLS is disabled. See VM self-trust
		// (scripts/provision/artifacts, RUSTFS_TLS_CA).
		CAPEM: caPEM,
	}

	// cloud-init user-data is retained for providers whose snippet/user-data
	// delivery works. On PVE 9.x the snippet-upload API is blocked, so the
	// identical provisioning is delivered over SSH after boot (see below).
	userData, err := provision.RenderArtifactsCloudInit(ciInputs)
	if err != nil {
		return fmt.Errorf("artifacts: render cloud-init: %w", err)
	}

	flavorID, err := m.resolveFlavorID(ctx, m.config.Artifacts.Flavor)
	if err != nil {
		return fmt.Errorf("artifacts: resolve flavor: %w", err)
	}

	imageID, err := m.resolveImageID(ctx, m.config.Artifacts.Template)
	if err != nil {
		return fmt.Errorf("artifacts: resolve image: %w", err)
	}

	req := &cpi.InstanceRequest{
		Name:                  vmName,
		Flavor:                flavorID,
		Image:                 imageID,
		KeyPairName:           m.options.BlocName + "-keypair",
		NetworkID:             networkID,
		SubnetID:              m.adjustSubnetForProvider(subnetInfo.ID),
		AvailabilityZone:      subnetInfo.AvailabilityZone,
		SecurityGroupIDs:      []string{sgID},
		UserData:              userData,
		Tags:                  artifactsTags(m.baseTags(), m.options.BlocName),
		StaticPrivateIP:       ipStr,
		StaticPrivateIPPrefix: m.bastionStaticIPPrefix(),
		PublicKey:             m.bastionPublicKey(),
		DefaultUsername:       m.bastionDefaultUsername(),
		GatewayIP:             m.bastionGatewayIP(),
		DNSServers:            m.config.Network.DNSServers,
		Hostname:              vmName,
		DomainSuffix:          m.bastionDomainSuffix(),
		VCPUsOverride:         m.config.Artifacts.CPU,
		MemoryMiBOverride:     m.config.Artifacts.MemoryMiB,
	}

	inst, err := m.provider.ComputeManager().CreateInstance(ctx, req)
	if err != nil {
		return fmt.Errorf("artifacts: create instance: %w", err)
	}

	vol, attachErr := m.attachArtifactsDataVolume(ctx, inst.ID, vmName)
	if attachErr != nil {
		m.cleanupOrphanArtifactsVM(ctx, inst.ID, vmName)

		return fmt.Errorf("artifacts: attach data volume: %w", attachErr)
	}

	// Cloud-init user-data (rendered above, attached to req.UserData) is the
	// default delivery path. Providers whose compute backend needs an extra
	// out-of-band step (PVE 9.x blocks cloud-init snippet upload) implement
	// artifactsDeliverer instead of branching here — see
	// resolveArtifactsDeliverer (artifacts_deliverer.go).
	err = resolveArtifactsDeliverer(m.options.Provider).deliverArtifacts(ctx, m, ciInputs, ipStr)
	if err != nil {
		// Non-fatal: leave the VM for triage and let the readiness probe
		// below surface the resulting unhealthy state. The pve delivery
		// script is idempotent, so a re-run (after clearing state) retries
		// cleanly.
		_, _ = fmt.Fprintf(os.Stderr, "warning: artifacts provisioning delivery: %v\n", err)
	}

	ep := buildArtifactsEndpoint(ip, m.config.Artifacts, caPEM)

	err = m.waitArtifactsReady(ctx, ep, creds)
	if err != nil {
		// Don't delete the VM here — operator may want to triage it. Surface
		// the timeout but leave the VM in place so `ocfp artifacts ssh` works.
		_, _ = fmt.Fprintf(os.Stderr, "warning: %v — VM left running for triage\n", err)
	}

	err = m.recordArtifactsState(vmName, inst, vol, ip, creds, ep, tlsMat, leafNotAfterRFC3339(tlsMat))
	if err != nil {
		return fmt.Errorf("artifacts: record state: %w", err)
	}

	if m.safe != nil {
		writer := vault.NewArtifactsWriter(m.config, m.safe, m.options.BlocName)

		err = writer.WriteArtifacts(ctx, m.options.BlocName, ep, creds, tlsMat)
		if err != nil {
			return fmt.Errorf("artifacts: write vault: %w", err)
		}
	} else {
		_, _ = fmt.Fprintln(os.Stderr, "warning: artifacts vault writer unavailable; run `ocfp vault populate --blobstore-endpoint=...` to sync")
	}

	bucketErr := artifacts.EnsureBuckets(ctx, ep, creds, artifactsBucketList(m.options.BlocName, m.config))
	if bucketErr != nil {
		_, _ = fmt.Fprintf(os.Stderr, "warning: artifacts bucket creation: %v\n", bucketErr)
	}

	return nil
}

// leafNotAfterRFC3339 extracts the issued leaf certificate's NotAfter (task
// 6.2, leaf-expiry visibility) for recording alongside the artifacts state
// resource. Both artifacts.GenerateSelfSignedTLS and artifacts.IssueLeafCert
// already populate TLSMaterial.NotAfter at issuance, so the common case is a
// direct read; the PEM-parse fallback covers TLSMaterial values built by
// older code paths or tests that only set CertPEM. Best-effort throughout: a
// parse failure logs a warning and returns "" rather than failing VM
// creation over expiry-metadata extraction — the leaf is already issued and
// serving by the time this runs.
func leafNotAfterRFC3339(tlsMat *artifacts.TLSMaterial) string {
	if tlsMat == nil {
		return ""
	}

	if tlsMat.NotAfter != "" {
		return tlsMat.NotAfter
	}

	if tlsMat.CertPEM == "" {
		return ""
	}

	block, _ := pem.Decode([]byte(tlsMat.CertPEM))
	if block == nil || block.Type != "CERTIFICATE" {
		logger.Warnf("artifacts: leaf cert PEM has no CERTIFICATE block; tls_leaf_not_after will not be recorded")

		return ""
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		logger.Warnf("artifacts: parsing leaf certificate for expiry recording: %v", err)

		return ""
	}

	return cert.NotAfter.UTC().Format(time.RFC3339)
}

// pemOrEmpty returns the cert or key PEM, defaulting to empty when TLS is off.
func pemOrEmpty(mat *artifacts.TLSMaterial, cert bool) string {
	if mat == nil {
		return ""
	}

	if cert {
		return mat.CertPEM
	}

	return mat.KeyPEM
}

// artifactsTags merges base tags with role/bloc tags for discovery.
func artifactsTags(base map[string]string, blocName string) map[string]string {
	if base == nil {
		base = map[string]string{}
	}

	base["ocfp:role"] = "artifacts"
	base["ocfp:bloc"] = blocName

	return base
}

// attachArtifactsDataVolume creates and attaches the bulk data volume.
func (m *Manager) attachArtifactsDataVolume(ctx context.Context, instanceID, vmName string) (*cpi.Volume, error) {
	vol, err := m.provider.StorageManager().CreateVolume(ctx, &cpi.VolumeRequest{
		Name:       vmName + "-data",
		SizeGB:     m.config.Artifacts.Data.DiskSizeGiB,
		Type:       m.config.Artifacts.Data.StoragePool,
		InstanceID: instanceID,
		Tags: map[string]string{
			"ocfp:role": "artifacts-data",
			"ocfp:bloc": m.options.BlocName,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create data volume: %w", err)
	}

	err = m.provider.StorageManager().AttachVolume(ctx, vol.ID, instanceID, artifactsDataDeviceHint)
	if err != nil {
		// Volume created but not attached — clean it up to avoid orphans.
		_ = m.provider.StorageManager().DeleteVolume(ctx, vol.ID)

		return nil, fmt.Errorf("attach data volume: %w", err)
	}

	return vol, nil
}

// cleanupOrphanArtifactsVM is best-effort: a VM whose attach failed cannot
// fulfill its purpose and the operator will re-run bootstrap. Leaving it
// running burns resources and confuses lookups.
func (m *Manager) cleanupOrphanArtifactsVM(ctx context.Context, vmID, vmName string) {
	logger.Warnf("Cleaning up orphan artifacts VM %s (%s) after attach failure", vmName, vmID)

	err := m.provider.ComputeManager().DeleteInstance(ctx, vmID)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "warning: orphan VM %s could not be deleted: %v\n", vmID, err)
	}
}

// waitArtifactsReady polls the RustFS S3 endpoint until ListBuckets succeeds
// or the deadline elapses. RustFS does not expose a dedicated health endpoint;
// ListBuckets is a cheap, idempotent probe that exercises auth + the S3 stack.
func (m *Manager) waitArtifactsReady(ctx context.Context, ep artifacts.Endpoint, creds artifacts.Credentials) error {
	deadline := time.Now().Add(artifactsReadinessTimeout)

	for {
		err := artifacts.Probe(ctx, ep, creds)
		if err == nil {
			return nil
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("%w: %w", artifacts.ErrReadinessTimeout, err)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(artifactsReadinessPoll):
		}
	}
}

// refreshArtifactsCACert re-syncs the recorded ca_cert with the bloc CA
// currently in vault, but ONLY for a VM actually provisioned with
// internal-ca. It gates on the STATE resource's recorded tls_mode — the same
// pattern internal/bastion/phases.go's blocCACertFromState uses — never on
// the current config mode: Phase 6 flipped the config default to
// internal-ca, so an existing bloc that was provisioned self-signed under
// the old default now loads config.Artifacts.TLS.Mode == internal-ca on
// every run even though its VM's ca_cert is the self-signed leaf itself
// (its actual working trust anchor). Gating on config here would mint a
// brand-new bloc CA and silently overwrite that leaf, breaking a working
// deployment the next time vault populate/WriteArtifacts runs. A tls.mode
// migration must be an explicit `ocfp artifacts provision` re-provision,
// never an implicit converge side effect — see the warning below.
//
// The artifacts leaf cert is issued from vault's bloc CA at provision time,
// so after a vault rebuild the CA captured in state goes stale and
// `ocfp vault populate` — which pins the blobstore CA from this state
// entry — writes a CA that no longer verifies the endpoint. That is the
// staleness this refresh actually fixes, for internal-ca VMs only.
func (m *Manager) refreshArtifactsCACert(existing *state.Resource) {
	stateMode, _ := existing.Properties["tls_mode"].(string)

	if stateMode != config.ArtifactsTLSModeInternalCA {
		if m.config.Artifacts.TLS.Mode == config.ArtifactsTLSModeInternalCA {
			logger.Warnf(
				"artifacts: config tls.mode=internal-ca but VM %s was provisioned tls_mode=%q; "+
					"leaving its ca_cert untouched. A mode migration must be an explicit "+
					"`ocfp artifacts provision --bloc %s`, not an implicit bootstrap re-run.",
				existing.Name, stateMode, m.options.BlocName)
		}

		return
	}

	if m.safe == nil {
		return
	}

	ca, err := vault.LoadOrGenerateBlocCA(m.safe, m.options.BlocName)
	if err != nil {
		logger.Warnf("artifacts: refresh bloc CA from vault: %v", err)

		return
	}

	if existing.Properties == nil {
		existing.Properties = map[string]interface{}{}
	}

	if existing.Properties["ca_cert"] == ca.CertPEM {
		return
	}

	existing.Properties["ca_cert"] = ca.CertPEM

	err = m.stateManager.UpdateResource(existing)
	if err != nil {
		logger.Warnf("artifacts: update ca_cert in state: %v", err)

		return
	}

	logger.Infof("Artifacts ca_cert in state refreshed from bloc CA")
}

// convergeExistingArtifacts re-syncs a previously recorded artifacts VM on
// every bootstrap re-run instead of a bare skip, so re-running bootstrap
// heals drift instead of silently trusting stale state:
//
//  1. refreshArtifactsCACert — re-sync ca_cert with vault's bloc CA.
//  2. A short, capped readiness probe — warn (don't fail) if the endpoint is
//     unreachable, since state says the VM should already be live.
//  3. Re-run WriteArtifacts when vault is reachable — idempotent, heals a
//     wiped/re-initialized vault without requiring a separate populate run.
//  4. Re-run EnsureBuckets — idempotent, heals a bucket deleted out of band.
//
// All three steps after the CA refresh are warning-only, matching the
// create-path posture: a transient RustFS restart or unreachable vault must
// never fail an otherwise no-op bootstrap re-run.
func (m *Manager) convergeExistingArtifacts(ctx context.Context, existing *state.Resource) {
	m.refreshArtifactsCACert(existing)

	ep, creds, ok := artifactsEndpointCredsFromState(existing, m.config)
	if !ok {
		logger.Warnf("artifacts: state resource %s missing endpoint/credential properties; skipping skip-path convergence", existing.Name)

		return
	}

	probeCtx, cancel := context.WithTimeout(ctx, artifactsSkipPathProbeTimeout)
	defer cancel()

	err := artifacts.Probe(probeCtx, ep, creds)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr,
			"warning: artifacts VM %s: state says deployed but endpoint unreachable (%v); "+
				"run `ocfp artifacts status` or `ocfp artifacts provision --bloc %s` to investigate\n",
			existing.Name, err, m.options.BlocName)
	}

	if m.safe != nil {
		writer := vault.NewArtifactsWriter(m.config, m.safe, m.options.BlocName)

		tlsMat := artifactsTLSMaterialFromState(existing)

		err = writer.WriteArtifacts(ctx, m.options.BlocName, ep, creds, tlsMat)
		if err != nil {
			logger.Warnf("artifacts: re-sync vault on skip path: %v", err)
		}
	} else {
		logger.Debugf("artifacts: vault unavailable on skip path; skipping WriteArtifacts re-sync")
	}

	err = artifacts.EnsureBuckets(ctx, ep, creds, artifactsBucketList(m.options.BlocName, m.config))
	if err != nil {
		logger.Warnf("artifacts: ensure buckets on skip path: %v", err)
	}
}

// artifactsEndpointCredsFromState rebuilds the Endpoint + Credentials the
// skip path needs to probe/re-sync from a state.Resource's recorded
// properties (the same keys recordArtifactsState writes). Port/region/
// path-style are resolved from config rather than the properties map because
// numeric values round-trip through JSON as float64, and the config already
// holds the authoritative port. Returns ok=false when the minimum required
// properties (endpoint, private_ip, access_key, secret_key) are absent —
// e.g. a hand-edited or partially-written state entry.
func artifactsEndpointCredsFromState(existing *state.Resource, cfg *config.Config) (artifacts.Endpoint, artifacts.Credentials, bool) {
	get := func(key string) string {
		v, _ := existing.Properties[key].(string)

		return v
	}

	endpointURL := get("endpoint")
	host := get("private_ip")
	accessKey := get("access_key")
	secretKey := get("secret_key")

	if endpointURL == "" || host == "" || accessKey == "" || secretKey == "" {
		return artifacts.Endpoint{}, artifacts.Credentials{}, false
	}

	port := cfg.Artifacts.Rustfs.S3Port
	if port == 0 {
		port = artifactsS3Port
	}

	ep := artifacts.Endpoint{
		URL:       endpointURL,
		Host:      host,
		Port:      port,
		Region:    config.BlobstoreDefaultRegion,
		PathStyle: true,
		CACert:    get("ca_cert"),
	}

	creds := artifacts.Credentials{AccessKey: accessKey, SecretKey: secretKey}

	return ep, creds, true
}

// artifactsTLSMaterialFromState recovers the recorded leaf fingerprint and
// expiry (when present) so WriteArtifacts' operational metadata write
// (tls_fingerprint_sha256, tls_leaf_not_after) stays populated on the skip
// path. The leaf's cert/key PEMs are never persisted to state (see
// resolveArtifactsProvisionTLS / CreateArtifacts's tlsMat handling), so only
// Fingerprint and NotAfter can be recovered here — WriteArtifacts only reads
// those two fields from this struct.
func artifactsTLSMaterialFromState(existing *state.Resource) *artifacts.TLSMaterial {
	// tls_fingerprint_sha256 read here is operator/status metadata only (see
	// the vault.ArtifactsWriter doc comment); never used to make a trust
	// decision.
	fp, _ := existing.Properties["tls_fingerprint_sha256"].(string)
	notAfter, _ := existing.Properties["tls_leaf_not_after"].(string)

	if fp == "" && notAfter == "" {
		return nil
	}

	return &artifacts.TLSMaterial{Fingerprint: fp, NotAfter: notAfter}
}

func (m *Manager) recordArtifactsState(
	vmName string,
	inst *cpi.Instance,
	vol *cpi.Volume,
	ip net.IP,
	creds artifacts.Credentials,
	ep artifacts.Endpoint,
	tlsMat *artifacts.TLSMaterial,
	tlsLeafNotAfter string,
) error {
	props := map[string]interface{}{
		"vm_id":                inst.ID,
		"private_ip":           ip.String(),
		"flavor":               m.config.Artifacts.Flavor,
		"image":                m.config.Artifacts.Template,
		"data_volume_id":       vol.ID,
		"data_volume_size_gib": m.config.Artifacts.Data.DiskSizeGiB,
		"filesystem":           m.config.Artifacts.ResolvedFilesystem(),
		"zfs_dataset":          m.config.Artifacts.ResolvedDataset(m.options.BlocName),
		"rustfs_version":       m.config.Artifacts.Rustfs.Version,
		"s3_port":              m.config.Artifacts.Rustfs.S3Port,
		"console_port":         m.config.Artifacts.Rustfs.ConsolePort,
		"endpoint":             ep.URL,
		"tls_mode":             m.config.Artifacts.TLS.Mode,
		"access_key":           creds.AccessKey,
		"secret_key":           creds.SecretKey,
		"ca_cert":              ep.CACert,
	}

	if tlsMat != nil {
		// tls_fingerprint_sha256 is operator/status metadata only (see the
		// vault.ArtifactsWriter doc comment); never used to make a trust
		// decision — TLS clients verify against ca_cert, not this value.
		props["tls_fingerprint_sha256"] = tlsMat.Fingerprint
	}

	if tlsLeafNotAfter != "" {
		// tls_leaf_not_after (RFC3339, task 6.2) lets `ocfp artifacts status`
		// warn on upcoming/passed leaf expiry without a live TLS dial.
		props["tls_leaf_not_after"] = tlsLeafNotAfter
	}

	err := m.stateManager.AddResource(&state.Resource{
		ID:         vmName,
		Type:       artifactsResourceType,
		Name:       vmName,
		Provider:   m.options.Provider,
		State:      "active",
		Properties: props,
	})
	if err != nil {
		return err
	}

	_ = m.stateManager.SetOutput("artifacts_ip", ip.String())
	_ = m.stateManager.SetOutput("artifacts_endpoint", ep.URL)
	_ = m.stateManager.SetOutput("artifacts_vm_id", inst.ID)

	if tlsMat != nil {
		_ = m.stateManager.SetOutput("artifacts_tls_fingerprint", tlsMat.Fingerprint)
	}

	return nil
}

// buildArtifactsEndpoint constructs the Endpoint value for the artifacts VM.
func buildArtifactsEndpoint(ip net.IP, cfg config.ArtifactsConfig, caPEM string) artifacts.Endpoint {
	scheme := "https"

	if cfg.TLS.Mode == config.ArtifactsTLSModeDisabled {
		scheme = "http"
	}

	return artifacts.Endpoint{
		URL:       fmt.Sprintf("%s://%s:%d", scheme, ip.String(), cfg.Rustfs.S3Port),
		Host:      ip.String(),
		Port:      cfg.Rustfs.S3Port,
		Region:    config.BlobstoreDefaultRegion,
		PathStyle: true,
		CACert:    caPEM,
	}
}

// artifactsBucketList enumerates the BOSH + CF buckets to create on the
// artifacts endpoint. Delegates to the canonical list shared with the
// `artifacts provision` command (internal/artifacts.CanonicalBuckets) so the
// two provisioning paths can never drift apart on the bucket roster again.
// _cfg is unused but kept so existing callers/tests do not need to change
// their call shape.
func artifactsBucketList(blocName string, _cfg *config.Config) []artifacts.BucketSpec {
	return artifacts.CanonicalBuckets(blocName)
}
