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
	fn := func(args ...string) ([]byte, error) {
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
	name := "bosh-openstack-kvm-ubuntu-jammy-go_agent"
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
	name := "bosh-openstack-kvm-ubuntu-jammy-go_agent"
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
	rb, _ := recordingRunBosh([]stubCall{
		{argPrefix: "stemcells", out: makeBoshStemcellsJSON(nil)},
	})

	got, err := stemcell.IsStemcellUploaded(context.Background(), rb,
		"bosh-openstack-kvm-ubuntu-jammy-go_agent", "1.584")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Fatal("expected false for empty director, got true")
	}
}

// TestIsStemcellUploaded_NameMismatch ensures a different name doesn't match.
func TestIsStemcellUploaded_NameMismatch(t *testing.T) {
	version := "1.584"
	rows := []map[string]string{
		{"Name": "bosh-vsphere-esxi-ubuntu-jammy-go_agent", "Version": version},
	}
	rb, _ := recordingRunBosh([]stubCall{
		{argPrefix: "stemcells", out: makeBoshStemcellsJSON(rows)},
	})

	got, err := stemcell.IsStemcellUploaded(context.Background(), rb,
		"bosh-openstack-kvm-ubuntu-jammy-go_agent", version)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Fatal("expected false for name mismatch, got true")
	}
}

// TestIsStemcellUploaded_RunBoshError propagates RunBosh errors.
func TestIsStemcellUploaded_RunBoshError(t *testing.T) {
	rb, _ := recordingRunBosh([]stubCall{
		{argPrefix: "stemcells", err: fmt.Errorf("bosh not logged in")},
	})

	_, err := stemcell.IsStemcellUploaded(context.Background(), rb,
		"bosh-openstack-kvm-ubuntu-jammy-go_agent", "1.584")
	if err == nil {
		t.Fatal("expected error from RunBosh failure, got nil")
	}
}

// TestIsStemcellUploaded_InvalidJSON propagates JSON parse errors.
func TestIsStemcellUploaded_InvalidJSON(t *testing.T) {
	rb, _ := recordingRunBosh([]stubCall{
		{argPrefix: "stemcells", out: []byte("not-json{")},
	})

	_, err := stemcell.IsStemcellUploaded(context.Background(), rb,
		"bosh-openstack-kvm-ubuntu-jammy-go_agent", "1.584")
	if err == nil {
		t.Fatal("expected JSON parse error, got nil")
	}
}

// TestIsStemcellUploaded_EmptyName rejects empty name.
func TestIsStemcellUploaded_EmptyName(t *testing.T) {
	rb, _ := recordingRunBosh(nil)
	_, err := stemcell.IsStemcellUploaded(context.Background(), rb, "", "1.584")
	if err == nil {
		t.Fatal("expected error for empty name, got nil")
	}
}

// TestIsStemcellUploaded_EmptyVersion rejects empty version.
func TestIsStemcellUploaded_EmptyVersion(t *testing.T) {
	rb, _ := recordingRunBosh(nil)
	_, err := stemcell.IsStemcellUploaded(context.Background(), rb, "bosh-openstack-kvm-ubuntu-jammy-go_agent", "")
	if err == nil {
		t.Fatal("expected error for empty version, got nil")
	}
}

// ---- TestFetchSHA1_FromBoshIO -----------------------------------------------

func TestFetchSHA1_FromBoshIO(t *testing.T) {
	name := "bosh-openstack-kvm-ubuntu-jammy-go_agent"
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
	name := "bosh-openstack-kvm-ubuntu-jammy-go_agent"

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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	client := &http.Client{Transport: rewriteHostTransport{base: srv.URL}}

	_, err := stemcell.FetchSHA1(context.Background(), client, "bosh-openstack-kvm-ubuntu-jammy-go_agent", "1.584")
	if err == nil {
		t.Fatal("expected HTTP error, got nil")
	}
}

// TestFetchSHA1_EmptySHA1 returns error when sha1 field is empty.
func TestFetchSHA1_EmptySHA1(t *testing.T) {
	name := "bosh-openstack-kvm-ubuntu-jammy-go_agent"
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
	_, err := stemcell.FetchSHA1(context.Background(), nil, "bosh-openstack-kvm-ubuntu-jammy-go_agent", "1.584")
	if err == nil {
		t.Fatal("expected error for nil client, got nil")
	}
}

// ---- T40 TestEnsureStemcell_SkipsIfPresent ----------------------------------

