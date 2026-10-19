package config

import (
	"github.com/goccy/go-yaml"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestCloudEgressIntent(t *testing.T) {
	for _, provider := range []string{"aws", "gcp"} {
		cfg := &Config{Provider: provider}
		require.NoError(t, cfg.ValidateCloudEgress())
		require.True(t, cfg.CloudPublicIP())
		require.NoError(t, yaml.Unmarshal([]byte("cloudEgress: existing-private\nid: imported-network\nsubnets:\n- id: imported-subnet\n  name: work-a\n  cidr: 10.0.4.0/22\n  availabilityZone: us-central1-b\n"), &cfg.Network))
		require.NoError(t, cfg.ValidateCloudEgress())
		require.False(t, cfg.CloudPublicIP())
		cfg.Network.Subnets[0].Type = "infra"
		require.Error(t, cfg.ValidateCloudEgress())
		cfg.Network.Subnets[0].Type = ""
		if provider == "gcp" {
			cfg.Network.Subnets[0].AvailabilityZone = ""
			require.Error(t, cfg.ValidateCloudEgress())
			cfg.Network.Subnets[0].AvailabilityZone = "us-central1-b"
		}
		cfg.Network.ID = ""
		require.Error(t, cfg.ValidateCloudEgress())
		cfg.Network.ID = "network"
		cfg.Network.Subnets[0].ID = ""
		require.Error(t, cfg.ValidateCloudEgress())
		cfg.Network.CloudEgress = "made-up"
		require.Error(t, cfg.ValidateCloudEgress())
	}
	require.Error(t, (&Config{Provider: "pve", Network: NetworkConfig{CloudEgress: "existing-private"}}).ValidateCloudEgress())
}
