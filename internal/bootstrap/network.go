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
	pveclient "github.com/ocfp/ocfp-cli-go/internal/cpi/pve"
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
	maxInt32               = 2147483647
	maxBitshift            = 30
	minSubnetSize          = 2
	byteMask               = 0xFF
	broadcastOffset        = 2
	dualSubnetSplitCount   = 2
	tripleSubnetSplitCount = 3
	subnetStrategyTriple   = "ocfp-triple"

	// PVE SDN simple-zone layout carves the parent vnet CIDR into one infra
	// subnet plus three AZ workload subnets. pveSubnetTargetPrefix is the
	// per-child mask (/22) and pveSubnetCount is the total number of children
	// (1 infra + 3 AZs). The infra subnet hosts the bastion, director, and
	// shared services; workload subnets back the BOSH AZs pvea/pveb/pvec.
	pveSubnetTargetPrefix = 22
	pveSubnetCount        = 4
	pveInfraSubnetSuffix  = "-infra"
	pveAZNamePrefix       = "pve"

	// Subnet role hints control reserved-IP assignment in addReservedIPOutputs.
	// "infra" routes bastion/director/shield/blacksmith reservations to the
	// dedicated infra subnet (PVE layout). "ocfp" preserves the legacy
	// STACKIT/AWS behavior of placing those reservations on the first
	// workload subnet.
	subnetRoleInfra = "infra"
	subnetRoleOCFP  = "ocfp"

	// Reserved IP slot constants for subnet IP assignment.
	vaultIPSlot      = 5
	jumpboxIPSlot    = 6
	concourseIPSlot  = 7
	prometheusIPSlot = 8
	bastionIPSlot    = 3
	boshIPSlot       = 4
	shieldIPSlot     = 9
	blacksmithIPSlot = 10
	doomsdayIPSlot   = 9
	shoutIPSlot      = 10
	// blacksmithOCFPIPSlot is the broker's static on workload subnet 1: the
	// blacksmith kit's ocfp blueprint resolves reserved-ips:blacksmith_ip from
	// ocfp-1 (broker pinned to z2). Slot 10 there is shout, and anything at or
	// above availableAIPSlot sits inside the PVE widened band, so the broker
	// reuses slot 3 — bastion's slot, which is only assigned on the infra
	// subnet (and on workload subnet 0 in the legacy STACKIT layout).
	blacksmithOCFPIPSlot = 3
	ocfpUIIPSlot         = 9
	// artifactsIPSlot is the RustFS blobstore VM. See plans/ocfp-artifacts-rustfs-vm.md.
	artifactsIPSlot     = 11
	availableAIPSlot    = 12
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

	// ErrBandOverridePartial is returned when only one of
	// network.availableBandStart/availableBandEnd is configured. Both must be
	// set together, or neither (to use the strategy's default layout).
	ErrBandOverridePartial = errors.New("network.availableBandStart and availableBandEnd must both be set, or neither")
	// ErrBandOverrideStartTooLow is returned when availableBandStart would
	// collide with the fixed named-slot offsets (0-11).
	ErrBandOverrideStartTooLow = errors.New("network.availableBandStart must be >= 12 to avoid colliding with reserved named-IP slots 0-11")
	// ErrBandOverrideEndNotAfterStart is returned when availableBandEnd does
	// not fall strictly after availableBandStart.
	ErrBandOverrideEndNotAfterStart = errors.New("network.availableBandEnd must be greater than availableBandStart")
	// ErrBandOverrideEndBeyondSubnet is returned when availableBandEnd falls
	// outside the target subnet's usable address range.
	ErrBandOverrideEndBeyondSubnet = errors.New("network.availableBandEnd is beyond the subnet's usable address range")
)

// reservedIPLayoutMinBandStart is the lowest offset an available-band
// override may start at: offsets 0-11 are the fixed named IP slots
// (bastion/bosh/vault/.../artifacts), so any override must start at or after
// the first free offset to avoid colliding with them.
const reservedIPLayoutMinBandStart = availableAIPSlot

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

	// Providers without native subnets use logical/virtual subnets backed by
	// state-only records. STACKIT has no subnet primitive; PVE bridge mode
	// is flat L2 (no per-subnet API). Either case routes through the same
	// state-population path that splits the parent CIDR into named children.
	if m.useVirtualSubnets() {
		return m.createVirtualSubnets(ctx, networkID)
	}

	return m.createStandardSubnets(ctx, networkID)
}

