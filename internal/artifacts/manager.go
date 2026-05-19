package artifacts

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	pveclient "github.com/ocfp/ocfp-cli-go/internal/cpi/pve"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

const (
	// providerNamePVE is the provider value required for artifacts in v1.
	providerNamePVE = "pve"

	// resourceType is the state.Resource Type for the artifacts VM.
	resourceType = "artifacts"

	// dataDeviceHint is the cloud-init-side device path we expect the attached
	// volume to land on. PVE's qemu-guest-agent typically presents the second
	// virtio disk as /dev/sdb (or /dev/vdb depending on bus). The cloud-init
	// runcmd in cloudinit_artifacts.go uses /dev/sdb explicitly.
	dataDeviceHint = "/dev/sdb"

	// readinessProbeInterval is how often the readiness loop ticks.
	readinessProbeInterval = 5 * time.Second

	// readinessProbeTimeout is the upper bound on waiting for RustFS to answer.
	readinessProbeTimeout = 10 * time.Minute
)

// VaultWriter persists artifacts blobstore credentials and metadata to vault.
// Implemented by internal/vault. Decoupled here so the artifacts package has
// no direct dependency on the vault client (avoids import cycles + simplifies tests).
type VaultWriter interface {
	WriteArtifacts(ctx context.Context, blocName string, ep Endpoint, creds Credentials, tls *TLSMaterial) error
}

// ReadinessProbe verifies the RustFS S3 endpoint is responding to ListBuckets.
// Production implementation hits the endpoint via the same AWS SDK v2 client
// used for bucket creation; tests substitute a stub.
type ReadinessProbe interface {
	Probe(ctx context.Context, ep Endpoint, creds Credentials, caPEM string) error
}

// Endpoint is the resolved RustFS S3 endpoint for the artifacts VM. URL is the
// full https://host:port (or http:// when TLS is disabled). PathStyle is always
// true for RustFS. Region is "us-east-1" (SigV4 needs a value; unused by RustFS).
type Endpoint struct {
	URL       string
	Host      string
	Port      int
	Region    string
	PathStyle bool
	CACert    string // empty when TLS is disabled
}

// Manager orchestrates artifacts VM creation, lookup, and lifecycle.
type Manager struct {
	cfg      *config.Config
	provider cpi.Provider
	state    *state.Manager
	vault    VaultWriter
	ready    ReadinessProbe
	log      logger.Logger
}

// NewManager constructs a Manager. provider must be the PVE CPI provider.
// vault and ready may be nil; in that case state-only paths still work.
func NewManager(cfg *config.Config, p cpi.Provider, sm *state.Manager, vault VaultWriter, ready ReadinessProbe) *Manager {
	return &Manager{
		cfg:      cfg,
		provider: p,
		state:    sm,
		vault:    vault,
		ready:    ready,
		log:      logger.WithOperation("artifacts"),
	}
}

