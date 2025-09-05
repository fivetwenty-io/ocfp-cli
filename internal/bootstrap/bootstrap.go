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
	"github.com/ocfp/ocfp-cli-go/internal/ui"
)

// Options represents bootstrap options.
type Options struct {
	BlocName string
	Provider string
	Region   string
	Force    bool
	DryRun   bool
	Output   string
	Timeout  time.Duration
}

// Manager handles the bootstrap process.
type Manager struct {
	config       *config.Config
	provider     cpi.Provider
	stateManager *state.Manager
	options      *Options
}

// NewManager creates a new bootstrap manager.
func NewManager(cfg *config.Config, provider cpi.Provider, stateManager *state.Manager, opts *Options) *Manager {
	return &Manager{
		config:       cfg,
		provider:     provider,
		stateManager: stateManager,
		options:      opts,
	}
}

// Execute runs the bootstrap process.
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

	// For dry-run, render a clear plan preview and exit early
	if m.options.DryRun {
		if err := m.renderDryRunPlan(ctx); err != nil {
			return err
		}

		logger.Infof("Bootstrap completed successfully for bloc: %s", m.options.BlocName)

		return nil
	}

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

// securityGroupDef represents a default security group and its rules (pre-creation).
type securityGroupDef struct {
	name        string
	description string
	rules       []*cpi.SecurityRule
}

