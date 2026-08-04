package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	pveclient "github.com/ocfp/ocfp-cli-go/internal/cpi/pve"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/netlayout"
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

	// Subnet role hints select which netlayout Layer A slot set a subnet
	// gets (see Manager.resolveReservedIPLayout). "infra" is the dedicated
	// bootstrap subnet's fixed layout; "ocfp" is a workload subnet, whose
	// statics come from the bloc's strategy at that subnet's index.
	subnetRoleInfra = "infra"
	subnetRoleOCFP  = "ocfp"

	// bastionIPSlot is the bastion VM's fallback static offset, used only
	// when slotForNamedIP cannot derive "bastion_ip" from the bloc's
	// resolved strategy. It matches the fixed infra layout
	// (internal/netlayout's infraBastionOffset) and Perl cidrhost().
	bastionIPSlot = 3
	// artifactsIPSlot is the RustFS blobstore VM's fallback static offset,
	// used only when slotForNamedIP cannot derive "artifacts_ip". See
	// plans/ocfp-artifacts-rustfs-vm.md.
	artifactsIPSlot = 11
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
		Properties: map[string]any{"cidr": cidr, "dns_servers": m.config.Network.DNSServers},
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
		Properties: map[string]any{"cidr": cidr, "dns_servers": m.config.Network.DNSServers},
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
func (m *Manager) createVirtualSubnets(ctx context.Context, networkID any) error {
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
func (m *Manager) createPVEVirtualSubnets(ctx context.Context, cidr string, networkID any) error {
	subnets := SplitToTargetPrefix(cidr, pveSubnetTargetPrefix, pveSubnetCount)
	if len(subnets) != pveSubnetCount {
		subnets = SplitIntoN(cidr, pveSubnetCount)
	}

	if len(subnets) != pveSubnetCount {
		return fmt.Errorf("%w: cannot carve %s into %d PVE subnets",
			errCannotSplitNetwork, cidr, pveSubnetCount)
	}

	// Enforce the bloc's configured strategy minimum BEFORE recording any
	// subnet (infra included): a strategy that needs more workload subnets
	// than this carve produces must reject the whole bloc, not record a
	// partial infra-only state ahead of the workload loop's own failure.
	if err := m.validateWorkloadSubnetCount("pve", subnets[1:]); err != nil {
		return err
	}

	// Subnet 0 → infra (no AZ assignment, hosts bastion/director/shared svc).
	// The infra role's slot set is fixed regardless of idx; -1 makes that
	// explicit rather than implying a workload position.
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
func (m *Manager) ensurePVESDNSubnets(ctx context.Context, networkID any, subnetCIDRs []string) error {
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

func (m *Manager) createStackitTripleSubnets(cidr string, networkID any) error {
	// Split into 4, skip first (reserved for infrastructure)
	allSubnets := SplitIntoN(cidr, tripleSubnetSplitCount+1)
	if len(allSubnets) < tripleSubnetSplitCount+1 {
		// Fallback to manual split
		allSubnets = m.generateFallbackChildren(cidr)
	}

	// Skip first subnet, use next 3
	subnets := allSubnets[1:]

	if err := m.validateWorkloadSubnetCount("stackit-triple", subnets); err != nil {
		return err
	}

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

func (m *Manager) createStackitSingleSubnet(cidr string, networkID any) error {
	if err := m.validateWorkloadSubnetCount("stackit-single", []string{cidr}); err != nil {
		return err
	}

	name := m.options.BlocName + "-subnet"

	// The single-subnet layout has no per-AZ index; -1 marks it as such, and
	// the layout answers it with the placements that apply to every subnet
	// index — never one pinned to a specific position it does not occupy
	// (see netlayout.Layout.LayerASlots).
	return m.addVirtualSubnetWithDependency(name, cidr, cidr, networkID, -1)
}

func (m *Manager) addVirtualSubnetWithDependency(name, subnetCIDR, parentCIDR string, networkID any, idx int) error {
	// Preserve the existing STACKIT/AWS contract: triple-subnet workload
	// children carry the "ocfp" role and an empty AZ (the caller did not
	// pass one, so we let downstream state lookups resolve from config).
	return m.addVirtualSubnetWithRole(name, subnetCIDR, parentCIDR, networkID, subnetRoleOCFP, "", idx)
}

// addVirtualSubnetWithRole records a virtual subnet with an explicit role hint
// and AZ. The role drives reserved-IP placement (infra vs. ocfp); az populates
// the subnet's availability_zone property and the matching state output. idx
// is the caller's own workload-subnet position (0/1/2 for PVE ocfp-N / STACKIT
// triple, -1 for infra/single-subnet layouts) and is passed to the layout
// rather than re-derived from name, so a strategy that pins a role to one
// position resolves it against the caller's own numbering.
func (m *Manager) addVirtualSubnetWithRole(name, subnetCIDR, parentCIDR string, networkID any, role, az string, idx int) error {
	slots, err := m.resolveReservedIPLayout(role, subnetCIDR, idx)
	if err != nil {
		return err
	}

	err = m.addVirtualSubnetToState(name, subnetCIDR, parentCIDR, networkID, role, az, slots)
	if err != nil {
		return err
	}

	m.addSubnetDependency(name)

	return nil
}

// validateWorkloadSubnetCount enforces that workloadCIDRs — the CIDRs a
// subnetStrategy is about to record as this bloc's workload subnets —
// satisfy the bloc's configured netlayout.Layout minimum subnet count
// (Layout.MinSubnets, checked via ValidateSubnetSet), before ANY subnet is
// recorded to state. strategyName identifies the caller for the wrapped
// error. Both Layer A (this function) and Layer B (internal/vault's
// provider Configure* paths) enforce the identical check against the same
// resolved layout, so a strategy that needs more workload subnets than a
// bloc's carve produces (e.g. stackit-single's one subnet against
// spanning's MinSubnets of 3) is rejected up front rather than silently
// producing a sparse table (see
// TestStackitSingleSubnet_SpanningDropsPinnedStatics).
func (m *Manager) validateWorkloadSubnetCount(strategyName string, workloadCIDRs []string) error {
	layout, err := m.config.ResolveReservedIPLayout()
	if err != nil {
		return fmt.Errorf("resolve reserved-ip layout strategy %q: %w", m.config.Network.Strategy, err)
	}

	if err := layout.ValidateSubnetSet(workloadCIDRs); err != nil {
		return fmt.Errorf("bootstrap %s strategy: %w", strategyName, err)
	}

	return nil
}

// resolveReservedIPLayout resolves the bloc's configured netlayout.Layout
// strategy (m.config.ResolveReservedIPLayout: m.config.Network.Strategy when
// set, else the provider/subnet-strategy default) and asks it for its
// named-slot/available-band offsets for role on subnetCIDR at
// workload-subnet index idx (-1 for the infra subnet and single-subnet
// layouts, which have no workload position), then applies any config-level
// available-band override (m.config.Network.Bands.Infra) on top of it.
// Every subnetStrategy shares this one resolution path — unlike the
// pre-netlayout scheme, where pveSubnetStrategy alone widened the ocfp
// role's band from its own CIDR, every strategy here reads the identical
// table internal/vault's reserved-ips population reads, so Layer A and Layer
// B can never disagree about where a role's statics or band sit. Returns an
// error if the strategy name is unrecognized, role is neither "infra" nor
// "ocfp", or a configured override is invalid for subnetCIDR.
func (m *Manager) resolveReservedIPLayout(role, subnetCIDR string, idx int) (netlayout.LayerASlots, error) {
	layout, err := m.config.ResolveReservedIPLayout()
	if err != nil {
		return netlayout.LayerASlots{}, fmt.Errorf("resolve reserved-ip layout strategy %q: %w",
			m.config.Network.Strategy, err)
	}

	slots, err := layout.LayerASlots(role, subnetCIDR, idx)
	if err != nil {
		return netlayout.LayerASlots{}, fmt.Errorf("resolve reserved-ip slots for role %q: %w", role, err)
	}

	return applyAvailableBandOverride(layout, slots, m.config.Network, subnetCIDR)
}

// subnetRoleAndIndex classifies a VM host subnet by name for slot
// resolution: a "-infra" suffix is PVE's dedicated infra subnet (fixed
// layout, no workload position), a trailing "-ocfp-<n>" segment is a
// workload subnet at index n, and anything else resolves as a workload
// subnet with no position (idx -1: only every-index placements apply).
func (m *Manager) subnetRoleAndIndex(subnetName string) (string, int) {
	if strings.HasSuffix(subnetName, pveInfraSubnetSuffix) {
		return subnetRoleInfra, -1
	}

	if i := strings.LastIndex(subnetName, "-ocfp-"); i >= 0 {
		if idx, err := strconv.Atoi(subnetName[i+len("-ocfp-"):]); err == nil {
			return subnetRoleOCFP, idx
		}
	}

	return subnetRoleOCFP, -1
}

// slotForNamedIP returns the static offset the bloc's resolved strategy
// assigns to ipKey (e.g. "bastion_ip", "artifacts_ip") on the named subnet,
// so VM placement follows a BYO strategy that relocates those statics
// instead of hardcoding the built-in offsets. fallback is returned — with a
// warning, never an error — when the strategy cannot resolve or places no
// such static on this subnet, preserving the historical placement for
// configs the strategy does not cover.
func (m *Manager) slotForNamedIP(subnetName, subnetCIDR, ipKey string, fallback int) int {
	role, idx := m.subnetRoleAndIndex(subnetName)

	slots, err := m.resolveReservedIPLayout(role, subnetCIDR, idx)
	if err != nil {
		logger.Warnf("Cannot resolve %s slot from strategy (using fallback offset %d): %v", ipKey, fallback, err)

		return fallback
	}

	for _, n := range slots.Named {
		if n.Key == ipKey {
			return n.Offset
		}
	}

	logger.Warnf("Strategy places no %s on subnet %s; using fallback offset %d", ipKey, subnetName, fallback)

	return fallback
}

// applyAvailableBandOverride overrides slots.AvailableA/AvailableB (and
// recomputes ReservedB as start-1 and ReservedC as end+1, so the replaced
// band stays self-consistent with its reserved complement) from
// netCfg.Bands.Infra.Start/End when both are configured. Zero values on both
// fields mean "no override": slots is returned unchanged and
// layout.ValidateBand is never called. Otherwise layout.ValidateBand
// validates the pair (partial pair, ordering, floor, subnet fit — see
// internal/netlayout's ValidateBand) before it is applied. The override
// applies uniformly to both infra and ocfp roles: it replaces whatever the
// strategy computed, it does not add to it.
func applyAvailableBandOverride(
	layout netlayout.Layout, slots netlayout.LayerASlots, netCfg config.NetworkConfig, subnetCIDR string,
) (netlayout.LayerASlots, error) {
	start := netCfg.Bands.Infra.Start
	end := netCfg.Bands.Infra.End

	if start == 0 && end == 0 {
		return slots, nil
	}

	if err := layout.ValidateBand(netlayout.TierInfra, subnetCIDR, start, end); err != nil {
		return netlayout.LayerASlots{}, err
	}

	slots.AvailableA = start
	slots.AvailableB = end
	slots.ReservedB = start - 1
	slots.ReservedC = end + 1

	return slots, nil
}

// Standard Subnet Functions

func (m *Manager) createStandardSubnets(ctx context.Context, networkID any) error {
	netID, err := m.validateNetworkID(networkID)
	if err != nil {
		return err
	}

	subnets := m.resolveSubnetsForCreation()

	// Layer A enforcement for provider-native subnets (AWS): the created
	// subnets are all workload subnets — generateDefaultSubnets skips the
	// infra child, and explicit Network.Subnets lists the workload set — so
	// the set must satisfy the resolved layout's MinSubnets before any
	// cloud subnet is created, matching the virtual-subnet paths above.
	// Every entry counts toward the floor — a subnet exists whether or not
	// its CIDR is known yet — while a CIDR-less (provider-assigned) entry
	// has no size for ValidateSubnetSet to check.
	cidrs := make([]string, 0, len(subnets))

	for _, subnet := range subnets {
		cidrs = append(cidrs, subnet.CIDR)
	}

	if err := m.validateWorkloadSubnetCount("standard", cidrs); err != nil {
		return err
	}

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

func (m *Manager) validateNetworkID(networkID any) (string, error) {
	netID, ok := networkID.(string)
	if !ok || netID == "" {
		return "", ErrInvalidNetworkID(networkID)
	}

	return netID, nil
}

// ==============================================================================
// Subnet Management Functions
// ==============================================================================

func (m *Manager) addVirtualSubnetToState(name string, subnetCIDR string, parentCIDR string, networkID any, role, az string, slots netlayout.LayerASlots) error {
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

	props := map[string]any{
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
	m.addReservedIPOutputs(name, subnetCIDR, slots)

	return nil
}

func (m *Manager) addSubnetDependency(subnetName string) {
	networkName := m.resolveNetworkName()
	_ = m.stateManager.AddDependency("subnet."+subnetName, "network."+networkName)
}

func (m *Manager) createSingleSubnet(ctx context.Context, subnet config.Subnet, netID string, networkID any) error {
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

func (m *Manager) saveSubnetToState(createdSubnet *cpi.Subnet, subnetName string, networkID any) error {
	// Save subnet to state
	err := m.stateManager.AddResource(&state.Resource{
		ID:       createdSubnet.ID,
		Type:     "subnet",
		Name:     subnetName,
		Provider: m.options.Provider,
		State:    string(cpi.ResourceStateActive),
		Properties: map[string]any{
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
// virtual subnet from slots, the netlayout.LayerASlots the caller
// (addVirtualSubnetWithRole) already resolved for this subnet's role and
// workload-subnet index. The role/index branching that used to live here is
// gone: slots already names exactly the statics that belong on THIS subnet,
// so a config-level band override or a strategy-specific layout never has to
// be threaded back through a string re-parse.
//
// These outputs are tier-blind by construction (slots carries no envType):
// the STACKIT and PVE vault providers therefore no longer read them
// to compute a tier's reserved-ips (both now compute independently from the
// subnet's own CIDR via the shared netlayout engine — see
// internal/vault/pve_reserved_ips.go's reservedIPsForSubnet, which both
// providers call). Deleting this writer outright would still regress two unrelated,
// non-Genesis consumers that read specific keys from these SAME outputs
// before vault is ever populated: internal/commands/bastion_lookup.go's
// last-resort bastion-IP fallback (reserved_<bloc>-ocfp-0_bastion_ip) and
// internal/commands/init.go's createBOSHManifest (reserved_<bloc>-ocfp-0_bosh_ip,
// a legacy non-Genesis manifest path). Those keys stay live; only the
// PVE/STACKIT vault-write path was made independent of them (see
// plans/pve-tiered-reserved-ip-map.md, "Retire tier-blind bootstrap
// reserved-IP outputs").
func (m *Manager) addReservedIPOutputs(name string, subnetCIDR string, slots netlayout.LayerASlots) {
	base := ipToUint32(net.ParseIP(CIDRFirstIP(subnetCIDR)))
	last := ipToUint32(net.ParseIP(CIDRLastUsableIP(subnetCIDR)))

	set := func(key, val string) { _ = m.stateManager.SetOutput(fmt.Sprintf("reserved_%s_%s", name, key), val) }
	ipAt := func(off int) string {
		if off < 0 || off > int(^uint32(0)) {
			panic("offset out of range for uint32")
		}

		return uint32ToIP(base + uint32(off)).String()
	}

	m.writeReservedIPNamedSlots(set, ipAt, slots)
	m.writeReservedIPBandOutputs(set, ipAt, slots, last)
}

// writeReservedIPNamedSlots writes the single-role-IP outputs (bastion_ip,
// bosh_ip, vault_ip, ...) for this subnet. Which statics belong here is the
// layout's decision, not this writer's: slots.Named already holds exactly
// the roles the strategy places on this subnet's role and index, each with
// the state-output stem the kits read.
func (m *Manager) writeReservedIPNamedSlots(set func(key, val string), ipAt func(off int) string, slots netlayout.LayerASlots) {
	for _, slot := range slots.Named {
		set(slot.Key, ipAt(slot.Offset))
	}
}

// writeReservedIPBandOutputs writes the available/reserved band outputs
// shared by both infra and ocfp subnets: available_a/b bound the allocation
// band Genesis' cloud-config IPAM reads; reserved_a/b/c/d bound the
// complement (reserved_a is always the subnet base, reserved_d is always its
// last usable IP; those two are structural, not part of the layout).
func (m *Manager) writeReservedIPBandOutputs(set func(key, val string), ipAt func(off int) string, slots netlayout.LayerASlots, last uint32) {
	set("available_a", ipAt(slots.AvailableA))
	set("available_b", ipAt(slots.AvailableB))
	set("reserved_a", ipAt(0))
	set("reserved_b", ipAt(slots.ReservedB))
	set("reserved_c", ipAt(slots.ReservedC))
	set("reserved_d", uint32ToIP(last).String())
}

// ==============================================================================
// Display Functions
// ==============================================================================
