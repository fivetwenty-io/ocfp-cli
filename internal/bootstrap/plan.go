package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/ui"
)

// ErrBootstrapCancelled is returned when user cancels bootstrap operation.
var ErrBootstrapCancelled = errors.New("bootstrap cancelled by user")

// Plan data structures for dry run rendering.
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
	keypairPreview struct {
		Name    string
		KeyType string
	}
	bastionPreview struct{ Name, Flavor, Image, Network, Subnet string }
	publicIPPlan   struct{ Ops, Jumpbox, Router, CFSSH, TCPRouter int }
	bootstrapPlan  struct {
		Bloc     string `json:"bloc"     yaml:"bloc"`
		Provider string `json:"provider" yaml:"provider"`
		Region   string `json:"region"   yaml:"region"`
		Network  struct {
			Name string   `json:"name" yaml:"name"`
			CIDR string   `json:"cidr" yaml:"cidr"`
			DNS  []string `json:"dns"  yaml:"dns"`
		} `json:"network" yaml:"network"`
		Subnets        []subnetPreview `json:"subnets"             yaml:"subnets"`
		PublicIPs      *publicIPPlan   `json:"publicIps,omitempty" yaml:"publicIPs,omitempty"`
		SecurityGroups []sgPreview     `json:"securityGroups"      yaml:"securityGroups"`
		KeyPair        *keypairPreview `json:"keypair,omitempty"   yaml:"keypair,omitempty"`
		Buckets        []string        `json:"buckets"             yaml:"buckets"`
		Volumes        []volumePreview `json:"volumes"             yaml:"volumes"`
		Bastion        bastionPreview  `json:"bastion"             yaml:"bastion"`
		CreateCount    int             `json:"createCount"         yaml:"createCount"`
	}
)

// Table display-related constants.
const (
	protocolAll         = "all"
	tableSleepSeconds   = 5 // Sleep time for confirmation
	tableRowStartCells  = 3
	tableFourColumnRows = 4
	tableFiveColumnRows = 5
	tableSixColumnRows  = 6
)

// ==============================================================================
// Bootstrap Plan Building
// ==============================================================================

func (m *Manager) renderDryRunPlan() error {
	plan := m.buildBootstrapPlan()
	table := m.buildPlanTable(plan)
	_ = table.Render()

	return nil
}

func (m *Manager) buildBootstrapPlan() *bootstrapPlan {
	plan := &bootstrapPlan{
		Bloc:     m.options.BlocName,
		Provider: m.options.Provider,
		Region:   m.options.Region,
	}

	// Conditionally setup plan sections based on selected flags
	if m.shouldShowNetwork() {
		m.setupNetworkPlan(plan)
		m.setupSubnetsPlan(plan)
	}

	if m.shouldShowPublicIPs() {
		m.setupPublicIPsPlan(plan)
	}

	if m.shouldShowSecurity() {
		m.setupSecurityGroupsPlan(plan)
	}

	if m.shouldShowBuckets() {
		m.setupBucketsPlan(plan)
	}

	if m.shouldShowVolumes() {
		m.setupVolumesPlan(plan)
	}

	if m.shouldShowCompute() {
		m.setupKeypairPlan(plan)
		// Only setup bastion if not in key-pairs-only mode
		if !m.options.KeyPairs || m.options.Servers || m.options.Bastion {
			m.setupBastionPlan(plan)
		}
	}

	m.calculateCreateCount(plan)

	return plan
}

// ==============================================================================
// Plan Setup Functions
// ==============================================================================

func (m *Manager) setupNetworkPlan(plan *bootstrapPlan) {
	cidr := m.resolveNetworkCIDR()
	plan.Network.Name = m.resolveNetworkName()
	plan.Network.CIDR = cidr
	plan.Network.DNS = m.config.Network.DNSServers
}

