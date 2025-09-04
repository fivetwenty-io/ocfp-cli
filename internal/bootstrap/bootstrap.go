package bootstrap

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/state"
	"github.com/olekukonko/tablewriter"
)

// Options represents bootstrap options
type Options struct {
	BlocName string
	Provider string
	Region   string
	Force    bool
	DryRun   bool
	Timeout  time.Duration
}

// Manager handles the bootstrap process
type Manager struct {
	config       *config.Config
	provider     cpi.Provider
	stateManager *state.Manager
	options      *Options
}

// NewManager creates a new bootstrap manager
func NewManager(cfg *config.Config, provider cpi.Provider, stateManager *state.Manager, opts *Options) *Manager {
	return &Manager{
		config:       cfg,
		provider:     provider,
		stateManager: stateManager,
		options:      opts,
	}
}

// Execute runs the bootstrap process
func (m *Manager) Execute(ctx context.Context) error {
	logger.Infof("Starting bootstrap for bloc: %s", m.options.BlocName)

	// Load or create state
	if _, err := m.stateManager.Load(m.options.BlocName); err != nil {
		return fmt.Errorf("failed to load state: %w", err)
	}

	// Acquire state lock
	if err := m.stateManager.Lock(m.options.BlocName); err != nil {
		return fmt.Errorf("failed to acquire state lock: %w", err)
	}
	defer func() {
		if err := m.stateManager.Unlock(m.options.BlocName); err != nil {
			logger.Warnf("Failed to unlock state: %v", err)
		}
	}()

	// Execute bootstrap phases
	phases := []struct {
		name string
		fn   func(context.Context) error
	}{
		{"network", m.createNetwork},
		{"subnets", m.createSubnets},
		{"security_groups", m.createSecurityGroups},
		{"public_ips", m.createPublicIPs},
		{"object_storage", m.createBuckets},
		{"keypair", m.createKeyPair},
		{"volumes", m.createVolumes},
		{"bastion", m.createBastion},
	}

	for _, phase := range phases {
		logger.Infof("Executing phase: %s", phase.name)

		if m.options.DryRun {
			logger.Infof("[DRY RUN] Would execute: %s", phase.name)
			continue
		}

		if err := phase.fn(ctx); err != nil {
			return fmt.Errorf("phase %s failed: %w", phase.name, err)
		}

		// Save state after each phase
		if err := m.stateManager.Save(); err != nil {
			logger.Warnf("Failed to save state after phase %s: %v", phase.name, err)
		}
	}

	logger.Infof("Bootstrap completed successfully for bloc: %s", m.options.BlocName)
	return nil
}

// baseTags returns standard tags/labels to attach to resources
func (m *Manager) baseTags() map[string]string {
	tags := map[string]string{
		"managed-by": "ocfp",
		"bloc":       m.options.BlocName,
	}
	if m.config.Environment != "" {
		tags["environment"] = m.config.Environment
	}
	// Mark management bloc resources with env=mgmt for parity with Perl
	if strings.EqualFold(m.config.Type, "management") || strings.EqualFold(m.options.BlocName, "mgmt") || strings.Contains(strings.ToLower(m.config.Name), "mgmt") {
		tags["env"] = "mgmt"
	}
	return tags
}

// createNetwork creates the VPC/network
func (m *Manager) createNetwork(ctx context.Context) error {
	logger.Info("Creating network...")

	// Check if network already exists in state
	existingNetwork, _ := m.stateManager.GetResource("network", m.config.Name)
	if existingNetwork != nil && existingNetwork.State == string(cpi.ResourceStateActive) {
		logger.Info("Network already exists, skipping creation")
		return nil
	}

	// Create network
	ntags := m.baseTags()
	ntags["type"] = "infra"
	network, err := m.provider.Network().CreateNetwork(ctx, &cpi.CreateNetworkRequest{
		Name:       fmt.Sprintf("%s-net", m.config.Name),
		CIDR:       m.config.Network.NetworkCIDR,
		DNSServers: m.config.DNS,
		Tags:       ntags,
	})
	if err != nil {
		return fmt.Errorf("failed to create network: %w", err)
	}

	// Save to state
	if err := m.stateManager.AddResource(&state.Resource{
		ID:       network.ID,
		Type:     "network",
		Name:     m.config.Name,
		Provider: m.options.Provider,
		State:    string(network.State),
		Properties: map[string]interface{}{
			"cidr":        network.CIDR,
			"dns_servers": network.DNSServers,
		},
		Tags: network.Tags,
	}); err != nil {
		return fmt.Errorf("failed to save network to state: %w", err)
	}

	// Save network ID as output
	if err := m.stateManager.SetOutput("network_id", network.ID); err != nil {
		logger.Warnf("Failed to set network_id output: %v", err)
	}

	logger.Infof("Network created: %s (%s)", network.Name, network.ID)
	return nil
}

