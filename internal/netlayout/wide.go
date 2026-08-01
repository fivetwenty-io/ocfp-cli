package netlayout

import (
	"fmt"

	"github.com/ocfp/ocfp-cli-go/internal/reservedip"
)

// wide per-tier reserved-IP offsets (relative to each workload /22's own
// base address). mgmt and ocf own disjoint windows on every workload subnet
// so the two directors' Genesis cloud-config allocators never claim the
// same physical IP on a shared subnet (see
// plans/pve-tiered-reserved-ip-map.md).
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
	pveMgmtAvailableStart  = 32
	pveMgmtAvailableEnd    = 63
	pveOCFAvailableStart   = 96

	// pveOCFHaproxyOffset is the CF haproxy static the cf kit's
	// routing/haproxy.yml reads (params.haproxy_ips -> static_ips). Only
	// meaningful on the ocf tier, which is the only tier that deploys CF.
	//
	// It MUST sit INSIDE the ocf available band, one past its start: the cf
	// kit's cloud-config hook does not read haproxy_ip — it claims a window
	// from the available band and marks the first three claimed IPs as the
	// subnet's static range. Only the env manifest reads haproxy_ip
	// (static_ips), so the seed has to land inside that claim-derived static
	// window or BOSH rejects it as "belongs to no subnet". The historical
	// layout encoded the same coupling (band start 12, haproxy 13).
	pveOCFHaproxyOffset = pveOCFAvailableStart + 1
)

// slotRoleInfra and slotRoleOCFP are the two role values Slots accepts,
// shared by wideLayout and compactLayout — any other role is rejected with
// unknownRoleError.
const (
	slotRoleInfra = "infra"
	slotRoleOCFP  = "ocfp"
)

// infra role's historical named-slot offsets and available band, carried
// over unchanged from internal/bootstrap.defaultReservedIPLayout /
// pveSubnetStrategy.reservedIPLayout (role != "ocfp" branch, which ignores
// subnetCIDR entirely). They are declared separately from wide's own
// pveXxxOffset constants above even where the numeric values coincide
// (offsets 3-11): the infra role's layout must stay pinned to this
// historical scheme regardless of any future retune of wide's or compact's
// own mgmt-tier offsets, and reusing the same constants would silently
// couple the two.
const (
	infraBastionOffset        = 3
	infraBoshOffset           = 4
	infraVaultOffset          = 5
	infraJumpboxOffset        = 6
	infraConcourseOffset      = 7
	infraPrometheusOffset     = 8
	infraShieldOffset         = 9
	infraBlacksmithOffset     = 10
	infraBlacksmithOCFPOffset = 3
	infraDoomsdayOffset       = 9
	infraShoutOffset          = 10
	infraOCFPUIOffset         = 9
	infraArtifactsOffset      = 11
	infraAvailableStart       = 12
	infraAvailableEnd         = 29
	infraReservedBOffset      = 10
	infraReservedCOffset      = 30
)

// infraSlots returns the infra role's InfraSlots, identical for every
// strategy and every subnet size — the infra role applies to the fixed
// infra subnet carved once per bloc, not a workload subnet whose layout
// varies by strategy.
func infraSlots() InfraSlots {
	return InfraSlots{
		Bastion:        infraBastionOffset,
		Bosh:           infraBoshOffset,
		Vault:          infraVaultOffset,
		Jumpbox:        infraJumpboxOffset,
		Concourse:      infraConcourseOffset,
		Prometheus:     infraPrometheusOffset,
		Shield:         infraShieldOffset,
		Blacksmith:     infraBlacksmithOffset,
		BlacksmithOCFP: infraBlacksmithOCFPOffset,
		Doomsday:       infraDoomsdayOffset,
		Shout:          infraShoutOffset,
		OCFPUI:         infraOCFPUIOffset,
		Artifacts:      infraArtifactsOffset,
		AvailableA:     infraAvailableStart,
		AvailableB:     infraAvailableEnd,
		ReservedB:      infraReservedBOffset,
		ReservedC:      infraReservedCOffset,
	}
}