// CreateArtifacts provisions the ocfp-artifacts VM when the feature is opted
// in. The flow is:
//
//  1. Validate preconditions (provider, bloc name, subnet, feature flag).
//  2. Resolve identity (name, IP, credentials, TLS material).
//  3. Render cloud-init.
//  4. Create the VM.
//  5. Create + attach the data volume.
//  6. Wait for RustFS readiness.
//  7. Record state.
//  8. Write vault.
//
// The function is idempotent: if state already records an artifacts VM and
// the readiness probe succeeds, it returns nil without re-provisioning.
func (m *Manager) CreateArtifacts(ctx context.Context, blocName, subnetCIDR string, ipAt func(slot int) (net.IP, error), artifactsIPSlot int) error {
	if !m.cfg.Artifacts.Enabled {
		return ErrDisabled
	}

	if blocName == "" {
		return ErrBlocNameRequired
	}

	if subnetCIDR == "" {
		return ErrSubnetRequired
	}

	if !strings.EqualFold(m.cfg.Provider, providerNamePVE) {
		return ErrProviderUnsupported
	}

	ip, err := ipAt(artifactsIPSlot)
	if err != nil {
		return fmt.Errorf("computing artifacts IP: %w", err)
	}

	vmName := fmt.Sprintf("%s-artifacts", blocName)

	if existing, _ := m.state.GetResource(resourceType, vmName); existing != nil {
		m.log.Infof("artifacts VM %s already recorded in state; verifying readiness", vmName)

		ep := endpointFromResource(existing, m.cfg.Artifacts)
		creds := credsFromResource(existing)

		if m.ready != nil {
			err := m.ready.Probe(ctx, ep, creds, ep.CACert)
			if err == nil {
				return nil
			}

			m.log.Warnf("existing artifacts VM not responding; re-provisioning is currently manual: %v", err)

			return fmt.Errorf("existing artifacts VM in state but not responsive: %w", err)
		}

		return nil
	}

	creds, err := ResolveCredentials(Credentials{
		AccessKey: m.cfg.Artifacts.Rustfs.AccessKey,
		SecretKey: m.cfg.Artifacts.Rustfs.SecretKey,
	})
	if err != nil {
		return fmt.Errorf("resolving RustFS credentials: %w", err)
	}

	var (
		tls       *TLSMaterial
		tlsCertCA string
	)

	if m.cfg.Artifacts.TLS.Mode == config.ArtifactsTLSModeSelfSigned {
		commonName := m.cfg.Artifacts.TLS.CommonName
		if commonName == "" {
			commonName = vmName
		}

		mat, err := GenerateSelfSignedTLS(commonName, []string{commonName, vmName}, []net.IP{ip})
		if err != nil {
			return fmt.Errorf("generating self-signed TLS: %w", err)
		}

		tls = &mat
		tlsCertCA = mat.CertPEM
	}

	if m.cfg.Artifacts.TLS.Mode == config.ArtifactsTLSModeInternalCA {
		// TODO: wire bloc internal CA. For now, refuse rather than silently fall back.
		return fmt.Errorf("internal-ca TLS mode is not yet wired; set artifacts.tls.mode to self-signed or disabled")
	}

	userData, err := pveclient.RenderArtifactsCloudInit(pveclient.ArtifactsCloudInitInputs{
		AccessKey:   creds.AccessKey,
		SecretKey:   creds.SecretKey,
		DownloadURL: m.cfg.Artifacts.ResolvedDownloadURL(),
		S3Port:      m.cfg.Artifacts.Rustfs.S3Port,
		ConsolePort: m.cfg.Artifacts.Rustfs.ConsolePort,
		Mountpoint:  m.cfg.Artifacts.Data.Mountpoint,
		ZFSDataset:  m.cfg.Artifacts.ResolvedDataset(blocName),
		TLSEnabled:  tls != nil,
		CertPEM: func() string {
			if tls == nil {
				return ""
			}
			return tls.CertPEM
		}(),
		KeyPEM: func() string {
			if tls == nil {
				return ""
			}
			return tls.KeyPEM
		}(),
	})
	if err != nil {
		return fmt.Errorf("rendering artifacts cloud-init: %w", err)
	}

	inst, err := m.provider.ComputeManager().CreateInstance(ctx, &cpi.InstanceRequest{
		Name:            vmName,
		Flavor:          m.cfg.Artifacts.Flavor,
		Image:           m.cfg.Artifacts.Template,
		StaticPrivateIP: ip.String(),
		UserData:        userData,
		Tags: map[string]string{
			"ocfp:role": "artifacts",
			"ocfp:bloc": blocName,
		},
	})
	if err != nil {
		return fmt.Errorf("creating artifacts VM: %w", err)
	}

	vol, err := m.provider.StorageManager().CreateVolume(ctx, &cpi.VolumeRequest{
		Name:   vmName + "-data",
		SizeGB: m.cfg.Artifacts.Data.DiskSizeGiB,
		Tags: map[string]string{
			"ocfp:role": "artifacts-data",
			"ocfp:bloc": blocName,
		},
	})
	if err != nil {
		return fmt.Errorf("creating artifacts data volume: %w", err)
	}

	if err := m.provider.StorageManager().AttachVolume(ctx, vol.ID, inst.ID, dataDeviceHint); err != nil {
		return fmt.Errorf("attaching artifacts data volume: %w", err)
	}

	ep := buildEndpoint(ip, m.cfg.Artifacts, tlsCertCA)

	if m.ready != nil {
		if err := m.waitReady(ctx, ep, creds, tlsCertCA); err != nil {
			return err
		}
	}

	if err := m.recordState(blocName, vmName, inst, vol, ip, creds, ep, tls); err != nil {
		return fmt.Errorf("recording artifacts state: %w", err)
	}

	if m.vault != nil {
		if err := m.vault.WriteArtifacts(ctx, blocName, ep, creds, tls); err != nil {
			return fmt.Errorf("writing artifacts vault entries: %w", err)
		}
	}

	return nil
}