// createSubnets creates the subnets
func (m *Manager) createSubnets(ctx context.Context) error {
	logger.Info("Creating subnets...")

	// Get network ID from state
	networkID, err := m.stateManager.GetOutput("network_id")
	if err != nil {
		return fmt.Errorf("network ID not found in state: %w", err)
	}

	// STACKIT: Do not create provider subnets. Use virtual subnets according to strategy.
	if strings.EqualFold(m.options.Provider, "stackit") {
		// Resolve network CIDR
		cidr := m.config.Network.CIDR
		if cidr == "" {
			cidr = m.config.Network.NetworkCIDR
		}
		if cidr == "" {
			cidr = "10.4.0.0/20"
		}

		// Choose virtual strategy
		strat := strings.ToLower(strings.TrimSpace(m.config.SubnetStrategy))
		if strat == "ocfp-triple" {
			// Split parent into 4 equal children, take indices 1..3 (skip first)
			children := splitIntoN(cidr, 4)
			if len(children) < 4 {
				// Fallback: derive three /24s starting after first child24 block
				c0 := firstChild24(cidr)
				c1 := nextSibling24(c0)
				c2 := nextSibling24(c1)
				c3 := nextSibling24(c2)
				children = []string{c0, c1, c2, c3}
			}
			// Use 1..3
			triples := []string{children[1], children[2], children[3]}
			for i, sCIDR := range triples {
				vname := fmt.Sprintf("%s-ocfp-%d", m.config.Name, i)
				if err := m.addVirtualSubnetToState(vname, sCIDR, cidr, networkID); err != nil {
					return err
				}
				if err := m.stateManager.AddDependency(fmt.Sprintf("subnet.%s", vname), "network."+m.config.Name); err != nil {
					logger.Warnf("Failed to add dependency for subnet %s: %v", vname, err)
				}
			}
			// Save parent network CIDR once
			_ = m.stateManager.SetOutput("network_cidr", cidr)
			return nil
		}

		// Default: single virtual ocfp-0 equal to parent
		subnetName := fmt.Sprintf("%s-%s", m.config.Name, "ocfp-0")
		if err := m.addVirtualSubnetToState(subnetName, cidr, cidr, networkID); err != nil {
			return err
		}
		if err := m.stateManager.AddDependency(fmt.Sprintf("subnet.%s", subnetName), "network."+m.config.Name); err != nil {
			logger.Warnf("Failed to add dependency for subnet %s: %v", subnetName, err)
		}
		_ = m.stateManager.SetOutput("network_cidr", cidr)
		return nil
	}

	subnets := m.config.Subnets
	if len(subnets) == 0 {
		// Derive mgmt and ocf subnets from the bloc network range
		parent := m.config.Network.CIDR
		if parent == "" {
			parent = m.config.Network.NetworkCIDR
		}
		if parent == "" {
			parent = "10.4.0.0/20"
		}
		mgmtCIDR, ocfCIDR := splitParentIntoTwo(parent)
		if mgmtCIDR == "" || ocfCIDR == "" {
			// Fallback: if parsing failed, use first /24s
			mgmtCIDR = firstChild24(parent)
			ocfCIDR = nextSibling24(mgmtCIDR)
		}
		subnets = []config.Subnet{
			{Name: "mgmt", CIDR: mgmtCIDR, Type: "public"},
			{Name: "ocf", CIDR: ocfCIDR, Type: "private"},
		}
		logger.Infof("No subnets defined in config. Using defaults: mgmt=%s, ocf=%s", mgmtCIDR, ocfCIDR)
		// Persist calculated CIDRs to state outputs for visibility
		_ = m.stateManager.SetOutput("network_cidr", parent)
		_ = m.stateManager.SetOutput(fmt.Sprintf("subnet_%s-%s_cidr", m.config.Name, "mgmt"), mgmtCIDR)
		_ = m.stateManager.SetOutput(fmt.Sprintf("subnet_%s-%s_cidr", m.config.Name, "ocf"), ocfCIDR)
	}

	// Create subnets for the current bloc
	for _, subnet := range subnets {
		subnetName := fmt.Sprintf("%s-%s", m.config.Name, subnet.Name)

		// Check if subnet already exists
		existingSubnet, _ := m.stateManager.GetResource("subnet", subnetName)
		if existingSubnet != nil && existingSubnet.State == string(cpi.ResourceStateActive) {
			logger.Infof("Subnet %s already exists, skipping", subnetName)
			continue
		}

		// Create subnet
		stags := m.baseTags()
		if m.config.Environment != "" {
			stags["environment"] = m.config.Environment
		}
		createdSubnet, err := m.provider.Network().CreateSubnet(ctx, &cpi.CreateSubnetRequest{
			Name:             subnetName,
			NetworkID:        networkID.(string),
			CIDR:             subnet.CIDR,
			AvailabilityZone: subnet.AvailabilityZone,
			Type:             subnet.Type,
			Tags:             stags,
		})
		if err != nil {
			return fmt.Errorf("failed to create subnet %s: %w", subnetName, err)
		}

		// Save to state
		if err := m.stateManager.AddResource(&state.Resource{
			ID:       createdSubnet.ID,
			Type:     "subnet",
			Name:     subnetName,
			Provider: m.options.Provider,
			State:    string(createdSubnet.State),
			Properties: map[string]interface{}{
				"cidr":              createdSubnet.CIDR,
				"availability_zone": createdSubnet.AvailabilityZone,
				"network_id":        networkID,
				"type":              createdSubnet.Type,
			},
			Tags: createdSubnet.Tags,
		}); err != nil {
			return fmt.Errorf("failed to save subnet to state: %w", err)
		}

		// Add dependency
		if err := m.stateManager.AddDependency(fmt.Sprintf("subnet.%s", subnetName), "network."+m.config.Name); err != nil {
			logger.Warnf("Failed to add dependency for subnet %s: %v", subnetName, err)
		}

		// Save subnet ID as output
		if err := m.stateManager.SetOutput(fmt.Sprintf("subnet_%s_id", subnetName), createdSubnet.ID); err != nil {
			logger.Warnf("Failed to set subnet_%s_id output: %v", subnetName, err)
		}
		// Save subnet CIDR as output
		if err := m.stateManager.SetOutput(fmt.Sprintf("subnet_%s_cidr", subnetName), createdSubnet.CIDR); err != nil {
			logger.Warnf("Failed to set subnet_%s_cidr output: %v", subnetName, err)
		}

		logger.Infof("Subnet created: %s (%s)", subnetName, createdSubnet.ID)
	}

	return nil
}

// firstChild24 returns a /24 from the provided network CIDR
func firstChild24(parentCIDR string) string {
	_, ipnet, err := net.ParseCIDR(parentCIDR)
	if err != nil || ipnet == nil {
		return ""
	}
	// If parent is already /24 or narrower, just return it
	ones, _ := ipnet.Mask.Size()
	if ones >= 24 {
		return ipnet.String()
	}
	// Construct a /24 starting at network base
	mask := net.CIDRMask(24, 32)
	base := ipnet.IP.Mask(ipnet.Mask)
	return (&net.IPNet{IP: base, Mask: mask}).String()
}

// nextSibling24 returns the next /24 network after the given /24 CIDR
func nextSibling24(child24 string) string {
	_, ipnet, err := net.ParseCIDR(child24)
	if err != nil || ipnet == nil {
		return ""
	}
	ones, bits := ipnet.Mask.Size()
	if ones != 24 || bits != 32 {
		return ""
	}
	base := ipToUint32(ipnet.IP.Mask(ipnet.Mask))
	next := base + 256 // increment by size of /24
	return (&net.IPNet{IP: uint32ToIP(next), Mask: net.CIDRMask(24, 32)}).String()
}

// splitParentIntoTwo splits a parent IPv4 CIDR into two equal subnets within the same range.
// If parent is /23, returns two /24s; if /24, returns two /25s; in general returns two subnets at prefix+1.
func splitParentIntoTwo(parentCIDR string) (string, string) {
	_, parent, err := net.ParseCIDR(parentCIDR)
	if err != nil || parent == nil {
		return "", ""
	}
	ones, bits := parent.Mask.Size()
	if bits != 32 {
		return "", ""
	}
	if ones >= 32 {
		return parent.String(), ""
	}
	nextPrefix := ones + 1
	// base of parent
	base := ipToUint32(parent.IP.Mask(parent.Mask))
	// size of each child block
	childSize := uint32(1) << uint32(32-nextPrefix)
	first := (&net.IPNet{IP: uint32ToIP(base), Mask: net.CIDRMask(nextPrefix, 32)}).String()
	second := (&net.IPNet{IP: uint32ToIP(base + childSize), Mask: net.CIDRMask(nextPrefix, 32)}).String()
	return first, second
}

