package stemcell_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/pve/stemcell"
)

// ---- helpers ----------------------------------------------------------------

// makeBoshStemcellsJSON builds a minimal bosh stemcells --json payload containing
// the supplied rows. Each entry is map[name, version].
func makeBoshStemcellsJSON(rows []map[string]string) []byte {
	type table struct {
		Rows []map[string]string `json:"Rows"`
	}
	type payload struct {
		Tables []table `json:"Tables"`
	}
	p := payload{Tables: []table{{Rows: rows}}}
	b, _ := json.Marshal(p)
	return b
}

// recordingRunBosh returns a RunBosh func that records every invocation and
// returns the pre-configured response for the first matching prefix.
type stubCall struct {
	argPrefix string // checked via strings.HasPrefix on first arg
	out       []byte
	err       error
}

func recordingRunBosh(stubs []stubCall) (stemcell.RunBosh, *[][]string) {
	calls := &[][]string{}
	fn := func(_ context.Context, args ...string) ([]byte, error) {
		*calls = append(*calls, args)
		for _, s := range stubs {
			if len(args) > 0 && strings.HasPrefix(args[0], s.argPrefix) {
				return s.out, s.err
			}
		}
		return nil, fmt.Errorf("unexpected bosh args: %v", args)
	}
	return fn, calls
}

// ---- T38 TestIsStemcellUploaded_Present -------------------------------------

func TestIsStemcellUploaded_Present(t *testing.T) {
	t.Parallel()

	name := "bosh-openstack-kvm-ubuntu-noble-go_agent"
	version := "1.584"

	rows := []map[string]string{
		{"Name": name, "Version": version + "*"}, // active stemcell has trailing *
	}
	rb, _ := recordingRunBosh([]stubCall{
		{argPrefix: "stemcells", out: makeBoshStemcellsJSON(rows)},
	})

	got, err := stemcell.IsStemcellUploaded(context.Background(), rb, name, version)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Fatal("expected true (stemcell present), got false")
	}
}

// ---- T39 TestIsStemcellUploaded_Absent --------------------------------------

func TestIsStemcellUploaded_Absent(t *testing.T) {
	t.Parallel()

	name := "bosh-openstack-kvm-ubuntu-noble-go_agent"
	version := "1.584"

	// Different version in the director.
	rows := []map[string]string{
		{"Name": name, "Version": "1.100"},
	}
	rb, _ := recordingRunBosh([]stubCall{
		{argPrefix: "stemcells", out: makeBoshStemcellsJSON(rows)},
	})

	got, err := stemcell.IsStemcellUploaded(context.Background(), rb, name, version)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Fatal("expected false (stemcell absent), got true")
	}
}

// TestIsStemcellUploaded_EmptyDirector ensures an empty rows set returns false without error.
func TestIsStemcellUploaded_EmptyDirector(t *testing.T) {
	t.Parallel()

	rb, _ := recordingRunBosh([]stubCall{
		{argPrefix: "stemcells", out: makeBoshStemcellsJSON(nil)},
	})

	got, err := stemcell.IsStemcellUploaded(context.Background(), rb,
		"bosh-openstack-kvm-ubuntu-noble-go_agent", "1.584")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Fatal("expected false for empty director, got true")
	}
}

// TestIsStemcellUploaded_NameMismatch ensures a different name doesn't match.
func TestIsStemcellUploaded_NameMismatch(t *testing.T) {
	t.Parallel()

	version := "1.584"
	rows := []map[string]string{
		{"Name": "bosh-vsphere-esxi-ubuntu-noble-go_agent", "Version": version},
	}
	rb, _ := recordingRunBosh([]stubCall{
		{argPrefix: "stemcells", out: makeBoshStemcellsJSON(rows)},
	})

	got, err := stemcell.IsStemcellUploaded(context.Background(), rb,
		"bosh-openstack-kvm-ubuntu-noble-go_agent", version)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Fatal("expected false for name mismatch, got true")
	}
}

// TestIsStemcellUploaded_RunBoshError propagates RunBosh errors.
func TestIsStemcellUploaded_RunBoshError(t *testing.T) {
	t.Parallel()

	rb, _ := recordingRunBosh([]stubCall{
		{argPrefix: "stemcells", err: fmt.Errorf("bosh not logged in")},
	})

	_, err := stemcell.IsStemcellUploaded(context.Background(), rb,
		"bosh-openstack-kvm-ubuntu-noble-go_agent", "1.584")
	if err == nil {
		t.Fatal("expected error from RunBosh failure, got nil")
	}
}

// TestIsStemcellUploaded_InvalidJSON propagates JSON parse errors.
func TestIsStemcellUploaded_InvalidJSON(t *testing.T) {
	t.Parallel()

	rb, _ := recordingRunBosh([]stubCall{
		{argPrefix: "stemcells", out: []byte("not-json{")},
	})

	_, err := stemcell.IsStemcellUploaded(context.Background(), rb,
		"bosh-openstack-kvm-ubuntu-noble-go_agent", "1.584")
	if err == nil {
		t.Fatal("expected JSON parse error, got nil")
	}
}