func TestEnsureStemcell_SkipsIfPresent(t *testing.T) {
	name := "bosh-openstack-kvm-ubuntu-jammy-go_agent"
	version := "1.584"

	rows := []map[string]string{
		{"Name": name, "Version": version},
	}
	rb, calls := recordingRunBosh([]stubCall{
		{argPrefix: "stemcells", out: makeBoshStemcellsJSON(rows)},
	})

	fetchCalled := false
	fakeFetchSHA1 := func(ctx context.Context, n, v string) (string, error) {
		fetchCalled = true
		return "sha1-should-not-be-called", nil
	}

	err := stemcell.EnsureStemcell(context.Background(), rb, fakeFetchSHA1, name, version,
		"https://bosh.io/d/stemcells/"+name+"?v="+version)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Only one RunBosh call (stemcells --json); no upload-stemcell call.
	if len(*calls) != 1 {
		t.Fatalf("expected 1 RunBosh call, got %d: %v", len(*calls), *calls)
	}
	if (*calls)[0][0] != "stemcells" {
		t.Fatalf("expected first call to be 'stemcells', got %q", (*calls)[0][0])
	}
	if fetchCalled {
		t.Fatal("fetchSHA1 must not be called when stemcell already present")
	}
}

// ---- TestEnsureStemcell_UploadsIfAbsent -------------------------------------

func TestEnsureStemcell_UploadsIfAbsent(t *testing.T) {
	name := "bosh-openstack-kvm-ubuntu-jammy-go_agent"
	version := "1.584"
	sha1 := "deadbeef01"
	url := "https://bosh.io/d/stemcells/" + name + "?v=" + version

	rb, calls := recordingRunBosh([]stubCall{
		{argPrefix: "stemcells", out: makeBoshStemcellsJSON(nil)}, // absent
		{argPrefix: "upload-stemcell", out: []byte("")},
	})

	fakeFetchSHA1 := func(ctx context.Context, n, v string) (string, error) {
		if n != name || v != version {
			return "", fmt.Errorf("unexpected name=%q version=%q", n, v)
		}
		return sha1, nil
	}

	err := stemcell.EnsureStemcell(context.Background(), rb, fakeFetchSHA1, name, version, url)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Expect exactly two calls: stemcells --json AND upload-stemcell.
	if len(*calls) != 2 {
		t.Fatalf("expected 2 RunBosh calls, got %d: %v", len(*calls), *calls)
	}

	uploadArgs := (*calls)[1]
	// Verify --sha1 flag and value are present.
	found := false
	for i, arg := range uploadArgs {
		if arg == "--sha1" && i+1 < len(uploadArgs) && uploadArgs[i+1] == sha1 {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("upload-stemcell call missing '--sha1 %s': got %v", sha1, uploadArgs)
	}

	// Verify URL is in the args.
	foundURL := false
	for _, arg := range uploadArgs {
		if arg == url {
			foundURL = true
			break
		}
	}
	if !foundURL {
		t.Fatalf("upload-stemcell call missing url %q: got %v", url, uploadArgs)
	}
}

// TestEnsureStemcell_FetchSHA1Error propagates fetchSHA1 errors.
func TestEnsureStemcell_FetchSHA1Error(t *testing.T) {
	name := "bosh-openstack-kvm-ubuntu-jammy-go_agent"
	version := "1.584"

	rb, _ := recordingRunBosh([]stubCall{
		{argPrefix: "stemcells", out: makeBoshStemcellsJSON(nil)},
	})

	fakeFetchSHA1 := func(ctx context.Context, n, v string) (string, error) {
		return "", fmt.Errorf("bosh.io unreachable")
	}

	err := stemcell.EnsureStemcell(context.Background(), rb, fakeFetchSHA1, name, version,
		"https://bosh.io/d/stemcells/"+name+"?v="+version)
	if err == nil {
		t.Fatal("expected error from fetchSHA1 failure, got nil")
	}
}

// TestEnsureStemcell_EmptyInputs validates input checks.
func TestEnsureStemcell_EmptyInputs(t *testing.T) {
	rb, _ := recordingRunBosh(nil)
	noopFetch := func(ctx context.Context, n, v string) (string, error) { return "sha1", nil }

	cases := []struct {
		name, version, url string
		label              string
	}{
		{"", "1.0", "https://example.com", "empty name"},
		{"bosh-openstack-kvm-ubuntu-jammy-go_agent", "", "https://example.com", "empty version"},
		{"bosh-openstack-kvm-ubuntu-jammy-go_agent", "1.0", "", "empty url"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.label, func(t *testing.T) {
			err := stemcell.EnsureStemcell(context.Background(), rb, noopFetch, tc.name, tc.version, tc.url)
			if err == nil {
				t.Fatalf("%s: expected error, got nil", tc.label)
			}
		})
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
