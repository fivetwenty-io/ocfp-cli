package bootstrap_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/bootstrap"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

// Use package defaultNetworkCIDR for shared test values

// Fakes for network + compute.
type fakeNet struct {
	createdNetworks        []*cpi.Network
	createdSubnets         []*cpi.Subnet
	createdSecurityGroups  []*cpi.SecurityGroup // Tracks NEW creations for test assertions
	existingSecurityGroups []*cpi.SecurityGroup // Pre-existing SGs in "cloud" (for Get/List)
}

func (f *fakeNet) CreateNetwork(ctx context.Context, req *cpi.NetworkRequest) (*cpi.Network, error) {
	network := &cpi.Network{
		ID:         "net-1",
		Name:       req.Name,
		CIDR:       req.CIDR,
		Region:     "",
		State:      cpi.ResourceStateActive,
		Tags:       req.Tags,
		DNSServers: nil,
		CreatedAt:  time.Time{},
		UpdatedAt:  time.Time{},
	}
	f.createdNetworks = append(f.createdNetworks, network)

	return network, nil
}
func (f *fakeNet) GetNetwork(ctx context.Context, id string) (*cpi.Network, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}
func (f *fakeNet) ListNetworks(ctx context.Context, filters map[string]string) ([]*cpi.Network, error) {
	return nil, nil
}
func (f *fakeNet) DeleteNetwork(ctx context.Context, id string) error { return nil }
func (f *fakeNet) CreateSubnet(ctx context.Context, req *cpi.SubnetRequest) (*cpi.Subnet, error) {
	id := "subnet-" + req.Name
	subnet := &cpi.Subnet{
		ID:               id,
		Name:             req.Name,
		NetworkID:        req.NetworkID,
		CIDR:             req.CIDR,
		AvailabilityZone: req.AvailabilityZone,
		Type:             req.Type,
		State:            cpi.ResourceStateActive,
		Tags:             req.Tags,
		CreatedAt:        time.Time{},
	}
	f.createdSubnets = append(f.createdSubnets, subnet)

	return subnet, nil
}
func (f *fakeNet) GetSubnet(ctx context.Context, id string) (*cpi.Subnet, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}
func (f *fakeNet) ListSubnets(ctx context.Context, networkID string) ([]*cpi.Subnet, error) {
	return nil, nil //nolint:nilnil // test fake
}
func (f *fakeNet) DeleteSubnet(ctx context.Context, id string) error { return nil }

// Security group operations.
func (f *fakeNet) CreateSecurityGroup(ctx context.Context, req *cpi.CreateSecurityGroupRequest) (*cpi.SecurityGroup, error) {
	sg := &cpi.SecurityGroup{
		ID:          "sg-" + req.Name,
		Name:        req.Name,
		Description: req.Description,
		NetworkID:   req.NetworkID,
		Rules:       req.Rules,
		Tags:        req.Tags,
	}

	if f.createdSecurityGroups == nil {
		f.createdSecurityGroups = make([]*cpi.SecurityGroup, 0)
	}

	f.createdSecurityGroups = append(f.createdSecurityGroups, sg)

	return sg, nil
}
func (f *fakeNet) GetSecurityGroup(ctx context.Context, id string) (*cpi.SecurityGroup, error) {
	// Check both newly created and pre-existing security groups
	for _, sg := range f.createdSecurityGroups {
		if sg.ID == id {
			return sg, nil
		}
	}
	for _, sg := range f.existingSecurityGroups {
		if sg.ID == id {
			return sg, nil
		}
	}
	return nil, fmt.Errorf("security group not found: %s", id)
}
func (f *fakeNet) ListSecurityGroups(ctx context.Context, filters map[string]string) ([]*cpi.SecurityGroup, error) {
	// Combine both newly created and pre-existing security groups
	allGroups := append([]*cpi.SecurityGroup{}, f.createdSecurityGroups...)
	allGroups = append(allGroups, f.existingSecurityGroups...)

	if len(filters) == 0 {
		return allGroups, nil
	}

	// Filter by provided criteria
	var filtered []*cpi.SecurityGroup
	for _, sg := range allGroups {
		match := true
		if name, ok := filters["name"]; ok && sg.Name != name {
			match = false
		}
		if networkID, ok := filters["network-id"]; ok && sg.NetworkID != networkID {
			match = false
		}
		if match {
			filtered = append(filtered, sg)
		}
	}
	return filtered, nil
}
func (f *fakeNet) DeleteSecurityGroup(ctx context.Context, id string) error { return nil }

