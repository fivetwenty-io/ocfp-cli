package netlayout

import (
	"fmt"

	"github.com/ocfp/ocfp-cli-go/internal/reservedip"
)

// compact per-tier reserved-IP offsets (relative to each workload subnet's
// own base address). It carries the same role set as wide, compressed to
// fit a /26: mgmt keeps wide's exact static offsets (3-22), ocf's four
// cross-tier statics move down from the 64-67 window to 23-26, and both
// tiers' available bands shrink to match.
//
//	 0-  2  reserved   network + gateway (.1)
//	 3- 22  mgmt statics (named below)
//	   23   spare/gap
//	 23- 26  ocf statics (named below)
//	   27   spare/gap
//	28- 35  mgmt available band (dynamic allocations)
//	36- 36  ocf haproxy static (see pveCompactOCFHaproxyOffset)
//	36-...  ocf available band (dynamic allocations)
//
// mgmt's named statics (pveBastionOffset, pveMgmtBoshOffset, ...) are
// declared in wide.go and reused here unchanged — compact's mgmt tier is
// numerically identical to wide's, so a second set of constants at the same
// values would only invite drift.
const (
	pveCompactOCFBoshOffset       = 23
	pveCompactOCFVaultOffset      = 24
	pveCompactOCFJumpboxOffset    = 25
	pveCompactOCFBlacksmithOffset = 26

	pveCompactMgmtAvailableStart = 28
	pveCompactMgmtAvailableEnd   = 35
	pveCompactOCFAvailableStart  = 36

	// pveCompactOCFHaproxyOffset sits INSIDE the ocf available band, one past
	// its start — the same coupling wide's pveOCFHaproxyOffset documents: the
	// cf kit's cloud-config hook claims a window from the available band and
	// marks the first three claimed IPs as the subnet's static range, so the
	// env manifest's haproxy_ip seed has to land inside that claim-derived
	// static window or BOSH rejects it as "belongs to no subnet".
	pveCompactOCFHaproxyOffset = pveCompactOCFAvailableStart + 1
)

// compactSchemeVersion is the guard-stamped scheme identity compact's tables
// were introduced under.
const compactSchemeVersion = "3-compact"

// compactMinPrefix is the minimum CIDR prefix length the compact strategy
// fits in: its highest fixed offset is compactHighestOffset (37, ocf
// haproxy), and a /26 (64 addresses, host offsets 0-63) is the widest
// (shortest) prefix that still clears it. ValidateSubnet rejects any
// narrower subnet (prefix > compactMinPrefix) rather than letting it
// silently emit an IP outside the subnet.
const compactMinPrefix = 26

// compactHighestOffset is the compact strategy's highest fixed offset (ocf
// haproxy, pveCompactOCFHaproxyOffset). ValidateSubnet's error names it so a
// rejected CIDR's message explains exactly which offset it fails to fit,
// without the caller needing to cross-reference compact's offset table.
const compactHighestOffset = pveCompactOCFHaproxyOffset

// compactLayout is the "compact" Layout: a /26-capable PVE per-tier
// reserved-IP scheme derived from wide by compressing its ocf statics and
// available bands. WorkloadTable, SchemeVersion, MinPrefix, ValidateSubnet,
// and Slots are real; ValidateBand remains a stub (see registry.go's
// Layout doc comment) until its owning task lands.
type compactLayout struct{}

func (compactLayout) Name() string { return "compact" }

// SchemeVersion returns the guard-stamped scheme identity ("3-compact").
func (compactLayout) SchemeVersion() string { return compactSchemeVersion }

