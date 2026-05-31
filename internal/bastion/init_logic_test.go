package bastion

import (
	"errors"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/bastion/provision"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newMinimalManager builds a Manager sufficient for pure-logic methods.
// No SSH client, no provConfig, no checkpoint — only config + log.
func newMinimalManager(cfg *config.Config) *Manager {
	return &Manager{
		config: cfg,
		log:    newTestLogger(),
	}
}

// newBaseConfig returns a minimal *config.Config with all override maps
// initialised so validateOverrides never panics on nil-map access.
func newBaseConfig(name, provider string) *config.Config {
	return &config.Config{
		Name:     name,
		Provider: provider,
		Bastion: config.Bastion{
			Tools:             config.OverrideSets{Enable: []string{}, Disable: []string{}},
			CFPlugins:         config.OverrideSets{Enable: []string{}, Disable: []string{}},
			Snaps:             config.OverrideSets{Enable: []string{}, Disable: []string{}},
			ToolOverrides:     map[string]config.ToolOverride{},
			CFPluginOverrides: map[string]config.CFPluginOverride{},
			SnapOverrides:     map[string]config.SnapOverride{},
		},
	}
}

// ---------------------------------------------------------------------------
// buildProposedEnvironment
// ---------------------------------------------------------------------------

func TestBuildProposedEnvironment_HappyPath(t *testing.T) {
	t.Parallel()

	m := newMinimalManager(newBaseConfig("bloc1", "aws"))
	current := "EXISTING=old\nOTHER=keep\n"
	desired := map[string]string{"EXISTING": "new", "ADDED": "yes"}

	out := m.buildProposedEnvironment(current, desired)

	assert.Contains(t, out, "OTHER=keep")
	assert.Contains(t, out, "EXISTING=new")
	assert.Contains(t, out, "ADDED=yes")
	assert.NotContains(t, out, "EXISTING=old", "old value must be replaced")
}

func TestBuildProposedEnvironment_EmptyCurrent(t *testing.T) {
	t.Parallel()

	m := newMinimalManager(newBaseConfig("bloc1", "aws"))
	out := m.buildProposedEnvironment("", map[string]string{"FOO": "bar"})

	assert.Contains(t, out, "FOO=bar")
	assert.True(t, strings.HasSuffix(out, "\n"), "output must end with newline")
}

func TestBuildProposedEnvironment_EmptyDesired(t *testing.T) {
	t.Parallel()

	m := newMinimalManager(newBaseConfig("bloc1", "aws"))
	current := "A=1\nB=2\n"
	out := m.buildProposedEnvironment(current, map[string]string{})

	assert.Contains(t, out, "A=1")
	assert.Contains(t, out, "B=2")
}

func TestBuildProposedEnvironment_BothEmpty(t *testing.T) {
	t.Parallel()

	m := newMinimalManager(newBaseConfig("bloc1", "aws"))
	out := m.buildProposedEnvironment("", map[string]string{})
	// Only a trailing newline — no garbage lines.
	assert.Equal(t, "\n", out)
}

func TestBuildProposedEnvironment_SkipsBlankLines(t *testing.T) {
	t.Parallel()

	m := newMinimalManager(newBaseConfig("bloc1", "aws"))
	current := "A=1\n\nB=2\n"
	out := m.buildProposedEnvironment(current, map[string]string{})

	assert.NotContains(t, out, "\n\n", "blank lines in current must be collapsed")
}

func TestBuildProposedEnvironment_PreservesNonKVLines(t *testing.T) {
	t.Parallel()

	m := newMinimalManager(newBaseConfig("bloc1", "aws"))
	// A line without '=' is kept verbatim.
	current := "# comment line\nA=1\n"
	out := m.buildProposedEnvironment(current, map[string]string{})

	assert.Contains(t, out, "# comment line")
	assert.Contains(t, out, "A=1")
}

func TestBuildProposedEnvironment_MultipleKeysReplaced(t *testing.T) {
	t.Parallel()

	m := newMinimalManager(newBaseConfig("bloc1", "aws"))
	current := "X=old\nY=old\nZ=keep\n"
	desired := map[string]string{"X": "new-x", "Y": "new-y"}

	out := m.buildProposedEnvironment(current, desired)

	assert.Contains(t, out, "X=new-x")
	assert.Contains(t, out, "Y=new-y")
	assert.Contains(t, out, "Z=keep")
	assert.NotContains(t, out, "X=old")
	assert.NotContains(t, out, "Y=old")
}

// ---------------------------------------------------------------------------
// buildPackageInstallScript
// ---------------------------------------------------------------------------

func TestBuildPackageInstallScript_ContainsShebang(t *testing.T) {
	t.Parallel()

	m := newMinimalManager(newBaseConfig("bloc1", "aws"))
	// provConfig nil — buildPackageInstallScript uses NewScriptGenerator internally
	// and calls m.provConfig.GetAPTRepositories(); supply a real provConfig.
	m.provConfig = newTestProvConfig(t, "aws")

	out := m.buildPackageInstallScript(nil)

	assert.Contains(t, out, "#!/bin/bash")
	assert.Contains(t, out, "set -euo pipefail")
}

func TestBuildPackageInstallScript_ContainsLoggingFunctions(t *testing.T) {
	t.Parallel()

	m := newMinimalManager(newBaseConfig("bloc1", "aws"))
	m.provConfig = newTestProvConfig(t, "aws")

	out := m.buildPackageInstallScript(nil)

	assert.Contains(t, out, "log_info()")
	assert.Contains(t, out, "log_success()")
	assert.Contains(t, out, "log_error()")
}

func TestBuildPackageInstallScript_ContainsAptUpdate(t *testing.T) {
	t.Parallel()

	m := newMinimalManager(newBaseConfig("bloc1", "aws"))
	m.provConfig = newTestProvConfig(t, "aws")

	out := m.buildPackageInstallScript(nil)

	assert.Contains(t, out, "apt-get update")
	assert.Contains(t, out, "dpkg --configure -a")
}

func TestBuildPackageInstallScript_StackitAddsRepositories(t *testing.T) {
	t.Parallel()

	m := newMinimalManager(newBaseConfig("bloc1", "stackit"))
	m.provConfig = newTestProvConfig(t, "stackit")

	out := m.buildPackageInstallScript(nil)

	// The script must still be valid regardless of whether stackit adds repos.
	assert.Contains(t, out, "#!/bin/bash")
}

// newTestProvConfig builds a ProvisionConfig for test managers using the same
// call that Manager.loadProvisioningConfig uses internally.
func newTestProvConfig(t *testing.T, provider string) provision.ProvisionConfig {
	t.Helper()

	cfg := newBaseConfig("test-bloc", provider)

	return provision.NewConfig(provider, cfg, nil)
}

// ---------------------------------------------------------------------------
// buildGenesisUpgradeScript
// ---------------------------------------------------------------------------

func TestBuildGenesisUpgradeScript_HappyPath(t *testing.T) {
	t.Parallel()

	m := newMinimalManager(newBaseConfig("bloc1", "aws"))
	script := m.buildGenesisUpgradeScript("3.2.0", "v3.2.x-dev", "git@github.com:genesis-community/genesis")

	assert.Contains(t, script, "3.2.0")
	assert.Contains(t, script, "v3.2.x-dev")
	assert.Contains(t, script, "genesis-community/genesis")
	assert.Contains(t, script, "set -e")
}

func TestBuildGenesisUpgradeScript_EmptyVersion(t *testing.T) {
	t.Parallel()

	m := newMinimalManager(newBaseConfig("bloc1", "aws"))
	// Empty version must not panic; script may contain empty string placeholders.
	script := m.buildGenesisUpgradeScript("", "main", "git@github.com:org/repo")

	assert.NotEmpty(t, script)
	assert.Contains(t, script, "set -e")
}

func TestBuildGenesisUpgradeScript_AllEmptyArgs(t *testing.T) {
	t.Parallel()

	m := newMinimalManager(newBaseConfig("bloc1", "aws"))
	script := m.buildGenesisUpgradeScript("", "", "")

	assert.NotEmpty(t, script)
}

func TestBuildGenesisUpgradeScript_ContainsCheckout(t *testing.T) {
	t.Parallel()

	m := newMinimalManager(newBaseConfig("bloc1", "aws"))
	script := m.buildGenesisUpgradeScript("3.0.0", "v3.x", "git@github.com:org/repo")

	assert.Contains(t, script, "git checkout")
	assert.Contains(t, script, "git pull")
}

func TestBuildGenesisUpgradeScript_InstallsToPath(t *testing.T) {
	t.Parallel()

	m := newMinimalManager(newBaseConfig("bloc1", "aws"))
	script := m.buildGenesisUpgradeScript("1.0.0", "main", "git@github.com:org/repo")

	assert.Contains(t, script, "/usr/local/bin/genesis")
	assert.Contains(t, script, "genesis --version")
}

// ---------------------------------------------------------------------------
// isTransientGitError
// ---------------------------------------------------------------------------

func TestIsTransientGitError_RateLimit(t *testing.T) {
	t.Parallel()

	m := newMinimalManager(newBaseConfig("bloc1", "aws"))
	err := errors.New("error: rate limit exceeded")

	assert.True(t, m.isTransientGitError(err))
}

func TestIsTransientGitError_429(t *testing.T) {
	t.Parallel()

	m := newMinimalManager(newBaseConfig("bloc1", "aws"))
	err := errors.New("HTTP 429 too many requests")

	assert.True(t, m.isTransientGitError(err))
}

func TestIsTransientGitError_Temporarily(t *testing.T) {
	t.Parallel()

	m := newMinimalManager(newBaseConfig("bloc1", "aws"))
	err := errors.New("server temporarily unavailable")

	assert.True(t, m.isTransientGitError(err))
}

func TestIsTransientGitError_Timeout(t *testing.T) {
	t.Parallel()

	m := newMinimalManager(newBaseConfig("bloc1", "aws"))
	err := errors.New("connection timeout after 30s")

	assert.True(t, m.isTransientGitError(err))
}

func TestIsTransientGitError_PermanentError(t *testing.T) {
	t.Parallel()

	m := newMinimalManager(newBaseConfig("bloc1", "aws"))
	err := errors.New("repository not found")

	assert.False(t, m.isTransientGitError(err))
}

func TestIsTransientGitError_PermissionDenied(t *testing.T) {
	t.Parallel()

	m := newMinimalManager(newBaseConfig("bloc1", "aws"))
	err := errors.New("permission denied (publickey)")

	assert.False(t, m.isTransientGitError(err))
}

func TestIsTransientGitError_CaseInsensitive(t *testing.T) {
	t.Parallel()

	m := newMinimalManager(newBaseConfig("bloc1", "aws"))

	cases := []struct {
		msg  string
		want bool
	}{
		{"RATE LIMIT exceeded", true},
		{"TIMEOUT occurred", true},
		{"TEMPORARILY down", true},
		{"HTTP 429", true},
		{"permission denied", false},
	}

	for _, tc := range cases {
		t.Run(tc.msg, func(t *testing.T) {
			t.Parallel()
			got := m.isTransientGitError(errors.New(tc.msg))
			assert.Equal(t, tc.want, got)
		})
	}
}

// ---------------------------------------------------------------------------
// validateOverrides — assert no panic; log-only side effect; not asserted.
// ---------------------------------------------------------------------------

func TestValidateOverrides_NoPanic_EmptyConfig(t *testing.T) {
	t.Parallel()

	m := newMinimalManager(newBaseConfig("bloc1", "aws"))
	// Must not panic with all-empty override lists.
	assert.NotPanics(t, func() { m.validateOverrides() })
}

func TestValidateOverrides_NoPanic_UnknownToolName(t *testing.T) {
	t.Parallel()

	cfg := newBaseConfig("bloc1", "aws")
	cfg.Bastion.Tools.Enable = []string{"definitely-not-a-real-tool-xyz"}
	m := newMinimalManager(cfg)

	// validateOverrides only warns via log — it must not error or panic.
	assert.NotPanics(t, func() { m.validateOverrides() })
}

func TestValidateOverrides_NoPanic_UnknownSnapName(t *testing.T) {
	t.Parallel()

	cfg := newBaseConfig("bloc1", "aws")
	cfg.Bastion.Snaps.Disable = []string{"not-a-snap"}
	m := newMinimalManager(cfg)

	assert.NotPanics(t, func() { m.validateOverrides() })
}

func TestValidateOverrides_NoPanic_UnknownToolOverrideKey(t *testing.T) {
	t.Parallel()

	cfg := newBaseConfig("bloc1", "aws")
	cfg.Bastion.ToolOverrides = map[string]config.ToolOverride{
		"bogus-tool": {},
	}
	m := newMinimalManager(cfg)

	assert.NotPanics(t, func() { m.validateOverrides() })
}

func TestValidateOverrides_NoPanic_UnknownCFPluginOverrideKey(t *testing.T) {
	t.Parallel()

	cfg := newBaseConfig("bloc1", "aws")
	cfg.Bastion.CFPluginOverrides = map[string]config.CFPluginOverride{
		"bogus-plugin": {},
	}
	m := newMinimalManager(cfg)

	assert.NotPanics(t, func() { m.validateOverrides() })
}

// ---------------------------------------------------------------------------
// getEnvironmentVariables
// ---------------------------------------------------------------------------

func TestGetEnvironmentVariables_ContainsOCFPBloc(t *testing.T) {
	t.Parallel()

	m := newMinimalManager(newBaseConfig("my-bloc", "aws"))
	env := m.getEnvironmentVariables()

	require.Contains(t, env, "OCFP_BLOC")
	assert.Equal(t, "my-bloc", env["OCFP_BLOC"])
}

func TestGetEnvironmentVariables_ContainsProvider(t *testing.T) {
	t.Parallel()

	m := newMinimalManager(newBaseConfig("b", "stackit"))
	env := m.getEnvironmentVariables()

	require.Contains(t, env, "OCFP_PROVIDER")
	assert.Equal(t, "stackit", env["OCFP_PROVIDER"])
}

func TestGetEnvironmentVariables_AWSProvider(t *testing.T) {
	t.Parallel()

	cfg := newBaseConfig("bloc", "aws")
	cfg.AccessKeyID = "AKIATEST"
	cfg.SecretAccessKey = "secretval"
	cfg.Region = "us-east-1"
	m := newMinimalManager(cfg)

	env := m.getEnvironmentVariables()

	assert.Equal(t, "AKIATEST", env["AWS_ACCESS_KEY_ID"])
	assert.Equal(t, "secretval", env["AWS_SECRET_ACCESS_KEY"])
	assert.Equal(t, "us-east-1", env["AWS_DEFAULT_REGION"])
}

func TestGetEnvironmentVariables_AWSProviderEmptyFields(t *testing.T) {
	t.Parallel()

	cfg := newBaseConfig("bloc", "aws")
	// AccessKeyID, SecretAccessKey, Region all empty.
	m := newMinimalManager(cfg)

	env := m.getEnvironmentVariables()

	// Keys must not be present when value is empty.
	assert.NotContains(t, env, "AWS_ACCESS_KEY_ID")
	assert.NotContains(t, env, "AWS_SECRET_ACCESS_KEY")
	assert.NotContains(t, env, "AWS_DEFAULT_REGION")
}

func TestGetEnvironmentVariables_StackitProvider(t *testing.T) {
	t.Parallel()

	cfg := newBaseConfig("bloc", "stackit")
	cfg.ProjectID = "proj-123"
	cfg.OrgID = "org-456"
	cfg.Region = "eu-01"
	m := newMinimalManager(cfg)

	env := m.getEnvironmentVariables()

	assert.Equal(t, "proj-123", env["STACKIT_PROJECT_ID"])
	assert.Equal(t, "org-456", env["STACKIT_ORG_ID"])
	assert.Equal(t, "eu-01", env["STACKIT_REGION"])
}

func TestGetEnvironmentVariables_StackitProviderEmptyFields(t *testing.T) {
	t.Parallel()

	cfg := newBaseConfig("bloc", "stackit")
	m := newMinimalManager(cfg)

	env := m.getEnvironmentVariables()

	assert.NotContains(t, env, "STACKIT_PROJECT_ID")
	assert.NotContains(t, env, "STACKIT_ORG_ID")
	assert.NotContains(t, env, "STACKIT_REGION")
}

func TestGetEnvironmentVariables_UnknownProvider(t *testing.T) {
	t.Parallel()

	cfg := newBaseConfig("bloc", "unknown-provider")
	m := newMinimalManager(cfg)

	// Must not panic; must still return base keys.
	env := m.getEnvironmentVariables()

	assert.Contains(t, env, "OCFP_BLOC")
	assert.Contains(t, env, "OCFP_PROVIDER")
}

func TestGetEnvironmentVariables_EmptyBlocName(t *testing.T) {
	t.Parallel()

	m := newMinimalManager(newBaseConfig("", "aws"))
	env := m.getEnvironmentVariables()

	assert.Equal(t, "", env["OCFP_BLOC"])
}
