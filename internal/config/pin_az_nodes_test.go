package config_test

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLoadPVEPinAZNodes proves the top-level pin_az_nodes key loads into
// Config.PinAZNodes next to cpi_host, and that an unset key reads as true.
func TestLoadPVEPinAZNodes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		blocBody  string
		wantField *bool
		wantPin   bool
	}{
		{name: "unset means true", blocBody: "cpi_host: 10.254.16.5", wantField: nil, wantPin: true},
		{name: "true", blocBody: "pin_az_nodes: true", wantField: new(true), wantPin: true},
		{name: "false", blocBody: "pin_az_nodes: false", wantField: new(false), wantPin: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg, err := config.LoadWithParams(writePVEBlocConfig(t, tt.blocBody), "lab")
			require.NoError(t, err)

			assert.Equal(t, tt.wantField, cfg.PinAZNodes)
			assert.Equal(t, tt.wantPin, cfg.PinAZNodesEnabled())
		})
	}
}

// TestPinAZNodesEnabledZeroValue — a Config built without the key pins nodes.
func TestPinAZNodesEnabledZeroValue(t *testing.T) {
	t.Parallel()

	assert.True(t, (&config.Config{}).PinAZNodesEnabled())
}
