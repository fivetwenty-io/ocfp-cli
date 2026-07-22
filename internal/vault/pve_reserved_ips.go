package vault

import (
	"errors"
	"fmt"
	"maps"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/reservedip"
	"go.uber.org/zap"
)

// PVE per-tier reserved-IP offsets (relative to each workload /22's own base
// address). mgmt and ocf own disjoint windows on every workload subnet so
// the two directors' Genesis cloud-config allocators never claim the same
// physical IP on a shared subnet (see plans/pve-tiered-reserved-ip-map.md).
//
//	 0-  2  reserved   network + gateway (.1)
//	 3- 31  mgmt statics (named below; 23-31 spare for growth)
//	32- 63  mgmt available band (dynamic allocations: concourse db/workers, mgmt compilation)
//	64- 95  ocf statics (named below; rest spare)
//	96-...  ocf available band (dynamic allocations: cf, scheduler, autoscaler, ocf compilation)
const (
	pveBastionOffset    = 3
	pveMgmtBoshOffset   = 4
	pveMgmtVaultOffset  = 5
	pveMgmtJumpbox      = 6
	pveConcourseOffset  = 7
	pvePrometheusOffset = 8
	pveShieldOffset     = 9
	pveMgmtBlacksmith   = 10
	pveArtifactsOffset  = 11
	pveWireguardOffset  = 12
	pveOVPNOffset       = 13
	// pveRustFSOffset is the RustFS blobstore's slot (the RustFS/Garage
	// kits are alternative blobstore implementations — only one deploys
	// per bloc — but the reservedip engine dedups by computed IP value, so
	// two roles cannot share one offset: the second processed would be
	// silently dropped by Calculate's usedIPs check. RustFS and Garage
	// therefore get distinct offsets (14 and pveGarageOffset=20) even
	// though at most one is ever live at once.
	pveRustFSOffset = 14
	// pveRustFSSmokeOffset is the RustFS smoke-tests errand's dedicated
	// static (kits/rustfs/hooks/blueprint.pm reads "rustfs_ip_smoke",
	// distinct from the role's own "rustfs_ip" at pveRustFSOffset).
	pveRustFSSmokeOffset = 21
	pveProxycacheOffset  = 15
	pveNFSOffset         = 16
	pveOCFPUIOffset      = 17
	// pveDoomsdayOffset/pveShoutOffset: kits/doomsday and kits/shout's
	// ocfp.yml both do an unconditional (no `||` default) `(( vault ...
	// /net/subnets/ocfp-1/reserved-ips:doomsday_ip ))`-style read, so a
	// missing key FATALs the manifest merge rather than degrading
	// gracefully. doomsday is deployed on every bloc today; shout is not
	// yet live but reads the same way, so both get a slot up front to
	// avoid a second breaking gap when shout ships (mgmt tier only,
	// mirroring the STACKIT reference table).
	pveDoomsdayOffset = 18
	pveShoutOffset    = 19
	// pveGarageOffset/pveGarageSmokeOffset: see pveRustFSOffset.
	pveGarageOffset      = 20
	pveGarageSmokeOffset = 22

	pveOCFBoshOffset       = 64
	pveOCFVaultOffset      = 65
	pveOCFJumpboxOffset    = 66
	pveOCFBlacksmithOffset = 67
	// pveOCFHaproxyOffset is the CF haproxy static the cf kit's
	// routing/haproxy.yml reads (params.haproxy_ips -> static_ips). Only
	// meaningful on the ocf tier, which is the only tier that deploys CF.
	pveOCFHaproxyOffset = 68

	pveMgmtAvailableStart = 32
	pveMgmtAvailableEnd   = 63
	pveOCFAvailableStart  = 96

	// pveMgmtBandOverrideFloor/Ceiling bound an operator-supplied
	// Network.AvailableBandStart/End override (see applyPVEMgmtBandOverride):
	// the override must stay inside the mgmt static zone's ceiling (>=32,
	// clearing the named/spare statics at 0-31) and the ocf zone's floor
	// (<=63, so a widened mgmt band can never reach into ocf's 64+
	// territory and reintroduce the cross-tier collision this table exists
	// to prevent).
	pveMgmtBandOverrideFloor   = pveMgmtAvailableStart
	pveMgmtBandOverrideCeiling = pveMgmtAvailableEnd
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
// gateway is passed in, regardless of subnet index).
func pveDefaultReservedIPAssignments() reservedip.AssignmentTable {
	return reservedip.AssignmentTable{
		"bastion":      {"mgmt": {Offset: pveBastionOffset}},
		"bosh":         {"mgmt": {Offset: pveMgmtBoshOffset}, "ocf": {Offset: pveOCFBoshOffset}},
		"vault":        {"mgmt": {Offset: pveMgmtVaultOffset}, "ocf": {Offset: pveOCFVaultOffset}},
		"jumpbox":      {"mgmt": {Offset: pveMgmtJumpbox}, "ocf": {Offset: pveOCFJumpboxOffset}},
		"concourse":    {"mgmt": {Offset: pveConcourseOffset}},
		"prometheus":   {"mgmt": {Offset: pvePrometheusOffset}},
		"shield":       {"mgmt": {Offset: pveShieldOffset}},
		"blacksmith":   {"mgmt": {Offset: pveMgmtBlacksmith}, "ocf": {Offset: pveOCFBlacksmithOffset}},
		"artifacts":    {"mgmt": {Offset: pveArtifactsOffset}},
		"wireguard":    {"mgmt": {Offset: pveWireguardOffset}},
		"ovpn":         {"mgmt": {Offset: pveOVPNOffset}},
		"rustfs":       {"mgmt": {Offset: pveRustFSOffset}},
		"rustfs_smoke": {"mgmt": {Offset: pveRustFSSmokeOffset, IPKey: "rustfs_ip_smoke"}},
		"proxycache":   {"mgmt": {Offset: pveProxycacheOffset}},
		"nfs":          {"mgmt": {Offset: pveNFSOffset}},
		"ocfp_ui":      {"mgmt": {Offset: pveOCFPUIOffset}},
		"doomsday":     {"mgmt": {Offset: pveDoomsdayOffset}},
		"shout":        {"mgmt": {Offset: pveShoutOffset}},
		"garage":       {"mgmt": {Offset: pveGarageOffset}},
		"garage_smoke": {"mgmt": {Offset: pveGarageSmokeOffset, IPKey: "garage_ip_smoke"}},
		"haproxy":      {"ocf": {Offset: pveOCFHaproxyOffset}},
		"available": {
			"mgmt": {RangeSpec: fmt.Sprintf("%d-%d", pveMgmtAvailableStart, pveMgmtAvailableEnd)},
			"ocf":  {RangeSpec: fmt.Sprintf("%d->", pveOCFAvailableStart)},
		},
		"reserved": {
			// mgmt's reserved complement wraps AROUND its own available
			// band (0-31, then 64-> to the end of the subnet) so the mgmt
			// director's cloud-config never claims an IP inside ocf's
			// 64-95 statics or 96+ available territory.
			"mgmt": {RangeSpec: fmt.Sprintf("0-%d,%d->", pveMgmtAvailableStart-1, pveMgmtAvailableEnd+1)},
			// ocf's reserved complement covers everything below its own
			// available band, which is the entirety of mgmt's territory
			// (0-95), so the ocf director's cloud-config never claims an
			// IP inside mgmt's statics or available band.
			"ocf": {RangeSpec: fmt.Sprintf("0-%d", pveOCFAvailableStart-1)},
		},
	}
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
