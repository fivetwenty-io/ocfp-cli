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

// InfraSlots is the Layer A named-slot set — today's
// bootstrap.reservedIPLayout, exported here so both layers can share one
// type. It is populated by Layout.Slots.
type InfraSlots struct {
	Bastion        int
	Bosh           int
	Vault          int
	Jumpbox        int
	Concourse      int
	Prometheus     int
	Shield         int
	Blacksmith     int
	BlacksmithOCFP int
	Doomsday       int
	Shout          int
	OCFPUI         int
	Artifacts      int
	AvailableA     int
	AvailableB     int
	ReservedB      int
	ReservedC      int
}

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

// Layout describes one reserved-IP numbering strategy. Implementations are
// registered in registry.go and obtained via Lookup or Default — callers
// never construct a Layout directly.
//
// Stub-safety: for as long as a registered strategy's real behavior has not
// yet landed, its Layout methods that can return an error return
// ErrNotImplemented rather than a silently-wrong zero value or a panic. Name
// is exempt — it is real for every registered strategy from registration
// onward, since it carries no table-generation logic.
type Layout interface {
	// Name returns the strategy's registry key, e.g. "wide" or "compact".
	Name() string

	// SchemeVersion returns the guard-stamped scheme identity written
	// beside a bloc's reserved-ips record, e.g. "2" or "3-compact".
	SchemeVersion() string

	// WorkloadTable returns the Layer B assignment table for cidr.
	// Strategies whose table does not vary by subnet size may ignore cidr.
	WorkloadTable(cidr string) (reservedip.AssignmentTable, error)

	// Slots returns the Layer A named-slot set for role ("infra" or
	// "ocfp") on cidr.
	//
	// The error return lets not-yet-implemented strategies participate
	// in the same ErrNotImplemented stub-safety contract as
	// WorkloadTable and ValidateSubnet.
	Slots(role, cidr string) (InfraSlots, error)

	// MinPrefix returns the minimum CIDR prefix length (e.g. 25, 26) this
	// strategy fits in.
	MinPrefix() int

	// ValidateSubnet returns an error if cidr is too small for this
	// strategy's highest fixed offset.
	ValidateSubnet(cidr string) error

	// ValidateBand returns an error if [start,end] is not a valid
	// override band for tier on cidr.
	ValidateBand(tier Tier, cidr string, start, end int) error
}
