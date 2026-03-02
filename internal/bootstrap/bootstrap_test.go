package bootstrap_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/bootstrap"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

// ==============================================================================
// Helper Functions for Execute Tests
// ==============================================================================

func setupExecuteTest(t *testing.T, provider string) (*bootstrap.Manager, *fakeNetEnhanced, *fakeComputeEnhanced, *state.Manager) {
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

	cfg := createExecuteTestConfig()
	fakeNetwork := newFakeNetEnhanced()
	fakeCompute := newFakeComputeEnhanced()
	fakeStorage := newFakeStorage()
	fakeProvider := &fakeProvWithStorage{n: fakeNetwork, c: fakeCompute, s: fakeStorage}

	manager := bootstrap.NewManager(cfg, fakeProvider, stateManager, &bootstrap.Options{
		BlocName: "prod",
		Provider: provider,
		Region:   "eu01",
		Force:    false,
		Yes:      true, // Skip confirmation prompt in tests
		DryRun:   false,
		Output:   "",
		Timeout:  0,
	})

	// Pre-create keypair in state to avoid file I/O in tests (tests idempotency)
	err = stateManager.AddResource(&state.Resource{
		ID:   "kp-prod-keypair",
		Type: state.ResourceTypeKeyPair,
		Name: "prod-keypair",
		Properties: map[string]interface{}{
			"public_key":  "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAFAKE prod-keypair",
			"fingerprint": "fake:fingerprint:test",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = stateManager.SetOutput("keypair_name", "prod-keypair")

	return manager, fakeNetwork, fakeCompute, stateManager
}

// fakeProvWithStorage extends fakeProv to support storage operations.
type fakeProvWithStorage struct {
	n cpi.NetworkManager
	c cpi.ComputeManager
	s cpi.StorageManager
}

func (p *fakeProvWithStorage) Name() string                                  { return "fake" }
func (p *fakeProvWithStorage) Region() string                                { return "eu01" }
func (p *fakeProvWithStorage) Authenticate(ctx context.Context) error        { return nil }
func (p *fakeProvWithStorage) ValidateCredentials(ctx context.Context) error { return nil }

//nolint:ireturn
func (p *fakeProvWithStorage) Network() cpi.NetworkManager { return p.n }

//nolint:ireturn
func (p *fakeProvWithStorage) Compute() cpi.ComputeManager { return p.c }

//nolint:ireturn
func (p *fakeProvWithStorage) Storage() cpi.StorageManager { return p.s }

//nolint:ireturn
func (p *fakeProvWithStorage) Security() cpi.SecurityManager { return nil }

//nolint:ireturn
func (p *fakeProvWithStorage) LoadBalancer() cpi.LoadBalancerManager { return nil }

//nolint:ireturn
func (p *fakeProvWithStorage) NetworkManager() cpi.NetworkManager { return p.Network() }

//nolint:ireturn
func (p *fakeProvWithStorage) ComputeManager() cpi.ComputeManager { return p.Compute() }

//nolint:ireturn
func (p *fakeProvWithStorage) StorageManager() cpi.StorageManager { return p.Storage() }

//nolint:ireturn
func (p *fakeProvWithStorage) SecurityManager() cpi.SecurityManager { return p.Security() }

//nolint:ireturn
func (p *fakeProvWithStorage) LoadBalancerManager() cpi.LoadBalancerManager { return p.LoadBalancer() }

func (p *fakeProvWithStorage) SupportsStorage() bool                                 { return true }
func (p *fakeProvWithStorage) Initialize(ctx context.Context, cfg interface{}) error { return nil }
func (p *fakeProvWithStorage) Cleanup(ctx context.Context) error                     { return nil }

func createExecuteTestConfig() *config.Config {
	return &config.Config{
		Name:   "prod",
		Region: "eu01",
		Network: config.NetworkConfig{
			NetworkCIDR: "10.4.0.0/20",
		},
		Bastion: config.Bastion{
			Flavor: "c1.2",
			Image:  "Ubuntu 24.04 LTS",
		},
		AZs: map[string]config.AvailabilityZone{
			"eu01-1": {},
		},
		PublicIPs: config.PublicIPsConfig{
			Ops:       1,
			Jumpbox:   0, // Skip for faster tests
			Router:    0,
			CFSSH:     0,
			TCPRouter: 0,
		},
	}
}

// ==============================================================================
// Test: Execute Workflow - Success Path
// ==============================================================================

func TestExecute_Success_STACKIT(t *testing.T) {
	t.Parallel()

	manager, fakeNet, fakeComp, sm := setupExecuteTest(t, "stackit")
	ctx := context.Background()

	err := manager.Execute(ctx)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// Verify network was created
	networks, err := sm.GetResourcesByType("network")
	if err != nil {
		t.Fatalf("Failed to get networks: %v", err)
	}
	if len(networks) == 0 {
		t.Error("Expected network to be created")
	}

	// Verify subnets were created
	subnets, err := sm.GetResourcesByType("subnet")
	if err != nil {
		t.Fatalf("Failed to get subnets: %v", err)
	}
	if len(subnets) == 0 {
		t.Error("Expected subnets to be created")
	}

	// Verify security groups were created
	securityGroups, err := sm.GetResourcesByType("security_group")
	if err != nil {
		t.Fatalf("Failed to get security groups: %v", err)
	}
	if len(securityGroups) == 0 {
		t.Error("Expected security groups to be created")
	}

	// Verify public IPs were created
	publicIPs, err := sm.GetResourcesByType("public_ip")
	if err != nil {
		t.Fatalf("Failed to get public IPs: %v", err)
	}
	// Should have at least ops + bastion IPs
	if len(publicIPs) < 2 {
		t.Errorf("Expected at least 2 public IPs, got %d", len(publicIPs))
	}

	// Verify bastion was created
	bastion, err := sm.GetResource("compute_instance", "prod-bastion")
	if err != nil || bastion == nil {
		t.Error("Expected bastion to be created")
	}

	// Verify fake providers were called
	if len(fakeNet.fakeNet.createdNetworks) == 0 {
		t.Error("Network creation not called")
	}

	// STACKIT uses virtual subnets that are added directly to state
	// without calling CreateSubnet(), so we verify state instead
	// Virtual subnets have IDs prefixed with "virtual:"
	for _, subnet := range subnets {
		if !strings.HasPrefix(subnet.ID, "virtual:") {
			t.Errorf("Expected virtual subnet ID for STACKIT, got %s", subnet.ID)
		}
	}

	if len(fakeNet.fakeNet.createdSecurityGroups) == 0 {
		t.Error("Security group creation not called")
	}

	if len(fakeNet.createdPublicIPs) == 0 {
		t.Error("Public IP creation not called")
	}

	if len(fakeComp.instances) == 0 {
		t.Error("Bastion instance creation not called")
	}
}

func TestExecute_Success_AWS(t *testing.T) {
	t.Parallel()

	manager, _, _, sm := setupExecuteTest(t, "aws")
	ctx := context.Background()

	err := manager.Execute(ctx)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// For AWS, public IPs should only be ops
	publicIPs, err := sm.GetResourcesByType("public_ip")
	if err != nil {
		t.Fatalf("Failed to get public IPs: %v", err)
	}

	// AWS should create ops IP only (no jumpbox/router/etc)
	if len(publicIPs) != 1 {
		t.Errorf("Expected 1 public IP for AWS, got %d", len(publicIPs))
	}

	// Verify bastion was still created
	bastion, err := sm.GetResource("compute_instance", "prod-bastion")
	if err != nil || bastion == nil {
		t.Error("Expected bastion to be created")
	}
}

// ==============================================================================
// Test: Execute Workflow - Failure Paths
// ==============================================================================

func TestExecute_FailsOnNetworkCreation(t *testing.T) {
	t.Parallel()

	manager, fakeNet, _, sm := setupExecuteTest(t, "stackit")
	ctx := context.Background()

	// Configure fake to fail on network creation
	fakeNet.shouldFailNext = "CreateNetwork"

	err := manager.Execute(ctx)
	if err == nil {
		t.Fatal("Expected error when network creation fails")
	}

	// Verify state was saved despite failure
	// Network should NOT be in state
	networks, _ := sm.GetResourcesByType("network")
	if len(networks) > 0 {
		t.Error("Network should not be in state after creation failure")
	}

	// Subsequent steps should not have executed
	subnets, _ := sm.GetResourcesByType("subnet")
	if len(subnets) > 0 {
		t.Error("Subnets should not be created when network creation fails")
	}
}

func TestExecute_FailsOnSubnetCreation(t *testing.T) {
	t.Parallel()

	// Use AWS provider since STACKIT uses virtual subnets that don't call CreateSubnet()
	// AWS creates real subnets, so the error injection will work properly
	manager, fakeNet, _, sm := setupExecuteTest(t, "aws")
	ctx := context.Background()

	// Configure fake to fail on subnet creation
	fakeNet.shouldFailNext = "CreateSubnet"

	err := manager.Execute(ctx)
	if err == nil {
		t.Fatal("Expected error when subnet creation fails")
	}

	// Network should have been created successfully
	networks, _ := sm.GetResourcesByType("network")
	if len(networks) == 0 {
		t.Error("Network should be in state before subnet failure")
	}

	// Subnets should not be in state
	subnets, _ := sm.GetResourcesByType("subnet")
	if len(subnets) > 0 {
		t.Error("Subnets should not be in state after creation failure")
	}

	// Subsequent steps should not have executed
	securityGroups, _ := sm.GetResourcesByType("security_group")
	if len(securityGroups) > 0 {
		t.Error("Security groups should not be created when subnet creation fails")
	}
}

func TestExecute_FailsOnSecurityGroupCreation(t *testing.T) {
	t.Parallel()

	manager, fakeNet, _, sm := setupExecuteTest(t, "stackit")
	ctx := context.Background()

	// Configure fake to fail on security group creation
	fakeNet.shouldFailNext = "CreateSecurityGroup"

	err := manager.Execute(ctx)
	if err == nil {
		t.Fatal("Expected error when security group creation fails")
	}

	// Network and subnets should have been created
	networks, _ := sm.GetResourcesByType("network")
	if len(networks) == 0 {
		t.Error("Network should be in state before security group failure")
	}

	subnets, _ := sm.GetResourcesByType("subnet")
	if len(subnets) == 0 {
		t.Error("Subnets should be in state before security group failure")
	}

	// Security groups should not be in state
	securityGroups, _ := sm.GetResourcesByType("security_group")
	if len(securityGroups) > 0 {
		t.Error("Security groups should not be in state after creation failure")
	}
}

func TestExecute_FailsOnBastionCreation(t *testing.T) {
	t.Parallel()

	manager, _, fakeComp, sm := setupExecuteTest(t, "stackit")
	ctx := context.Background()

	// Configure fake to fail on instance creation
	fakeComp.shouldFailNext = "CreateInstance"

	err := manager.Execute(ctx)
	if err == nil {
		t.Fatal("Expected error when bastion creation fails")
	}

	// All previous steps should have succeeded
	networks, _ := sm.GetResourcesByType("network")
	if len(networks) == 0 {
		t.Error("Network should be in state before bastion failure")
	}

	subnets, _ := sm.GetResourcesByType("subnet")
	if len(subnets) == 0 {
		t.Error("Subnets should be in state before bastion failure")
	}

	securityGroups, _ := sm.GetResourcesByType("security_group")
	if len(securityGroups) == 0 {
		t.Error("Security groups should be in state before bastion failure")
	}

	// Bastion should NOT be in state
	bastion, _ := sm.GetResource("compute_instance", "prod-bastion")
	if bastion != nil {
		t.Error("Bastion should not be in state after creation failure")
	}
}

// ==============================================================================
// Test: Execute Workflow - Idempotency
// ==============================================================================

func TestExecute_Idempotency_SkipsExistingResources(t *testing.T) {
	t.Parallel()

	manager, fakeNet, fakeComp, sm := setupExecuteTest(t, "stackit")
	ctx := context.Background()

	// First execution
	err := manager.Execute(ctx)
	if err != nil {
		t.Fatalf("First Execute failed: %v", err)
	}

	// Record counts after first execution
	firstNetworkCount := len(fakeNet.fakeNet.createdNetworks)
	firstSubnetCount := len(fakeNet.fakeNet.createdSubnets)
	firstSGCount := len(fakeNet.fakeNet.createdSecurityGroups)
	firstIPCount := len(fakeNet.createdPublicIPs)
	firstInstanceCount := len(fakeComp.instances)

	// Second execution (should be idempotent)
	err = manager.Execute(ctx)
	if err != nil {
		t.Fatalf("Second Execute failed: %v", err)
	}

	// Verify no duplicate resources were created
	if len(fakeNet.fakeNet.createdNetworks) != firstNetworkCount {
		t.Errorf("Networks created again: first=%d second=%d", firstNetworkCount, len(fakeNet.fakeNet.createdNetworks))
	}

	if len(fakeNet.fakeNet.createdSubnets) != firstSubnetCount {
		t.Errorf("Subnets created again: first=%d second=%d", firstSubnetCount, len(fakeNet.fakeNet.createdSubnets))
	}

	if len(fakeNet.fakeNet.createdSecurityGroups) != firstSGCount {
		t.Errorf("Security groups created again: first=%d second=%d", firstSGCount, len(fakeNet.fakeNet.createdSecurityGroups))
	}

	if len(fakeNet.createdPublicIPs) > firstIPCount {
		t.Errorf("Public IPs created again: first=%d second=%d", firstIPCount, len(fakeNet.createdPublicIPs))
	}

	if len(fakeComp.instances) != firstInstanceCount {
		t.Errorf("Instances created again: first=%d second=%d", firstInstanceCount, len(fakeComp.instances))
	}

	// Verify state still has correct resources
	networks, _ := sm.GetResourcesByType("network")
	if len(networks) != 1 {
		t.Errorf("Expected 1 network in state, got %d", len(networks))
	}
}

// ==============================================================================
// Test: Execute Workflow - State Persistence
// ==============================================================================

func TestExecute_SavesStateAfterEachStep(t *testing.T) {
	t.Parallel()

	manager, _, _, sm := setupExecuteTest(t, "stackit")
	ctx := context.Background()

	err := manager.Execute(ctx)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// Verify state file was saved
	// State should have resources from all steps
	resources := sm.Current().Resources

	hasNetwork := false
	hasSubnet := false
	hasSG := false
	hasIP := false
	hasInstance := false

	for _, resource := range resources {
		switch resource.Type {
		case "network":
			hasNetwork = true
		case "subnet":
			hasSubnet = true
		case "security_group":
			hasSG = true
		case "public_ip":
			hasIP = true
		case "instance":
			hasInstance = true
		}
	}

	if !hasNetwork {
		t.Error("State should contain network")
	}
	if !hasSubnet {
		t.Error("State should contain subnet")
	}
	if !hasSG {
		t.Error("State should contain security group")
	}
	if !hasIP {
		t.Error("State should contain public IP")
	}
	if !hasInstance {
		t.Error("State should contain instance")
	}
}

// ==============================================================================
// Test: Execute Workflow - Dry Run
// ==============================================================================

func TestExecute_DryRun_DoesNotCreateResources(t *testing.T) {
	t.Parallel()

	manager, fakeNet, fakeComp, sm := setupExecuteTest(t, "stackit")

	// Remove pre-created keypair since we're testing dry-run from scratch
	_ = sm.RemoveResource("keypair", "prod-keypair")

	// Enable dry-run mode with storage manager
	fakeStorage := newFakeStorage()
	manager = bootstrap.NewManager(
		createExecuteTestConfig(),
		&fakeProvWithStorage{n: fakeNet, c: fakeComp, s: fakeStorage},
		sm,
		&bootstrap.Options{
			BlocName: "prod",
			Provider: "stackit",
			Region:   "eu01",
			DryRun:   true, // Enable dry-run
		},
	)

	ctx := context.Background()
	err := manager.Execute(ctx)
	if err != nil {
		t.Fatalf("Dry-run Execute failed: %v", err)
	}

	// Verify no resources were created
	if len(fakeNet.fakeNet.createdNetworks) > 0 {
		t.Error("Dry-run should not create networks")
	}

	if len(fakeNet.fakeNet.createdSubnets) > 0 {
		t.Error("Dry-run should not create subnets")
	}

	if len(fakeNet.fakeNet.createdSecurityGroups) > 0 {
		t.Error("Dry-run should not create security groups")
	}

	if len(fakeNet.createdPublicIPs) > 0 {
		t.Error("Dry-run should not create public IPs")
	}

	if len(fakeComp.instances) > 0 {
		t.Error("Dry-run should not create instances")
	}

	// Verify no resources in state
	resources := sm.Current().Resources
	if len(resources) > 0 {
		t.Errorf("Dry-run should not save resources to state, got %d resources", len(resources))
	}
}

// ==============================================================================
// Test: Base Tags
// ==============================================================================

func TestExecute_ResourcesHaveCorrectTags(t *testing.T) {
	t.Parallel()

	manager, fakeNet, _, sm := setupExecuteTest(t, "stackit")
	ctx := context.Background()

	err := manager.Execute(ctx)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// Check network tags
	if len(fakeNet.fakeNet.createdNetworks) > 0 {
		network := fakeNet.fakeNet.createdNetworks[0]
		verifyBaseTags(t, network.Tags, "prod", "stackit", "eu01")
	}

	// Check subnet tags
	if len(fakeNet.fakeNet.createdSubnets) > 0 {
		subnet := fakeNet.fakeNet.createdSubnets[0]
		verifyBaseTags(t, subnet.Tags, "prod", "stackit", "eu01")
	}

	// Check security group tags
	if len(fakeNet.fakeNet.createdSecurityGroups) > 0 {
		sg := fakeNet.fakeNet.createdSecurityGroups[0]
		verifyBaseTags(t, sg.Tags, "prod", "stackit", "eu01")
	}

	// Verify state resource tags
	networks, _ := sm.GetResourcesByType("network")
	if len(networks) > 0 {
		verifyBaseTags(t, networks[0].Tags, "prod", "stackit", "eu01")
	}
}

func verifyBaseTags(t *testing.T, tags map[string]string, bloc, provider, region string) {
	t.Helper()

	// Verify required metadata tags (using hyphenated names as per metadata.go)
	expectedTags := map[string]string{
		"bloc":       bloc,
		"managed-by": "ocfp",
		"created-by": "ocfp",
	}

	for key, expectedValue := range expectedTags {
		if tags[key] != expectedValue {
			t.Errorf("Tag %s = %v, want %v", key, tags[key], expectedValue)
		}
	}

	// Verify timestamp tags exist
	if _, ok := tags["created-at"]; !ok {
		t.Error("Missing created-at tag")
	}
	if _, ok := tags["updated-at"]; !ok {
		t.Error("Missing updated-at tag")
	}
}

// ==============================================================================
// Test: Selective Resource Filtering
// ==============================================================================

// setupSelectiveTest creates a manager with specific bootstrap options for selective mode testing
func setupSelectiveTest(t *testing.T, opts *bootstrap.Options) (*bootstrap.Manager, *fakeNetEnhanced, *fakeComputeEnhanced, *state.Manager) {
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

	cfg := createExecuteTestConfig()
	fakeNetwork := newFakeNetEnhanced()
	fakeCompute := newFakeComputeEnhanced()
	fakeStorage := newFakeStorage()
	fakeProvider := &fakeProvWithStorage{n: fakeNetwork, c: fakeCompute, s: fakeStorage}

	// Pre-create keypair in state
	err = stateManager.AddResource(&state.Resource{
		ID:   "kp-prod-keypair",
		Type: state.ResourceTypeKeyPair,
		Name: "prod-keypair",
		Properties: map[string]interface{}{
			"public_key":  "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAFAKE prod-keypair",
			"fingerprint": "fake:fingerprint:test",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = stateManager.SetOutput("keypair_name", "prod-keypair")

	manager := bootstrap.NewManager(cfg, fakeProvider, stateManager, opts)

	return manager, fakeNetwork, fakeCompute, stateManager
}

func TestFilterSteps_BastionOnly(t *testing.T) {
	t.Parallel()

	manager, _, fakeComp, sm := setupSelectiveTest(t, &bootstrap.Options{
		BlocName: "prod",
		Provider: "stackit",
		Region:   "eu01",
		Bastion:  true, // Only bastion
		Yes:      true, // Skip confirmation
	})

	ctx := context.Background()
	err := manager.Execute(ctx)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// Verify bastion was created
	bastion, err := sm.GetResource("compute_instance", "prod-bastion")
	if err != nil || bastion == nil {
		t.Errorf("Expected bastion to be created (err: %v)", err)
	}

	// Verify compute manager was called for bastion
	if len(fakeComp.instances) == 0 {
		t.Error("Expected bastion instance to be created")
	}

	// Verify network and security groups were also created (required dependencies)
	networks, _ := sm.GetResourcesByType("network")
	if len(networks) == 0 {
		t.Error("Expected network to be created for bastion")
	}

	securityGroups, _ := sm.GetResourcesByType("security_group")
	if len(securityGroups) == 0 {
		t.Error("Expected security groups to be created for bastion")
	}
}

func TestFilterSteps_NetworkOnly(t *testing.T) {
	t.Parallel()

	manager, fakeNet, fakeComp, sm := setupSelectiveTest(t, &bootstrap.Options{
		BlocName: "prod",
		Provider: "stackit",
		Region:   "eu01",
		Network:  true, // Only network resources
		Yes:      true, // Skip confirmation
	})

	ctx := context.Background()
	err := manager.Execute(ctx)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// Verify network resources were created
	networks, _ := sm.GetResourcesByType("network")
	if len(networks) == 0 {
		t.Error("Expected network to be created")
	}

	subnets, _ := sm.GetResourcesByType("subnet")
	if len(subnets) == 0 {
		t.Error("Expected subnets to be created")
	}

	publicIPs, _ := sm.GetResourcesByType("public_ip")
	if len(publicIPs) == 0 {
		t.Error("Expected public IPs to be created")
	}

	// Verify bastion was NOT created
	bastion, _ := sm.GetResource("compute_instance", "prod-bastion")
	if bastion != nil {
		t.Error("Expected bastion NOT to be created with network-only flag")
	}

	// Verify network manager was called
	if len(fakeNet.fakeNet.createdNetworks) == 0 {
		t.Error("Expected network creation to be called")
	}

	// Verify compute manager was NOT called
	if len(fakeComp.instances) > 0 {
		t.Error("Expected compute instances NOT to be created with network-only flag")
	}
}

func TestFilterSteps_ServersOnly(t *testing.T) {
	t.Parallel()

	manager, _, fakeComp, sm := setupSelectiveTest(t, &bootstrap.Options{
		BlocName: "prod",
		Provider: "stackit",
		Region:   "eu01",
		Servers:  true, // Only servers (bastion)
		Yes:      true, // Skip confirmation
	})

	ctx := context.Background()
	err := manager.Execute(ctx)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// Verify bastion was created
	bastion, err := sm.GetResource("compute_instance", "prod-bastion")
	if err != nil || bastion == nil {
		t.Errorf("Expected bastion to be created (err: %v)", err)
	}

	// Verify compute manager was called
	if len(fakeComp.instances) == 0 {
		t.Error("Expected bastion instance to be created")
	}

	// Verify required dependencies were created (network, security groups)
	networks, _ := sm.GetResourcesByType("network")
	if len(networks) == 0 {
		t.Error("Expected network to be created as dependency for servers")
	}

	securityGroups, _ := sm.GetResourcesByType("security_group")
	if len(securityGroups) == 0 {
		t.Error("Expected security groups to be created as dependency for servers")
	}

	// Verify volumes and buckets were NOT created
	volumes, _ := sm.GetResourcesByType("volume")
	if len(volumes) > 0 {
		t.Error("Expected volumes NOT to be created with servers-only flag")
	}

	buckets, _ := sm.GetResourcesByType("object_storage_bucket")
	if len(buckets) > 0 {
		t.Error("Expected buckets NOT to be created with servers-only flag")
	}
}

func TestFilterSteps_VolumesOnly(t *testing.T) {
	t.Parallel()

	manager, _, fakeComp, sm := setupSelectiveTest(t, &bootstrap.Options{
		BlocName: "prod",
		Provider: "stackit",
		Region:   "eu01",
		Volumes:  true, // Only volumes
		Yes:      true, // Skip confirmation
	})

	ctx := context.Background()
	err := manager.Execute(ctx)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// TODO: Volume creation is now disabled (volumes never attached to bastion)
	// Verify volumes were NOT created (volumes disabled)
	volumes, _ := sm.GetResourcesByType("volume")
	if len(volumes) > 0 {
		t.Error("Expected volumes NOT to be created (volume creation is now disabled)")
	}

	// Verify bastion was NOT created
	bastion, _ := sm.GetResource("compute_instance", "prod-bastion")
	if bastion != nil {
		t.Error("Expected bastion NOT to be created with volumes-only flag")
	}

	// Verify compute manager was NOT called
	if len(fakeComp.instances) > 0 {
		t.Error("Expected compute instances NOT to be created with volumes-only flag")
	}
}

func TestFilterSteps_BucketsOnly(t *testing.T) {
	t.Parallel()

	manager, _, fakeComp, sm := setupSelectiveTest(t, &bootstrap.Options{
		BlocName: "prod",
		Provider: "stackit",
		Region:   "eu01",
		Buckets:  true, // Only buckets
		Yes:      true, // Skip confirmation
	})

	ctx := context.Background()
	err := manager.Execute(ctx)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// Verify buckets were created
	buckets, _ := sm.GetResourcesByType("object_storage_bucket")
	if len(buckets) == 0 {
		t.Error("Expected buckets to be created")
	}

	// Verify bastion was NOT created
	bastion, _ := sm.GetResource("compute_instance", "prod-bastion")
	if bastion != nil {
		t.Error("Expected bastion NOT to be created with buckets-only flag")
	}

	// Verify compute manager was NOT called
	if len(fakeComp.instances) > 0 {
		t.Error("Expected compute instances NOT to be created with buckets-only flag")
	}
}

func TestFilterSteps_SecurityGroupsOnly(t *testing.T) {
	t.Parallel()

	manager, fakeNet, fakeComp, sm := setupSelectiveTest(t, &bootstrap.Options{
		BlocName:       "prod",
		Provider:       "stackit",
		Region:         "eu01",
		SecurityGroups: true, // Only security groups
		Yes:            true, // Skip confirmation
	})

	// Pre-create network in state (security groups require existing network)
	err := sm.AddResource(&state.Resource{
		ID:   "net-test-123",
		Type: "network",
		Name: "prod-net",
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = sm.SetOutput("network_id", "net-test-123")

	ctx := context.Background()
	err = manager.Execute(ctx)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// Verify security groups were created
	securityGroups, _ := sm.GetResourcesByType("security_group")
	if len(securityGroups) == 0 {
		t.Error("Expected security groups to be created")
	}

	// Verify network manager was called for security groups
	if len(fakeNet.fakeNet.createdSecurityGroups) == 0 {
		t.Error("Expected security group creation to be called")
	}

	// Verify bastion was NOT created
	bastion, _ := sm.GetResource("compute_instance", "prod-bastion")
	if bastion != nil {
		t.Error("Expected bastion NOT to be created with security-groups-only flag")
	}

	// Verify compute manager was NOT called
	if len(fakeComp.instances) > 0 {
		t.Error("Expected compute instances NOT to be created with security-groups-only flag")
	}
}

func TestFilterSteps_MultipleFlags_NetworkAndServers(t *testing.T) {
	t.Parallel()

	manager, fakeNet, fakeComp, sm := setupSelectiveTest(t, &bootstrap.Options{
		BlocName: "prod",
		Provider: "stackit",
		Region:   "eu01",
		Network:  true, // Network resources
		Servers:  true, // Server resources
		Yes:      true, // Skip confirmation
	})

	ctx := context.Background()
	err := manager.Execute(ctx)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// Verify network resources were created
	networks, _ := sm.GetResourcesByType("network")
	if len(networks) == 0 {
		t.Error("Expected network to be created")
	}

	subnets, _ := sm.GetResourcesByType("subnet")
	if len(subnets) == 0 {
		t.Error("Expected subnets to be created")
	}

	publicIPs, _ := sm.GetResourcesByType("public_ip")
	if len(publicIPs) == 0 {
		t.Error("Expected public IPs to be created")
	}

	// Verify bastion was created
	bastion, err := sm.GetResource("compute_instance", "prod-bastion")
	if err != nil || bastion == nil {
		t.Error("Expected bastion to be created")
	}

	// Verify both managers were called
	if len(fakeNet.fakeNet.createdNetworks) == 0 {
		t.Error("Expected network creation to be called")
	}

	if len(fakeComp.instances) == 0 {
		t.Error("Expected compute instance creation to be called")
	}

	// Verify volumes and buckets were NOT created
	volumes, _ := sm.GetResourcesByType("volume")
	if len(volumes) > 0 {
		t.Error("Expected volumes NOT to be created with network+servers flags only")
	}

	buckets, _ := sm.GetResourcesByType("object_storage_bucket")
	if len(buckets) > 0 {
		t.Error("Expected buckets NOT to be created with network+servers flags only")
	}
}

func TestFilterSteps_AllFlag(t *testing.T) {
	t.Parallel()

	manager, fakeNet, fakeComp, sm := setupSelectiveTest(t, &bootstrap.Options{
		BlocName: "prod",
		Provider: "stackit",
		Region:   "eu01",
		All:      true, // All resources
		Yes:      true, // Skip confirmation
	})

	ctx := context.Background()
	err := manager.Execute(ctx)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// Verify all resource types were created
	networks, _ := sm.GetResourcesByType("network")
	if len(networks) == 0 {
		t.Error("Expected network to be created with --all flag")
	}

	subnets, _ := sm.GetResourcesByType("subnet")
	if len(subnets) == 0 {
		t.Error("Expected subnets to be created with --all flag")
	}

	securityGroups, _ := sm.GetResourcesByType("security_group")
	if len(securityGroups) == 0 {
		t.Error("Expected security groups to be created with --all flag")
	}

	publicIPs, _ := sm.GetResourcesByType("public_ip")
	if len(publicIPs) == 0 {
		t.Error("Expected public IPs to be created with --all flag")
	}

	bastion, err := sm.GetResource("compute_instance", "prod-bastion")
	if err != nil || bastion == nil {
		t.Error("Expected bastion to be created with --all flag")
	}

	buckets, _ := sm.GetResourcesByType("object_storage_bucket")
	if len(buckets) == 0 {
		t.Error("Expected buckets to be created with --all flag")
	}

	// Verify providers were called
	if len(fakeNet.fakeNet.createdNetworks) == 0 {
		t.Error("Expected network creation with --all flag")
	}

	if len(fakeComp.instances) == 0 {
		t.Error("Expected compute instance creation with --all flag")
	}
}
