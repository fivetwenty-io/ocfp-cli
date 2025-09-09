package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/state"
	"github.com/ocfp/ocfp-cli-go/internal/ui"
)

const (
	subnetStrategyTriple = "ocfp-triple"
	roleBastion          = "bastion"
	defaultNetworkCIDR   = "10.4.0.0/20"

	// Network ports.
	sshPort      = 22
	httpPort     = 80
	httpsPort    = 443
	httpAltPort  = 8080
	httpsAltPort = 8443
	boshPort     = 25555

	// Network configuration.
	networkSplitCount = 4
	defaultSubnetBits = 24
	ipv4Bits          = 32
	subnetIncrement   = 256

	// Default IP counts.
	defaultJumpboxCount   = 2
	defaultRouterCount    = 4
	defaultTCPRouterCount = 2

	// File permissions.
	sshKeyDirMode  = 0700
	sshKeyFileMode = 0600

	// Network bit shifts.
	shiftBy24Bits = 24
	shiftBy16Bits = 16
	shiftBy8Bits  = 8

	// Mathematical constants.
	decimalBase   = 10
	maxInt32      = 2147483647
	maxBitshift   = 30
	minSubnetSize = 2

	// IP allocation slots.
	vaultIPSlot      = 5
	jumpboxIPSlot    = 6
	concourseIPSlot  = 7
	prometheusIPSlot = 8
	bastionIPSlot    = 3
	shieldIPSlot     = 9
	blacksmithIPSlot = 10
	doomsdayIPSlot   = 9
	ocfpUIIPSlot     = 9
	availableAIPSlot = 11
	availableBIPSlot = 29
	reservedBIPSlot  = 10
	reservedCIPSlot  = 30

	// Special indices.
	ocfpUIProviderIndex = 2

	// Additional network ports.
	boshAgentPort = 6868
	cfSSHPort     = 2222
	tcpRouterMin  = 1024
	tcpRouterMax  = 65535

	// Disk sizes (GB).
	bastionRootDiskSize = 50
	bastionDataDiskSize = 100

	// Bit masks and operations.
	byteMask        = 0xFF
	broadcastOffset = 2

	// Additional IP slots.
	boshIPSlot = 4

	// Protocol constants.
	protocolAll = "all"

	// Subnet splitting configuration.
	dualSubnetSplitCount   = 2
	tripleSubnetSplitCount = 3

	// Bucket lifecycle policy days.
	buildpacksRetentionDays = 30
	dropletsRetentionDays   = 7
	packagesRetentionDays   = 14
	blobstoreRetentionDays  = 90
)

// Network configuration errors.
var (
	errInvalidParentCIDR   = errors.New("invalid parent CIDR")
	errOnlyIPv4Supported   = errors.New("only IPv4 CIDRs are supported")
	errInvalidPrefixLength = errors.New("invalid prefix length")
	errCannotSplitNetwork  = errors.New("cannot split network into subnets")
)

// errCannotSplitNetworkInto wraps the static error with count information.
func errCannotSplitNetworkInto(count int) error {
	return fmt.Errorf("%w: %d", errCannotSplitNetwork, count)
}

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
		Buckets        []string        `json:"buckets"             yaml:"buckets"`
		Volumes        []volumePreview `json:"volumes"             yaml:"volumes"`
		Bastion        bastionPreview  `json:"bastion"             yaml:"bastion"`
		CreateCount    int             `json:"createCount"         yaml:"createCount"`
	}
)

// securityGroupDef represents a default security group and its rules (pre-creation).
type securityGroupDef struct {
	name        string
	description string
	rules       []*cpi.SecurityRule
}

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

// Package-level functions (non-methods).
func firstChild24(parentCIDR string) string {
	_, ipnet, err := net.ParseCIDR(parentCIDR)
	if err != nil || ipnet == nil {
		return ""
	}

	base := ipToUint32(ipnet.IP.Mask(ipnet.Mask))

	return uint32ToIP(base).String() + "/24"
}

func nextSibling24(child24 string) string {
	_, ipnet, err := net.ParseCIDR(child24)
	if err != nil || ipnet == nil {
		return ""
	}

	base := ipToUint32(ipnet.IP.Mask(ipnet.Mask))

	return uint32ToIP(base+subnetIncrement).String() + "/24"
}

func SplitParentIntoTwo(parentCIDR string) (string, string) {
	parent, err := parseParentCIDR(parentCIDR)
	if err != nil {
		logger.Errorf("Error parsing parent CIDR %s: %v", parentCIDR, err)

		return "", ""
	}

	newPrefix, err := calculateNewPrefix(parent, dualSubnetSplitCount)
	if err != nil {
		logger.Errorf("Error calculating new prefix for %s: %v", parentCIDR, err)

		return "", ""
	}

	subnets := generateSubnets(parent, newPrefix, dualSubnetSplitCount)
	if len(subnets) != dualSubnetSplitCount {
		logger.Errorf("Expected %d subnets, got %d", dualSubnetSplitCount, len(subnets))

		return "", ""
	}

	return subnets[0], subnets[1]
}

func ipToUint32(ip net.IP) uint32 {
	ip = ip.To4()

	return uint32(ip[0])<<shiftBy24Bits + uint32(ip[1])<<shiftBy16Bits + uint32(ip[2])<<shiftBy8Bits + uint32(ip[3])
}

func uint32ToIP(n uint32) net.IP {
	return net.IPv4(byte(n>>shiftBy24Bits&byteMask), byte(n>>shiftBy16Bits&byteMask), byte(n>>shiftBy8Bits&byteMask), byte(n&byteMask))
}

func renderPublicIPsTable(ips []*cpi.PublicIP) {
	if len(ips) == 0 {
		return
	}

	sort.Slice(ips, func(i, j int) bool {
		iLabels := ips[i].Labels
		jLabels := ips[j].Labels

		iJob, iIndex := iLabels["job"], parseIndex(iLabels["index"])
		jJob, jIndex := jLabels["job"], parseIndex(jLabels["index"])

		if iJob != jJob {
			return iJob < jJob
		}

		return iIndex < jIndex
	})

	table := ui.NewTable("Public IPs")
	table.SetHeaders([]string{"Name", "Job", "Address", "Labels"})

	for _, ip := range ips {
		table.AddRow([]string{
			ip.Name,
			ip.Labels["job"],
			ip.IPAddress,
			formatLabels(ip.Labels),
		})
	}

	_ = table.Render()
}

func formatLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}

	pairs := make([]string, 0, len(labels))

	for k, v := range labels {
		pairs = append(pairs, k+"="+v)
	}

	sort.Strings(pairs)

	return strings.Join(pairs, ", ")
}

func parseIndex(indexString string) int {
	if indexString == "" {
		return -1
	}

	index, err := strconv.Atoi(indexString)
	if err != nil {
		return -1
	}

	return index
}

func generateBastionUserData(cfg *config.Config) string {
	if cfg.Bastion.UserData != "" {
		return cfg.Bastion.UserData
	}

	return `#!/bin/bash
# OCFP Bastion Host Setup
apt-get update
apt-get install -y vim curl wget git
# Configure SSH
sed -i 's/#PasswordAuthentication yes/PasswordAuthentication no/' /etc/ssh/sshd_config
systemctl restart sshd
# Create OCFP user
useradd -m -s /bin/bash ocfp
usermod -aG sudo ocfp
mkdir -p /home/ocfp/.ssh
chmod 700 /home/ocfp/.ssh
chown ocfp:ocfp /home/ocfp/.ssh
# Log completion
echo "$(date): Bastion setup completed" >> /var/log/ocfp-setup.log
`
}

func splitIntoN(parentCIDR string, count int) []string {
	if !isValidCount(count) {
		return nil
	}

	parent, err := parseParentCIDR(parentCIDR)
	if err != nil {
		return nil
	}

	newPrefix, err := calculateNewPrefix(parent, count)
	if err != nil {
		return nil
	}

	return generateSubnets(parent, newPrefix, count)
}

func isValidCount(count int) bool {
	return count > 0 && count <= maxInt32
}

