package bootstrap

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

// Security-specific constants - network ports.
const (
	sshPort                = 22
	httpPort               = 80
	httpsPort              = 443
	httpAltPort            = 8080
	httpsAltPort           = 8443
	boshPort               = 25555
	boshAgentPort          = 6868
	cfSSHPort              = 2222
	tcpRouterMin           = 1024
	tcpRouterMax           = 65535
	maxPortsCompactDisplay = 3
)

// securityGroupDef represents a default security group and its rules (pre-creation).
type securityGroupDef struct {
	name        string
	description string
	rules       []*cpi.SecurityRule
}

// ==============================================================================
// Security Group Creation
// ==============================================================================

// CreateSecurityGroups creates all default security groups (exported for testing).
func (m *Manager) CreateSecurityGroups(ctx context.Context) error {
	netMgr := m.provider.NetworkManager()
	groups := m.defaultSecurityGroupDefs()

	networkIDStr, err := m.getNetworkIDFromState()
	if err != nil {
		return err
	}

	logger.Infof("Creating %d security groups for network %s", len(groups), networkIDStr)

	for _, group := range groups {
		groupName := fmt.Sprintf("%s-%s", m.options.BlocName, group.name)

		// Check if already exists in state and verify it exists in cloud
		if handled := m.handleExistingSecurityGroupInState(ctx, netMgr, groupName, group.name); handled {
			continue
		}

		// Check if already exists in cloud (state may be out of sync)
		handled, err := m.handleExistingSecurityGroupInCloud(ctx, netMgr, groupName, group.name, networkIDStr)
		if err != nil {
			return err
		} else if handled {
			continue
		}

		// Create new security group (doesn't exist in state or cloud)
		err = m.createNewSecurityGroup(ctx, netMgr, groupName, &group)
		if err != nil {
			return err
		}
	}

	return nil
}

// handleExistingSecurityGroupInState checks if a security group exists in state and verifies it in cloud.
// Returns true if the security group was handled (exists and verified), false if it needs to be created.
func (m *Manager) handleExistingSecurityGroupInState(ctx context.Context, netMgr cpi.NetworkManager, groupName, shortName string) bool {
	existingSG, _ := m.stateManager.GetResource("security_group", groupName)
	if existingSG == nil {
		return false
	}

	// Verify the security group actually exists in the cloud provider
	existingInCloud, err := netMgr.GetSecurityGroup(ctx, existingSG.ID)
	if err == nil && existingInCloud != nil {
		logger.Infof("Security group %s already exists (verified in cloud: %s), skipping creation", groupName, existingSG.ID)
		// Set output even if skipping
		_ = m.stateManager.SetOutput(fmt.Sprintf("sg_%s_id", shortName), existingSG.ID)

		return true
	}

	// Security group exists in state but NOT in cloud - remove from state and recreate
	logger.Warnf("Security group %s found in state (ID: %s) but not in cloud, will recreate", groupName, existingSG.ID)
	_ = m.stateManager.RemoveResource("security_group", groupName)

	return false
}

// handleExistingSecurityGroupInCloud checks if a security group exists in cloud and syncs it to state.
// Returns true if the security group was handled (exists in cloud), false if it needs to be created.
func (m *Manager) handleExistingSecurityGroupInCloud(ctx context.Context, netMgr cpi.NetworkManager, groupName, shortName, networkIDStr string) (bool, error) {
	existingFilters := map[string]string{
		"name":       groupName,
		"network-id": networkIDStr,
	}

	existingGroups, _ := netMgr.ListSecurityGroups(ctx, existingFilters)
	if len(existingGroups) == 0 {
		return false, nil
	}

	// Security group exists in cloud but not in state - sync state
	existingGroup := existingGroups[0]
	logger.Infof("Security group %s already exists in cloud (id=%s), syncing to state", groupName, existingGroup.ID)
	_, _ = fmt.Fprintf(os.Stdout, "    • Security group %s already exists, recording in state...\n", groupName)

	// Add to state
	err := m.stateManager.AddResource(&state.Resource{
		ID:         existingGroup.ID,
		Type:       "security_group",
		Name:       groupName,
		Provider:   m.options.Provider,
		State:      string(cpi.ResourceStateActive),
		Properties: map[string]interface{}{"rules": len(existingGroup.Rules)},
		Tags:       m.baseTags(),
		CreatedAt:  existingGroup.CreatedAt,
		UpdatedAt:  time.Now(),
	})
	if err != nil {
		return false, fmt.Errorf("failed to save existing security group to state: %w", err)
	}

	// Set output
	_ = m.stateManager.SetOutput(fmt.Sprintf("sg_%s_id", shortName), existingGroup.ID)

	return true, nil
}

