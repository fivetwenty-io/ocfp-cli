package bootstrap_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/bootstrap"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

// ==============================================================================
// Helper Functions for PublicIP Test Setup
// ==============================================================================

func setupPublicIPTest(t *testing.T, provider string) (*bootstrap.Manager, *fakeNetEnhanced, *state.Manager) {
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

	cfg := &config.Config{
		Name:   "prod",
		Region: "eu01",
		Network: config.NetworkConfig{
			NetworkCIDR: "10.4.0.0/20",
		},
		PublicIPs: config.PublicIPsConfig{
			Ops:       1,
			Jumpbox:   2,
			Router:    4,
			CFSSH:     1,
			TCPRouter: 2,
		},
	}

	fakeNetwork := newFakeNetEnhanced()
	fakeCompute := newFakeComputeEnhanced()
	fakeProvider := &fakeProv{n: fakeNetwork, c: fakeCompute}

	manager := bootstrap.NewManager(cfg, fakeProvider, stateManager, &bootstrap.Options{
		BlocName: "prod",
		Provider: provider,
		Region:   "eu01",
	})

	return manager, fakeNetwork, stateManager
}

// fakeNetEnhanced is an enhanced fake network manager that supports public IPs.
type fakeNetEnhanced struct {
	*fakeNet
	createdPublicIPs []*cpi.PublicIP
	publicIPCounter  int
	shouldFailNext   string // For error simulation
}

func newFakeNetEnhanced() *fakeNetEnhanced {
	return &fakeNetEnhanced{
		fakeNet: &fakeNet{
			createdNetworks:       make([]*cpi.Network, 0),
			createdSubnets:        make([]*cpi.Subnet, 0),
			createdSecurityGroups: make([]*cpi.SecurityGroup, 0),
		},
		createdPublicIPs: make([]*cpi.PublicIP, 0),
		publicIPCounter:  0,
	}
}

func (f *fakeNetEnhanced) CreatePublicIP(ctx context.Context, req *cpi.PublicIPRequest) (*cpi.PublicIP, error) {
	f.publicIPCounter++
	ipAddress := "203.0.113." + string(rune(f.publicIPCounter))
	ip := &cpi.PublicIP{
		ID:        "ip-" + req.Name,
		Name:      req.Name,
		IPAddress: ipAddress, // Set IPAddress for bootstrap code compatibility
		Address:   ipAddress, // Set Address for other code compatibility
		Labels:    req.Labels,
		Tags:      req.Tags,
	}

	f.createdPublicIPs = append(f.createdPublicIPs, ip)

	return ip, nil
}

func (f *fakeNetEnhanced) ListPublicIPs(ctx context.Context) ([]*cpi.PublicIP, error) {
	return f.createdPublicIPs, nil
}

// EnsureFloatingIP implements the STACKIT-specific interface for floating IP management.
func (f *fakeNetEnhanced) EnsureFloatingIP(ctx context.Context, req *cpi.PublicIPRequest) (*cpi.PublicIP, error) {
	// First check if it already exists
	for _, ip := range f.createdPublicIPs {
		if ip.Name == req.Name {
			return ip, nil
		}
	}

	// Create new floating IP
	return f.CreatePublicIP(ctx, req)
}

// Override CreateNetwork to support error simulation.
func (f *fakeNetEnhanced) CreateNetwork(ctx context.Context, req *cpi.NetworkRequest) (*cpi.Network, error) {
	if f.shouldFailNext == "CreateNetwork" {
		f.shouldFailNext = ""
		return nil, fmt.Errorf("fake CreateNetwork error")
	}
	return f.fakeNet.CreateNetwork(ctx, req)
}

// Override CreateSubnet to support error simulation.
func (f *fakeNetEnhanced) CreateSubnet(ctx context.Context, req *cpi.SubnetRequest) (*cpi.Subnet, error) {
	if f.shouldFailNext == "CreateSubnet" {
		f.shouldFailNext = ""
		return nil, fmt.Errorf("fake CreateSubnet error")
	}
	return f.fakeNet.CreateSubnet(ctx, req)
}

// Override CreateSecurityGroup to support error simulation.
func (f *fakeNetEnhanced) CreateSecurityGroup(ctx context.Context, req *cpi.CreateSecurityGroupRequest) (*cpi.SecurityGroup, error) {
	if f.shouldFailNext == "CreateSecurityGroup" {
		f.shouldFailNext = ""
		return nil, fmt.Errorf("fake CreateSecurityGroup error")
	}
	return f.fakeNet.CreateSecurityGroup(ctx, req)
}

