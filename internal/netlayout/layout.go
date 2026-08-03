// Package netlayout defines the reserved-IP layout strategy abstraction
// shared by Layer A (bootstrap subnet carving) and Layer B (vault
// reserved-ips population). A Layout answers "which named role gets which
// offset" for one strategy; callers select a strategy by name via Lookup or
// Default and never construct a Layout directly.
package netlayout

import "github.com/ocfp/ocfp-cli-go/internal/reservedip"

// Tier identifies which reserved-IP tier a Layout operation applies to.
type Tier string

const (
	// TierMgmt is the management-plane tier (bastion, vault, concourse, ...).
	TierMgmt Tier = "mgmt"
	// TierOCF is the OCF/workload tier (bosh, blacksmith, haproxy, ...).
	TierOCF Tier = "ocf"
	// TierInfra is the Layer A bootstrap-subnet tier.
	TierInfra Tier = "infra"
)

// NamedSlot is one Layer A named static: Key is the full state-output stem
// ("bastion_ip", or an ip_key like "rustfs_ip_smoke").
type NamedSlot struct {
	Key    string
	Offset int
}

// LayerASlots is the Layer A slot set for one (role, subnet index).
type LayerASlots struct {
	Named      []NamedSlot
	AvailableA int
	AvailableB int
	ReservedB  int
	ReservedC  int
}

// Layout describes one reserved-IP numbering strategy. Every implementation
// is a *compiledLayout built from a Definition (see compiled.go); callers
// obtain one via Lookup or Default and never construct a Layout directly.
type Layout interface {
	// Name returns the strategy's registry key, e.g. "wide" or "compact".
	Name() string

	// SchemeVersion returns the guard-stamped scheme identity written
	// beside a bloc's reserved-ips record, e.g. "2" or "3-compact".
	SchemeVersion() string

	// Placement returns how the strategy distributes roles across a bloc's
	// workload subnets (colocated or spanning).
	Placement() Placement

	// MinPrefix returns the minimum CIDR prefix length (e.g. 25, 26) this
	// strategy fits in.
	MinPrefix() int

	// MinSubnets returns the minimum number of workload subnets this
	// strategy needs (1 for colocated placements).
	MinSubnets() int

	// WorkloadTable returns the Layer B assignment table for cidr.
	// Strategies whose table does not vary by subnet size may ignore cidr.
	WorkloadTable(cidr string) (reservedip.AssignmentTable, error)

	// LayerASlots returns the Layer A named-slot set for role ("infra" or
	// "ocfp") on cidr at workload-subnet index idx. A negative idx means
	// "no workload position" (the infra subnet, or a single-subnet
	// layout): only placements that apply to every index are returned.
	LayerASlots(role, cidr string, idx int) (LayerASlots, error)

	// ValidateSubnet returns an error if cidr is too small for this
	// strategy's highest fixed offset.
	ValidateSubnet(cidr string) error

	// ValidateSubnetSet returns an error if cidrs holds fewer subnets than
	// this strategy needs, or if any one of them fails ValidateSubnet.
	ValidateSubnetSet(cidrs []string) error

	// ValidateBand returns an error if [start,end] is not a valid
	// override band for tier on cidr.
	ValidateBand(tier Tier, cidr string, start, end int) error
}