func (m *Manager) setupSubnetsPlan(plan *bootstrapPlan) {
	cidr := m.resolveNetworkCIDR()

	switch m.config.Network.SubnetStrategy {
	case subnetStrategyTriple:
		plan.Subnets = m.buildStackitSubnets(cidr)
	case "single":
		plan.Subnets = m.buildSingleSubnet(cidr)
	case "standard":
		plan.Subnets = m.buildStandardSubnets(cidr)
	case "default":
		plan.Subnets = m.buildDefaultSubnets(cidr)
	case "": // Empty strategy - use default subnet generation
		plan.Subnets = m.buildDefaultSubnets(cidr)
	default:
		// If explicit subnets configured, use them; otherwise default to standard
		plan.Subnets = m.buildConfiguredSubnets()
		if len(plan.Subnets) == 0 {
			plan.Subnets = m.buildDefaultSubnets(cidr)
		}
	}
}

func (m *Manager) setupPublicIPsPlan(plan *bootstrapPlan) {
	if !m.supportsPublicIPs() {
		return
	}

	plan.PublicIPs = &publicIPPlan{
		Ops:       m.getPublicIPCountWithDefault(m.config.PublicIPs.Ops, 1),
		Jumpbox:   m.getPublicIPCountWithDefault(m.config.PublicIPs.Jumpbox, defaultJumpboxCount),
		Router:    m.getPublicIPCountWithDefault(m.config.PublicIPs.Router, defaultRouterCount),
		CFSSH:     m.getPublicIPCountWithDefault(m.config.PublicIPs.CFSSH, 1),
		TCPRouter: m.getPublicIPCountWithDefault(m.config.PublicIPs.TCPRouter, defaultTCPRouterCount),
	}
}

func (m *Manager) setupSecurityGroupsPlan(plan *bootstrapPlan) {
	groups := m.defaultSecurityGroupDefs()

	for _, group := range groups {
		plan.SecurityGroups = append(plan.SecurityGroups, sgPreview{
			Name:  group.name,
			Rules: len(group.rules),
		})
	}
}

func (m *Manager) setupBucketsPlan(plan *bootstrapPlan) {
	if !m.provider.SupportsStorage() {
		return
	}

	plan.Buckets = m.getRequiredBucketNames()
}

func (m *Manager) setupVolumesPlan(plan *bootstrapPlan) {
	// Volume creation disabled - volumes are never attached to bastion.
	// Both AWS and STACKIT handle volumes inline during instance creation
	// (via BlockDeviceMappings for AWS and bootVolume for STACKIT).
	plan.Volumes = []volumePreview{}

	// Original code (disabled):
	// rootDiskSize := m.config.Bastion.RootDiskSize
	// if rootDiskSize == 0 {
	// 	rootDiskSize = bastionRootDiskSize
	// }
	// dataDiskSize := m.config.Bastion.DataDiskSize
	// if dataDiskSize == 0 {
	// 	dataDiskSize = bastionDataDiskSize
	// }
	// plan.Volumes = []volumePreview{
	// 	{Name: m.options.BlocName + "-bastion-root", SizeGB: rootDiskSize, Type: "gp3"},
	// 	{Name: m.options.BlocName + "-bastion-data", SizeGB: dataDiskSize, Type: "gp3"},
	// }
}

func (m *Manager) setupKeypairPlan(plan *bootstrapPlan) {
	plan.KeyPair = &keypairPreview{
		Name:    m.options.BlocName + "-keypair",
		KeyType: "ed25519",
	}
}

func (m *Manager) setupBastionPlan(plan *bootstrapPlan) {
	plan.Bastion = bastionPreview{
		Name:    m.options.BlocName + "-bastion",
		Flavor:  m.config.Bastion.Flavor,
		Image:   m.config.Bastion.Image,
		Network: plan.Network.Name,
		Subnet:  m.determineBastionSubnet(),
	}
}

func (m *Manager) determineBastionSubnet() string {
	// Use first management subnet if available
	for _, subnet := range m.config.Network.Subnets {
		if strings.Contains(subnet.Name, "mgmt") {
			return subnet.Name
		}
	}

	// Fallback to first subnet
	if len(m.config.Network.Subnets) > 0 {
		return m.config.Network.Subnets[0].Name
	}

	return m.options.BlocName + "-ocfp-0"
}

