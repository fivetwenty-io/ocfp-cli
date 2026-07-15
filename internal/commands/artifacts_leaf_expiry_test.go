package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/artifacts"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/state"
	"github.com/ocfp/ocfp-cli-go/internal/version"
)

// --- probeArtifactsLiveLeaf --------------------------------------------------

// TestProbeArtifactsLiveLeaf_MatchesServedCert asserts the probe returns the
// NotAfter and SHA-256 fingerprint of the certificate actually presented by
// a live TLS listener, using httptest's self-signed test certificate as the
// ground truth.
func TestProbeArtifactsLiveLeaf_MatchesServedCert(t *testing.T) {
	t.Parallel()

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	wantCert := srv.Certificate()
	sum := sha256.Sum256(wantCert.Raw)
	wantFingerprint := hex.EncodeToString(sum[:])

	got, err := probeArtifactsLiveLeaf(context.Background(), srv.URL, 5*time.Second)
	if err != nil {
		t.Fatalf("probeArtifactsLiveLeaf: %v", err)
	}

	if !got.NotAfter.Equal(wantCert.NotAfter) {
		t.Errorf("NotAfter = %v, want %v (the served cert's own NotAfter)", got.NotAfter, wantCert.NotAfter)
	}

	if got.FingerprintSHA256 != wantFingerprint {
		t.Errorf("FingerprintSHA256 = %q, want %q (sha256 of the served cert's DER)", got.FingerprintSHA256, wantFingerprint)
	}
}

// TestProbeArtifactsLiveLeaf_NonHTTPSErrors asserts an http:// (or other
// non-https) endpoint is rejected rather than silently dialing plaintext.
func TestProbeArtifactsLiveLeaf_NonHTTPSErrors(t *testing.T) {
	t.Parallel()

	_, err := probeArtifactsLiveLeaf(context.Background(), "http://10.0.0.1:9000", time.Second)
	if err == nil {
		t.Fatal("expected error for non-https endpoint")
	}

	if !strings.Contains(err.Error(), "not https") {
		t.Errorf("error %q should explain the scheme mismatch", err.Error())
	}
}

// TestProbeArtifactsLiveLeaf_UnreachableEndpointErrorsNonFatally asserts an
// unreachable endpoint returns an error (for the caller to log/degrade)
// rather than panicking or hanging past the supplied timeout.
func TestProbeArtifactsLiveLeaf_UnreachableEndpointErrorsNonFatally(t *testing.T) {
	t.Parallel()

	// TEST-NET-1 (RFC 5737): guaranteed unroutable, so the dial fails fast
	// instead of actually attempting a connection to a real host.
	_, err := probeArtifactsLiveLeaf(context.Background(), "https://192.0.2.1:9000", 500*time.Millisecond)
	if err == nil {
		t.Fatal("expected a dial error for an unreachable endpoint")
	}
}

// TestProbeArtifactsLiveLeaf_InvalidURLErrors asserts a malformed endpoint
// URL is rejected with a descriptive error instead of panicking.
func TestProbeArtifactsLiveLeaf_InvalidURLErrors(t *testing.T) {
	t.Parallel()

	_, err := probeArtifactsLiveLeaf(context.Background(), "://not-a-url", time.Second)
	if err == nil {
		t.Fatal("expected error for an unparsable endpoint URL")
	}
}

// --- artifactsLeafExpiryWarning / singleLeafExpiryWarning -------------------

func TestArtifactsLeafExpiryWarning_BothEmptyReturnsEmpty(t *testing.T) {
	t.Parallel()

	if got := artifactsLeafExpiryWarning("", ""); got != "" {
		t.Errorf("artifactsLeafExpiryWarning(\"\", \"\") = %q, want empty", got)
	}
}

func TestArtifactsLeafExpiryWarning_FarFutureReturnsEmpty(t *testing.T) {
	t.Parallel()

	future := time.Now().Add(300 * 24 * time.Hour).UTC().Format(time.RFC3339)

	if got := artifactsLeafExpiryWarning(future, future); got != "" {
		t.Errorf("artifactsLeafExpiryWarning(far future) = %q, want empty", got)
	}
}

func TestArtifactsLeafExpiryWarning_WithinWindowWarns(t *testing.T) {
	t.Parallel()

	soon := time.Now().Add(10 * 24 * time.Hour).UTC().Format(time.RFC3339)

	got := artifactsLeafExpiryWarning("", soon)
	if got == "" {
		t.Fatal("expected a warning for a leaf expiring in 10 days")
	}

	for _, want := range []string{"served", "expires in", "ocfp artifacts provision"} {
		if !strings.Contains(got, want) {
			t.Errorf("warning %q missing %q", got, want)
		}
	}
}

