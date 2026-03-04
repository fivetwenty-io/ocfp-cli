package commands

import (
	"context"
	"fmt"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

// configureNetworkStub implements cpi.NetworkManager for configure tests.
// It captures the filters passed to ListFloatingIPs and records AssociateFloatingIP calls.
type configureNetworkStub struct {
	listFloatingIPsFilters map[string]string
	listFloatingIPsReturn  []*cpi.FloatingIP
	associatedIPID         string
	associatedInstanceID   string
}

func (s *configureNetworkStub) ListFloatingIPs(_ context.Context, filters map[string]string) ([]*cpi.FloatingIP, error) {
	s.listFloatingIPsFilters = filters

	return s.listFloatingIPsReturn, nil
}

func (s *configureNetworkStub) AssociateFloatingIP(_ context.Context, ipID string, instanceID string) error {
	s.associatedIPID = ipID
	s.associatedInstanceID = instanceID

	return nil
}

// Remaining NetworkManager stubs (unused in these tests).

func (s *configureNetworkStub) CreateNetwork(_ context.Context, _ *cpi.NetworkRequest) (*cpi.Network, error) { //nolint:nilnil // test stub
	return nil, nil
}

func (s *configureNetworkStub) GetNetwork(_ context.Context, _ string) (*cpi.Network, error) { //nolint:nilnil // test stub
	return nil, nil
}

func (s *configureNetworkStub) ListNetworks(_ context.Context, _ map[string]string) ([]*cpi.Network, error) { //nolint:nilnil // test stub
	return nil, nil
}

func (s *configureNetworkStub) DeleteNetwork(_ context.Context, _ string) error { return nil }

func (s *configureNetworkStub) CreateSubnet(_ context.Context, _ *cpi.SubnetRequest) (*cpi.Subnet, error) { //nolint:nilnil // test stub
	return nil, nil
}

func (s *configureNetworkStub) GetSubnet(_ context.Context, _ string) (*cpi.Subnet, error) { //nolint:nilnil // test stub
	return nil, nil
}

func (s *configureNetworkStub) ListSubnets(_ context.Context, _ string) ([]*cpi.Subnet, error) { //nolint:nilnil // test stub
	return nil, nil
}

func (s *configureNetworkStub) DeleteSubnet(_ context.Context, _ string) error { return nil }

func (s *configureNetworkStub) CreateSecurityGroup(_ context.Context, _ *cpi.CreateSecurityGroupRequest) (*cpi.SecurityGroup, error) { //nolint:nilnil // test stub
	return nil, nil
}

func (s *configureNetworkStub) GetSecurityGroup(_ context.Context, _ string) (*cpi.SecurityGroup, error) { //nolint:nilnil // test stub
	return nil, nil
}

func (s *configureNetworkStub) ListSecurityGroups(_ context.Context, _ map[string]string) ([]*cpi.SecurityGroup, error) { //nolint:nilnil // test stub
	return nil, nil
}

func (s *configureNetworkStub) DeleteSecurityGroup(_ context.Context, _ string) error { return nil }

func (s *configureNetworkStub) CreatePublicIP(_ context.Context, _ *cpi.PublicIPRequest) (*cpi.PublicIP, error) { //nolint:nilnil // test stub
	return nil, nil
}

func (s *configureNetworkStub) GetPublicIP(_ context.Context, _ string) (*cpi.PublicIP, error) { //nolint:nilnil // test stub
	return nil, nil
}

func (s *configureNetworkStub) ListPublicIPs(_ context.Context) ([]*cpi.PublicIP, error) { //nolint:nilnil // test stub
	return nil, nil
}

func (s *configureNetworkStub) DeletePublicIP(_ context.Context, _ string) error { return nil }

func (s *configureNetworkStub) AllocateFloatingIP(_ context.Context, _ *cpi.AllocateFloatingIPRequest) (*cpi.FloatingIP, error) { //nolint:nilnil // test stub
	return nil, nil
}

func (s *configureNetworkStub) GetFloatingIP(_ context.Context, _ string) (*cpi.FloatingIP, error) { //nolint:nilnil // test stub
	return nil, nil
}

func (s *configureNetworkStub) DisassociateFloatingIP(_ context.Context, _ string) error { return nil }

func (s *configureNetworkStub) ReleaseFloatingIP(_ context.Context, _ string) error { return nil }

func (s *configureNetworkStub) CreateRouter(_ context.Context, _ *cpi.CreateRouterRequest) (*cpi.Router, error) { //nolint:nilnil // test stub
	return nil, nil
}

func (s *configureNetworkStub) GetRouter(_ context.Context, _ string) (*cpi.Router, error) { //nolint:nilnil // test stub
	return nil, nil
}

func (s *configureNetworkStub) ListRouters(_ context.Context) ([]*cpi.Router, error) { //nolint:nilnil // test stub
	return nil, nil
}

func (s *configureNetworkStub) AttachRouterInterface(_ context.Context, _, _ string) error {
	return nil
}

func (s *configureNetworkStub) DetachRouterInterface(_ context.Context, _, _ string) error {
	return nil
}

func (s *configureNetworkStub) DeleteRouter(_ context.Context, _ string) error { return nil }

func (s *configureNetworkStub) CreateLoadBalancer(_ context.Context, _ *cpi.LoadBalancer) (*cpi.LoadBalancer, error) { //nolint:nilnil // test stub
	return nil, nil
}

func (s *configureNetworkStub) GetLoadBalancer(_ context.Context, _ string) (*cpi.LoadBalancer, error) { //nolint:nilnil // test stub
	return nil, nil
}

func (s *configureNetworkStub) ListLoadBalancers(_ context.Context, _ map[string]string) ([]*cpi.LoadBalancer, error) { //nolint:nilnil // test stub
	return nil, nil
}

func (s *configureNetworkStub) UpdateLoadBalancer(_ context.Context, _ *cpi.LoadBalancer) error {
	return nil
}

func (s *configureNetworkStub) DeleteLoadBalancer(_ context.Context, _ string) error { return nil }

func (s *configureNetworkStub) GetBackendPools(_ context.Context, _ string) ([]*cpi.BackendPool, error) { //nolint:nilnil // test stub
	return nil, nil
}

func (s *configureNetworkStub) AddBackendMember(_ context.Context, _ string, _ *cpi.BackendMember) error {
	return nil
}

func (s *configureNetworkStub) RemoveBackendMember(_ context.Context, _, _ string) error {
	return nil
}

func (s *configureNetworkStub) ConfigureHealthCheck(_ context.Context, _ string, _ *cpi.HealthCheck) error {
	return nil
}

func (s *configureNetworkStub) GetLoadBalancerHealth(_ context.Context, _ string) (*cpi.HealthStatus, error) { //nolint:nilnil // test stub
	return nil, nil
}

// configureTestProvider implements cpi.Provider for configure tests.
type configureTestProvider struct {
	network cpi.NetworkManager
	compute cpi.ComputeManager
}

func (p *configureTestProvider) Name() string                                   { return "aws" }
func (p *configureTestProvider) Region() string                                 { return "us-east-1" }
func (p *configureTestProvider) Authenticate(_ context.Context) error           { return nil }
func (p *configureTestProvider) ValidateCredentials(_ context.Context) error    { return nil }
func (p *configureTestProvider) Network() cpi.NetworkManager                    { return p.network } //nolint:ireturn
func (p *configureTestProvider) Compute() cpi.ComputeManager                   { return p.compute } //nolint:ireturn
func (p *configureTestProvider) Storage() cpi.StorageManager                   { return nil }        //nolint:ireturn
func (p *configureTestProvider) Security() cpi.SecurityManager                 { return nil }        //nolint:ireturn
func (p *configureTestProvider) LoadBalancer() cpi.LoadBalancerManager         { return nil }        //nolint:ireturn
func (p *configureTestProvider) NetworkManager() cpi.NetworkManager             { return p.network } //nolint:ireturn
func (p *configureTestProvider) ComputeManager() cpi.ComputeManager            { return p.compute } //nolint:ireturn
func (p *configureTestProvider) StorageManager() cpi.StorageManager            { return nil }        //nolint:ireturn
func (p *configureTestProvider) SecurityManager() cpi.SecurityManager          { return nil }        //nolint:ireturn
func (p *configureTestProvider) LoadBalancerManager() cpi.LoadBalancerManager  { return nil }        //nolint:ireturn
func (p *configureTestProvider) SupportsStorage() bool                          { return false }
func (p *configureTestProvider) Initialize(_ context.Context, _ interface{}) error { return nil }
func (p *configureTestProvider) Cleanup(_ context.Context) error                { return nil }

func TestConfigureFloatingIPsFiltersByBloc(t *testing.T) {
	t.Parallel()

	blocName := "520-aws-wayne"

	eip := &cpi.FloatingIP{
		ID:      "eipalloc-123",
		Address: "1.2.3.4",
	}

	network := &configureNetworkStub{
		listFloatingIPsReturn: []*cpi.FloatingIP{eip},
	}

	compute := &stubCompute{
		listResponses: map[string][]*cpi.Instance{
			fmt.Sprintf("map[label.bloc:%s label.role:bastion]", blocName): {
				{ID: "i-bastion", Name: blocName + "-bastion"},
			},
		},
	}

	provider := &configureTestProvider{
		network: network,
		compute: compute,
	}

	err := configureFloatingIPs(context.Background(), provider, blocName, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if network.listFloatingIPsFilters == nil {
		t.Fatal("expected bloc-scoped filters passed to ListFloatingIPs, got nil")
	}

	if got := network.listFloatingIPsFilters["bloc"]; got != blocName {
		t.Errorf("bloc filter: want %q, got %q", blocName, got)
	}

	if got := network.listFloatingIPsFilters["managed-by"]; got != "ocfp" {
		t.Errorf("managed-by filter: want %q, got %q", "ocfp", got)
	}
}

func TestConfigureFloatingIPsAssociatesCorrectEIP(t *testing.T) {
	t.Parallel()

	blocName := "520-aws-wayne"

	eip := &cpi.FloatingIP{
		ID:      "eipalloc-abc",
		Address: "5.6.7.8",
	}

	network := &configureNetworkStub{
		listFloatingIPsReturn: []*cpi.FloatingIP{eip},
	}

	compute := &stubCompute{
		listResponses: map[string][]*cpi.Instance{
			fmt.Sprintf("map[label.bloc:%s label.role:bastion]", blocName): {
				{ID: "i-bastion-456", Name: blocName + "-bastion"},
			},
		},
	}

	provider := &configureTestProvider{
		network: network,
		compute: compute,
	}

	// Not dry-run: should actually associate the EIP.
	err := configureFloatingIPs(context.Background(), provider, blocName, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if network.associatedIPID != eip.ID {
		t.Errorf("associated IP ID: want %q, got %q", eip.ID, network.associatedIPID)
	}

	if network.associatedInstanceID != "i-bastion-456" {
		t.Errorf("associated instance ID: want %q, got %q", "i-bastion-456", network.associatedInstanceID)
	}
}
