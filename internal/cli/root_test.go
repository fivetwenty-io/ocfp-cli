package cli

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/version"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureStderr redirects os.Stderr for the duration of fn and returns
// everything written to it. Restores the original os.Stderr afterward
// (even if fn panics), so it's safe to use alongside t.Parallel siblings
// that don't touch stderr — but tests using it must NOT run in parallel
// with each other, since os.Stderr is process-global.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()

	original := os.Stderr

	r, w, err := os.Pipe()
	require.NoError(t, err)

	os.Stderr = w

	defer func() {
		os.Stderr = original
	}()

	fn()

	require.NoError(t, w.Close())

	out, err := io.ReadAll(r)
	require.NoError(t, err)
	require.NoError(t, r.Close())

	return string(out)
}

func TestCreatePreRunHandler_BlocFromEnvVar(t *testing.T) {
	t.Run("uses flag when provided", func(t *testing.T) {
		viper.Reset()
		t.Cleanup(viper.Reset)

		// Set environment variable
		t.Setenv("OCFP_BLOC", "env-bloc-name")

		// Create flag with value
		flagValue := "flag-bloc-name"
		blocName := &flagValue

		lock := &lockInfo{}
		handler := createPreRunHandler(blocName, lock)

		// Create a mock command
		rootCmd := createRootCommand()
		handler(rootCmd, []string{})

		// Should use flag value, not env var
		assert.Equal(t, "flag-bloc-name", viper.GetString("bloc"))
	})

	t.Run("uses env var when flag is empty", func(t *testing.T) {
		viper.Reset()
		t.Cleanup(viper.Reset)

		// Set environment variable
		t.Setenv("OCFP_BLOC", "env-bloc-name")

		// Create empty flag
		flagValue := ""
		blocName := &flagValue

		lock := &lockInfo{}
		handler := createPreRunHandler(blocName, lock)

		// Create a mock command
		rootCmd := createRootCommand()
		handler(rootCmd, []string{})

		// Should use env var value
		assert.Equal(t, "env-bloc-name", viper.GetString("bloc"))
	})

	t.Run("uses empty string when neither provided", func(t *testing.T) {
		viper.Reset()
		t.Cleanup(viper.Reset)

		// Isolate from real ~/.ocfp/ so GetCurrentBloc and ListBlocNames
		// don't read the developer's config file.
		t.Setenv("OCFP_HOME", t.TempDir())
		// Use t.Setenv so the env var is restored after the subtest, and
		// set it to empty rather than calling os.Unsetenv which is not
		// test-safe and doesn't restore the previous value.
		t.Setenv("OCFP_BLOC", "")

		// Create empty flag
		flagValue := ""
		blocName := &flagValue

		lock := &lockInfo{}
		handler := createPreRunHandler(blocName, lock)

		// Create a mock command
		rootCmd := createRootCommand()
		handler(rootCmd, []string{})

		// Should be empty
		assert.Equal(t, "", viper.GetString("bloc"))
	})
}

func TestCreatePreRunHandler_PreservesFlagPriority(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	// Set environment variable to one value
	t.Setenv("OCFP_BLOC", "env-value")

	// Set flag to different value
	flagValue := "flag-value"
	blocName := &flagValue

	lock := &lockInfo{}
	handler := createPreRunHandler(blocName, lock)

	// Create a mock command
	rootCmd := createRootCommand()
	handler(rootCmd, []string{})

	// Flag should take precedence over env var
	result := viper.GetString("bloc")
	require.Equal(t, "flag-value", result, "Flag should take precedence over environment variable")
}