func ipToUint32(ip net.IP) uint32 {
	v := ip.To4()
	if v == nil {
		return 0
	}
	return uint32(v[0])<<24 | uint32(v[1])<<16 | uint32(v[2])<<8 | uint32(v[3])
}

func uint32ToIP(n uint32) net.IP {
	return net.IPv4(byte(n>>24), byte((n>>16)&0xFF), byte((n>>8)&0xFF), byte(n&0xFF))
}

// createSecurityGroups creates security groups with default rules
func (m *Manager) createSecurityGroups(ctx context.Context) error {
	logger.Info("Creating security groups...")

	// Get network ID
	networkID, err := m.stateManager.GetOutput("network_id")
	if err != nil {
		return fmt.Errorf("network ID not found in state: %w", err)
	}

	// Security groups modeled after Perl implementation
	// Names are prefixed with bloc name below
	securityGroups := []struct {
		name        string
		description string
		rules       []*cpi.SecurityRule
	}{
		{
			name:        "bastion",
			description: "Security group for bastion host",
			rules: []*cpi.SecurityRule{
				{Direction: "ingress", Protocol: "tcp", PortRangeMin: 22, PortRangeMax: 22, RemoteIPCIDR: "0.0.0.0/0", Description: "SSH"},
				{Direction: "egress", Protocol: "all", RemoteIPCIDR: "0.0.0.0/0", Description: "Allow all outbound"},
			},
		},
		{
			name:        "infra",
			description: "Security group for STACKIT infrastructure",
			rules: []*cpi.SecurityRule{
				{Direction: "ingress", Protocol: "tcp", PortRangeMin: 22, PortRangeMax: 22, Description: "SSH"},
				{Direction: "ingress", Protocol: "tcp", PortRangeMin: 80, PortRangeMax: 80, Description: "HTTP"},
				{Direction: "ingress", Protocol: "tcp", PortRangeMin: 443, PortRangeMax: 443, Description: "HTTPS"},
				{Direction: "ingress", Protocol: "tcp", PortRangeMin: 8080, PortRangeMax: 8080, Description: "HTTP-ALT"},
				{Direction: "ingress", Protocol: "tcp", PortRangeMin: 8443, PortRangeMax: 8443, Description: "HTTPS-ALT"},
				{Direction: "egress", Protocol: "all", RemoteIPCIDR: "0.0.0.0/0", Description: "Allow all outbound"},
			},
		},
		{
			name:        "ocfp",
			description: "Security group for OCFP services",
			rules: []*cpi.SecurityRule{
				{Direction: "ingress", Protocol: "tcp", PortRangeMin: 22, PortRangeMax: 22, Description: "SSH"},
				{Direction: "ingress", Protocol: "tcp", PortRangeMin: 80, PortRangeMax: 80, Description: "HTTP"},
				{Direction: "ingress", Protocol: "tcp", PortRangeMin: 443, PortRangeMax: 443, Description: "HTTPS"},
				{Direction: "ingress", Protocol: "tcp", PortRangeMin: 25555, PortRangeMax: 25555, Description: "BOSH Director"},
				{Direction: "ingress", Protocol: "tcp", PortRangeMin: 6868, PortRangeMax: 6868, Description: "BOSH Agent"},
				{Direction: "egress", Protocol: "all", RemoteIPCIDR: "0.0.0.0/0", Description: "Allow all outbound"},
			},
		},
		{
			name:        "lb-ext",
			description: "Security group for external load balancers",
			rules: []*cpi.SecurityRule{
				{Direction: "ingress", Protocol: "tcp", PortRangeMin: 443, PortRangeMax: 443, RemoteIPCIDR: "0.0.0.0/0", Description: "HTTPS external"},
				{Direction: "egress", Protocol: "all", RemoteIPCIDR: "0.0.0.0/0", Description: "Allow all outbound"},
			},
		},
		{
			name:        "ocf-cf-router-ingress",
			description: "Security group for Cloud Foundry router ingress",
			rules: []*cpi.SecurityRule{
				{Direction: "ingress", Protocol: "tcp", PortRangeMin: 80, PortRangeMax: 80, RemoteIPCIDR: "0.0.0.0/0", Description: "CF Router HTTP"},
				{Direction: "ingress", Protocol: "tcp", PortRangeMin: 443, PortRangeMax: 443, RemoteIPCIDR: "0.0.0.0/0", Description: "CF Router HTTPS"},
				{Direction: "ingress", Protocol: "tcp", PortRangeMin: 2222, PortRangeMax: 2222, RemoteIPCIDR: "0.0.0.0/0", Description: "CF SSH"},
				{Direction: "egress", Protocol: "all", RemoteIPCIDR: "0.0.0.0/0", Description: "Allow all outbound"},
			},
		},
		{
			name:        "ocf-cf-tcp-router-ingress",
			description: "Security group for Cloud Foundry TCP router ingress",
			rules: []*cpi.SecurityRule{
				{Direction: "ingress", Protocol: "tcp", PortRangeMin: 1024, PortRangeMax: 65535, RemoteIPCIDR: "0.0.0.0/0", Description: "CF TCP Router"},
				{Direction: "egress", Protocol: "all", RemoteIPCIDR: "0.0.0.0/0", Description: "Allow all outbound"},
			},
		},
		{
			name:        "ocf-cf-ssh-ingress",
			description: "Security group for Cloud Foundry SSH proxy ingress",
			rules: []*cpi.SecurityRule{
				{Direction: "ingress", Protocol: "tcp", PortRangeMin: 2222, PortRangeMax: 2222, RemoteIPCIDR: "0.0.0.0/0", Description: "CF SSH Proxy"},
				{Direction: "egress", Protocol: "all", RemoteIPCIDR: "0.0.0.0/0", Description: "Allow all outbound"},
			},
		},
	}

	for _, sg := range securityGroups {
		sgName := fmt.Sprintf("%s-%s", m.config.Name, sg.name)

		// Check if already exists
		existingSG, _ := m.stateManager.GetResource("security_group", sgName)
		if existingSG != nil {
			logger.Infof("Security group %s already exists, skipping", sgName)
			continue
		}

		// Create security group
		createdSG, err := m.provider.Security().CreateSecurityGroup(ctx, &cpi.CreateSecurityGroupRequest{
			Name:        sgName,
			Description: sg.description,
			NetworkID:   networkID.(string),
			Rules:       sg.rules,
			Tags:        m.baseTags(),
		})
		if err != nil {
			return fmt.Errorf("failed to create security group %s: %w", sgName, err)
		}

		// Save to state
		if err := m.stateManager.AddResource(&state.Resource{
			ID:       createdSG.ID,
			Type:     "security_group",
			Name:     sgName,
			Provider: m.options.Provider,
			State:    "active",
			Properties: map[string]interface{}{
				"network_id": networkID,
				"rules":      len(createdSG.Rules),
			},
			Tags: createdSG.Tags,
		}); err != nil {
			return fmt.Errorf("failed to save security group to state: %w", err)
		}

		_ = m.stateManager.SetOutput(fmt.Sprintf("sg_%s_id", sg.name), createdSG.ID)
		logger.Infof("Security group created: %s", sgName)
	}

	return nil
}

