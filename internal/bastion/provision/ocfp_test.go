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

// TestGenerateOCFPConfigureScript_ContainsRepoInit verifies that the generated
// configure script includes genesis repo-init with the required flags for both
// the mgmt (bosh) and ocf (cf) deployment directories.
func TestGenerateOCFPConfigureScript_ContainsRepoInit(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Name: "ocfp-aws-us-east-1",
	}
	om := NewOCFPManager("aws", cfg, nil)
	script := om.GenerateOCFPConfigureScript(context.Background())

	requiredSubstrings := []string{
		"genesis repo-init",
		"--kit bosh",
		"--kit cf",
		"--ci-provider concourse",
		"--skip-vault",
	}

	for _, s := range requiredSubstrings {
		if !strings.Contains(script, s) {
			t.Errorf("expected configure script to contain %q\nscript:\n%s", s, script)
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

// TestGenerateOCFPConfigureScript_RepoInitBeforeSecretsProvider verifies ordering:
// genesis repo-init must appear before the per-deployment secrets-provider
// configuration (the `genesis embed` + .genesis/config rewrite) in the combined
// configure + secrets-provider output, reflecting the required bootstrap
// sequence (repo must exist before its secrets provider is configured).
//
// GenerateOCFPConfigureScript produces the repo-init section; the
// secrets-provider configuration lives in GenerateGenesisSecretsProvidersScript.
// Together they model the ordered bastion provisioning sequence, so the
// repo-init index must be lower.
func TestGenerateOCFPConfigureScript_RepoInitBeforeSecretsProvider(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Name: "ocfp-aws-us-east-1",
	}
	om := NewOCFPManager("aws", cfg, nil)

	configureScript := om.GenerateOCFPConfigureScript(context.Background())
	secretsScript := om.GenerateGenesisSecretsProvidersScript(context.Background())

	combined := configureScript + "\n" + secretsScript

	repoInitIdx := strings.Index(combined, "genesis repo-init")
	secretsProviderIdx := strings.Index(combined, "genesis embed")

	if repoInitIdx == -1 {
		t.Fatal("combined script missing 'genesis repo-init'")
	}

	if secretsProviderIdx == -1 {
		t.Fatal("combined script missing 'genesis embed'")
	}

	if repoInitIdx >= secretsProviderIdx {
		t.Errorf("genesis repo-init (pos %d) must appear before genesis embed (pos %d)",
			repoInitIdx, secretsProviderIdx)
	}
}

// TestGenerateOCFPConfigureScript_RepoInitFlags verifies that all required flags
// are present on the repo-init invocations: --kit bosh, --kit cf,
// --ci-provider concourse, --skip-vault.
func TestGenerateOCFPConfigureScript_RepoInitFlags(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Name: "ocfp-aws-us-east-1",
	}
	om := NewOCFPManager("aws", cfg, nil)
	script := om.GenerateOCFPConfigureScript(context.Background())

	tests := []struct {
		name     string
		contains string
	}{
		{"kit bosh flag", "--kit bosh"},
		{"kit cf flag", "--kit cf"},
		{"ci-provider concourse flag", "--ci-provider concourse"},
		{"skip-vault flag", "--skip-vault"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if !strings.Contains(script, tc.contains) {
				t.Errorf("configure script missing %q\nscript:\n%s", tc.contains, script)
			}
		})
	}
}

// TestGenerateOCFPConfigureScript_RepoInitIdempotent verifies the repo-init
// invocations include --force so repeated script runs do not prompt or fail when
// the deployment directory already exists.
func TestGenerateOCFPConfigureScript_RepoInitIdempotent(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Name: "ocfp-aws-us-east-1",
	}
	om := NewOCFPManager("aws", cfg, nil)
	script := om.GenerateOCFPConfigureScript(context.Background())

	if !strings.Contains(script, "--force") {
		t.Errorf("configure script repo-init calls must include --force for idempotency\nscript:\n%s", script)
	}
}

// TestGenerateOCFPConfigureScript_RepoInitDirectoryFlags verifies that repo-init
// uses --directory with deployment-specific paths so repos land in the correct
// subdirectory under DEPLOYMENTS_ROOT.
func TestGenerateOCFPConfigureScript_RepoInitDirectoryFlags(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Name: "ocfp-aws-us-east-1",
	}
	om := NewOCFPManager("aws", cfg, nil)
	script := om.GenerateOCFPConfigureScript(context.Background())

	if !strings.Contains(script, "--directory") {
		t.Errorf("configure script repo-init calls must include --directory flag\nscript:\n%s", script)
	}
}

