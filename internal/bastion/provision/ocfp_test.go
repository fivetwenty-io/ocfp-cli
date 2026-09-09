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

// repoInitScriptLines splits a script into lines and returns a lookup that
// gives the 1-based number of the first executable (non-comment, non-blank)
// line containing substr, or -1. Comments describe the commands they sit
// next to and would otherwise produce false matches.
func repoInitScriptLines(script string) ([]string, func(string) int) {
	lines := strings.Split(script, "\n")

	isComment := func(line string) bool {
		trimmed := strings.TrimSpace(line)

		return trimmed == "" || strings.HasPrefix(trimmed, "#")
	}

	lineOf := func(substr string) int {
		for i, line := range lines {
			if !isComment(line) && strings.Contains(line, substr) {
				return i + 1
			}
		}

		return -1
	}

	return lines, lineOf
}

// TestGenerateOCFPConfigureScript_ContainsRepoInit verifies the configure
// script runs genesis repo-init once per dev-mode deployment directory, links
// the staged kit with -l, defers vault, and passes the bare kit name so the
// repo's deployment_type follows from it.
func TestGenerateOCFPConfigureScript_ContainsRepoInit(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Name: "ocfp-aws-us-east-1",
	}
	om := NewOCFPManager("aws", cfg, nil)
	script := om.GenerateOCFPConfigureScript(context.Background())

	requiredSubstrings := []string{
		`for deployment in "${DEV_DEPLOYMENTS[@]}"; do`,
		`genesis repo-init -l "${KIT_DIR}" --skip-vault --no-commit "${deployment}"`,
		`DEV_DEPLOYMENTS=("bosh" "openbao" "concourse" "cf"`,
	}

	for _, s := range requiredSubstrings {
		if !strings.Contains(script, s) {
			t.Errorf("expected configure script to contain %q\nscript:\n%s", s, script)
		}
	}
}

// TestGenerateOCFPConfigureScript_RepoInitRejectedFlagsAbsent verifies the
// repo-init invocation carries none of the flags that the maintained genesis
// rejects or that would destroy operator data: --ci-provider is unknown,
// -d/--directory must be a bare name, and -f/--force deletes the target.
// Nothing may name mgmt or ocf directories as repo roots either.
func TestGenerateOCFPConfigureScript_RepoInitRejectedFlagsAbsent(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Name: "ocfp-aws-us-east-1",
	}
	om := NewOCFPManager("aws", cfg, nil)
	script := om.GenerateOCFPConfigureScript(context.Background())

	_, lineOf := repoInitScriptLines(script)

	for _, forbidden := range []string{"--ci-provider", "--directory", "--force", `/mgmt"`, `/ocf"`} {
		if line := lineOf(forbidden); line != -1 {
			t.Errorf("configure script line %d still uses %q\nscript:\n%s", line, forbidden, script)
		}
	}

	lines, _ := repoInitScriptLines(script)
	for i, line := range lines {
		if !strings.Contains(line, "genesis repo-init") || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}

		for _, short := range []string{" -f ", " -d ", " -f\\", " -d\\"} {
			if strings.Contains(line, short) {
				t.Errorf("repo-init line %d carries %q: %s", i+1, strings.TrimSpace(short), line)
			}
		}
	}
}

// TestGenerateOCFPConfigureScript_RepoInitStagesAndMoves verifies the
// staging sequence: repo-init runs inside a mktemp directory, only .genesis
// is moved into the deployment directory, the dev symlink is created only
// when none exists, and the staging directory is removed.
func TestGenerateOCFPConfigureScript_RepoInitStagesAndMoves(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Name: "ocfp-aws-us-east-1",
	}
	om := NewOCFPManager("aws", cfg, nil)
	script := om.GenerateOCFPConfigureScript(context.Background())

	_, lineOf := repoInitScriptLines(script)

	ordered := []string{
		`STAGE_DIR=$(mktemp -d)`,
		`cd "${STAGE_DIR}" && GIT_AUTHOR_NAME="${REPO_INIT_GIT_NAME}" GIT_AUTHOR_EMAIL="${REPO_INIT_GIT_EMAIL}" genesis repo-init`,
		`if [ ! -f "${STAGE_DIR}/${deployment}/.genesis/config" ]`,
		`mkdir -p "${DEPLOY_PATH}"`,
		`mv "${STAGE_DIR}/${deployment}/.genesis" "${DEPLOY_PATH}/.genesis"`,
		`if [ ! -e "${DEPLOY_PATH}/dev" ]`,
		`ln -s "${KIT_DIR}" "${DEPLOY_PATH}/dev"`,
	}

	previous := 0

	for _, step := range ordered {
		line := lineOf(step)
		if line == -1 {
			t.Fatalf("configure script missing executable %q\nscript:\n%s", step, script)
		}

		if line <= previous {
			t.Errorf("%q (line %d) must follow the previous step (line %d)", step, line, previous)
		}

		previous = line
	}

	// The failure branch removes the staging directory before exiting, so
	// the cleanup that matters is the last one, and it must follow the move.
	if last := strings.LastIndex(script, `rm -rf "${STAGE_DIR}"`); last < strings.Index(script, `mv "${STAGE_DIR}/${deployment}/.genesis"`) {
		t.Errorf("staging directory is removed before .genesis is moved out of it\nscript:\n%s", script)
	}
}