// SecurityManager interface methods (for rule management).
func (f *fakeNet) AddSecurityRule(ctx context.Context, groupID string, rule *cpi.SecurityRule) error {
	// Find the security group and add the rule
	for _, sg := range f.createdSecurityGroups {
		if sg.ID == groupID {
			sg.Rules = append(sg.Rules, rule)
			return nil
		}
	}
	for _, sg := range f.existingSecurityGroups {
		if sg.ID == groupID {
			sg.Rules = append(sg.Rules, rule)
			return nil
		}
	}
	return fmt.Errorf("security group not found: %s", groupID)
}

func (f *fakeNet) RemoveSecurityRule(ctx context.Context, groupID string, ruleID string) error {
	return nil
}

func (f *fakeNet) ListSecurityRules(ctx context.Context, groupID string) ([]*cpi.SecurityRule, error) {
	for _, sg := range f.createdSecurityGroups {
		if sg.ID == groupID {
			return sg.Rules, nil
		}
	}
	for _, sg := range f.existingSecurityGroups {
		if sg.ID == groupID {
			return sg.Rules, nil
		}
	}
	return nil, fmt.Errorf("security group not found: %s", groupID)
}

// Public IP operations.
func (f *fakeNet) CreatePublicIP(ctx context.Context, req *cpi.PublicIPRequest) (*cpi.PublicIP, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}
func (f *fakeNet) GetPublicIP(ctx context.Context, id string) (*cpi.PublicIP, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}
func (f *fakeNet) ListPublicIPs(ctx context.Context) ([]*cpi.PublicIP, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}
func (f *fakeNet) DeletePublicIP(ctx context.Context, id string) error { return nil }
func (f *fakeNet) AllocateFloatingIP(ctx context.Context, req *cpi.AllocateFloatingIPRequest) (*cpi.FloatingIP, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}
func (f *fakeNet) GetFloatingIP(ctx context.Context, id string) (*cpi.FloatingIP, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}
func (f *fakeNet) ListFloatingIPs(ctx context.Context, filters map[string]string) ([]*cpi.FloatingIP, error) { //nolint:nilnil // test fake
	return nil, nil
}
func (f *fakeNet) AssociateFloatingIP(ctx context.Context, ipID string, instanceID string) error {
	return nil
}
func (f *fakeNet) DisassociateFloatingIP(ctx context.Context, ipID string) error { return nil }
func (f *fakeNet) ReleaseFloatingIP(ctx context.Context, id string) error        { return nil }
func (f *fakeNet) CreateRouter(ctx context.Context, req *cpi.CreateRouterRequest) (*cpi.Router, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}
func (f *fakeNet) GetRouter(ctx context.Context, id string) (*cpi.Router, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}
func (f *fakeNet) ListRouters(ctx context.Context) ([]*cpi.Router, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}
func (f *fakeNet) AttachRouterInterface(ctx context.Context, routerID string, subnetID string) error {
	return nil
}
func (f *fakeNet) DetachRouterInterface(ctx context.Context, routerID string, subnetID string) error {
	return nil
}
func (f *fakeNet) DeleteRouter(ctx context.Context, id string) error { return nil }
func (f *fakeNet) CreateLoadBalancer(ctx context.Context, config *cpi.LoadBalancer) (*cpi.LoadBalancer, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}
func (f *fakeNet) GetLoadBalancer(ctx context.Context, nameOrID string) (*cpi.LoadBalancer, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}
func (f *fakeNet) ListLoadBalancers(ctx context.Context, filters map[string]string) ([]*cpi.LoadBalancer, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}
func (f *fakeNet) UpdateLoadBalancer(ctx context.Context, lb *cpi.LoadBalancer) error { return nil }
func (f *fakeNet) DeleteLoadBalancer(ctx context.Context, id string) error            { return nil }
func (f *fakeNet) GetBackendPools(ctx context.Context, lbID string) ([]*cpi.BackendPool, error) { //nolint:nilnil // test fake
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
func (f *fakeNet) GetLoadBalancerHealth(ctx context.Context, lbID string) (*cpi.HealthStatus, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}

type fakeCompute struct{ lastReq *cpi.InstanceRequest }

func (f *fakeCompute) CreateInstance(ctx context.Context, req *cpi.InstanceRequest) (*cpi.Instance, error) {
	f.lastReq = req

	return &cpi.Instance{
		ID:               "inst-1",
		Name:             req.Name,
		State:            cpi.ResourceStateActive,
		Flavor:           "",
		Image:            "",
		NetworkID:        req.NetworkID,
		SubnetID:         req.SubnetID,
		PrivateIP:        "10.0.0.10",
		PublicIP:         "",
		FloatingIP:       "",
		SecurityGroups:   nil,
		KeyPair:          "",
		AvailabilityZone: "",
		Tags:             req.Tags,
		Volumes:          nil,
		CreatedAt:        time.Time{},
		UpdatedAt:        time.Time{},
	}, nil
}
func (f *fakeCompute) GetInstance(ctx context.Context, id string) (*cpi.Instance, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}
func (f *fakeCompute) ListInstances(ctx context.Context, filters map[string]string) ([]*cpi.Instance, error) { //nolint:nilnil // test fake
	return nil, nil
}
func (f *fakeCompute) StartInstance(ctx context.Context, id string) error  { return nil }
func (f *fakeCompute) StopInstance(ctx context.Context, id string) error   { return nil }
func (f *fakeCompute) RebootInstance(ctx context.Context, id string) error { return nil }
func (f *fakeCompute) DeleteInstance(ctx context.Context, id string) error { return nil }
func (f *fakeCompute) CreateKeyPair(ctx context.Context, req *cpi.KeyPairRequest) (*cpi.KeyPair, error) {
	return &cpi.KeyPair{
		Name:        req.Name,
		Fingerprint: "",
		PublicKey:   "",
		PrivateKey:  "",
		CreatedAt:   time.Time{},
	}, nil
}
func (f *fakeCompute) ImportKeyPair(ctx context.Context, name string, publicKey string) error {
	return nil
}
func (f *fakeCompute) GetKeyPair(ctx context.Context, name string) (*cpi.KeyPair, error) {
	return &cpi.KeyPair{
		Name:        name,
		Fingerprint: "",
		PublicKey:   "",
		PrivateKey:  "",
		CreatedAt:   time.Time{},
	}, nil
}
func (f *fakeCompute) ListKeyPairs(ctx context.Context) ([]*cpi.KeyPair, error) { //nolint:nilnil // test fake
	return nil, nil
}
func (f *fakeCompute) DeleteKeyPair(ctx context.Context, name string) error { return nil }
func (f *fakeCompute) ListImages(ctx context.Context, filters map[string]string) ([]*cpi.Image, error) { //nolint:nilnil // test fake
	return nil, nil
}
func (f *fakeCompute) GetImage(ctx context.Context, id string) (*cpi.Image, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}
func (f *fakeCompute) ListFlavors(ctx context.Context) ([]*cpi.Flavor, error) { //nolint:nilnil // test fake
	return nil, nil
}
func (f *fakeCompute) GetFlavor(ctx context.Context, id string) (*cpi.Flavor, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}

// Volume operations.
func (f *fakeCompute) CreateVolume(ctx context.Context, req *cpi.VolumeRequest) (*cpi.Volume, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}
func (f *fakeCompute) GetVolume(ctx context.Context, id string) (*cpi.Volume, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}
func (f *fakeCompute) ListVolumes(ctx context.Context, filters map[string]string) ([]*cpi.Volume, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}
func (f *fakeCompute) DeleteVolume(ctx context.Context, id string) error { return nil }

type fakeProv struct {
	n interface {
		cpi.NetworkManager
		cpi.SecurityManager
	}
	c cpi.ComputeManager
}

func (p *fakeProv) Name() string                                  { return "fake" }
func (p *fakeProv) Region() string                                { return "eu01" }
func (p *fakeProv) Authenticate(ctx context.Context) error        { return nil }
func (p *fakeProv) ValidateCredentials(ctx context.Context) error { return nil }

//nolint:ireturn
func (p *fakeProv) Network() cpi.NetworkManager { return p.n }

//nolint:ireturn
func (p *fakeProv) Compute() cpi.ComputeManager { return p.c }

//nolint:ireturn
func (p *fakeProv) Security() cpi.SecurityManager { return p.n }

//nolint:ireturn
func (p *fakeProv) Storage() cpi.StorageManager { return nil }

//nolint:ireturn
func (p *fakeProv) LoadBalancer() cpi.LoadBalancerManager { return nil }

// New method names for backward compatibility
//
//nolint:ireturn
func (p *fakeProv) NetworkManager() cpi.NetworkManager { return p.Network() }

//nolint:ireturn
func (p *fakeProv) ComputeManager() cpi.ComputeManager { return p.Compute() }

//nolint:ireturn
func (p *fakeProv) StorageManager() cpi.StorageManager { return p.Storage() }

//nolint:ireturn
func (p *fakeProv) SecurityManager() cpi.SecurityManager { return p.Security() }

//nolint:ireturn
func (p *fakeProv) LoadBalancerManager() cpi.LoadBalancerManager { return p.LoadBalancer() }

func (p *fakeProv) SupportsStorage() bool                                 { return true }
func (p *fakeProv) Initialize(ctx context.Context, cfg interface{}) error { return nil }
func (p *fakeProv) Cleanup(ctx context.Context) error                     { return nil }

func createTestConfig() *config.Config {
	cfg := &config.Config{
		Name:      "prod",
		Region:    "eu01",
		Network:   createTestNetworkConfig(),
		Bastion:   createTestBastionConfig(),
		Genesis:   createTestGenesisConfig(),
		Routers:   createTestComponentConfig(),
		Cells:     createTestComponentConfig(),
		Blobstore: createTestBlobstoreConfig(),
	}

	return cfg
}

func createTestNetworkConfig() config.NetworkConfig {
	return config.NetworkConfig{}
}

func createTestBastionConfig() config.Bastion {
	return config.Bastion{
		Genesis:   createTestGenesisConfig(),
		Git:       createTestGitConfig(),
		Tools:     createTestOverrideSets(),
		CFPlugins: createTestOverrideSets(),
		Snaps:     createTestOverrideSets(),
	}
}

func createTestGenesisConfig() config.Genesis {
	return config.Genesis{}
}

func createTestGitConfig() config.GitConfig {
	return config.GitConfig{
		User: config.GitUser{},
	}
}

func createTestOverrideSets() config.OverrideSets {
	return config.OverrideSets{}
}

func createTestComponentConfig() config.ComponentConfig {
	return config.ComponentConfig{}
}

func createTestBlobstoreConfig() config.BlobstoreConfig {
	return config.BlobstoreConfig{
		BoshBlobstore: config.BucketSettings{},
		CFBuildpacks:  config.BucketSettings{},
		CFDroplets:    config.BucketSettings{},
		CFAppPackages: config.BucketSettings{},
	}
}

func TestSplitParentIntoTwo(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in           string
		want1, want2 string
	}{
		{"10.4.0.0/20", "10.4.0.0/21", "10.4.8.0/21"},
		{"10.4.0.0/23", "10.4.0.0/24", "10.4.1.0/24"},
		{"10.4.0.0/24", "10.4.0.0/25", "10.4.0.128/25"},
	}
	for _, testCase := range cases {
		first, second := bootstrap.SplitParentIntoTwo(testCase.in)
		if first != testCase.want1 || second != testCase.want2 {
			t.Fatalf("splitParentIntoTwo(%s) = %s,%s want %s,%s", testCase.in, first, second, testCase.want1, testCase.want2)
		}
	}
}

