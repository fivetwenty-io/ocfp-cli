package provision

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// --- A4: defaultDeploymentNames conditionalization ---

func TestDefaultDeploymentNamesFor_OpenbaoDefault(t *testing.T) {
	t.Parallel()

	names := defaultDeploymentNamesFor("openbao")

	for _, n := range names {
		if n == "vault" {
			t.Error("defaultDeploymentNamesFor(openbao) must not contain 'vault'")
		}
	}

	found := false

	for _, n := range names {
		if n == "openbao" {
			found = true

			break
		}
	}

	if !found {
		t.Error("defaultDeploymentNamesFor(openbao) must contain 'openbao'")
	}
}

func TestDefaultDeploymentNamesFor_VaultOptIn(t *testing.T) {
	t.Parallel()

	names := defaultDeploymentNamesFor("vault")

	for _, n := range names {
		if n == "openbao" {
			t.Error("defaultDeploymentNamesFor(vault) must not contain 'openbao'")
		}
	}

	found := false

	for _, n := range names {
		if n == "vault" {
			found = true

			break
		}
	}

	if !found {
		t.Error("defaultDeploymentNamesFor(vault) must contain 'vault'")
	}
}

func TestGenerateOCFPConfigureScript_OpenbaoDefault(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{Name: "test-bloc"}
	om := NewOCFPManager("aws", cfg, nil)
	script := om.GenerateOCFPConfigureScript(context.Background())

	if strings.Contains(script, `"vault"`) {
		t.Error("configure script with default backend must not contain deployment name 'vault'")
	}

	if !strings.Contains(script, `"openbao"`) {
		t.Error("configure script with default backend must contain deployment name 'openbao'")
	}
}

func TestGenerateOCFPConfigureScript_VaultOptIn(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{Name: "test-bloc", SecretsBackend: "vault"}
	om := NewOCFPManager("aws", cfg, nil)
	script := om.GenerateOCFPConfigureScript(context.Background())

	if !strings.Contains(script, `"vault"`) {
		t.Error("configure script with vault backend must contain deployment name 'vault'")
	}

	if strings.Contains(script, `"openbao"`) {
		t.Error("configure script with vault backend must not contain deployment name 'openbao'")
	}
}

// --- A6: tool verification script required tools ---

// vault and ruby are required regardless of the secrets backend: the
// inception vault runs on the vault binary, and `bosh create-env` renders
// CPI job ERB templates with ruby.
func TestGenerateOCFPToolVerificationScript_AlwaysChecksVaultBaoRuby(t *testing.T) {
	t.Parallel()

	for _, backend := range []string{"", "openbao", "vault"} {
		backend := backend
		t.Run("backend="+backend, func(t *testing.T) {
			t.Parallel()

			cfg := &config.Config{Name: "test-bloc", SecretsBackend: backend}
			om := NewOCFPManager("aws", cfg, nil)
			script := om.GenerateOCFPToolVerificationScript(context.Background())

			for _, tool := range []string{"vault", "bao", "ruby"} {
				if !strings.Contains(script, "command -v "+tool) {
					t.Errorf("tool verification must always check for %q", tool)
				}
			}
		})
	}
}

// TestBastionInceptionPort_StartAndConsumersAgree pins the coupling that broke:
// for a given bloc, the port the bastion vault is started on and the port baked
// into the .genesis/config secrets_provider rewrite must be the same value.
// Independent literals let them drift silently, and the drift only surfaces
// several phases later as a Genesis secrets failure, far from its cause.
func TestBastionInceptionPort_StartAndConsumersAgree(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name         string
		blocName     string
		configPort   int
		expectedPort int
	}{
		{
			// Derived: no config pin, so both sides must land on the port the
			// bastion's own ocfp will derive from the bloc name.
			name:         "derived port",
			blocName:     "ocfp-lab-drhu",
			configPort:   0,
			expectedPort: config.DeterministicInceptionVaultPort("ocfp-lab-drhu"),
		},
		{
			// Pinned: the live reference bloc pins 8234 in config, and both
			// sides must honour the pin rather than the derived port.
			name:         "config-pinned port",
			blocName:     "ocfp-lab-nabramovitz",
			configPort:   8234,
			expectedPort: 8234,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := &config.Config{Name: tc.blocName, VaultInceptionPort: tc.configPort}
			om := NewOCFPManager("pve", cfg, nil)

			startScript := om.GenerateVaultInceptionScript(context.Background())
			rewrite := strings.Join(om.secretsProviderRewriteSnippet(), "\n")

			assert.Contains(t, startScript, fmt.Sprintf("VAULT_PORT=%d", tc.expectedPort),
				"vault start must use the bloc's resolved inception port")
			assert.Contains(t, rewrite, fmt.Sprintf("http://127.0.0.1:%d", tc.expectedPort),
				"secrets_provider rewrite must target the port the vault actually binds")
		})
	}
}

