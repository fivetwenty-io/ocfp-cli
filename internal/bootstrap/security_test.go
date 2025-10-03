package bootstrap_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/bootstrap"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

// ==============================================================================
// Helper Functions for Security Test Setup
// ==============================================================================

func setupSecurityTest(t *testing.T) (*bootstrap.Manager, *fakeNet, *state.Manager) {
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
	}

	fakeNetwork := &fakeNet{
		createdSecurityGroups: make([]*cpi.SecurityGroup, 0),
	}

	fakeCompute := newFakeComputeEnhanced()
	fakeProvider := &fakeProv{n: fakeNetwork, c: fakeCompute}

	manager := bootstrap.NewManager(cfg, fakeProvider, stateManager, &bootstrap.Options{
		BlocName: "prod",
		Provider: "stackit",
		Region:   "eu01",
	})

	// Setup prerequisite: network_id must exist
	_ = stateManager.SetOutput("network_id", "net-test-123")

	return manager, fakeNetwork, stateManager
}

// ==============================================================================
// Test: CreateSecurityGroups Workflow
// ==============================================================================

func TestCreateSecurityGroups_Success(t *testing.T) {
	t.Parallel()

	manager, fakeNet, sm := setupSecurityTest(t)
	ctx := context.Background()

	err := manager.CreateSecurityGroups(ctx)
	if err != nil {
		t.Fatalf("CreateSecurityGroups failed: %v", err)
	}

	// Verify all 7 security groups were created
	if len(fakeNet.createdSecurityGroups) != 7 {
		t.Errorf("Created %d security groups, want 7", len(fakeNet.createdSecurityGroups))
	}

	expectedGroups := []string{
		"prod-bastion",
		"prod-infra",
		"prod-ocfp",
		"prod-lb-ext",
		"prod-ocf-cf-router-ingress",
		"prod-ocf-cf-tcp-router-ingress",
		"prod-ocf-cf-ssh-ingress",
	}

	// Verify each expected group exists
	for _, expectedName := range expectedGroups {
		found := false
		for _, sg := range fakeNet.createdSecurityGroups {
			if sg.Name == expectedName {
				found = true
				break
			}
		}

		if !found {
			t.Errorf("Expected security group %s not found", expectedName)
		}
	}

	// Verify security groups were saved to state
	for _, expectedName := range expectedGroups {
		sg, err := sm.GetResource("security_group", expectedName)
		if err != nil || sg == nil {
			t.Errorf("Security group %s not found in state", expectedName)
		}
	}

	// Verify outputs were set
	_, err = sm.GetOutput("sg_bastion_id")
	if err != nil {
		t.Error("sg_bastion_id output not set")
	}
}

func TestCreateSecurityGroups_SkipsExisting(t *testing.T) {
	t.Parallel()

	manager, fakeNet, sm := setupSecurityTest(t)
	ctx := context.Background()

	// Pre-create one security group in both state AND fake cloud
	existingSG := &cpi.SecurityGroup{
		ID:          "existing-sg-id",
		Name:        "prod-bastion",
		Description: "Bastion security group",
		NetworkID:   "net-test-123",
		Rules:       []*cpi.SecurityRule{},
	}
	fakeNet.existingSecurityGroups = append(fakeNet.existingSecurityGroups, existingSG)

	err := sm.AddResource(&state.Resource{
		ID:   "existing-sg-id",
		Type: "security_group",
		Name: "prod-bastion",
	})
	if err != nil {
		t.Fatalf("Failed to add existing security group: %v", err)
	}

	err = manager.CreateSecurityGroups(ctx)
	if err != nil {
		t.Fatalf("CreateSecurityGroups failed: %v", err)
	}

	// Verify only 6 new security groups were created (bastion skipped)
	if len(fakeNet.createdSecurityGroups) != 6 {
		t.Errorf("Created %d security groups, want 6 (bastion should be skipped)", len(fakeNet.createdSecurityGroups))
	}

	// Verify bastion was not re-created
	for _, sg := range fakeNet.createdSecurityGroups {
		if sg.Name == "prod-bastion" {
			t.Error("Bastion security group should not be recreated")
		}
	}
}