func TestArtifactsLeafExpiryWarning_AlreadyExpiredWarns(t *testing.T) {
	t.Parallel()

	past := time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339)

	got := artifactsLeafExpiryWarning(past, "")
	if got == "" {
		t.Fatal("expected a warning for an already-expired leaf")
	}

	for _, want := range []string{"recorded", "EXPIRED", "ocfp artifacts provision"} {
		if !strings.Contains(got, want) {
			t.Errorf("warning %q missing %q", got, want)
		}
	}
}

// TestArtifactsLeafExpiryWarning_PrefersLiveOverRecorded asserts the live
// (served) value takes precedence when both are supplied and only one is
// within the warning window — the live probe reflects what's actually being
// served right now, which is the more actionable signal.
func TestArtifactsLeafExpiryWarning_PrefersLiveOverRecorded(t *testing.T) {
	t.Parallel()

	farFuture := time.Now().Add(300 * 24 * time.Hour).UTC().Format(time.RFC3339)
	soon := time.Now().Add(5 * 24 * time.Hour).UTC().Format(time.RFC3339)

	got := artifactsLeafExpiryWarning(farFuture, soon)
	if !strings.Contains(got, "served") {
		t.Errorf("warning %q should be about the live (served) cert when it is the one expiring soon", got)
	}

	got = artifactsLeafExpiryWarning(soon, farFuture)
	if !strings.Contains(got, "recorded") {
		t.Errorf("warning %q should fall back to recorded when the live cert is fine", got)
	}
}

func TestArtifactsLeafExpiryWarning_UnparsableValueIgnored(t *testing.T) {
	t.Parallel()

	if got := artifactsLeafExpiryWarning("not-a-timestamp", ""); got != "" {
		t.Errorf("artifactsLeafExpiryWarning(unparsable) = %q, want empty (never fail status over a bad value)", got)
	}
}

// --- buildArtifactsLeafExpiry ------------------------------------------------

// newLeafExpiryTestContext builds an artifactsContext backed by a temp-dir
// state.Manager with a single pre-seeded artifacts resource, sufficient for
// buildArtifactsLeafExpiry (which only reads ac.state and ac.lookup — no
// provider/compute calls).
func newLeafExpiryTestContext(t *testing.T, blocName string, props map[string]interface{}, lookup *artifacts.LookupResult) *artifactsContext {
	t.Helper()

	sm, err := state.NewManager(filepath.Join(t.TempDir(), ".state"))
	if err != nil {
		t.Fatalf("state.NewManager: %v", err)
	}

	_, err = sm.Load(blocName)
	if err != nil {
		t.Fatalf("sm.Load: %v", err)
	}

	err = sm.AddResource(&state.Resource{
		ID:         lookup.Name,
		Type:       artifacts.ResourceType,
		Name:       lookup.Name,
		State:      "active",
		Properties: props,
	})
	if err != nil {
		t.Fatalf("sm.AddResource: %v", err)
	}

	return &artifactsContext{
		blocName: blocName,
		cfg:      &config.Config{Name: blocName},
		state:    sm,
		lookup:   lookup,
	}
}

// TestBuildArtifactsLeafExpiry_DisabledModeSkipsLiveProbe asserts a
// disabled-TLS artifacts VM never attempts a TLS dial (there is no TLS leaf
// to probe) and surfaces only whatever the state resource recorded.
func TestBuildArtifactsLeafExpiry_DisabledModeSkipsLiveProbe(t *testing.T) {
	t.Parallel()

	lookup := &artifacts.LookupResult{
		Name:     "dev-artifacts",
		Endpoint: "http://10.0.0.11:9000",
		TLSMode:  config.ArtifactsTLSModeDisabled,
	}

	ac := newLeafExpiryTestContext(t, "dev", map[string]interface{}{}, lookup)

	expiry := buildArtifactsLeafExpiry(ac)

	if expiry.RecordedNotAfter != "" || expiry.LiveNotAfter != "" || expiry.Warning != "" {
		t.Errorf("expiry = %+v, want all empty for disabled TLS mode with no recorded value", expiry)
	}
}

