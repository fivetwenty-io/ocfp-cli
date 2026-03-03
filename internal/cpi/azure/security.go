package azure

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v5"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// CreateSecurityGroup creates a new network security group.
func (m *SecurityManager) CreateSecurityGroup(ctx context.Context, req *cpi.CreateSecurityGroupRequest) (*cpi.SecurityGroup, error) {
	if req == nil {
		return nil, ErrInvalidRequest
	}

	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	// Ensure resource group exists
	err = m.client.EnsureResourceGroup(ctx)
	if err != nil {
		return nil, err
	}

	// Prepare NSG parameters
	nsgParams := armnetwork.SecurityGroup{
		Location: to.Ptr(m.client.getLocation()),
		Tags:     BuildTags(MergeTags(m.client.config.DefaultTags, req.Tags)),
	}

	// Add initial rules if provided
	if len(req.Rules) > 0 {
		nsgParams.Properties = &armnetwork.SecurityGroupPropertiesFormat{
			SecurityRules: m.convertRulesToAzure(req.Rules),
		}
	}

	poller, err := m.client.networkSecurityGroupsClient.BeginCreateOrUpdate(
		ctx,
		m.client.getResourceGroup(),
		req.Name,
		nsgParams,
		nil,
	)
	if err != nil {
		return nil, WrapAzureError(err, "CreateSecurityGroup")
	}

	result, err := poller.PollUntilDone(ctx, nil)
	if err != nil {
		return nil, WrapAzureError(err, "CreateSecurityGroup")
	}

	logger.Infow("Created network security group", "name", req.Name)

	return m.nsgToSecurityGroup(&result.SecurityGroup), nil
}

// GetSecurityGroup retrieves a network security group by ID or name.
func (m *SecurityManager) GetSecurityGroup(ctx context.Context, id string) (*cpi.SecurityGroup, error) { //nolint:varnamelen
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	name := ExtractResourceName(id)

	result, err := m.client.networkSecurityGroupsClient.Get(ctx, m.client.getResourceGroup(), name, nil)
	if err != nil {
		return nil, WrapAzureError(err, "GetSecurityGroup")
	}

	return m.nsgToSecurityGroup(&result.SecurityGroup), nil
}

// ListSecurityGroups lists all network security groups.
func (m *SecurityManager) ListSecurityGroups(ctx context.Context, filters map[string]string) ([]*cpi.SecurityGroup, error) {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	pager := m.client.networkSecurityGroupsClient.NewListPager(m.client.getResourceGroup(), nil)

	var securityGroups []*cpi.SecurityGroup

	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, WrapAzureError(err, "ListSecurityGroups")
		}

		for _, nsg := range page.Value {
			sg := m.nsgToSecurityGroup(nsg)
			if matchesSecurityGroupFilters(sg.Tags, filters) {
				securityGroups = append(securityGroups, sg)
			}
		}
	}

	return securityGroups, nil
}

// DeleteSecurityGroup deletes a network security group.
func (m *SecurityManager) DeleteSecurityGroup(ctx context.Context, id string) error { //nolint:varnamelen
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return err
	}

	name := ExtractResourceName(id)

	poller, err := m.client.networkSecurityGroupsClient.BeginDelete(ctx, m.client.getResourceGroup(), name, nil)
	if err != nil {
		return WrapAzureError(err, "DeleteSecurityGroup")
	}

	_, err = poller.PollUntilDone(ctx, nil)
	if err != nil {
		return WrapAzureError(err, "DeleteSecurityGroup")
	}

	logger.Infow("Deleted network security group", "name", name)

	return nil
}

