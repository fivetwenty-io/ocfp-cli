package cli

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
