package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/artifacts"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	pveclient "github.com/ocfp/ocfp-cli-go/internal/cpi/pve"
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
)

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
func (m *Manager) CreateArtifacts(ctx context.Context) error {
	if !m.config.Artifacts.Enabled {
		_, _ = fmt.Fprintf(os.Stdout, "    • artifacts feature disabled in config; skipping\n")

		return nil
	}

	vmName := m.options.BlocName + "-artifacts"

	if existing, _ := m.stateManager.GetResource(artifactsResourceType, vmName); existing != nil {
		_, _ = fmt.Fprintf(os.Stdout, "    • Artifacts VM %s already in state; skipping\n", vmName)
		logger.Infof("Artifacts VM %s already recorded; skipping", vmName)

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

		mat, err := artifacts.GenerateSelfSignedTLS(cn, []string{cn, vmName}, []net.IP{ip})
		if err != nil {
			return fmt.Errorf("artifacts: generate self-signed TLS: %w", err)
		}

		tlsMat = &mat
		caPEM = mat.CertPEM
	case config.ArtifactsTLSModeInternalCA:
		if m.safe == nil {
			return errors.New("artifacts: internal-ca TLS mode requires vault access; set OCFP_VAULT_ADDR/TOKEN or switch artifacts.tls.mode to self-signed/disabled")
		}

		ca, caErr := vault.LoadOrGenerateBlocCA(m.safe, m.options.BlocName)
		if caErr != nil {
			return fmt.Errorf("artifacts: load bloc CA: %w", caErr)
		}

		cn := m.config.Artifacts.TLS.CommonName
		if cn == "" {
			cn = vmName
		}

		leaf, leafErr := artifacts.IssueLeafCert(ca, cn, []string{cn, vmName}, []net.IP{ip})
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

	ciInputs := pveclient.ArtifactsCloudInitInputs{
		AccessKey:   creds.AccessKey,
		SecretKey:   creds.SecretKey,
		DownloadURL: m.config.Artifacts.ResolvedDownloadURL(),
		S3Port:      m.config.Artifacts.Rustfs.S3Port,
		ConsolePort: m.config.Artifacts.Rustfs.ConsolePort,
		Mountpoint:  m.config.Artifacts.Data.Mountpoint,
		ZFSDataset:  m.config.Artifacts.ResolvedDataset(m.options.BlocName),
		TLSEnabled:  tlsMat != nil,
		CertPEM:     pemOrEmpty(tlsMat, true),
		KeyPEM:      pemOrEmpty(tlsMat, false),
	}

	// cloud-init user-data is retained for providers whose snippet/user-data
	// delivery works. On PVE 9.x the snippet-upload API is blocked, so the
	// identical provisioning is delivered over SSH after boot (see below).
	userData, err := pveclient.RenderArtifactsCloudInit(ciInputs)
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

	// PVE 9.x drops cloud-init user-data (snippet upload is blocked), so deliver
	// the identical RustFS provisioning over SSH, hopping through the bastion.
	if strings.EqualFold(m.options.Provider, "pve") {
		err := m.provisionArtifactsViaSSH(ctx, ciInputs, ipStr)
		if err != nil {
			// Non-fatal: leave the VM for triage and let the readiness probe
			// below surface the resulting unhealthy state. The script is
			// idempotent, so a re-run (after clearing state) retries cleanly.
			_, _ = fmt.Fprintf(os.Stderr, "warning: artifacts SSH provisioning: %v\n", err)
		}
	}

	ep := buildArtifactsEndpoint(ip, m.config.Artifacts, caPEM)

	err = m.waitArtifactsReady(ctx, ep, creds)
	if err != nil {
		// Don't delete the VM here — operator may want to triage it. Surface
		// the timeout but leave the VM in place so `ocfp artifacts ssh` works.
		_, _ = fmt.Fprintf(os.Stderr, "warning: %v — VM left running for triage\n", err)
	}

	err = m.recordArtifactsState(vmName, inst, vol, ip, creds, ep, tlsMat)
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

func (m *Manager) recordArtifactsState(
	vmName string,
	inst *cpi.Instance,
	vol *cpi.Volume,
	ip net.IP,
	creds artifacts.Credentials,
	ep artifacts.Endpoint,
	tlsMat *artifacts.TLSMaterial,
) error {
	props := map[string]interface{}{
		"vm_id":                inst.ID,
		"private_ip":           ip.String(),
		"flavor":               m.config.Artifacts.Flavor,
		"image":                m.config.Artifacts.Template,
		"data_volume_id":       vol.ID,
		"data_volume_size_gib": m.config.Artifacts.Data.DiskSizeGiB,
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
		props["tls_fingerprint_sha256"] = tlsMat.Fingerprint
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
// artifacts endpoint. Names follow the existing {bloc}-{env}-{type} convention.
//
// One bucket per BOSH director (mgmt and ocf/env), and four buckets for the
// CF cloud-controller blobstore (droplets, packages, buildpacks, resource-pool).
func artifactsBucketList(blocName string, _cfg *config.Config) []artifacts.BucketSpec {
	return []artifacts.BucketSpec{
		{Name: blocName + "-mgmt-bosh"},
		{Name: blocName + "-ocf-bosh"},
		{Name: blocName + "-ocf-cf-droplets"},
		{Name: blocName + "-ocf-cf-packages"},
		{Name: blocName + "-ocf-cf-buildpacks"},
		{Name: blocName + "-ocf-cf-resource-pool"},
	}
}
