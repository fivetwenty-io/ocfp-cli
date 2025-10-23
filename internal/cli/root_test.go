package cli

import (
	"os"
	"testing"

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

		// Ensure env var is not set
		os.Unsetenv("OCFP_BLOC")

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
