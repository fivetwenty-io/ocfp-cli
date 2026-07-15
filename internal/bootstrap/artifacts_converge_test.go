package bootstrap

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/state"
	"github.com/ocfp/ocfp-cli-go/internal/vault"
)

// fakeConvergeSafe is a recording SafeInterface stub: SetMultiple captures
// every path+body write so tests can assert WriteArtifacts ran during
// skip-path convergence without a real vault. Every other method is a no-op
// returning zero values — convergeExistingArtifacts only calls SetMultiple
// (via vault.ArtifactsWriter) on this stub.
type fakeConvergeSafe struct {
	writes map[string]map[string]interface{}
}

func newFakeConvergeSafe() *fakeConvergeSafe {
	return &fakeConvergeSafe{writes: map[string]map[string]interface{}{}}
}

func (f *fakeConvergeSafe) Set(string, string, interface{}) error { return nil }

func (f *fakeConvergeSafe) SetMultiple(path string, body map[string]interface{}) error {
	f.writes[path] = body

	return nil
}

func (f *fakeConvergeSafe) Get(string, string) (interface{}, error)         { return nil, nil }
func (f *fakeConvergeSafe) GetAll(string) (map[string]interface{}, error)   { return nil, nil }
func (f *fakeConvergeSafe) Exists(string) (bool, error)                     { return false, nil }
func (f *fakeConvergeSafe) Delete(string, string) error                     { return nil }
func (f *fakeConvergeSafe) List(string) ([]string, error)                   { return nil, nil }
func (f *fakeConvergeSafe) Export(string) (map[string]interface{}, error)   { return nil, nil }
func (f *fakeConvergeSafe) Import(string, map[string]interface{}) error     { return nil }
func (f *fakeConvergeSafe) GetEngineInfo(string) (*vault.EngineInfo, error) { return nil, nil }
func (f *fakeConvergeSafe) MustGet(string, string) interface{}              { return nil }
func (f *fakeConvergeSafe) GetString(string, string) (string, error)        { return "", nil }
func (f *fakeConvergeSafe) GetJSON(string, string) ([]byte, error)          { return nil, nil }