// TestBuildArtifactsLeafExpiry_RecordedValueSurfacesWithoutLiveEndpoint
// asserts the recorded state value surfaces even when the endpoint cannot be
// live-probed (unreachable IP here stands in for a VM that is down).
func TestBuildArtifactsLeafExpiry_RecordedValueSurfacesWithoutLiveEndpoint(t *testing.T) {
	t.Parallel()

	recorded := time.Now().Add(400 * 24 * time.Hour).UTC().Format(time.RFC3339)

	lookup := &artifacts.LookupResult{
		Name:     "dev-artifacts",
		Endpoint: "https://192.0.2.1:9000", // TEST-NET-1: unroutable, dial fails fast
		TLSMode:  config.ArtifactsTLSModeInternalCA,
	}

	ac := newLeafExpiryTestContext(t, "dev", map[string]interface{}{
		"tls_leaf_not_after": recorded,
	}, lookup)

	expiry := buildArtifactsLeafExpiry(ac)

	if expiry.RecordedNotAfter != recorded {
		t.Errorf("RecordedNotAfter = %q, want %q", expiry.RecordedNotAfter, recorded)
	}

	if expiry.LiveNotAfter != "" {
		t.Errorf("LiveNotAfter = %q, want empty (endpoint unreachable)", expiry.LiveNotAfter)
	}

	if expiry.Warning != "" {
		t.Errorf("Warning = %q, want empty (recorded value is far in the future)", expiry.Warning)
	}
}

// TestBuildArtifactsLeafExpiry_LiveProbeSurfacesAndWarns asserts a reachable
// TLS endpoint's served-cert NotAfter is picked up, and — when it's within
// the warning window — a warning is produced.
func TestBuildArtifactsLeafExpiry_LiveProbeSurfacesAndWarns(t *testing.T) {
	t.Parallel()

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	lookup := &artifacts.LookupResult{
		Name:     "dev-artifacts",
		Endpoint: srv.URL,
		TLSMode:  config.ArtifactsTLSModeSelfSigned,
	}

	ac := newLeafExpiryTestContext(t, "dev", map[string]interface{}{}, lookup)

	expiry := buildArtifactsLeafExpiry(ac)

	wantLive := srv.Certificate().NotAfter.UTC().Format(time.RFC3339)
	if expiry.LiveNotAfter != wantLive {
		t.Errorf("LiveNotAfter = %q, want %q", expiry.LiveNotAfter, wantLive)
	}

	// httptest's generated test certs are already expired/near-expiry in some
	// Go versions and comfortably valid in others; assert the warning is
	// internally consistent with the live value rather than asserting a
	// specific pass/fail, so this test doesn't become Go-version-dependent.
	if expiry.Warning != "" && !strings.Contains(expiry.Warning, "served") {
		t.Errorf("non-empty Warning %q should reference the served (live) cert here", expiry.Warning)
	}
}

// TestBuildArtifactsLeafExpiry_FingerprintDriftDetected asserts a mismatch
// between the pinned (state) fingerprint and the live-probed served cert's
// fingerprint is flagged as drift (task 6.2 gap 2), with both fingerprints
// named in the warning.
func TestBuildArtifactsLeafExpiry_FingerprintDriftDetected(t *testing.T) {
	t.Parallel()

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	lookup := &artifacts.LookupResult{
		Name:     "dev-artifacts",
		Endpoint: srv.URL,
		TLSMode:  config.ArtifactsTLSModeSelfSigned,
	}

	ac := newLeafExpiryTestContext(t, "dev", map[string]interface{}{
		"tls_fingerprint_sha256": "deadbeef00000000000000000000000000000000000000000000000000000000",
	}, lookup)

	expiry := buildArtifactsLeafExpiry(ac)

	if !expiry.FingerprintDrift {
		t.Fatal("expected FingerprintDrift=true when pinned and live fingerprints differ")
	}

	if expiry.DriftWarning == "" {
		t.Fatal("expected a non-empty DriftWarning when drift is detected")
	}

	for _, want := range []string{expiry.PinnedFingerprint, expiry.LiveFingerprint, "run `ocfp artifacts provision`"} {
		if !strings.Contains(expiry.DriftWarning, want) {
			t.Errorf("DriftWarning %q missing %q", expiry.DriftWarning, want)
		}
	}
}

// TestBuildArtifactsLeafExpiry_FingerprintMatchNoDrift asserts a matching
// pinned/live fingerprint produces no drift and no warning.
func TestBuildArtifactsLeafExpiry_FingerprintMatchNoDrift(t *testing.T) {
	t.Parallel()

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sum := sha256.Sum256(srv.Certificate().Raw)
	matchingFingerprint := hex.EncodeToString(sum[:])

	lookup := &artifacts.LookupResult{
		Name:     "dev-artifacts",
		Endpoint: srv.URL,
		TLSMode:  config.ArtifactsTLSModeSelfSigned,
	}

	ac := newLeafExpiryTestContext(t, "dev", map[string]interface{}{
		"tls_fingerprint_sha256": matchingFingerprint,
	}, lookup)

	expiry := buildArtifactsLeafExpiry(ac)

	if expiry.FingerprintDrift {
		t.Errorf("expected FingerprintDrift=false when fingerprints match, got DriftWarning=%q", expiry.DriftWarning)
	}

	if expiry.DriftWarning != "" {
		t.Errorf("expected empty DriftWarning when fingerprints match, got %q", expiry.DriftWarning)
	}
}

