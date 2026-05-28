package aws

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

const (
	directionIngress  = "ingress"
	directionInbound  = "inbound"
	directionEgress   = "egress"
	directionOutbound = "outbound"
	protocolAll       = "all"
	ruleIDPartCount   = 5
	maxTCPUDPPort     = 65535
)

// SecurityManager handles AWS security group operations.
type SecurityManager struct {
	client *Client
	ec2    EC2API
}

// getEC2 returns the injected EC2API if set, otherwise falls back to the real client.
func (m *SecurityManager) getEC2(ctx context.Context) (EC2API, error) {
	if m.ec2 != nil {
		return m.ec2, nil
	}

	return m.client.getEC2Client(ctx)
}

// CreateSecurityGroup creates a new security group in AWS.
func (m *SecurityManager) CreateSecurityGroup(ctx context.Context, req *cpi.CreateSecurityGroupRequest) (*cpi.SecurityGroup, error) {
	if req.Name == "" {
		return nil, ErrInvalidRequest
	}

	if req.NetworkID == "" {
		return nil, fmt.Errorf("network ID (VPC ID) is required for AWS security groups: %w", ErrInvalidRequest)
	}

	ec2Client, err := m.getEC2(ctx)
	if err != nil {
		return nil, wrapError(err, "get EC2 client")
	}

	tags := m.prepareTags(req.Tags)
	createInput := m.buildCreateInput(req, tags)

	createResp, err := ec2Client.CreateSecurityGroup(ctx, createInput)
	if err != nil {
		return m.handleCreateSecurityGroupError(ctx, err, req)
	}

	groupID := aws.ToString(createResp.GroupId)

	if ruleErr := m.configureSecurityGroupRules(ctx, ec2Client, groupID, req.Rules); ruleErr != nil {
		return nil, fmt.Errorf("security group %s created but rule configuration failed: %w", groupID, ruleErr)
	}

	// Fetch the created security group with all details
	return m.GetSecurityGroup(ctx, groupID)
}

// GetSecurityGroup retrieves a security group by ID.
func (m *SecurityManager) GetSecurityGroup(ctx context.Context, groupID string) (*cpi.SecurityGroup, error) {
	if groupID == "" {
		return nil, ErrInvalidRequest
	}

	ec2Client, err := m.getEC2(ctx)
	if err != nil {
		return nil, wrapError(err, "get EC2 client")
	}

	resp, err := ec2Client.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{
		GroupIds: []string{groupID},
	})
	if err != nil {
		return nil, wrapError(err, "describe security group")
	}

	if len(resp.SecurityGroups) == 0 {
		return nil, ErrSecurityGroupNotFound
	}

	sg := resp.SecurityGroups[0]

	return m.convertSecurityGroup(&sg), nil
}

// GetSecurityGroupByName retrieves a security group by name and VPC ID.
func (m *SecurityManager) GetSecurityGroupByName(ctx context.Context, name, vpcID string) (*cpi.SecurityGroup, error) {
	if name == "" {
		return nil, ErrInvalidRequest
	}

	ec2Client, err := m.getEC2(ctx)
	if err != nil {
		return nil, wrapError(err, "get EC2 client")
	}

	filters := []types.Filter{
		{
			Name:   aws.String("group-name"),
			Values: []string{name},
		},
	}

	// Add VPC filter if provided
	if vpcID != "" {
		filters = append(filters, types.Filter{
			Name:   aws.String("vpc-id"),
			Values: []string{vpcID},
		})
	}

	resp, err := ec2Client.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{
		Filters: filters,
	})
	if err != nil {
		return nil, wrapError(err, "describe security group by name")
	}

	if len(resp.SecurityGroups) == 0 {
		return nil, ErrSecurityGroupNotFound
	}

	// If multiple groups with same name (shouldn't happen in same VPC), return first
	sg := resp.SecurityGroups[0]

	return m.convertSecurityGroup(&sg), nil
}