// useVirtualSubnets reports whether the current bloc should populate
// logical (state-only) subnets instead of calling the provider's
// CreateSubnet API.
func (m *Manager) useVirtualSubnets() bool {
	if m.config.Network.SubnetStrategy == subnetStrategyTriple {
		return true
	}

	switch strings.ToLower(m.options.Provider) {
	case "stackit", "pve":
		return true
	}

	return false
}

// useVirtualSubnetsForPVE reports whether the current bloc is the PVE
// provider specifically. It exists alongside useVirtualSubnets so call sites
// that branch on PVE semantics (e.g., carving multiple AZ subnets out of a
// single SDN vnet) can be explicit about the provider they handle rather
// than implicitly relying on the broader virtual-subnet predicate.
func (m *Manager) useVirtualSubnetsForPVE() bool {
	return strings.EqualFold(m.options.Provider, "pve")
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

	cidr := m.resolveImportedNetworkCIDR(network)

	err := m.stateManager.AddResource(&state.Resource{
		ID:         network.ID,
		Type:       "network",
		Name:       networkName,
		Provider:   m.options.Provider,
		State:      string(cpi.ResourceStateActive),
		Properties: map[string]interface{}{"cidr": cidr, "dns_servers": m.config.Network.DNSServers},
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

	setErr = m.stateManager.SetOutput("network_cidr", cidr)
	if setErr != nil {
		logger.Warnf("Failed to set network_cidr output: %v", setErr)
	}

	setErr = m.stateManager.SetOutput("network_name", networkName)
	if setErr != nil {
		logger.Warnf("Failed to set network_name output: %v", setErr)
	}

	return nil
}

// resolveImportedNetworkCIDR picks the CIDR to record for a discovered
// (already-existing) network resource.
//
// For PVE bridge mode, ListNetworks/GetNetwork report the physical bridge
// device's host-level address (e.g. a cluster node's own mgmt IP/24, such as
// 10.254.0.10/24) — there is no real "network" API resource to discover on
// PVE, only the shared vnet/bridge device the bloc's default_bridge/
// network.name point at. Trusting that host address as the bloc's network
// range corrupts every downstream computation that reads the network_cidr
// state output (subnet carve, bastion/static IP placement), landing VMs in
// the cluster's shared management range instead of the bloc's configured
// workload CIDR. So for PVE, the bloc config's network_cidr is authoritative
// once it's actually configured (not just the package default) — the
// discovery match only confirms the bridge exists, it is not a source of
// truth for addressing. Non-PVE providers (including the AWS vpc_id import
// path in importConfiguredNetwork, which never reports
// useVirtualSubnetsForPVE()==true) are unaffected and keep using the
// discovered network's own CIDR, as before.
func (m *Manager) resolveImportedNetworkCIDR(network *cpi.Network) string {
	if m.useVirtualSubnetsForPVE() {
		if configured := m.resolveNetworkCIDR(); configured != "" && configured != defaultNetworkCIDR {
			return configured
		}
	}

	return network.CIDR
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

// SplitToTargetPrefix splits a parent CIDR into `count` consecutive subnets of
// the requested `targetPrefix` length, walking the address space in fixed-size
// strides anchored at the parent's base address.
//
// Returns nil when the parent cannot accommodate count*2^(targetPrefix-parentPrefix)
// addresses, when the target prefix is not strictly longer than the parent's,
// or when any input is malformed. This is the semantic backbone of the PVE
// /22 carve: parent 10.64.64.0/18 → /22 × 4 yields
// [10.64.64.0/22, 10.64.68.0/22, 10.64.72.0/22, 10.64.76.0/22].
func SplitToTargetPrefix(parentCIDR string, targetPrefix, count int) []string {
	if !isValidCount(count) {
		return nil
	}

	if targetPrefix <= 0 || targetPrefix > ipv4Bits {
		return nil
	}

	parent, err := parseParentCIDR(parentCIDR)
	if err != nil {
		return nil
	}

	parentPrefix, _ := parent.Mask.Size()
	if targetPrefix <= parentPrefix {
		return nil
	}

	// Ensure the parent has enough address space for count children at the
	// requested prefix. parentSize / childSize == 2^(targetPrefix-parentPrefix).
	maxChildren := 1 << (targetPrefix - parentPrefix)
	if count > maxChildren {
		return nil
	}

	base := ipToUint32(parent.IP.Mask(parent.Mask))
	stride := uint32(1) << (ipv4Bits - targetPrefix)
	parentCIDRStr := parent.String()
	subnets := make([]string, count)

	for i := range count {
		if !isValidIndex(i) {
			return nil
		}

		subnetCIDR := createSubnetCIDR(base, uint32(i), stride, targetPrefix)
		if !IsSubnetWithinParent(parentCIDRStr, subnetCIDR) {
			return nil
		}

		subnets[i] = subnetCIDR
	}

	return subnets
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

// createVirtualSubnets carves the bloc parent CIDR into the named subnets the
// provider needs, dispatching to the selected subnetStrategy (PVE / STACKIT
// triple / STACKIT single). Each strategy owns its own carve and any real
// provider subnets it requires; see subnet_strategy.go.
func (m *Manager) createVirtualSubnets(ctx context.Context, networkID interface{}) error {
	cidr := m.resolveNetworkCIDRForSubnets()
	strategy := m.selectVirtualSubnetStrategy()

	logger.Infof("Creating virtual subnets via %s strategy (parent %s)", strategy.name(), cidr)

	return strategy.createSubnets(ctx, m, cidr, networkID)
}

// createPVEVirtualSubnets carves the PVE bloc's parent CIDR into the
// infra+AZ layout described above. Subnet 0 is `{bloc}-infra` and hosts
// bastion/director/shield/blacksmith reservations; subnets 1..3 are
// `{bloc}-ocfp-{0,1,2}` aligned to AZs pvea/pveb/pvec.
//
// If the parent CIDR is narrower than the target /22 prefix (so a
// SplitToTargetPrefix call would fail), the carve falls back to an even
// 4-way split via SplitIntoN. This keeps small test/dev CIDRs functional
// while preserving the canonical layout for production /18 parents.
func (m *Manager) createPVEVirtualSubnets(ctx context.Context, cidr string, networkID interface{}) error {
	subnets := SplitToTargetPrefix(cidr, pveSubnetTargetPrefix, pveSubnetCount)
	if len(subnets) != pveSubnetCount {
		subnets = SplitIntoN(cidr, pveSubnetCount)
	}

	if len(subnets) != pveSubnetCount {
		return fmt.Errorf("%w: cannot carve %s into %d PVE subnets",
			errCannotSplitNetwork, cidr, pveSubnetCount)
	}

	// Subnet 0 → infra (no AZ assignment, hosts bastion/director/shared svc).
	// idx is irrelevant for the infra role (addReservedIPOutputs never
	// branches on it there); -1 makes that explicit rather than implying a
	// workload position.
	infraName := m.options.BlocName + pveInfraSubnetSuffix

	err := m.addVirtualSubnetWithRole(infraName, subnets[0], cidr, networkID, subnetRoleInfra, "", -1)
	if err != nil {
		return err
	}

	// Subnets 1..3 → ocfp-{0,1,2} mapped to AZs pvea/pveb/pvec.
	for i := 1; i < pveSubnetCount; i++ {
		ocfpIdx := i - 1
		name := fmt.Sprintf("%s-ocfp-%d", m.options.BlocName, ocfpIdx)
		az := pveAZNamePrefix + string(rune('a'+ocfpIdx))

		err := m.addVirtualSubnetWithRole(name, subnets[i], cidr, networkID, subnetRoleOCFP, az, ocfpIdx)
		if err != nil {
			return err
		}
	}

	// Provision the matching REAL SDN subnets so each /22 has a routed gateway
	// (.X.1) the PVE host answers. Without these, the per-/22 gateways written
	// to vault would be unroutable and guest egress would break. No-op in PVE
	// bridge mode (no native subnet API).
	return m.ensurePVESDNSubnets(ctx, networkID, subnets)
}

// ensurePVESDNSubnets creates one real PVE SDN subnet per carved /22, each with
// its own in-range gateway (first host) and SNAT enabled. Creation is
// idempotent: the PVE CreateSubnet reuses an existing subnet that already
// contains the requested CIDR. In bridge mode the provider has no subnet API
// and returns ErrSubnetsNotSupported, which is treated as a benign skip (the
// state-only virtual subnets are sufficient there).
func (m *Manager) ensurePVESDNSubnets(ctx context.Context, networkID interface{}, subnetCIDRs []string) error {
	netMgr := m.provider.NetworkManager()
	vnet := fmt.Sprintf("%v", networkID)

	for _, subnetCIDR := range subnetCIDRs {
		gateway := CIDRGatewayIP(subnetCIDR)

		_, err := netMgr.CreateSubnet(ctx, &cpi.SubnetRequest{
			Name:      strings.ReplaceAll(subnetCIDR, "/", "-"),
			NetworkID: vnet,
			CIDR:      subnetCIDR,
			Type:      "public",
			Gateway:   gateway,
			SNAT:      true,
			Tags:      m.baseTags(),
		})
		if err != nil {
			if errors.Is(err, pveclient.ErrSubnetsNotSupported) {
				logger.Infof("PVE bridge mode: skipping real SDN subnet for %s (virtual subnet only)", subnetCIDR)

				return nil
			}

			return fmt.Errorf("failed to create SDN subnet %s (gw %s): %w", subnetCIDR, gateway, err)
		}

		logger.Infof("Ensured PVE SDN subnet %s with gateway %s (SNAT)", subnetCIDR, gateway)
	}

	return nil
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

		err := m.addVirtualSubnetWithDependency(name, subnetCIDR, cidr, networkID, i)
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

	// The single-subnet layout has no per-AZ index; -1 marks it as such and
	// is treated the same as any other idx that isn't 0/1/2 by
	// addReservedIPOutputs (vault/jumpbox/concourse/prometheus/artifacts +
	// the available/reserved band, no bastion/doomsday/ocfp_ui statics).
	return m.addVirtualSubnetWithDependency(name, cidr, cidr, networkID, -1)
}

func (m *Manager) addVirtualSubnetWithDependency(name, subnetCIDR, parentCIDR string, networkID interface{}, idx int) error {
	// Preserve the existing STACKIT/AWS contract: triple-subnet workload
	// children carry the "ocfp" role and an empty AZ (the caller did not
	// pass one, so we let downstream state lookups resolve from config).
	return m.addVirtualSubnetWithRole(name, subnetCIDR, parentCIDR, networkID, subnetRoleOCFP, "", idx)
}

// addVirtualSubnetWithRole records a virtual subnet with an explicit role hint
// and AZ. The role drives reserved-IP placement (infra vs. ocfp); az populates
// the subnet's availability_zone property and the matching state output. idx
// is the caller's own workload-subnet position (0/1/2 for PVE ocfp-N / STACKIT
// triple, -1 for infra/single-subnet layouts) and is forwarded verbatim to
// addReservedIPOutputs rather than re-derived from name.
func (m *Manager) addVirtualSubnetWithRole(name, subnetCIDR, parentCIDR string, networkID interface{}, role, az string, idx int) error {
	layout, err := m.resolveReservedIPLayout(role, subnetCIDR)
	if err != nil {
		return err
	}

	err = m.addVirtualSubnetToState(name, subnetCIDR, parentCIDR, networkID, role, az, idx, layout)
	if err != nil {
		return err
	}

	m.addSubnetDependency(name)

	return nil
}

// resolveReservedIPLayout asks the bloc's selected subnetStrategy for its
// named-slot/available-band layout, then applies any config-level available-
// band override (m.config.Network.AvailableBandStart/End) on top of it.
// Returns an error if an override is configured but invalid for subnetCIDR.
func (m *Manager) resolveReservedIPLayout(role, subnetCIDR string) (reservedIPLayout, error) {
	strategy := m.selectVirtualSubnetStrategy()
	layout := strategy.reservedIPLayout(role, subnetCIDR)

	return applyAvailableBandOverride(layout, m.config.Network, subnetCIDR)
}

// applyAvailableBandOverride overrides layout.availableA/availableB (and
// forces reservedC to end+1) from netCfg.AvailableBandStart/End when both are
// configured. Zero values on both fields mean "no override" and layout is
// returned unchanged. The override applies uniformly to both infra and ocfp
// roles: it replaces whatever the strategy computed, it does not add to it.
func applyAvailableBandOverride(layout reservedIPLayout, netCfg config.NetworkConfig, subnetCIDR string) (reservedIPLayout, error) {
	start := netCfg.AvailableBandStart
	end := netCfg.AvailableBandEnd

	if start == 0 && end == 0 {
		return layout, nil
	}

	if start == 0 || end == 0 {
		return reservedIPLayout{}, ErrBandOverridePartial
	}

	if start < reservedIPLayoutMinBandStart {
		return reservedIPLayout{}, fmt.Errorf("%w: got %d", ErrBandOverrideStartTooLow, start)
	}

	if end <= start {
		return reservedIPLayout{}, fmt.Errorf("%w: start=%d end=%d", ErrBandOverrideEndNotAfterStart, start, end)
	}

	total, ok := subnetTotalSize(subnetCIDR)
	if !ok {
		return reservedIPLayout{}, fmt.Errorf("%w: %s", ErrInvalidCIDR, subnetCIDR)
	}

	// Last usable offset within the subnet, relative to its base address
	// (excludes the network and broadcast addresses).
	lastUsableOffset := int(total) - broadcastOffset
	if end > lastUsableOffset {
		return reservedIPLayout{}, fmt.Errorf("%w: end=%d last-usable-offset=%d subnet=%s",
			ErrBandOverrideEndBeyondSubnet, end, lastUsableOffset, subnetCIDR)
	}

	layout.availableA = start
	layout.availableB = end
	layout.reservedC = end + 1

	return layout, nil
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
	// AZ names come from config (e.g., AWS: us-east-1a) with STACKIT-style numeric fallback
	subnets := make([]config.Subnet, 0, tripleSubnetSplitCount)

	for i := range tripleSubnetSplitCount {
		subnets = append(subnets, config.Subnet{
			Name:             fmt.Sprintf("%s-ocfp-%d", m.options.BlocName, i),
			CIDR:             subnetCIDRs[i],
			Type:             "public",
			AvailabilityZone: m.getAvailabilityZone(i),
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

func (m *Manager) addVirtualSubnetToState(name string, subnetCIDR string, parentCIDR string, networkID interface{}, role, az string, idx int, layout reservedIPLayout) error {
	// Skip if already present
	if existingSubnet, _ := m.stateManager.GetResource("subnet", name); existingSubnet != nil {
		logger.Infof("Virtual subnet %s already recorded, skipping", name)

		return nil
	}

	// Default unknown/empty role to "ocfp" so legacy callers retain their
	// reserved-IP semantics unchanged.
	if role == "" {
		role = subnetRoleOCFP
	}

	props := map[string]interface{}{
		"cidr":              subnetCIDR,
		"availability_zone": az,
		"network_id":        networkID,
		"type":              "public",
		"virtual":           true,
		"role":              role,
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
			t["role"] = role

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

	if az != "" {
		_ = m.stateManager.SetOutput(fmt.Sprintf("subnet_%s_az", name), az)
	}

	// Reserved IP role assignments (STACKIT parity for "ocfp"; PVE infra
	// reservations when role == infra).
	m.addReservedIPOutputs(name, subnetCIDR, role, idx, layout)

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

// addReservedIPOutputs writes the reserved_<name>_* state outputs for a
// virtual subnet, using layout for the named-slot/available-band offsets and
// idx for the caller's own workload-subnet position (0/1/2, or -1 for
// infra/single-subnet layouts that don't have one). idx and layout are always
// supplied by the caller (addVirtualSubnetWithRole) rather than derived from
// name, so a config-level band override or a strategy-specific layout never
// has to be threaded back through a string re-parse.
//
// These outputs are tier-blind by construction (role/idx/layout carry no
// envType): the STACKIT and PVE vault providers therefore no longer read them
// to compute a tier's reserved-ips (both now compute independently from the
// subnet's own CIDR plus a per-envType assignment table — see
// internal/vault/stackit_provider.go's getDefaultReservedIPAssignments and
// internal/vault/pve_reserved_ips.go's pveDefaultReservedIPAssignments).
// Deleting this writer outright would still regress two unrelated,
// non-Genesis consumers that read specific keys from these SAME outputs
// before vault is ever populated: internal/commands/bastion_lookup.go's
// last-resort bastion-IP fallback (reserved_<bloc>-ocfp-0_bastion_ip) and
// internal/commands/init.go's createBOSHManifest (reserved_<bloc>-ocfp-0_bosh_ip,
// a legacy non-Genesis manifest path). Those keys stay live; only the
// PVE/STACKIT vault-write path was made independent of them (see
// plans/pve-tiered-reserved-ip-map.md, "Retire tier-blind bootstrap
// reserved-IP outputs").
func (m *Manager) addReservedIPOutputs(name string, subnetCIDR string, role string, idx int, layout reservedIPLayout) {
	base := ipToUint32(net.ParseIP(CIDRFirstIP(subnetCIDR)))
	last := ipToUint32(net.ParseIP(CIDRLastUsableIP(subnetCIDR)))

	set := func(key, val string) { _ = m.stateManager.SetOutput(fmt.Sprintf("reserved_%s_%s", name, key), val) }
	ipAt := func(off int) string {
		if off < 0 || off > int(^uint32(0)) {
			panic("offset out of range for uint32")
		}

		return uint32ToIP(base + uint32(off)).String()
	}

	isInfra := role == subnetRoleInfra

	m.writeReservedIPNamedSlots(set, ipAt, layout, isInfra, idx)
	m.writeReservedIPBandOutputs(set, ipAt, layout, last)
}

// writeReservedIPNamedSlots writes the single-role-IP outputs (bastion,
// bosh/director, vault, jumpbox, ...). Infra subnets host
// bastion/director/shield/blacksmith reservations alongside the shared mgmt
// set. OCFP workload subnets keep the legacy STACKIT-style "idx==0 owns
// bastion" semantics so existing providers stay byte-identical.
func (m *Manager) writeReservedIPNamedSlots(set func(key, val string), ipAt func(off int) string, layout reservedIPLayout, isInfra bool, idx int) {
	if isInfra {
		set("bastion_ip", ipAt(layout.bastion))
		set("bosh_ip", ipAt(layout.bosh))
		set("shield_ip", ipAt(layout.shield))
		set("blacksmith_ip", ipAt(layout.blacksmith))

		return
	}

	// Single-IP assignments for mgmt. All ocfp subnets get these.
	set("vault_ip", ipAt(layout.vault))
	set("jumpbox_ip", ipAt(layout.jumpbox))
	set("concourse_ip", ipAt(layout.concourse))
	set("prometheus_ip", ipAt(layout.prometheus))

	// Conditional per-subnet.
	if idx == 0 {
		set("bastion_ip", ipAt(layout.bastion))
		set("bosh_ip", ipAt(layout.bosh))
		set("shield_ip", ipAt(layout.shield))
		set("blacksmith_ip", ipAt(layout.blacksmith))
	}

	if idx == 1 {
		set("doomsday_ip", ipAt(layout.doomsday))
		set("shout_ip", ipAt(layout.shout))
		set("blacksmith_ip", ipAt(layout.blacksmithOCFP))
	}

	if idx == ocfpUIProviderIndex {
		set("ocfp_ui_ip", ipAt(layout.ocfpUI))
	}

	// TODO: gate on Artifacts.Enabled once config wired
	set("artifacts_ip", ipAt(layout.artifacts))
}

// writeReservedIPBandOutputs writes the available/reserved band outputs
// shared by both infra and ocfp subnets: available_a/b bound the allocation
// band Genesis' cloud-config IPAM reads; reserved_a/b/c/d bound the
// complement (reserved_a is always the subnet base, reserved_d is always its
// last usable IP; those two are structural, not part of layout).
func (m *Manager) writeReservedIPBandOutputs(set func(key, val string), ipAt func(off int) string, layout reservedIPLayout, last uint32) {
	set("available_a", ipAt(layout.availableA))
	set("available_b", ipAt(layout.availableB))
	set("reserved_a", ipAt(0))
	set("reserved_b", ipAt(layout.reservedB))
	set("reserved_c", ipAt(layout.reservedC))
	set("reserved_d", uint32ToIP(last).String())
}

// ==============================================================================
// Display Functions
// ==============================================================================