func parseParentCIDR(parentCIDR string) (*net.IPNet, error) {
	ip, ipnet, err := net.ParseCIDR(parentCIDR)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", errInvalidParentCIDR, parentCIDR)
	}

	if ip.To4() == nil {
		return nil, errOnlyIPv4Supported
	}

	return ipnet, nil
}

func calculateNewPrefix(parent *net.IPNet, count int) (int, error) {
	oldPrefix, _ := parent.Mask.Size()
	if oldPrefix < 0 || oldPrefix > ipv4Bits {
		return 0, errInvalidPrefixLength
	}

	// Calculate new prefix to accommodate count subnets
	for newPrefix := oldPrefix + 1; newPrefix <= maxBitshift; newPrefix++ {
		maxSubnets := 1 << (newPrefix - oldPrefix)
		if maxSubnets >= count {
			return newPrefix, nil
		}
	}

	return 0, errCannotSplitNetworkInto(count)
}

func generateSubnets(parent *net.IPNet, newPrefix, count int) []string {
	base := ipToUint32(parent.IP.Mask(parent.Mask))
	size := uint32(1) << (ipv4Bits - newPrefix)
	subnets := make([]string, count)

	for subnetIndex := range count {
		if !isValidIndex(subnetIndex) {
			break
		}

		// Safe conversion: subnetIndex is validated to be >= 0 and <= maxInt32 (2147483647)
		// which is well within uint32 range (0 to 4294967295)
		if subnetIndex < 0 || subnetIndex > maxInt32 {
			break // Additional safety check for gosec
		}
		index := uint32(subnetIndex)
		subnets[subnetIndex] = createSubnetCIDR(base, index, size, newPrefix)
	}

	return subnets
}

func isValidIndex(index int) bool {
	return index >= 0 && index <= maxInt32
}

func createSubnetCIDR(base uint32, index uint32, size uint32, newPrefix int) string {
	subnetBase := base + (index * size)

	return fmt.Sprintf("%s/%d", uint32ToIP(subnetBase), newPrefix)
}

func cidrFirstIP(cidr string) string {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil || ipnet == nil {
		return ""
	}

	return ipnet.IP.String()
}

func cidrLastUsableIP(cidr string) string {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil || ipnet == nil {
		return ""
	}

	base := ipToUint32(ipnet.IP.Mask(ipnet.Mask))
	prefixLen, _ := ipnet.Mask.Size()
	size := uint32(1) << (ipv4Bits - prefixLen)

	if size < minSubnetSize {
		return ""
	}

	return uint32ToIP(base + size - broadcastOffset).String()
}

func cidrGatewayIP(parentCIDR string) string {
	_, ipnet, err := net.ParseCIDR(parentCIDR)
	if err != nil || ipnet == nil {
		return ""
	}

	base := ipToUint32(ipnet.IP.Mask(ipnet.Mask))

	return uint32ToIP(base + 1).String()
}

// Constructor
// NewManager creates a new bootstrap manager.
func NewManager(cfg *config.Config, provider cpi.Provider, stateManager *state.Manager, opts *Options) *Manager {
	return &Manager{
		config:       cfg,
		provider:     provider,
		stateManager: stateManager,
		options:      opts,
	}
}

// Exported methods (public methods starting with capital letters)

// StateManager returns the state manager.
func (m *Manager) StateManager() *state.Manager {
	return m.stateManager
}

// Execute executes the bootstrap process.
func (m *Manager) Execute(ctx context.Context) error {
	logger.Infof("Starting bootstrap for bloc=%s provider=%s region=%s", m.options.BlocName, m.options.Provider, m.options.Region)

	if m.options.DryRun {
		return m.renderDryRunPlan()
	}

	steps := []struct {
		name string
		fn   func(context.Context) error
	}{
		{"Create Network", m.CreateNetwork},
		{"Create Subnets", m.CreateSubnets},
		{"Create Security Groups", m.createSecurityGroups},
		{"Create Public IPs", m.createPublicIPs},
		{"Create Key Pair", m.createKeyPair},
		{"Create Volumes", m.createVolumes},
		{"Create Bastion", m.CreateBastion},
		{"Create Buckets", m.CreateBuckets},
	}

	for _, step := range steps {
		logger.Infof("Executing step: %s", step.name)

		err := step.fn(ctx)
		if err != nil {
			return fmt.Errorf("failed to %s: %w", strings.ToLower(step.name), err)
		}

		logger.Infof("Completed step: %s", step.name)
	}

	logger.Infof("Bootstrap completed successfully")

	return nil
}

// CreateNetwork creates the network infrastructure.
func (m *Manager) CreateNetwork(ctx context.Context) error {
	networkName := m.options.BlocName + "-network"

	// Check if network already exists
	if existingNetwork, _ := m.stateManager.GetResource("network", networkName); existingNetwork != nil {
		logger.Infof("Network %s already exists, skipping creation", networkName)

		return nil
	}

	netMgr := m.provider.NetworkManager()
	cidr := m.resolveNetworkCIDR()

	logger.Infof("Creating network: name=%s cidr=%s", networkName, cidr)

	network, err := netMgr.CreateNetwork(ctx, &cpi.NetworkRequest{
		Name:        networkName,
		CIDR:        cidr,
		DNSServers:  m.config.Network.DNSServers,
		Description: "Network for OCFP bloc " + m.options.BlocName,
		Tags:        m.baseTags(),
	})
	if err != nil {
		return fmt.Errorf("failed to create network: %w", err)
	}

	// Save network to state
	err = m.stateManager.AddResource(&state.Resource{
		ID:         network.ID,
		Type:       "network",
		Name:       networkName,
		Provider:   m.options.Provider,
		State:      string(cpi.ResourceStateActive),
		Properties: map[string]interface{}{"cidr": cidr, "dns_servers": m.config.Network.DNSServers},
		Tags:       m.baseTags(),
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	})
	if err != nil {
		return fmt.Errorf("failed to save network to state: %w", err)
	}

	// Set network outputs
	_ = m.stateManager.SetOutput("network_name", networkName)
	_ = m.stateManager.SetOutput("network_id", network.ID)
	_ = m.stateManager.SetOutput("network_cidr", cidr)

	logger.Infof("Network created successfully: id=%s", network.ID)

	return nil
}

// CreateSubnets creates the network subnets.
func (m *Manager) CreateSubnets(ctx context.Context) error {
	// Get network ID from state
	networkResource, err := m.stateManager.GetResource("network", m.options.BlocName+"-network")
	if err != nil {
		return fmt.Errorf("failed to get network from state: %w", err)
	}

	networkID := networkResource.ID
	logger.Infof("Creating subnets for network: id=%s", networkID)

	// Handle STACKIT virtual subnet strategy
	if m.config.Network.SubnetStrategy == subnetStrategyTriple {
		return m.createStackitVirtualSubnets(networkID)
	}

	return m.createStandardSubnets(ctx, networkID)
}

// CreateBastion creates the bastion host.
func (m *Manager) CreateBastion(ctx context.Context) error {
	bastionName := m.options.BlocName + "-bastion"

	// Check if bastion already exists
	if m.bastionAlreadyExists(bastionName) {
		logger.Infof("Bastion %s already exists, skipping creation", bastionName)

		return nil
	}

	// Resolve networking
	_, subnetInfo, err := m.resolveBastionNetworking()
	if err != nil {
		return fmt.Errorf("failed to resolve bastion networking: %w", err)
	}

	// Get security group
	sgID, err := m.getBastionSecurityGroup()
	if err != nil {
		return fmt.Errorf("failed to get bastion security group: %w", err)
	}

	// Create bastion instance
	instance, err := m.createBastionInstance(ctx, bastionName, subnetInfo.ID, sgID)
	if err != nil {
		return fmt.Errorf("failed to create bastion instance: %w", err)
	}

	// Save to state
	err = m.saveBastionToState(instance, bastionName)
	if err != nil {
		return fmt.Errorf("failed to save bastion to state: %w", err)
	}

	logger.Infof("Bastion created successfully: id=%s name=%s", instance.ID, bastionName)

	return nil
}

