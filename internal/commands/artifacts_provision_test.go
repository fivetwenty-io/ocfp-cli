package commands

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/artifacts"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

func testArtifactsCfg() *config.Config {
	cfg := &config.Config{Name: "ocfp-lab-wayne"}
	cfg.Artifacts.Enabled = true
	cfg.Artifacts.Rustfs.Version = "1.0.0-beta.3"
	cfg.Artifacts.Rustfs.S3Port = 9000
	cfg.Artifacts.Rustfs.ConsolePort = 9001
	cfg.Artifacts.Data.Mountpoint = "/data"

	return cfg
}

func TestArtifactsProvisionBuckets_FollowsNamingConvention(t *testing.T) {
	t.Parallel()

	got := artifactsProvisionBuckets("ocfp-lab-wayne")
	want := []string{
		"ocfp-lab-wayne-mgmt-bosh",
		"ocfp-lab-wayne-ocf-bosh",
		"ocfp-lab-wayne-ocf-cf-droplets",
		"ocfp-lab-wayne-ocf-cf-packages",
		"ocfp-lab-wayne-ocf-cf-buildpacks",
		"ocfp-lab-wayne-ocf-cf-resource-pool",
	}

	if len(got) != len(want) {
		t.Fatalf("bucket count = %d, want %d", len(got), len(want))
	}

	for i := range want {
		if got[i] != want[i] {
			t.Errorf("bucket[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestBuildArtifactsProvisionEnv_TLSEnabled(t *testing.T) {
	t.Parallel()

	cfg := testArtifactsCfg()
	lr := &artifacts.LookupResult{
		AccessKey:  "AKIAEXAMPLE",
		SecretKey:  "secret/value",
		ZFSDataset: "rpool/ocfp-lab-wayne",
		TLSMode:    config.ArtifactsTLSModeInternalCA,
		PrivateIP:  "10.64.64.11",
	}

	env := buildArtifactsProvisionEnv(cfg, lr, "CERTPEM", "KEYPEM", "CAPEM")

	checks := map[string]string{
		"RUSTFS_ACCESS_KEY":   "AKIAEXAMPLE",
		"RUSTFS_SECRET_KEY":   "secret/value",
		"RUSTFS_S3_PORT":      "9000",
		"RUSTFS_CONSOLE_PORT": "9001",
		"RUSTFS_VOLUMES":      "/data",
		"RUSTFS_ZFS_DATASET":  "rpool/ocfp-lab-wayne",
		"RUSTFS_TLS_ENABLED":  "true",
		"RUSTFS_TLS_CERT":     "CERTPEM",
		"RUSTFS_TLS_KEY":      "KEYPEM",
		"RUSTFS_TLS_CA":       "CAPEM",

		// filesystem is unset in testArtifactsCfg, so the ext4 default applies
		"ARTIFACTS_DATA_FILESYSTEM": "ext4",
	}

	for k, want := range checks {
		if env[k] != want {
			t.Errorf("env[%q] = %q, want %q", k, env[k], want)
		}
	}

	if !strings.Contains(env["RUSTFS_DOWNLOAD_URL"], "1.0.0-beta.3") {
		t.Errorf("RUSTFS_DOWNLOAD_URL = %q, want it to contain the version", env["RUSTFS_DOWNLOAD_URL"])
	}

	if !strings.Contains(env["ARTIFACTS_BUCKETS"], "ocfp-lab-wayne-mgmt-bosh") {
		t.Errorf("ARTIFACTS_BUCKETS = %q, missing expected bucket", env["ARTIFACTS_BUCKETS"])
	}
}

func TestBuildArtifactsProvisionEnv_TLSDisabledOmitsCertKey(t *testing.T) {
	t.Parallel()

	cfg := testArtifactsCfg()
	lr := &artifacts.LookupResult{
		AccessKey: "AK",
		SecretKey: "SK",
		TLSMode:   config.ArtifactsTLSModeDisabled,
		PrivateIP: "10.64.64.11",
	}

	env := buildArtifactsProvisionEnv(cfg, lr, "", "", "")

	if env["RUSTFS_TLS_ENABLED"] != "false" {
		t.Errorf("RUSTFS_TLS_ENABLED = %q, want false", env["RUSTFS_TLS_ENABLED"])
	}

	if _, ok := env["RUSTFS_TLS_CERT"]; ok {
		t.Errorf("RUSTFS_TLS_CERT should be absent when TLS disabled")
	}

	if _, ok := env["RUSTFS_TLS_KEY"]; ok {
		t.Errorf("RUSTFS_TLS_KEY should be absent when TLS disabled")
	}

	if _, ok := env["RUSTFS_TLS_CA"]; ok {
		t.Errorf("RUSTFS_TLS_CA should be absent when no CA is delivered")
	}
}

// TestBuildArtifactsProvisionEnv_CAOmittedWhenEmptyEvenIfTLSEnabled asserts
// RUSTFS_TLS_CA is only ever added when a non-empty ca is supplied, decoupled
// from RUSTFS_TLS_ENABLED (a caller could theoretically have TLS material but
// no resolvable CA — resolveArtifactsProvisionTLS never does this today, but
// the env builder must not assume it never will).
func TestBuildArtifactsProvisionEnv_CAOmittedWhenEmptyEvenIfTLSEnabled(t *testing.T) {
	t.Parallel()

	cfg := testArtifactsCfg()
	lr := &artifacts.LookupResult{AccessKey: "AK", SecretKey: "SK", PrivateIP: "10.64.64.11"}

	env := buildArtifactsProvisionEnv(cfg, lr, "CERTPEM", "KEYPEM", "")

	if _, ok := env["RUSTFS_TLS_CA"]; ok {
		t.Errorf("RUSTFS_TLS_CA should be absent when ca argument is empty")
	}
}

func TestBuildArtifactsProvisionEnv_FilesystemOverride(t *testing.T) {
	t.Parallel()

	cfg := testArtifactsCfg()
	cfg.Artifacts.Data.Filesystem = "ZFS" // case-insensitive
	lr := &artifacts.LookupResult{AccessKey: "AK", SecretKey: "SK", PrivateIP: "10.64.64.11"}

	env := buildArtifactsProvisionEnv(cfg, lr, "", "", "")

	if env["ARTIFACTS_DATA_FILESYSTEM"] != "zfs" {
		t.Errorf("ARTIFACTS_DATA_FILESYSTEM = %q, want zfs", env["ARTIFACTS_DATA_FILESYSTEM"])
	}
}

func TestRenderEnvAssignments_SortedAndQuoteSafe(t *testing.T) {
	t.Parallel()

	out := renderEnvAssignments(map[string]string{
		"B_KEY": "plain",
		"A_KEY": "has'quote",
	})

	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), out)
	}

	if lines[0] != `A_KEY='has'\''quote'` {
		t.Errorf("line 0 = %q, want quote-escaped A_KEY first (sorted)", lines[0])
	}

	if lines[1] != `B_KEY='plain'` {
		t.Errorf("line 1 = %q, want B_KEY='plain'", lines[1])
	}
}

