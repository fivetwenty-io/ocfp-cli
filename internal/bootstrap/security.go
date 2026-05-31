package bootstrap

import (
	"context"
	"fmt"
	"os"
	"strings"
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
	credhubPort            = 8844
	vaultPort              = 8484
	boshPort               = 25555
	boshAgentPort          = 6868
	cfSSHPort              = 2222
	tcpRouterMin           = 1024
	tcpRouterMax           = 65535
	maxPortsCompactDisplay = 3
	artifactsS3Port        = 9000
	artifactsConsolePort   = 9001
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
		if handled := m.handleExistingSecurityGroupInState(ctx, netMgr, groupName, &group); handled {
			continue
		}

		// Check if already exists in cloud (state may be out of sync)
		handled, err := m.handleExistingSecurityGroupInCloud(ctx, netMgr, groupName, &group, networkIDStr)
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
func (m *Manager) handleExistingSecurityGroupInState(ctx context.Context, netMgr cpi.NetworkManager, groupName string, groupDef *securityGroupDef) bool {
	existingSG, _ := m.stateManager.GetResource("security_group", groupName)
	if existingSG == nil {
		return false
	}

	// Verify the security group actually exists in the cloud provider
	existingInCloud, err := netMgr.GetSecurityGroup(ctx, existingSG.ID)
	if err == nil && existingInCloud != nil {
		logger.Infof("Security group %s already exists (verified in cloud: %s), ensuring rules", groupName, existingSG.ID)

		// Ensure security group has all required rules
		err = m.ensureSecurityGroupRules(ctx, existingSG.ID, groupDef)
		if err != nil {
			logger.Warnf("Failed to ensure rules for security group %s: %v", groupName, err)
			// Continue anyway, don't fail bootstrap
		}

		// Update state resource with current timestamp and properties
		err = m.stateManager.AddResource(&state.Resource{
			ID:         existingSG.ID,
			Type:       "security_group",
			Name:       groupName,
			Provider:   m.options.Provider,
			State:      string(cpi.ResourceStateActive),
			Properties: map[string]interface{}{"rules": len(groupDef.rules)},
			Tags:       m.baseTags(),
			CreatedAt:  existingSG.CreatedAt,
			UpdatedAt:  time.Now(),
		})
		if err != nil {
			logger.Warnf("Failed to update security group state for %s: %v", groupName, err)
			// Continue anyway, don't fail bootstrap
		}

		// Set output even if skipping
		_ = m.stateManager.SetOutput(fmt.Sprintf("sg_%s_id", groupDef.name), existingSG.ID)

		return true
	}

	// Security group exists in state but NOT in cloud - remove from state and recreate
	logger.Warnf("Security group %s found in state (ID: %s) but not in cloud, will recreate", groupName, existingSG.ID)
	_ = m.stateManager.RemoveResource("security_group", groupName)

	return false
}

// handleExistingSecurityGroupInCloud checks if a security group exists in cloud and syncs it to state.
// Returns true if the security group was handled (exists in cloud), false if it needs to be created.
func (m *Manager) handleExistingSecurityGroupInCloud(ctx context.Context, netMgr cpi.NetworkManager, groupName string, groupDef *securityGroupDef, networkIDStr string) (bool, error) {
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
	logger.Infof("Security group %s already exists in cloud (id=%s), syncing to state and ensuring rules", groupName, existingGroup.ID)
	_, _ = fmt.Fprintf(os.Stdout, "    • Security group %s already exists, ensuring rules...\n", groupName)

	// Ensure security group has all required rules
	err := m.ensureSecurityGroupRules(ctx, existingGroup.ID, groupDef)
	if err != nil {
		return false, fmt.Errorf("failed to ensure rules for security group %s: %w", groupName, err)
	}

	// Add to state
	err = m.stateManager.AddResource(&state.Resource{
		ID:         existingGroup.ID,
		Type:       "security_group",
		Name:       groupName,
		Provider:   m.options.Provider,
		State:      string(cpi.ResourceStateActive),
		Properties: map[string]interface{}{"rules": len(groupDef.rules)},
		Tags:       m.baseTags(),
		CreatedAt:  existingGroup.CreatedAt,
		UpdatedAt:  time.Now(),
	})
	if err != nil {
		return false, fmt.Errorf("failed to save existing security group to state: %w", err)
	}

	// Set output
	_ = m.stateManager.SetOutput(fmt.Sprintf("sg_%s_id", groupDef.name), existingGroup.ID)

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
		m.artifactsSecurityGroupDef(),
	}
}

