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
// Network.Bands.Mgmt override (see applyPVEMgmtBandOverride):
// the override must stay inside the mgmt static zone's ceiling (>=32,
// clearing the named/spare statics at 0-31) and the ocf zone's floor
// (<=63, so a widened mgmt band can never reach into ocf's 64+
// territory and reintroduce the cross-tier collision this table exists
// to prevent). These bounds mirror the "wide" netlayout strategy's own
// mgmt available band (internal/netlayout/wide.go); they are declared as
// literals here, rather than imported, because this override-bounds check
// is a vault-layer policy independent of the Layout's table construction.
// TestPVEMgmtBandOverrideBoundsMatchWideMgmtAvailable (pve_reserved_ips_test.go)
// couples these literals to wide's actual emitted mgmt available band, so a
// wide retune fails loudly here instead of silently admitting a
// now-collision-prone override.
//
// These bounds are wide-specific: applyPVEMgmtBandOverride hard-errors an
// explicit override for any other strategy (see
// ErrPVEBandOverrideUnsupportedStrategy) rather than checking it against
// the wrong tier layout.
const (
	pveMgmtBandOverrideFloor   = 32
	pveMgmtBandOverrideCeiling = 63

	// pveMgmtBandOverrideStrategy is the only strategy name
	// applyPVEMgmtBandOverride accepts an explicit Bands.Mgmt override for.
	pveMgmtBandOverrideStrategy = "wide"
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

// resolveLayout resolves netCfg.Strategy to a registered netlayout.Layout.
// It is a thin wrapper over netlayout.Lookup so strategy routing is
// testable in isolation from WorkloadTable's body: an empty Strategy
// resolves to netlayout.Default() ("wide"), and an unrecognized name
// returns an error wrapping netlayout.ErrUnknownStrategy for errors.Is
// callers.
func resolveLayout(netCfg config.NetworkConfig) (netlayout.Layout, error) {
	layout, err := netlayout.Lookup(netCfg.Strategy)
	if err != nil {
		return nil, fmt.Errorf("resolve reserved-ip layout strategy %q: %w", netCfg.Strategy, err)
	}

	return layout, nil
}

// PVE mgmt-band override errors. Network.Bands.Mgmt (see
// config.NetworkConfig) is honored only for the mgmt tier: it is the only
// per-tier band override this package exposes, and widening it into ocf's
// 64+ territory would reintroduce the cross-tier collision this table
// exists to prevent. Operators who need a wider ocf band should raise it in
// a follow-up change to the config shape rather than overloading this knob
// (see plans/pve-tiered-reserved-ip-map.md, "Keep the mgmt band override...
// now applied per-tier").
var (
	ErrPVEBandOverridePartial = errors.New(
		"network.bands.mgmt.start and network.bands.mgmt.end must both be set, or neither")
	ErrPVEBandOverrideOutOfRange = fmt.Errorf(
		"network.bands.mgmt.start/end must satisfy %d <= start < end <= %d (the mgmt tier's static/available zone)",
		pveMgmtBandOverrideFloor, pveMgmtBandOverrideCeiling)
	// ErrPVEBandOverrideUnsupportedStrategy is returned when an explicit
	// Bands.Mgmt override is set for a strategy other than "wide": the
	// override's floor/ceiling are wide-specific literals (32/63) and would
	// silently mis-validate a differently-shaped strategy's mgmt zone (e.g.
	// compact's 28-35), so a non-wide override is rejected outright rather
	// than checked against the wrong bounds.
	ErrPVEBandOverrideUnsupportedStrategy = errors.New("mgmt band override not supported for strategy")
)

// applyPVEMgmtBandOverride returns a copy of assignments with the mgmt
// tier's "available"/"reserved" entries replaced by
// cfg.Network.Bands.Mgmt.Start/End when both are set (non-zero). Returns
// the input unchanged (not a copy) when no override is configured. Returns
// an error if only one of the pair is set, if the pair falls outside the
// mgmt static/available zone (pveMgmtBandOverrideFloor..Ceiling), or if an
// override is set for any strategy other than strategyName ==
// pveMgmtBandOverrideStrategy ("wide") — see
// ErrPVEBandOverrideUnsupportedStrategy.
func applyPVEMgmtBandOverride(
	assignments reservedip.AssignmentTable, netCfg config.NetworkConfig, strategyName string,
) (reservedip.AssignmentTable, error) {
	start := netCfg.Bands.Mgmt.Start
	end := netCfg.Bands.Mgmt.End

	if start == 0 && end == 0 {
		return assignments, nil
	}

	if strategyName != pveMgmtBandOverrideStrategy {
		return nil, fmt.Errorf("%w %q", ErrPVEBandOverrideUnsupportedStrategy, strategyName)
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
// table does not vary by index. netCfg carries the selected reserved-ip
// layout Strategy (resolved via resolveLayout; empty means the netlayout
// default) and the optional mgmt-only Bands.Mgmt override.
//
// Every step below that can fail — strategy resolution, subnet validation,
// and table construction — returns its error immediately rather than
// falling through: a not-yet-implemented strategy's Slots/ValidateBand
// (neither reached on this path) or a future registered strategy whose
// ValidateSubnet or WorkloadTable is still stubbed must fail loudly here,
// never reach applyPVEMgmtBandOverride with a nil/partial table (which
// indexes "available"/"reserved" unconditionally and would panic on a nil
// map), and never report success having written nothing. Each wrap names
// the strategy and the failing step so an operator can tell a too-small
// subnet from an unimplemented strategy without reading source.
func pveReservedIPsForSubnet(
	subnetCIDR string, envType string, subnetNum int, netCfg config.NetworkConfig, log *zap.SugaredLogger,
) (map[string]any, error) {
	layout, err := resolveLayout(netCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to calculate reserved IPs for %s subnet %d: %w", envType, subnetNum, err)
	}

	return pveReservedIPsForSubnetWithLayout(layout, subnetCIDR, envType, subnetNum, netCfg, log)
}

// pveReservedIPsForSubnetWithLayout is pveReservedIPsForSubnet after
// strategy resolution, split out so the per-step failure handling can be
// exercised with any Layout implementation, not only the registered ones.
func pveReservedIPsForSubnetWithLayout(
	layout netlayout.Layout, subnetCIDR string, envType string, subnetNum int,
	netCfg config.NetworkConfig, log *zap.SugaredLogger,
) (map[string]any, error) {
	if err := layout.ValidateSubnet(subnetCIDR); err != nil {
		return nil, fmt.Errorf("failed to calculate reserved IPs for %s subnet %d: strategy %q: validate subnet: %w",
			envType, subnetNum, layout.Name(), err)
	}

	table, err := layout.WorkloadTable(subnetCIDR)
	if err != nil {
		return nil, fmt.Errorf("failed to calculate reserved IPs for %s subnet %d: strategy %q: workload table: %w",
			envType, subnetNum, layout.Name(), err)
	}

	assignments, err := applyPVEMgmtBandOverride(table, netCfg, layout.Name())
	if err != nil {
		return nil, err
	}

	vaultIPs, err := reservedip.Calculate(subnetCIDR, assignments, envType, subnetNum, pveAssignmentPriority, log)
	if err != nil {
		return nil, fmt.Errorf("failed to calculate reserved IPs for %s subnet %d: %w", envType, subnetNum, err)
	}

	return vaultIPs, nil
}
