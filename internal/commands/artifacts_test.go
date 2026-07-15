package commands

import (
	"crypto/x509"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/artifacts"
	"github.com/ocfp/ocfp-cli-go/internal/vault"
	"github.com/ocfp/ocfp-cli-go/internal/version"
)

// fakeCASafe is a minimal vault.SafeInterface stub backed by an in-memory
// map, mirroring the fakeSafe pattern in internal/vault/ca_test.go (that
// type is unexported to package vault, so the `ca` action's tests need
// their own copy here).
type fakeCASafe struct {
	data map[string]map[string]interface{}
}

func newFakeCASafe() *fakeCASafe {
	return &fakeCASafe{data: map[string]map[string]interface{}{}}
}

func (f *fakeCASafe) Set(path, key string, value interface{}) error {
	if _, ok := f.data[path]; !ok {
		f.data[path] = map[string]interface{}{}
	}

	f.data[path][key] = value

	return nil
}

func (f *fakeCASafe) SetMultiple(path string, data map[string]interface{}) error {
	if _, ok := f.data[path]; !ok {
		f.data[path] = map[string]interface{}{}
	}

	for k, v := range data {
		f.data[path][k] = v
	}

	return nil
}

func (f *fakeCASafe) Get(path, key string) (interface{}, error) {
	if d, ok := f.data[path]; ok {
		return d[key], nil
	}

	return nil, nil
}

func (f *fakeCASafe) GetAll(path string) (map[string]interface{}, error) {
	if d, ok := f.data[path]; ok {
		return d, nil
	}

	return nil, nil
}

func (f *fakeCASafe) Exists(path string) (bool, error) {
	_, ok := f.data[path]
	return ok, nil
}

func (f *fakeCASafe) Delete(path, key string) error {
	if d, ok := f.data[path]; ok {
		delete(d, key)
	}

	return nil
}

func (f *fakeCASafe) List(string) ([]string, error)                 { return nil, nil }
func (f *fakeCASafe) Export(string) (map[string]interface{}, error) { return nil, nil }
func (f *fakeCASafe) Import(string, map[string]interface{}) error   { return nil }
func (f *fakeCASafe) GetEngineInfo(string) (*vault.EngineInfo, error) {
	return nil, nil
}
func (f *fakeCASafe) MustGet(path, key string) interface{} { v, _ := f.Get(path, key); return v }
func (f *fakeCASafe) GetString(path, key string) (string, error) {
	v, _ := f.Get(path, key)
	s, _ := v.(string)

	return s, nil
}
func (f *fakeCASafe) GetJSON(string, string) ([]byte, error) { return nil, nil }

var _ vault.SafeInterface = (*fakeCASafe)(nil)

// --- resolveArtifactsCAMaterial -------------------------------------------

func TestResolveArtifactsCAMaterial_ReadOnlyFoundReturnsStored(t *testing.T) {
	t.Parallel()

	safe := newFakeCASafe()
	safe.data["secret/ocfp/dev/ca"] = map[string]interface{}{
		"cert":        "CERT-PEM",
		"key":         "KEY-PEM",
		"fingerprint": "abc123",
	}

	mat, err := resolveArtifactsCAMaterial(safe, "dev", false)
	if err != nil {
		t.Fatalf("resolveArtifactsCAMaterial: %v", err)
	}

	if mat.CertPEM != "CERT-PEM" || mat.Fingerprint != "abc123" {
		t.Errorf("got %+v, want CertPEM=CERT-PEM Fingerprint=abc123", mat)
	}
}

func TestResolveArtifactsCAMaterial_ReadOnlyMissingErrorsActionably(t *testing.T) {
	t.Parallel()

	safe := newFakeCASafe()

	_, err := resolveArtifactsCAMaterial(safe, "dev", false)
	if !errors.Is(err, vault.ErrBlocCANotFound) {
		t.Fatalf("expected error wrapping vault.ErrBlocCANotFound, got %v", err)
	}

	msg := err.Error()
	if !strings.Contains(msg, "ocfp vault inception --bloc dev") {
		t.Errorf("error message missing inception remediation: %q", msg)
	}

	if !strings.Contains(msg, "--generate") {
		t.Errorf("error message missing --generate escape hatch: %q", msg)
	}

	// Read-only path must never mint — no secret should have been written.
	if _, ok := safe.data["secret/ocfp/dev/ca"]; ok {
		t.Errorf("resolveArtifactsCAMaterial(generate=false) must never write to the safe")
	}
}