// artifactsSecurityGroupDef defines the security group for the ocfp-artifacts
// (RustFS S3) VM. RustFS S3 (9000) + console (9001) and SSH (22) are opened to
// the bloc network CIDR only — the blobstore is an intra-SDN service reached by
// the bastion, the BOSH directors, and CF VMs, plus the `ocfp artifacts
// provision` step. It is never exposed to the operator's public ingress IPs.
func (m *Manager) artifactsSecurityGroupDef() securityGroupDef {
	cidr := m.blocNetworkCIDR()

	return securityGroupDef{
		name:        "artifacts",
		description: "Security group for ocfp-artifacts RustFS blobstore",
		rules: []*cpi.SecurityRule{
			{Direction: "ingress", Protocol: "tcp", PortRangeMin: sshPort, PortRangeMax: sshPort, RemoteIPCIDR: cidr, Description: "SSH from bloc network"},
			{Direction: "ingress", Protocol: "tcp", PortRangeMin: artifactsS3Port, PortRangeMax: artifactsS3Port, RemoteIPCIDR: cidr, Description: "RustFS S3 from bloc network"},
			{Direction: "ingress", Protocol: "tcp", PortRangeMin: artifactsConsolePort, PortRangeMax: artifactsConsolePort, RemoteIPCIDR: cidr, Description: "RustFS console from bloc network"},
			{Direction: "egress", Protocol: "all", RemoteIPCIDR: "0.0.0.0/0", Description: "Allow all outbound"},
		},
	}
}

// blocNetworkCIDR returns the bloc's primary network CIDR, preferring the
// canonical `cidr` field and falling back to `networkCidr`/`network_cidr`.
// When neither is set it returns the broad RFC1918 10/8 range so intra-SDN
// services remain reachable rather than silently firewalled off.
func (m *Manager) blocNetworkCIDR() string {
	if c := strings.TrimSpace(m.config.Network.CIDR); c != "" {
		return c
	}

	if c := strings.TrimSpace(m.config.Network.NetworkCIDR); c != "" {
		return c
	}

	return "10.0.0.0/8"
}

func (m *Manager) bastionSecurityGroupDef() securityGroupDef {
	// Build SSH ingress rules based on allowed_ingress_ips from config
	var sshRules []*cpi.SecurityRule

	if len(m.config.AllowedIngressIPs) > 0 {
		// Create a rule for each allowed IP/CIDR
		for _, allowedIP := range m.config.AllowedIngressIPs {
			// Normalize IP to CIDR format (add /32 if missing)
			cidr := m.normalizeToCIDR(allowedIP)
			sshRules = append(sshRules, &cpi.SecurityRule{
				Direction:    "ingress",
				Protocol:     "tcp",
				PortRangeMin: sshPort,
				PortRangeMax: sshPort,
				RemoteIPCIDR: cidr,
				Description:  "Allow SSH from " + cidr,
			})
		}
	} else {
		// Fallback to allow from anywhere if no IPs are configured
		logger.Warnf("No allowed_ingress_ips configured, bastion SSH will be open to 0.0.0.0/0")

		sshRules = append(sshRules, &cpi.SecurityRule{
			Direction:    "ingress",
			Protocol:     "tcp",
			PortRangeMin: sshPort,
			PortRangeMax: sshPort,
			RemoteIPCIDR: "0.0.0.0/0",
			Description:  "SSH",
		})
	}

	// Add egress rule
	sshRules = append(sshRules, &cpi.SecurityRule{
		Direction:    "egress",
		Protocol:     "all",
		RemoteIPCIDR: "0.0.0.0/0",
		Description:  "Allow all outbound",
	})

	return securityGroupDef{
		name:        "bastion",
		description: "Security group for bastion host",
		rules:       sshRules,
	}
}