// parseLeafCert decodes a PEM-encoded leaf cert for SAN assertions.
func parseLeafCert(t *testing.T, certPEM string) *x509.Certificate {
	t.Helper()

	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		t.Fatalf("no PEM block found in cert: %q", certPEM)
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parsing leaf certificate: %v", err)
	}

	return cert
}

// TestResolveArtifactsProvisionTLS_SelfSigned_IncludesLoopbackSANs asserts
// the issued self-signed leaf's SAN set carries the VM's private IP plus
// both loopback addresses, so on-VM clients (the provisioning script's own
// health check) can verify without SkipTLSVerify.
func TestResolveArtifactsProvisionTLS_SelfSigned_IncludesLoopbackSANs(t *testing.T) {
	t.Parallel()

	cfg := testArtifactsCfg()
	cfg.Artifacts.TLS.Mode = config.ArtifactsTLSModeSelfSigned

	lr := &artifacts.LookupResult{PrivateIP: "10.108.16.11", TLSMode: config.ArtifactsTLSModeSelfSigned}

	certPEM, keyPEM, caPEM, fingerprint, err := resolveArtifactsProvisionTLS(cfg, lr, "ocfp-lab-wayne")
	if err != nil {
		t.Fatalf("resolveArtifactsProvisionTLS: %v", err)
	}

	if certPEM == "" || keyPEM == "" {
		t.Fatal("expected non-empty cert/key PEM for self-signed mode")
	}

	if fingerprint == "" {
		t.Error("expected non-empty fingerprint for self-signed mode")
	}

	if caPEM != certPEM {
		t.Errorf("caPEM = %q, want it to equal certPEM for self-signed mode (the leaf is its own trust anchor)", caPEM)
	}

	cert := parseLeafCert(t, certPEM)

	wantIPs := []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback, net.ParseIP("10.108.16.11")}
	if len(cert.IPAddresses) != len(wantIPs) {
		t.Fatalf("leaf IP SANs = %v, want %v", cert.IPAddresses, wantIPs)
	}

	for i, want := range wantIPs {
		if !cert.IPAddresses[i].Equal(want) {
			t.Errorf("leaf IP SAN[%d] = %v, want %v", i, cert.IPAddresses[i], want)
		}
	}
}

