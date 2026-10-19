package commands

import (
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestGCPSharedVPCProviderTransport(t *testing.T) {
	cfg := &config.Config{Provider: "gcp", ProjectID: "service", EnableSharedVPC: true, HostProjectID: "host", Network: config.NetworkConfig{Name: "shared"}}
	m := buildProviderConfig(cfg, "us-central1")
	require.Equal(t, "shared", m["network_name"])
	require.Equal(t, "service", m["project_id"])
	require.Equal(t, "host", m["host_project_id"])
	require.Equal(t, true, m["enable_shared_vpc"])
}
