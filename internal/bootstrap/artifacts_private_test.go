package bootstrap

import (
	"context"
	"fmt"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/state"
	"github.com/stretchr/testify/require"
	"testing"
)

type privateArtifactCompute struct {
	cpi.ComputeManager
	req *cpi.InstanceRequest
}

func (c *privateArtifactCompute) CreateInstance(_ context.Context, r *cpi.InstanceRequest) (*cpi.Instance, error) {
	c.req = r
	return nil, fmt.Errorf("captured request")
}

func (c *privateArtifactCompute) GetFlavor(_ context.Context, id string) (*cpi.Flavor, error) {
	return &cpi.Flavor{ID: id, Name: id, Disk: 40}, nil
}

func (c *privateArtifactCompute) ListImages(_ context.Context, _ map[string]string) ([]*cpi.Image, error) {
	return []*cpi.Image{{ID: "image", Name: "image"}}, nil
}

func (c *privateArtifactCompute) ListFlavors(_ context.Context) ([]*cpi.Flavor, error) {
	return []*cpi.Flavor{{ID: "shape", Name: "shape", Disk: 40}}, nil
}

type privateArtifactProvider struct {
	cpi.Provider
	c *privateArtifactCompute
}

func (p *privateArtifactProvider) ComputeManager() cpi.ComputeManager { return p.c }
func TestPrivateArtifactsRequestHasNoPublicIP(t *testing.T) {
	for _, provider := range []string{"aws", "gcp"} {
		t.Run(provider, func(t *testing.T) {
			t.Setenv("OCFP_HOME", t.TempDir())
			sm, err := state.NewManager(t.TempDir())
			require.NoError(t, err)
			_, err = sm.Load("prod")
			require.NoError(t, err)
			require.NoError(t, sm.SetOutput("network_id", "net"))
			require.NoError(t, sm.AddResource(&state.Resource{Type: "subnet", ID: "sub", Name: "prod-ocfp-0", Properties: map[string]any{"cidr": "10.0.4.0/22", "role": "ocfp", "workload_index": 0, "availability_zone": "us-east-1a"}}))
			require.NoError(t, sm.AddResource(&state.Resource{Type: "security_group", ID: "sg", Name: "prod-artifacts"}))
			cfg := &config.Config{Provider: provider, Network: config.NetworkConfig{CloudEgress: "existing-private", ID: "net", Strategy: "wide", Subnets: []config.Subnet{{ID: "sub", Name: "prod-ocfp-0", CIDR: "10.0.4.0/22", AvailabilityZone: "us-central1-b"}}}, Artifacts: config.ArtifactsConfig{Enabled: true, Flavor: "shape", Template: "image", Data: config.ArtifactsDataConfig{Mountpoint: "/data", Filesystem: "ext4"}, Rustfs: config.RustfsConfig{AccessKey: "access", SecretKey: "secret", S3Port: 9000, ConsolePort: 9001}, TLS: config.ArtifactsTLSConfig{Mode: config.ArtifactsTLSModeDisabled}}}
			comp := &privateArtifactCompute{}
			m := NewManager(cfg, &privateArtifactProvider{c: comp}, sm, &Options{Provider: provider, BlocName: "prod"})
			createErr := m.CreateArtifacts(context.Background())
			require.Error(t, createErr)
			require.NotNil(t, comp.req, "%v", createErr)
			require.NotNil(t, comp.req.PublicIP)
			require.False(t, *comp.req.PublicIP)
		})
	}
}