// ==============================================================================
// Test: CreatePublicIPs Workflow for Non-STACKIT Provider
// ==============================================================================

func TestCreatePublicIPs_NonStackitProvider(t *testing.T) {
	t.Parallel()

	manager, fakeNet, sm := setupPublicIPTest(t, "aws")
	ctx := context.Background()

	err := manager.CreatePublicIPs(ctx)
	if err != nil {
		t.Fatalf("CreatePublicIPs failed: %v", err)
	}

	// For non-STACKIT providers, only ops public IPs are created
	if len(fakeNet.createdPublicIPs) != 1 {
		t.Errorf("Created %d public IPs for non-STACKIT, want 1 (ops only)", len(fakeNet.createdPublicIPs))
	}

	// Verify ops public IP was created
	opsFound := false
	for _, ip := range fakeNet.createdPublicIPs {
		if ip.Labels["job"] == "ops" {
			opsFound = true
			break
		}
	}

	if !opsFound {
		t.Error("Ops public IP not found for non-STACKIT provider")
	}

	// Verify state was saved
	resource, err := sm.GetResource("public_ip", "prod-ops-0")
	if err != nil || resource == nil {
		t.Error("Ops public IP not saved to state")
	}
}

func TestCreatePublicIPs_StackitProvider(t *testing.T) {
	t.Parallel()

	manager, fakeNet, sm := setupPublicIPTest(t, "stackit")
	ctx := context.Background()

	err := manager.CreatePublicIPs(ctx)
	if err != nil {
		t.Fatalf("CreatePublicIPs failed: %v", err)
	}

	// For STACKIT providers, all IP types should be created
	// Expected: 1 ops + 2 jumpbox + 4 router + 1 cf-ssh + 2 tcp-router + 1 bastion = 11 total
	expectedCount := 11
	if len(fakeNet.createdPublicIPs) != expectedCount {
		t.Errorf("Created %d public IPs for STACKIT, want %d", len(fakeNet.createdPublicIPs), expectedCount)
	}

	// Verify each type of public IP
	expectedJobs := map[string]int{
		"ops":        1,
		"jumpbox":    2,
		"router":     4,
		"cf-ssh":     1,
		"tcp-router": 2,
		"bastion":    1,
	}

	jobCounts := make(map[string]int)
	for _, ip := range fakeNet.createdPublicIPs {
		job := ip.Labels["job"]
		jobCounts[job]++
	}

	for job, expectedCount := range expectedJobs {
		if jobCounts[job] != expectedCount {
			t.Errorf("Job %s: created %d IPs, want %d", job, jobCounts[job], expectedCount)
		}
	}

	// Verify some IPs were saved to state
	resource, err := sm.GetResource("public_ip", "prod-ops-0")
	if err != nil || resource == nil {
		t.Error("Ops public IP not saved to state")
	}

	resource, err = sm.GetResource("public_ip", "prod-bastion")
	if err != nil || resource == nil {
		t.Error("Bastion public IP not saved to state")
	}
}

// ==============================================================================
// Test: Public IP Count Configuration
// ==============================================================================

func TestCreatePublicIPs_CustomCounts(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	stateManager, err := state.NewManager(filepath.Join(tmp, ".state"))
	if err != nil {
		t.Fatal(err)
	}

	_, err = stateManager.Load("prod")
	if err != nil {
		t.Fatal(err)
	}

	// Custom configuration with different counts
	cfg := &config.Config{
		Name:   "prod",
		Region: "eu01",
		PublicIPs: config.PublicIPsConfig{
			Ops:       3, // Custom: 3 instead of default 1
			Jumpbox:   5, // Custom: 5 instead of default 2
			Router:    6, // Custom: 6 instead of default 4
			CFSSH:     2, // Custom: 2 instead of default 1
			TCPRouter: 4, // Custom: 4 instead of default 2
		},
	}

	fakeNetwork := newFakeNetEnhanced()
	fakeCompute := newFakeComputeEnhanced()
	fakeProvider := &fakeProv{n: fakeNetwork, c: fakeCompute}

	manager := bootstrap.NewManager(cfg, fakeProvider, stateManager, &bootstrap.Options{
		BlocName: "prod",
		Provider: "stackit",
		Region:   "eu01",
	})

	ctx := context.Background()
	err = manager.CreatePublicIPs(ctx)
	if err != nil {
		t.Fatalf("CreatePublicIPs failed: %v", err)
	}

	// Expected: 3 ops + 5 jumpbox + 6 router + 2 cf-ssh + 4 tcp-router + 1 bastion = 21 total
	expectedCount := 21
	if len(fakeNetwork.createdPublicIPs) != expectedCount {
		t.Errorf("Created %d public IPs with custom counts, want %d", len(fakeNetwork.createdPublicIPs), expectedCount)
	}

	// Verify job counts
	jobCounts := make(map[string]int)
	for _, ip := range fakeNetwork.createdPublicIPs {
		job := ip.Labels["job"]
		jobCounts[job]++
	}

	expectedJobs := map[string]int{
		"ops":        3,
		"jumpbox":    5,
		"router":     6,
		"cf-ssh":     2,
		"tcp-router": 4,
		"bastion":    1, // Always 1
	}

	for job, expectedCount := range expectedJobs {
		if jobCounts[job] != expectedCount {
			t.Errorf("Job %s: created %d IPs, want %d", job, jobCounts[job], expectedCount)
		}
	}
}