// TestResolveArtifactsProvisionTLS_Disabled_ReturnsEmpty asserts disabled
// (and unset) TLS mode returns empty cert/key/fingerprint with no error.
func TestResolveArtifactsProvisionTLS_Disabled_ReturnsEmpty(t *testing.T) {
	t.Parallel()

	cfg := testArtifactsCfg()

	for _, mode := range []string{config.ArtifactsTLSModeDisabled, ""} {
		lr := &artifacts.LookupResult{PrivateIP: "10.108.16.11", TLSMode: mode}

		certPEM, keyPEM, caPEM, fingerprint, err := resolveArtifactsProvisionTLS(cfg, lr, "ocfp-lab-wayne")
		if err != nil {
			t.Fatalf("resolveArtifactsProvisionTLS(mode=%q): %v", mode, err)
		}

		if certPEM != "" || keyPEM != "" || caPEM != "" || fingerprint != "" {
			t.Errorf("resolveArtifactsProvisionTLS(mode=%q) = (%q, %q, %q, %q), want all empty", mode, certPEM, keyPEM, caPEM, fingerprint)
		}
	}
}

// TestResolveArtifactsProvisionTLS_UnsupportedMode_Errors asserts an
// unrecognized recorded TLS mode fails loudly instead of silently disabling
// TLS or falling back to a guessed mode.
func TestResolveArtifactsProvisionTLS_UnsupportedMode_Errors(t *testing.T) {
	t.Parallel()

	cfg := testArtifactsCfg()
	lr := &artifacts.LookupResult{PrivateIP: "10.108.16.11", TLSMode: "bogus"}

	_, _, _, _, err := resolveArtifactsProvisionTLS(cfg, lr, "ocfp-lab-wayne")
	if err == nil {
		t.Fatal("expected error for unsupported TLS mode")
	}
}

// newProvisionPinsTestState builds a loaded state.Manager (backed by a temp
// directory, never the operator's real ~/.ocfp state) with a single
// artifacts resource pre-seeded, for updateArtifactsProvisionPins tests.
func newProvisionPinsTestState(t *testing.T, blocName string, props map[string]interface{}) *state.Manager {
	t.Helper()

	sm, err := state.NewManager(filepath.Join(t.TempDir(), ".state"))
	if err != nil {
		t.Fatalf("state.NewManager: %v", err)
	}

	_, err = sm.Load(blocName)
	if err != nil {
		t.Fatalf("sm.Load: %v", err)
	}

	vmName := blocName + "-artifacts"

	err = sm.AddResource(&state.Resource{
		ID:         vmName,
		Type:       artifacts.ResourceType,
		Name:       vmName,
		State:      "active",
		Properties: props,
	})
	if err != nil {
		t.Fatalf("sm.AddResource: %v", err)
	}

	return sm
}

