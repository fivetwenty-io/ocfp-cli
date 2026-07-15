package provision

import (
	"context"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
)

// TestSystemEnvironmentVars_GenesisEnvironmentPresent verifies that
// GENESIS_ENVIRONMENT is exported in the system environment script and
// GENESIS_ENV (deprecated bare form) is NOT present.
func TestSystemEnvironmentVars_GenesisEnvironmentPresent(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Name: "my-bloc",
	}
	em := NewEnvironmentManager("aws", cfg)
	script := em.GenerateSystemEnvironmentScript(context.Background())

	if !strings.Contains(script, "GENESIS_ENVIRONMENT=") {
		t.Errorf("expected GENESIS_ENVIRONMENT= in system env script, got:\n%s", script)
	}
}

// TestSystemEnvironmentVars_GenesisEnvBareAbsent verifies GENESIS_ENV (bare,
// no IRONMENT suffix) is NOT present in the system environment script.
func TestSystemEnvironmentVars_GenesisEnvBareAbsent(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Name: "my-bloc",
	}
	em := NewEnvironmentManager("aws", cfg)
	script := em.GenerateSystemEnvironmentScript(context.Background())

	// Match GENESIS_ENV not immediately followed by [A-Z_] — i.e., the bare form.
	for _, line := range strings.Split(script, "\n") {
		bare := strings.Contains(line, "GENESIS_ENV") && !strings.Contains(line, "GENESIS_ENVIRONMENT")
		if bare {
			t.Errorf("found bare GENESIS_ENV (not GENESIS_ENVIRONMENT) in line: %q", line)
		}
	}
}

// TestSystemEnvironmentVars_GenesisEnvironmentValue verifies the exported value
// equals the bloc name.
func TestSystemEnvironmentVars_GenesisEnvironmentValue(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Name: "520-aws-wayne",
	}
	em := NewEnvironmentManager("aws", cfg)
	script := em.GenerateSystemEnvironmentScript(context.Background())

	expected := "GENESIS_ENVIRONMENT=520-aws-wayne"
	if !strings.Contains(script, expected) {
		t.Errorf("expected %q in system env script, got:\n%s", expected, script)
	}
}

// TestSystemEnvironmentVars_AWSCABundlePresentForInternalCA verifies
// AWS_CA_BUNDLE is exported (pointed at the merged system bundle the
// bloc_ca_trust bastion phase produces) when artifacts is enabled in
// internal-ca TLS mode — the one mode where the AWS CLI otherwise fails to
// verify the artifacts endpoint's certificate.
func TestSystemEnvironmentVars_AWSCABundlePresentForInternalCA(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{Name: "bloc1"}
	cfg.Artifacts.Enabled = true
	cfg.Artifacts.TLS.Mode = config.ArtifactsTLSModeInternalCA

	em := NewEnvironmentManager("aws", cfg)
	vars := em.GetSystemEnvironmentVarsForPreview()

	got, ok := vars["AWS_CA_BUNDLE"]
	if !ok {
		t.Fatalf("expected AWS_CA_BUNDLE to be present for internal-ca mode, vars: %+v", vars)
	}

	if got != "/etc/ssl/certs/ca-certificates.crt" {
		t.Errorf("AWS_CA_BUNDLE = %q, want /etc/ssl/certs/ca-certificates.crt", got)
	}
}

// TestSystemEnvironmentVars_AWSCABundleAbsentForSelfSigned verifies
// AWS_CA_BUNDLE is NOT exported for self-signed mode: bloc_ca_trust never
// installs a self-signed leaf into the system trust store, so there is
// nothing the bundle override would add over the AWS CLI's own bundled CAs.
func TestSystemEnvironmentVars_AWSCABundleAbsentForSelfSigned(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{Name: "bloc1"}
	cfg.Artifacts.Enabled = true
	cfg.Artifacts.TLS.Mode = config.ArtifactsTLSModeSelfSigned

	em := NewEnvironmentManager("aws", cfg)
	vars := em.GetSystemEnvironmentVarsForPreview()

	if _, ok := vars["AWS_CA_BUNDLE"]; ok {
		t.Errorf("expected AWS_CA_BUNDLE absent for self-signed mode, vars: %+v", vars)
	}
}

// TestSystemEnvironmentVars_AWSCABundleAbsentWhenArtifactsDisabled verifies
// AWS_CA_BUNDLE is NOT exported when the artifacts feature itself is
// disabled, regardless of whatever tls.mode is configured underneath it.
func TestSystemEnvironmentVars_AWSCABundleAbsentWhenArtifactsDisabled(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{Name: "bloc1"}
	cfg.Artifacts.Enabled = false
	cfg.Artifacts.TLS.Mode = config.ArtifactsTLSModeInternalCA

	em := NewEnvironmentManager("aws", cfg)
	vars := em.GetSystemEnvironmentVarsForPreview()

	if _, ok := vars["AWS_CA_BUNDLE"]; ok {
		t.Errorf("expected AWS_CA_BUNDLE absent when artifacts disabled, vars: %+v", vars)
	}
}