// normalizeToCIDR ensures an IP address has CIDR notation.
// If the IP doesn't contain a '/', it appends /32 for single host.
func (m *Manager) normalizeToCIDR(ip string) string {
	if !strings.Contains(ip, "/") {
		return ip + "/32"
	}

	return ip
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
			{Direction: "ingress", Protocol: "tcp", PortRangeMin: httpsAltPort, PortRangeMax: httpsAltPort, Description: "UAA/HTTPS-ALT"},
			{Direction: "ingress", Protocol: "tcp", PortRangeMin: credhubPort, PortRangeMax: credhubPort, Description: "CredHub"},
			{Direction: "ingress", Protocol: "tcp", PortRangeMin: boshPort, PortRangeMax: boshPort, Description: "BOSH Director"},
			{Direction: "ingress", Protocol: "tcp", PortRangeMin: vaultPort, PortRangeMax: vaultPort, Description: "Vault"},
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
// Security Group Rules Reconciliation
// ==============================================================================

// ensureSecurityGroupRules ensures a security group has all required rules from its definition.
// This fixes security groups that exist but have missing or incomplete rules.
func (m *Manager) ensureSecurityGroupRules(ctx context.Context, groupID string, groupDef *securityGroupDef) error {
	secMgr := m.provider.Security()
	if secMgr == nil {
		logger.Debugf("Security manager not available, skipping rule reconciliation for group %s", groupID)

		return nil
	}

	// List current rules
	currentRules, err := secMgr.ListSecurityRules(ctx, groupID)
	if err != nil {
		return fmt.Errorf("failed to list security rules for group %s: %w", groupID, err)
	}

	// Find and add missing rules
	addedCount := 0

	for _, expectedRule := range groupDef.rules {
		if !m.ruleExists(currentRules, expectedRule) {
			logger.Infof("Adding missing rule to security group %s: %s %s port %d-%d",
				groupID, expectedRule.Direction, expectedRule.Protocol,
				expectedRule.PortRangeMin, expectedRule.PortRangeMax)

			err = secMgr.AddSecurityRule(ctx, groupID, expectedRule)
			if err != nil {
				return fmt.Errorf("failed to add security rule to group %s: %w", groupID, err)
			}

			addedCount++
		}
	}

	if addedCount > 0 {
		logger.Infof("Added %d missing rules to security group %s", addedCount, groupID)
		_, _ = fmt.Fprintf(os.Stdout, "    • Added %d missing rules to security group\n", addedCount)
	} else {
		logger.Debugf("Security group %s already has all required rules", groupID)
	}

	return nil
}

// ruleExists checks if a rule exists in the list of current rules.
func (m *Manager) ruleExists(currentRules []*cpi.SecurityRule, expectedRule *cpi.SecurityRule) bool {
	for _, current := range currentRules {
		if m.rulesMatch(current, expectedRule) {
			return true
		}
	}

	return false
}

// rulesMatch compares two security rules for equivalence.
func (m *Manager) rulesMatch(r1, r2 *cpi.SecurityRule) bool { //nolint:varnamelen // r2 is clear in context
	// Direction must match
	if r1.Direction != r2.Direction {
		return false
	}

	// Protocol must match (treat empty and "all" as equivalent)
	proto1 := r1.Protocol
	proto2 := r2.Protocol

	if proto1 == "" {
		proto1 = protocolAll
	}

	if proto2 == "" {
		proto2 = protocolAll
	}

	if proto1 != proto2 {
		return false
	}

	// For protocol "all", ports don't matter
	if proto1 == protocolAll || proto2 == protocolAll {
		// Check CIDR or remote group
		return r1.RemoteIPCIDR == r2.RemoteIPCIDR && r1.RemoteGroup == r2.RemoteGroup
	}

	// Port ranges must match
	if r1.PortRangeMin != r2.PortRangeMin || r1.PortRangeMax != r2.PortRangeMax {
		return false
	}

	// Remote CIDR or remote group must match
	return r1.RemoteIPCIDR == r2.RemoteIPCIDR && r1.RemoteGroup == r2.RemoteGroup
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