// TestUpdateArtifactsProvisionPins_SelfSigned_UpdatesCACertAndFingerprint
// asserts a self-signed re-provision pins the new leaf as ca_cert (the leaf
// IS the trust anchor for self-signed mode) and refreshes the fingerprint.
// VAULT_ADDR/TOKEN/HOME are redirected so the vault re-sync leg cannot reach
// a real vault (no token + no ~/.saferc ⇒ authenticate() fails locally with
// no network call) — vault sync failure here is expected and non-fatal; the
// state write (asserted below) happens before that leg runs.
func TestUpdateArtifactsProvisionPins_SelfSigned_UpdatesCACertAndFingerprint(t *testing.T) {
	t.Setenv("VAULT_ADDR", "")
	t.Setenv("VAULT_TOKEN", "")
	t.Setenv("HOME", t.TempDir())

	blocName := "ocfp-lab-wayne"

	sm := newProvisionPinsTestState(t, blocName, map[string]interface{}{
		"endpoint":               "https://10.108.16.11:9000",
		"private_ip":             "10.108.16.11",
		"access_key":             "AK",
		"secret_key":             "SK",
		"ca_cert":                "OLD-CERT-PEM",
		"tls_fingerprint_sha256": "old-fingerprint",
	})

	cfg := testArtifactsCfg()
	cfg.Name = blocName

	acx := &artifactsContext{
		parent:   context.Background(),
		blocName: blocName,
		cfg:      cfg,
		state:    sm,
		lookup:   &artifacts.LookupResult{TLSMode: config.ArtifactsTLSModeSelfSigned},
	}

	err := updateArtifactsProvisionPins(acx, "NEW-CERT-PEM", "new-fingerprint")
	if err != nil {
		t.Fatalf("updateArtifactsProvisionPins: %v", err)
	}

	res, err := sm.GetResource(artifacts.ResourceType, blocName+"-artifacts")
	if err != nil {
		t.Fatalf("GetResource: %v", err)
	}

	if res.Properties["ca_cert"] != "NEW-CERT-PEM" {
		t.Errorf("ca_cert = %v, want NEW-CERT-PEM (self-signed leaf is the trust anchor)", res.Properties["ca_cert"])
	}

	if res.Properties["tls_fingerprint_sha256"] != "new-fingerprint" {
		t.Errorf("tls_fingerprint_sha256 = %v, want new-fingerprint", res.Properties["tls_fingerprint_sha256"])
	}
}

// TestUpdateArtifactsProvisionPins_InternalCA_LeavesCACertPinned asserts an
// internal-ca re-provision updates only the fingerprint — the ca_cert pin
// stays anchored to the bloc CA and must never be overwritten with the
// (non-trust-anchor) leaf cert.
func TestUpdateArtifactsProvisionPins_InternalCA_LeavesCACertPinned(t *testing.T) {
	t.Setenv("VAULT_ADDR", "")
	t.Setenv("VAULT_TOKEN", "")
	t.Setenv("HOME", t.TempDir())

	blocName := "ocfp-lab-wayne"

	sm := newProvisionPinsTestState(t, blocName, map[string]interface{}{
		"endpoint":               "https://10.108.16.11:9000",
		"private_ip":             "10.108.16.11",
		"access_key":             "AK",
		"secret_key":             "SK",
		"ca_cert":                "BLOC-CA-CERT-PEM",
		"tls_fingerprint_sha256": "old-fingerprint",
	})

	cfg := testArtifactsCfg()
	cfg.Name = blocName

	acx := &artifactsContext{
		parent:   context.Background(),
		blocName: blocName,
		cfg:      cfg,
		state:    sm,
		lookup:   &artifacts.LookupResult{TLSMode: config.ArtifactsTLSModeInternalCA},
	}

	err := updateArtifactsProvisionPins(acx, "NEW-LEAF-CERT-PEM", "new-fingerprint")
	if err != nil {
		t.Fatalf("updateArtifactsProvisionPins: %v", err)
	}

	res, err := sm.GetResource(artifacts.ResourceType, blocName+"-artifacts")
	if err != nil {
		t.Fatalf("GetResource: %v", err)
	}

	if res.Properties["ca_cert"] != "BLOC-CA-CERT-PEM" {
		t.Errorf("ca_cert = %v, want unchanged BLOC-CA-CERT-PEM (internal-ca pins stay CA-anchored)", res.Properties["ca_cert"])
	}

	if res.Properties["tls_fingerprint_sha256"] != "new-fingerprint" {
		t.Errorf("tls_fingerprint_sha256 = %v, want new-fingerprint", res.Properties["tls_fingerprint_sha256"])
	}
}

