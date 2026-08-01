package vault

import (
	"errors"
	"fmt"
	"maps"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/netlayout"
	"github.com/ocfp/ocfp-cli-go/internal/reservedip"
	"go.uber.org/zap"
)

// pveMgmtBandOverrideFloor/Ceiling bound an operator-supplied
// Network.AvailableBandStart/End override (see applyPVEMgmtBandOverride):
// the override must stay inside the mgmt static zone's ceiling (>=32,
// clearing the named/spare statics at 0-31) and the ocf zone's floor
// (<=63, so a widened mgmt band can never reach into ocf's 64+
// territory and reintroduce the cross-tier collision this table exists
// to prevent). These bounds mirror the "wide" netlayout strategy's own
// mgmt available band (internal/netlayout/wide.go); they are declared as
// literals here, rather than imported, because this override-bounds check
// is a vault-layer policy independent of the Layout's table construction.
const (
	pveMgmtBandOverrideFloor   = 32
	pveMgmtBandOverrideCeiling = 63
)

// pveAssignmentPriority orders assignment types for deterministic reserved-
// ips output. Named statics are processed roughly in the order an operator
// would read the offset table, with available/reserved (the two range-spec
// pseudo-roles) processed last.
var pveAssignmentPriority = map[string]int{ //nolint:gochecknoglobals // static ordering table, read-only
	"bastion":      1,
	"bosh":         2,  //nolint:mnd
	"vault":        3,  //nolint:mnd
	"jumpbox":      4,  //nolint:mnd
	"concourse":    5,  //nolint:mnd
	"prometheus":   6,  //nolint:mnd
	"shield":       7,  //nolint:mnd
	"blacksmith":   8,  //nolint:mnd
	"artifacts":    9,  //nolint:mnd
	"wireguard":    10, //nolint:mnd
	"ovpn":         11, //nolint:mnd
	"rustfs":       12, //nolint:mnd
	"rustfs_smoke": 13, //nolint:mnd
	"proxycache":   14, //nolint:mnd
	"nfs":          15, //nolint:mnd
	"ocfp_ui":      16, //nolint:mnd
	"doomsday":     17, //nolint:mnd
	"shout":        18, //nolint:mnd
	"garage":       19, //nolint:mnd
	"garage_smoke": 20, //nolint:mnd
	"haproxy":      21, //nolint:mnd
	"available":    22, //nolint:mnd
	"reserved":     23, //nolint:mnd
}

// pveDefaultReservedIPAssignments returns the PVE per-tier assignment table.
// Every named-static entry uses a plain Offset (not SubnetMapping): unlike
// STACKIT's shared address space, each PVE workload subnet (ocfp-0/1/2) is
// its own physical /22, so every workload subnet independently gets the same
// role set computed from its own base address (mirrors the current PVE
// per-subnet computation in writeStateReservedBand/writeFallbackSubnet,
// which likewise derives bosh_ip/jumpbox_ip from whichever subnet's own
// gateway is passed in, regardless of subnet index). The table itself is
// owned by netlayout's "wide" strategy (internal/netlayout/wide.go); this
// wrapper preserves the pre-netlayout call signature for this file's other
// callers.
func pveDefaultReservedIPAssignments() reservedip.AssignmentTable {
	table, _ := netlayout.Default().WorkloadTable("")

	return table
}

// PVE mgmt-band override errors. Network.AvailableBandStart/End (see
// config.NetworkConfig) is honored only for the mgmt tier: it is a single
// (non-per-tier) config knob inherited from the pre-tiered layout, and
// widening it into ocf's 64+ territory would reintroduce the cross-tier
// collision this table exists to prevent. Operators who need a wider ocf
// band should raise it in a follow-up change to the config shape rather
// than overloading this knob (see plans/pve-tiered-reserved-ip-map.md,
// "Keep Network.AvailableBandStart/End... now applied per-tier").
var (
	ErrPVEBandOverridePartial = errors.New(
		"network.availableBandStart and network.availableBandEnd must both be set, or neither")
	ErrPVEBandOverrideOutOfRange = fmt.Errorf(
		"network.availableBandStart/End must satisfy %d <= start < end <= %d (the mgmt tier's static/available zone)",
		pveMgmtBandOverrideFloor, pveMgmtBandOverrideCeiling)
)

// applyPVEMgmtBandOverride returns a copy of assignments with the mgmt
// tier's "available"/"reserved" entries replaced by
// cfg.Network.AvailableBandStart/End when both are set (non-zero). Returns
// the input unchanged (not a copy) when no override is configured. Returns
// an error if only one of the pair is set, or if the pair falls outside the
// mgmt static/available zone (pveMgmtBandOverrideFloor..Ceiling).
func applyPVEMgmtBandOverride(assignments reservedip.AssignmentTable, netCfg config.NetworkConfig) (reservedip.AssignmentTable, error) {
	start := netCfg.AvailableBandStart
	end := netCfg.AvailableBandEnd

	if start == 0 && end == 0 {
		return assignments, nil
	}

	if start == 0 || end == 0 {
		return nil, ErrPVEBandOverridePartial
	}

	if start < pveMgmtBandOverrideFloor || end > pveMgmtBandOverrideCeiling || end <= start {
		return nil, fmt.Errorf("%w: got start=%d end=%d", ErrPVEBandOverrideOutOfRange, start, end)
	}

	overridden := make(reservedip.AssignmentTable, len(assignments))
	for assignmentType, envMap := range assignments {
		clonedEnvMap := make(map[string]*reservedip.Assignment, len(envMap))
		maps.Copy(clonedEnvMap, envMap)

		overridden[assignmentType] = clonedEnvMap
	}

	overridden["available"]["mgmt"] = &reservedip.Assignment{RangeSpec: fmt.Sprintf("%d-%d", start, end)}
	overridden["reserved"]["mgmt"] = &reservedip.Assignment{
		RangeSpec: fmt.Sprintf("0-%d,%d->", start-1, end+1),
	}

	return overridden, nil
}

// pveReservedIPsForSubnet computes the full reserved-ips secret data for one
// PVE workload subnet: subnetCIDR is that subnet's OWN CIDR (each ocfp-N is
// a distinct physical /22 in the state-driven path, or the single shared
// CIDR reused across all three in the stateless-fallback path), envType is
// "mgmt" or "ocf", and subnetNum is the workload subnet's index (0/1/2),
// forwarded to the shared engine for parity even though the current PVE
// table does not vary by index. netCfg carries the optional mgmt-only
// AvailableBandStart/End override.
func pveReservedIPsForSubnet(
	subnetCIDR string, envType string, subnetNum int, netCfg config.NetworkConfig, log *zap.SugaredLogger,
) (map[string]any, error) {
	assignments, err := applyPVEMgmtBandOverride(pveDefaultReservedIPAssignments(), netCfg)
	if err != nil {
		return nil, err
	}

	vaultIPs, err := reservedip.Calculate(subnetCIDR, assignments, envType, subnetNum, pveAssignmentPriority, log)
	if err != nil {
		return nil, fmt.Errorf("failed to calculate reserved IPs for %s subnet %d: %w", envType, subnetNum, err)
	}

	return vaultIPs, nil
}