// TestBastionInceptionPort_NoHardcodedLegacyPort guards the regression
// directly: a derived bloc must produce scripts free of the legacy port, which
// is what the bastion consumers previously assumed while the vault bound
// something else entirely.
func TestBastionInceptionPort_NoHardcodedLegacyPort(t *testing.T) {
	t.Parallel()

	blocName := "ocfp-lab-drhu"
	require.NotEqual(t, config.LegacyInceptionVaultPort,
		config.DeterministicInceptionVaultPort(blocName),
		"test bloc must derive a port different from the legacy one")

	om := NewOCFPManager("pve", &config.Config{Name: blocName}, nil)
	legacy := strconv.Itoa(config.LegacyInceptionVaultPort)

	assert.NotContains(t, om.GenerateVaultInceptionScript(context.Background()), legacy,
		"vault inception script must not hardcode the legacy port")
	assert.NotContains(t, strings.Join(om.secretsProviderRewriteSnippet(), "\n"), legacy,
		"secrets_provider rewrite must not hardcode the legacy port")
}

// TestEnvironmentScript_ExportsResolvedInceptionPort covers the third place the
// port appears. The profile export is not what the vault-start path relies on
// any more, but an interactive `ocfp` on the bastion does read it, and the env
// var outranks every other source — so a stale legacy value there would send
// interactive commands to a port the vault never bound.
func TestEnvironmentScript_ExportsResolvedInceptionPort(t *testing.T) {
	t.Parallel()

	blocName := "ocfp-lab-drhu"
	derived := config.DeterministicInceptionVaultPort(blocName)
	require.NotEqual(t, config.LegacyInceptionVaultPort, derived,
		"test bloc must derive a port different from the legacy one")

	provCfg := NewConfig("pve", &config.Config{Name: blocName}, nil)
	script := provCfg.generateEnvironmentScript()

	assert.Contains(t, script, fmt.Sprintf("export %s='%d'", config.InceptionVaultPortEnvVar, derived),
		"profile must export the port the bloc actually resolves to")
}

// TestGenerateGenesisSecretsProvidersScript_GatesOnInceptionTarget verifies the
// script only points deployments at the inception vault while that vault is the
// bloc's source of truth, and otherwise clears the block. The inception vault is
// torn down at the end of every init, and genesis 3.2 fails hard on an
// unreachable secrets_provider, so an unconditional rewrite breaks every
// manifest render on an established bloc.
func TestGenerateGenesisSecretsProvidersScript_GatesOnInceptionTarget(t *testing.T) {
	t.Parallel()

	script := NewOCFPManager("pve", &config.Config{Name: "ocfp-lab-example"}, nil).
		GenerateGenesisSecretsProvidersScript(context.Background())

	for _, want := range []string{
		"INCEPTION_ACTIVE=no",
		`BLOC_VAULT_TARGET="${OCFP_BLOC}-mgmt"`,
		`safe targets 2>/dev/null | grep -q "$BLOC_VAULT_TARGET"`,
		"safe target 2>/dev/null | grep -q 'inception'",
		`if [ "$INCEPTION_ACTIVE" != yes ]; then`,
		"genesis secrets-provider -c",
		"yq -i 'del(.secrets_provider)'",
		`elif safe target "$BLOC_VAULT_TARGET" >/dev/null 2>&1; then`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("expected secrets-provider script to contain %q\ngot:\n%s", want, script)
		}
	}

	// The rewrite that pins deployments to inception must sit behind the gate.
	gate := strings.Index(script, `if [ "$INCEPTION_ACTIVE" != yes ]; then`)
	rewrite := strings.Index(script, `.secrets_provider.alias = "inception"`)

	if gate < 0 || rewrite < 0 || gate > rewrite {
		t.Errorf("expected the inception rewrite to follow the gate (gate=%d rewrite=%d)", gate, rewrite)
	}
}

// TestGenerateOCFPConfigureScript_DoesNotReinvokeConfigure guards against the
// script re-entering the command that produced it. `ocfp configure` provisions
// the bastion, and provisioning generates and runs this script, so a trailing
// `ocfp configure ...` call made the two invoke each other without bound: a
// fresh process roughly every 53 seconds, 51 of them alive after 22 minutes,
// none exiting, and `ocfp init bastion` never returning.
func TestGenerateOCFPConfigureScript_DoesNotReinvokeConfigure(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Name: "ocfp-lab-example",
	}
	om := NewOCFPManager("pve", cfg, nil)
	script := om.GenerateOCFPConfigureScript(context.Background())

	if strings.Contains(script, "configure deployments") {
		t.Errorf("configure script calls `ocfp configure deployments`, which re-enters bastion provisioning\nScript:\n%s", script)
	}

	if strings.Contains(script, "OCFP_CLI_PATH} configure") {
		t.Errorf("configure script invokes `ocfp configure`, which re-enters bastion provisioning\nScript:\n%s", script)
	}
}
