package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

// Network-specific constants.
const (
	defaultNetworkCIDR     = "10.4.0.0/20"
	networkSplitCount      = 4
	defaultSubnetBits      = 24
	ipv4Bits               = 32
	subnetIncrement        = 256
	shiftBy24Bits          = 24
	shiftBy16Bits          = 16
	shiftBy8Bits           = 8
	decimalBase            = 10
	maxInt32               = 2147483647
	maxBitshift            = 30
	minSubnetSize          = 2
	byteMask               = 0xFF
	broadcastOffset        = 2
	dualSubnetSplitCount   = 2
	tripleSubnetSplitCount = 3
	subnetStrategyTriple   = "ocfp-triple"

	// Reserved IP slot constants for subnet IP assignment.
	vaultIPSlot         = 5
	jumpboxIPSlot       = 6
	concourseIPSlot     = 7
	prometheusIPSlot    = 8
	bastionIPSlot       = 3
	boshIPSlot          = 4
	shieldIPSlot        = 9
	blacksmithIPSlot    = 10
	doomsdayIPSlot      = 9
	ocfpUIIPSlot        = 9
	availableAIPSlot    = 11
	availableBIPSlot    = 29
	reservedBIPSlot     = 10
	reservedCIPSlot     = 30
	ocfpUIProviderIndex = 2
)

// Network configuration errors.
var (
	errInvalidParentCIDR         = errors.New("invalid parent CIDR")
	errOnlyIPv4Supported         = errors.New("only IPv4 CIDRs are supported")
	errInvalidPrefixLength       = errors.New("invalid prefix length")
	errCannotSplitNetwork        = errors.New("cannot split network into subnets")
	ErrInvalidCIDROffsetNegative = errors.New("CIDR offset must be non-negative")
	ErrInvalidCIDR               = errors.New("invalid CIDR")
	ErrOffsetOutOfRange          = errors.New("offset out of range for uint32")
)

// CreateNetwork creates the network/VPC.
func (m *Manager) CreateNetwork(ctx context.Context) error {
	// Determine network name - use override from config if provided, otherwise use bloc-name
	networkName := m.resolveNetworkName()

	// Check if network already exists in state
	if existingNetwork, _ := m.stateManager.GetResource("network", networkName); existingNetwork != nil {
		_, _ = fmt.Fprintf(os.Stdout, "    • Network %s already exists in state, skipping\n", networkName)
		logger.Infof("Network %s already exists in state, skipping creation", networkName)

		return nil
	}

	netMgr := m.provider.NetworkManager()
	cidr := m.resolveNetworkCIDR()

	// Check if a specific network/VPC ID is configured (e.g., vpc_id for AWS)
	if m.config.Network.ID != "" {
		return m.importConfiguredNetwork(ctx, netMgr, networkName, m.config.Network.ID)
	}

	// Check if network exists in cloud provider
	existingNetwork := m.findExistingNetwork(ctx, netMgr, networkName)
	if existingNetwork != nil {
		return m.importExistingNetwork(networkName, existingNetwork)
	}

	// Create new network
	return m.createNewNetwork(ctx, netMgr, networkName, cidr)
}

// CreateSubnets creates the network subnets.
func (m *Manager) CreateSubnets(ctx context.Context) error {
	// Get network ID from state
	networkName := m.resolveNetworkName()

	networkResource, err := m.stateManager.GetResource("network", networkName)
	if err != nil {
		return fmt.Errorf("failed to get network from state: %w", err)
	}

	networkID := networkResource.ID
	logger.Infof("Creating subnets for network: id=%s", networkID)

	// Handle STACKIT virtual subnet strategy
	// STACKIT doesn't support traditional subnets, use virtual subnets instead
	if m.config.Network.SubnetStrategy == subnetStrategyTriple || strings.EqualFold(m.options.Provider, "stackit") {
		return m.createStackitVirtualSubnets(networkID)
	}

	return m.createStandardSubnets(ctx, networkID)
}