// AddSecurityRule adds a rule to a network security group.
func (m *SecurityManager) AddSecurityRule(ctx context.Context, groupID string, rule *cpi.SecurityRule) error {
	if rule == nil {
		return ErrInvalidRequest
	}

	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return err
	}

	nsgName := ExtractResourceName(groupID)

	// Generate rule name if not provided
	ruleName := rule.ID
	if ruleName == "" {
		ruleName = GenerateUniqueName("rule", 80) //nolint:mnd
	}

	// Convert CPI rule to Azure rule
	azureRule := m.convertRuleToAzure(rule, 100) //nolint:mnd // Default priority

	poller, err := m.client.securityRulesClient.BeginCreateOrUpdate(
		ctx,
		m.client.getResourceGroup(),
		nsgName,
		ruleName,
		*azureRule,
		nil,
	)
	if err != nil {
		return WrapAzureError(err, "AddSecurityRule")
	}

	_, err = poller.PollUntilDone(ctx, nil)
	if err != nil {
		return WrapAzureError(err, "AddSecurityRule")
	}

	logger.Infow("Added security rule", "nsg", nsgName, "rule", ruleName)

	return nil
}

// RemoveSecurityRule removes a rule from a network security group.
func (m *SecurityManager) RemoveSecurityRule(ctx context.Context, groupID string, ruleID string) error {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return err
	}

	nsgName := ExtractResourceName(groupID)
	ruleName := ExtractResourceName(ruleID)

	poller, err := m.client.securityRulesClient.BeginDelete(ctx, m.client.getResourceGroup(), nsgName, ruleName, nil)
	if err != nil {
		return WrapAzureError(err, "RemoveSecurityRule")
	}

	_, err = poller.PollUntilDone(ctx, nil)
	if err != nil {
		return WrapAzureError(err, "RemoveSecurityRule")
	}

	logger.Infow("Removed security rule", "nsg", nsgName, "rule", ruleName)

	return nil
}

// ListSecurityRules lists all rules in a network security group.
func (m *SecurityManager) ListSecurityRules(ctx context.Context, groupID string) ([]*cpi.SecurityRule, error) {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	nsgName := ExtractResourceName(groupID)

	pager := m.client.securityRulesClient.NewListPager(m.client.getResourceGroup(), nsgName, nil)

	var rules []*cpi.SecurityRule

	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, WrapAzureError(err, "ListSecurityRules")
		}

		for _, rule := range page.Value {
			rules = append(rules, m.azureRuleToRule(rule))
		}
	}

	return rules, nil
}

// Helper functions

func (m *SecurityManager) nsgToSecurityGroup(nsg *armnetwork.SecurityGroup) *cpi.SecurityGroup {
	if nsg == nil {
		return nil
	}

	sg := &cpi.SecurityGroup{ //nolint:varnamelen // sg is clear in context
		ID:        DerefString(nsg.ID),
		Name:      DerefString(nsg.Name),
		Tags:      ExtractTags(nsg.Tags),
		CreatedAt: time.Now(),
	}

	if nsg.Properties != nil {
		// Extract rules
		if nsg.Properties.SecurityRules != nil {
			for _, rule := range nsg.Properties.SecurityRules {
				sg.Rules = append(sg.Rules, m.azureRuleToRule(rule))
			}
		}
	}

	return sg
}

func (m *SecurityManager) azureRuleToRule(rule *armnetwork.SecurityRule) *cpi.SecurityRule {
	if rule == nil {
		return nil
	}

	r := &cpi.SecurityRule{ //nolint:varnamelen // r is clear in context
		ID:          DerefString(rule.Name),
		Description: DerefString(rule.Properties.Description),
	}

	if rule.Properties == nil {
		return r
	}

	// Direction
	if rule.Properties.Direction != nil {
		switch *rule.Properties.Direction {
		case armnetwork.SecurityRuleDirectionInbound:
			r.Direction = "ingress"
		case armnetwork.SecurityRuleDirectionOutbound:
			r.Direction = "egress"
		}
	}

	// Protocol
	if rule.Properties.Protocol != nil {
		r.Protocol = string(*rule.Properties.Protocol)
		if r.Protocol == "*" {
			r.Protocol = "all"
		}
	}

	// Port range
	if rule.Properties.DestinationPortRange != nil {
		portRange := DerefString(rule.Properties.DestinationPortRange)
		r.PortRangeMin, r.PortRangeMax = parsePortRange(portRange)
	}

	// Source address
	if rule.Properties.SourceAddressPrefix != nil {
		r.RemoteIPCIDR = DerefString(rule.Properties.SourceAddressPrefix)
	}

	return r
}