// ListSecurityGroups lists all security groups matching the given filters.
func (m *SecurityManager) ListSecurityGroups(ctx context.Context, filters map[string]string) ([]*cpi.SecurityGroup, error) {
	ec2Client, err := m.getEC2(ctx)
	if err != nil {
		return nil, wrapError(err, "get EC2 client")
	}

	// Preprocess filters to handle security-group-specific mappings
	processedFilters := make(map[string]string, len(filters))
	for key, value := range filters {
		switch key {
		case "network-id":
			// Map network-id to vpc-id for security groups
			processedFilters["vpc-id"] = value
		case "name":
			// Security groups use "group-name" filter
			processedFilters["group-name"] = value
		case "description":
			// Description is an AWS-specific filter
			processedFilters["description"] = value
		default:
			// All other filters (including bloc, managed-by) are passed through
			processedFilters[key] = value
		}
	}

	// Build AWS filters with proper tag handling
	awsFilters := buildAWSTagFilters(processedFilters)

	input := &ec2.DescribeSecurityGroupsInput{}
	if len(awsFilters) > 0 {
		input.Filters = awsFilters
	}

	resp, err := ec2Client.DescribeSecurityGroups(ctx, input)
	if err != nil {
		return nil, wrapError(err, "describe security groups")
	}

	result := make([]*cpi.SecurityGroup, 0, len(resp.SecurityGroups))
	for i := range resp.SecurityGroups {
		result = append(result, m.convertSecurityGroup(&resp.SecurityGroups[i]))
	}

	return result, nil
}

// DeleteSecurityGroup deletes a security group.
func (m *SecurityManager) DeleteSecurityGroup(ctx context.Context, groupID string) error {
	if groupID == "" {
		return ErrInvalidRequest
	}

	ec2Client, err := m.getEC2(ctx)
	if err != nil {
		return wrapError(err, "get EC2 client")
	}

	// Check if security group exists and is not in use
	group, err := m.GetSecurityGroup(ctx, groupID)
	if err != nil {
		return err
	}

	// Check if it's the default security group (cannot be deleted)
	if group.Name == "default" {
		return fmt.Errorf("cannot delete default security group: %w", ErrInvalidRequest)
	}

	_, err = ec2Client.DeleteSecurityGroup(ctx, &ec2.DeleteSecurityGroupInput{
		GroupId: aws.String(groupID),
	})
	if err != nil {
		return wrapError(err, "delete security group")
	}

	return nil
}

// AddSecurityRule adds a rule to a security group.
func (m *SecurityManager) AddSecurityRule(ctx context.Context, groupID string, rule *cpi.SecurityRule) error {
	if groupID == "" || rule == nil {
		return ErrInvalidRequest
	}

	err := m.validateSecurityRule(rule)
	if err != nil {
		return err
	}

	ec2Client, err := m.getEC2(ctx)
	if err != nil {
		return wrapError(err, "get EC2 client")
	}

	// Check if rule already exists (idempotency)
	exists, err := m.ruleExists(ctx, ec2Client, groupID, rule)
	if err != nil {
		return err
	}

	if exists {
		// Rule already exists, return success (idempotent)
		return nil
	}

	ipPerm, err := m.ruleToIPPermission(rule)
	if err != nil {
		return err
	}

	// Add rule based on direction
	switch strings.ToLower(rule.Direction) {
	case directionIngress, directionInbound:
		_, err = ec2Client.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
			GroupId:       aws.String(groupID),
			IpPermissions: []types.IpPermission{*ipPerm},
		})
	case directionEgress, directionOutbound:
		_, err = ec2Client.AuthorizeSecurityGroupEgress(ctx, &ec2.AuthorizeSecurityGroupEgressInput{
			GroupId:       aws.String(groupID),
			IpPermissions: []types.IpPermission{*ipPerm},
		})
	default:
		return fmt.Errorf("invalid direction %q: must be ingress or egress: %w", rule.Direction, ErrInvalidRequest)
	}

	if err != nil {
		return wrapError(err, "add security rule")
	}

	return nil
}