func (m *Manager) findExistingNetwork(ctx context.Context, netMgr cpi.NetworkManager, networkName string) *cpi.Network {
	logger.Infof("Checking for existing networks with name %s", networkName)

	allNetworks, err := netMgr.ListNetworks(ctx, nil)
	if err != nil {
		logger.Warnf("Failed to list networks, proceeding with caution: %v", err)

		return nil // Continue but log the warning
	}

	var existingNetworks []*cpi.Network

	for _, net := range allNetworks {
		if net.Name == networkName {
			existingNetworks = append(existingNetworks, net)
		}
	}

	if len(existingNetworks) > 0 {
		if len(existingNetworks) > 1 {
			logger.Warnf("Found %d networks with name %s, using first one (ID: %s)",
				len(existingNetworks), networkName, existingNetworks[0].ID)
		}

		return existingNetworks[0]
	}

	return nil
}

func (m *Manager) importConfiguredNetwork(ctx context.Context, netMgr cpi.NetworkManager, networkName, networkID string) error {
	_, _ = fmt.Fprintf(os.Stdout, "    • Using configured network/VPC ID: %s\n", networkID)
	logger.Infof("Using configured network/VPC ID %s", networkID)

	// Get the network details from the provider
	network, err := netMgr.GetNetwork(ctx, networkID)
	if err != nil {
		return fmt.Errorf("failed to get configured network %s: %w", networkID, err)
	}

	return m.importExistingNetwork(networkName, network)
}

func (m *Manager) importExistingNetwork(networkName string, network *cpi.Network) error {
	_, _ = fmt.Fprintf(os.Stdout, "    • Network %s found in cloud (ID: %s), importing to state\n", networkName, network.ID)
	logger.Infof("Found existing network %s with ID %s, importing to state", networkName, network.ID)

	err := m.stateManager.AddResource(&state.Resource{
		ID:         network.ID,
		Type:       "network",
		Name:       networkName,
		Provider:   m.options.Provider,
		State:      string(cpi.ResourceStateActive),
		Properties: map[string]interface{}{"cidr": network.CIDR, "dns_servers": m.config.Network.DNSServers},
		Tags:       network.Tags,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	})
	if err != nil {
		return fmt.Errorf("failed to import network to state: %w", err)
	}

	setErr := m.stateManager.SetOutput("network_id", network.ID)
	if setErr != nil {
		logger.Warnf("Failed to set network_id output: %v", setErr)
	}

	setErr = m.stateManager.SetOutput("network_cidr", network.CIDR)
	if setErr != nil {
		logger.Warnf("Failed to set network_cidr output: %v", setErr)
	}

	setErr = m.stateManager.SetOutput("network_name", networkName)
	if setErr != nil {
		logger.Warnf("Failed to set network_name output: %v", setErr)
	}

	return nil
}

func (m *Manager) createNewNetwork(ctx context.Context, netMgr cpi.NetworkManager, networkName, cidr string) error {
	_, _ = fmt.Fprintf(os.Stdout, "    • Creating network %s with CIDR %s...\n", networkName, cidr)
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

	_, _ = fmt.Fprintf(os.Stdout, "    • Network created with ID: %s\n", network.ID)

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

	setErr := m.stateManager.SetOutput("network_name", networkName)
	if setErr != nil {
		logger.Warnf("Failed to set network_name output: %v", setErr)
	}

	setErr = m.stateManager.SetOutput("network_id", network.ID)
	if setErr != nil {
		logger.Warnf("Failed to set network_id output: %v", setErr)
	}

	setErr = m.stateManager.SetOutput("network_cidr", cidr)
	if setErr != nil {
		logger.Warnf("Failed to set network_cidr output: %v", setErr)
	}

	logger.Infof("Network created successfully: id=%s", network.ID)

	return nil
}

func (m *Manager) resolveNetworkCIDR() string {
	if m.config.Network.CIDR != "" {
		return m.config.Network.CIDR
	}

	return defaultNetworkCIDR
}

func (m *Manager) resolveNetworkName() string {
	// Use override from config if provided
	if m.config.Network.Name != "" {
		return m.config.Network.Name
	}

	// Otherwise use bloc-name + "-net"
	return m.options.BlocName + "-net"
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

// CIDR Utility Functions

// SplitParentIntoTwo splits a parent CIDR into two equal subnets.
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

// SplitIntoN splits a parent CIDR into N equal subnets.
// Exported for testing and utility use.
func SplitIntoN(parentCIDR string, count int) []string {
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

	// Get parent CIDR string for validation
	parentCIDR := parent.String()

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
		subnetCIDR := createSubnetCIDR(base, index, size, newPrefix)

		// Validate subnet is within parent network bounds
		if !IsSubnetWithinParent(parentCIDR, subnetCIDR) {
			logger.Errorf("Generated subnet %s is outside parent network %s bounds", subnetCIDR, parentCIDR)
			// Return only valid subnets generated so far
			return subnets[:subnetIndex]
		}

		subnets[subnetIndex] = subnetCIDR
	}

	return subnets
}