func (m *Manager) calculateCreateCount(plan *bootstrapPlan) {
	count := 0

	// Only count resources that will actually be created
	if plan.Network.Name != "" {
		count++ // Network
	}

	count += len(plan.Subnets)
	count += len(plan.SecurityGroups)

	if plan.KeyPair != nil {
		count++ // SSH KeyPair
	}

	count += len(plan.Buckets)
	count += len(plan.Volumes)

	if plan.Bastion.Name != "" {
		count++ // Bastion
	}

	if plan.PublicIPs != nil {
		count += plan.PublicIPs.Ops + plan.PublicIPs.Jumpbox + plan.PublicIPs.Router + plan.PublicIPs.CFSSH + plan.PublicIPs.TCPRouter
	}

	plan.CreateCount = count
}

// ==============================================================================
// Subnet Building Functions
// ==============================================================================

func (m *Manager) buildStackitSubnets(parentCIDR string) []subnetPreview {
	return m.buildTripleSubnets(parentCIDR)
}

func (m *Manager) buildTripleSubnets(parentCIDR string) []subnetPreview {
	// Split into 4, skip first (reserved for infrastructure)
	allSubnets := SplitIntoN(parentCIDR, tripleSubnetSplitCount+1)
	if len(allSubnets) < tripleSubnetSplitCount+1 {
		logger.Warnf("Expected %d subnets from parent %s, got %d", tripleSubnetSplitCount+1, parentCIDR, len(allSubnets))

		return nil
	}

	// Skip first subnet, use next 3
	subnets := allSubnets[1:]

	previews := make([]subnetPreview, 0, len(subnets))

	for i, subnet := range subnets {
		previews = append(previews, subnetPreview{
			Name: fmt.Sprintf("%s-ocfp-%d", m.options.BlocName, i),
			CIDR: subnet,
			Type: "public",
			AZ:   config.FormatAvailabilityZone(m.options.Provider, m.options.Region, i),
		})
	}

	return previews
}

func (m *Manager) buildSingleSubnet(parentCIDR string) []subnetPreview {
	return []subnetPreview{
		{
			Name: m.options.BlocName + "-subnet",
			CIDR: parentCIDR,
			Type: "public",
			AZ:   "",
		},
	}
}

func (m *Manager) buildStandardSubnets(parentCIDR string) []subnetPreview {
	// Split into 4, skip first (reserved for infrastructure)
	allSubnets := SplitIntoN(parentCIDR, tripleSubnetSplitCount+1)
	if len(allSubnets) < tripleSubnetSplitCount+1 {
		// Fallback
		allSubnets = m.generateFallbackChildren(parentCIDR)
	}

	// Skip first subnet, use next 3
	subnets := allSubnets[1:]

	previews := make([]subnetPreview, 0, tripleSubnetSplitCount)

	for i := range tripleSubnetSplitCount {
		previews = append(previews, subnetPreview{
			Name: fmt.Sprintf("%s-ocfp-%d", m.options.BlocName, i),
			CIDR: subnets[i],
			Type: "public",
			AZ:   config.FormatAvailabilityZone(m.options.Provider, m.options.Region, i),
		})
	}

	return previews
}

func (m *Manager) buildDefaultSubnets(parentCIDR string) []subnetPreview {
	// Split into 4, skip first (reserved for infrastructure)
	allSubnets := SplitIntoN(parentCIDR, tripleSubnetSplitCount+1)
	if len(allSubnets) < tripleSubnetSplitCount+1 {
		// Fallback
		allSubnets = m.generateFallbackChildren(parentCIDR)
	}

	// Skip first subnet, use next 3
	subnets := allSubnets[1:]

	previews := make([]subnetPreview, 0, tripleSubnetSplitCount)

	for i := range tripleSubnetSplitCount {
		previews = append(previews, subnetPreview{
			Name: fmt.Sprintf("%s-ocfp-%d", m.options.BlocName, i),
			CIDR: subnets[i],
			Type: "public",
			AZ:   config.FormatAvailabilityZone(m.options.Provider, m.options.Region, i),
		})
	}

	return previews
}