// RemoveSecurityRule removes a rule from a security group.
func (m *SecurityManager) RemoveSecurityRule(ctx context.Context, groupID string, ruleID string) error {
	if groupID == "" || ruleID == "" {
		return ErrInvalidRequest
	}

	// In AWS, rules are not identified by a unique ID but by their properties.
	// The ruleID should be formatted as: direction:protocol:fromPort:toPort:cidr_or_sgid
	// Example: "ingress:tcp:22:22:0.0.0.0/0" or "egress:all:0:0:sg-abc123"

	parts := strings.Split(ruleID, ":")
	if len(parts) < ruleIDPartCount {
		return fmt.Errorf("invalid rule ID format: %w", ErrInvalidRequest)
	}

	direction := parts[0]
	protocol := parts[1]

	fromPort, err := strconv.Atoi(parts[2])
	if err != nil {
		return fmt.Errorf("invalid fromPort %q in rule ID: %w", parts[2], ErrInvalidRequest)
	}

	toPort, err := strconv.Atoi(parts[3])
	if err != nil {
		return fmt.Errorf("invalid toPort %q in rule ID: %w", parts[3], ErrInvalidRequest)
	}

	remote := parts[4]

	// Reconstruct the rule
	rule := &cpi.SecurityRule{
		Direction:    direction,
		Protocol:     protocol,
		PortRangeMin: fromPort,
		PortRangeMax: toPort,
	}

	if strings.HasPrefix(remote, "sg-") {
		rule.RemoteGroup = remote
	} else {
		rule.RemoteIPCIDR = remote
	}

	// Convert to AWS IP permission
	ipPerm, err := m.ruleToIPPermission(rule)
	if err != nil {
		return err
	}

	ec2Client, err := m.getEC2(ctx)
	if err != nil {
		return wrapError(err, "get EC2 client")
	}

	// Remove rule based on direction
	switch strings.ToLower(direction) {
	case directionIngress, directionInbound:
		_, err = ec2Client.RevokeSecurityGroupIngress(ctx, &ec2.RevokeSecurityGroupIngressInput{
			GroupId:       aws.String(groupID),
			IpPermissions: []types.IpPermission{*ipPerm},
		})
	case directionEgress, directionOutbound:
		_, err = ec2Client.RevokeSecurityGroupEgress(ctx, &ec2.RevokeSecurityGroupEgressInput{
			GroupId:       aws.String(groupID),
			IpPermissions: []types.IpPermission{*ipPerm},
		})
	default:
		return fmt.Errorf("invalid direction %q: %w", direction, ErrInvalidRequest)
	}

	if err != nil {
		return wrapError(err, "remove security rule")
	}

	return nil
}

// ListSecurityRules lists all rules for a security group.
func (m *SecurityManager) ListSecurityRules(ctx context.Context, groupID string) ([]*cpi.SecurityRule, error) {
	if groupID == "" {
		return nil, ErrInvalidRequest
	}

	sg, err := m.GetSecurityGroup(ctx, groupID)
	if err != nil {
		return nil, err
	}

	return sg.Rules, nil
}

// handleCreateSecurityGroupError handles errors during security group creation.
func (m *SecurityManager) handleCreateSecurityGroupError(ctx context.Context, err error, req *cpi.CreateSecurityGroupRequest) (*cpi.SecurityGroup, error) {
	// Handle duplicate error idempotently - fetch and return existing group
	if IsAlreadyExists(err) {
		existingGroup, getErr := m.GetSecurityGroupByName(ctx, req.Name, req.NetworkID)
		if getErr != nil {
			return nil, fmt.Errorf("security group already exists but failed to retrieve: %w", getErr)
		}

		// Ensure rules match (add any missing rules); idempotent on duplicates.
		if len(req.Rules) > 0 {
			for _, rule := range req.Rules {
				if addErr := m.AddSecurityRule(ctx, existingGroup.ID, rule); addErr != nil && !IsAlreadyExists(addErr) {
					return nil, fmt.Errorf("reconcile rule %s:%s on existing group %s: %w",
						rule.Direction, rule.Protocol, existingGroup.ID, addErr)
				}
			}
		}

		return existingGroup, nil
	}

	return nil, wrapError(err, "create security group")
}

