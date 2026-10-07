package config_test

import (
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNetworkConfigSubnetRecordsAliases proves both the snake_case key and
// the camelCase form land in NetworkConfig.SubnetRecords.
func TestNetworkConfigSubnetRecordsAliases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		yaml string
		want string
	}{
		{name: "snake_case key", yaml: "subnet_records: per_subnet", want: config.SubnetRecordsPerSubnet},
		{name: "camelCase key", yaml: "subnetRecords: per_subnet", want: config.SubnetRecordsPerSubnet},
		{name: "parent", yaml: "subnet_records: parent", want: config.SubnetRecordsParent},
		{name: "unset", yaml: "cidr: 10.0.0.0/24", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var nc config.NetworkConfig

			require.NoError(t, yaml.Unmarshal([]byte(tt.yaml), &nc))

			assert.Equal(t, tt.want, nc.SubnetRecords)
		})
	}
}

// TestValidateSubnetRecords covers the accepted values and the error for
// anything else.
func TestValidateSubnetRecords(t *testing.T) {
	t.Parallel()

	for _, ok := range []string{"", "parent", "per_subnet"} {
		require.NoError(t, config.ValidateSubnetRecords(ok), "value %q", ok)
	}

	err := config.ValidateSubnetRecords("per-subnet")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "network.subnet_records")
	assert.Contains(t, err.Error(), `"per-subnet"`)
	assert.Contains(t, err.Error(), "parent")
	assert.Contains(t, err.Error(), "per_subnet")
}

// TestLoadPVESubnetRecords exercises the full LoadWithParams path: the key
// loads for a PVE bloc and an unknown value fails the load.
func TestLoadPVESubnetRecords(t *testing.T) {
	t.Parallel()

	t.Run("per_subnet loads", func(t *testing.T) {
		t.Parallel()

		cfgPath := writePVEBlocConfig(t, "network:\n  cidr: 10.254.16.0/20\n  subnet_records: per_subnet")

		cfg, err := config.LoadWithParams(cfgPath, "lab")
		require.NoError(t, err)

		assert.Equal(t, config.SubnetRecordsPerSubnet, cfg.Network.SubnetRecords)
	})

	t.Run("omitted stays empty", func(t *testing.T) {
		t.Parallel()

		cfgPath := writePVEBlocConfig(t, "network:\n  cidr: 10.254.16.0/20")

		cfg, err := config.LoadWithParams(cfgPath, "lab")
		require.NoError(t, err)

		assert.Empty(t, cfg.Network.SubnetRecords)
	})

	t.Run("unknown value fails the load", func(t *testing.T) {
		t.Parallel()

		cfgPath := writePVEBlocConfig(t, "network:\n  cidr: 10.254.16.0/20\n  subnet_records: carved")

		_, err := config.LoadWithParams(cfgPath, "lab")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "network.subnet_records")
		assert.Contains(t, err.Error(), "parent")
		assert.Contains(t, err.Error(), "per_subnet")
	})
}
