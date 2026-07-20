package bootstrap

import (
	"context"
	"net"
	"strings"
)

// reservedIPLayoutSmallSubnetUsableFloor is the minimum usable-host count a
// workload subnet must have before pveSubnetStrategy will widen the available
// band past the shared default. Subnets narrower than this (e.g. hand-crafted
// test /27s) fall back to the constant layout rather than computing a
// near-empty or negative band.
const reservedIPLayoutSmallSubnetUsableFloor = 64

// reservedIPLayoutPVEWideBandBuffer is subtracted from half the workload
// subnet's total address count to derive availableB, leaving a small gap
// before reservedC (availableB+1) rather than splitting the subnet exactly
// down the middle.
const reservedIPLayoutPVEWideBandBuffer = 3

// reservedIPLayout is the strategy-owned set of named-slot and available/
// reserved-band offsets used by addReservedIPOutputs. Offsets are relative to
// the subnet's first IP (subnet base address). Only fields that vary by
// strategy/role/subnet size are represented here; reserved_a (offset 0) and
// reserved_d (the subnet's last usable IP) are structural and are not part of
// the layout.
type reservedIPLayout struct {
	bastion    int
	bosh       int
	vault      int
	jumpbox    int
	concourse  int
	prometheus int
	shield     int
	blacksmith int
	// blacksmithOCFP is the broker's slot on workload subnet 1 (the kit
	// resolves blacksmith_ip from ocfp-1); blacksmith above is the infra /
	// legacy workload-0 slot.
	blacksmithOCFP int
	doomsday       int
	shout          int
	ocfpUI         int
	artifacts      int
	availableA     int
	availableB     int
	reservedB      int
	reservedC      int
}

// defaultReservedIPLayout returns the layout matching today's package-level
// slot constants. It is the layout every strategy falls back to unless it has
// a role/subnet-size-specific reason to diverge (see pveSubnetStrategy).
func defaultReservedIPLayout() reservedIPLayout {
	return reservedIPLayout{
		bastion:        bastionIPSlot,
		bosh:           boshIPSlot,
		vault:          vaultIPSlot,
		jumpbox:        jumpboxIPSlot,
		concourse:      concourseIPSlot,
		prometheus:     prometheusIPSlot,
		shield:         shieldIPSlot,
		blacksmith:     blacksmithIPSlot,
		blacksmithOCFP: blacksmithOCFPIPSlot,
		doomsday:       doomsdayIPSlot,
		shout:          shoutIPSlot,
		ocfpUI:         ocfpUIIPSlot,
		artifacts:      artifactsIPSlot,
		availableA:     availableAIPSlot,
		availableB:     availableBIPSlot,
		reservedB:      reservedBIPSlot,
		reservedC:      reservedCIPSlot,
	}
}

// subnetTotalSize returns the total number of addresses (2^(32-prefix)) in
// subnetCIDR, and whether it could be parsed as an IPv4 CIDR.
func subnetTotalSize(subnetCIDR string) (uint32, bool) {
	_, ipnet, err := net.ParseCIDR(subnetCIDR)
	if err != nil || ipnet == nil {
		return 0, false
	}

	prefixLen, bits := ipnet.Mask.Size()
	if bits != ipv4Bits || prefixLen < 0 || prefixLen > ipv4Bits {
		return 0, false
	}

	return uint32(1) << (ipv4Bits - prefixLen), true
}

// subnetStrategy encapsulates one way of carving a bloc's parent CIDR into the
// named subnets a provider needs. Each provider/network layout maps to exactly
// one strategy, selected by selectVirtualSubnetStrategy. This replaces the
// previous inline switch so new layouts are added as a strategy rather than a
// new case bolted onto the dispatch.
//
// Strategies operate on the already-resolved parent CIDR and network ID and own
// both the state-recorded virtual subnets and any real provider subnets they
// require (e.g. the PVE strategy also provisions real per-/22 SDN subnets).
type subnetStrategy interface {
	// name identifies the strategy for logging/diagnostics.
	name() string
	// createSubnets carves parentCIDR and records/creates the subnets.
	createSubnets(ctx context.Context, m *Manager, parentCIDR string, networkID interface{}) error
	// reservedIPLayout returns the named-slot/available-band offsets this
	// strategy uses for a subnet with the given role ("infra"/"ocfp") and
	// CIDR. Implementations that don't vary by role/CIDR simply return
	// defaultReservedIPLayout().
	reservedIPLayout(role, subnetCIDR string) reservedIPLayout
}

