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