// fakeArtifactsS3Server answers just enough of the S3 API (ListBuckets via
// GET /, CreateBucket via PUT /{bucket}) for artifacts.Probe and
// artifacts.EnsureBuckets to succeed against a real HTTP round trip, without
// standing up RustFS. Request signing/auth is not verified — this is a
// convergence-plumbing test, not an S3-protocol test.
func fakeArtifactsS3Server(t *testing.T) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/xml")
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><ListAllMyBucketsResult><Buckets></Buckets></ListAllMyBucketsResult>`)
		default:
			w.WriteHeader(http.StatusOK)
		}
	})

	return httptest.NewServer(mux)
}

// newConvergeTestManager builds a Manager with a loaded state.Manager and no
// compute provider (convergeExistingArtifacts never touches the provider).
func newConvergeTestManager(t *testing.T, blocName string, cfg *config.Config) *Manager {
	t.Helper()

	tmp := t.TempDir()

	sm, err := state.NewManager(filepath.Join(tmp, ".state"))
	if err != nil {
		t.Fatalf("state.NewManager: %v", err)
	}

	_, err = sm.Load(blocName)
	if err != nil {
		t.Fatalf("sm.Load: %v", err)
	}

	return NewManager(cfg, nil, sm, &Options{BlocName: blocName})
}

// TestRefreshArtifactsCACert_StateSelfSigned_ConfigInternalCA_LeavesCACertPinned
// reproduces the Phase 6 default-flip hazard: an existing bloc was
// provisioned tls_mode=self-signed (ca_cert = the leaf, its actual trust
// anchor), but config now loads tls.mode=internal-ca (the new default
// applies to every load, not just fresh bootstraps). convergeExistingArtifacts
// must leave ca_cert untouched and must NOT mint/write a bloc CA to vault —
// gating on config here would silently replace a working self-signed
// deployment's trust anchor with an unrelated freshly-minted CA. A mode
// migration must be an explicit `ocfp artifacts provision` re-provision.
//
// This intentionally does not assert on the mismatch warning's log content:
// internal/logger is a process-wide singleton with no capture hook, and
// package tests run with t.Parallel() elsewhere — any attempt to
// re-point/read the global logger here raced with concurrently-running
// (and orphaned-background-goroutine) log writes from unrelated tests. The
// behavioral assertions below (ca_cert unchanged, no vault CA write) are the
// load-bearing regression pins for this bug; the warning call site itself is
// covered by reading internal/bootstrap/artifacts.go directly in review.
func TestRefreshArtifactsCACert_StateSelfSigned_ConfigInternalCA_LeavesCACertPinned(t *testing.T) {
	srv := fakeArtifactsS3Server(t)
	defer srv.Close()

	cfg := &config.Config{Name: "prod"}
	cfg.Artifacts.TLS.Mode = config.ArtifactsTLSModeInternalCA // the new Phase 6 default
	cfg.Artifacts.Rustfs.S3Port = 9000

	m := newConvergeTestManager(t, "prod", cfg)

	fakeSafe := newFakeConvergeSafe()
	m.SetSafe(fakeSafe)

	const originalLeafCert = "SELF-SIGNED-LEAF-CERT-PEM"

	existing := &state.Resource{
		ID:   "prod-artifacts",
		Type: artifactsResourceType,
		Name: "prod-artifacts",
		Properties: map[string]interface{}{
			"endpoint":               srv.URL,
			"private_ip":             "10.64.64.11",
			"access_key":             "AK",
			"secret_key":             "SK",
			"tls_mode":               config.ArtifactsTLSModeSelfSigned, // VM was actually provisioned self-signed
			"ca_cert":                originalLeafCert,
			"tls_fingerprint_sha256": "leaf-fingerprint",
		},
	}

	m.convergeExistingArtifacts(context.Background(), existing)

	if existing.Properties["ca_cert"] != originalLeafCert {
		t.Errorf("ca_cert = %v, want unchanged %q (self-signed leaf must stay the trust anchor)",
			existing.Properties["ca_cert"], originalLeafCert)
	}

	caVaultPath := "secret/ocfp/prod/ca"
	if _, wrote := fakeSafe.writes[caVaultPath]; wrote {
		t.Errorf("expected no bloc CA write at %s; convergence must never mint a CA for a self-signed VM", caVaultPath)
	}
}

// TestConvergeExistingArtifacts_ReSyncsVaultAndEnsuresBuckets asserts the
// skip path re-runs WriteArtifacts (heals a wiped/re-inited vault) and
// EnsureBuckets (heals a bucket deleted out of band) against a pre-seeded
// state resource, recovering the fingerprint metadata purely from state
// (no TLSMaterial cert/key is ever persisted).
func TestConvergeExistingArtifacts_ReSyncsVaultAndEnsuresBuckets(t *testing.T) {
	t.Parallel()

	srv := fakeArtifactsS3Server(t)
	defer srv.Close()

	cfg := &config.Config{Name: "prod"}
	cfg.Artifacts.TLS.Mode = config.ArtifactsTLSModeDisabled
	cfg.Artifacts.Rustfs.S3Port = 9000

	m := newConvergeTestManager(t, "prod", cfg)

	fakeSafe := newFakeConvergeSafe()
	m.SetSafe(fakeSafe)

	existing := &state.Resource{
		ID:   "prod-artifacts",
		Type: artifactsResourceType,
		Name: "prod-artifacts",
		Properties: map[string]interface{}{
			"endpoint":               srv.URL,
			"private_ip":             "10.64.64.11",
			"access_key":             "AK",
			"secret_key":             "SK",
			"tls_mode":               config.ArtifactsTLSModeDisabled,
			"tls_fingerprint_sha256": "deadbeef",
		},
	}

	m.convergeExistingArtifacts(context.Background(), existing)

	if len(fakeSafe.writes) == 0 {
		t.Fatal("expected WriteArtifacts to write at least one vault path during skip-path convergence")
	}

	metaPath := "secret/ocfp/prod/artifacts"

	meta, ok := fakeSafe.writes[metaPath]
	if !ok {
		t.Fatalf("expected vault metadata write at %s, got paths: %v", metaPath, mapKeys(fakeSafe.writes))
	}

	if meta["tls_fingerprint_sha256"] != "deadbeef" {
		t.Errorf("meta tls_fingerprint_sha256 = %v, want %q (recovered from state)", meta["tls_fingerprint_sha256"], "deadbeef")
	}
}

// TestConvergeExistingArtifacts_MissingPropertiesSkipsWithoutPanic asserts a
// state resource missing the minimum required properties (e.g. hand-edited)
// is skipped gracefully — no panic, no vault write attempted.
func TestConvergeExistingArtifacts_MissingPropertiesSkipsWithoutPanic(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{Name: "prod"}
	cfg.Artifacts.TLS.Mode = config.ArtifactsTLSModeDisabled

	m := newConvergeTestManager(t, "prod", cfg)

	fakeSafe := newFakeConvergeSafe()
	m.SetSafe(fakeSafe)

	existing := &state.Resource{
		ID:         "prod-artifacts",
		Type:       artifactsResourceType,
		Name:       "prod-artifacts",
		Properties: map[string]interface{}{"endpoint": "https://10.64.64.11:9000"}, // missing private_ip/creds
	}

	m.convergeExistingArtifacts(context.Background(), existing)

	if len(fakeSafe.writes) != 0 {
		t.Errorf("expected no vault writes when required state properties are missing, got: %v", mapKeys(fakeSafe.writes))
	}
}

// TestArtifactsEndpointCredsFromState_MissingRequiredFieldReturnsNotOK covers
// each individually-missing required property.
func TestArtifactsEndpointCredsFromState_MissingRequiredFieldReturnsNotOK(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	cfg.Artifacts.Rustfs.S3Port = 9000

	full := map[string]interface{}{
		"endpoint":   "https://10.64.64.11:9000",
		"private_ip": "10.64.64.11",
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

		_, _, ok := artifactsEndpointCredsFromState(&state.Resource{Properties: props}, cfg)
		if ok {
			t.Errorf("expected ok=false with %q missing", missing)
		}
	}

	_, _, ok := artifactsEndpointCredsFromState(&state.Resource{Properties: full}, cfg)
	if !ok {
		t.Error("expected ok=true when all required properties are present")
	}
}

// TestArtifactsTLSMaterialFromState_NoFingerprintOrNotAfterReturnsNil asserts
// the helper returns nil (not a zero-value struct) when neither fingerprint
// nor expiry was recorded, so WriteArtifacts' `if tls != nil` branch is
// skipped cleanly.
func TestArtifactsTLSMaterialFromState_NoFingerprintOrNotAfterReturnsNil(t *testing.T) {
	t.Parallel()

	got := artifactsTLSMaterialFromState(&state.Resource{Properties: map[string]interface{}{}})
	if got != nil {
		t.Errorf("expected nil TLSMaterial when no fingerprint/not_after recorded, got %+v", got)
	}
}

// TestArtifactsTLSMaterialFromState_RecoversNotAfter (task 6.2 gap 1) asserts
// the recorded tls_leaf_not_after state property is threaded through the
// recovered TLSMaterial, so the skip-path WriteArtifacts re-sync keeps the
// vault meta's tls_leaf_not_after fresh even when only expiry (not
// fingerprint) was recorded.
func TestArtifactsTLSMaterialFromState_RecoversNotAfter(t *testing.T) {
	t.Parallel()

	got := artifactsTLSMaterialFromState(&state.Resource{Properties: map[string]interface{}{
		"tls_leaf_not_after": "2027-01-02T03:04:05Z",
	}})
	if got == nil {
		t.Fatal("expected non-nil TLSMaterial when tls_leaf_not_after is recorded")
	}

	if got.NotAfter != "2027-01-02T03:04:05Z" {
		t.Errorf("NotAfter = %q, want 2027-01-02T03:04:05Z", got.NotAfter)
	}

	if got.Fingerprint != "" {
		t.Errorf("Fingerprint = %q, want empty (not recorded)", got.Fingerprint)
	}
}

// TestArtifactsTLSMaterialFromState_RecoversBothFields asserts both
// fingerprint and expiry round-trip together when both are recorded.
func TestArtifactsTLSMaterialFromState_RecoversBothFields(t *testing.T) {
	t.Parallel()

	got := artifactsTLSMaterialFromState(&state.Resource{Properties: map[string]interface{}{
		"tls_fingerprint_sha256": "abc123",
		"tls_leaf_not_after":     "2027-01-02T03:04:05Z",
	}})
	if got == nil {
		t.Fatal("expected non-nil TLSMaterial")
	}

	if got.Fingerprint != "abc123" {
		t.Errorf("Fingerprint = %q, want abc123", got.Fingerprint)
	}

	if got.NotAfter != "2027-01-02T03:04:05Z" {
		t.Errorf("NotAfter = %q, want 2027-01-02T03:04:05Z", got.NotAfter)
	}
}

func mapKeys(m map[string]map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	return keys
}