// createPublicIPs creates public IPs for various services
func (m *Manager) createPublicIPs(ctx context.Context) error {
	logger.Info("Creating public IPs...")

	// Check if provider is STACKIT (only STACKIT supports public IPs currently)
	if m.options.Provider != "stackit" {
		logger.Info("Provider does not support public IP management, skipping")
		return nil
	}

	// Get network manager
	netMgr := m.provider.Network()
	if netMgr == nil {
		logger.Warn("Network manager not available; skipping public IPs")
		return nil
	}

	// We need STACKIT-specific Ensure methods not in the generic interface
	// Use a type assertion to the STACKIT network manager
	type stackitEnsure interface {
		EnsureJumpboxPublicIPs(ctx context.Context, blocName string, count int) ([]*cpi.PublicIP, error)
		// Optional additional ensures can be added later (router, cf-ssh, tcp-router)
		ListPublicIPs(ctx context.Context, filters map[string]string) ([]*cpi.PublicIP, error)
		CreatePublicIP(ctx context.Context, req *cpi.CreatePublicIPRequest) (*cpi.PublicIP, error)
	}

	s, ok := netMgr.(stackitEnsure)
	if !ok {
		logger.Warn("STACKIT-specific network features not available; skipping public IP creation")
		return nil
	}

	// Collect ensured IPs for summary output
	var allIPs []*cpi.PublicIP

	// Ops IP (single) for general operational access
	{
		type stackitOpsEnsure interface {
			EnsureOpsPublicIPs(ctx context.Context, blocName string, count int) ([]*cpi.PublicIP, error)
		}
		if so, ok := netMgr.(stackitOpsEnsure); ok {
			if opsIPs, err := so.EnsureOpsPublicIPs(ctx, m.config.Name, 1); err != nil {
				logger.Warnf("Failed ensuring ops public IP: %v", err)
			} else {
				for _, ip := range opsIPs {
					resName := fmt.Sprintf("%s-ops-%s", m.config.Name, ip.Labels["index"]) // usually index 0
					if err := m.stateManager.AddResource(&state.Resource{
						ID:       ip.ID,
						Type:     "public_ip",
						Name:     resName,
						Provider: m.options.Provider,
						State:    "active",
						Properties: map[string]interface{}{
							"job":       "ops",
							"index":     ip.Labels["index"],
							"address":   ip.Address,
							"networkId": ip.NetworkID,
						},
						Tags: ip.Labels,
					}); err != nil {
						logger.Warnf("Failed to save ops IP %s to state: %v", ip.ID, err)
					}
				}
				logger.Infof("Ops public IP ensured: %d", len(opsIPs))
				allIPs = append(allIPs, opsIPs...)
			}
		}
	}

	// Jumpbox IPs (default 2)
	jumpboxIPCount := m.config.JumpboxPublicIPs
	if jumpboxIPCount <= 0 {
		jumpboxIPCount = 2
	}
	logger.Infof("Ensuring %d jumpbox public IPs for bloc %s", jumpboxIPCount, m.config.Name)
	jumpboxIPs, err := s.EnsureJumpboxPublicIPs(ctx, m.config.Name, jumpboxIPCount)
	if err != nil {
		return fmt.Errorf("failed ensuring jumpbox public IPs: %w", err)
	}

	// Persist jumpbox IPs to state
	for _, ip := range jumpboxIPs {
		resName := fmt.Sprintf("%s-jumpbox-%s", m.config.Name, ip.Labels["index"])
		if err := m.stateManager.AddResource(&state.Resource{
			ID:       ip.ID,
			Type:     "public_ip",
			Name:     resName,
			Provider: m.options.Provider,
			State:    "active",
			Properties: map[string]interface{}{
				"job":       "jumpbox",
				"index":     ip.Labels["index"],
				"address":   ip.Address,
				"networkId": ip.NetworkID,
			},
			Tags: ip.Labels,
		}); err != nil {
			logger.Warnf("Failed to save jumpbox IP %s to state: %v", ip.ID, err)
		}
	}

	logger.Infof("Jumpbox public IPs ensured: %d", len(jumpboxIPs))
	allIPs = append(allIPs, jumpboxIPs...)

	// Router IPs (default 4)
	routerCount := m.config.RouterPublicIPs
	if routerCount <= 0 {
		routerCount = 4
	}
	if routerCount > 0 {
		type stackitRouterEnsure interface {
			EnsureRouterPublicIPs(ctx context.Context, blocName string, count int) ([]*cpi.PublicIP, error)
		}
		if sr, ok := netMgr.(stackitRouterEnsure); ok {
			if routerIPs, err := sr.EnsureRouterPublicIPs(ctx, m.config.Name, routerCount); err != nil {
				logger.Warnf("Failed ensuring router public IPs: %v", err)
			} else {
				for _, ip := range routerIPs {
					resName := fmt.Sprintf("%s-router-%s", m.config.Name, ip.Labels["index"])
					if err := m.stateManager.AddResource(&state.Resource{
						ID:       ip.ID,
						Type:     "public_ip",
						Name:     resName,
						Provider: m.options.Provider,
						State:    "active",
						Properties: map[string]interface{}{
							"job":       "router",
							"index":     ip.Labels["index"],
							"address":   ip.Address,
							"networkId": ip.NetworkID,
						},
						Tags: ip.Labels,
					}); err != nil {
						logger.Warnf("Failed to save router IP %s to state: %v", ip.ID, err)
					}
				}
				logger.Infof("Router public IPs ensured: %d", len(routerIPs))
				allIPs = append(allIPs, routerIPs...)
			}
		}
	}

	// CF SSH IPs (default 1)
	cfsshCount := m.config.CFSSHPublicIPs
	if cfsshCount <= 0 {
		cfsshCount = 1
	}
	if cfsshCount > 0 {
		type stackitCFEnsure interface {
			EnsureCFSSHPublicIPs(ctx context.Context, blocName string, count int) ([]*cpi.PublicIP, error)
		}
		if sc, ok := netMgr.(stackitCFEnsure); ok {
			if cfIPs, err := sc.EnsureCFSSHPublicIPs(ctx, m.config.Name, cfsshCount); err != nil {
				logger.Warnf("Failed ensuring cf-ssh public IPs: %v", err)
			} else {
				for _, ip := range cfIPs {
					resName := fmt.Sprintf("%s-cf-ssh-%s", m.config.Name, ip.Labels["index"])
					if err := m.stateManager.AddResource(&state.Resource{
						ID:       ip.ID,
						Type:     "public_ip",
						Name:     resName,
						Provider: m.options.Provider,
						State:    "active",
						Properties: map[string]interface{}{
							"job":       "cf-ssh",
							"index":     ip.Labels["index"],
							"address":   ip.Address,
							"networkId": ip.NetworkID,
						},
						Tags: ip.Labels,
					}); err != nil {
						logger.Warnf("Failed to save cf-ssh IP %s to state: %v", ip.ID, err)
					}
				}
				logger.Infof("CF-SSH public IPs ensured: %d", len(cfIPs))
				allIPs = append(allIPs, cfIPs...)
			}
		}
	}

	// TCP Router IPs (default 2)
	tcpRouterCount := m.config.TCPRouterPublicIPs
	if tcpRouterCount <= 0 {
		tcpRouterCount = 2
	}
	if tcpRouterCount > 0 {
		type stackitTCPEnsure interface {
			EnsureTCPRouterPublicIPs(ctx context.Context, blocName string, count int) ([]*cpi.PublicIP, error)
		}
		if st, ok := netMgr.(stackitTCPEnsure); ok {
			if tcpIPs, err := st.EnsureTCPRouterPublicIPs(ctx, m.config.Name, tcpRouterCount); err != nil {
				logger.Warnf("Failed ensuring tcp-router public IPs: %v", err)
			} else {
				for _, ip := range tcpIPs {
					resName := fmt.Sprintf("%s-tcp-router-%s", m.config.Name, ip.Labels["index"])
					if err := m.stateManager.AddResource(&state.Resource{
						ID:       ip.ID,
						Type:     "public_ip",
						Name:     resName,
						Provider: m.options.Provider,
						State:    "active",
						Properties: map[string]interface{}{
							"job":       "tcp-router",
							"index":     ip.Labels["index"],
							"address":   ip.Address,
							"networkId": ip.NetworkID,
						},
						Tags: ip.Labels,
					}); err != nil {
						logger.Warnf("Failed to save tcp-router IP %s to state: %v", ip.ID, err)
					}
				}
				logger.Infof("TCP Router public IPs ensured: %d", len(tcpIPs))
				allIPs = append(allIPs, tcpIPs...)
			}
		}
	}

	// Print a summary table
	if len(allIPs) > 0 {
		renderPublicIPsTable(allIPs)
	}
	return nil
}