func TestCreateSecurityGroups_NetworkIDNotFound(t *testing.T) {
	t.Parallel()

	manager, _, sm := setupSecurityTest(t)
	ctx := context.Background()

	// Remove network_id to trigger error
	sm.Current().Outputs = make(map[string]interface{})

	err := manager.CreateSecurityGroups(ctx)
	if err == nil {
		t.Fatal("Expected error when network_id not found")
	}
}

// ==============================================================================
// Test: Security Group Definitions
// ==============================================================================

func TestBastionSecurityGroupDef(t *testing.T) {
	t.Parallel()

	manager, _, _ := setupSecurityTest(t)
	ctx := context.Background()

	err := manager.CreateSecurityGroups(ctx)
	if err != nil {
		t.Fatalf("CreateSecurityGroups failed: %v", err)
	}

	// Find bastion security group
	var bastionSG *cpi.SecurityGroup

	// We need to verify through the created groups
	// Since we can't access private methods directly, we test through CreateSecurityGroups
	sg, err := manager.StateManager().GetResource("security_group", "prod-bastion")
	if err != nil {
		t.Fatal("Bastion security group not found")
	}

	if sg.ID != "sg-prod-bastion" {
		t.Errorf("Bastion SG ID = %v, want sg-prod-bastion", sg.ID)
	}

	// Verify bastion SG was created (indirectly through state)
	bastionIDOutput, err := manager.StateManager().GetOutput("sg_bastion_id")
	if err != nil {
		t.Fatal("sg_bastion_id output not found")
	}

	if bastionIDOutput != "sg-prod-bastion" {
		t.Errorf("sg_bastion_id = %v, want sg-prod-bastion", bastionIDOutput)
	}

	_ = bastionSG // Keep for potential future use
}

func TestInfraSecurityGroupDef(t *testing.T) {
	t.Parallel()

	manager, _, _ := setupSecurityTest(t)
	ctx := context.Background()

	err := manager.CreateSecurityGroups(ctx)
	if err != nil {
		t.Fatalf("CreateSecurityGroups failed: %v", err)
	}

	// Verify infra security group exists
	sg, err := manager.StateManager().GetResource("security_group", "prod-infra")
	if err != nil {
		t.Fatal("Infra security group not found")
	}

	if sg.ID != "sg-prod-infra" {
		t.Errorf("Infra SG ID = %v, want sg-prod-infra", sg.ID)
	}
}

func TestOcfpSecurityGroupDef(t *testing.T) {
	t.Parallel()

	manager, _, _ := setupSecurityTest(t)
	ctx := context.Background()

	err := manager.CreateSecurityGroups(ctx)
	if err != nil {
		t.Fatalf("CreateSecurityGroups failed: %v", err)
	}

	// Verify ocfp security group exists
	sg, err := manager.StateManager().GetResource("security_group", "prod-ocfp")
	if err != nil {
		t.Fatal("OCFP security group not found")
	}

	if sg.ID != "sg-prod-ocfp" {
		t.Errorf("OCFP SG ID = %v, want sg-prod-ocfp", sg.ID)
	}
}

func TestLbExtSecurityGroupDef(t *testing.T) {
	t.Parallel()

	manager, _, _ := setupSecurityTest(t)
	ctx := context.Background()

	err := manager.CreateSecurityGroups(ctx)
	if err != nil {
		t.Fatalf("CreateSecurityGroups failed: %v", err)
	}

	// Verify lb-ext security group exists
	sg, err := manager.StateManager().GetResource("security_group", "prod-lb-ext")
	if err != nil {
		t.Fatal("LB-Ext security group not found")
	}

	if sg.ID != "sg-prod-lb-ext" {
		t.Errorf("LB-Ext SG ID = %v, want sg-prod-lb-ext", sg.ID)
	}
}

func TestCFRouterSecurityGroupDef(t *testing.T) {
	t.Parallel()

	manager, _, _ := setupSecurityTest(t)
	ctx := context.Background()

	err := manager.CreateSecurityGroups(ctx)
	if err != nil {
		t.Fatalf("CreateSecurityGroups failed: %v", err)
	}

	// Verify cf-router security group exists
	sg, err := manager.StateManager().GetResource("security_group", "prod-ocf-cf-router-ingress")
	if err != nil {
		t.Fatal("CF Router security group not found")
	}

	if sg.ID != "sg-prod-ocf-cf-router-ingress" {
		t.Errorf("CF Router SG ID = %v, want sg-prod-ocf-cf-router-ingress", sg.ID)
	}
}

