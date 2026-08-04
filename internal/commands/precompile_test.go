package commands

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/ocfp/ocfp-cli-go/internal/artifacts"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/vault"
	"github.com/spf13/cobra"
)

// precompileTestCACert returns a parseable (self-signed) cert PEM: the CA
// pool code path (x509.CertPool.AppendCertsFromPEM) rejects an unparseable
// placeholder string, so tests exercising "CA cert present" need real PEM.
func precompileTestCACert(t *testing.T) string {
	t.Helper()

	mat, err := artifacts.GenerateSelfSignedTLS("precompile-test-ca", nil, nil)
	if err != nil {
		t.Fatalf("generating test CA cert: %v", err)
	}

	return mat.CertPEM
}

func TestPrecompileS3Client_CACertPresent_InternalCA_NoVaultCallNeeded(t *testing.T) {
	t.Parallel()

	lr := &artifacts.LookupResult{
		Endpoint:  "https://10.0.0.42:9000",
		AccessKey: "ak",
		SecretKey: "sk",
		TLSMode:   config.ArtifactsTLSModeInternalCA,
		CACert:    precompileTestCACert(t),
	}

	cli, ep, err := precompileS3Client("mybloc", lr, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cli == nil {
		t.Fatal("expected non-nil S3 client")
	}

	if ep != lr.Endpoint {
		t.Errorf("endpoint = %q, want %q", ep, lr.Endpoint)
	}
}

func TestPrecompileS3Client_CACertPresent_SelfSigned(t *testing.T) {
	t.Parallel()

	lr := &artifacts.LookupResult{
		Endpoint:  "https://10.0.0.42:9000",
		AccessKey: "ak",
		SecretKey: "sk",
		TLSMode:   config.ArtifactsTLSModeSelfSigned,
		CACert:    precompileTestCACert(t),
	}

	cli, _, err := precompileS3Client("mybloc", lr, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cli == nil {
		t.Fatal("expected non-nil S3 client")
	}
}

func TestPrecompileS3Client_SelfSigned_NoCACert_RequiresInsecureOptIn(t *testing.T) {
	t.Parallel()

	lr := &artifacts.LookupResult{
		Endpoint:  "https://10.0.0.42:9000",
		AccessKey: "ak",
		SecretKey: "sk",
		TLSMode:   config.ArtifactsTLSModeSelfSigned,
	}

	_, _, err := precompileS3Client("mybloc", lr, false)
	if !errors.Is(err, artifacts.ErrArtifactsInsecureOptInRequired) {
		t.Fatalf("err = %v, want wrapping ErrArtifactsInsecureOptInRequired", err)
	}
}

func TestPrecompileS3Client_SelfSigned_NoCACert_InsecureOptIn_Succeeds(t *testing.T) {
	t.Parallel()

	lr := &artifacts.LookupResult{
		Endpoint:  "https://10.0.0.42:9000",
		AccessKey: "ak",
		SecretKey: "sk",
		TLSMode:   config.ArtifactsTLSModeSelfSigned,
	}

	cli, _, err := precompileS3Client("mybloc", lr, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cli == nil {
		t.Fatal("expected non-nil S3 client")
	}
}

func TestPrecompileS3Client_HTTPEndpoint_NoTLSMaterialNeeded(t *testing.T) {
	t.Parallel()

	lr := &artifacts.LookupResult{
		Endpoint:  "http://10.0.0.42:9000",
		AccessKey: "ak",
		SecretKey: "sk",
		TLSMode:   config.ArtifactsTLSModeDisabled,
	}

	cli, _, err := precompileS3Client("mybloc", lr, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cli == nil {
		t.Fatal("expected non-nil S3 client")
	}
}

func TestPrecompileS3Client_InvalidEndpoint_Errors(t *testing.T) {
	t.Parallel()

	lr := &artifacts.LookupResult{
		Endpoint:  "not-a-url",
		AccessKey: "ak",
		SecretKey: "sk",
		TLSMode:   config.ArtifactsTLSModeDisabled,
	}

	_, _, err := precompileS3Client("mybloc", lr, false)
	if !errors.Is(err, artifacts.ErrArtifactsEndpointInvalid) {
		t.Fatalf("err = %v, want wrapping ErrArtifactsEndpointInvalid", err)
	}
}

// TestPrecompileS3Client_InternalCA_NoCACert_NoVaultAccess_ErrorsDeterministically
// exercises the vault-recovery attempt without touching a real vault: with
// VAULT_TOKEN unset, no ~/.saferc, and the default "token" auth type, vault
// client authentication fails synchronously (no network dial), giving a
// deterministic error path for "internal-ca configured but neither state nor
// vault has the CA".
func TestPrecompileS3Client_InternalCA_NoCACert_NoVaultAccess_ErrorsDeterministically(t *testing.T) {
	// Mutates process env (VAULT_TOKEN/VAULT_ADDR/HOME); not parallel-safe.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("VAULT_TOKEN", "")
	t.Setenv("VAULT_ADDR", "")
	t.Setenv("VAULT_AUTH_TYPE", "")
	t.Setenv("VAULT_NAMESPACE", "")

	lr := &artifacts.LookupResult{
		Endpoint:  "https://10.0.0.42:9000",
		AccessKey: "ak",
		SecretKey: "sk",
		TLSMode:   config.ArtifactsTLSModeInternalCA,
	}

	_, _, err := precompileS3Client("mybloc", lr, false)
	if err == nil {
		t.Fatal("expected error when internal-ca has no CA cert and no vault access")
	}

	if !errors.Is(err, vault.ErrTokenAuthRequiresVaultToken) {
		t.Fatalf("err = %v, want wrapping vault.ErrTokenAuthRequiresVaultToken", err)
	}
}

// fakeStemcellS3 is a minimal precompileObjectAPI fake: a HeadObject miss by
// default (s3types.NotFound, mirroring internal/precompile's own test fakes),
// present objects seeded via the objects map, and a puts counter so tests can
// assert non-mutation.
type fakeStemcellS3 struct {
	mu      sync.Mutex
	objects map[string]string // key -> sha256 hex metadata
	puts    int
}

func newFakeStemcellS3() *fakeStemcellS3 { return &fakeStemcellS3{objects: map[string]string{}} }

func (f *fakeStemcellS3) HeadObject(_ context.Context, in *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	sha, ok := f.objects[*in.Key]
	if !ok {
		return nil, &s3types.NotFound{}
	}

	return &s3.HeadObjectOutput{Metadata: map[string]string{"sha256": sha}}, nil
}

func (f *fakeStemcellS3) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.puts++
	f.objects[*in.Key] = in.Metadata["sha256"]

	return &s3.PutObjectOutput{}, nil
}

func TestNewPrecompileCmd_StemcellSubcommand_Registered(t *testing.T) {
	t.Parallel()

	root := NewPrecompileCmd()

	var stemcellCmd *cobra.Command
	for _, c := range root.Commands() {
		if strings.HasPrefix(c.Use, "stemcell ") {
			stemcellCmd = c

			break
		}
	}

	if stemcellCmd == nil {
		t.Fatal("expected a 'stemcell' subcommand registered under precompile")
	}

	for _, flag := range []string{"bloc", "force", "dry-run", "sha1", "blobstore-endpoint", "blobstore-access-key", "blobstore-secret-key", "blobstore-ca-cert-file", "insecure-blobstore"} {
		if stemcellCmd.Flags().Lookup(flag) == nil {
			t.Errorf("stemcell subcommand missing --%s flag", flag)
		}
	}

	if err := stemcellCmd.Args(stemcellCmd, []string{"a", "b"}); err == nil {
		t.Error("expected Args to reject fewer than 3 positional args")
	}

	if err := stemcellCmd.Args(stemcellCmd, []string{"a", "b", "c"}); err != nil {
		t.Errorf("expected Args to accept exactly 3 positional args, got %v", err)
	}
}

func TestRunPrecompileStemcellCore_DryRun_CacheMiss_NonMutating(t *testing.T) {
	t.Parallel()

	s3c := newFakeStemcellS3()

	var out bytes.Buffer

	err := runPrecompileStemcellCore(context.Background(), s3c, nil, &out,
		"dev-ocf-bosh", "https://10.0.0.5:9000",
		"ubuntu-noble", "1.383", "https://bosh.io/d/stemcell", "",
		false, true)
	if err != nil {
		t.Fatalf("runPrecompileStemcellCore: %v", err)
	}

	if s3c.puts != 0 {
		t.Errorf("dry-run must not mutate the blobstore, got %d PutObject calls", s3c.puts)
	}

	if !strings.Contains(out.String(), "not cached") {
		t.Errorf("expected dry-run report to note the cache miss, got %q", out.String())
	}
}

func TestRunPrecompileStemcellCore_DryRun_CacheHit_NonMutating(t *testing.T) {
	t.Parallel()

	s3c := newFakeStemcellS3()
	s3c.objects["stemcells/ubuntu-noble-1.383.tgz"] = "cafe"

	var out bytes.Buffer

	// upstreamURL deliberately unreachable: a present-hit dry-run must never
	// attempt to fetch it.
	err := runPrecompileStemcellCore(context.Background(), s3c, nil, &out,
		"dev-ocf-bosh", "https://10.0.0.5:9000",
		"ubuntu-noble", "1.383", "http://127.0.0.1:0/unreachable", "",
		false, true)
	if err != nil {
		t.Fatalf("runPrecompileStemcellCore: %v", err)
	}

	if s3c.puts != 0 {
		t.Errorf("dry-run must not mutate the blobstore, got %d PutObject calls", s3c.puts)
	}

	if !strings.Contains(out.String(), "already cached") {
		t.Errorf("expected dry-run report to note the cache hit, got %q", out.String())
	}
}

func TestRunPrecompileStemcellCore_CacheMiss_FetchesAndCaches(t *testing.T) {
	t.Parallel()

	body := []byte("fake-stemcell-tarball-bytes")
	h := sha256.Sum256(body)
	wantSHA := hex.EncodeToString(h[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	s3c := newFakeStemcellS3()

	var out bytes.Buffer

	err := runPrecompileStemcellCore(context.Background(), s3c, srv.Client(), &out,
		"dev-ocf-bosh", "https://10.0.0.5:9000",
		"ubuntu-noble", "1.383", srv.URL, "",
		false, false)
	if err != nil {
		t.Fatalf("runPrecompileStemcellCore: %v", err)
	}

	if s3c.puts != 1 {
		t.Errorf("expected exactly 1 PutObject call on cache miss, got %d", s3c.puts)
	}

	if got := s3c.objects["stemcells/ubuntu-noble-1.383.tgz"]; got != wantSHA {
		t.Errorf("cached sha256 = %q, want %q", got, wantSHA)
	}

	if !strings.Contains(out.String(), wantSHA) {
		t.Errorf("expected report to include sha256 %q, got %q", wantSHA, out.String())
	}
}

func TestRunPrecompileStemcellCore_ValidatesRequiredInputs(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name, version, url string
	}{
		{"", "1.383", "https://example.com/x"},
		{"ubuntu-noble", "", "https://example.com/x"},
		{"ubuntu-noble", "1.383", ""},
	}

	for _, tc := range cases {
		var out bytes.Buffer

		err := runPrecompileStemcellCore(context.Background(), newFakeStemcellS3(), nil, &out,
			"dev-ocf-bosh", "https://10.0.0.5:9000", tc.name, tc.version, tc.url, "", false, true)
		if err == nil {
			t.Errorf("expected error for name=%q version=%q url=%q, got nil", tc.name, tc.version, tc.url)
		}
	}
}