func (m *SecurityManager) convertRulesToAzure(rules []*cpi.SecurityRule) []*armnetwork.SecurityRule {
	azureRules := make([]*armnetwork.SecurityRule, 0, len(rules))

	for i, rule := range rules {
		// Priority must be between 100 and 4096
		priority := int32(100 + i) //nolint:mnd
		azureRules = append(azureRules, m.convertRuleToAzure(rule, priority))
	}

	return azureRules
}

func (m *SecurityManager) convertRuleToAzure(rule *cpi.SecurityRule, priority int32) *armnetwork.SecurityRule {
	if rule == nil {
		return nil
	}

	// Determine direction
	direction := armnetwork.SecurityRuleDirectionInbound
	if rule.Direction == "egress" {
		direction = armnetwork.SecurityRuleDirectionOutbound
	}

	// Determine protocol
	protocol := armnetwork.SecurityRuleProtocolAsterisk

	switch rule.Protocol {
	case "tcp", "TCP":
		protocol = armnetwork.SecurityRuleProtocolTCP
	case "udp", "UDP":
		protocol = armnetwork.SecurityRuleProtocolUDP
	case "icmp", "ICMP":
		protocol = armnetwork.SecurityRuleProtocolIcmp
	}

	// Build port range
	portRange := "*"

	if rule.PortRangeMin > 0 && rule.PortRangeMax > 0 {
		if rule.PortRangeMin == rule.PortRangeMax {
			portRange = strconv.Itoa(rule.PortRangeMin)
		} else {
			portRange = fmt.Sprintf("%d-%d", rule.PortRangeMin, rule.PortRangeMax)
		}
	}

	// Source address
	sourceAddress := "*"
	if rule.RemoteIPCIDR != "" {
		sourceAddress = rule.RemoteIPCIDR
	}

	// Generate name if not provided
	name := rule.ID
	if name == "" {
		name = GenerateUniqueName("rule", 80) //nolint:mnd
	}

	return &armnetwork.SecurityRule{
		Name: to.Ptr(name),
		Properties: &armnetwork.SecurityRulePropertiesFormat{
			Description:              to.Ptr(rule.Description),
			Protocol:                 to.Ptr(protocol),
			SourcePortRange:          to.Ptr("*"),
			DestinationPortRange:     to.Ptr(portRange),
			SourceAddressPrefix:      to.Ptr(sourceAddress),
			DestinationAddressPrefix: to.Ptr("*"),
			Access:                   to.Ptr(armnetwork.SecurityRuleAccessAllow),
			Priority:                 to.Ptr(priority),
			Direction:                to.Ptr(direction),
		},
	}
}

func parsePortRange(portRange string) (int, int) {
	if portRange == "*" || portRange == "" {
		return 0, 65535 //nolint:mnd
	}

	// Check for range format "min-max"
	for i, c := range portRange {
		if c == '-' {
			minStr := portRange[:i]
			maxStr := portRange[i+1:]

			portMin, err1 := strconv.Atoi(minStr)
			portMax, err2 := strconv.Atoi(maxStr)

			if err1 == nil && err2 == nil {
				return portMin, portMax
			}

			break
		}
	}

	// Single port
	port, err := strconv.Atoi(portRange)
	if err == nil {
		return port, port
	}

	return 0, 65535 //nolint:mnd
}

func matchesSecurityGroupFilters(tags map[string]string, filters map[string]string) bool {
	if len(filters) == 0 {
		return true
	}

	for key, value := range filters {
		if tagValue, ok := tags[key]; !ok || tagValue != value {
			return false
		}
	}

	return true
}