// TestGenerateOCFPConfigureScript_RepoInitSkipsAndFails verifies the loop
// skips a directory that already has .genesis/config, skips a deployment whose
// kit is not staged, and turns a failed repo-init into a phase failure rather
// than a warning.
func TestGenerateOCFPConfigureScript_RepoInitSkipsAndFails(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Name: "ocfp-aws-us-east-1",
	}
	om := NewOCFPManager("aws", cfg, nil)
	script := om.GenerateOCFPConfigureScript(context.Background())

	lines, lineOf := repoInitScriptLines(script)

	for _, guard := range []string{
		`if [ -f "${DEPLOY_PATH}/.genesis/config" ]; then`,
		`if [ ! -d "${KIT_DIR}" ]; then`,
	} {
		if lineOf(guard) == -1 {
			t.Errorf("configure script missing guard %q\nscript:\n%s", guard, script)
		}
	}

	failLine := lineOf(`log_error "genesis repo-init failed for ${deployment}"`)
	if failLine == -1 {
		t.Fatalf("configure script does not log_error on repo-init failure\nscript:\n%s", script)
	}

	exitSeen := false

	for _, line := range lines[failLine : failLine+3] {
		if strings.TrimSpace(line) == "exit 1" {
			exitSeen = true
		}
	}

	if !exitSeen {
		t.Errorf("repo-init failure must exit non-zero within the lines after the log_error\nscript:\n%s", script)
	}

	for i, line := range lines {
		if strings.Contains(line, "log_warning") && strings.Contains(line, "repo-init") {
			t.Errorf("line %d downgrades a repo-init failure to a warning: %s", i+1, line)
		}
	}
}

