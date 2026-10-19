package gcp

import (
	ocfpconfig "github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestDirectOCFPSharedVPCConfig(t *testing.T) {
	c := &Client{}
	cfg := c.convertOCFPConfig(&ocfpconfig.Config{Provider: "gcp", ProjectID: "service", HostProjectID: "host", EnableSharedVPC: true, Network: ocfpconfig.NetworkConfig{Name: "shared"}})
	require.Equal(t, "host", cfg.GetNetworkProject())
	require.Equal(t, "shared", cfg.NetworkName)
}

func TestDirectOCFPSharedVPCRejectsForeignNetwork(t *testing.T) {
	_, err := (&Client{}).parseConfig(&ocfpconfig.Config{Provider: "gcp", ProjectID: "service", Network: ocfpconfig.NetworkConfig{ID: "projects/host/global/networks/shared"}})
	require.Error(t, err)
}