// renderPublicIPsTable prints a table overview of ensured public IPs
func renderPublicIPsTable(ips []*cpi.PublicIP) {
	// Stable sort by job then numeric index
	sort.Slice(ips, func(i, j int) bool {
		if ips[i].Job == ips[j].Job {
			ii, ij := parseIndex(ips[i].Index), parseIndex(ips[j].Index)
			if ii == ij {
				return ips[i].Index < ips[j].Index
			}
			return ii < ij
		}
		return ips[i].Job < ips[j].Job
	})

	table := tablewriter.NewWriter(os.Stdout)
	table.SetHeader([]string{"JOB", "INDEX", "ADDRESS", "ID", "NAME", "NETWORK", "LABELS"})
	table.SetAutoWrapText(false)

	for _, ip := range ips {
		table.Append([]string{
			ip.Job,
			ip.Index,
			ip.Address,
			ip.ID,
			ip.Name,
			ip.NetworkID,
			formatLabels(ip.Labels),
		})
	}

	table.Render()
}

func formatLabels(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", k, m[k]))
	}
	return strings.Join(parts, ",")
}

func parseIndex(s string) int {
	var n int
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return 1 << 30
		}
		n = n*10 + int(ch-'0')
	}
	return n
}

// createKeyPair creates or imports SSH key pair
func (m *Manager) createKeyPair(ctx context.Context) error {
	logger.Info("Managing SSH key pair...")

	keypairName := fmt.Sprintf("%s-bastion", m.config.Name)

	// Check if keypair already exists
	existingKP, _ := m.stateManager.GetResource("keypair", keypairName)
	if existingKP != nil {
		logger.Info("Key pair already exists, skipping")
		return nil
	}

	// Try to get existing key pair first
	existingKeyPair, err := m.provider.Compute().GetKeyPair(ctx, keypairName)
	if err != nil && !cpi.IsNotFound(err) {
		return fmt.Errorf("failed to check existing key pair: %w", err)
	}

	if existingKeyPair != nil {
		logger.Infof("Using existing key pair: %s", keypairName)
	} else {
		// Create new key pair
		keyPair, err := m.provider.Compute().CreateKeyPair(ctx, keypairName)
		if err != nil {
			return fmt.Errorf("failed to create key pair: %w", err)
		}

		// Save private key to local file
		if keyPair.PrivateKey != "" {
			// Prefer new standard location: ~/.ocfp/keys/<bloc>-bastion/id_rsa
			home, _ := os.UserHomeDir()
			keyDir := filepath.Join(home, ".ocfp", "keys", fmt.Sprintf("%s-bastion", m.config.Name))
			if err := os.MkdirAll(keyDir, 0700); err != nil {
				logger.Warnf("Failed to create key directory %s: %v", keyDir, err)
			} else {
				keyPath := filepath.Join(keyDir, "id_rsa")
				if err := os.WriteFile(keyPath, []byte(keyPair.PrivateKey), 0600); err != nil {
					logger.Warnf("Failed to save private key to %s: %v", keyPath, err)
				} else {
					logger.Infof("Saved bastion private key to: %s", keyPath)
				}
			}
		}

		logger.Infof("Key pair created: %s", keypairName)
	}

	// Save to state
	if err := m.stateManager.AddResource(&state.Resource{
		ID:       keypairName,
		Type:     "keypair",
		Name:     keypairName,
		Provider: m.options.Provider,
		State:    "active",
		Tags: map[string]string{
			"managed-by": "ocfp",
			"bloc":       m.options.BlocName,
		},
	}); err != nil {
		return fmt.Errorf("failed to save key pair to state: %w", err)
	}

	return nil
}