// TestGenerateOCFPConfigureScript_GenesisCallSequence generates the configure
// and secrets-provider scripts together and asserts the genesis call order by
// executable line: repo-init before genesis embed, bosh ahead of cf in the
// deployment list, and no `genesis init` (the v3.1 command) anywhere.
func TestGenerateOCFPConfigureScript_GenesisCallSequence(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Name: "ocfp-aws-us-east-1",
	}
	om := NewOCFPManager("aws", cfg, nil)

	configureScript := om.GenerateOCFPConfigureScript(context.Background())
	secretsScript := om.GenerateGenesisSecretsProvidersScript(context.Background())
	combined := configureScript + "\n" + secretsScript

	lines, lineOf := repoInitScriptLines(combined)

	repoInitLine := lineOf("genesis repo-init")
	if repoInitLine == -1 {
		t.Fatal("combined script missing executable 'genesis repo-init'")
	}

	embedLine := lineOf("genesis embed")
	if embedLine == -1 {
		t.Fatal("combined script missing executable 'genesis embed'")
	}

	if repoInitLine >= embedLine {
		t.Errorf("genesis repo-init (line %d) must appear before genesis embed (line %d)", repoInitLine, embedLine)
	}

	devLine := lineOf("DEV_DEPLOYMENTS=(")
	if devLine == -1 {
		t.Fatal("combined script missing DEV_DEPLOYMENTS array")
	}

	devList := lines[devLine-1]
	if strings.Index(devList, `"bosh"`) > strings.Index(devList, `"cf"`) {
		t.Errorf("bosh must be initialised before cf: %s", devList)
	}

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		if strings.Contains(line, "genesis init") && !strings.Contains(line, "genesis repo-init") {
			t.Errorf("line %d contains deprecated 'genesis init' command (old v3.1): %q", i+1, line)
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
//
// safe reports its target and target list on stderr. Reading stdout alone
// never matched, so the gate saw no inception target, took the clear branch,
// and left every fresh repo-init without a provider. Both streams are read.
func TestGenerateGenesisSecretsProvidersScript_GatesOnInceptionTarget(t *testing.T) {
	t.Parallel()

	script := NewOCFPManager("pve", &config.Config{Name: "ocfp-lab-example"}, nil).
		GenerateGenesisSecretsProvidersScript(context.Background())

	for _, want := range []string{
		"INCEPTION_ACTIVE=no",
		`BLOC_VAULT_TARGET="${OCFP_BLOC}-mgmt"`,
		`safe targets 2>&1 | grep -q "$BLOC_VAULT_TARGET"`,
		`CURRENT_VAULT_TARGET="$(safe target 2>&1 | sed -n 's/^Currently targeting \(.*\) at .*$/\1/p' | head -n 1)"`,
		`INCEPTION_TARGET="$CURRENT_VAULT_TARGET"`,
		`if [ "$INCEPTION_ACTIVE" != yes ]; then`,
		"genesis secrets-provider -c",
		"printf 'secrets_provider: (( prune ))\\n'",
		`genesis secrets-provider "$INCEPTION_TARGET"`,
		`graft merge "$GENESIS_CONFIG" "$sp_fragment"`,
		`elif safe target "$BLOC_VAULT_TARGET" >/dev/null 2>&1; then`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("expected secrets-provider script to contain %q\ngot:\n%s", want, script)
		}
	}

	for _, reject := range []string{
		"2>/dev/null | grep",
		"yq ",
		"awk ",
	} {
		if strings.Contains(script, reject) {
			t.Errorf("secrets-provider script must not contain %q\ngot:\n%s", reject, script)
		}
	}

	// The rewrite that pins deployments to inception must sit behind the gate.
	gate := strings.Index(script, `if [ "$INCEPTION_ACTIVE" != yes ]; then`)
	rewrite := strings.Index(script, `genesis secrets-provider "$INCEPTION_TARGET"`)

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

// TestGenerateOCFPConfigureScript_RepoInitGitIdentity pins that genesis
// repo-init always gets a git author identity on its own invocation, taken
// from bastion.git.user when both keys are set and otherwise derived from the
// bloc name, with a warning only in the fallback case. An apostrophe in the
// configured name proves the single-quote escaping.
func TestGenerateOCFPConfigureScript_RepoInitGitIdentity(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		user         config.GitUser
		wantName     string
		wantEmail    string
		wantFallback bool
	}{
		{
			name:      "configured identity",
			user:      config.GitUser{Name: "Sinéad O'Connor", Email: "sinead@example.com"},
			wantName:  `REPO_INIT_GIT_NAME='Sinéad O'\''Connor'`,
			wantEmail: `REPO_INIT_GIT_EMAIL='sinead@example.com'`,
		},
		{
			name:         "no identity configured",
			user:         config.GitUser{},
			wantName:     `REPO_INIT_GIT_NAME='ocfp bastion (ocfp-cf1-lab)'`,
			wantEmail:    `REPO_INIT_GIT_EMAIL='ocfp-bastion@ocfp-cf1-lab.invalid'`,
			wantFallback: true,
		},
		{
			name:         "name without email falls back as a whole",
			user:         config.GitUser{Name: "Only Name"},
			wantName:     `REPO_INIT_GIT_NAME='ocfp bastion (ocfp-cf1-lab)'`,
			wantEmail:    `REPO_INIT_GIT_EMAIL='ocfp-bastion@ocfp-cf1-lab.invalid'`,
			wantFallback: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := &config.Config{Name: "ocfp-cf1-lab"}
			cfg.Bastion.Git.User = tc.user
			om := NewOCFPManager("pve", cfg, nil)
			script := om.GenerateOCFPConfigureScript(context.Background())
			lines, lineOf := repoInitScriptLines(script)

			for _, want := range []string{tc.wantName, tc.wantEmail} {
				if !strings.Contains(script, want) {
					t.Errorf("configure script missing %q\nscript:\n%s", want, script)
				}
			}

			invocation := `GIT_AUTHOR_NAME="${REPO_INIT_GIT_NAME}" GIT_AUTHOR_EMAIL="${REPO_INIT_GIT_EMAIL}" genesis repo-init`
			if lineOf(invocation) < 0 {
				t.Errorf("repo-init is not invoked with the exported identity\nscript:\n%s", script)
			}

			for i, line := range lines {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "export GIT_AUTHOR") || strings.HasPrefix(trimmed, "git config") {
					t.Errorf("line %d leaks the repo-init identity beyond the genesis invocation: %s", i+1, line)
				}
			}

			warnLine := lineOf(`log_warning 'Bloc config has no bastion.git.user`)
			if tc.wantFallback != (warnLine >= 0) {
				t.Errorf("fallback warning present = %v, want %v\nscript:\n%s", warnLine >= 0, tc.wantFallback, script)
			}

			if tc.wantFallback && warnLine > lineOf(invocation) {
				t.Errorf("fallback warning at line %d must precede the repo-init loop", warnLine)
			}
		})
	}
}

// TestShellSingleQuote pins the bash single-quote escaping used for values
// that reach the configure script from operator config.
func TestShellSingleQuote(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"":               `''`,
		"plain":          `'plain'`,
		"O'Connor":       `'O'\''Connor'`,
		`$HOME "x" \n`:   `'$HOME "x" \n'`,
		"two ' quotes '": `'two '\'' quotes '\'''`,
	}

	for in, want := range cases {
		if got := shellSingleQuote(in); got != want {
			t.Errorf("shellSingleQuote(%q) = %s, want %s", in, got, want)
		}
	}
}
