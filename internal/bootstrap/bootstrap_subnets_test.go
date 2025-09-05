package bootstrap

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

// Fakes for network + compute
type fakeNet struct {
	createdNetworks []*cpi.Network
	createdSubnets  []*cpi.Subnet
}

func (f *fakeNet) CreateNetwork(ctx context.Context, req *cpi.CreateNetworkRequest) (*cpi.Network, error) {
	n := &cpi.Network{ID: "net-1", Name: req.Name, CIDR: req.CIDR, Tags: req.Tags, State: cpi.ResourceStateActive}
	f.createdNetworks = append(f.createdNetworks, n)
	return n, nil
}
func (f *fakeNet) GetNetwork(ctx context.Context, id string) (*cpi.Network, error) { return nil, nil }
func (f *fakeNet) ListNetworks(ctx context.Context, filters map[string]string) ([]*cpi.Network, error) {
	return nil, nil
}
func (f *fakeNet) DeleteNetwork(ctx context.Context, id string) error { return nil }
func (f *fakeNet) CreateSubnet(ctx context.Context, req *cpi.CreateSubnetRequest) (*cpi.Subnet, error) {
	id := "subnet-" + req.Name
	s := &cpi.Subnet{ID: id, Name: req.Name, NetworkID: req.NetworkID, CIDR: req.CIDR, AvailabilityZone: req.AvailabilityZone, Type: req.Type, State: cpi.ResourceStateActive, Tags: req.Tags}
	f.createdSubnets = append(f.createdSubnets, s)
	return s, nil
}
func (f *fakeNet) GetSubnet(ctx context.Context, id string) (*cpi.Subnet, error) { return nil, nil }
func (f *fakeNet) ListSubnets(ctx context.Context, networkID string) ([]*cpi.Subnet, error) {
	return nil, nil
}
func (f *fakeNet) DeleteSubnet(ctx context.Context, id string) error { return nil }
func (f *fakeNet) AllocateFloatingIP(ctx context.Context, req *cpi.AllocateFloatingIPRequest) (*cpi.FloatingIP, error) {
	return nil, nil
}
func (f *fakeNet) GetFloatingIP(ctx context.Context, id string) (*cpi.FloatingIP, error) {
	return nil, nil
}
func (f *fakeNet) ListFloatingIPs(ctx context.Context) ([]*cpi.FloatingIP, error) { return nil, nil }
func (f *fakeNet) AssociateFloatingIP(ctx context.Context, ipID string, instanceID string) error {
	return nil
}
func (f *fakeNet) DisassociateFloatingIP(ctx context.Context, ipID string) error { return nil }
func (f *fakeNet) ReleaseFloatingIP(ctx context.Context, id string) error        { return nil }
func (f *fakeNet) CreateRouter(ctx context.Context, req *cpi.CreateRouterRequest) (*cpi.Router, error) {
	return nil, nil
}
func (f *fakeNet) GetRouter(ctx context.Context, id string) (*cpi.Router, error) { return nil, nil }
func (f *fakeNet) ListRouters(ctx context.Context) ([]*cpi.Router, error)        { return nil, nil }
func (f *fakeNet) AttachRouterInterface(ctx context.Context, routerID string, subnetID string) error {
	return nil
}
func (f *fakeNet) DetachRouterInterface(ctx context.Context, routerID string, subnetID string) error {
	return nil
}
func (f *fakeNet) DeleteRouter(ctx context.Context, id string) error { return nil }
func (f *fakeNet) CreateLoadBalancer(ctx context.Context, config *cpi.LoadBalancer) (*cpi.LoadBalancer, error) {
	return nil, nil
}
func (f *fakeNet) GetLoadBalancer(ctx context.Context, nameOrID string) (*cpi.LoadBalancer, error) {
	return nil, nil
}
func (f *fakeNet) ListLoadBalancers(ctx context.Context, filters map[string]string) ([]*cpi.LoadBalancer, error) {
	return nil, nil
}
func (f *fakeNet) UpdateLoadBalancer(ctx context.Context, lb *cpi.LoadBalancer) error { return nil }
func (f *fakeNet) DeleteLoadBalancer(ctx context.Context, id string) error            { return nil }
func (f *fakeNet) GetBackendPools(ctx context.Context, lbID string) ([]*cpi.BackendPool, error) {
	return nil, nil
}
func (f *fakeNet) AddBackendMember(ctx context.Context, lbID string, member *cpi.BackendMember) error {
	return nil
}
func (f *fakeNet) RemoveBackendMember(ctx context.Context, lbID string, memberIP string) error {
	return nil
}
func (f *fakeNet) ConfigureHealthCheck(ctx context.Context, lbID string, check *cpi.HealthCheck) error {
	return nil
}
func (f *fakeNet) GetLoadBalancerHealth(ctx context.Context, lbID string) (*cpi.HealthStatus, error) {
	return nil, nil
}