func TestCreatePreRunHandler_BlocResolutionPriority(t *testing.T) {
	t.Run("uses state file when flag and env are empty", func(t *testing.T) {
		viper.Reset()
		t.Cleanup(viper.Reset)

		tmpDir := t.TempDir()
		t.Setenv("OCFP_HOME", tmpDir)
		t.Setenv("OCFP_BLOC", "")

		// Write state file with a current bloc
		err := config.SetCurrentBloc("state-bloc", "/path/to/config.yml")
		require.NoError(t, err)

		flagValue := ""
		blocName := &flagValue

		lock := &lockInfo{}
		handler := createPreRunHandler(blocName, lock)

		rootCmd := createRootCommand()
		handler(rootCmd, []string{})

		assert.Equal(t, "state-bloc", viper.GetString("bloc"))
	})

	t.Run("env var takes precedence over state file", func(t *testing.T) {
		viper.Reset()
		t.Cleanup(viper.Reset)

		tmpDir := t.TempDir()
		t.Setenv("OCFP_HOME", tmpDir)
		t.Setenv("OCFP_BLOC", "env-bloc")

		// Write state file with a different bloc
		err := config.SetCurrentBloc("state-bloc", "/path/to/config.yml")
		require.NoError(t, err)

		flagValue := ""
		blocName := &flagValue

		lock := &lockInfo{}
		handler := createPreRunHandler(blocName, lock)

		rootCmd := createRootCommand()
		handler(rootCmd, []string{})

		assert.Equal(t, "env-bloc", viper.GetString("bloc"))
	})

	t.Run("flag takes precedence over state file and env", func(t *testing.T) {
		viper.Reset()
		t.Cleanup(viper.Reset)

		tmpDir := t.TempDir()
		t.Setenv("OCFP_HOME", tmpDir)
		t.Setenv("OCFP_BLOC", "env-bloc")

		err := config.SetCurrentBloc("state-bloc", "/path/to/config.yml")
		require.NoError(t, err)

		flagValue := "flag-bloc"
		blocName := &flagValue

		lock := &lockInfo{}
		handler := createPreRunHandler(blocName, lock)

		rootCmd := createRootCommand()
		handler(rootCmd, []string{})

		assert.Equal(t, "flag-bloc", viper.GetString("bloc"))
	})
}

// TestWarnUnstampedBuild covers both branches warnUnstampedBuild decides
// on: version.GitCommit == "unknown" (the ldflags zero-value, meaning the
// binary was built without `make build`/`make install-local`) and any other
// value (a stamped build, whether a real commit hash or a test-injected
// sentinel). No parallel subtests: os.Stderr and version.GitCommit are both
// process-global mutable state.
func TestWarnUnstampedBuild(t *testing.T) {
	t.Run("unknown commit prints warning to stderr", func(t *testing.T) {
		original := version.GitCommit
		version.GitCommit = "unknown"

		t.Cleanup(func() { version.GitCommit = original })

		output := captureStderr(t, warnUnstampedBuild)

		assert.Contains(t, output, "unstamped build")
		assert.Contains(t, output, "make build")
	})

	t.Run("stamped commit prints nothing", func(t *testing.T) {
		original := version.GitCommit
		version.GitCommit = "abc1234"

		t.Cleanup(func() { version.GitCommit = original })

		output := captureStderr(t, warnUnstampedBuild)

		assert.Empty(t, output)
	})
}

// TestCreatePreRunHandler_UnstampedBuildWarningDoesNotBlockExecution asserts
// the PersistentPreRun handler itself still completes and sets viper state
// normally when the warning fires — the warning must never short-circuit
// command execution, only annotate stderr.
func TestCreatePreRunHandler_UnstampedBuildWarningDoesNotBlockExecution(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	original := version.GitCommit
	version.GitCommit = "unknown"
	t.Cleanup(func() { version.GitCommit = original })

	t.Setenv("OCFP_BLOC", "env-bloc-name")

	flagValue := ""
	blocName := &flagValue

	lock := &lockInfo{}
	handler := createPreRunHandler(blocName, lock)

	rootCmd := createRootCommand()

	output := captureStderr(t, func() {
		handler(rootCmd, []string{})
	})

	assert.Contains(t, output, "unstamped build")
	// The rest of PersistentPreRun still ran: bloc resolution completed.
	assert.Equal(t, "env-bloc-name", viper.GetString("bloc"))
}