func createSubnetCIDR(base uint32, index uint32, size uint32, newPrefix int) string {
	subnetBase := base + (index * size)

	return fmt.Sprintf("%s/%d", uint32ToIP(subnetBase), newPrefix)
}

// CIDRFirstIP returns the first IP address in a CIDR block.
// Exported for testing and utility use.
func CIDRFirstIP(cidr string) string {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil || ipnet == nil {
		return ""
	}

	return ipnet.IP.String()
}

// CIDRLastUsableIP returns the last usable IP address in a CIDR block.
// Exported for testing and utility use.
func CIDRLastUsableIP(cidr string) string {
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

// CIDRGatewayIP returns the gateway IP for a CIDR block.
// Exported for testing and utility use.
func CIDRGatewayIP(parentCIDR string) string {
	_, ipnet, err := net.ParseCIDR(parentCIDR)
	if err != nil || ipnet == nil {
		return ""
	}

	base := ipToUint32(ipnet.IP.Mask(ipnet.Mask))

	return uint32ToIP(base + 1).String()
}

// IsSubnetWithinParent validates that a child subnet CIDR is completely contained
// within the parent network CIDR. This ensures carved subnets don't overflow the
// parent network boundaries.
// Exported for testing and validation use.
func IsSubnetWithinParent(parentCIDR, childCIDR string) bool {
	// Parse parent network
	_, parentNet, err := net.ParseCIDR(parentCIDR)
	if err != nil || parentNet == nil {
		return false
	}

	// Parse child subnet
	childIP, childNet, err := net.ParseCIDR(childCIDR)
	if err != nil || childNet == nil {
		return false
	}

	// Calculate parent network boundaries
	parentBase := ipToUint32(parentNet.IP.Mask(parentNet.Mask))
	parentMask, _ := parentNet.Mask.Size()
	parentSize := uint32(1) << (ipv4Bits - parentMask)
	parentLast := parentBase + parentSize - 1

	// Calculate child subnet boundaries
	childBase := ipToUint32(childIP.Mask(childNet.Mask))
	childMask, _ := childNet.Mask.Size()
	childSize := uint32(1) << (ipv4Bits - childMask)
	childLast := childBase + childSize - 1

	// Validate child is within parent
	// Child's first IP must be >= parent's first IP
	// Child's last IP must be <= parent's last IP
	return childBase >= parentBase && childLast <= parentLast
}

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

func ipToUint32(ip net.IP) uint32 {
	ip = ip.To4()

	return uint32(ip[0])<<shiftBy24Bits + uint32(ip[1])<<shiftBy16Bits + uint32(ip[2])<<shiftBy8Bits + uint32(ip[3])
}

func uint32ToIP(n uint32) net.IP {
	return net.IPv4(byte(n>>shiftBy24Bits&byteMask), byte(n>>shiftBy16Bits&byteMask), byte(n>>shiftBy8Bits&byteMask), byte(n&byteMask))
}

func isValidCount(count int) bool {
	return count > 0 && count <= maxInt32
}

func isValidIndex(index int) bool {
	return index >= 0 && index <= maxInt32
}

func errCannotSplitNetworkInto(count int) error {
	return fmt.Errorf("%w: %d", errCannotSplitNetwork, count)
}

// STACKIT Virtual Subnet Functions

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

func (m *Manager) createStackitTripleSubnets(cidr string, networkID interface{}) error {
	// Split into 4, skip first (reserved for infrastructure)
	allSubnets := SplitIntoN(cidr, tripleSubnetSplitCount+1)
	if len(allSubnets) < tripleSubnetSplitCount+1 {
		// Fallback to manual split
		allSubnets = m.generateFallbackChildren(cidr)
	}

	// Skip first subnet, use next 3
	subnets := allSubnets[1:]

	for i, subnetCIDR := range subnets {
		name := fmt.Sprintf("%s-ocfp-%d", m.options.BlocName, i)

		err := m.addVirtualSubnetWithDependency(name, subnetCIDR, cidr, networkID)
		if err != nil {
			return err
		}
	}

	return nil
}

func (m *Manager) generateFallbackChildren(cidr string) []string {
	child0 := firstChild24(cidr) // Reserved for infrastructure
	child1 := nextSibling24(child0)
	child2 := nextSibling24(child1)
	child3 := nextSibling24(child2)

	return []string{child0, child1, child2, child3}
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

// Standard Subnet Functions

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

	// Split parent CIDR into 4 subnets, skip the first (reserved for infrastructure)
	allSubnets := SplitIntoN(parent, tripleSubnetSplitCount+1)
	if len(allSubnets) < tripleSubnetSplitCount+1 {
		// Fallback to manual split
		allSubnets = m.generateFallbackChildren(parent)
	}

	// Skip first subnet (reserved), use next 3
	subnetCIDRs := allSubnets[1:]

	// Create 3 subnets with different AZs
	// STACKIT uses numeric AZ suffixes: {region}-{index+1} (e.g., eu01-1, eu01-2, eu01-3)
	subnets := make([]config.Subnet, 0, tripleSubnetSplitCount)

	for i := range tripleSubnetSplitCount {
		subnets = append(subnets, config.Subnet{
			Name:             fmt.Sprintf("%s-ocfp-%d", m.options.BlocName, i),
			CIDR:             subnetCIDRs[i],
			Type:             "public",
			AvailabilityZone: fmt.Sprintf("%s-%d", m.options.Region, i+1),
		})
	}

	m.saveDefaultSubnetOutputs(parent, subnetCIDRs)

	return subnets
}

func (m *Manager) saveDefaultSubnetOutputs(parent string, subnetCIDRs []string) {
	_ = m.stateManager.SetOutput("subnet_parent_cidr", parent)
	for i, cidr := range subnetCIDRs {
		_ = m.stateManager.SetOutput(fmt.Sprintf("subnet_ocfp_%d_cidr", i), cidr)
	}
}

func (m *Manager) validateNetworkID(networkID interface{}) (string, error) {
	netID, ok := networkID.(string)
	if !ok || netID == "" {
		return "", ErrInvalidNetworkID(networkID)
	}

	return netID, nil
}

// ==============================================================================
// Subnet Management Functions
// ==============================================================================

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
		"ip_0":        CIDRFirstIP(subnetCIDR),
		"ip_n":        CIDRLastUsableIP(subnetCIDR),
		"gateway":     CIDRGatewayIP(parentCIDR),
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

func (m *Manager) addSubnetDependency(subnetName string) {
	networkName := m.resolveNetworkName()
	_ = m.stateManager.AddDependency("subnet."+subnetName, "network."+networkName)
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
		AvailabilityZone: subnet.AvailabilityZone,
		Type:             subnet.Type,
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

func (m *Manager) saveSubnetOutputs(subnetName string, createdSubnet *cpi.Subnet) {
	_ = m.stateManager.SetOutput(fmt.Sprintf("subnet_%s_id", subnetName), createdSubnet.ID)
	_ = m.stateManager.SetOutput(fmt.Sprintf("subnet_%s_cidr", subnetName), createdSubnet.CIDR)

	if createdSubnet.AvailabilityZone != "" {
		_ = m.stateManager.SetOutput(fmt.Sprintf("subnet_%s_az", subnetName), createdSubnet.AvailabilityZone)
	}
}

// CalculateIPFromCIDR calculates an IP address from CIDR and offset.
// This matches the Perl implementation's cidrhost() function.
// Example: CalculateIPFromCIDR("10.0.0.0/24", 3) returns "10.0.0.3".
func CalculateIPFromCIDR(cidr string, offset int) (string, error) {
	if offset < 0 {
		return "", fmt.Errorf("%w: got %d", ErrInvalidCIDROffsetNegative, offset)
	}

	baseIP := CIDRFirstIP(cidr)
	if baseIP == "" {
		return "", fmt.Errorf("%w: %s", ErrInvalidCIDR, cidr)
	}

	base := ipToUint32(net.ParseIP(baseIP))

	if offset > int(^uint32(0)) {
		return "", fmt.Errorf("%w: %d", ErrOffsetOutOfRange, offset)
	}

	resultIP := uint32ToIP(base + uint32(offset)).String()

	return resultIP, nil
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

	base := ipToUint32(net.ParseIP(CIDRFirstIP(subnetCIDR)))
	last := ipToUint32(net.ParseIP(CIDRLastUsableIP(subnetCIDR)))

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

// ==============================================================================
// Display Functions
// ==============================================================================