type fakeCompute struct{ lastReq *cpi.CreateInstanceRequest }

func (f *fakeCompute) CreateInstance(ctx context.Context, req *cpi.CreateInstanceRequest) (*cpi.Instance, error) {
	f.lastReq = req
	return &cpi.Instance{ID: "inst-1", Name: req.Name, NetworkID: req.NetworkID, SubnetID: req.SubnetID, PrivateIP: "10.0.0.10", State: cpi.ResourceStateActive, Tags: req.Tags}, nil
}
func (f *fakeCompute) GetInstance(ctx context.Context, id string) (*cpi.Instance, error) {
	return nil, nil
}
func (f *fakeCompute) ListInstances(ctx context.Context, filters map[string]string) ([]*cpi.Instance, error) {
	return nil, nil
}
func (f *fakeCompute) StartInstance(ctx context.Context, id string) error  { return nil }
func (f *fakeCompute) StopInstance(ctx context.Context, id string) error   { return nil }
func (f *fakeCompute) RebootInstance(ctx context.Context, id string) error { return nil }
func (f *fakeCompute) DeleteInstance(ctx context.Context, id string) error { return nil }
func (f *fakeCompute) CreateKeyPair(ctx context.Context, name string) (*cpi.KeyPair, error) {
	return &cpi.KeyPair{Name: name}, nil
}
func (f *fakeCompute) ImportKeyPair(ctx context.Context, name string, publicKey string) error {
	return nil
}
func (f *fakeCompute) GetKeyPair(ctx context.Context, name string) (*cpi.KeyPair, error) {
	return &cpi.KeyPair{Name: name}, nil
}
func (f *fakeCompute) ListKeyPairs(ctx context.Context) ([]*cpi.KeyPair, error) { return nil, nil }
func (f *fakeCompute) DeleteKeyPair(ctx context.Context, name string) error     { return nil }
func (f *fakeCompute) ListImages(ctx context.Context, filters map[string]string) ([]*cpi.Image, error) {
	return nil, nil
}
func (f *fakeCompute) GetImage(ctx context.Context, id string) (*cpi.Image, error)   { return nil, nil }
func (f *fakeCompute) ListFlavors(ctx context.Context) ([]*cpi.Flavor, error)        { return nil, nil }
func (f *fakeCompute) GetFlavor(ctx context.Context, id string) (*cpi.Flavor, error) { return nil, nil }

type fakeProv struct {
	n cpi.NetworkManager
	c cpi.ComputeManager
}