func TestResolveArtifactsCAMaterial_MalformedStoredCAPassesThrough(t *testing.T) {
	t.Parallel()

	safe := newFakeCASafe()
	safe.data["secret/ocfp/dev/ca"] = map[string]interface{}{
		"cert": "",
		"key":  "KEY-PEM",
	}

	_, err := resolveArtifactsCAMaterial(safe, "dev", false)
	if !errors.Is(err, vault.ErrBlocCAMalformed) {
		t.Fatalf("expected error wrapping vault.ErrBlocCAMalformed, got %v", err)
	}
}

func TestResolveArtifactsCAMaterial_GenerateMintsWhenAbsent(t *testing.T) {
	t.Parallel()

	safe := newFakeCASafe()

	mat, err := resolveArtifactsCAMaterial(safe, "dev", true)
	if err != nil {
		t.Fatalf("resolveArtifactsCAMaterial(generate=true): %v", err)
	}

	if mat.CertPEM == "" || mat.KeyPEM == "" || mat.Fingerprint == "" {
		t.Errorf("generated material incomplete: %+v", mat)
	}

	if _, ok := safe.data["secret/ocfp/dev/ca"]; !ok {
		t.Errorf("generate=true must persist the minted CA to the safe")
	}
}

func TestResolveArtifactsCAMaterial_GenerateIsIdempotent(t *testing.T) {
	t.Parallel()

	safe := newFakeCASafe()

	first, err := resolveArtifactsCAMaterial(safe, "dev", true)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}

	second, err := resolveArtifactsCAMaterial(safe, "dev", true)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}

	if first.Fingerprint != second.Fingerprint {
		t.Errorf("fingerprint changed across generate=true calls: %q -> %q", first.Fingerprint, second.Fingerprint)
	}
}

// --- parseCACertPEM --------------------------------------------------------

func TestParseCACertPEM_Valid(t *testing.T) {
	t.Parallel()

	mat, err := artifacts.GenerateInternalCA("dev")
	if err != nil {
		t.Fatalf("GenerateInternalCA: %v", err)
	}

	cert, err := parseCACertPEM(mat.CertPEM)
	if err != nil {
		t.Fatalf("parseCACertPEM: %v", err)
	}

	if cert.Subject.CommonName != "ocfp-dev-internal-ca" {
		t.Errorf("CommonName = %q, want ocfp-dev-internal-ca", cert.Subject.CommonName)
	}

	if !cert.NotAfter.After(cert.NotBefore) {
		t.Errorf("NotAfter (%v) must be after NotBefore (%v)", cert.NotAfter, cert.NotBefore)
	}
}

func TestParseCACertPEM_InvalidInputs(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"empty":             "",
		"not pem":           "this is not a pem block",
		"wrong block type":  "-----BEGIN RSA PRIVATE KEY-----\nZm9v\n-----END RSA PRIVATE KEY-----\n",
		"corrupt der bytes": "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n",
	}

	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := parseCACertPEM(input)
			if err == nil {
				t.Errorf("parseCACertPEM(%q): expected error, got nil", name)
			}
		})
	}
}

// --- printArtifactsCA -------------------------------------------------------

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	orig := os.Stdout

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}

	os.Stdout = w

	fn()

	//nolint:errcheck // best-effort close before restoring stdout
	w.Close()

	os.Stdout = orig

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading captured stdout: %v", err)
	}

	return string(out)
}

// NOTE: the printArtifactsCA tests below deliberately do not call
// t.Parallel() — captureStdout swaps the process-global os.Stdout, which
// would race across parallel subtests.
func TestPrintArtifactsCA_DefaultPrintsPEM(t *testing.T) {
	mat, cert := testCAMaterialAndCert(t)

	out := captureStdout(t, func() {
		if err := printArtifactsCA(mat, cert, false, false, ""); err != nil {
			t.Fatalf("printArtifactsCA: %v", err)
		}
	})

	if strings.TrimSpace(out) != strings.TrimSpace(mat.CertPEM) {
		t.Errorf("stdout = %q, want the cert PEM verbatim", out)
	}
}