// TestGenerateOCFPConfigureScript_GenesisCallSequence is an integration test that
// generates a full provision script with realistic inputs and asserts the complete
// genesis call sequence by line-number position, not just substring presence.
//
// Required order:
//  1. genesis repo-init --kit bosh  (mgmt repo, first)
//  2. genesis repo-init --kit cf    (ocf repo, second)
//  3. genesis embed (per-deployment secrets-provider config, after all repo-inits)
//
// Also asserts: no "genesis init" (old v3.1 command) appears anywhere.
func TestGenerateOCFPConfigureScript_GenesisCallSequence(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Name: "ocfp-aws-us-east-1",
	}
	om := NewOCFPManager("aws", cfg, nil)

	configureScript := om.GenerateOCFPConfigureScript(context.Background())
	secretsScript := om.GenerateGenesisSecretsProvidersScript(context.Background())
	combined := configureScript + "\n" + secretsScript

	lines := strings.Split(combined, "\n")

	// isComment reports whether a shell script line is a comment or blank.
	// Comment lines starting with '#' are documentation/annotations, not commands.
	// Ordering assertions must target executable lines only.
	isComment := func(line string) bool {
		trimmed := strings.TrimSpace(line)
		return trimmed == "" || strings.HasPrefix(trimmed, "#")
	}

	// lineOfCommand returns the 1-based line number of the first non-comment line
	// containing substr, or -1 if not found. Using line numbers (not byte offsets)
	// gives clearer failure messages and is immune to variable-length content before
	// the match. Skipping comment lines prevents false positives from inline
	// documentation that references command names (e.g. "# runs genesis init").
	lineOfCommand := func(substr string) int {
		for i, line := range lines {
			if !isComment(line) && strings.Contains(line, substr) {
				return i + 1
			}
		}

		return -1
	}

	// allCommandLines returns all 1-based line numbers of non-comment lines
	// containing substr.
	allCommandLines := func(substr string) []int {
		var hits []int

		for i, line := range lines {
			if !isComment(line) && strings.Contains(line, substr) {
				hits = append(hits, i+1)
			}
		}

		return hits
	}

	// 1. Both repo-init calls must be present on executable lines.
	repoInitBoshLine := lineOfCommand("genesis repo-init")
	if repoInitBoshLine == -1 {
		t.Fatal("combined script missing executable 'genesis repo-init'")
	}

	// Find the executable line that carries --kit bosh (must exist).
	kitBoshLine := lineOfCommand("--kit bosh")
	if kitBoshLine == -1 {
		t.Fatal("combined script missing executable '--kit bosh'")
	}

	// Find the executable line that carries --kit cf (must exist, after bosh block).
	kitCFLine := lineOfCommand("--kit cf")
	if kitCFLine == -1 {
		t.Fatal("combined script missing executable '--kit cf'")
	}

	// 2. bosh repo-init block must precede cf repo-init block.
	if kitBoshLine >= kitCFLine {
		t.Errorf("genesis repo-init --kit bosh (line %d) must appear before genesis repo-init --kit cf (line %d)",
			kitBoshLine, kitCFLine)
	}

	// 3. The per-deployment secrets-provider config (genesis embed + .genesis/config
	// rewrite) must appear after both repo-init blocks. This logic lives in
	// GenerateGenesisSecretsProvidersScript; the repo-init calls live in
	// GenerateOCFPConfigureScript. The combined script models the ordered bastion
	// provisioning sequence.
	secretsProviderLine := lineOfCommand("genesis embed")
	if secretsProviderLine == -1 {
		t.Fatal("combined script missing executable 'genesis embed'")
	}

	if kitBoshLine >= secretsProviderLine {
		t.Errorf("genesis repo-init --kit bosh (line %d) must appear before genesis embed (line %d)",
			kitBoshLine, secretsProviderLine)
	}

	if kitCFLine >= secretsProviderLine {
		t.Errorf("genesis repo-init --kit cf (line %d) must appear before genesis embed (line %d)",
			kitCFLine, secretsProviderLine)
	}

	// 4. "genesis init" (old v3.1 command) must not appear on any executable line.
	// Comments referencing "genesis init" in documentation context are allowed;
	// only actual invocations are rejected.
	genesisInitLines := allCommandLines("genesis init")
	for _, lineNum := range genesisInitLines {
		line := lines[lineNum-1]
		// "genesis repo-init" contains the substring "genesis init"; skip those.
		if !strings.Contains(line, "genesis repo-init") {
			t.Errorf("line %d contains deprecated 'genesis init' command (old v3.1): %q", lineNum, line)
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

// TestGenerateOCFPConfigureScript_RepoInitBeforeOCFPConfigure verifies that the
// genesis repo-init section appears before the ocfp configure deployments section
// within GenerateOCFPConfigureScript. Env files must be written into an
// already-initialised genesis repo.
func TestGenerateOCFPConfigureScript_RepoInitBeforeOCFPConfigure(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Name: "ocfp-aws-us-east-1",
	}
	om := NewOCFPManager("aws", cfg, nil)
	script := om.GenerateOCFPConfigureScript(context.Background())

	repoInitIdx := strings.Index(script, "genesis repo-init")
	ocfpConfigureIdx := strings.Index(script, "ocfp configure deployments")

	if repoInitIdx == -1 {
		t.Fatal("script missing 'genesis repo-init'")
	}

	if ocfpConfigureIdx == -1 {
		// ocfp configure deployments block may be absent when OCFP_CLI_PATH is empty —
		// check for the fallback log_warning path instead.
		ocfpConfigureIdx = strings.Index(script, "configure deployments")
		if ocfpConfigureIdx == -1 {
			t.Fatal("script missing 'configure deployments' section")
		}
	}

	if repoInitIdx >= ocfpConfigureIdx {
		t.Errorf("genesis repo-init (pos %d) must appear before configure deployments (pos %d)",
			repoInitIdx, ocfpConfigureIdx)
	}
}