// TestIsStemcellUploaded_EmptyName rejects empty name.
func TestIsStemcellUploaded_EmptyName(t *testing.T) {
	t.Parallel()

	rb, _ := recordingRunBosh(nil)
	_, err := stemcell.IsStemcellUploaded(context.Background(), rb, "", "1.584")
	if err == nil {
		t.Fatal("expected error for empty name, got nil")
	}
}

// TestIsStemcellUploaded_EmptyVersion rejects empty version.
func TestIsStemcellUploaded_EmptyVersion(t *testing.T) {
	t.Parallel()

	rb, _ := recordingRunBosh(nil)
	_, err := stemcell.IsStemcellUploaded(context.Background(), rb, "bosh-openstack-kvm-ubuntu-noble-go_agent", "")
	if err == nil {
		t.Fatal("expected error for empty version, got nil")
	}
}

// ---- TestFetchSHA1_FromBoshIO -----------------------------------------------

func TestFetchSHA1_FromBoshIO(t *testing.T) {
	t.Parallel()

	name := "bosh-openstack-kvm-ubuntu-noble-go_agent"
	version := "1.584"
	wantSHA1 := "abc123def456"

	// Serve a fixture matching the bosh.io API shape.
	fixture := fmt.Sprintf(`[
		{"version":"1.100","regular":{"sha1":"oldsha","url":"https://example.com/old"}},
		{"version":"%s","regular":{"sha1":"%s","url":"https://example.com/current"}},
		{"version":"1.600","regular":{"sha1":"newsha","url":"https://example.com/new"}}
	]`, version, wantSHA1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Validate the path contains the stemcell name.
		if !strings.Contains(r.URL.Path, name) {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, fixture)
	}))
	defer srv.Close()

	// Patch the API base URL by creating a custom HTTP client that redirects to the test server.
	// FetchSHA1 constructs the URL internally; use a transport that rewrites the host.
	client := &http.Client{
		Transport: rewriteHostTransport{base: srv.URL},
	}

	got, err := stemcell.FetchSHA1(context.Background(), client, name, version)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != wantSHA1 {
		t.Fatalf("sha1 mismatch: want %q, got %q", wantSHA1, got)
	}
}

// TestFetchSHA1_VersionNotFound returns a descriptive error when the version is absent.
func TestFetchSHA1_VersionNotFound(t *testing.T) {
	t.Parallel()

	name := "bosh-openstack-kvm-ubuntu-noble-go_agent"

	fixture := `[{"version":"1.100","regular":{"sha1":"sha","url":"https://example.com"}}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, fixture)
	}))
	defer srv.Close()

	client := &http.Client{Transport: rewriteHostTransport{base: srv.URL}}

	_, err := stemcell.FetchSHA1(context.Background(), client, name, "9.999")
	if err == nil {
		t.Fatal("expected error for missing version, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error should mention 'not found', got: %v", err)
	}
}

// TestFetchSHA1_HTTP4xx propagates non-2xx responses as errors.
func TestFetchSHA1_HTTP4xx(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	client := &http.Client{Transport: rewriteHostTransport{base: srv.URL}}

	_, err := stemcell.FetchSHA1(context.Background(), client, "bosh-openstack-kvm-ubuntu-noble-go_agent", "1.584")
	if err == nil {
		t.Fatal("expected HTTP error, got nil")
	}
}

// TestFetchSHA1_EmptySHA1 returns error when sha1 field is empty.
func TestFetchSHA1_EmptySHA1(t *testing.T) {
	t.Parallel()

	name := "bosh-openstack-kvm-ubuntu-noble-go_agent"
	version := "1.584"

	// Entry exists but regular.sha1 is empty (light stemcell only, no regular build).
	fixture := fmt.Sprintf(`[{"version":"%s","regular":{"sha1":"","url":""}}]`, version)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, fixture)
	}))
	defer srv.Close()

	client := &http.Client{Transport: rewriteHostTransport{base: srv.URL}}

	_, err := stemcell.FetchSHA1(context.Background(), client, name, version)
	if err == nil {
		t.Fatal("expected error for empty sha1, got nil")
	}
}

// TestFetchSHA1_NilClient returns error for nil http client.
func TestFetchSHA1_NilClient(t *testing.T) {
	t.Parallel()

	_, err := stemcell.FetchSHA1(context.Background(), nil, "bosh-openstack-kvm-ubuntu-noble-go_agent", "1.584")
	if err == nil {
		t.Fatal("expected error for nil client, got nil")
	}
}

// ---- transport helper -------------------------------------------------------

// rewriteHostTransport rewrites the HTTP request host to the given base URL,
// enabling tests to intercept calls to bosh.io without DNS changes.
type rewriteHostTransport struct {
	base string
}

func (t rewriteHostTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Parse base to extract scheme+host, keep original path+query.
	base := t.base
	// Strip scheme prefix to get host.
	host := strings.TrimPrefix(strings.TrimPrefix(base, "https://"), "http://")
	scheme := "http"
	if strings.HasPrefix(base, "https://") {
		scheme = "https"
	}

	cloned := req.Clone(req.Context())
	cloned.URL.Scheme = scheme
	cloned.URL.Host = host
	return http.DefaultTransport.RoundTrip(cloned)
}