func TestCreateSubnets_Stackit_UsesVirtualOcfp0Only(t *testing.T) {
	t.Parallel()

	manager, fakeNetwork := setupStackitSubnetTest(t)
	ctx := context.Background()

	createNetworkAndSubnets(t, manager, ctx)
	verifyVirtualOnlySubnetsCreated(t, fakeNetwork, manager.StateManager())
}

func setupStackitSubnetTest(t *testing.T) (*bootstrap.Manager, *fakeNet) {
	t.Helper()
	tmp := t.TempDir()

	stateManager, err := state.NewManager(filepath.Join(tmp, ".state"))
	if err != nil {
		t.Fatal(err)
	}

	_, err = stateManager.Load("prod")
	if err != nil {
		t.Fatal(err)
	}

	cfg := createTestConfig()
	cfg.Network.NetworkCIDR = "10.4.0.0/20"

	fakeNetwork := &fakeNet{
		createdNetworks: nil,
		createdSubnets:  nil,
	}
	fakeProvider := &fakeProv{n: fakeNetwork, c: &fakeCompute{
		lastReq: nil,
	}}
	manager := bootstrap.NewManager(cfg, fakeProvider, stateManager, &bootstrap.Options{
		BlocName: "prod",
		Provider: "stackit",
		Region:   "eu01",
		Force:    false,
		DryRun:   false,
		Output:   "",
		Timeout:  0,
	})

	return manager, fakeNetwork
}

