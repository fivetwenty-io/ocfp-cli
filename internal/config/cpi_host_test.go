package config_test

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLoadPVECPIHost proves the top-level cpi_host key loads into
// Config.CPIHost next to api_endpoint, and stays empty when omitted.
func TestLoadPVECPIHost(t *testing.T) {
	t.Parallel()

	t.Run("set", func(t *testing.T) {
		t.Parallel()

		cfgPath := writePVEBlocConfig(t, "cpi_host: 10.254.16.5")

		cfg, err := config.LoadWithParams(cfgPath, "lab")
		require.NoError(t, err)

		assert.Equal(t, "10.254.16.5", cfg.CPIHost)
		assert.Equal(t, "https://pve.example:8006", cfg.APIEndpoint)
	})

	t.Run("omitted stays empty", func(t *testing.T) {
		t.Parallel()

		cfgPath := writePVEBlocConfig(t, "network:\n  cidr: 10.254.16.0/20")

		cfg, err := config.LoadWithParams(cfgPath, "lab")
		require.NoError(t, err)

		assert.Empty(t, cfg.CPIHost)
	})
}
