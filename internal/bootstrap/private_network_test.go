package bootstrap_test

import (
	"context"
	"fmt"
	"github.com/ocfp/ocfp-cli-go/internal/bootstrap"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/state"
	"github.com/stretchr/testify/require"
	"testing"
)

type privateNet struct {
	*fakeNet
	missing      bool
	wrongNetwork bool
}

func (n *privateNet) GetNetwork(context.Context, string) (*cpi.Network, error) {
	return &cpi.Network{ID: "net-existing", CIDR: "10.0.0.0/20"}, nil
}
func (n *privateNet) GetSubnet(context.Context, string) (*cpi.Subnet, error) {
	if n.missing {
		return nil, fmt.Errorf("not found")
	}
	network := "net-existing"
	if n.wrongNetwork {
		network = "foreign"
	}
	return &cpi.Subnet{ID: "sub-existing", Name: "actual", NetworkID: network, CIDR: "10.0.4.0/22", AvailabilityZone: "us-east-1b"}, nil
}
func TestPrivateNetworkImportsWithoutCreating(t *testing.T) {
	for _, failure := range []string{"", "missing", "foreign", "zone", "state-collision"} {
		t.Run(failure, func(t *testing.T) {
			sm, err := state.NewManager(t.TempDir())
			require.NoError(t, err)
			_, err = sm.Load("private")
			require.NoError(t, err)
			cfg := &config.Config{Provider: "aws", Network: config.NetworkConfig{CloudEgress: "existing-private", ID: "net-existing", Strategy: "wide", Subnets: []config.Subnet{{ID: "sub-existing", Name: "custom", CIDR: "10.0.4.0/22"}}}}
			if failure == "zone" {
				cfg.Network.Subnets[0].AvailabilityZone = "us-east-1a"
			}
			if failure == "state-collision" {
				require.NoError(t, sm.AddResource(&state.Resource{Type: "subnet", Name: "custom", ID: "old-managed"}))
			}
			net := &privateNet{fakeNet: &fakeNet{}, missing: failure == "missing", wrongNetwork: failure == "foreign"}
			m := bootstrap.NewManager(cfg, &fakeProv{n: net, c: &fakeCompute{}}, sm, &bootstrap.Options{BlocName: "private", Provider: "aws"})
			err = m.CreateNetwork(context.Background())
			if failure != "" {
				require.Error(t, err)
				resources, _ := sm.GetResourcesByType("network")
				require.Empty(t, resources)
			} else {
				require.NoError(t, err)
				require.NoError(t, m.CreateSubnets(context.Background()))
				r, err := sm.GetResource("subnet", "custom")
				require.NoError(t, err)
				require.Equal(t, "sub-existing", r.ID)
				require.Equal(t, true, r.Properties["imported"])
				require.Equal(t, true, r.Properties["preserve"])
			}
			require.Empty(t, net.createdNetworks)
			require.Empty(t, net.createdSubnets)
		})
	}
}
