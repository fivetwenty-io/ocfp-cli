package config

import (
	"github.com/goccy/go-yaml"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestGCPSharedVPCConfiguration(t *testing.T) {
	var cfg Config
	require.NoError(t, yaml.Unmarshal([]byte("provider: gcp\nproject_id: service\nenable_shared_vpc: true\nhost_project_id: host\nnetwork:\n  id: projects/host/global/networks/shared\n"), &cfg))
	require.True(t, cfg.EnableSharedVPC)
	require.Equal(t, "host", cfg.HostProjectID)
	require.NoError(t, cfg.ValidateCloudEgress())
	cfg.HostProjectID = "wrong"
	require.Error(t, cfg.ValidateCloudEgress())
	cfg.HostProjectID = ""
	require.Error(t, cfg.ValidateCloudEgress())
	cfg.EnableSharedVPC = false
	require.Error(t, cfg.ValidateCloudEgress())
	cfg.Network.ID = "projects/service/global/networks/local"
	require.NoError(t, cfg.ValidateCloudEgress())
}