// createVolumes creates persistent volumes
func (m *Manager) createVolumes(ctx context.Context) error {
	logger.Info("Creating volumes...")

	// Default volumes for bastion
	volumes := []struct {
		name    string
		size    int
		volType string
	}{
		{"bastion-root", 50, "standard"},
		{"bastion-data", 100, "standard"},
	}

	for _, vol := range volumes {
		volName := fmt.Sprintf("%s-%s", m.config.Name, vol.name)

		// Check if already exists
		existingVol, _ := m.stateManager.GetResource("volume", volName)
		if existingVol != nil {
			logger.Infof("Volume %s already exists, skipping", volName)
			continue
		}

		// Create volume
		volume, err := m.provider.Storage().CreateVolume(ctx, &cpi.CreateVolumeRequest{
			Name: volName,
			Size: vol.size,
			Type: vol.volType,
			Tags: func() map[string]string { t := m.baseTags(); t["role"] = "bastion"; return t }(),
		})
		if err != nil {
			return fmt.Errorf("failed to create volume %s: %w", volName, err)
		}

		// Save to state
		if err := m.stateManager.AddResource(&state.Resource{
			ID:       volume.ID,
			Type:     "volume",
			Name:     volName,
			Provider: m.options.Provider,
			State:    string(volume.State),
			Properties: map[string]interface{}{
				"size": volume.Size,
				"type": volume.Type,
			},
			Tags: volume.Tags,
		}); err != nil {
			return fmt.Errorf("failed to save volume to state: %w", err)
		}

		_ = m.stateManager.SetOutput(fmt.Sprintf("volume_%s_id", vol.name), volume.ID)
		logger.Infof("Volume created: %s (%dGB)", volName, vol.size)
	}

	return nil
}

// createBastion creates the bastion host
func (m *Manager) createBastion(ctx context.Context) error {
	logger.Info("Creating bastion host...")

	bastionName := fmt.Sprintf("%s-bastion", m.config.Name)

	// Check if already exists
	existingBastion, _ := m.stateManager.GetResource("instance", bastionName)
	if existingBastion != nil && existingBastion.State == string(cpi.ResourceStateActive) {
		logger.Info("Bastion already exists, skipping")
		return nil
	}

	// Get required IDs from state
	networkID, _ := m.stateManager.GetOutput("network_id")
	// Resolve a subnet for bastion.
	// STACKIT: no real subnets; use network only and record dependency on virtual ocfp-0.
	// Other providers: Prefer <bloc>-mgmt; if missing, fall back to any bloc subnet, preferring type=public.
	var (
		subnetIDVal      interface{}
		subnetNameForDep string
	)
	if strings.EqualFold(m.options.Provider, "stackit") {
		// No subnet ID for STACKIT
		subnetIDVal = nil
		subnetNameForDep = fmt.Sprintf("%s-%s", m.config.Name, "ocfp-0")
	} else {
		mgmtSubnetKey := fmt.Sprintf("subnet_%s-%s_id", m.config.Name, "mgmt")
		if id, err := m.stateManager.GetOutput(mgmtSubnetKey); err == nil && id != nil {
			subnetIDVal = id
			subnetNameForDep = fmt.Sprintf("%s-%s", m.config.Name, "mgmt")
		} else {
			// Fallback: scan state for subnets in this bloc
			subs, err := m.stateManager.ListResources("subnet")
			if err != nil {
				return fmt.Errorf("failed to list subnets from state: %w", err)
			}
			var anyCandidate *state.Resource
			for _, r := range subs {
				if !strings.HasPrefix(r.Name, m.config.Name+"-") {
					continue
				}
				// Prefer public type if available
				if t, ok := r.Properties["type"].(string); ok && strings.EqualFold(t, "public") {
					subnetIDVal = r.ID
					subnetNameForDep = r.Name
					break
				}
				if anyCandidate == nil {
					anyCandidate = r
				}
			}
			if subnetIDVal == nil && anyCandidate != nil {
				subnetIDVal = anyCandidate.ID
				subnetNameForDep = anyCandidate.Name
			}
			if subnetIDVal == nil {
				return fmt.Errorf("no suitable subnet found for bastion; ensure subnets phase created at least one subnet")
			}
			// Standardize: publish chosen subnet as mgmt if not set
			_ = m.stateManager.SetOutput(mgmtSubnetKey, subnetIDVal)
		}
	}
	sgID, _ := m.stateManager.GetOutput("sg_bastion_id")

	// Create bastion instance
	instance, err := m.provider.Compute().CreateInstance(ctx, &cpi.CreateInstanceRequest{
		Name:      bastionName,
		Flavor:    m.config.Bastion.Flavor,
		Image:     m.config.Bastion.Image,
		NetworkID: networkID.(string),
		SubnetID: func() string {
			if subnetIDVal == nil {
				return ""
			}
			return subnetIDVal.(string)
		}(),
		SecurityGroups: []string{sgID.(string)},
		KeyPair:        fmt.Sprintf("%s-bastion", m.config.Name),
		UserData:       generateBastionUserData(m.config),
		Tags:           func() map[string]string { t := m.baseTags(); t["role"] = "bastion"; t["job"] = "bastion"; return t }(),
	})
	if err != nil {
		return fmt.Errorf("failed to create bastion: %w", err)
	}

	// Save to state
	if err := m.stateManager.AddResource(&state.Resource{
		ID:       instance.ID,
		Type:     "instance",
		Name:     bastionName,
		Provider: m.options.Provider,
		State:    string(instance.State),
		Properties: map[string]interface{}{
			"flavor":     instance.Flavor,
			"image":      instance.Image,
			"private_ip": instance.PrivateIP,
			"public_ip":  instance.PublicIP,
		},
		Tags: instance.Tags,
	}); err != nil {
		return fmt.Errorf("failed to save bastion to state: %w", err)
	}

	// Add dependencies
	if subnetNameForDep == "" {
		if strings.EqualFold(m.options.Provider, "stackit") {
			subnetNameForDep = fmt.Sprintf("%s-%s", m.config.Name, "ocfp-0")
		} else {
			subnetNameForDep = fmt.Sprintf("%s-%s", m.config.Name, "mgmt")
		}
	}
	_ = m.stateManager.AddDependency(fmt.Sprintf("instance.%s", bastionName), fmt.Sprintf("subnet.%s", subnetNameForDep))
	_ = m.stateManager.AddDependency(fmt.Sprintf("instance.%s", bastionName), fmt.Sprintf("security_group.%s-bastion", m.config.Name))

	// Save outputs
	_ = m.stateManager.SetOutput("bastion_id", instance.ID)
	_ = m.stateManager.SetOutput("bastion_private_ip", instance.PrivateIP)
	if instance.PublicIP != "" {
		_ = m.stateManager.SetOutput("bastion_public_ip", instance.PublicIP)
	}

	logger.Infof("Bastion created: %s (IP: %s)", bastionName, instance.PrivateIP)
	return nil
}