// convertSecurityGroup converts an AWS security group to the CPI type.
func (m *SecurityManager) convertSecurityGroup(securityGroup *types.SecurityGroup) *cpi.SecurityGroup {
	tags := make(map[string]string)

	for _, tag := range securityGroup.Tags {
		if tag.Key != nil && tag.Value != nil {
			tags[*tag.Key] = *tag.Value
		}
	}

	rules := make([]*cpi.SecurityRule, 0, len(securityGroup.IpPermissions)+len(securityGroup.IpPermissionsEgress))
	for _, perm := range securityGroup.IpPermissions {
		rules = append(rules, m.ipPermissionToRules(directionIngress, &perm)...)
	}

	for _, perm := range securityGroup.IpPermissionsEgress {
		rules = append(rules, m.ipPermissionToRules(directionEgress, &perm)...)
	}

	return &cpi.SecurityGroup{
		ID:          aws.ToString(securityGroup.GroupId),
		Name:        aws.ToString(securityGroup.GroupName),
		Description: aws.ToString(securityGroup.Description),
		NetworkID:   aws.ToString(securityGroup.VpcId),
		Rules:       rules,
		Tags:        tags,
		CreatedAt:   time.Now(),
	}
}

// ipPermissionToRules converts an AWS IP permission to CPI security rules.
// One AWS IP permission can result in multiple CPI rules (one per CIDR/SG reference).
func (m *SecurityManager) ipPermissionToRules(direction string, perm *types.IpPermission) []*cpi.SecurityRule {
	rules := make([]*cpi.SecurityRule, 0, len(perm.IpRanges)+len(perm.Ipv6Ranges)+len(perm.UserIdGroupPairs))

	protocol := aws.ToString(perm.IpProtocol)
	if protocol == "-1" {
		protocol = protocolAll
	}

	fromPort := int(aws.ToInt32(perm.FromPort))
	toPort := int(aws.ToInt32(perm.ToPort))

	// Handle ICMP protocol (no ports)
	if strings.HasPrefix(protocol, "icmp") {
		fromPort = 0
		toPort = 0
	}

	// Convert CIDR blocks
	for _, cidr := range perm.IpRanges {
		ruleID := m.generateRuleID(direction, protocol, fromPort, toPort, aws.ToString(cidr.CidrIp))
		rules = append(rules, &cpi.SecurityRule{
			ID:           ruleID,
			Direction:    direction,
			Protocol:     protocol,
			PortRangeMin: fromPort,
			PortRangeMax: toPort,
			RemoteIPCIDR: aws.ToString(cidr.CidrIp),
			Description:  aws.ToString(cidr.Description),
		})
	}

	// Convert IPv6 CIDR blocks
	for _, cidr := range perm.Ipv6Ranges {
		ruleID := m.generateRuleID(direction, protocol, fromPort, toPort, aws.ToString(cidr.CidrIpv6))
		rules = append(rules, &cpi.SecurityRule{
			ID:           ruleID,
			Direction:    direction,
			Protocol:     protocol,
			PortRangeMin: fromPort,
			PortRangeMax: toPort,
			RemoteIPCIDR: aws.ToString(cidr.CidrIpv6),
			Description:  aws.ToString(cidr.Description),
		})
	}

	// Convert security group references
	for _, groupPair := range perm.UserIdGroupPairs {
		ruleID := m.generateRuleID(direction, protocol, fromPort, toPort, aws.ToString(groupPair.GroupId))
		rules = append(rules, &cpi.SecurityRule{
			ID:           ruleID,
			Direction:    direction,
			Protocol:     protocol,
			PortRangeMin: fromPort,
			PortRangeMax: toPort,
			RemoteGroup:  aws.ToString(groupPair.GroupId),
			Description:  aws.ToString(groupPair.Description),
		})
	}

	return rules
}

// ruleToIPPermission converts a CPI security rule to an AWS IP permission.
func (m *SecurityManager) ruleToIPPermission(rule *cpi.SecurityRule) (*types.IpPermission, error) {
	ipPerm := &types.IpPermission{}

	m.setProtocol(ipPerm, rule)
	m.setPorts(ipPerm, rule)

	err := m.setRemote(ipPerm, rule)
	if err != nil {
		return nil, err
	}

	return ipPerm, nil
}