// ocfpSlots returns the ocfp role's InfraSlots for a strategy whose own
// mgmt-tier available band is [mgmtAvailableStart, mgmtAvailableEnd] (the
// same bounds that strategy's WorkloadTable emits under available/mgmt).
// Every other field keeps its infra-role value: historically
// pveSubnetStrategy.reservedIPLayout started from defaultReservedIPLayout()
// for the ocfp role too and only ever overrode the available band (and the
// reservedC offset that immediately follows it) — the named per-role
// statics (bastion, bosh, ...) were never role-specific. What changes here
// is that the band is now the strategy's OWN mgmt band unconditionally,
// replacing the old CIDR-size-derived "total/2 - 3" widening: Layer A and
// Layer B must never disagree about where that band sits again (see
// TestSlots_LayerAgreement).
func ocfpSlots(mgmtAvailableStart, mgmtAvailableEnd int) InfraSlots {
	slots := infraSlots()
	slots.AvailableA = mgmtAvailableStart
	slots.AvailableB = mgmtAvailableEnd
	slots.ReservedC = mgmtAvailableEnd + 1

	return slots
}

// wideSchemeVersion is the guard-stamped scheme identity this strategy's
// tables were introduced under (see internal/vault reservedIPSchemeVersion,
// which independently pins the same value for the vault-layer guard).
const wideSchemeVersion = "2"

// wideMinPrefix is the minimum CIDR prefix length the wide strategy fits:
// its highest fixed offset is wideHighestOffset (97, ocf haproxy), and a
// /25 (128 addresses, host offsets 0-127) is the widest (shortest) prefix
// that still clears it. ValidateSubnet rejects any narrower subnet (prefix
// > wideMinPrefix) rather than letting it silently emit an IP outside the
// subnet.
const wideMinPrefix = 25

// wideHighestOffset is the wide strategy's highest fixed offset (ocf
// haproxy, pveOCFHaproxyOffset). ValidateSubnet's error names it so a
// rejected CIDR's message explains exactly which offset it fails to fit,
// without the caller needing to cross-reference wide's offset table.
const wideHighestOffset = pveOCFHaproxyOffset

// wideLayout is the "wide" Layout: the PVE per-tier reserved-IP scheme
// carried over from the pre-netlayout internal/vault implementation. Every
// Layout method is real.
type wideLayout struct{}

func (wideLayout) Name() string { return "wide" }

// SchemeVersion returns the guard-stamped scheme identity ("2").
func (wideLayout) SchemeVersion() string { return wideSchemeVersion }

// WorkloadTable returns the wide strategy's Layer B assignment table. The
// table does not vary by subnet size, so cidr is ignored — every workload
// subnet independently gets the same role set computed from its own base
// address (mirrors the current PVE per-subnet computation in
// writeStateReservedBand/writeFallbackSubnet, which likewise derives
// bosh_ip/jumpbox_ip from whichever subnet's own gateway is passed in,
// regardless of subnet index).
func (wideLayout) WorkloadTable(_ string) (reservedip.AssignmentTable, error) {
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
	}, nil
}

// Slots returns wide's Layer A named-slot set for role. cidr is accepted
// for interface conformance but ignored: neither role's layout varies by
// subnet size (see infraSlots and ocfpSlots).
func (wideLayout) Slots(role, _ string) (InfraSlots, error) {
	switch role {
	case slotRoleInfra:
		return infraSlots(), nil
	case slotRoleOCFP:
		return ocfpSlots(pveMgmtAvailableStart, pveMgmtAvailableEnd), nil
	default:
		return InfraSlots{}, unknownRoleError(role)
	}
}

// MinPrefix returns 25: the shortest (widest) CIDR prefix that still fits
// wide's highest fixed offset (wideHighestOffset, 97).
func (wideLayout) MinPrefix() int { return wideMinPrefix }

// ValidateSubnet rejects cidr if its prefix is longer (fewer host
// addresses) than wideMinPrefix, wrapping ErrSubnetTooSmall with the
// strategy name, the offending cidr, its prefix, wideMinPrefix, and
// wideHighestOffset. A malformed cidr returns reservedip.ParseCIDR's error
// unchanged.
func (wideLayout) ValidateSubnet(cidr string) error {
	_, prefix, err := reservedip.ParseCIDR(cidr)
	if err != nil {
		return err
	}

	if prefix > wideMinPrefix {
		return subnetTooSmallError("wide", cidr, prefix, wideMinPrefix, wideHighestOffset)
	}

	return nil
}

// ValidateBand validates [start,end] as a reserved-IP available-band
// override for tier on cidr. See validateBand (band.go) for the shared
// implementation both wide and compact delegate to.
func (wideLayout) ValidateBand(tier Tier, cidr string, start, end int) error {
	return validateBand(tier, cidr, start, end)
}