func (p *fakeProv) Name() string                                          { return "fake" }
func (p *fakeProv) Region() string                                        { return "eu01" }
func (p *fakeProv) Authenticate(ctx context.Context) error                { return nil }
func (p *fakeProv) ValidateCredentials(ctx context.Context) error         { return nil }
func (p *fakeProv) Network() cpi.NetworkManager                           { return p.n }
func (p *fakeProv) Compute() cpi.ComputeManager                           { return p.c }
func (p *fakeProv) Storage() cpi.StorageManager                           { return nil }
func (p *fakeProv) Security() cpi.SecurityManager                         { return nil }
func (p *fakeProv) LoadBalancer() cpi.LoadBalancerManager                 { return nil }
func (p *fakeProv) Initialize(ctx context.Context, cfg interface{}) error { return nil }
func (p *fakeProv) Cleanup(ctx context.Context) error                     { return nil }

func TestSplitParentIntoTwo(t *testing.T) {
	cases := []struct {
		in           string
		want1, want2 string
	}{
		{"10.4.0.0/20", "10.4.0.0/21", "10.4.8.0/21"},
		{"10.4.0.0/23", "10.4.0.0/24", "10.4.1.0/24"},
		{"10.4.0.0/24", "10.4.0.0/25", "10.4.0.128/25"},
	}
	for _, testCase := range cases {
		first, second := splitParentIntoTwo(testCase.in)
		if first != testCase.want1 || second != testCase.want2 {
			t.Fatalf("splitParentIntoTwo(%s) = %s,%s want %s,%s", testCase.in, first, second, testCase.want1, testCase.want2)
		}
	}
}