// createNewSecurityGroup creates a new security group and saves it to state.
func (m *Manager) createNewSecurityGroup(ctx context.Context, netMgr cpi.NetworkManager, groupName string, group *securityGroupDef) error {
	networkIDStr, err := m.getNetworkIDFromState()
	if err != nil {
		return err
	}

	logger.Infof("Creating security group: name=%s rules=%d", groupName, len(group.rules))
	_, _ = fmt.Fprintf(os.Stdout, "    • Creating security group %s (%d rules)...\n", groupName, len(group.rules))

	// Add Name tag for AWS console display
	tags := m.baseTags()
	tags["Name"] = groupName

	securityGroup, err := netMgr.CreateSecurityGroup(ctx, &cpi.CreateSecurityGroupRequest{
		Name:        groupName,
		Description: group.description,
		NetworkID:   networkIDStr,
		Rules:       group.rules,
		Tags:        tags,
	})
	if err != nil {
		return fmt.Errorf("failed to create security group %s: %w", groupName, err)
	}

	// Save to state
	err = m.stateManager.AddResource(&state.Resource{
		ID:         securityGroup.ID,
		Type:       "security_group",
		Name:       groupName,
		Provider:   m.options.Provider,
		State:      string(cpi.ResourceStateActive),
		Properties: map[string]interface{}{"rules": len(group.rules)},
		Tags:       m.baseTags(),
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	})
	if err != nil {
		return fmt.Errorf("failed to save security group to state: %w", err)
	}

	// Set output
	_ = m.stateManager.SetOutput(fmt.Sprintf("sg_%s_id", group.name), securityGroup.ID)

	logger.Infof("Security group created successfully: id=%s name=%s", securityGroup.ID, groupName)

	return nil
}

func (m *Manager) getNetworkIDFromState() (string, error) {
	networkOutput, err := m.stateManager.GetOutput("network_id")
	if err != nil {
		return "", fmt.Errorf("failed to get network ID from state: %w", err)
	}

	networkIDStr, ok := networkOutput.(string)
	if !ok || networkIDStr == "" {
		return "", ErrInvalidNetworkID(networkOutput)
	}

	return networkIDStr, nil
}

// ==============================================================================
// Security Group Definitions
// ==============================================================================

func (m *Manager) defaultSecurityGroupDefs() []securityGroupDef {
	return []securityGroupDef{
		m.bastionSecurityGroupDef(),
		m.infraSecurityGroupDef(),
		m.ocfpSecurityGroupDef(),
		m.lbExtSecurityGroupDef(),
		m.cfRouterSecurityGroupDef(),
		m.cfTCPRouterSecurityGroupDef(),
		m.cfSSHSecurityGroupDef(),
	}
}

func (m *Manager) bastionSecurityGroupDef() securityGroupDef {
	return securityGroupDef{
		name:        "bastion",
		description: "Security group for bastion host",
		rules: []*cpi.SecurityRule{
			{Direction: "ingress", Protocol: "tcp", PortRangeMin: sshPort, PortRangeMax: sshPort, RemoteIPCIDR: "0.0.0.0/0", Description: "SSH"},
			{Direction: "egress", Protocol: "all", RemoteIPCIDR: "0.0.0.0/0", Description: "Allow all outbound"},
		},
	}
}

func (m *Manager) infraSecurityGroupDef() securityGroupDef {
	return securityGroupDef{
		name:        "infra",
		description: "Security group for infrastructure services",
		rules: []*cpi.SecurityRule{
			{Direction: "ingress", Protocol: "tcp", PortRangeMin: sshPort, PortRangeMax: sshPort, Description: "SSH"},
			{Direction: "ingress", Protocol: "tcp", PortRangeMin: httpPort, PortRangeMax: httpPort, Description: "HTTP"},
			{Direction: "ingress", Protocol: "tcp", PortRangeMin: httpsPort, PortRangeMax: httpsPort, Description: "HTTPS"},
			{Direction: "ingress", Protocol: "tcp", PortRangeMin: httpAltPort, PortRangeMax: httpAltPort, Description: "HTTP-ALT"},
			{Direction: "ingress", Protocol: "tcp", PortRangeMin: httpsAltPort, PortRangeMax: httpsAltPort, Description: "HTTPS-ALT"},
			{Direction: "egress", Protocol: "all", RemoteIPCIDR: "0.0.0.0/0", Description: "Allow all outbound"},
		},
	}
}