// defaultSecurityGroupDefs returns the list of default security groups and their rules used by bootstrap.
func (m *Manager) defaultSecurityGroupDefs() []securityGroupDef {
	return []securityGroupDef{
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
}

// renderDryRunPlan prints a concise, user-friendly plan for bootstrap actions.
func (m *Manager) renderDryRunPlan(ctx context.Context) error {
	// Shared plan data struct
	type (
		subnetPreview struct{ Name, CIDR, Type, AZ string }
		sgPreview     struct {
			Name  string
			Rules int
		}
		volumePreview struct {
			Name   string
			SizeGB int
			Type   string
		}
		bastionPreview struct{ Name, Flavor, Image, Network, Subnet string }
		publicIPPlan   struct{ Ops, Jumpbox, Router, CFSSH, TCPRouter int }
		plan           struct {
			Bloc     string `json:"bloc"     yaml:"bloc"`
			Provider string `json:"provider" yaml:"provider"`
			Region   string `json:"region"   yaml:"region"`
			Network  struct {
				Name string   `json:"name" yaml:"name"`
				CIDR string   `json:"cidr" yaml:"cidr"`
				DNS  []string `json:"dns"  yaml:"dns"`
			} `json:"network" yaml:"network"`
			Subnets        []subnetPreview `json:"subnets"             yaml:"subnets"`
			PublicIPs      *publicIPPlan   `json:"publicIps,omitempty" yaml:"publicIps,omitempty"`
			SecurityGroups []sgPreview     `json:"securityGroups"      yaml:"securityGroups"`
			Buckets        []string        `json:"buckets"             yaml:"buckets"`
			Volumes        []volumePreview `json:"volumes"             yaml:"volumes"`
			Bastion        bastionPreview  `json:"bastion"             yaml:"bastion"`
			CreateCount    int             `json:"createCount"         yaml:"createCount"`
		}
	)

	bootstrapPlan := plan{Bloc: m.options.BlocName, Provider: m.options.Provider, Region: m.options.Region}

	// Resolve network CIDR and derived subnets
	parentCIDR := m.config.Network.NetworkCIDR
	if parentCIDR == "" {
		parentCIDR = m.config.Network.CIDR
	}

	if parentCIDR == "" {
		parentCIDR = "10.4.0.0/20"
	}

	bootstrapPlan.Network.Name = m.config.Name + "-net"
	bootstrapPlan.Network.CIDR = parentCIDR
	bootstrapPlan.Network.DNS = m.config.DNS

	// Compute subnets preview
	var subnets []subnetPreview

	if strings.EqualFold(m.options.Provider, "stackit") {
		start := strings.ToLower(strings.TrimSpace(m.config.SubnetStrategy))
		if start == "ocfp-triple" {
			children := splitIntoN(parentCIDR, 4)
			if len(children) < 4 {
				c0 := firstChild24(parentCIDR)
				c1 := nextSibling24(c0)
				c2 := nextSibling24(c1)
				c3 := nextSibling24(c2)
				children = []string{c0, c1, c2, c3}
			}

			triples := []string{children[1], children[2], children[3]}
			for i, cidr := range triples {
				subnets = append(subnets, subnetPreview{
					Name: fmt.Sprintf("%s-ocfp-%d", m.config.Name, i),
					CIDR: cidr,
					Type: "public (virtual)",
				})
			}
		} else {
			subnets = append(subnets, subnetPreview{
				Name: m.config.Name + "-ocfp-0",
				CIDR: parentCIDR,
				Type: "public (virtual)",
			})
		}
	} else {
		if len(m.config.Subnets) == 0 {
			mgmtCIDR, ocfCIDR := splitParentIntoTwo(parentCIDR)
			if mgmtCIDR == "" || ocfCIDR == "" {
				mgmtCIDR = firstChild24(parentCIDR)
				ocfCIDR = nextSibling24(mgmtCIDR)
			}

			subnets = append(subnets,
				subnetPreview{Name: m.config.Name + "-mgmt", CIDR: mgmtCIDR, Type: "public"},
				subnetPreview{Name: m.config.Name + "-ocf", CIDR: ocfCIDR, Type: "private"},
			)
		} else {
			for _, sn := range m.config.Subnets {
				subnets = append(subnets, subnetPreview{
					Name: fmt.Sprintf("%s-%s", m.config.Name, sn.Name),
					CIDR: sn.CIDR,
					Type: sn.Type,
					AZ:   sn.AvailabilityZone,
				})
			}
		}
	}

	// Public IP counts (STACKIT only)
	var (
		opsCount       = 0
		jumpboxCount   = 0
		routerCount    = 0
		cfsshCount     = 0
		tcpRouterCount = 0
	)
	if strings.EqualFold(m.options.Provider, "stackit") {
		opsCount = 1

		jumpboxCount = m.config.JumpboxPublicIPs
		if jumpboxCount <= 0 {
			jumpboxCount = 2
		}

		routerCount = m.config.RouterPublicIPs
		if routerCount <= 0 {
			routerCount = 4
		}

		cfsshCount = m.config.CFSSHPublicIPs
		if cfsshCount <= 0 {
			cfsshCount = 1
		}

		tcpRouterCount = m.config.TCPRouterPublicIPs
		if tcpRouterCount <= 0 {
			tcpRouterCount = 2
		}
	}

	// Security groups planned (derive from default definitions)
	plannedSGs := []sgPreview{}
	for _, def := range m.defaultSecurityGroupDefs() {
		plannedSGs = append(plannedSGs, sgPreview{
			Name:  fmt.Sprintf("%s-%s", m.config.Name, def.name),
			Rules: len(def.rules),
		})
	}

	// Buckets planned
	bucketNames := []string{
		m.config.Name + "-bosh-blobstore",
		m.config.Name + "-cf-app-packages",
		m.config.Name + "-cf-buildpacks",
		m.config.Name + "-cf-droplets",
		m.config.Name + "-cf-resources",
		m.config.Name + "-artifacts",
		m.config.Name + "-shield-backups",
	}

	// Volumes planned
	volumes := []volumePreview{
		{Name: m.config.Name + "-bastion-root", SizeGB: 50, Type: "standard"},
		{Name: m.config.Name + "-bastion-data", SizeGB: 100, Type: "standard"},
	}

	// Bastion plan
	bastionSubnet := ""
	if strings.EqualFold(m.options.Provider, "stackit") {
		bastionSubnet = m.config.Name + "-ocfp-0 (virtual)"
		if strings.ToLower(strings.TrimSpace(m.config.SubnetStrategy)) == "ocfp-triple" {
			bastionSubnet = m.config.Name + "-ocfp-1 (virtual)"
		}
	} else {
		bastionSubnet = m.config.Name + "-mgmt"
	}

	// Summary counts
	totalCreates := 1 + len(subnets) + len(plannedSGs) + len(bucketNames) + len(volumes) + 1 // network + subnets + sgs + buckets + volumes + bastion
	if strings.EqualFold(m.options.Provider, "stackit") {
		totalCreates += (opsCount + jumpboxCount + routerCount + cfsshCount + tcpRouterCount) // public IPs tracked
		totalCreates += 1                                                                     // keypair tracked
	} else {
		totalCreates += 1 // keypair
	}

	// Build plan struct
	bootstrapPlan.Subnets = subnets
	if strings.EqualFold(m.options.Provider, "stackit") {
		bootstrapPlan.PublicIPs = &publicIPPlan{Ops: opsCount, Jumpbox: jumpboxCount, Router: routerCount, CFSSH: cfsshCount, TCPRouter: tcpRouterCount}
	}

	bootstrapPlan.SecurityGroups = plannedSGs
	bootstrapPlan.Buckets = bucketNames
	bootstrapPlan.Volumes = volumes
	bootstrapPlan.Bastion = bastionPreview{
		Name:    m.config.Name + "-bastion",
		Flavor:  m.config.Bastion.Flavor,
		Image:   m.config.Bastion.Image,
		Network: m.config.Name + "-net",
		Subnet:  bastionSubnet,
	}
	bootstrapPlan.CreateCount = totalCreates

	// Build UI table and render via shared UI renderer
	title := fmt.Sprintf("DRY RUN — Bootstrap Plan for bloc '%s' (provider=%s, region=%s)", m.options.BlocName, m.options.Provider, m.options.Region)
	summary := fmt.Sprintf("Plan: create %d resources, 0 to change, 0 to destroy", totalCreates)
	t := &ui.Table{Title: title, Summary: summary}

	// Network
	t.Sections = append(t.Sections, ui.Section{
		Title:   "Network",
		Headers: []string{"NAME", "CIDR", "DNS"},
		Rows:    [][]string{{bootstrapPlan.Network.Name, bootstrapPlan.Network.CIDR, strings.Join(bootstrapPlan.Network.DNS, ",")}},
	})

	// Subnets
	if len(subnets) > 0 {
		rows := make([][]string, 0, len(subnets))
		for _, s := range subnets {
			rows = append(rows, []string{s.Name, s.CIDR, s.Type, s.AZ})
		}

		t.Sections = append(t.Sections, ui.Section{Title: "Subnets", Headers: []string{"NAME", "CIDR", "TYPE", "AZ"}, Rows: rows})
	}

	// Public IPs (STACKIT only)
	if bootstrapPlan.PublicIPs != nil {
		t.Sections = append(t.Sections, ui.Section{
			Title:   "Public IPs",
			Headers: []string{"JOB", "COUNT"},
			Rows: [][]string{
				{"ops", strconv.Itoa(bootstrapPlan.PublicIPs.Ops)},
				{"jumpbox", strconv.Itoa(bootstrapPlan.PublicIPs.Jumpbox)},
				{"router", strconv.Itoa(bootstrapPlan.PublicIPs.Router)},
				{"cf-ssh", strconv.Itoa(bootstrapPlan.PublicIPs.CFSSH)},
				{"tcp-router", strconv.Itoa(bootstrapPlan.PublicIPs.TCPRouter)},
			},
		})
	}

	// Security Groups
	if len(plannedSGs) > 0 {
		rows := make([][]string, 0, len(plannedSGs))
		for _, s := range plannedSGs {
			rows = append(rows, []string{s.Name, strconv.Itoa(s.Rules)})
		}

		t.Sections = append(t.Sections, ui.Section{Title: "Security Groups", Headers: []string{"NAME", "RULES"}, Rows: rows})
	}

	// Security Group Rules (detailed)
	{
		ruleRows := [][]string{}

		for _, def := range m.defaultSecurityGroupDefs() {
			groupName := fmt.Sprintf("%s-%s", m.config.Name, def.name)
			for _, r := range def.rules {
				dir := strings.ToUpper(r.Direction)
				proto := strings.ToLower(r.Protocol)
				// Ports formatting
				ports := "-"

				if proto != "all" {
					if r.PortRangeMin > 0 && r.PortRangeMax > 0 {
						if r.PortRangeMin == r.PortRangeMax {
							ports = strconv.Itoa(r.PortRangeMin)
						} else {
							ports = fmt.Sprintf("%d-%d", r.PortRangeMin, r.PortRangeMax)
						}
					}
				}

				remote := r.RemoteIPCIDR
				if strings.TrimSpace(remote) == "" {
					remote = "-"
				}

				desc := r.Description
				ruleRows = append(ruleRows, []string{groupName, dir, proto, ports, remote, desc})
			}
		}

		if len(ruleRows) > 0 {
			t.Sections = append(t.Sections, ui.Section{
				Title:   "Security Group Rules",
				Headers: []string{"GROUP", "DIRECTION", "PROTO", "PORTS", "REMOTE", "DESCRIPTION"},
				Rows:    ruleRows,
			})
		}
	}

	// Buckets
	if len(bucketNames) > 0 {
		rows := make([][]string, 0, len(bucketNames))
		for _, b := range bucketNames {
			rows = append(rows, []string{b})
		}

		t.Sections = append(t.Sections, ui.Section{Title: "Object Storage Buckets", Headers: []string{"NAME"}, Rows: rows})
	}

	// Volumes
	if len(volumes) > 0 {
		rows := make([][]string, 0, len(volumes))
		for _, v := range volumes {
			rows = append(rows, []string{v.Name, strconv.Itoa(v.SizeGB), v.Type})
		}

		t.Sections = append(t.Sections, ui.Section{Title: "Volumes", Headers: []string{"NAME", "SIZE(GB)", "TYPE"}, Rows: rows})
	}

	// Bastion
	t.Sections = append(t.Sections, ui.Section{
		Title:   "Bastion",
		Headers: []string{"NAME", "FLAVOR", "IMAGE", "NETWORK", "SUBNET"},
		Rows: [][]string{{
			bootstrapPlan.Bastion.Name,
			bootstrapPlan.Bastion.Flavor,
			bootstrapPlan.Bastion.Image,
			bootstrapPlan.Bastion.Network,
			bootstrapPlan.Bastion.Subnet,
		}},
	})

	format := strings.ToLower(strings.TrimSpace(m.options.Output))
	if format == "" {
		format = "table"
	}

	return ui.Render(t, format)
}

// baseTags returns standard tags/labels to attach to resources.
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

// createNetwork creates the VPC/network.
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
		Name:       m.config.Name + "-net",
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

// createSubnets creates the subnets.
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
		start := strings.ToLower(strings.TrimSpace(m.config.SubnetStrategy))
		if start == "ocfp-triple" {
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

				if err := m.stateManager.AddDependency("subnet."+vname, "network."+m.config.Name); err != nil {
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

		if err := m.stateManager.AddDependency("subnet."+subnetName, "network."+m.config.Name); err != nil {
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
		if err := m.stateManager.AddDependency("subnet."+subnetName, "network."+m.config.Name); err != nil {
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

// firstChild24 returns a /24 from the provided network CIDR.
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

// nextSibling24 returns the next /24 network after the given /24 CIDR.
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
	if nextPrefix < 0 || nextPrefix > 32 {
		return "", ""
	}
	// Safe conversion: nextPrefix is validated to be 0-32
	if nextPrefix < 0 || nextPrefix > 32 {
		return "", ""
	}

	shift := uint32(32 - nextPrefix) // #nosec G115 - nextPrefix bounds validated above
	childSize := uint32(1) << shift
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

// createSecurityGroups creates security groups with default rules.
func (m *Manager) createSecurityGroups(ctx context.Context) error {
	logger.Info("Creating security groups...")

	// Get network ID
	networkID, err := m.stateManager.GetOutput("network_id")
	if err != nil {
		return fmt.Errorf("network ID not found in state: %w", err)
	}

	// Security groups modeled after Perl implementation (shared defs)
	// Names are prefixed with bloc name below
	securityGroups := m.defaultSecurityGroupDefs()

	for _, secGroup := range securityGroups {
		sgName := fmt.Sprintf("%s-%s", m.config.Name, secGroup.name)

		// Check if already exists
		existingSG, _ := m.stateManager.GetResource("security_group", sgName)
		if existingSG != nil {
			logger.Infof("Security group %s already exists, skipping", sgName)
			continue
		}

		// Create security group
		createdSG, err := m.provider.Security().CreateSecurityGroup(ctx, &cpi.CreateSecurityGroupRequest{
			Name:        sgName,
			Description: secGroup.description,
			NetworkID:   networkID.(string),
			Rules:       secGroup.rules,
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

		_ = m.stateManager.SetOutput(fmt.Sprintf("sg_%s_id", secGroup.name), createdSG.ID)

		logger.Infof("Security group created: %s", sgName)
	}

	return nil
}

// createPublicIPs creates public IPs for various services.
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

	stackitProvider, ok := netMgr.(stackitEnsure)
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
				for _, opsIP := range opsIPs {
					resName := fmt.Sprintf("%s-ops-%s", m.config.Name, opsIP.Labels["index"]) // usually index 0
					if err := m.stateManager.AddResource(&state.Resource{
						ID:       opsIP.ID,
						Type:     "public_ip",
						Name:     resName,
						Provider: m.options.Provider,
						State:    "active",
						Properties: map[string]interface{}{
							"job":       "ops",
							"index":     opsIP.Labels["index"],
							"address":   opsIP.Address,
							"networkId": opsIP.NetworkID,
						},
						Tags: opsIP.Labels,
					}); err != nil {
						logger.Warnf("Failed to save ops IP %s to state: %v", opsIP.ID, err)
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

	jumpboxIPs, err := stackitProvider.EnsureJumpboxPublicIPs(ctx, m.config.Name, jumpboxIPCount)
	if err != nil {
		return fmt.Errorf("failed ensuring jumpbox public IPs: %w", err)
	}

	// Persist jumpbox IPs to state
	for _, jumpboxIP := range jumpboxIPs {
		resName := fmt.Sprintf("%s-jumpbox-%s", m.config.Name, jumpboxIP.Labels["index"])
		if err := m.stateManager.AddResource(&state.Resource{
			ID:       jumpboxIP.ID,
			Type:     "public_ip",
			Name:     resName,
			Provider: m.options.Provider,
			State:    "active",
			Properties: map[string]interface{}{
				"job":       "jumpbox",
				"index":     jumpboxIP.Labels["index"],
				"address":   jumpboxIP.Address,
				"networkId": jumpboxIP.NetworkID,
			},
			Tags: jumpboxIP.Labels,
		}); err != nil {
			logger.Warnf("Failed to save jumpbox IP %s to state: %v", jumpboxIP.ID, err)
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
				for _, routerIP := range routerIPs {
					resName := fmt.Sprintf("%s-router-%s", m.config.Name, routerIP.Labels["index"])
					if err := m.stateManager.AddResource(&state.Resource{
						ID:       routerIP.ID,
						Type:     "public_ip",
						Name:     resName,
						Provider: m.options.Provider,
						State:    "active",
						Properties: map[string]interface{}{
							"job":       "router",
							"index":     routerIP.Labels["index"],
							"address":   routerIP.Address,
							"networkId": routerIP.NetworkID,
						},
						Tags: routerIP.Labels,
					}); err != nil {
						logger.Warnf("Failed to save router IP %s to state: %v", routerIP.ID, err)
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
				for _, publicIP := range cfIPs {
					resName := fmt.Sprintf("%s-cf-ssh-%s", m.config.Name, publicIP.Labels["index"])
					if err := m.stateManager.AddResource(&state.Resource{
						ID:       publicIP.ID,
						Type:     "public_ip",
						Name:     resName,
						Provider: m.options.Provider,
						State:    "active",
						Properties: map[string]interface{}{
							"job":       "cf-ssh",
							"index":     publicIP.Labels["index"],
							"address":   publicIP.Address,
							"networkId": publicIP.NetworkID,
						},
						Tags: publicIP.Labels,
					}); err != nil {
						logger.Warnf("Failed to save cf-ssh IP %s to state: %v", publicIP.ID, err)
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
				for _, publicIP := range tcpIPs {
					resName := fmt.Sprintf("%s-tcp-router-%s", m.config.Name, publicIP.Labels["index"])
					if err := m.stateManager.AddResource(&state.Resource{
						ID:       publicIP.ID,
						Type:     "public_ip",
						Name:     resName,
						Provider: m.options.Provider,
						State:    "active",
						Properties: map[string]interface{}{
							"job":       "tcp-router",
							"index":     publicIP.Labels["index"],
							"address":   publicIP.Address,
							"networkId": publicIP.NetworkID,
						},
						Tags: publicIP.Labels,
					}); err != nil {
						logger.Warnf("Failed to save tcp-router IP %s to state: %v", publicIP.ID, err)
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

// renderPublicIPsTable prints a table overview of ensured public IPs.
func renderPublicIPsTable(ips []*cpi.PublicIP) {
	// Stable sort by job then numeric index
	sort.Slice(ips, func(first, second int) bool {
		if ips[first].Job == ips[second].Job {
			firstIndex, secondIndex := parseIndex(ips[first].Index), parseIndex(ips[second].Index)
			if firstIndex == secondIndex {
				return ips[first].Index < ips[second].Index
			}

			return firstIndex < secondIndex
		}

		return ips[first].Job < ips[second].Job
	})

	// Render via shared UI renderer for consistency
	t := &ui.Table{Title: "Public IPs"}

	rows := make([][]string, 0, len(ips))
	for _, publicIP := range ips {
		rows = append(rows, []string{
			publicIP.Job,
			publicIP.Index,
			publicIP.Address,
			publicIP.ID,
			publicIP.Name,
			publicIP.NetworkID,
			formatLabels(publicIP.Labels),
		})
	}

	t.Sections = append(t.Sections, ui.Section{Title: "Ensured", Headers: []string{"JOB", "INDEX", "ADDRESS", "ID", "NAME", "NETWORK", "LABELS"}, Rows: rows})
	_ = ui.Render(t, "table")
}

func formatLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}

	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", key, labels[key]))
	}

	return strings.Join(parts, ",")
}

func parseIndex(indexString string) int {
	var result int

	for _, ch := range indexString {
		if ch < '0' || ch > '9' {
			return 1 << 30
		}

		result = result*10 + int(ch-'0')
	}

	return result
}

// createKeyPair creates or imports SSH key pair.
func (m *Manager) createKeyPair(ctx context.Context) error {
	logger.Info("Managing SSH key pair...")

	keypairName := m.config.Name + "-bastion"

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

			keyDir := filepath.Join(home, ".ocfp", "keys", m.config.Name+"-bastion")
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

// createVolumes creates persistent volumes.
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

// createBastion creates the bastion host.
func (m *Manager) createBastion(ctx context.Context) error {
	logger.Info("Creating bastion host...")

	bastionName := m.config.Name + "-bastion"

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

			for _, subnet := range subs {
				if !strings.HasPrefix(subnet.Name, m.config.Name+"-") {
					continue
				}
				// Prefer public type if available
				if t, ok := subnet.Properties["type"].(string); ok && strings.EqualFold(t, "public") {
					subnetIDVal = subnet.ID
					subnetNameForDep = subnet.Name

					break
				}

				if anyCandidate == nil {
					anyCandidate = subnet
				}
			}

			if subnetIDVal == nil && anyCandidate != nil {
				subnetIDVal = anyCandidate.ID
				subnetNameForDep = anyCandidate.Name
			}

			if subnetIDVal == nil {
				return errors.New("no suitable subnet found for bastion; ensure subnets phase created at least one subnet")
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
		KeyPair:        m.config.Name + "-bastion",
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

	_ = m.stateManager.AddDependency("instance."+bastionName, "subnet."+subnetNameForDep)
	_ = m.stateManager.AddDependency("instance."+bastionName, fmt.Sprintf("security_group.%s-bastion", m.config.Name))

	// Save outputs
	_ = m.stateManager.SetOutput("bastion_id", instance.ID)

	_ = m.stateManager.SetOutput("bastion_private_ip", instance.PrivateIP)
	if instance.PublicIP != "" {
		_ = m.stateManager.SetOutput("bastion_public_ip", instance.PublicIP)
	}

	logger.Infof("Bastion created: %s (IP: %s)", bastionName, instance.PrivateIP)

	return nil
}

// generateBastionUserData generates cloud-init user data for bastion.
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

// createBuckets ensures object storage buckets exist.
func (m *Manager) createBuckets(ctx context.Context) error {
	logger.Info("Ensuring object storage buckets...")

	storage := m.provider.Storage()
	if storage == nil {
		logger.Info("Provider does not support object storage; skipping")
		return nil
	}

	// Desired buckets based on Perl implementation
	bucketNames := []string{
		m.config.Name + "-bosh-blobstore",
		m.config.Name + "-cf-app-packages",
		m.config.Name + "-cf-buildpacks",
		m.config.Name + "-cf-droplets",
		m.config.Name + "-cf-resources",
		m.config.Name + "-artifacts",
		m.config.Name + "-shield-backups",
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
		if storageProvider, ok := storage.(ensureGroup); ok {
			if groupID, err := storageProvider.EnsureObjectStorageCredentialsGroup(ctx, "ocfp-cli"); err == nil {
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

			if versionProvider, ok := storage.(ver); ok {
				// Defaults tuned per bucket type
				switch {
				case strings.HasSuffix(name, "-cf-buildpacks"):
					settings := m.config.Blobstore.CFBuildpacks
					if settings.Versioning {
						if err := versionProvider.EnableBucketVersioning(ctx, name); err != nil {
							logger.Warnf("versioning %s: %v", name, err)
						}
					}

					if l, ok := storage.(life); ok && settings.NoncurrentDays > 0 {
						if settings.NoncurrentDays >= 0 && settings.NoncurrentDays <= int(^int32(0)>>1) {
							_ = l.SetBucketLifecycleNoncurrentDays(ctx, name, int32(settings.NoncurrentDays))
						}
					}

					res.Properties["versioning"] = settings.Versioning
					res.Properties["noncurrent_days"] = settings.NoncurrentDays
				case strings.HasSuffix(name, "-cf-droplets"):
					settings := m.config.Blobstore.CFDroplets
					if settings.Versioning {
						if err := versionProvider.EnableBucketVersioning(ctx, name); err != nil {
							logger.Warnf("versioning %s: %v", name, err)
						}
					}

					if l, ok := storage.(life); ok && settings.NoncurrentDays > 0 {
						if settings.NoncurrentDays >= 0 && settings.NoncurrentDays <= int(^int32(0)>>1) {
							_ = l.SetBucketLifecycleNoncurrentDays(ctx, name, int32(settings.NoncurrentDays))
						}
					}

					res.Properties["versioning"] = settings.Versioning
					res.Properties["noncurrent_days"] = settings.NoncurrentDays
				case strings.HasSuffix(name, "-cf-app-packages"):
					settings := m.config.Blobstore.CFAppPackages
					if vsettings := settings.Versioning; vsettings {
						if err := versionProvider.EnableBucketVersioning(ctx, name); err != nil {
							logger.Warnf("versioning %s: %v", name, err)
						}
					}

					if l, ok := storage.(life); ok && settings.NoncurrentDays > 0 {
						if settings.NoncurrentDays >= 0 && settings.NoncurrentDays <= int(^int32(0)>>1) {
							_ = l.SetBucketLifecycleNoncurrentDays(ctx, name, int32(settings.NoncurrentDays))
						}
					}

					res.Properties["versioning"] = settings.Versioning
					res.Properties["noncurrent_days"] = settings.NoncurrentDays
				case strings.HasSuffix(name, "-bosh-blobstore"):
					settings := m.config.Blobstore.BoshBlobstore
					if settings.Versioning {
						if err := versionProvider.EnableBucketVersioning(ctx, name); err != nil {
							logger.Warnf("versioning %s: %v", name, err)
						}
					}

					if l, ok := storage.(life); ok && settings.NoncurrentDays > 0 {
						if settings.NoncurrentDays >= 0 && settings.NoncurrentDays <= int(^int32(0)>>1) {
							_ = l.SetBucketLifecycleNoncurrentDays(ctx, name, int32(settings.NoncurrentDays))
						}
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
func splitIntoN(parentCIDR string, count int) []string {
	if count <= 0 || (count&(count-1)) != 0 { // count must be power of two
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
	for (1 << inc) < count {
		inc++
	}

	newPrefix := ones + inc
	if newPrefix > 32 {
		return nil
	}

	base := ipToUint32(parent.IP.Mask(parent.Mask))

	if newPrefix < 0 || newPrefix > 32 {
		return nil
	}
	// Safe conversion: newPrefix is validated to be 0-32
	if newPrefix < 0 || newPrefix > 32 {
		return nil
	}

	shift := uint32(32 - newPrefix) // #nosec G115 - newPrefix bounds validated above
	size := uint32(1) << shift

	out := make([]string, 0, count)
	for index := range count {
		if index < 0 || index > int(^uint32(0)) {
			continue
		}

		cidr := (&net.IPNet{IP: uint32ToIP(base + uint32(index)*size), Mask: net.CIDRMask(newPrefix, 32)}).String()
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
	if ones < 0 || ones > 32 {
		return ""
	}
	// Safe conversion: ones is validated to be 0-32
	if ones < 0 || ones > 32 {
		return ""
	}

	shift := uint32(32 - ones) // #nosec G115 - ones bounds validated above

	size := uint32(1) << shift
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

// addVirtualSubnetToState records a virtual subnet resource + outputs, including reserved IPs.
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

// addReservedIPOutputs mirrors Perl reserved-ips mapping for STACKIT mgmt env.
func (m *Manager) addReservedIPOutputs(name string, subnetCIDR string, parentCIDR string) {
	// Determine ocfp subnet index from name suffix
	idx := -1

	if i := strings.LastIndex(name, "-"); i != -1 && i+1 < len(name) {
		// parse trailing integer
		var result int

		for j := i + 1; j < len(name); j++ {
			ch := name[j]
			if ch < '0' || ch > '9' {
				result = -1
				break
			}

			result = result*10 + int(ch-'0')
		}

		if result >= 0 {
			idx = result
		}
	}

	base := ipToUint32(net.ParseIP(cidrFirstIP(subnetCIDR)))
	last := ipToUint32(net.ParseIP(cidrLastUsableIP(subnetCIDR)))

	// helper to set output
	set := func(key, val string) { _ = m.stateManager.SetOutput(fmt.Sprintf("reserved_%s_%s", name, key), val) }
	ipAt := func(off int) string {
		if off < 0 || off > int(^uint32(0)) {
			panic("offset out of range for uint32")
		}

		return uint32ToIP(base + uint32(off)).String()
	}

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
