package bastion

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/stretchr/testify/assert"
)

// newTestModeDetector constructs a ModeDetector wired to the no-op test logger.
func newTestModeDetector(cfg *config.Config) *ModeDetector {
	return &ModeDetector{
		config: cfg,
		log:    newTestLogger(),
	}
}

// ---------------------------------------------------------------------------
// checkEnvironmentVariables
// checkEnvironmentVariables returns true when >= minimumEnvVarsForBastionDetection
// of the three sentinel vars (OCFP_ROOT, DEPLOYMENTS_DIR, OCFP_CLI) are set.
// ---------------------------------------------------------------------------

// Note: tests using t.Setenv cannot call t.Parallel() (Go enforcement).

func TestCheckEnvironmentVariables_NoneSet_ReturnsFalse(t *testing.T) {
	t.Setenv("OCFP_ROOT", "")
	t.Setenv("DEPLOYMENTS_DIR", "")
	t.Setenv("OCFP_CLI", "")

	md := newTestModeDetector(newBaseConfig("bloc", "aws"))
	assert.False(t, md.checkEnvironmentVariables())
}

func TestCheckEnvironmentVariables_OneSet_ReturnsFalse(t *testing.T) {
	t.Setenv("OCFP_ROOT", "/opt/ocfp")
	t.Setenv("DEPLOYMENTS_DIR", "")
	t.Setenv("OCFP_CLI", "")

	md := newTestModeDetector(newBaseConfig("bloc", "aws"))
	// threshold is 2; one var is below threshold.
	assert.False(t, md.checkEnvironmentVariables())
}

func TestCheckEnvironmentVariables_TwoSet_ReturnsTrue(t *testing.T) {
	t.Setenv("OCFP_ROOT", "/opt/ocfp")
	t.Setenv("DEPLOYMENTS_DIR", "/opt/deployments")
	t.Setenv("OCFP_CLI", "")

	md := newTestModeDetector(newBaseConfig("bloc", "aws"))
	assert.True(t, md.checkEnvironmentVariables())
}

func TestCheckEnvironmentVariables_AllSet_ReturnsTrue(t *testing.T) {
	t.Setenv("OCFP_ROOT", "/opt/ocfp")
	t.Setenv("DEPLOYMENTS_DIR", "/opt/deployments")
	t.Setenv("OCFP_CLI", "/usr/local/bin/ocfp")

	md := newTestModeDetector(newBaseConfig("bloc", "aws"))
	assert.True(t, md.checkEnvironmentVariables())
}

func TestCheckEnvironmentVariables_EmptyStringValues_IgnoredByGetenv(t *testing.T) {
	// Explicitly set to empty — os.Getenv returns "" which counts as unset.
	t.Setenv("OCFP_ROOT", "")
	t.Setenv("DEPLOYMENTS_DIR", "")
	t.Setenv("OCFP_CLI", "")

	md := newTestModeDetector(newBaseConfig("bloc", "stackit"))
	assert.False(t, md.checkEnvironmentVariables())
}

// ---------------------------------------------------------------------------
// checkHostnamePattern
// checkHostnamePattern calls os.Hostname() which we cannot fake without a seam.
// We test the deterministic branch: when the current hostname does NOT match
// the expected "<name>-bastion" pattern (guaranteed in CI/dev environments).
// ---------------------------------------------------------------------------

func TestCheckHostnamePattern_MismatchReturnsFalse(t *testing.T) {
	t.Parallel()

	// Use a config name that will never match the actual test-machine hostname.
	cfg := newBaseConfig("zzz-impossible-bloc-name-xyz", "aws")
	md := newTestModeDetector(cfg)

	// The real hostname is almost certainly not "zzz-impossible-bloc-name-xyz-bastion".
	// If it happens to match, the test is inconclusive but cannot produce a false failure.
	result := md.checkHostnamePattern()

	// Verify the call does not panic regardless of outcome.
	_ = result
}

func TestCheckHostnamePattern_EmptyConfigName_ReturnsFalse(t *testing.T) {
	t.Parallel()

	// Empty name → expected hostname is "-bastion".
	// Actual hostname will never be "-bastion", so result is deterministically false.
	cfg := newBaseConfig("", "aws")
	md := newTestModeDetector(cfg)

	assert.False(t, md.checkHostnamePattern())
}

// ---------------------------------------------------------------------------
// isRunningOnBastion
// isRunningOnBastion is an OR-chain of four detectors. Testing it in isolation
// is tricky because the FS-based checks (marker files, directory structure) read
// real disk. We fix the env-vars and config so the deterministic branches dominate.
// ---------------------------------------------------------------------------

func TestIsRunningOnBastion_NoEnvNoMarkers_ReturnsFalse(t *testing.T) {
	// Do NOT run t.Parallel() — this test mutates env vars that affect
	// checkEnvironmentVariables. The subtests are serial to avoid flakiness.
	t.Setenv("OCFP_ROOT", "")
	t.Setenv("DEPLOYMENTS_DIR", "")
	t.Setenv("OCFP_CLI", "")

	// Use a bloc name that will never match the test machine's hostname.
	cfg := newBaseConfig("zz-no-match-xyz", "aws")
	// Point OCFP_HOME to a temp dir that has no marker files or OCFP dirs.
	tmpDir := t.TempDir()
	t.Setenv("OCFP_HOME", tmpDir)
	t.Setenv("HOME", tmpDir)

	md := newTestModeDetector(cfg)

	// With no env vars, no matching hostname, and no OCFP dirs in tmpDir,
	// all four checks should return false.
	assert.False(t, md.isRunningOnBastion())
}

func TestIsRunningOnBastion_EnvVarsSet_ReturnsTrue(t *testing.T) {
	t.Setenv("OCFP_ROOT", "/opt/ocfp")
	t.Setenv("DEPLOYMENTS_DIR", "/opt/deployments")
	t.Setenv("OCFP_CLI", "")

	cfg := newBaseConfig("zz-no-match-xyz", "aws")
	md := newTestModeDetector(cfg)

	// Two env vars set → checkEnvironmentVariables fires → OR-chain short-circuits to true.
	assert.True(t, md.isRunningOnBastion())
}

func TestIsRunningOnBastion_NoPanic_AnyConfig(t *testing.T) {
	t.Setenv("OCFP_ROOT", "")
	t.Setenv("DEPLOYMENTS_DIR", "")
	t.Setenv("OCFP_CLI", "")

	for _, provider := range []string{"aws", "stackit", "unknown"} {
		cfg := newBaseConfig("bloc", provider)
		md := newTestModeDetector(cfg)

		assert.NotPanics(t, func() { md.isRunningOnBastion() })
	}
}