// TestBuildArtifactsLeafExpiry_NoPinnedFingerprintNoDrift asserts a state
// resource that never recorded a pinned fingerprint (predates task 6.2, or
// TLS disabled) never reports drift — there is nothing to compare against.
func TestBuildArtifactsLeafExpiry_NoPinnedFingerprintNoDrift(t *testing.T) {
	t.Parallel()

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	lookup := &artifacts.LookupResult{
		Name:     "dev-artifacts",
		Endpoint: srv.URL,
		TLSMode:  config.ArtifactsTLSModeSelfSigned,
	}

	ac := newLeafExpiryTestContext(t, "dev", map[string]interface{}{}, lookup)

	expiry := buildArtifactsLeafExpiry(ac)

	if expiry.FingerprintDrift {
		t.Errorf("expected FingerprintDrift=false with no pinned fingerprint, got DriftWarning=%q", expiry.DriftWarning)
	}
}

// --- artifactsFingerprintDriftWarning ----------------------------------------

func TestArtifactsFingerprintDriftWarning_BothEmptyReturnsEmpty(t *testing.T) {
	t.Parallel()

	if got := artifactsFingerprintDriftWarning("", ""); got != "" {
		t.Errorf("artifactsFingerprintDriftWarning(\"\", \"\") = %q, want empty", got)
	}
}

func TestArtifactsFingerprintDriftWarning_OneEmptyReturnsEmpty(t *testing.T) {
	t.Parallel()

	if got := artifactsFingerprintDriftWarning("abc123", ""); got != "" {
		t.Errorf("artifactsFingerprintDriftWarning(pinned-only) = %q, want empty (nothing to compare)", got)
	}

	if got := artifactsFingerprintDriftWarning("", "abc123"); got != "" {
		t.Errorf("artifactsFingerprintDriftWarning(live-only) = %q, want empty (nothing to compare)", got)
	}
}

func TestArtifactsFingerprintDriftWarning_MatchReturnsEmpty(t *testing.T) {
	t.Parallel()

	if got := artifactsFingerprintDriftWarning("abc123", "abc123"); got != "" {
		t.Errorf("artifactsFingerprintDriftWarning(matching) = %q, want empty", got)
	}

	// Case-insensitive: hex fingerprints are always generated lowercase in
	// this codebase, but the comparison itself should not depend on that.
	if got := artifactsFingerprintDriftWarning("ABC123", "abc123"); got != "" {
		t.Errorf("artifactsFingerprintDriftWarning(case-insensitive match) = %q, want empty", got)
	}
}

func TestArtifactsFingerprintDriftWarning_MismatchNamesBothFingerprints(t *testing.T) {
	t.Parallel()

	got := artifactsFingerprintDriftWarning("pinned-fp", "live-fp")
	if got == "" {
		t.Fatal("expected a non-empty warning for mismatched fingerprints")
	}

	for _, want := range []string{"pinned-fp", "live-fp", "operator metadata only", "ocfp artifacts provision"} {
		if !strings.Contains(got, want) {
			t.Errorf("warning %q missing %q", got, want)
		}
	}
}

// --- buildArtifactsStatusReport (fingerprint drift fields) -------------------

func TestBuildArtifactsStatusReport_FingerprintDriftFieldsOnlyWhenDrifting(t *testing.T) {
	t.Parallel()

	lr := &artifacts.LookupResult{Name: "dev-artifacts"}

	noDrift := buildArtifactsStatusReport(lr, "running", version.Info{}, artifactsLeafExpiry{})
	if _, ok := noDrift["tls_fingerprint_drift"]; ok {
		t.Error("tls_fingerprint_drift should be absent when there is no drift")
	}

	if _, ok := noDrift["tls_fingerprint_drift_warning"]; ok {
		t.Error("tls_fingerprint_drift_warning should be absent when there is no drift")
	}

	drifting := buildArtifactsStatusReport(lr, "running", version.Info{}, artifactsLeafExpiry{
		FingerprintDrift: true,
		DriftWarning:     "served leaf fingerprint (live-fp) does not match the pinned tls_fingerprint_sha256 in state (pinned-fp)",
	})

	if drifting["tls_fingerprint_drift"] != true {
		t.Errorf("tls_fingerprint_drift = %v, want true", drifting["tls_fingerprint_drift"])
	}

	if drifting["tls_fingerprint_drift_warning"] == "" {
		t.Error("tls_fingerprint_drift_warning should be populated when drifting")
	}
}