// validateSecurityRule validates a security rule.
func (m *SecurityManager) validateSecurityRule(rule *cpi.SecurityRule) error {
	err := m.validateDirection(rule.Direction)
	if err != nil {
		return err
	}

	err = m.validateProtocol(rule.Protocol)
	if err != nil {
		return err
	}

	err = m.validatePorts(rule)
	if err != nil {
		return err
	}

	err = m.validateRemote(rule)
	if err != nil {
		return err
	}

	return nil
}

func (m *SecurityManager) prepareTags(reqTags map[string]string) []types.Tag {
	return buildResourceTags(reqTags)
}

func (m *SecurityManager) buildCreateInput(req *cpi.CreateSecurityGroupRequest, tags []types.Tag) *ec2.CreateSecurityGroupInput {
	description := req.Description
	if description == "" {
		description = "Security group created by OCFP"
	}

	return &ec2.CreateSecurityGroupInput{
		GroupName:   aws.String(req.Name),
		Description: aws.String(description),
		VpcId:       aws.String(req.NetworkID),
		TagSpecifications: []types.TagSpecification{
			{
				ResourceType: types.ResourceTypeSecurityGroup,
				Tags:         tags,
			},
		},
	}
}

func (m *SecurityManager) configureSecurityGroupRules(ctx context.Context, ec2Client EC2API, groupID string, rules []*cpi.SecurityRule) error {
	if len(rules) == 0 {
		return nil
	}

	describeResp, err := ec2Client.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{
		GroupIds: []string{groupID},
	})
	if err == nil && len(describeResp.SecurityGroups) > 0 {
		group := describeResp.SecurityGroups[0]
		if len(group.IpPermissionsEgress) > 0 {
			if _, revokeErr := ec2Client.RevokeSecurityGroupEgress(ctx, &ec2.RevokeSecurityGroupEgressInput{
				GroupId:       aws.String(groupID),
				IpPermissions: group.IpPermissionsEgress,
			}); revokeErr != nil {
				logger.WithOperation("CreateSecurityGroup").Warnf("revoke default egress on %s: %v", groupID, revokeErr)
			}
		}
	}

	var errs []error

	for _, rule := range rules {
		if addErr := m.AddSecurityRule(ctx, groupID, rule); addErr != nil {
			errs = append(errs, fmt.Errorf("add rule %s:%s: %w", rule.Direction, rule.Protocol, addErr))
		}
	}

	return errors.Join(errs...)
}

func (m *SecurityManager) validateDirection(direction string) error {
	dir := strings.ToLower(direction)
	switch dir {
	case directionIngress, directionInbound, directionEgress, directionOutbound:
		return nil
	default:
		return fmt.Errorf("invalid direction %q: must be ingress or egress: %w", direction, ErrInvalidRequest)
	}
}

func (m *SecurityManager) validateProtocol(protocol string) error {
	proto := strings.ToLower(protocol)
	validProtocols := map[string]bool{
		"tcp": true, "udp": true, "icmp": true, "icmpv6": true, protocolAll: true, "": true,
	}

	if validProtocols[proto] {
		return nil
	}

	num, err := strconv.Atoi(proto)
	if err != nil || num < 0 || num > 255 {
		return fmt.Errorf("invalid protocol %q: must be tcp, udp, icmp, icmpv6, all, or a number 0-255: %w", protocol, ErrInvalidRequest)
	}

	return nil
}

func (m *SecurityManager) validatePorts(rule *cpi.SecurityRule) error {
	if rule.PortRangeMin < 0 || rule.PortRangeMin > 65535 {
		return fmt.Errorf("invalid port range min %d: must be 0-65535: %w", rule.PortRangeMin, ErrInvalidRequest)
	}

	if rule.PortRangeMax < 0 || rule.PortRangeMax > 65535 {
		return fmt.Errorf("invalid port range max %d: must be 0-65535: %w", rule.PortRangeMax, ErrInvalidRequest)
	}

	if rule.PortRangeMin > 0 && rule.PortRangeMax > 0 && rule.PortRangeMin > rule.PortRangeMax {
		return fmt.Errorf("invalid port range: min %d > max %d: %w", rule.PortRangeMin, rule.PortRangeMax, ErrInvalidRequest)
	}

	return nil
}