// ==============================================================================
// Test: Public IP Labels and Tags
// ==============================================================================

func TestCreatePublicIPs_LabelsAndTags(t *testing.T) {
	t.Parallel()

	manager, fakeNet, _ := setupPublicIPTest(t, "stackit")
	ctx := context.Background()

	err := manager.CreatePublicIPs(ctx)
	if err != nil {
		t.Fatalf("CreatePublicIPs failed: %v", err)
	}

	// Verify all IPs have correct labels and tags
	for _, ip := range fakeNet.createdPublicIPs {
		// Verify job label exists
		if ip.Labels["job"] == "" {
			t.Errorf("Public IP %s missing job label", ip.Name)
		}

		// Verify index label exists
		if ip.Labels["index"] == "" {
			t.Errorf("Public IP %s missing index label", ip.Name)
		}

		// Verify base metadata tags (using hyphenated names as per metadata.go)
		if ip.Tags == nil {
			t.Errorf("Public IP %s has no tags", ip.Name)
			continue
		}

		if ip.Tags["managed-by"] != "ocfp" {
			t.Errorf("Public IP %s missing or wrong managed-by tag: got %v, want ocfp", ip.Name, ip.Tags["managed-by"])
		}

		if ip.Tags["bloc"] != "prod" {
			t.Errorf("Public IP %s has wrong bloc tag: got %v, want prod", ip.Name, ip.Tags["bloc"])
		}

		if ip.Tags["created-by"] != "ocfp" {
			t.Errorf("Public IP %s missing or wrong created-by tag: got %v, want ocfp", ip.Name, ip.Tags["created-by"])
		}

		// Verify timestamp tags exist
		if _, ok := ip.Tags["created-at"]; !ok {
			t.Errorf("Public IP %s missing created-at tag", ip.Name)
		}

		if _, ok := ip.Tags["updated-at"]; !ok {
			t.Errorf("Public IP %s missing updated-at tag", ip.Name)
		}
	}
}

// ==============================================================================
// Test: Public IP Naming Convention
// ==============================================================================

func TestCreatePublicIPs_NamingConvention(t *testing.T) {
	t.Parallel()

	manager, fakeNet, _ := setupPublicIPTest(t, "stackit")
	ctx := context.Background()

	err := manager.CreatePublicIPs(ctx)
	if err != nil {
		t.Fatalf("CreatePublicIPs failed: %v", err)
	}

	// Verify naming patterns
	expectedPatterns := map[string]string{
		"ops":        "prod-ops-",
		"jumpbox":    "prod-jumpbox-",
		"router":     "prod-router-",
		"cf-ssh":     "prod-cf-ssh-",
		"tcp-router": "prod-tcp-router-",
		"bastion":    "prod-bastion", // No index for bastion
	}

	for _, ip := range fakeNet.createdPublicIPs {
		job := ip.Labels["job"]
		expectedPrefix := expectedPatterns[job]

		if job == "bastion" {
			if ip.Name != expectedPrefix {
				t.Errorf("Bastion IP name = %v, want %v", ip.Name, expectedPrefix)
			}
		} else {
			if len(ip.Name) <= len(expectedPrefix) || ip.Name[:len(expectedPrefix)] != expectedPrefix {
				t.Errorf("IP %s doesn't match pattern %s*", ip.Name, expectedPrefix)
			}
		}
	}
}

