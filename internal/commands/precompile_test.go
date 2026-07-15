package commands

import (
	"errors"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/artifacts"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/vault"
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