func createNetworkAndSubnets(t *testing.T, manager *bootstrap.Manager, ctx context.Context) {
	t.Helper()

	err := manager.CreateNetwork(ctx)
	if err != nil {
		t.Fatalf("createNetwork: %v", err)
	}

	err = manager.CreateSubnets(ctx)
	if err != nil {
		t.Fatalf("createSubnets: %v", err)
	}
}

func verifyVirtualOnlySubnetsCreated(t *testing.T, fakeNetwork *fakeNet, stateManager *state.Manager) {
	t.Helper()

	if got := len(fakeNetwork.createdSubnets); got != 0 {
		t.Fatalf("created %d real subnets, want 0 (virtual only)", got)
	}

	verifyVirtualSubnetInState(t, stateManager)
	verifySubnetOutputs(t, stateManager)
	verifyReservedIPOutputs(t, stateManager)
}

func verifyVirtualSubnetInState(t *testing.T, stateManager *state.Manager) {
	t.Helper()

	res, err := stateManager.GetResource("subnet", "prod-ocfp-0")
	if err != nil {
		t.Fatalf("expected virtual subnet prod-ocfp-0 in state: %v", err)
	}

	if res.Properties["ip_0"] == "" || res.Properties["ip_n"] == "" || res.Properties["gateway"] == "" {
		t.Fatalf("expected reserved fields on virtual subnet: %+v", res.Properties)
	}
}