func (m *Manager) buildConfiguredSubnets() []subnetPreview {
	previews := make([]subnetPreview, 0, len(m.config.Network.Subnets))

	for _, subnet := range m.config.Network.Subnets {
		previews = append(previews, subnetPreview{
			Name: subnet.Name,
			CIDR: subnet.CIDR,
			Type: subnet.Type,
			AZ:   subnet.AvailabilityZone,
		})
	}

	return previews
}

// ==============================================================================
// Plan Table Building
// ==============================================================================

func (m *Manager) buildPlanTable(plan *bootstrapPlan) *ui.Table {
	table := ui.NewTable(fmt.Sprintf("Bootstrap Plan - %s (%s/%s)", plan.Bloc, plan.Provider, plan.Region))

	// Only add sections for resources that will be created
	if plan.Network.Name != "" {
		m.addNetworkSection(table, plan)
	}

	if len(plan.Subnets) > 0 {
		m.addSubnetsSection(table, plan)
	}

	if plan.PublicIPs != nil {
		m.addPublicIPsSection(table, plan)
	}

	if len(plan.SecurityGroups) > 0 {
		m.addSecurityGroupsSection(table, plan)
	}

	if plan.KeyPair != nil {
		m.addKeypairSection(table, plan)
	}

	if len(plan.Buckets) > 0 {
		m.addBucketsSection(table, plan)
	}

	if len(plan.Volumes) > 0 {
		m.addVolumesSection(table, plan)
	}

	if plan.Bastion.Name != "" {
		m.addBastionSection(table, plan)
	}

	table.AddSection("Summary")
	idx := len(table.Sections) - 1
	table.Sections[idx].Rows = append(table.Sections[idx].Rows, []string{"Total Resources", strconv.Itoa(plan.CreateCount)})

	return table
}

func (m *Manager) addNetworkSection(t *ui.Table, plan *bootstrapPlan) {
	t.AddSection("Network")
	idx := len(t.Sections) - 1
	t.Sections[idx].Rows = append(t.Sections[idx].Rows, []string{"Name", plan.Network.Name})
	t.Sections[idx].Rows = append(t.Sections[idx].Rows, []string{"CIDR", plan.Network.CIDR})
	t.Sections[idx].Rows = append(t.Sections[idx].Rows, []string{"DNS", strings.Join(plan.Network.DNS, ", ")})
}

func (m *Manager) addSubnetsSection(table *ui.Table, plan *bootstrapPlan) {
	if len(plan.Subnets) == 0 {
		return
	}

	table.AddSection("Subnets")
	idx := len(table.Sections) - 1

	for _, subnet := range plan.Subnets {
		table.Sections[idx].Rows = append(table.Sections[idx].Rows, []string{
			subnet.Name,
			fmt.Sprintf("%s (%s)", subnet.CIDR, subnet.Type),
		})
	}
}

func (m *Manager) addPublicIPsSection(table *ui.Table, plan *bootstrapPlan) {
	if plan.PublicIPs == nil {
		return
	}

	table.AddSection("Public IPs")
	idx := len(table.Sections) - 1
	table.Sections[idx].Rows = append(table.Sections[idx].Rows, []string{"Ops", strconv.Itoa(plan.PublicIPs.Ops)})
	table.Sections[idx].Rows = append(table.Sections[idx].Rows, []string{"Jumpbox", strconv.Itoa(plan.PublicIPs.Jumpbox)})
	table.Sections[idx].Rows = append(table.Sections[idx].Rows, []string{"Router", strconv.Itoa(plan.PublicIPs.Router)})
	table.Sections[idx].Rows = append(table.Sections[idx].Rows, []string{"CF SSH", strconv.Itoa(plan.PublicIPs.CFSSH)})
	table.Sections[idx].Rows = append(table.Sections[idx].Rows, []string{"TCP Router", strconv.Itoa(plan.PublicIPs.TCPRouter)})
}

