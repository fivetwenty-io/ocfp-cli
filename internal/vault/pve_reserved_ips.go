package vault

import (
	"fmt"
	"maps"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/netlayout"
	"github.com/ocfp/ocfp-cli-go/internal/reservedip"
	"go.uber.org/zap"
)

// assignmentPriority orders assignment types for deterministic reserved-
// ips output. Named statics are processed roughly in the order an operator
// would read the offset table, with available/reserved (the two range-spec
// pseudo-roles) processed last.
var assignmentPriority = map[string]int{ //nolint:gochecknoglobals // static ordering table, read-only
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

// resolveLayout resolves cfg's reserved-ip layout strategy to a
// netlayout.Layout via cfg.ResolveReservedIPLayout: the explicit
// cfg.Network.Strategy when set, else the provider/subnet-strategy default,
// looked up in cfg's catalog (built-ins plus any network.strategyPaths
// definitions). An unrecognized name returns an error wrapping
// netlayout.ErrUnknownStrategy for errors.Is callers.
func resolveLayout(cfg *config.Config) (netlayout.Layout, error) {
	layout, err := cfg.ResolveReservedIPLayout()
	if err != nil {
		return nil, fmt.Errorf("resolve reserved-ip layout strategy %q: %w", cfg.Network.Strategy, err)
	}

	return layout, nil
}

// applyMgmtBandOverride returns a copy of assignments with the mgmt tier's
// "available"/"reserved" entries replaced by netCfg.Bands.Mgmt.Start/End
// when both are set (non-zero). Returns the input unchanged (not a copy)
// when no override is configured. Validation is delegated entirely to
// layout.ValidateBand(netlayout.TierMgmt, cidr, start, end) — a half-set
// pair (netlayout.ErrBandOverridePartial), a static collision from either
// tier (netlayout.ErrBandOverrideCollidesStatic), or an intersection with
// the ocf tier's own available band (netlayout.ErrBandOverrideCrossTier) —
// so this function needs no strategy-specific bounds of its own and works
// identically for every registered (or BYO) layout.
func applyMgmtBandOverride(
	assignments reservedip.AssignmentTable, netCfg config.NetworkConfig, layout netlayout.Layout, cidr string,
) (reservedip.AssignmentTable, error) {
	start := netCfg.Bands.Mgmt.Start
	end := netCfg.Bands.Mgmt.End

	if start == 0 && end == 0 {
		return assignments, nil
	}

	if err := layout.ValidateBand(netlayout.TierMgmt, cidr, start, end); err != nil {
		return nil, err
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

// validateWorkloadSubnetCIDRs enforces that workloadCIDRs — the ocfp/
// workload subnet CIDRs a provider is about to write reserved-ips or subnet
// records for — satisfy cfg's resolved netlayout.Layout minimum subnet
// count (Layout.MinSubnets, checked via ValidateSubnetSet), before ANY
// subnet path is written to vault. label identifies the caller for the
// wrapped error (provider and phase). This is Layer B's half of the shared
// enforcement Task 12 wires in; internal/bootstrap's
// Manager.validateWorkloadSubnetCount enforces the identical check for
// Layer A before a subnet is ever recorded to state.
//
// An empty workloadCIDRs is not validated: it means this call touches no
// workload subnet at all (e.g. an infra-only bootstrap-state fixture, or a
// provider's own no-subnets-configured fallback, which always writes a
// fixed count and is out of scope here) rather than a workload set that is
// present but short of the strategy's minimum — the scenario this
// enforcement targets (e.g. stackit-single's one subnet against spanning's
// MinSubnets of 3).
func validateWorkloadSubnetCIDRs(cfg *config.Config, label string, workloadCIDRs []string) error {
	if len(workloadCIDRs) == 0 {
		return nil
	}

	layout, err := resolveLayout(cfg)
	if err != nil {
		return err
	}

	if err := layout.ValidateSubnetSet(workloadCIDRs); err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}

	return nil
}

// collectWorkloadSubnetCIDRs extracts the ocfp/workload subnet CIDRs from a
// STACKIT- or AWS-shaped config.Subnet list (sourced from bootstrap state
// via config.Subnets/Network.Subnets) for validateWorkloadSubnetCIDRs,
// skipping any non-workload (subnetType != DefaultSubnetType) or CIDR-less
// entry. PVE has no config.Subnet list to read; see pveWorkloadSubnetCIDRs
// in pve_provider.go for its state.Resource-based equivalent.
func collectWorkloadSubnetCIDRs(subnets []config.Subnet) []string {
	cidrs := make([]string, 0, len(subnets))

	for _, sub := range subnets {
		subnetType := sub.Type
		if subnetType == "" {
			subnetType = DefaultSubnetType
		}

		if subnetType != DefaultSubnetType || sub.CIDR == "" {
			continue
		}

		cidrs = append(cidrs, sub.CIDR)
	}

	return cidrs
}

// reservedIPsForSubnet computes the full reserved-ips secret data for one
// PVE or STACKIT workload subnet: subnetCIDR is that subnet's OWN CIDR
// (each ocfp-N is a distinct physical /22 in the PVE state-driven path, or
// the single shared CIDR reused across all three in the PVE
// stateless-fallback path or a STACKIT triple/single subnet), envType is
// "mgmt" or "ocf", and subnetNum is the workload subnet's index (0/1/2),
// forwarded to the shared engine so a strategy with per-index pinning (e.g.
// spanning) can vary the table by index even where the colocated built-ins
// (wide/compact) do not. cfg carries the selected reserved-ip layout
// Strategy (resolved via resolveLayout/cfg.ResolveReservedIPLayout; empty
// means the provider default — see netlayout.DefaultNameFor) and the
// optional mgmt-only Network.Bands.Mgmt override.
//
// Every step below that can fail — strategy resolution, subnet validation,
// and table construction — returns its error immediately rather than
// falling through: a not-yet-implemented strategy's Slots/ValidateBand
// (neither reached on this path) or a future registered strategy whose
// ValidateSubnet or WorkloadTable is still stubbed must fail loudly here,
// never reach applyMgmtBandOverride with a nil/partial table (which
// indexes "available"/"reserved" unconditionally and would panic on a nil
// map), and never report success having written nothing. Each wrap names
// the strategy and the failing step so an operator can tell a too-small
// subnet from an unimplemented strategy without reading source.
func reservedIPsForSubnet(
	subnetCIDR string, envType string, subnetNum int, cfg *config.Config, log *zap.SugaredLogger,
) (map[string]any, error) {
	layout, err := resolveLayout(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to calculate reserved IPs for %s subnet %d: %w", envType, subnetNum, err)
	}

	return reservedIPsForSubnetWithLayout(layout, subnetCIDR, envType, subnetNum, cfg.Network, log)
}

// reservedIPsForSubnetWithLayout is reservedIPsForSubnet after
// strategy resolution, split out so the per-step failure handling can be
// exercised with any Layout implementation, not only the registered ones.
func reservedIPsForSubnetWithLayout(
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

	assignments, err := applyMgmtBandOverride(table, netCfg, layout, subnetCIDR)
	if err != nil {
		return nil, err
	}

	vaultIPs, err := reservedip.Calculate(subnetCIDR, assignments, envType, subnetNum, assignmentPriority, log)
	if err != nil {
		return nil, fmt.Errorf("failed to calculate reserved IPs for %s subnet %d: %w", envType, subnetNum, err)
	}

	return vaultIPs, nil
}