func verifySubnetOutputs(t *testing.T, stateManager *state.Manager) {
	t.Helper()

	_, err := stateManager.GetOutput("subnet_prod-ocfp-0_id")
	if err != nil {
		t.Fatalf("missing output subnet_prod-ocfp-0_id")
	}

	_, err = stateManager.GetOutput("subnet_prod-ocfp-0_cidr")
	if err != nil {
		t.Fatalf("missing output subnet_prod-ocfp-0_cidr")
	}
}

func verifyReservedIPOutputs(t *testing.T, stateManager *state.Manager) {
	t.Helper()

	_, err := stateManager.GetOutput("reserved_prod-ocfp-0_bastion_ip")
	if err != nil {
		t.Fatalf("missing bastion reserved ip output")
	}

	_, err = stateManager.GetOutput("reserved_prod-ocfp-0_vault_ip")
	if err != nil {
		t.Fatalf("missing vault reserved ip output")
	}

	_, err = stateManager.GetOutput("reserved_prod-ocfp-0_available_a")
	if err != nil {
		t.Fatalf("missing available_a output")
	}
}

func TestCreateBastion_Stackit_UsesNetworkOnlyAndDependsOnVirtual(t *testing.T) {
	t.Parallel()

	manager, fakeComp := setupStackitBastionTest(t)
	ctx := context.Background()

	createNetworkSubnetsAndBastion(t, manager, ctx)
	verifyBastionStackitBehavior(t, fakeComp, manager.StateManager())
}