func (m *Manager) addSecurityGroupsSection(table *ui.Table, plan *bootstrapPlan) {
	if len(plan.SecurityGroups) == 0 {
		return
	}

	table.AddSection("Security Groups")
	idx := len(table.Sections) - 1

	for _, sg := range plan.SecurityGroups {
		table.Sections[idx].Rows = append(table.Sections[idx].Rows, []string{sg.Name, fmt.Sprintf("%d rules", sg.Rules)})
	}

	// Add detailed rules section
	m.addSecurityGroupRulesSection(table)
}

func (m *Manager) addSecurityGroupRulesSection(table *ui.Table) {
	table.AddSection("Security Group Rules")
	idx := len(table.Sections) - 1
	table.Sections[idx].Headers = []string{"Group", "Direction", "Protocol", "Ports", "Remote", "Description"}

	rows := m.buildSecurityGroupRuleRows()
	table.Sections[idx].Rows = append(table.Sections[idx].Rows, rows...)
}

func (m *Manager) buildSecurityGroupRuleRows() [][]string {
	var rows [][]string

	groups := m.defaultSecurityGroupDefs()

	for _, group := range groups {
		for _, rule := range group.rules {
			rows = append(rows, m.buildSecurityGroupRuleRow(group.name, rule))
		}
	}

	return rows
}

func (m *Manager) buildSecurityGroupRuleRow(groupName string, rule *cpi.SecurityRule) []string {
	proto := rule.Protocol
	ports := m.formatPorts(rule, proto)
	remote := m.formatRemote(rule.RemoteIPCIDR)

	return []string{groupName, rule.Direction, proto, ports, remote, rule.Description}
}

func (m *Manager) formatPorts(rule *cpi.SecurityRule, proto string) string {
	if proto == protocolAll {
		return protocolAll
	}

	if rule.PortRangeMin == rule.PortRangeMax {
		return strconv.Itoa(rule.PortRangeMin)
	}

	if rule.PortRangeMin == 0 && rule.PortRangeMax == 0 {
		return protocolAll
	}

	return fmt.Sprintf("%d-%d", rule.PortRangeMin, rule.PortRangeMax)
}

func (m *Manager) addKeypairSection(table *ui.Table, plan *bootstrapPlan) {
	table.AddSection("SSH KeyPair")
	idx := len(table.Sections) - 1
	table.Sections[idx].Rows = append(table.Sections[idx].Rows, []string{"Name", plan.KeyPair.Name})
	table.Sections[idx].Rows = append(table.Sections[idx].Rows, []string{"Type", plan.KeyPair.KeyType})
	table.Sections[idx].Rows = append(table.Sections[idx].Rows, []string{"Storage", fmt.Sprintf("~/.ocfp/%s/ssh/id_%s", m.options.BlocName, plan.KeyPair.KeyType)})
}

func (m *Manager) addBucketsSection(table *ui.Table, plan *bootstrapPlan) {
	if len(plan.Buckets) == 0 {
		return
	}

	table.AddSection("Buckets")
	idx := len(table.Sections) - 1

	for _, bucket := range plan.Buckets {
		table.Sections[idx].Rows = append(table.Sections[idx].Rows, []string{bucket, "S3-compatible"})
	}
}

func (m *Manager) addVolumesSection(table *ui.Table, plan *bootstrapPlan) {
	if len(plan.Volumes) == 0 {
		return
	}

	table.AddSection("Volumes")
	idx := len(table.Sections) - 1

	for _, volume := range plan.Volumes {
		table.Sections[idx].Rows = append(table.Sections[idx].Rows, []string{
			volume.Name,
			fmt.Sprintf("%d GB (%s)", volume.SizeGB, volume.Type),
		})
	}
}