func TestCFTCPRouterSecurityGroupDef(t *testing.T) {
	t.Parallel()

	manager, _, _ := setupSecurityTest(t)
	ctx := context.Background()

	err := manager.CreateSecurityGroups(ctx)
	if err != nil {
		t.Fatalf("CreateSecurityGroups failed: %v", err)
	}

	// Verify cf-tcp-router security group exists
	sg, err := manager.StateManager().GetResource("security_group", "prod-ocf-cf-tcp-router-ingress")
	if err != nil {
		t.Fatal("CF TCP Router security group not found")
	}

	if sg.ID != "sg-prod-ocf-cf-tcp-router-ingress" {
		t.Errorf("CF TCP Router SG ID = %v, want sg-prod-ocf-cf-tcp-router-ingress", sg.ID)
	}
}

func TestCFSSHSecurityGroupDef(t *testing.T) {
	t.Parallel()

	manager, _, _ := setupSecurityTest(t)
	ctx := context.Background()

	err := manager.CreateSecurityGroups(ctx)
	if err != nil {
		t.Fatalf("CreateSecurityGroups failed: %v", err)
	}

	// Verify cf-ssh security group exists
	sg, err := manager.StateManager().GetResource("security_group", "prod-ocf-cf-ssh-ingress")
	if err != nil {
		t.Fatal("CF SSH security group not found")
	}

	if sg.ID != "sg-prod-ocf-cf-ssh-ingress" {
		t.Errorf("CF SSH SG ID = %v, want sg-prod-ocf-cf-ssh-ingress", sg.ID)
	}
}

// ==============================================================================
// Test: Security Group Rules
// ==============================================================================

func TestSecurityGroupRules_BastionHasSSH(t *testing.T) {
	t.Parallel()

	manager, fakeNet, _ := setupSecurityTest(t)
	ctx := context.Background()

	err := manager.CreateSecurityGroups(ctx)
	if err != nil {
		t.Fatalf("CreateSecurityGroups failed: %v", err)
	}

	// Find bastion security group
	var bastionSG *cpi.SecurityGroup
	for _, sg := range fakeNet.createdSecurityGroups {
		if sg.Name == "prod-bastion" {
			bastionSG = sg
			break
		}
	}

	if bastionSG == nil {
		t.Fatal("Bastion security group not found")
	}

	// Verify SSH rule exists (port 22)
	sshRuleFound := false
	for _, rule := range bastionSG.Rules {
		if rule.Direction == "ingress" && rule.Protocol == "tcp" && rule.PortRangeMin == 22 {
			sshRuleFound = true
			if rule.RemoteIPCIDR != "0.0.0.0/0" {
				t.Errorf("SSH rule RemoteIPCIDR = %v, want 0.0.0.0/0", rule.RemoteIPCIDR)
			}
			break
		}
	}

	if !sshRuleFound {
		t.Error("SSH ingress rule not found in bastion security group")
	}
}

func TestSecurityGroupRules_InfraHasMultiplePorts(t *testing.T) {
	t.Parallel()

	manager, fakeNet, _ := setupSecurityTest(t)
	ctx := context.Background()

	err := manager.CreateSecurityGroups(ctx)
	if err != nil {
		t.Fatalf("CreateSecurityGroups failed: %v", err)
	}

	// Find infra security group
	var infraSG *cpi.SecurityGroup
	for _, sg := range fakeNet.createdSecurityGroups {
		if sg.Name == "prod-infra" {
			infraSG = sg
			break
		}
	}

	if infraSG == nil {
		t.Fatal("Infra security group not found")
	}

	// Verify infra has multiple ports (SSH, HTTP, HTTPS, HTTP-ALT, HTTPS-ALT)
	expectedPorts := []int{22, 80, 443, 8080, 8443}
	foundPorts := make(map[int]bool)

	for _, rule := range infraSG.Rules {
		if rule.Direction == "ingress" && rule.Protocol == "tcp" {
			foundPorts[rule.PortRangeMin] = true
		}
	}

	for _, port := range expectedPorts {
		if !foundPorts[port] {
			t.Errorf("Expected port %d not found in infra security group", port)
		}
	}
}