// selectVirtualSubnetStrategy picks the strategy for the current bloc. PVE has a
// dedicated layout (infra + 3 AZ /22 children, each a real SDN subnet with its
// own gateway). Otherwise the STACKIT-style strategies apply, keyed by
// params.subnet_strategy: "single" -> one subnet, anything else (incl. the
// canonical "ocfp-triple") -> the triple-subnet carve.
func (m *Manager) selectVirtualSubnetStrategy() subnetStrategy {
	if m.useVirtualSubnetsForPVE() {
		return pveSubnetStrategy{}
	}

	if strings.Contains(m.config.Network.SubnetStrategy, "single") {
		return stackitSingleSubnetStrategy{}
	}

	return stackitTripleSubnetStrategy{}
}

// pveSubnetStrategy carves the parent CIDR into 1 infra + 3 AZ /22 children and
// provisions a matching real SDN subnet per /22 (own gateway + SNAT).
type pveSubnetStrategy struct{}

func (pveSubnetStrategy) name() string { return "pve" }

func (pveSubnetStrategy) createSubnets(ctx context.Context, m *Manager, parentCIDR string, networkID interface{}) error {
	return m.createPVEVirtualSubnets(ctx, parentCIDR, networkID)
}

// reservedIPLayout keeps the shared 12-29 available band on the infra subnet
// (bastion/director/shield/blacksmith live there, so there is no room to
// widen it) but sizes the workload (role=="ocfp") band from the subnet's own
// CIDR: PVE workload children are real /22s with ~1020 usable hosts, and the
// default 12-29 band leaves nearly all of that address space unassigned. The
// available band is widened to roughly the lower half of the subnet, with
// reservedC immediately following it and the tail (up to the last usable IP)
// left reserved. Subnets too small to safely widen (e.g. hand-crafted test
// CIDRs) fall back to the constant layout unchanged.
func (pveSubnetStrategy) reservedIPLayout(role, subnetCIDR string) reservedIPLayout {
	if role != subnetRoleOCFP {
		return defaultReservedIPLayout()
	}

	total, ok := subnetTotalSize(subnetCIDR)
	if !ok {
		return defaultReservedIPLayout()
	}

	usable := total - broadcastOffset
	if usable < reservedIPLayoutSmallSubnetUsableFloor {
		return defaultReservedIPLayout()
	}

	layout := defaultReservedIPLayout()
	half := total / 2
	layout.availableB = int(half) - reservedIPLayoutPVEWideBandBuffer
	layout.reservedC = layout.availableB + 1

	return layout
}

// stackitTripleSubnetStrategy splits the parent into 4, skips the first
// (reserved for infrastructure), and records the next 3 as workload subnets.
type stackitTripleSubnetStrategy struct{}

func (stackitTripleSubnetStrategy) name() string { return "stackit-triple" }

func (stackitTripleSubnetStrategy) createSubnets(_ context.Context, m *Manager, parentCIDR string, networkID interface{}) error {
	return m.createStackitTripleSubnets(parentCIDR, networkID)
}

// reservedIPLayout returns the historical constant layout unchanged: STACKIT
// triple children are /22-or-narrower carves of a much smaller parent than
// PVE's, and widening the band here would be a behavior change existing
// STACKIT blocs don't need.
func (stackitTripleSubnetStrategy) reservedIPLayout(_, _ string) reservedIPLayout {
	return defaultReservedIPLayout()
}

// stackitSingleSubnetStrategy records the whole parent CIDR as one subnet.
type stackitSingleSubnetStrategy struct{}

func (stackitSingleSubnetStrategy) name() string { return "stackit-single" }

func (stackitSingleSubnetStrategy) createSubnets(_ context.Context, m *Manager, parentCIDR string, networkID interface{}) error {
	return m.createStackitSingleSubnet(parentCIDR, networkID)
}

// reservedIPLayout returns the historical constant layout unchanged; the
// single-subnet strategy records the whole parent CIDR as one subnet and has
// no per-AZ widening need.
func (stackitSingleSubnetStrategy) reservedIPLayout(_, _ string) reservedIPLayout {
	return defaultReservedIPLayout()
}