// CreateBuckets creates the required storage buckets.
func (m *Manager) CreateBuckets(ctx context.Context) error {
	if !m.provider.SupportsStorage() {
		logger.Infof("Provider %s does not support storage, skipping bucket creation", m.options.Provider)

		return nil
	}

	storage := m.provider.StorageManager()
	bucketNames := m.getRequiredBucketNames()

	logger.Infof("Creating %d buckets", len(bucketNames))

	existing := m.getExistingBuckets(ctx, storage)

	// Ensure credentials bucket group exists
	m.ensureCredentialsGroup(ctx, storage)

	// Process each bucket
	var errs []error

	for _, name := range bucketNames {
		err := m.processBucket(ctx, storage, name, existing)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to process bucket %s: %w", name, err))
		}
	}

	if len(errs) > 0 {
		return ErrBucketCreationErrors(errs)
	}

	logger.Infof("All buckets processed successfully")

	return nil
}

// Unexported methods (private methods starting with lowercase letters)

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

	m.setupNetworkPlan(plan)
	m.setupSubnetsPlan(plan)
	m.setupPublicIPsPlan(plan)
	m.setupSecurityGroupsPlan(plan)
	m.setupBucketsPlan(plan)
	m.setupVolumesPlan(plan)
	m.setupBastionPlan(plan)
	m.calculateCreateCount(plan)

	return plan
}

func (m *Manager) setupNetworkPlan(plan *bootstrapPlan) {
	cidr := m.resolveNetworkCIDR()
	plan.Network.Name = m.options.BlocName + "-network"
	plan.Network.CIDR = cidr
	plan.Network.DNS = m.config.Network.DNSServers
}

func (m *Manager) resolveNetworkCIDR() string {
	if m.config.Network.CIDR != "" {
		return m.config.Network.CIDR
	}

	return defaultNetworkCIDR
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
	default:
		plan.Subnets = m.buildConfiguredSubnets()
	}
}

func (m *Manager) buildStackitSubnets(parentCIDR string) []subnetPreview {
	return m.buildTripleSubnets(parentCIDR)
}