func TestSecurityGroupRules_TCPRouterHasFullRange(t *testing.T) {
	t.Parallel()

	manager, fakeNet, _ := setupSecurityTest(t)
	ctx := context.Background()

	err := manager.CreateSecurityGroups(ctx)
	if err != nil {
		t.Fatalf("CreateSecurityGroups failed: %v", err)
	}

	// Find TCP router security group
	var tcpRouterSG *cpi.SecurityGroup
	for _, sg := range fakeNet.createdSecurityGroups {
		if sg.Name == "prod-ocf-cf-tcp-router-ingress" {
			tcpRouterSG = sg
			break
		}
	}

	if tcpRouterSG == nil {
		t.Fatal("TCP Router security group not found")
	}

	// Verify TCP router has wide port range (1024-65535)
	rangeRuleFound := false
	for _, rule := range tcpRouterSG.Rules {
		if rule.Direction == "ingress" && rule.Protocol == "tcp" {
			if rule.PortRangeMin == 1024 && rule.PortRangeMax == 65535 {
				rangeRuleFound = true
				break
			}
		}
	}

	if !rangeRuleFound {
		t.Error("TCP router port range rule (1024-65535) not found")
	}
}

// ==============================================================================
// Test: All Security Groups Have Egress Rules
// ==============================================================================

func TestAllSecurityGroups_HaveEgressRules(t *testing.T) {
	t.Parallel()

	manager, fakeNet, _ := setupSecurityTest(t)
	ctx := context.Background()

	err := manager.CreateSecurityGroups(ctx)
	if err != nil {
		t.Fatalf("CreateSecurityGroups failed: %v", err)
	}

	// Verify each security group has at least one egress rule
	for _, sg := range fakeNet.createdSecurityGroups {
		egressRuleFound := false
		for _, rule := range sg.Rules {
			if rule.Direction == "egress" {
				egressRuleFound = true
				break
			}
		}

		if !egressRuleFound {
			t.Errorf("Security group %s has no egress rules", sg.Name)
		}
	}
}

// ==============================================================================
// Test: Security Group Tags
// ==============================================================================

func TestSecurityGroups_HaveCorrectTags(t *testing.T) {
	t.Parallel()

	manager, fakeNet, _ := setupSecurityTest(t)
	ctx := context.Background()

	err := manager.CreateSecurityGroups(ctx)
	if err != nil {
		t.Fatalf("CreateSecurityGroups failed: %v", err)
	}

	// Verify each security group has correct tags
	for _, sg := range fakeNet.createdSecurityGroups {
		if sg.Tags == nil {
			t.Errorf("Security group %s has no tags", sg.Name)
			continue
		}

		// Verify base metadata tags (using hyphenated names as per metadata.go)
		if sg.Tags["managed-by"] != "ocfp" {
			t.Errorf("Security group %s missing or wrong managed-by tag: got %v, want ocfp", sg.Name, sg.Tags["managed-by"])
		}

		if sg.Tags["bloc"] != "prod" {
			t.Errorf("Security group %s has wrong bloc tag: got %v, want prod", sg.Name, sg.Tags["bloc"])
		}

		if sg.Tags["created-by"] != "ocfp" {
			t.Errorf("Security group %s missing or wrong created-by tag: got %v, want ocfp", sg.Name, sg.Tags["created-by"])
		}

		// Verify timestamp tags exist
		if _, ok := sg.Tags["created-at"]; !ok {
			t.Errorf("Security group %s missing created-at tag", sg.Name)
		}

		if _, ok := sg.Tags["updated-at"]; !ok {
			t.Errorf("Security group %s missing updated-at tag", sg.Name)
		}

		// Verify Name tag for AWS
		if sg.Tags["Name"] != sg.Name {
			t.Errorf("Security group %s has wrong Name tag: %v", sg.Name, sg.Tags["Name"])
		}
	}
}

// ==============================================================================
// Test: Network ID Association
// ==============================================================================

func TestSecurityGroups_AssociatedWithCorrectNetwork(t *testing.T) {
	t.Parallel()

	manager, fakeNet, _ := setupSecurityTest(t)
	ctx := context.Background()

	err := manager.CreateSecurityGroups(ctx)
	if err != nil {
		t.Fatalf("CreateSecurityGroups failed: %v", err)
	}

	// Verify all security groups have correct network ID
	for _, sg := range fakeNet.createdSecurityGroups {
		if sg.NetworkID != "net-test-123" {
			t.Errorf("Security group %s has wrong NetworkID: %v, want net-test-123", sg.Name, sg.NetworkID)
		}
	}
}