func TestCreateSubnets_Stackit_UsesVirtualOcfp0Only(t *testing.T) {
	tmp := t.TempDir()
	stateManager, err := state.NewManager(filepath.Join(tmp, ".state"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stateManager.Load("prod"); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{Name: "prod", Region: "eu01"}
	cfg.Network.NetworkCIDR = "10.4.0.0/20"

	fakeNetwork := &fakeNet{}
	fakeProvider := &fakeProv{n: fakeNetwork, c: &fakeCompute{}}
	manager := NewManager(cfg, fakeProvider, stateManager, &Options{BlocName: "prod", Provider: "stackit", Region: "eu01"})

	ctx := context.Background()
	if err := manager.createNetwork(ctx); err != nil {
		t.Fatalf("createNetwork: %v", err)
	}
	if err := manager.createSubnets(ctx); err != nil {
		t.Fatalf("createSubnets: %v", err)
	}

	// Expect no real subnets created for stackit
	if got := len(fakeNetwork.createdSubnets); got != 0 {
		t.Fatalf("created %d real subnets, want 0 (virtual only)", got)
	}
	// Virtual subnet resource exists in state with reserved fields
	res, err := stateManager.GetResource("subnet", "prod-ocfp-0")
	if err != nil {
		t.Fatalf("expected virtual subnet prod-ocfp-0 in state: %v", err)
	}
	if _, err := stateManager.GetOutput("subnet_prod-ocfp-0_id"); err != nil {
		t.Fatalf("missing output subnet_prod-ocfp-0_id")
	}
	if _, err := stateManager.GetOutput("subnet_prod-ocfp-0_cidr"); err != nil {
		t.Fatalf("missing output subnet_prod-ocfp-0_cidr")
	}
	if res.Properties["ip_0"] == "" || res.Properties["ip_n"] == "" || res.Properties["gateway"] == "" {
		t.Fatalf("expected reserved fields on virtual subnet: %+v", res.Properties)
	}
	// Check a couple of reserved IP outputs
	if _, err := stateManager.GetOutput("reserved_prod-ocfp-0_bastion_ip"); err != nil {
		t.Fatalf("missing bastion reserved ip output")
	}
	if _, err := stateManager.GetOutput("reserved_prod-ocfp-0_vault_ip"); err != nil {
		t.Fatalf("missing vault reserved ip output")
	}
	if _, err := stateManager.GetOutput("reserved_prod-ocfp-0_available_a"); err != nil {
		t.Fatalf("missing available_a output")
	}
}

func TestCreateBastion_Stackit_UsesNetworkOnlyAndDependsOnVirtual(t *testing.T) {
	tmp := t.TempDir()
	stateManager, err := state.NewManager(filepath.Join(tmp, ".state"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stateManager.Load("prod"); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{Name: "prod", Region: "eu01"}
	cfg.Network.NetworkCIDR = "10.4.0.0/23" // becomes two /24s

	fakeNetwork := &fakeNet{}
	fakeComp := &fakeCompute{}
	fakeProvider := &fakeProv{n: fakeNetwork, c: fakeComp}
	manager := NewManager(cfg, fakeProvider, stateManager, &Options{BlocName: "prod", Provider: "stackit", Region: "eu01"})

	ctx := context.Background()
	if err := manager.createNetwork(ctx); err != nil {
		t.Fatalf("createNetwork: %v", err)
	}
	if err := manager.createSubnets(ctx); err != nil {
		t.Fatalf("createSubnets: %v", err)
	}
	// Seed required SG output for bastion
	_ = stateManager.SetOutput("sg_bastion_id", "sg-1")

	if err := manager.createBastion(ctx); err != nil {
		t.Fatalf("createBastion: %v", err)
	}

	if fakeComp.lastReq == nil {
		t.Fatalf("expected instance create to be called")
	}
	// Subnet should be empty for STACKIT requests
	if fakeComp.lastReq.SubnetID != "" {
		t.Fatalf("bastion SubnetID = %q, want empty for stackit", fakeComp.lastReq.SubnetID)
	}

	// Dependencies should include subnet.prod-ocfp-0
	deps, err := stateManager.GetDependencies("instance.prod-bastion")
	if err != nil {
		t.Fatalf("get deps: %v", err)
	}
	found := false
	for _, dependency := range deps {
		if dependency == "subnet.prod-ocfp-0" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected dependency on subnet.prod-ocfp-0, got %v", deps)
	}
}

func TestCreateSubnets_Stackit_OcfpTriple_VirtualsAndReserved(t *testing.T) {
	tmp := t.TempDir()
	stateManager, err := state.NewManager(filepath.Join(tmp, ".state"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stateManager.Load("prod"); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{Name: "prod", Region: "eu01", SubnetStrategy: "ocfp-triple"}
	cfg.Network.NetworkCIDR = "10.4.0.0/20"

	fakeNetwork := &fakeNet{}
	fakeProvider := &fakeProv{n: fakeNetwork, c: &fakeCompute{}}
	manager := NewManager(cfg, fakeProvider, stateManager, &Options{BlocName: "prod", Provider: "stackit", Region: "eu01"})

	ctx := context.Background()
	if err := manager.createNetwork(ctx); err != nil {
		t.Fatalf("createNetwork: %v", err)
	}
	if err := manager.createSubnets(ctx); err != nil {
		t.Fatalf("createSubnets: %v", err)
	}

	// No real subnets for stackit
	if len(fakeNetwork.createdSubnets) != 0 {
		t.Fatalf("expected 0 real subnets, got %d", len(fakeNetwork.createdSubnets))
	}

	// Expect ocfp-0..2
	for index, want := range []string{"10.4.4.0/22", "10.4.8.0/22", "10.4.12.0/22"} {
		name := fmt.Sprintf("prod-ocfp-%d", index)
		res, err := stateManager.GetResource("subnet", name)
		if err != nil {
			t.Fatalf("missing virtual subnet %s: %v", name, err)
		}
		if res.Properties["cidr"] != want {
			t.Fatalf("%s cidr=%v want %s", name, res.Properties["cidr"], want)
		}
		if res.Properties["ip_0"] == "" || res.Properties["ip_n"] == "" || res.Properties["gateway"] == "" {
			t.Fatalf("%s missing reserved fields: %+v", name, res.Properties)
		}
		// spot-check conditional assignments
		if index == 1 {
			if _, err := stateManager.GetOutput("reserved_" + name + "_doomsday_ip"); err != nil {
				t.Fatalf("missing doomsday ip for %s", name)
			}
		}
	}
}