func (m *SecurityManager) validateRemote(rule *cpi.SecurityRule) error {
	if rule.RemoteIPCIDR == "" && rule.RemoteGroup == "" {
		return fmt.Errorf("rule must specify either RemoteIPCIDR or RemoteGroup: %w", ErrInvalidRequest)
	}

	if rule.RemoteIPCIDR != "" && rule.RemoteGroup != "" {
		return fmt.Errorf("rule cannot specify both RemoteIPCIDR and RemoteGroup: %w", ErrInvalidRequest)
	}

	if rule.RemoteIPCIDR != "" {
		_, _, err := net.ParseCIDR(rule.RemoteIPCIDR)
		if err != nil {
			return fmt.Errorf("invalid CIDR %q: %w: %w", rule.RemoteIPCIDR, err, ErrInvalidRequest)
		}
	}

	if rule.RemoteGroup != "" && !strings.HasPrefix(rule.RemoteGroup, "sg-") {
		return fmt.Errorf("invalid security group ID %q: must start with sg-: %w", rule.RemoteGroup, ErrInvalidRequest)
	}

	return nil
}

func (m *SecurityManager) setProtocol(ipPerm *types.IpPermission, rule *cpi.SecurityRule) {
	protocol := strings.ToLower(rule.Protocol)
	if protocol == protocolAll || protocol == "" {
		ipPerm.IpProtocol = aws.String("-1")
	} else {
		ipPerm.IpProtocol = aws.String(protocol)
	}
}

func (m *SecurityManager) setPorts(ipPerm *types.IpPermission, rule *cpi.SecurityRule) {
	protocol := strings.ToLower(rule.Protocol)
	if protocol == "icmp" || protocol == "icmpv6" || protocol == protocolAll || protocol == "" {
		return
	}

	if rule.PortRangeMin > 0 && rule.PortRangeMin <= maxTCPUDPPort {
		ipPerm.FromPort = aws.Int32(int32(rule.PortRangeMin))
	}

	if rule.PortRangeMax > 0 && rule.PortRangeMax <= maxTCPUDPPort {
		ipPerm.ToPort = aws.Int32(int32(rule.PortRangeMax))
	}

	if rule.PortRangeMin > 0 && rule.PortRangeMax == 0 {
		ipPerm.ToPort = ipPerm.FromPort
	}

	if rule.PortRangeMax > 0 && rule.PortRangeMin == 0 {
		ipPerm.FromPort = ipPerm.ToPort
	}
}

func (m *SecurityManager) setRemote(ipPerm *types.IpPermission, rule *cpi.SecurityRule) error {
	switch {
	case rule.RemoteIPCIDR != "":
		if strings.Contains(rule.RemoteIPCIDR, ":") {
			ipPerm.Ipv6Ranges = []types.Ipv6Range{{
				CidrIpv6:    aws.String(rule.RemoteIPCIDR),
				Description: aws.String(rule.Description),
			}}
		} else {
			ipPerm.IpRanges = []types.IpRange{{
				CidrIp:      aws.String(rule.RemoteIPCIDR),
				Description: aws.String(rule.Description),
			}}
		}
	case rule.RemoteGroup != "":
		ipPerm.UserIdGroupPairs = []types.UserIdGroupPair{{
			GroupId:     aws.String(rule.RemoteGroup),
			Description: aws.String(rule.Description),
		}}
	default:
		return fmt.Errorf("rule must specify either RemoteIPCIDR or RemoteGroup: %w", ErrInvalidRequest)
	}

	return nil
}