// generateBastionUserData generates cloud-init user data for bastion
func generateBastionUserData(cfg *config.Config) string {
	return fmt.Sprintf(`#cloud-config
package_update: true
package_upgrade: true
packages:
  - git
  - tmux
  - vim
  - curl
  - wget
  - jq
  - unzip
  - ca-certificates

users:
  - default
  - name: ocfp
    gecos: OCFP Admin
    groups: [sudo]
    shell: /bin/bash
    sudo: ALL=(ALL) NOPASSWD:ALL
    lock_passwd: true

write_files:
  - path: /etc/profile.d/ocfp.sh
    permissions: '0644'
    content: |
      export OCFP_BLOC_NAME=%[1]s
      export OCFP_ENV=%[1]s
  - path: /home/ocfp/.bashrc
    owner: ocfp:ocfp
    permissions: '0644'
    content: |
      export OCFP_BLOC_NAME=%[1]s

runcmd:
  - [ bash, -lc, 'hostnamectl set-hostname %[1]s-bastion' ]
  - [ bash, -lc, 'mkdir -p /home/ocfp/.ocfp && chown -R ocfp:ocfp /home/ocfp/.ocfp' ]
  - [ bash, -lc, 'echo "Bastion initialized for %[1]s"' ]
`, cfg.Name)
}

// createBuckets ensures object storage buckets exist
func (m *Manager) createBuckets(ctx context.Context) error {
	logger.Info("Ensuring object storage buckets...")
	storage := m.provider.Storage()
	if storage == nil {
		logger.Info("Provider does not support object storage; skipping")
		return nil
	}

	// Desired buckets based on Perl implementation
	bucketNames := []string{
		fmt.Sprintf("%s-bosh-blobstore", m.config.Name),
		fmt.Sprintf("%s-cf-app-packages", m.config.Name),
		fmt.Sprintf("%s-cf-buildpacks", m.config.Name),
		fmt.Sprintf("%s-cf-droplets", m.config.Name),
		fmt.Sprintf("%s-cf-resources", m.config.Name),
		fmt.Sprintf("%s-artifacts", m.config.Name),
		fmt.Sprintf("%s-shield-backups", m.config.Name),
	}

	// List existing buckets (best-effort)
	existing := map[string]bool{}
	if list, err := storage.ListBuckets(ctx); err == nil {
		for _, b := range list {
			existing[b.Name] = true
		}
	} else {
		logger.Warnf("Failed to list buckets: %v (will attempt to create expected ones)", err)
	}

	// Optionally ensure a credentials group up-front (STACKIT only)
	if m.options.Provider == "stackit" {
		type ensureGroup interface {
			EnsureObjectStorageCredentialsGroup(context.Context, string) (string, error)
		}
		if s, ok := storage.(ensureGroup); ok {
			if groupID, err := s.EnsureObjectStorageCredentialsGroup(ctx, "ocfp-cli"); err == nil {
				// Persist credentials group to state
				_ = m.stateManager.AddResource(&state.Resource{
					ID:       groupID,
					Type:     "credentials_group",
					Name:     "ocfp-cli",
					Provider: m.options.Provider,
					State:    "active",
				})
			} else {
				logger.Warnf("Failed to ensure credentials group: %v", err)
			}
		}
	}

	for _, name := range bucketNames {
		if existing[name] {
			logger.Infof("Bucket exists: %s", name)
			continue
		}
		if m.options.DryRun {
			logger.Infof("[DRY RUN] Would create bucket: %s", name)
			continue
		}
		if _, err := storage.CreateBucket(ctx, name); err != nil {
			logger.Warnf("Failed to create bucket %s: %v", name, err)
			continue
		}
		// Save to state
		res := &state.Resource{
			ID:       name,
			Type:     "bucket",
			Name:     name,
			Provider: m.options.Provider,
			State:    "active",
			Properties: map[string]interface{}{
				"region": m.options.Region,
			},
		}
		_ = m.stateManager.AddResource(res)
		logger.Infof("Bucket created: %s", name)

		// Optional: enable versioning/lifecycle for parity with Perl (config-driven)
		enablePolicies := m.config.Blobstore.EnablePolicies || os.Getenv("OCFP_ENABLE_BUCKET_POLICIES") == "1" || os.Getenv("OCFP_ENABLE_BUCKET_POLICIES") == "true"
		if enablePolicies {
			// Best-effort: ignore errors, log warnings
			type ver interface {
				EnableBucketVersioning(context.Context, string) error
			}
			type life interface {
				SetBucketLifecycleNoncurrentDays(context.Context, string, int32) error
			}
			if v, ok := storage.(ver); ok {
				// Defaults tuned per bucket type
				switch {
				case strings.HasSuffix(name, "-cf-buildpacks"):
					settings := m.config.Blobstore.CFBuildpacks
					if settings.Versioning {
						if err := v.EnableBucketVersioning(ctx, name); err != nil {
							logger.Warnf("versioning %s: %v", name, err)
						}
					}
					if l, ok := storage.(life); ok && settings.NoncurrentDays > 0 {
						_ = l.SetBucketLifecycleNoncurrentDays(ctx, name, int32(settings.NoncurrentDays))
					}
					res.Properties["versioning"] = settings.Versioning
					res.Properties["noncurrent_days"] = settings.NoncurrentDays
				case strings.HasSuffix(name, "-cf-droplets"):
					settings := m.config.Blobstore.CFDroplets
					if settings.Versioning {
						if err := v.EnableBucketVersioning(ctx, name); err != nil {
							logger.Warnf("versioning %s: %v", name, err)
						}
					}
					if l, ok := storage.(life); ok && settings.NoncurrentDays > 0 {
						_ = l.SetBucketLifecycleNoncurrentDays(ctx, name, int32(settings.NoncurrentDays))
					}
					res.Properties["versioning"] = settings.Versioning
					res.Properties["noncurrent_days"] = settings.NoncurrentDays
				case strings.HasSuffix(name, "-cf-app-packages"):
					settings := m.config.Blobstore.CFAppPackages
					if vsettings := settings.Versioning; vsettings {
						if err := v.EnableBucketVersioning(ctx, name); err != nil {
							logger.Warnf("versioning %s: %v", name, err)
						}
					}
					if l, ok := storage.(life); ok && settings.NoncurrentDays > 0 {
						_ = l.SetBucketLifecycleNoncurrentDays(ctx, name, int32(settings.NoncurrentDays))
					}
					res.Properties["versioning"] = settings.Versioning
					res.Properties["noncurrent_days"] = settings.NoncurrentDays
				case strings.HasSuffix(name, "-bosh-blobstore"):
					settings := m.config.Blobstore.BoshBlobstore
					if settings.Versioning {
						if err := v.EnableBucketVersioning(ctx, name); err != nil {
							logger.Warnf("versioning %s: %v", name, err)
						}
					}
					if l, ok := storage.(life); ok && settings.NoncurrentDays > 0 {
						_ = l.SetBucketLifecycleNoncurrentDays(ctx, name, int32(settings.NoncurrentDays))
					}
					res.Properties["versioning"] = settings.Versioning
					res.Properties["noncurrent_days"] = settings.NoncurrentDays
				}
			}
		}
	}
	return nil
}

