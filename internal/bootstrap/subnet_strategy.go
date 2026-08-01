package bootstrap

import (
	"context"
	"strings"
)

// subnetStrategy encapsulates one way of carving a bloc's parent CIDR into the
// named subnets a provider needs. Each provider/network layout maps to exactly
// one strategy, selected by selectVirtualSubnetStrategy. This replaces the
// previous inline switch so new layouts are added as a strategy rather than a
// new case bolted onto the dispatch.
//
// Strategies operate on the already-resolved parent CIDR and network ID and own
// both the state-recorded virtual subnets and any real provider subnets they
// require (e.g. the PVE strategy also provisions real per-/22 SDN subnets).
//
// Reserved-IP layout (named-slot/available-band offsets) is not a strategy
// concern: every subnetStrategy shares one resolution path through the
// bloc's configured internal/netlayout.Layout (see
// Manager.resolveReservedIPLayout in network.go), so Layer A (this package's
// subnet carving) and Layer B (internal/vault's reserved-ips population)
// read the same table and can never disagree about where a role's band
// sits.
type subnetStrategy interface {
	// name identifies the strategy for logging/diagnostics.
	name() string
	// createSubnets carves parentCIDR and records/creates the subnets.
	createSubnets(ctx context.Context, m *Manager, parentCIDR string, networkID interface{}) error
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

// stackitTripleSubnetStrategy splits the parent into 4, skips the first
// (reserved for infrastructure), and records the next 3 as workload subnets.
type stackitTripleSubnetStrategy struct{}

func (stackitTripleSubnetStrategy) name() string { return "stackit-triple" }

func (stackitTripleSubnetStrategy) createSubnets(_ context.Context, m *Manager, parentCIDR string, networkID interface{}) error {
	return m.createStackitTripleSubnets(parentCIDR, networkID)
}

// stackitSingleSubnetStrategy records the whole parent CIDR as one subnet.
type stackitSingleSubnetStrategy struct{}

func (stackitSingleSubnetStrategy) name() string { return "stackit-single" }

func (stackitSingleSubnetStrategy) createSubnets(_ context.Context, m *Manager, parentCIDR string, networkID interface{}) error {
	return m.createStackitSingleSubnet(parentCIDR, networkID)
}