// generateRuleID generates a unique ID for a security rule based on its properties.
func (m *SecurityManager) generateRuleID(direction, protocol string, fromPort, toPort int, remote string) string {
	return fmt.Sprintf("%s:%s:%d:%d:%s", direction, protocol, fromPort, toPort, remote)
}

// ruleExists checks if a security rule already exists in a security group.
func (m *SecurityManager) ruleExists(ctx context.Context, ec2Client EC2API, groupID string, rule *cpi.SecurityRule) (bool, error) {
	resp, err := ec2Client.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{
		GroupIds: []string{groupID},
	})
	if err != nil {
		return false, wrapError(err, "describe security group")
	}

	if len(resp.SecurityGroups) == 0 {
		return false, ErrSecurityGroupNotFound
	}

	secGroup := resp.SecurityGroups[0]

	// Convert rule to target IPPermission
	targetPerm, err := m.ruleToIPPermission(rule)
	if err != nil {
		return false, err
	}

	// Check existing rules based on direction
	var existingPerms []types.IpPermission

	switch strings.ToLower(rule.Direction) {
	case directionIngress, directionInbound:
		existingPerms = secGroup.IpPermissions
	case directionEgress, directionOutbound:
		existingPerms = secGroup.IpPermissionsEgress
	default:
		return false, fmt.Errorf("invalid direction %q: %w", rule.Direction, ErrInvalidRequest)
	}

	// Check if rule matches any existing permission
	for _, existingPerm := range existingPerms {
		if m.ipPermissionsMatch(targetPerm, &existingPerm) {
			return true, nil
		}
	}

	return false, nil
}

// ipPermissionsMatch checks if two IP permissions are equivalent.
func (m *SecurityManager) ipPermissionsMatch(perm1, perm2 *types.IpPermission) bool {
	// Check protocol
	if aws.ToString(perm1.IpProtocol) != aws.ToString(perm2.IpProtocol) {
		return false
	}

	// Check ports
	if aws.ToInt32(perm1.FromPort) != aws.ToInt32(perm2.FromPort) {
		return false
	}

	if aws.ToInt32(perm1.ToPort) != aws.ToInt32(perm2.ToPort) {
		return false
	}

	// Check IP ranges
	if !m.ipRangesMatch(perm1.IpRanges, perm2.IpRanges) {
		return false
	}

	// Check IPv6 ranges
	if !m.ipv6RangesMatch(perm1.Ipv6Ranges, perm2.Ipv6Ranges) {
		return false
	}

	// Check security group references
	return m.userIDGroupPairsMatch(perm1.UserIdGroupPairs, perm2.UserIdGroupPairs)
}

// ipRangesMatch checks if IP ranges match.
func (m *SecurityManager) ipRangesMatch(ranges1, ranges2 []types.IpRange) bool {
	if len(ranges1) != len(ranges2) {
		return false
	}

	for _, r1 := range ranges1 {
		found := false

		for _, r2 := range ranges2 {
			if aws.ToString(r1.CidrIp) == aws.ToString(r2.CidrIp) {
				found = true

				break
			}
		}

		if !found {
			return false
		}
	}

	return true
}

// ipv6RangesMatch checks if IPv6 ranges match.
func (m *SecurityManager) ipv6RangesMatch(ranges1, ranges2 []types.Ipv6Range) bool {
	if len(ranges1) != len(ranges2) {
		return false
	}

	for _, r1 := range ranges1 {
		found := false

		for _, r2 := range ranges2 {
			if aws.ToString(r1.CidrIpv6) == aws.ToString(r2.CidrIpv6) {
				found = true

				break
			}
		}

		if !found {
			return false
		}
	}

	return true
}

// userIDGroupPairsMatch checks if user ID group pairs match.
func (m *SecurityManager) userIDGroupPairsMatch(pairs1, pairs2 []types.UserIdGroupPair) bool {
	if len(pairs1) != len(pairs2) {
		return false
	}

	for _, p1 := range pairs1 {
		found := false

		for _, p2 := range pairs2 {
			if aws.ToString(p1.GroupId) == aws.ToString(p2.GroupId) {
				found = true

				break
			}
		}

		if !found {
			return false
		}
	}

	return true
}