func setupStackitBastionTest(t *testing.T) (*bootstrap.Manager, *fakeCompute) {
	t.Helper()
	tmp := t.TempDir()

	stateManager, err := state.NewManager(filepath.Join(tmp, ".state"))
	if err != nil {
		t.Fatal(err)
	}

	_, err = stateManager.Load("prod")
	if err != nil {
		t.Fatal(err)
	}

	cfg := createTestConfig()
	cfg.Network.NetworkCIDR = "10.4.0.0/23"

	fakeNetwork := &fakeNet{
		createdNetworks: nil,
		createdSubnets:  nil,
	}
	fakeComp := &fakeCompute{
		lastReq: nil,
	}
	fakeProvider := &fakeProv{n: fakeNetwork, c: fakeComp}
	manager := bootstrap.NewManager(cfg, fakeProvider, stateManager, &bootstrap.Options{
		BlocName: "prod",
		Provider: "stackit",
		Region:   "eu01",
		Force:    false,
		DryRun:   false,
		Output:   "",
		Timeout:  0,
	})

	return manager, fakeComp
}

func createNetworkSubnetsAndBastion(t *testing.T, manager *bootstrap.Manager, ctx context.Context) {
	t.Helper()

	err := manager.CreateNetwork(ctx)
	if err != nil {
		t.Fatalf("createNetwork: %v", err)
	}

	err = manager.CreateSubnets(ctx)
	if err != nil {
		t.Fatalf("createSubnets: %v", err)
	}

	_ = manager.StateManager().SetOutput("sg_bastion_id", "sg-1")

	err = manager.CreateBastion(ctx)
	if err != nil {
		t.Fatalf("createBastion: %v", err)
	}
}

func verifyBastionStackitBehavior(t *testing.T, fakeComp *fakeCompute, stateManager *state.Manager) {
	t.Helper()

	if fakeComp.lastReq == nil {
		t.Fatalf("expected instance create to be called")
	}

	if fakeComp.lastReq.SubnetID != "" {
		t.Fatalf("bastion SubnetID = %q, want empty for stackit", fakeComp.lastReq.SubnetID)
	}

	verifyBastionDependsOnVirtualSubnet(t, stateManager)
}

func verifyBastionDependsOnVirtualSubnet(t *testing.T, stateManager *state.Manager) {
	t.Helper()

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
	t.Parallel()

	manager, fakeNetwork := setupStackitOcfpTripleTest(t)
	ctx := context.Background()

	createNetworkAndSubnets(t, manager, ctx)
	verifyOcfpTripleSubnets(t, fakeNetwork, manager.StateManager())
}

func setupStackitOcfpTripleTest(t *testing.T) (*bootstrap.Manager, *fakeNet) {
	t.Helper()
	tmp := t.TempDir()

	stateManager, err := state.NewManager(filepath.Join(tmp, ".state"))
	if err != nil {
		t.Fatal(err)
	}

	_, err = stateManager.Load("prod")
	if err != nil {
		t.Fatal(err)
	}

	cfg := createOcfpTripleConfig()
	fakeNetwork := &fakeNet{
		createdNetworks: nil,
		createdSubnets:  nil,
	}
	fakeProvider := &fakeProv{n: fakeNetwork, c: &fakeCompute{
		lastReq: nil,
	}}
	manager := bootstrap.NewManager(cfg, fakeProvider, stateManager, &bootstrap.Options{
		BlocName: "prod",
		Provider: "stackit",
		Region:   "eu01",
		Force:    false,
		DryRun:   false,
		Output:   "",
		Timeout:  0,
	})

	return manager, fakeNetwork
}

