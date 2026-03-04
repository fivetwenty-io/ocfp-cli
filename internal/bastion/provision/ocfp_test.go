package provision

import (
	"context"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
)

func TestGenerateVaultInceptionScript_ContainsIdempotencyCheck(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Name: "520-aws-wayne",
	}

	om := NewOCFPManager("aws", cfg, nil)
	script := om.GenerateVaultInceptionScript(context.Background())

	// The fast-path idempotency check should be present
	idempotencyPatterns := []string{
		"tmux has-session -t",
		"vault status",
		"Inception vault already running - skipping",
	}

	for _, pattern := range idempotencyPatterns {
		if !strings.Contains(script, pattern) {
			t.Errorf("Expected script to contain idempotency pattern %q\nScript:\n%s", pattern, script)
		}
	}
}

func TestGenerateVaultInceptionScript_ContainsFallbackCheck(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Name: "520-aws-wayne",
	}

	om := NewOCFPManager("aws", cfg, nil)
	script := om.GenerateVaultInceptionScript(context.Background())

	// The fallback check on failure should still be present
	fallbackPatterns := []string{
		`safe target 2>&1 | grep -q 'inception\|production'`,
		"Vault already configured",
	}

	for _, pattern := range fallbackPatterns {
		if !strings.Contains(script, pattern) {
			t.Errorf("Expected script to contain fallback pattern %q\nScript:\n%s", pattern, script)
		}
	}
}

func TestGenerateVaultInceptionScript_SessionNameFromBloc(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Name: "520-aws-wayne",
	}

	om := NewOCFPManager("aws", cfg, nil)
	script := om.GenerateVaultInceptionScript(context.Background())

	// Should derive session name from OCFP_BLOC
	if !strings.Contains(script, `INCEPTION_SESSION="${OCFP_BLOC}-inception-vault"`) {
		t.Errorf("Expected script to derive session name from OCFP_BLOC\nScript:\n%s", script)
	}

	// Should also handle no-bloc case
	if !strings.Contains(script, `INCEPTION_SESSION="inception-vault"`) {
		t.Errorf("Expected script to handle no-bloc case\nScript:\n%s", script)
	}
}