// TestUpdateArtifactsProvisionPins_RecordsLeafNotAfter (task 6.2) asserts a
// re-provision records the re-issued leaf's NotAfter (RFC3339) so `ocfp
// artifacts status` can surface expiry without a live TLS dial.
func TestUpdateArtifactsProvisionPins_RecordsLeafNotAfter(t *testing.T) {
	t.Setenv("VAULT_ADDR", "")
	t.Setenv("VAULT_TOKEN", "")
	t.Setenv("HOME", t.TempDir())

	blocName := "ocfp-lab-wayne"

	sm := newProvisionPinsTestState(t, blocName, map[string]interface{}{
		"endpoint":   "https://10.108.16.11:9000",
		"private_ip": "10.108.16.11",
		"access_key": "AK",
		"secret_key": "SK",
	})

	cfg := testArtifactsCfg()
	cfg.Name = blocName

	acx := &artifactsContext{
		parent:   context.Background(),
		blocName: blocName,
		cfg:      cfg,
		state:    sm,
		lookup:   &artifacts.LookupResult{TLSMode: config.ArtifactsTLSModeSelfSigned},
	}

	mat, err := artifacts.GenerateSelfSignedTLS("dev-artifacts", []string{"dev-artifacts"}, nil)
	if err != nil {
		t.Fatalf("GenerateSelfSignedTLS: %v", err)
	}

	wantCert, err := parseCACertPEM(mat.CertPEM)
	if err != nil {
		t.Fatalf("parseCACertPEM: %v", err)
	}

	err = updateArtifactsProvisionPins(acx, mat.CertPEM, mat.Fingerprint)
	if err != nil {
		t.Fatalf("updateArtifactsProvisionPins: %v", err)
	}

	res, err := sm.GetResource(artifacts.ResourceType, blocName+"-artifacts")
	if err != nil {
		t.Fatalf("GetResource: %v", err)
	}

	want := wantCert.NotAfter.UTC().Format(time.RFC3339)
	if res.Properties["tls_leaf_not_after"] != want {
		t.Errorf("tls_leaf_not_after = %v, want %v", res.Properties["tls_leaf_not_after"], want)
	}
}

// TestUpdateArtifactsProvisionPins_UnparsableCertSkipsNotAfterNonFatally
// asserts an unparsable certPEM (e.g. a test fixture placeholder string, or
// a future caller passing malformed material) never fails the pins update —
// tls_leaf_not_after recording is best-effort status metadata, not a
// correctness requirement for the state/vault pin refresh itself.
func TestUpdateArtifactsProvisionPins_UnparsableCertSkipsNotAfterNonFatally(t *testing.T) {
	t.Setenv("VAULT_ADDR", "")
	t.Setenv("VAULT_TOKEN", "")
	t.Setenv("HOME", t.TempDir())

	blocName := "ocfp-lab-wayne"

	sm := newProvisionPinsTestState(t, blocName, map[string]interface{}{
		"endpoint":   "https://10.108.16.11:9000",
		"private_ip": "10.108.16.11",
		"access_key": "AK",
		"secret_key": "SK",
	})

	cfg := testArtifactsCfg()
	cfg.Name = blocName

	acx := &artifactsContext{
		parent:   context.Background(),
		blocName: blocName,
		cfg:      cfg,
		state:    sm,
		lookup:   &artifacts.LookupResult{TLSMode: config.ArtifactsTLSModeSelfSigned},
	}

	err := updateArtifactsProvisionPins(acx, "NOT-A-REAL-PEM", "fingerprint")
	if err != nil {
		t.Fatalf("updateArtifactsProvisionPins must not fail on unparsable cert: %v", err)
	}

	res, err := sm.GetResource(artifacts.ResourceType, blocName+"-artifacts")
	if err != nil {
		t.Fatalf("GetResource: %v", err)
	}

	if _, ok := res.Properties["tls_leaf_not_after"]; ok {
		t.Errorf("tls_leaf_not_after should be absent when the cert PEM could not be parsed, got %v", res.Properties["tls_leaf_not_after"])
	}
}

// TestArtifactsProvisionEndpointCreds_MissingRequiredFieldReturnsNotOK covers
// each individually-missing required property.
func TestArtifactsProvisionEndpointCreds_MissingRequiredFieldReturnsNotOK(t *testing.T) {
	t.Parallel()

	cfg := testArtifactsCfg()

	full := map[string]interface{}{
		"endpoint":   "https://10.108.16.11:9000",
		"private_ip": "10.108.16.11",
		"access_key": "AK",
		"secret_key": "SK",
	}

	for _, missing := range []string{"endpoint", "private_ip", "access_key", "secret_key"} {
		props := map[string]interface{}{}
		for k, v := range full {
			if k != missing {
				props[k] = v
			}
		}

		_, _, ok := artifactsProvisionEndpointCreds(&state.Resource{Properties: props}, cfg)
		if ok {
			t.Errorf("expected ok=false with %q missing", missing)
		}
	}

	ep, creds, ok := artifactsProvisionEndpointCreds(&state.Resource{Properties: full}, cfg)
	if !ok {
		t.Fatal("expected ok=true when all required properties are present")
	}

	if ep.URL != full["endpoint"] || creds.AccessKey != "AK" || creds.SecretKey != "SK" {
		t.Errorf("unexpected endpoint/creds: %+v / %+v", ep, creds)
	}
}