func (m *Manager) addBastionSection(table *ui.Table, plan *bootstrapPlan) {
	table.AddSection("Bastion")
	idx := len(table.Sections) - 1
	table.Sections[idx].Rows = append(table.Sections[idx].Rows, []string{"Name", plan.Bastion.Name})
	table.Sections[idx].Rows = append(table.Sections[idx].Rows, []string{"Flavor", plan.Bastion.Flavor})
	table.Sections[idx].Rows = append(table.Sections[idx].Rows, []string{"Image", plan.Bastion.Image})
	table.Sections[idx].Rows = append(table.Sections[idx].Rows, []string{"Network", plan.Bastion.Network})
	table.Sections[idx].Rows = append(table.Sections[idx].Rows, []string{"Subnet", plan.Bastion.Subnet})
}

// ==============================================================================
// showBootstrapPlan displays the bootstrap plan before execution.
// ==============================================================================

func (m *Manager) showBootstrapPlan(ctx context.Context) error {
	_, _ = fmt.Fprintf(os.Stdout, "\n╔══════════════════════════════════════════════════════════════╗\n")
	_, _ = fmt.Fprintf(os.Stdout, "║              Bootstrap Plan Summary                          ║\n")
	_, _ = fmt.Fprintf(os.Stdout, "╚══════════════════════════════════════════════════════════════╝\n")

	// Build a plan showing what WILL be created (not what exists)
	plan := m.buildBootstrapPlan()

	// Display the plan sections
	if plan.Network.Name != "" {
		m.showPlannedNetwork(plan)
	}

	if len(plan.Subnets) > 0 {
		m.showPlannedSubnets(plan)
	}

	if plan.PublicIPs != nil {
		m.showPlannedPublicIPs(plan)
	}

	if len(plan.SecurityGroups) > 0 {
		m.showPlannedSecurityGroups(plan)
	}

	if plan.KeyPair != nil {
		m.showPlannedKeypair(plan)
	}

	if len(plan.Buckets) > 0 {
		m.showPlannedBuckets(plan)
	}

	if len(plan.Volumes) > 0 {
		m.showPlannedVolumes(plan)
	}

	if plan.Bastion.Name != "" {
		m.showPlannedBastion(plan)
	}

	// Show public IPs for STACKIT (if creating network infrastructure)
	if strings.EqualFold(m.options.Provider, "stackit") && plan.Network.Name != "" {
		m.showStackitPublicIPsTable(ctx)
	}

	// Prompt for confirmation unless --yes flag is set
	if !m.options.Yes {
		if !m.confirmBootstrap(plan.CreateCount) {
			return ErrBootstrapCancelled
		}
	}

	return nil
}

// confirmBootstrap asks user for confirmation before proceeding.
func (m *Manager) confirmBootstrap(resourceCount int) bool {
	confirmMsg := fmt.Sprintf("\nThis will create %d resources in bloc '%s'. Continue? [y/N]: ",
		resourceCount, m.options.BlocName)

	_, err := fmt.Fprint(os.Stdout, confirmMsg)
	if err != nil {
		logger.Get().Error(fmt.Sprintf("Failed to write confirmation prompt: %v", err))

		return false
	}

	var response string

	_, _ = fmt.Scanln(&response)

	return strings.HasPrefix(strings.ToLower(response), "y")
}

// shouldShowNetwork determines if network resources should be displayed.
func (m *Manager) shouldShowNetwork() bool {
	// Determine if we're in selective mode (any specific flags set)
	selectiveMode := m.options.Servers || m.options.Volumes || m.options.Snapshots ||
		m.options.Buckets || m.options.SecurityGroups || m.options.Network ||
		m.options.PublicIPs || m.options.KeyPairs || m.options.Bastion

	// If no selective flags and not --all, show everything (default all mode)
	if !selectiveMode && !m.options.All {
		return true
	}

	// In selective mode, only show network if explicitly requested
	// OR if --servers flag is set (servers need network infrastructure)
	// Note: --bastion alone does NOT require showing network (may already exist)
	return m.options.Network || m.options.Servers
}