func (m *Manager) ocfpSecurityGroupDef() securityGroupDef {
	return securityGroupDef{
		name:        "ocfp",
		description: "Security group for OCFP services",
		rules: []*cpi.SecurityRule{
			{Direction: "ingress", Protocol: "tcp", PortRangeMin: sshPort, PortRangeMax: sshPort, Description: "SSH"},
			{Direction: "ingress", Protocol: "tcp", PortRangeMin: httpPort, PortRangeMax: httpPort, Description: "HTTP"},
			{Direction: "ingress", Protocol: "tcp", PortRangeMin: httpsPort, PortRangeMax: httpsPort, Description: "HTTPS"},
			{Direction: "ingress", Protocol: "tcp", PortRangeMin: boshPort, PortRangeMax: boshPort, Description: "BOSH Director"},
			{Direction: "ingress", Protocol: "tcp", PortRangeMin: boshAgentPort, PortRangeMax: boshAgentPort, Description: "BOSH Agent"},
			{Direction: "egress", Protocol: "all", RemoteIPCIDR: "0.0.0.0/0", Description: "Allow all outbound"},
		},
	}
}

func (m *Manager) lbExtSecurityGroupDef() securityGroupDef {
	return securityGroupDef{
		name:        "lb-ext",
		description: "Security group for external load balancers",
		rules: []*cpi.SecurityRule{
			{Direction: "ingress", Protocol: "tcp", PortRangeMin: httpsPort, PortRangeMax: httpsPort, RemoteIPCIDR: "0.0.0.0/0", Description: "HTTPS external"},
			{Direction: "egress", Protocol: "all", RemoteIPCIDR: "0.0.0.0/0", Description: "Allow all outbound"},
		},
	}
}

func (m *Manager) cfRouterSecurityGroupDef() securityGroupDef {
	return securityGroupDef{
		name:        "ocf-cf-router-ingress",
		description: "Security group for Cloud Foundry router ingress",
		rules: []*cpi.SecurityRule{
			{Direction: "ingress", Protocol: "tcp", PortRangeMin: httpPort, PortRangeMax: httpPort, RemoteIPCIDR: "0.0.0.0/0", Description: "CF Router HTTP"},
			{Direction: "ingress", Protocol: "tcp", PortRangeMin: httpsPort, PortRangeMax: httpsPort, RemoteIPCIDR: "0.0.0.0/0", Description: "CF Router HTTPS"},
			{Direction: "ingress", Protocol: "tcp", PortRangeMin: cfSSHPort, PortRangeMax: cfSSHPort, RemoteIPCIDR: "0.0.0.0/0", Description: "CF SSH"},
			{Direction: "egress", Protocol: "all", RemoteIPCIDR: "0.0.0.0/0", Description: "Allow all outbound"},
		},
	}
}

func (m *Manager) cfTCPRouterSecurityGroupDef() securityGroupDef {
	return securityGroupDef{
		name:        "ocf-cf-tcp-router-ingress",
		description: "Security group for Cloud Foundry TCP router ingress",
		rules: []*cpi.SecurityRule{
			{Direction: "ingress", Protocol: "tcp", PortRangeMin: tcpRouterMin, PortRangeMax: tcpRouterMax, RemoteIPCIDR: "0.0.0.0/0", Description: "CF TCP Router"},
			{Direction: "egress", Protocol: "all", RemoteIPCIDR: "0.0.0.0/0", Description: "Allow all outbound"},
		},
	}
}

func (m *Manager) cfSSHSecurityGroupDef() securityGroupDef {
	return securityGroupDef{
		name:        "ocf-cf-ssh-ingress",
		description: "Security group for Cloud Foundry SSH proxy ingress",
		rules: []*cpi.SecurityRule{
			{Direction: "ingress", Protocol: "tcp", PortRangeMin: cfSSHPort, PortRangeMax: cfSSHPort, RemoteIPCIDR: "0.0.0.0/0", Description: "CF SSH Proxy"},
			{Direction: "egress", Protocol: "all", RemoteIPCIDR: "0.0.0.0/0", Description: "Allow all outbound"},
		},
	}
}

// ==============================================================================
// Utility Functions
// ==============================================================================

func (m *Manager) formatRemote(remote string) string {
	if remote == "" || remote == "0.0.0.0/0" || remote == "::/0" {
		return "any"
	}

	return remote
}

// ==============================================================================
// Display Functions
// ==============================================================================