func TestPrintArtifactsCA_FingerprintOnly(t *testing.T) {
	mat, cert := testCAMaterialAndCert(t)

	out := captureStdout(t, func() {
		if err := printArtifactsCA(mat, cert, false, true, ""); err != nil {
			t.Fatalf("printArtifactsCA: %v", err)
		}
	})

	if strings.TrimSpace(out) != mat.Fingerprint {
		t.Errorf("stdout = %q, want fingerprint %q", out, mat.Fingerprint)
	}
}

func TestPrintArtifactsCA_JSONIncludesAllFields(t *testing.T) {
	mat, cert := testCAMaterialAndCert(t)

	out := captureStdout(t, func() {
		if err := printArtifactsCA(mat, cert, true, false, ""); err != nil {
			t.Fatalf("printArtifactsCA: %v", err)
		}
	})

	for _, want := range []string{`"cert"`, `"fingerprint"`, `"not_before"`, `"not_after"`, mat.Fingerprint} {
		if !strings.Contains(out, want) {
			t.Errorf("json output missing %q: %s", want, out)
		}
	}
}

func TestPrintArtifactsCA_OutPathSuppressesStdout(t *testing.T) {
	mat, cert := testCAMaterialAndCert(t)

	out := captureStdout(t, func() {
		if err := printArtifactsCA(mat, cert, false, false, "/tmp/wherever-ca.pem"); err != nil {
			t.Fatalf("printArtifactsCA: %v", err)
		}
	})

	if out != "" {
		t.Errorf("stdout = %q, want empty when --out already wrote the PEM", out)
	}
}

func testCAMaterialAndCert(t *testing.T) (artifacts.CAMaterial, *x509.Certificate) {
	t.Helper()

	mat, err := artifacts.GenerateInternalCA("dev")
	if err != nil {
		t.Fatalf("GenerateInternalCA: %v", err)
	}

	cert, err := parseCACertPEM(mat.CertPEM)
	if err != nil {
		t.Fatalf("parseCACertPEM: %v", err)
	}

	return mat, cert
}

// --- buildArtifactsStatusReport (task 1.4) ---------------------------------

func TestBuildArtifactsStatusReport_IncludesVersionFields(t *testing.T) {
	t.Parallel()

	lr := &artifacts.LookupResult{
		Name:       "dev-artifacts",
		VMID:       "101",
		PrivateIP:  "10.0.0.11",
		Endpoint:   "https://10.0.0.11:9000",
		TLSMode:    "internal-ca",
		ZFSDataset: "rpool/dev",
	}

	vinfo := version.Info{
		Version:   "1.2.3",
		BuildTime: "2026-07-15T00:00:00Z",
		GitCommit: "e902d39",
	}

	report := buildArtifactsStatusReport(lr, "running", vinfo, artifactsLeafExpiry{})

	checks := map[string]any{
		"name":           "dev-artifacts",
		"vm_id":          "101",
		"state":          "running",
		"private_ip":     "10.0.0.11",
		"endpoint":       "https://10.0.0.11:9000",
		"tls_mode":       "internal-ca",
		"dataset":        "rpool/dev",
		"cli_version":    "1.2.3",
		"cli_git_commit": "e902d39",
		"cli_build_time": "2026-07-15T00:00:00Z",
	}

	for k, want := range checks {
		if report[k] != want {
			t.Errorf("report[%q] = %v, want %v", k, report[k], want)
		}
	}
}

func TestBuildArtifactsStatusReport_StaleBinaryMarkersSurface(t *testing.T) {
	t.Parallel()

	lr := &artifacts.LookupResult{Name: "dev-artifacts", VMID: "101"}

	// Zero-value version.Info mirrors an unstamped `go build` (no -ldflags),
	// the exact failure class the incident this task fixes was rooted in.
	report := buildArtifactsStatusReport(lr, "running", version.Info{}, artifactsLeafExpiry{})

	if report["cli_git_commit"] != "" {
		t.Errorf("cli_git_commit = %v, want empty for unstamped build.Info zero value", report["cli_git_commit"])
	}
}