func (m *Manager) buildTripleSubnets(parentCIDR string) []subnetPreview {
	subnets := splitIntoN(parentCIDR, tripleSubnetSplitCount)
	if len(subnets) != tripleSubnetSplitCount {
		logger.Warnf("Expected %d subnets from parent %s, got %d", tripleSubnetSplitCount, parentCIDR, len(subnets))

		return nil
	}

	names := []string{"mgmt", "ocf", "services"}
	types := []string{"public", "public", "public"}

	previews := make([]subnetPreview, 0, len(subnets))

	for i, subnet := range subnets {
		previews = append(previews, subnetPreview{
			Name: fmt.Sprintf("%s-%s", m.options.BlocName, names[i]),
			CIDR: subnet,
			Type: types[i],
			AZ:   "",
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
	mgmtCIDR, ocfCIDR := SplitParentIntoTwo(parentCIDR)

	return []subnetPreview{
		{Name: m.options.BlocName + "-mgmt", CIDR: mgmtCIDR, Type: "public", AZ: ""},
		{Name: m.options.BlocName + "-ocf", CIDR: ocfCIDR, Type: "public", AZ: ""},
	}
}

func (m *Manager) buildDefaultSubnets(parentCIDR string) []subnetPreview {
	mgmtCIDR, ocfCIDR := m.calculateDefaultSubnetCIDRs(parentCIDR)

	return []subnetPreview{
		{Name: m.options.BlocName + "-mgmt", CIDR: mgmtCIDR, Type: "public", AZ: ""},
		{Name: m.options.BlocName + "-ocf", CIDR: ocfCIDR, Type: "public", AZ: ""},
	}
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

func (m *Manager) setupPublicIPsPlan(plan *bootstrapPlan) {
	if !m.supportsPublicIPs() {
		return
	}

	plan.PublicIPs = &publicIPPlan{
		Ops:       m.getPublicIPCount(m.config.PublicIPs.Ops, 1),
		Jumpbox:   m.getPublicIPCount(m.config.PublicIPs.Jumpbox, defaultJumpboxCount),
		Router:    m.getPublicIPCount(m.config.PublicIPs.Router, defaultRouterCount),
		CFSSH:     m.getPublicIPCount(m.config.PublicIPs.CFSSH, 1),
		TCPRouter: m.getPublicIPCount(m.config.PublicIPs.TCPRouter, defaultTCPRouterCount),
	}
}

func (m *Manager) getPublicIPCount(configured, defaultCount int) int {
	if configured > 0 {
		return configured
	}

	return defaultCount
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
	plan.Volumes = []volumePreview{
		{Name: m.options.BlocName + "-bastion-root", SizeGB: bastionRootDiskSize, Type: "gp3"},
		{Name: m.options.BlocName + "-bastion-data", SizeGB: bastionDataDiskSize, Type: "gp3"},
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

	return m.options.BlocName + "-mgmt"
}

func (m *Manager) calculateCreateCount(plan *bootstrapPlan) {
	count := 1 // Network
	count += len(plan.Subnets)
	count += len(plan.SecurityGroups)
	count += len(plan.Buckets)
	count += len(plan.Volumes)
	count += 1 // Bastion

	if plan.PublicIPs != nil {
		count += plan.PublicIPs.Ops + plan.PublicIPs.Jumpbox + plan.PublicIPs.Router + plan.PublicIPs.CFSSH + plan.PublicIPs.TCPRouter
	}

	plan.CreateCount = count
}

func (m *Manager) buildPlanTable(plan *bootstrapPlan) *ui.Table {
	table := ui.NewTable(fmt.Sprintf("Bootstrap Plan - %s (%s/%s)", plan.Bloc, plan.Provider, plan.Region))

	m.addNetworkSection(table, plan)
	m.addSubnetsSection(table, plan)

	if plan.PublicIPs != nil {
		m.addPublicIPsSection(table, plan)
	}

	m.addSecurityGroupsSection(table, plan)
	m.addBucketsSection(table, plan)
	m.addVolumesSection(table, plan)
	m.addBastionSection(table, plan)

	table.AddSeparator()
	table.AddRow([]string{"Total Resources", strconv.Itoa(plan.CreateCount)})

	return table
}

func (m *Manager) addNetworkSection(t *ui.Table, plan *bootstrapPlan) {
	t.AddSection("Network")
	t.AddRow([]string{"Name", plan.Network.Name})
	t.AddRow([]string{"CIDR", plan.Network.CIDR})
	t.AddRow([]string{"DNS", strings.Join(plan.Network.DNS, ", ")})
}

func (m *Manager) addSubnetsSection(table *ui.Table, plan *bootstrapPlan) {
	if len(plan.Subnets) == 0 {
		return
	}

	table.AddSection("Subnets")

	for _, subnet := range plan.Subnets {
		table.AddRow([]string{
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
	table.AddRow([]string{"Ops", strconv.Itoa(plan.PublicIPs.Ops)})
	table.AddRow([]string{"Jumpbox", strconv.Itoa(plan.PublicIPs.Jumpbox)})
	table.AddRow([]string{"Router", strconv.Itoa(plan.PublicIPs.Router)})
	table.AddRow([]string{"CF SSH", strconv.Itoa(plan.PublicIPs.CFSSH)})
	table.AddRow([]string{"TCP Router", strconv.Itoa(plan.PublicIPs.TCPRouter)})
}

func (m *Manager) addSecurityGroupsSection(table *ui.Table, plan *bootstrapPlan) {
	if len(plan.SecurityGroups) == 0 {
		return
	}

	table.AddSection("Security Groups")

	for _, sg := range plan.SecurityGroups {
		table.AddRow([]string{sg.Name, fmt.Sprintf("%d rules", sg.Rules)})
	}

	// Add detailed rules section
	m.addSecurityGroupRulesSection(table)
}

func (m *Manager) addSecurityGroupRulesSection(table *ui.Table) {
	table.AddSection("Security Group Rules")
	table.SetHeaders([]string{"Group", "Direction", "Protocol", "Ports", "Remote", "Description"})

	rows := m.buildSecurityGroupRuleRows()

	for _, row := range rows {
		table.AddRow(row)
	}

	table.SetHeaders(nil) // Reset headers
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

func (m *Manager) formatRemote(remote string) string {
	if remote == "" || remote == "0.0.0.0/0" {
		return "anywhere"
	}

	return remote
}

func (m *Manager) addBucketsSection(table *ui.Table, plan *bootstrapPlan) {
	if len(plan.Buckets) == 0 {
		return
	}

	table.AddSection("Buckets")

	for _, bucket := range plan.Buckets {
		table.AddRow([]string{bucket, "S3-compatible"})
	}
}

func (m *Manager) addVolumesSection(table *ui.Table, plan *bootstrapPlan) {
	if len(plan.Volumes) == 0 {
		return
	}

	table.AddSection("Volumes")

	for _, volume := range plan.Volumes {
		table.AddRow([]string{
			volume.Name,
			fmt.Sprintf("%d GB (%s)", volume.SizeGB, volume.Type),
		})
	}
}

func (m *Manager) addBastionSection(table *ui.Table, plan *bootstrapPlan) {
	table.AddSection("Bastion")
	table.AddRow([]string{"Name", plan.Bastion.Name})
	table.AddRow([]string{"Flavor", plan.Bastion.Flavor})
	table.AddRow([]string{"Image", plan.Bastion.Image})
	table.AddRow([]string{"Network", plan.Bastion.Network})
	table.AddRow([]string{"Subnet", plan.Bastion.Subnet})
}

func (m *Manager) baseTags() map[string]string {
	return map[string]string{
		"managed-by":    "ocfp-cli",
		"ocfp-bloc":     m.options.BlocName,
		"ocfp-provider": m.options.Provider,
		"ocfp-region":   m.options.Region,
		"created-by":    "bootstrap",
	}
}

func (m *Manager) createStackitVirtualSubnets(networkID interface{}) error {
	cidr := m.resolveNetworkCIDRForSubnets()

	switch {
	case strings.Contains(m.config.Network.SubnetStrategy, "triple"):
		return m.createStackitTripleSubnets(cidr, networkID)
	case strings.Contains(m.config.Network.SubnetStrategy, "single"):
		return m.createStackitSingleSubnet(cidr, networkID)
	default:
		return m.createStackitTripleSubnets(cidr, networkID)
	}
}

func (m *Manager) resolveNetworkCIDRForSubnets() string {
	networkOutput, err := m.stateManager.GetOutput("network_cidr")
	if err == nil {
		if cidr, ok := networkOutput.(string); ok && cidr != "" {
			return cidr
		}
	}

	return m.resolveNetworkCIDR()
}

func (m *Manager) createStackitTripleSubnets(cidr string, networkID interface{}) error {
	subnets := splitIntoN(cidr, tripleSubnetSplitCount)
	if len(subnets) != tripleSubnetSplitCount {
		// Fallback to manual split
		subnets = m.generateFallbackChildren(cidr)
	}

	names := []string{"mgmt", "ocf", "services"}

	for i, subnetCIDR := range subnets {
		name := fmt.Sprintf("%s-%s", m.options.BlocName, names[i])

		err := m.addVirtualSubnetWithDependency(name, subnetCIDR, cidr, networkID)
		if err != nil {
			return err
		}
	}

	return nil
}

func (m *Manager) generateFallbackChildren(cidr string) []string {
	child1 := firstChild24(cidr)
	child2 := nextSibling24(child1)
	child3 := nextSibling24(child2)

	return []string{child1, child2, child3}
}

func (m *Manager) createStackitSingleSubnet(cidr string, networkID interface{}) error {
	name := m.options.BlocName + "-subnet"

	return m.addVirtualSubnetWithDependency(name, cidr, cidr, networkID)
}

func (m *Manager) addVirtualSubnetWithDependency(name, subnetCIDR, parentCIDR string, networkID interface{}) error {
	err := m.addVirtualSubnetToState(name, subnetCIDR, parentCIDR, networkID)
	if err != nil {
		return err
	}

	m.addSubnetDependency(name)

	return nil
}

func (m *Manager) createStandardSubnets(ctx context.Context, networkID interface{}) error {
	netID, err := m.validateNetworkID(networkID)
	if err != nil {
		return err
	}

	subnets := m.resolveSubnetsForCreation()

	for _, subnet := range subnets {
		err := m.createSingleSubnet(ctx, subnet, netID, networkID)
		if err != nil {
			return fmt.Errorf("failed to create subnet %s: %w", subnet.Name, err)
		}
	}

	return nil
}

func (m *Manager) resolveSubnetsForCreation() []config.Subnet {
	if len(m.config.Network.Subnets) > 0 {
		return m.config.Network.Subnets
	}

	return m.generateDefaultSubnets()
}

func (m *Manager) generateDefaultSubnets() []config.Subnet {
	parent := m.resolveNetworkCIDRForSubnets()
	mgmtCIDR, ocfCIDR := m.calculateDefaultSubnetCIDRs(parent)

	subnets := []config.Subnet{
		{
			Name: m.options.BlocName + "-mgmt",
			CIDR: mgmtCIDR,
			Type: "public",
		},
		{
			Name: m.options.BlocName + "-ocf",
			CIDR: ocfCIDR,
			Type: "public",
		},
	}

	m.saveDefaultSubnetOutputs(parent, mgmtCIDR, ocfCIDR)

	return subnets
}

func (m *Manager) calculateDefaultSubnetCIDRs(parent string) (string, string) {
	mgmtCIDR, ocfCIDR := SplitParentIntoTwo(parent)
	if mgmtCIDR == "" || ocfCIDR == "" {
		// Fallback to /24 subnets
		mgmtCIDR = firstChild24(parent)
		ocfCIDR = nextSibling24(mgmtCIDR)
	}

	return mgmtCIDR, ocfCIDR
}

func (m *Manager) saveDefaultSubnetOutputs(parent, mgmtCIDR, ocfCIDR string) {
	_ = m.stateManager.SetOutput("subnet_parent_cidr", parent)
	_ = m.stateManager.SetOutput("subnet_mgmt_cidr", mgmtCIDR)
	_ = m.stateManager.SetOutput("subnet_ocf_cidr", ocfCIDR)
}

func (m *Manager) validateNetworkID(networkID interface{}) (string, error) {
	netID, ok := networkID.(string)
	if !ok || netID == "" {
		return "", ErrInvalidNetworkID(networkID)
	}

	return netID, nil
}

func (m *Manager) createSingleSubnet(ctx context.Context, subnet config.Subnet, netID string, networkID interface{}) error {
	subnetName := subnet.Name

	if m.subnetAlreadyExists(subnetName) {
		logger.Infof("Subnet %s already exists, skipping creation", subnetName)

		return nil
	}

	createdSubnet, err := m.createSubnetWithProvider(ctx, subnet, subnetName, netID)
	if err != nil {
		return err
	}

	return m.saveSubnetToState(createdSubnet, subnetName, networkID)
}

func (m *Manager) subnetAlreadyExists(subnetName string) bool {
	existingSubnet, _ := m.stateManager.GetResource("subnet", subnetName)

	return existingSubnet != nil
}

func (m *Manager) createSubnetWithProvider(ctx context.Context, subnet config.Subnet, subnetName, netID string) (*cpi.Subnet, error) {
	netMgr := m.provider.NetworkManager()

	logger.Infof("Creating subnet: name=%s cidr=%s type=%s", subnetName, subnet.CIDR, subnet.Type)

	createdSubnet, err := netMgr.CreateSubnet(ctx, &cpi.SubnetRequest{
		Name:             subnetName,
		NetworkID:        netID,
		CIDR:             subnet.CIDR,
		Type:             subnet.Type,
		AvailabilityZone: subnet.AvailabilityZone,
		Tags:             m.baseTags(),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create subnet %s: %w", subnetName, err)
	}

	return createdSubnet, nil
}

func (m *Manager) saveSubnetToState(createdSubnet *cpi.Subnet, subnetName string, networkID interface{}) error {
	// Save subnet to state
	err := m.stateManager.AddResource(&state.Resource{
		ID:       createdSubnet.ID,
		Type:     "subnet",
		Name:     subnetName,
		Provider: m.options.Provider,
		State:    string(cpi.ResourceStateActive),
		Properties: map[string]interface{}{
			"cidr":              createdSubnet.CIDR,
			"availability_zone": createdSubnet.AvailabilityZone,
			"network_id":        networkID,
			"type":              createdSubnet.Type,
		},
		Tags:      m.baseTags(),
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})
	if err != nil {
		return fmt.Errorf("failed to save subnet to state: %w", err)
	}

	m.addSubnetDependency(subnetName)
	m.saveSubnetOutputs(subnetName, createdSubnet)

	logger.Infof("Subnet created successfully: id=%s name=%s", createdSubnet.ID, subnetName)

	return nil
}

func (m *Manager) addSubnetDependency(subnetName string) {
	networkName := m.options.BlocName + "-network"
	_ = m.stateManager.AddDependency("subnet."+subnetName, "network."+networkName)
}

func (m *Manager) saveSubnetOutputs(subnetName string, createdSubnet *cpi.Subnet) {
	_ = m.stateManager.SetOutput(fmt.Sprintf("subnet_%s_id", subnetName), createdSubnet.ID)
	_ = m.stateManager.SetOutput(fmt.Sprintf("subnet_%s_cidr", subnetName), createdSubnet.CIDR)

	if createdSubnet.AvailabilityZone != "" {
		_ = m.stateManager.SetOutput(fmt.Sprintf("subnet_%s_az", subnetName), createdSubnet.AvailabilityZone)
	}
}

func (m *Manager) createSecurityGroups(ctx context.Context) error {
	netMgr := m.provider.NetworkManager()
	groups := m.defaultSecurityGroupDefs()

	logger.Infof("Creating %d security groups", len(groups))

	for _, group := range groups {
		groupName := fmt.Sprintf("%s-%s", m.options.BlocName, group.name)

		// Check if already exists
		if existingSG, _ := m.stateManager.GetResource("security_group", groupName); existingSG != nil {
			logger.Infof("Security group %s already exists, skipping creation", groupName)

			continue
		}

		logger.Infof("Creating security group: name=%s rules=%d", groupName, len(group.rules))

		securityGroup, err := netMgr.CreateSecurityGroup(ctx, &cpi.CreateSecurityGroupRequest{
			Name:        groupName,
			Description: group.description,
			Rules:       group.rules,
			Tags:        m.baseTags(),
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
	}

	return nil
}

func (m *Manager) createPublicIPs(ctx context.Context) error {
	if !m.supportsPublicIPs() {
		logger.Infof("Provider %s does not support public IPs, skipping creation", m.options.Provider)

		return nil
	}

	netMgr := m.provider.NetworkManager()
	stackitProvider, isStackit := m.getStackitProvider(netMgr)

	var allIPs []*cpi.PublicIP

	if isStackit {
		allIPs = m.createAllPublicIPs(ctx, netMgr, stackitProvider)
	} else {
		allIPs = m.createOpsPublicIPs(ctx, netMgr)
	}

	if len(allIPs) > 0 {
		m.renderPublicIPsSummary(allIPs)
	}

	return nil
}

func (m *Manager) supportsPublicIPs() bool {
	return m.provider.NetworkManager() != nil
}

type stackitEnsure interface {
	EnsureFloatingIP(ctx context.Context, req *cpi.PublicIPRequest) (*cpi.PublicIP, error)
}

type bucketVersioner interface {
	SetBucketVersioning(ctx context.Context, bucketName string, enabled bool) error
}

type bucketLifecycler interface {
	SetBucketLifecycle(ctx context.Context, bucketName string, noncurrentDays int) error
}

type bastionSubnetInfo struct {
	ID   string
	CIDR string
	Name string
}

//nolint:ireturn // Required for interface type switching
func (m *Manager) getStackitProvider(netMgr cpi.NetworkManager) (stackitEnsure, bool) {
	if m.options.Provider != "stackit" {
		return nil, false
	}

	// Type assertion to check if it supports EnsureFloatingIP
	if se, ok := netMgr.(stackitEnsure); ok {
		return se, true
	}

	return nil, false
}

func (m *Manager) createAllPublicIPs(ctx context.Context, netMgr cpi.NetworkManager, stackitProvider stackitEnsure) []*cpi.PublicIP {
	var allIPs []*cpi.PublicIP

	// Create ops public IPs
	allIPs = append(allIPs, m.createOpsPublicIPs(ctx, netMgr)...)

	// Create jumpbox public IPs
	allIPs = append(allIPs, m.createJumpboxPublicIPs(ctx, stackitProvider)...)

	// Create router public IPs
	allIPs = append(allIPs, m.createRouterPublicIPs(ctx, netMgr)...)

	// Create CF SSH public IPs
	allIPs = append(allIPs, m.createCFSSHPublicIPs(ctx, netMgr)...)

	// Create TCP router public IPs
	allIPs = append(allIPs, m.createTCPRouterPublicIPs(ctx, netMgr)...)

	return allIPs
}

func (m *Manager) createOpsPublicIPs(ctx context.Context, netMgr cpi.NetworkManager) []*cpi.PublicIP {
	count := m.getPublicIPCountWithDefault(m.config.PublicIPs.Ops, 1)

	return m.ensureAndRecordPublicIPs(
		ctx, netMgr, "ops", count,
		"ops-%d", map[string]string{
			"job": "ops",
		},
	)
}

func (m *Manager) createJumpboxPublicIPs(ctx context.Context, stackitProvider stackitEnsure) []*cpi.PublicIP {
	count := m.getPublicIPCountWithDefault(m.config.PublicIPs.Jumpbox, defaultJumpboxCount)

	ips := make([]*cpi.PublicIP, 0, count)

	for i := range count {
		name := fmt.Sprintf("%s-jumpbox-%d", m.options.BlocName, i)
		labels := map[string]string{"job": "jumpbox", "index": strconv.Itoa(i)}

		publicIP, err := stackitProvider.EnsureFloatingIP(ctx, &cpi.PublicIPRequest{
			Name:   name,
			Labels: labels,
			Tags:   m.baseTags(),
		})
		if err != nil {
			logger.Errorf("Failed to create jumpbox public IP %s: %v", name, err)

			continue
		}

		ips = append(ips, publicIP)
	}

	m.savePublicIPsToState(ips, "jumpbox", "jumpbox-%d")

	return ips
}

func (m *Manager) createRouterPublicIPs(ctx context.Context, netMgr cpi.NetworkManager) []*cpi.PublicIP {
	count := m.getPublicIPCountWithDefault(m.config.PublicIPs.Router, defaultRouterCount)

	return m.ensureAndRecordPublicIPs(
		ctx, netMgr, "router", count,
		"router-%d", map[string]string{
			"job": "router",
		},
	)
}

func (m *Manager) createCFSSHPublicIPs(ctx context.Context, netMgr cpi.NetworkManager) []*cpi.PublicIP {
	count := m.getPublicIPCountWithDefault(m.config.PublicIPs.CFSSH, 1)

	return m.ensureAndRecordPublicIPs(
		ctx, netMgr, "cf-ssh", count,
		"cf-ssh-%d", map[string]string{
			"job": "cf-ssh",
		},
	)
}

func (m *Manager) createTCPRouterPublicIPs(ctx context.Context, netMgr cpi.NetworkManager) []*cpi.PublicIP {
	count := m.getPublicIPCountWithDefault(m.config.PublicIPs.TCPRouter, defaultTCPRouterCount)

	return m.ensureAndRecordPublicIPs(
		ctx, netMgr, "tcp-router", count,
		"tcp-router-%d", map[string]string{
			"job": "tcp-router",
		},
	)
}

func (m *Manager) getPublicIPCountWithDefault(configured, defaultCount int) int {
	if configured > 0 {
		return configured
	}

	return defaultCount
}

func (m *Manager) savePublicIPsToState(ips []*cpi.PublicIP, job, nameFormat string) {
	for index, publicIP := range ips {
		name := fmt.Sprintf("%s-%s", m.options.BlocName, fmt.Sprintf(nameFormat, index))

		err := m.stateManager.AddResource(&state.Resource{
			ID:         publicIP.ID,
			Type:       "public_ip",
			Name:       name,
			Provider:   m.options.Provider,
			State:      string(cpi.ResourceStateActive),
			Properties: map[string]interface{}{"ip_address": publicIP.IPAddress},
			Tags:       m.baseTags(),
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
		})
		if err != nil {
			logger.Errorf("Failed to save public IP to state: %v", err)
		}

		// Set outputs
		_ = m.stateManager.SetOutput(fmt.Sprintf("%s_public_ip_%d", job, index), publicIP.IPAddress)
		_ = m.stateManager.SetOutput(fmt.Sprintf("%s_public_ip_%d_id", job, index), publicIP.ID)
	}
}

func (m *Manager) renderPublicIPsSummary(allIPs []*cpi.PublicIP) {
	logger.Infof("Created %d public IP(s)", len(allIPs))
	renderPublicIPsTable(allIPs)
}

func (m *Manager) ensureAndRecordPublicIPs(
	ctx context.Context,
	netMgr cpi.NetworkManager,
	job string,
	count int,
	nameFormat string,
	baseLabels map[string]string,
) []*cpi.PublicIP {
	ips := make([]*cpi.PublicIP, 0, count)

	for index := range count {
		name := fmt.Sprintf("%s-%s", m.options.BlocName, fmt.Sprintf(nameFormat, index))

		// Check if already exists
		if existingIP, _ := m.stateManager.GetResource("public_ip", name); existingIP != nil {
			logger.Infof("Public IP %s already exists, skipping creation", name)

			continue
		}

		labels := make(map[string]string)
		for k, v := range baseLabels {
			labels[k] = v
		}

		labels["index"] = strconv.Itoa(index)

		publicIP, err := netMgr.CreatePublicIP(ctx, &cpi.PublicIPRequest{
			Name:   name,
			Labels: labels,
			Tags:   m.baseTags(),
		})
		if err != nil {
			logger.Errorf("Failed to create public IP %s: %v", name, err)

			continue
		}

		ips = append(ips, publicIP)

		logger.Infof("Public IP created successfully: id=%s address=%s", publicIP.ID, publicIP.IPAddress)
	}

	m.savePublicIPsToState(ips, job, nameFormat)

	return ips
}

func (m *Manager) createKeyPair(ctx context.Context) error {
	keypairName := m.options.BlocName + "-keypair"

	// Check if keypair already exists
	if existingKeypair, _ := m.stateManager.GetResource("keypair", keypairName); existingKeypair != nil {
		logger.Infof("Keypair %s already exists, skipping creation", keypairName)

		return nil
	}

	return m.createNewKeyPair(ctx, keypairName)
}

func (m *Manager) createNewKeyPair(ctx context.Context, keypairName string) error {
	computeMgr := m.provider.ComputeManager()

	logger.Infof("Creating keypair: name=%s", keypairName)

	keypair, err := computeMgr.CreateKeyPair(ctx, &cpi.KeyPairRequest{
		Name: keypairName,
		Tags: m.baseTags(),
	})
	if err != nil {
		return fmt.Errorf("failed to create keypair: %w", err)
	}

	// Save private key to file
	err = m.savePrivateKey(keypair.PrivateKey)
	if err != nil {
		return fmt.Errorf("failed to save private key: %w", err)
	}

	// Save keypair to state
	err = m.stateManager.AddResource(&state.Resource{
		ID:       keypair.ID,
		Type:     "keypair",
		Name:     keypairName,
		Provider: m.options.Provider,
		State:    string(cpi.ResourceStateActive),
		Properties: map[string]interface{}{
			"public_key":  keypair.PublicKey,
			"fingerprint": keypair.Fingerprint,
		},
		Tags:      m.baseTags(),
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})
	if err != nil {
		return fmt.Errorf("failed to save keypair to state: %w", err)
	}

	// Set outputs
	_ = m.stateManager.SetOutput("keypair_name", keypairName)
	_ = m.stateManager.SetOutput("keypair_id", keypair.ID)
	_ = m.stateManager.SetOutput("keypair_public_key", keypair.PublicKey)
	_ = m.stateManager.SetOutput("keypair_fingerprint", keypair.Fingerprint)

	logger.Infof("Keypair created successfully: id=%s", keypair.ID)

	return nil
}

func (m *Manager) savePrivateKey(privateKey string) error {
	keyDir := filepath.Join(os.Getenv("HOME"), ".ssh", "ocfp", m.options.BlocName)
	keyFile := filepath.Join(keyDir, "id_rsa")

	// Create directory if it doesn't exist
	err := os.MkdirAll(keyDir, sshKeyDirMode)
	if err != nil {
		return fmt.Errorf("failed to create SSH key directory: %w", err)
	}

	// Write private key
	err = os.WriteFile(keyFile, []byte(privateKey), sshKeyFileMode)
	if err != nil {
		return fmt.Errorf("failed to write private key: %w", err)
	}

	logger.Infof("Private key saved to: %s", keyFile)

	return nil
}

func (m *Manager) createVolumes(ctx context.Context) error {
	computeMgr := m.provider.ComputeManager()

	volumes := []struct {
		name   string
		sizeGB int
		vType  string
	}{
		{m.options.BlocName + "-bastion-root", bastionRootDiskSize, "gp3"},
		{m.options.BlocName + "-bastion-data", bastionDataDiskSize, "gp3"},
	}

	logger.Infof("Creating %d volumes", len(volumes))

	for _, vol := range volumes {
		// Check if volume already exists
		if existingVol, _ := m.stateManager.GetResource("volume", vol.name); existingVol != nil {
			logger.Infof("Volume %s already exists, skipping creation", vol.name)

			continue
		}

		logger.Infof("Creating volume: name=%s size=%dGB type=%s", vol.name, vol.sizeGB, vol.vType)

		volume, err := computeMgr.CreateVolume(ctx, &cpi.VolumeRequest{
			Name:       vol.name,
			SizeGB:     vol.sizeGB,
			VolumeType: vol.vType,
			Tags:       m.baseTags(),
		})
		if err != nil {
			return fmt.Errorf("failed to create volume %s: %w", vol.name, err)
		}

		// Save volume to state
		err = m.stateManager.AddResource(&state.Resource{
			ID:       volume.ID,
			Type:     "volume",
			Name:     vol.name,
			Provider: m.options.Provider,
			State:    string(cpi.ResourceStateActive),
			Properties: map[string]interface{}{
				"size_gb":     vol.sizeGB,
				"volume_type": vol.vType,
			},
			Tags:      m.baseTags(),
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		})
		if err != nil {
			return fmt.Errorf("failed to save volume to state: %w", err)
		}

		logger.Infof("Volume created successfully: id=%s name=%s", volume.ID, vol.name)
	}

	return nil
}

func (m *Manager) bastionAlreadyExists(bastionName string) bool {
	existingBastion, _ := m.stateManager.GetResource("instance", bastionName)

	return existingBastion != nil
}

func (m *Manager) resolveBastionNetworking() (string, *bastionSubnetInfo, error) {
	// Get network ID from outputs
	networkOutput, err := m.stateManager.GetOutput("network_id")
	if err != nil {
		return "", nil, fmt.Errorf("failed to get network ID: %w", err)
	}

	networkID, ok := networkOutput.(string)
	if !ok || networkID == "" {
		return "", nil, ErrInvalidNetworkID(networkOutput)
	}

	// Try to find bastion subnet
	subnetInfo, err := m.findBastionSubnet()
	if err != nil {
		logger.Warnf("Failed to find bastion subnet: %v", err)

		// Try fallback subnet
		subnetInfo, err = m.findFallbackSubnet()
		if err != nil {
			return "", nil, fmt.Errorf("failed to find any suitable subnet for bastion: %w", err)
		}

		logger.Infof("Using fallback subnet for bastion: %s", subnetInfo.Name)
	}

	return networkID, subnetInfo, nil
}

func (m *Manager) findBastionSubnet() (*bastionSubnetInfo, error) {
	// Look for management subnet first
	bastionSubnet := m.options.BlocName + "-mgmt"

	if subnet, _ := m.stateManager.GetResource("subnet", bastionSubnet); subnet != nil {
		cidr, ok := subnet.Properties["cidr"].(string)
		if !ok {
			return nil, ErrInvalidCIDRTypeForSubnet(bastionSubnet)
		}

		return &bastionSubnetInfo{
			ID:   subnet.ID,
			CIDR: cidr,
			Name: bastionSubnet,
		}, nil
	}

	return nil, ErrBastionSubnetNotFound(bastionSubnet)
}

func (m *Manager) findFallbackSubnet() (*bastionSubnetInfo, error) {
	// Get all subnets from state
	resources, err := m.stateManager.GetResourcesByType("subnet")
	if err != nil {
		return nil, fmt.Errorf("failed to get subnets from state: %w", err)
	}

	// Find first subnet belonging to this bloc
	for _, resource := range resources {
		if strings.HasPrefix(resource.Name, m.options.BlocName+"-") {
			m.saveFallbackAsManagementSubnet(resource.ID)

			cidr, ok := resource.Properties["cidr"].(string)
			if !ok {
				return nil, ErrInvalidCIDRTypeForResource(resource.Name)
			}

			return &bastionSubnetInfo{
				ID:   resource.ID,
				CIDR: cidr,
				Name: resource.Name,
			}, nil
		}
	}

	return nil, ErrNoSuitableSubnetFoundForBastion
}

func (m *Manager) saveFallbackAsManagementSubnet(subnetID string) {
	_ = m.stateManager.SetOutput("mgmt_subnet_id", subnetID)
}

func (m *Manager) getBastionSecurityGroup() (string, error) {
	sgName := m.options.BlocName + "-bastion"

	if sg, _ := m.stateManager.GetResource("security_group", sgName); sg != nil {
		return sg.ID, nil
	}

	return "", ErrBastionSecurityGroupNotFound(sgName)
}

func (m *Manager) createBastionInstance(ctx context.Context, bastionName, subnetID, sgID string) (*cpi.Instance, error) {
	computeMgr := m.provider.ComputeManager()
	userData := generateBastionUserData(m.config)

	logger.Infof("Creating bastion instance: name=%s flavor=%s image=%s", bastionName, m.config.Bastion.Flavor, m.config.Bastion.Image)

	instance, err := computeMgr.CreateInstance(ctx, &cpi.InstanceRequest{
		Name:             bastionName,
		Flavor:           m.config.Bastion.Flavor,
		Image:            m.config.Bastion.Image,
		KeyPairName:      m.options.BlocName + "-keypair",
		SubnetID:         subnetID,
		SecurityGroupIDs: []string{sgID},
		UserData:         userData,
		Tags:             m.baseTags(),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create bastion instance %s: %w", bastionName, err)
	}

	return instance, nil
}

func (m *Manager) saveBastionToState(instance *cpi.Instance, bastionName string) error {
	err := m.stateManager.AddResource(&state.Resource{
		ID:       instance.ID,
		Type:     "instance",
		Name:     bastionName,
		Provider: m.options.Provider,
		State:    string(cpi.ResourceStateActive),
		Properties: map[string]interface{}{
			"flavor":          instance.Flavor,
			"image":           instance.Image,
			"private_ip":      instance.PrivateIP,
			"public_ip":       instance.PublicIP,
			"keypair":         instance.KeyPairName,
			"subnet_id":       instance.SubnetID,
			"security_groups": instance.SecurityGroupIDs,
		},
		Tags:      m.baseTags(),
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})
	if err != nil {
		return fmt.Errorf("failed to save bastion to state: %w", err)
	}

	// Add dependencies
	subnetName := m.getDefaultSubnetName()
	m.addBastionDependencies(bastionName, subnetName)

	// Save outputs
	m.saveBastionOutputs(instance)

	return nil
}

func (m *Manager) addBastionDependencies(bastionName, subnetName string) {
	_ = m.stateManager.AddDependency("instance."+bastionName, "subnet."+subnetName)
	_ = m.stateManager.AddDependency("instance."+bastionName, "security_group."+m.options.BlocName+"-bastion")
	_ = m.stateManager.AddDependency("instance."+bastionName, "keypair."+m.options.BlocName+"-keypair")
}

func (m *Manager) getDefaultSubnetName() string {
	return m.options.BlocName + "-mgmt"
}

func (m *Manager) saveBastionOutputs(instance *cpi.Instance) {
	_ = m.stateManager.SetOutput("bastion_id", instance.ID)
	_ = m.stateManager.SetOutput("bastion_private_ip", instance.PrivateIP)

	if instance.PublicIP != "" {
		_ = m.stateManager.SetOutput("bastion_public_ip", instance.PublicIP)
	}

	_ = m.stateManager.SetOutput("bastion_flavor", instance.Flavor)
	_ = m.stateManager.SetOutput("bastion_image", instance.Image)

	// SSH connection info
	if instance.PublicIP != "" {
		sshCommand := fmt.Sprintf("ssh -i ~/.ssh/ocfp/%s/id_rsa ubuntu@%s", m.options.BlocName, instance.PublicIP)
		_ = m.stateManager.SetOutput("bastion_ssh_command", sshCommand)
	}
}

func (m *Manager) getRequiredBucketNames() []string {
	buckets := []string{
		m.options.BlocName + "-cf-buildpacks",
		m.options.BlocName + "-cf-droplets",
		m.options.BlocName + "-cf-packages",
		m.options.BlocName + "-bosh-blobstore",
	}

	// Add any additional configured buckets
	for _, bucket := range m.config.Buckets {
		buckets = append(buckets, bucket.Name)
	}

	return buckets
}

func (m *Manager) getExistingBuckets(ctx context.Context, storage cpi.StorageManager) map[string]bool {
	existing := make(map[string]bool)

	buckets, err := storage.ListBuckets(ctx)
	if err != nil {
		logger.Warnf("Failed to list existing buckets: %v", err)

		return existing
	}

	for _, bucket := range buckets {
		existing[bucket.Name] = true
	}

	return existing
}

func (m *Manager) ensureCredentialsGroup(ctx context.Context, storage cpi.StorageManager) {
	// Skip if provider doesn't need credential groups
	if m.options.Provider != "stackit" {
		return
	}

	credentialsGroupName := m.options.BlocName + "-credentials"

	if existingGroup, _ := m.stateManager.GetResource("credentials_group", credentialsGroupName); existingGroup != nil {
		logger.Infof("Credentials group %s already exists", credentialsGroupName)

		return
	}

	logger.Infof("Creating credentials group: %s", credentialsGroupName)

	// Create credentials group using storage interface
	credentialsGroup, err := storage.CreateCredentialsGroup(ctx, &cpi.CredentialsGroupRequest{
		Name: credentialsGroupName,
		Tags: m.baseTags(),
	})
	if err != nil {
		logger.Errorf("Failed to create credentials group: %v", err)

		return
	}

	// Save to state
	err = m.stateManager.AddResource(&state.Resource{
		ID:         credentialsGroup.ID,
		Type:       "credentials_group",
		Name:       credentialsGroupName,
		Provider:   m.options.Provider,
		State:      string(cpi.ResourceStateActive),
		Properties: make(map[string]interface{}),
		Tags:       m.baseTags(),
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	})
	if err != nil {
		logger.Errorf("Failed to save credentials group to state: %v", err)
	}

	logger.Infof("Credentials group created successfully: %s", credentialsGroup.ID)
}

func (m *Manager) processBucket(ctx context.Context, storage cpi.StorageManager, name string, existing map[string]bool) error {
	if existing[name] {
		logger.Infof("Bucket %s already exists, configuring policies", name)
		m.configureBucketPolicies(ctx, storage, name)

		return nil
	}

	// Create new bucket
	err := m.createBucket(ctx, storage, name)
	if err != nil {
		return err
	}

	// Configure policies for new bucket
	m.configureBucketPolicies(ctx, storage, name)

	return nil
}

func (m *Manager) createBucket(ctx context.Context, storage cpi.StorageManager, name string) error {
	logger.Infof("Creating bucket: %s", name)

	bucket, err := storage.CreateBucket(ctx, &cpi.BucketRequest{
		Name: name,
		Tags: m.baseTags(),
	})
	if err != nil {
		return fmt.Errorf("failed to create bucket: %w", err)
	}

	// Save bucket to state
	err = m.stateManager.AddResource(&state.Resource{
		ID:         bucket.ID,
		Type:       "bucket",
		Name:       name,
		Provider:   m.options.Provider,
		State:      string(cpi.ResourceStateActive),
		Properties: map[string]interface{}{"region": bucket.Region},
		Tags:       m.baseTags(),
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	})
	if err != nil {
		return fmt.Errorf("failed to save bucket to state: %w", err)
	}

	// Set output
	_ = m.stateManager.SetOutput(fmt.Sprintf("bucket_%s_name", strings.ReplaceAll(name, "-", "_")), name)

	logger.Infof("Bucket created successfully: %s", name)

	return nil
}

func (m *Manager) configureBucketPolicies(ctx context.Context, storage cpi.StorageManager, name string) {
	if !m.shouldEnablePolicies() {
		return
	}

	versionProvider, lifecycleProvider := m.getBucketPolicyProviders(storage)
	if versionProvider == nil || lifecycleProvider == nil {
		logger.Warnf("Provider does not support bucket policies for %s", name)

		return
	}

	m.configureBucketByType(ctx, name, versionProvider, lifecycleProvider)
}

func (m *Manager) shouldEnablePolicies() bool {
	return len(m.config.Buckets) > 0
}

//nolint:ireturn // Required for interface type switching
func (m *Manager) getBucketPolicyProviders(storage cpi.StorageManager) (bucketVersioner, bucketLifecycler) {
	if versioner, ok := storage.(bucketVersioner); ok {
		if lifecycler, ok := storage.(bucketLifecycler); ok {
			return versioner, lifecycler
		}
	}

	return nil, nil
}

func (m *Manager) configureBucketByType(ctx context.Context, name string, versionProvider bucketVersioner, lifecycleProvider bucketLifecycler) {
	switch {
	case strings.Contains(name, "cf-buildpacks"):
		m.configureCFBuildpacksBucket(ctx, name, versionProvider, lifecycleProvider)
	case strings.Contains(name, "cf-droplets"):
		m.configureCFDropletsBucket(ctx, name, versionProvider, lifecycleProvider)
	case strings.Contains(name, "cf-packages"):
		m.configureCFAppPackagesBucket(ctx, name, versionProvider, lifecycleProvider)
	case strings.Contains(name, "bosh-blobstore"):
		m.configureBoshBlobstoreBucket(ctx, name, versionProvider, lifecycleProvider)
	default:
		logger.Infof("No specific policy configuration for bucket: %s", name)
	}
}

func (m *Manager) configureCFBuildpacksBucket(ctx context.Context, name string, versionProvider bucketVersioner, lifecycleProvider bucketLifecycler) {
	m.applyBucketSettings(ctx, name, false, buildpacksRetentionDays, versionProvider, lifecycleProvider)
}

func (m *Manager) configureCFDropletsBucket(ctx context.Context, name string, versionProvider bucketVersioner, lifecycleProvider bucketLifecycler) {
	m.applyBucketSettings(ctx, name, true, dropletsRetentionDays, versionProvider, lifecycleProvider)
}

func (m *Manager) configureCFAppPackagesBucket(ctx context.Context, name string, versionProvider bucketVersioner, lifecycleProvider bucketLifecycler) {
	m.applyBucketSettings(ctx, name, true, packagesRetentionDays, versionProvider, lifecycleProvider)
}

func (m *Manager) configureBoshBlobstoreBucket(ctx context.Context, name string, versionProvider bucketVersioner, lifecycleProvider bucketLifecycler) {
	m.applyBucketSettings(ctx, name, true, blobstoreRetentionDays, versionProvider, lifecycleProvider)
}

func (m *Manager) applyBucketSettings(ctx context.Context, name string, versioning bool, noncurrentDays int, versionProvider bucketVersioner, lifecycleProvider bucketLifecycler) {
	if versioning {
		err := versionProvider.SetBucketVersioning(ctx, name, versioning)
		if err != nil {
			logger.Warnf("Failed to configure versioning for %s: %v", name, err)
		} else {
			logger.Infof("Configured versioning for %s: %t", name, versioning)
		}
	}

	if m.isValidNoncurrentDays(noncurrentDays) {
		err := lifecycleProvider.SetBucketLifecycle(ctx, name, noncurrentDays)
		if err != nil {
			logger.Warnf("Failed to configure lifecycle for %s: %v", name, err)
		} else {
			logger.Infof("Configured lifecycle for %s: %d days", name, noncurrentDays)
		}
	}

	m.updateBucketResourceProperties(name, versioning, noncurrentDays)
}

func (m *Manager) isValidNoncurrentDays(days int) bool {
	return days > 0 && days <= 365
}

func (m *Manager) updateBucketResourceProperties(name string, versioning bool, noncurrentDays int) {
	if resource, _ := m.stateManager.GetResource("bucket", name); resource != nil {
		if resource.Properties == nil {
			resource.Properties = make(map[string]interface{})
		}

		resource.Properties["versioning"] = versioning

		if noncurrentDays > 0 {
			resource.Properties["lifecycle_noncurrent_days"] = noncurrentDays
		}

		_ = m.stateManager.UpdateResource(resource)
	}
}

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
		description: "Security group for STACKIT infrastructure",
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

	err := m.stateManager.AddResource(&state.Resource{
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
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})
	if err != nil {
		return fmt.Errorf("failed to save virtual subnet to state: %w", err)
	}
	// Outputs
	_ = m.stateManager.SetOutput(fmt.Sprintf("subnet_%s_id", name), "virtual:"+name)
	_ = m.stateManager.SetOutput(fmt.Sprintf("subnet_%s_cidr", name), subnetCIDR)
	_ = m.stateManager.SetOutput(fmt.Sprintf("subnet_%s_ip_0", name), props["ip_0"])
	_ = m.stateManager.SetOutput(fmt.Sprintf("subnet_%s_ip_n", name), props["ip_n"])
	_ = m.stateManager.SetOutput(fmt.Sprintf("subnet_%s_gateway", name), props["gateway"])
	// Reserved IP role assignments (STACKIT parity)
	m.addReservedIPOutputs(name, subnetCIDR)

	return nil
}

func (m *Manager) addReservedIPOutputs(name string, subnetCIDR string) {
	// Determine ocfp subnet index from name suffix
	idx := -1

	if i := strings.LastIndex(name, "-"); i != -1 && i+1 < len(name) {
		// parse trailing integer
		var result int

		for j := i + 1; j < len(name); j++ {
			character := name[j]
			if character < '0' || character > '9' {
				result = -1

				break
			}

			result = result*decimalBase + int(character-'0')
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
	set("vault_ip", ipAt(vaultIPSlot))
	set("jumpbox_ip", ipAt(jumpboxIPSlot))
	set("concourse_ip", ipAt(concourseIPSlot))
	set("prometheus_ip", ipAt(prometheusIPSlot))

	// Conditional per-subnet
	if idx == 0 {
		set("bastion_ip", ipAt(bastionIPSlot))
		set("bosh_ip", ipAt(boshIPSlot))
		set("shield_ip", ipAt(shieldIPSlot))
		set("blacksmith_ip", ipAt(blacksmithIPSlot))
	}

	if idx == 1 {
		set("doomsday_ip", ipAt(doomsdayIPSlot))
	}

	if idx == ocfpUIProviderIndex {
		set("ocfp_ui_ip", ipAt(ocfpUIIPSlot))
	}

	// Available range: 11-29
	set("available_a", ipAt(availableAIPSlot))
	set("available_b", ipAt(availableBIPSlot))
	// Reserved ranges: 0-10, 30->
	set("reserved_a", ipAt(0))
	set("reserved_b", ipAt(reservedBIPSlot))
	set("reserved_c", ipAt(reservedCIPSlot))
	// reserved_d end of subnet (use last usable as approximation)
	set("reserved_d", uint32ToIP(last).String())
}