// isolateXDGHome points HOME at a fresh temp directory and clears every
// XDG_*_HOME/OCFP_HOME override, so ConfigHome/StateHome/DataHome/OcfpHome
// all resolve to distinct, disjoint directories under the temp home rather
// than the developer's real home directory or a shared OCFP_HOME override
// (which would collapse all four to the same path and defeat dual-read
// assertions).
func isolateXDGHome(t *testing.T) string {
	t.Helper()

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("OCFP_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")

	return tmpHome
}

// TestGetBaseDir covers both branches of getBaseDir's dual-read: StateHome()
// used when it exists (or neither location exists, since a fresh temp home
// has no pre-existing directories at all), and the legacy ~/.ocfp fallback
// used only when StateHome() is absent but the legacy directory is present.
func TestGetBaseDir(t *testing.T) {
	t.Run("resolves under StateHome by default", func(t *testing.T) {
		isolateXDGHome(t)

		baseDir, err := getBaseDir()
		require.NoError(t, err)
		assert.Equal(t, config.StateHome(), baseDir)
		assert.NotEqual(t, config.OcfpHome(), baseDir)
	})

	t.Run("falls back to legacy OcfpHome when only legacy exists", func(t *testing.T) {
		isolateXDGHome(t)

		legacyDir := config.OcfpHome()
		require.NoError(t, os.MkdirAll(legacyDir, 0o750))

		baseDir, err := getBaseDir()
		require.NoError(t, err)
		assert.Equal(t, legacyDir, baseDir)
	})
}

// TestDefineFlags_NoLogHelpText asserts the --no-log flag's help text was
// updated off the pre-migration ~/.ocfp/logs/ literal to describe the new
// state-log default.
func TestDefineFlags_NoLogHelpText(t *testing.T) {
	rootCmd := createRootCommand()
	flags := setupFlags()
	defineFlags(rootCmd, flags)

	f := rootCmd.PersistentFlags().Lookup("no-log")
	require.NotNil(t, f)
	assert.NotContains(t, f.Usage, "~/.ocfp/logs/")
	assert.Contains(t, f.Usage, "state log")
}

// TestInitConfig_OCFPConfigPathResolution covers initConfig's OCFP_CONFIG_PATH
// dual-read: it defaults to ConfigHome() when no config.yml exists anywhere
// (or only the new location has one), and falls back to the legacy
// ~/.ocfp directory when only ~/.ocfp/config.yml exists.
func TestInitConfig_OCFPConfigPathResolution(t *testing.T) {
	t.Run("defaults to ConfigHome when no config.yml exists anywhere", func(t *testing.T) {
		viper.Reset()
		t.Cleanup(viper.Reset)
		isolateXDGHome(t)
		t.Setenv("OCFP_CONFIG_PATH", "")

		initConfig("", false)

		assert.Equal(t, config.ConfigHome(), os.Getenv("OCFP_CONFIG_PATH"))
	})

	t.Run("falls back to legacy OcfpHome when only legacy config.yml exists", func(t *testing.T) {
		viper.Reset()
		t.Cleanup(viper.Reset)
		isolateXDGHome(t)
		t.Setenv("OCFP_CONFIG_PATH", "")

		legacyDir := config.OcfpHome()
		require.NoError(t, os.MkdirAll(legacyDir, 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(legacyDir, "config.yml"), []byte("blocs: {}\n"), 0o600))

		initConfig("", false)

		assert.Equal(t, legacyDir, os.Getenv("OCFP_CONFIG_PATH"))
	})

	t.Run("respects an existing OCFP_CONFIG_PATH override", func(t *testing.T) {
		viper.Reset()
		t.Cleanup(viper.Reset)
		isolateXDGHome(t)

		override := t.TempDir()
		t.Setenv("OCFP_CONFIG_PATH", override)

		initConfig("", false)

		assert.Equal(t, override, os.Getenv("OCFP_CONFIG_PATH"))
	})
}