// splitIntoN splits parent network into N equal-power-of-two child CIDRs if possible.
// Returns empty slice if not feasible.
func splitIntoN(parentCIDR string, n int) []string {
	if n <= 0 || (n&(n-1)) != 0 { // n must be power of two
		return nil
	}
	_, parent, err := net.ParseCIDR(parentCIDR)
	if err != nil || parent == nil {
		return nil
	}
	ones, bits := parent.Mask.Size()
	if bits != 32 {
		return nil
	}
	// Increase prefix by log2(n)
	inc := 0
	for (1 << inc) < n {
		inc++
	}
	newPrefix := ones + inc
	if newPrefix > 32 {
		return nil
	}
	base := ipToUint32(parent.IP.Mask(parent.Mask))
	size := uint32(1) << uint32(32-newPrefix)
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		cidr := (&net.IPNet{IP: uint32ToIP(base + uint32(i)*size), Mask: net.CIDRMask(newPrefix, 32)}).String()
		out = append(out, cidr)
	}
	return out
}

func cidrFirstIP(cidr string) string {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil || ipnet == nil {
		return ""
	}
	return ipnet.IP.Mask(ipnet.Mask).String()
}

func cidrLastUsableIP(cidr string) string {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil || ipnet == nil {
		return ""
	}
	base := ipToUint32(ipnet.IP.Mask(ipnet.Mask))
	ones, _ := ipnet.Mask.Size()
	size := uint32(1) << uint32(32-ones)
	if size <= 2 {
		return uint32ToIP(base).String()
	}
	return uint32ToIP(base + size - 2).String()
}

func cidrGatewayIP(parentCIDR string) string {
	_, ipnet, err := net.ParseCIDR(parentCIDR)
	if err != nil || ipnet == nil {
		return ""
	}
	base := ipToUint32(ipnet.IP.Mask(ipnet.Mask))
	return uint32ToIP(base + 1).String()
}

// addVirtualSubnetToState records a virtual subnet resource + outputs, including reserved IPs
func (m *Manager) addVirtualSubnetToState(name string, subnetCIDR string, parentCIDR string, networkID interface{}) error {
	// Skip if already present
	if existingSubnet, _ := m.stateManager.GetResource("subnet", name); existingSubnet != nil {
		logger.Infof("Virtual subnet %s already recorded, skipping", name)
		return nil
	}
	props := map[string]interface{}{
		"cidr":              subnetCIDR,
		"availability_zone": "",
		"network_id":        networkID,
		"type":              "public",
		"virtual":           true,
		// Reserved fields
		"ip_0":        cidrFirstIP(subnetCIDR),
		"ip_n":        cidrLastUsableIP(subnetCIDR),
		"gateway":     cidrGatewayIP(parentCIDR),
		"parent_cidr": parentCIDR,
	}
	if err := m.stateManager.AddResource(&state.Resource{
		ID:         "virtual:" + name,
		Type:       "subnet",
		Name:       name,
		Provider:   m.options.Provider,
		State:      string(cpi.ResourceStateActive),
		Properties: props,
		Tags: func() map[string]string {
			t := m.baseTags()
			t["subnet-kind"] = "virtual"
			t["role"] = "ocfp"
			return t
		}(),
	}); err != nil {
		return fmt.Errorf("failed to save virtual subnet to state: %w", err)
	}
	// Outputs
	_ = m.stateManager.SetOutput(fmt.Sprintf("subnet_%s_id", name), "virtual:"+name)
	_ = m.stateManager.SetOutput(fmt.Sprintf("subnet_%s_cidr", name), subnetCIDR)
	_ = m.stateManager.SetOutput(fmt.Sprintf("subnet_%s_ip_0", name), props["ip_0"])
	_ = m.stateManager.SetOutput(fmt.Sprintf("subnet_%s_ip_n", name), props["ip_n"])
	_ = m.stateManager.SetOutput(fmt.Sprintf("subnet_%s_gateway", name), props["gateway"])
	// Reserved IP role assignments (STACKIT parity)
	m.addReservedIPOutputs(name, subnetCIDR, parentCIDR)
	return nil
}

// addReservedIPOutputs mirrors Perl reserved-ips mapping for STACKIT mgmt env
func (m *Manager) addReservedIPOutputs(name string, subnetCIDR string, parentCIDR string) {
	// Determine ocfp subnet index from name suffix
	idx := -1
	if i := strings.LastIndex(name, "-"); i != -1 && i+1 < len(name) {
		// parse trailing integer
		var n int
		for j := i + 1; j < len(name); j++ {
			ch := name[j]
			if ch < '0' || ch > '9' {
				n = -1
				break
			}
			n = n*10 + int(ch-'0')
		}
		if n >= 0 {
			idx = n
		}
	}
	base := ipToUint32(net.ParseIP(cidrFirstIP(subnetCIDR)))
	last := ipToUint32(net.ParseIP(cidrLastUsableIP(subnetCIDR)))

	// helper to set output
	set := func(key, val string) { _ = m.stateManager.SetOutput(fmt.Sprintf("reserved_%s_%s", name, key), val) }
	ipAt := func(off int) string { return uint32ToIP(base + uint32(off)).String() }

	// Single-IP assignments for mgmt
	// All ocfp subnets
	set("vault_ip", ipAt(5))
	set("jumpbox_ip", ipAt(6))
	set("concourse_ip", ipAt(7))
	set("prometheus_ip", ipAt(8))

	// Conditional per-subnet
	if idx == 0 {
		set("bastion_ip", ipAt(3))
		set("bosh_ip", ipAt(4))
		set("shield_ip", ipAt(9))
		set("blacksmith_ip", ipAt(10))
	}
	if idx == 1 {
		set("doomsday_ip", ipAt(9))
	}
	if idx == 2 {
		set("ocfp_ui_ip", ipAt(9))
	}

	// Available range: 11-29
	set("available_a", ipAt(11))
	set("available_b", ipAt(29))
	// Reserved ranges: 0-10, 30->
	set("reserved_a", ipAt(0))
	set("reserved_b", ipAt(10))
	set("reserved_c", ipAt(30))
	// reserved_d end of subnet (use last usable as approximation)
	set("reserved_d", uint32ToIP(last).String())
}