// ==============================================================================
// Test: Public IP State Persistence
// ==============================================================================

func TestCreatePublicIPs_StatePersistence(t *testing.T) {
	t.Parallel()

	manager, _, sm := setupPublicIPTest(t, "stackit")
	ctx := context.Background()

	err := manager.CreatePublicIPs(ctx)
	if err != nil {
		t.Fatalf("CreatePublicIPs failed: %v", err)
	}

	// Verify resources were saved to state
	resources, err := sm.GetResourcesByType("public_ip")
	if err != nil {
		t.Fatalf("Failed to get public IP resources: %v", err)
	}

	if len(resources) == 0 {
		t.Error("No public IP resources found in state")
	}

	// Verify each resource has required properties
	for _, resource := range resources {
		if resource.ID == "" {
			t.Errorf("Resource %s has empty ID", resource.Name)
		}

		if resource.Type != "public_ip" {
			t.Errorf("Resource %s has wrong type: %v", resource.Name, resource.Type)
		}

		if _, ok := resource.Properties["ip_address"]; !ok {
			t.Errorf("Resource %s missing ip_address property", resource.Name)
		}
	}

	// Verify outputs were set
	_, err = sm.GetOutput("ops_public_ip_0")
	if err != nil {
		t.Error("ops_public_ip_0 output not set")
	}

	_, err = sm.GetOutput("ops_public_ip_0_id")
	if err != nil {
		t.Error("ops_public_ip_0_id output not set")
	}
}

// ==============================================================================
// Test: Provider Without Public IP Support
// ==============================================================================

func TestCreatePublicIPs_ProviderWithoutSupport(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	stateManager, err := state.NewManager(filepath.Join(tmp, ".state"))
	if err != nil {
		t.Fatal(err)
	}

	_, err = stateManager.Load("prod")
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Name:   "prod",
		Region: "eu01",
		PublicIPs: config.PublicIPsConfig{
			Ops: 1,
		},
	}

	// Provider with no NetworkManager (doesn't support public IPs)
	fakeProvider := &fakeProv{n: nil, c: newFakeComputeEnhanced()}

	manager := bootstrap.NewManager(cfg, fakeProvider, stateManager, &bootstrap.Options{
		BlocName: "prod",
		Provider: "custom",
		Region:   "eu01",
	})

	ctx := context.Background()
	err = manager.CreatePublicIPs(ctx)

	// Should succeed but skip creation
	if err != nil {
		t.Fatalf("CreatePublicIPs should succeed for providers without support: %v", err)
	}

	// Verify no resources were created
	resources, err := stateManager.GetResourcesByType("public_ip")
	if err != nil {
		t.Fatalf("Failed to get resources: %v", err)
	}

	if len(resources) != 0 {
		t.Errorf("Expected no public IPs for unsupported provider, got %d", len(resources))
	}
}

// ==============================================================================
// Test: Idempotency - Already Existing Public IPs
// ==============================================================================

func TestCreatePublicIPs_Idempotency(t *testing.T) {
	t.Parallel()

	manager, fakeNet, sm := setupPublicIPTest(t, "stackit")
	ctx := context.Background()

	// First creation
	err := manager.CreatePublicIPs(ctx)
	if err != nil {
		t.Fatalf("First CreatePublicIPs failed: %v", err)
	}

	firstCount := len(fakeNet.createdPublicIPs)

	// Second creation (should be idempotent)
	manager2, fakeNet2, _ := setupPublicIPTest(t, "stackit")

	// Pre-populate fakeNet2 with the IPs from first run
	fakeNet2.createdPublicIPs = append(fakeNet2.createdPublicIPs, fakeNet.createdPublicIPs...)

	// Also populate state
	for _, ip := range fakeNet.createdPublicIPs {
		err := sm.AddResource(&state.Resource{
			ID:         ip.ID,
			Type:       "public_ip",
			Name:       ip.Name,
			Properties: map[string]interface{}{"ip_address": ip.IPAddress},
		})
		if err != nil {
			t.Fatalf("Failed to add resource to state: %v", err)
		}
	}

	err = manager2.CreatePublicIPs(ctx)
	if err != nil {
		t.Fatalf("Second CreatePublicIPs failed: %v", err)
	}

	secondCount := len(fakeNet2.createdPublicIPs)

	// Should not create duplicates
	if secondCount != firstCount {
		t.Errorf("Second run created %d IPs, expected %d (idempotent)", secondCount, firstCount)
	}
}