// shouldShowSecurity determines if security group resources should be displayed.
func (m *Manager) shouldShowSecurity() bool {
	// Determine if we're in selective mode (any specific flags set)
	selectiveMode := m.options.Servers || m.options.Volumes || m.options.Snapshots ||
		m.options.Buckets || m.options.SecurityGroups || m.options.Network ||
		m.options.PublicIPs || m.options.KeyPairs || m.options.Bastion

	// If no selective flags and not --all, show everything (default all mode)
	if !selectiveMode && !m.options.All {
		return true
	}

	// In selective mode, only show security groups if explicitly requested
	// OR if --servers flag is set (servers need security groups)
	// Note: --bastion alone does NOT require showing security groups (may already exist)
	return m.options.SecurityGroups || m.options.Servers
}

// shouldShowBuckets determines if bucket resources should be displayed.
func (m *Manager) shouldShowBuckets() bool {
	selectiveMode := m.options.Servers || m.options.Volumes || m.options.Snapshots ||
		m.options.Buckets || m.options.SecurityGroups || m.options.Network || m.options.PublicIPs || m.options.Bastion || m.options.KeyPairs

	if !selectiveMode && !m.options.All {
		return true // Default all mode
	}

	return m.options.Buckets
}

// shouldShowVolumes determines if volume resources should be displayed.
func (m *Manager) shouldShowVolumes() bool {
	selectiveMode := m.options.Servers || m.options.Volumes || m.options.Snapshots ||
		m.options.Buckets || m.options.SecurityGroups || m.options.Network || m.options.PublicIPs || m.options.Bastion || m.options.KeyPairs

	if !selectiveMode && !m.options.All {
		return true // Default all mode
	}

	return m.options.Volumes
}

// shouldShowCompute determines if compute resources should be displayed.
func (m *Manager) shouldShowCompute() bool {
	selectiveMode := m.options.Servers || m.options.Volumes || m.options.Snapshots ||
		m.options.Buckets || m.options.SecurityGroups || m.options.Network || m.options.PublicIPs || m.options.KeyPairs

	if !selectiveMode && !m.options.All {
		return true // Default all mode
	}

	return m.options.Servers || m.options.Bastion || m.options.KeyPairs
}

// shouldShowPublicIPs determines if public IP resources should be displayed.
func (m *Manager) shouldShowPublicIPs() bool {
	// Determine if we're in selective mode (any specific flags set)
	selectiveMode := m.options.Servers || m.options.Volumes || m.options.Snapshots ||
		m.options.Buckets || m.options.SecurityGroups || m.options.Network ||
		m.options.PublicIPs || m.options.Bastion || m.options.KeyPairs

	// If no selective flags and not --all, show everything (default all mode)
	if !selectiveMode && !m.options.All {
		return true
	}

	// In selective mode, only show public IPs if explicitly requested
	// Note: removed dependency on --network flag to prevent showing existing IPs
	return m.options.PublicIPs
}

// ==============================================================================
// Planned Resource Display Functions
// ==============================================================================

func (m *Manager) showPlannedNetwork(plan *bootstrapPlan) {
	_, _ = fmt.Fprintf(os.Stdout, "\n📡 Network:\n")
	table := ui.NewTable("")
	table.SetHeaders([]string{"Name", "CIDR", "DNS", "Metadata"})

	metadata := FormatMetadataForDisplay(m.baseTags())
	table.AddRow([]string{plan.Network.Name, plan.Network.CIDR, strings.Join(plan.Network.DNS, ", "), metadata})
	_ = table.Render()
}

func (m *Manager) showPlannedSubnets(plan *bootstrapPlan) {
	_, _ = fmt.Fprintf(os.Stdout, "\n🔌 Subnets:\n")
	table := ui.NewTable("")
	table.SetHeaders([]string{"Name", "CIDR", "Type", "AZ", "Metadata"})

	metadata := FormatMetadataForDisplay(m.baseTags())
	for _, subnet := range plan.Subnets {
		table.AddRow([]string{subnet.Name, subnet.CIDR, subnet.Type, subnet.AZ, metadata})
	}

	_ = table.Render()
}