// waitReady polls the readiness probe until it succeeds or the deadline elapses.
func (m *Manager) waitReady(ctx context.Context, ep Endpoint, creds Credentials, caPEM string) error {
	deadline := time.Now().Add(readinessProbeTimeout)

	for {
		err := m.ready.Probe(ctx, ep, creds, caPEM)
		if err == nil {
			return nil
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("%w: last error %v", ErrReadinessTimeout, err)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(readinessProbeInterval):
		}
	}
}

func (m *Manager) recordState(
	blocName, vmName string,
	inst *cpi.Instance,
	vol *cpi.Volume,
	ip net.IP,
	creds Credentials,
	ep Endpoint,
	tls *TLSMaterial,
) error {
	props := map[string]interface{}{
		"vm_id":                inst.ID,
		"private_ip":           ip.String(),
		"flavor":               m.cfg.Artifacts.Flavor,
		"image":                m.cfg.Artifacts.Template,
		"data_volume_id":       vol.ID,
		"data_volume_size_gib": m.cfg.Artifacts.Data.DiskSizeGiB,
		"zfs_dataset":          m.cfg.Artifacts.ResolvedDataset(blocName),
		"rustfs_version":       m.cfg.Artifacts.Rustfs.Version,
		"s3_port":              m.cfg.Artifacts.Rustfs.S3Port,
		"console_port":         m.cfg.Artifacts.Rustfs.ConsolePort,
		"endpoint":             ep.URL,
		"tls_mode":             m.cfg.Artifacts.TLS.Mode,
		"access_key":           creds.AccessKey,
		"secret_key":           creds.SecretKey,
	}

	if tls != nil {
		props["tls_fingerprint_sha256"] = tls.Fingerprint
	}

	err := m.state.AddResource(&state.Resource{
		ID:         vmName,
		Type:       resourceType,
		Name:       vmName,
		Provider:   m.cfg.Provider,
		State:      "active",
		Properties: props,
	})
	if err != nil {
		return err
	}

	_ = m.state.SetOutput("artifacts_ip", ip.String())
	_ = m.state.SetOutput("artifacts_endpoint", ep.URL)
	_ = m.state.SetOutput("artifacts_vm_id", inst.ID)

	if tls != nil {
		_ = m.state.SetOutput("artifacts_tls_fingerprint", tls.Fingerprint)
	}

	return nil
}

// buildEndpoint constructs the Endpoint value from the resolved IP and config.
func buildEndpoint(ip net.IP, cfg config.ArtifactsConfig, caPEM string) Endpoint {
	scheme := "http"

	if cfg.TLS.Mode != config.ArtifactsTLSModeDisabled {
		scheme = "https"
	}

	url := fmt.Sprintf("%s://%s:%d", scheme, ip.String(), cfg.Rustfs.S3Port)

	return Endpoint{
		URL:       url,
		Host:      ip.String(),
		Port:      cfg.Rustfs.S3Port,
		Region:    config.BlobstoreDefaultRegion,
		PathStyle: true,
		CACert:    caPEM,
	}
}

// endpointFromResource reconstructs an Endpoint from a recorded state.Resource.
// Used by idempotent re-runs of CreateArtifacts.
func endpointFromResource(r *state.Resource, cfg config.ArtifactsConfig) Endpoint {
	getString := func(k string) string {
		v, ok := r.Properties[k].(string)
		if !ok {
			return ""
		}
		return v
	}

	return Endpoint{
		URL:       getString("endpoint"),
		Host:      getString("private_ip"),
		Port:      cfg.Rustfs.S3Port,
		Region:    config.BlobstoreDefaultRegion,
		PathStyle: true,
		CACert:    getString("ca_cert"),
	}
}

func credsFromResource(r *state.Resource) Credentials {
	get := func(k string) string {
		v, ok := r.Properties[k].(string)
		if !ok {
			return ""
		}
		return v
	}

	return Credentials{AccessKey: get("access_key"), SecretKey: get("secret_key")}
}