// WorkloadTable returns the compact strategy's Layer B assignment table. The
// table does not vary by subnet size, so cidr is ignored — every workload
// subnet independently gets the same role set computed from its own base
// address, mirroring wide's WorkloadTable contract.
func (compactLayout) WorkloadTable(_ string) (reservedip.AssignmentTable, error) {
	return reservedip.AssignmentTable{
		"bastion":      {"mgmt": {Offset: pveBastionOffset}},
		"bosh":         {"mgmt": {Offset: pveMgmtBoshOffset}, "ocf": {Offset: pveCompactOCFBoshOffset}},
		"vault":        {"mgmt": {Offset: pveMgmtVaultOffset}, "ocf": {Offset: pveCompactOCFVaultOffset}},
		"jumpbox":      {"mgmt": {Offset: pveMgmtJumpbox}, "ocf": {Offset: pveCompactOCFJumpboxOffset}},
		"concourse":    {"mgmt": {Offset: pveConcourseOffset}},
		"prometheus":   {"mgmt": {Offset: pvePrometheusOffset}},
		"shield":       {"mgmt": {Offset: pveShieldOffset}},
		"blacksmith":   {"mgmt": {Offset: pveMgmtBlacksmith}, "ocf": {Offset: pveCompactOCFBlacksmithOffset}},
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
		"haproxy":      {"ocf": {Offset: pveCompactOCFHaproxyOffset}},
		"available": {
			"mgmt": {RangeSpec: fmt.Sprintf("%d-%d", pveCompactMgmtAvailableStart, pveCompactMgmtAvailableEnd)},
			"ocf":  {RangeSpec: fmt.Sprintf("%d->", pveCompactOCFAvailableStart)},
		},
		"reserved": {
			// mgmt's reserved complement wraps AROUND its own available band
			// (0-27, then 36-> to the end of the subnet) so the mgmt
			// director's cloud-config never claims an IP inside ocf's 23-26
			// statics, the 27 spare, or ocf's 36+ available territory.
			"mgmt": {
				RangeSpec: fmt.Sprintf("0-%d,%d->", pveCompactMgmtAvailableStart-1, pveCompactMgmtAvailableEnd+1),
			},
			// ocf's reserved complement covers everything below its own
			// available band, which is the entirety of mgmt's territory
			// (0-35), so the ocf director's cloud-config never claims an IP
			// inside mgmt's statics or available band.
			"ocf": {RangeSpec: fmt.Sprintf("0-%d", pveCompactOCFAvailableStart-1)},
		},
	}, nil
}

// Slots returns compact's Layer A named-slot set for role. cidr is
// accepted for interface conformance but ignored: neither role's layout
// varies by subnet size. The infra role's 12-29 band is identical to
// wide's — a /26 (compact's narrowest supported subnet, host offsets
// 0-63) clears offset 29 with room to spare, so no divergence from wide's
// historical infra layout is needed. The infra role applies to the fixed
// infra subnet, not a workload subnet whose size varies by strategy, so
// this holds regardless of how compact's own workload layout is shaped.
func (compactLayout) Slots(role, _ string) (InfraSlots, error) {
	switch role {
	case slotRoleInfra:
		return infraSlots(), nil
	case slotRoleOCFP:
		return ocfpSlots(pveCompactMgmtAvailableStart, pveCompactMgmtAvailableEnd), nil
	default:
		return InfraSlots{}, unknownRoleError(role)
	}
}

// MinPrefix returns 26: the shortest (widest) CIDR prefix that still fits
// compact's highest fixed offset (compactHighestOffset, 37).
func (compactLayout) MinPrefix() int { return compactMinPrefix }

// ValidateSubnet rejects cidr if its prefix is longer (fewer host
// addresses) than compactMinPrefix, wrapping ErrSubnetTooSmall with the
// strategy name, the offending cidr, its prefix, compactMinPrefix, and
// compactHighestOffset. A malformed cidr returns reservedip.ParseCIDR's
// error unchanged.
func (compactLayout) ValidateSubnet(cidr string) error {
	_, prefix, err := reservedip.ParseCIDR(cidr)
	if err != nil {
		return err
	}

	if prefix > compactMinPrefix {
		return subnetTooSmallError("compact", cidr, prefix, compactMinPrefix, compactHighestOffset)
	}

	return nil
}

func (compactLayout) ValidateBand(_ Tier, _ string, _, _ int) error {
	return ErrNotImplemented
}