func createOcfpTripleConfig() *config.Config {
	cfg := &config.Config{
		Name:              "prod",
		Region:            "eu01",
		SubnetStrategy:    "ocfp-triple",
		Network:           createEmptyNetworkConfig(),
		Bastion:           createEmptyBastionConfig(),
		Genesis:           createEmptyGenesisConfig(),
		Deployments:       config.NewDeploymentSettings("", nil),
		Routers:           createEmptyComponentConfig(),
		Cells:             createEmptyComponentConfig(),
		Blobstore:         createEmptyBlobstoreConfig(),
		DNS:               []string{},
		AZs:               map[string]config.AvailabilityZone{},
		FQDNs:             map[string]interface{}{},
		S3:                map[string]string{},
		AllowedIngressIPs: []string{},
		Subnets:           []config.Subnet{},
		LBs:               map[string]config.LBService{},
		Users:             map[string]string{},
	}
	cfg.Network.NetworkCIDR = "10.4.0.0/20"

	return cfg
}

func createEmptyNetworkConfig() config.NetworkConfig {
	return config.NetworkConfig{}
}

func createEmptyBastionConfig() config.Bastion {
	return config.Bastion{
		Genesis:   createEmptyGenesisConfig(),
		Git:       createEmptyGitConfig(),
		Tools:     createEmptyOverrideSets(),
		CFPlugins: createEmptyOverrideSets(),
		Snaps:     createEmptyOverrideSets(),
	}
}

func createEmptyGenesisConfig() config.Genesis {
	return config.Genesis{}
}

func createEmptyComponentConfig() config.ComponentConfig {
	return config.ComponentConfig{}
}

func createEmptyBlobstoreConfig() config.BlobstoreConfig {
	return config.BlobstoreConfig{
		BoshBlobstore: config.BucketSettings{},
		CFBuildpacks:  config.BucketSettings{},
		CFDroplets:    config.BucketSettings{},
		CFAppPackages: config.BucketSettings{},
	}
}

func createEmptyGitConfig() config.GitConfig {
	return config.GitConfig{
		User: config.GitUser{},
	}
}

func createEmptyOverrideSets() config.OverrideSets {
	return config.OverrideSets{}
}

func verifyOcfpTripleSubnets(t *testing.T, fakeNetwork *fakeNet, stateManager *state.Manager) {
	t.Helper()

	if len(fakeNetwork.createdSubnets) != 0 {
		t.Fatalf("expected 0 real subnets, got %d", len(fakeNetwork.createdSubnets))
	}

	expectedCIDRs := []string{"10.4.4.0/22", "10.4.8.0/22", "10.4.12.0/22"}
	for index, expectedCIDR := range expectedCIDRs {
		verifyOcfpSubnet(t, stateManager, index, expectedCIDR)
	}
}

func verifyOcfpSubnet(t *testing.T, stateManager *state.Manager, index int, expectedCIDR string) {
	t.Helper()

	name := fmt.Sprintf("prod-ocfp-%d", index)

	res, err := stateManager.GetResource("subnet", name)
	if err != nil {
		t.Fatalf("missing virtual subnet %s: %v", name, err)
	}

	if res.Properties["cidr"] != expectedCIDR {
		t.Fatalf("%s cidr=%v want %s", name, res.Properties["cidr"], expectedCIDR)
	}

	if res.Properties["ip_0"] == "" || res.Properties["ip_n"] == "" || res.Properties["gateway"] == "" {
		t.Fatalf("%s missing reserved fields: %+v", name, res.Properties)
	}

	if index == 1 {
		verifyDoomsdayIPOutput(t, stateManager, name)
	}
}

func verifyDoomsdayIPOutput(t *testing.T, stateManager *state.Manager, subnetName string) {
	t.Helper()

	_, err := stateManager.GetOutput("reserved_" + subnetName + "_doomsday_ip")
	if err != nil {
		t.Fatalf("missing doomsday ip for %s", subnetName)
	}
}