func (m *Manager) showPlannedPublicIPs(plan *bootstrapPlan) {
	_, _ = fmt.Fprintf(os.Stdout, "\n🌐 Public IPs:\n")
	table := ui.NewTable("")
	table.SetHeaders([]string{"Purpose", "Count"})

	table.AddRow([]string{"Ops", strconv.Itoa(plan.PublicIPs.Ops)})
	table.AddRow([]string{"Jumpbox", strconv.Itoa(plan.PublicIPs.Jumpbox)})
	table.AddRow([]string{"Router", strconv.Itoa(plan.PublicIPs.Router)})
	table.AddRow([]string{"CF SSH", strconv.Itoa(plan.PublicIPs.CFSSH)})
	table.AddRow([]string{"TCP Router", strconv.Itoa(plan.PublicIPs.TCPRouter)})

	_ = table.Render()
}

func (m *Manager) showPlannedSecurityGroups(plan *bootstrapPlan) {
	_, _ = fmt.Fprintf(os.Stdout, "\n🔒 Security Groups:\n")
	table := ui.NewTable("")
	table.SetHeaders([]string{"Name", "Rules", "Metadata"})

	metadata := FormatMetadataForDisplay(m.baseTags())
	for _, sg := range plan.SecurityGroups {
		table.AddRow([]string{sg.Name, fmt.Sprintf("%d rules", sg.Rules), metadata})
	}

	_ = table.Render()
}

func (m *Manager) showPlannedKeypair(plan *bootstrapPlan) {
	_, _ = fmt.Fprintf(os.Stdout, "\n🔑 SSH KeyPair:\n")
	table := ui.NewTable("")
	table.SetHeaders([]string{"Property", "Value"})
	table.AddRow([]string{"Name", plan.KeyPair.Name})
	table.AddRow([]string{"Type", plan.KeyPair.KeyType})
	table.AddRow([]string{"Local Storage", fmt.Sprintf("~/.ocfp/%s/ssh/id_%s", m.options.BlocName, plan.KeyPair.KeyType)})
	metadata := FormatMetadataForDisplay(m.baseTags())
	table.AddRow([]string{"Metadata", metadata})
	_ = table.Render()
}

func (m *Manager) showPlannedBuckets(plan *bootstrapPlan) {
	_, _ = fmt.Fprintf(os.Stdout, "\n🗄️  Buckets:\n")
	table := ui.NewTable("")
	table.SetHeaders([]string{"Name", "Type", "Metadata"})

	metadata := FormatMetadataForDisplay(m.baseTags())
	for _, bucket := range plan.Buckets {
		table.AddRow([]string{bucket, "S3-compatible", metadata})
	}

	_ = table.Render()
}

func (m *Manager) showPlannedVolumes(plan *bootstrapPlan) {
	_, _ = fmt.Fprintf(os.Stdout, "\n💾 Volumes:\n")
	table := ui.NewTable("")
	table.SetHeaders([]string{"Name", "Size", "Type", "Metadata"})

	metadata := FormatMetadataForDisplay(m.baseTags())
	for _, volume := range plan.Volumes {
		table.AddRow([]string{volume.Name, fmt.Sprintf("%d GB", volume.SizeGB), volume.Type, metadata})
	}

	_ = table.Render()
}

func (m *Manager) showPlannedBastion(plan *bootstrapPlan) {
	_, _ = fmt.Fprintf(os.Stdout, "\n💻 Bastion:\n")
	table := ui.NewTable("")
	table.SetHeaders([]string{"Property", "Value"})
	table.AddRow([]string{"Name", plan.Bastion.Name})
	table.AddRow([]string{"Flavor", plan.Bastion.Flavor})
	table.AddRow([]string{"Image", plan.Bastion.Image})
	table.AddRow([]string{"Network", plan.Bastion.Network})
	table.AddRow([]string{"Subnet", plan.Bastion.Subnet})

	metadata := FormatMetadataForDisplay(m.baseTags())
	table.AddRow([]string{"Metadata", metadata})
	_ = table.Render()
}
