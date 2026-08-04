package commands

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/netlayout"
	"github.com/ocfp/ocfp-cli-go/internal/state"
	"github.com/ocfp/ocfp-cli-go/internal/ui"
	"github.com/ocfp/ocfp-cli-go/internal/vault"
)

// serviceFQDN pairs a service name with its derived (or explicit) FQDN for
// one environment type, in the display order Section 1 renders.
type serviceFQDN struct {
	Service string
	FQDN    string
}

// dashIfEmpty renders a blank cell value as an em dash, matching every other
// bloc-scoped listing command's convention for "nothing to show here".
func dashIfEmpty(value string) string {
	if value == "" {
		return "—"
	}

	return value
}

// orderedServiceFQDNs returns the ordered (service, fqdn) pairs for one
// environment type: every service in vault.GetServicesForEnvType(envType),
// in its declared order, followed by any explicit-override service not
// already in that list, in alphabetical order. Never errors; an empty base
// with no matching explicit override simply yields a blank FQDN for that
// service (vault.GetFQDN's own contract), not a partial or missing row.
func orderedServiceFQDNs(envType string, explicit map[string]string, base string, systemScoped bool) []serviceFQDN {
	known := vault.GetServicesForEnvType(envType)

	seen := make(map[string]bool, len(known))
	pairs := make([]serviceFQDN, 0, len(known)+len(explicit))

	for _, service := range known {
		seen[service] = true

		pairs = append(pairs, serviceFQDN{
			Service: service,
			FQDN:    vault.GetFQDN(service, explicit, base, systemScoped),
		})
	}

	extras := make([]string, 0, len(explicit))

	for service := range explicit {
		if seen[service] {
			continue
		}

		extras = append(extras, service)
	}

	sort.Strings(extras)

	for _, service := range extras {
		pairs = append(pairs, serviceFQDN{
			Service: service,
			FQDN:    vault.GetFQDN(service, explicit, base, systemScoped),
		})
	}

	return pairs
}

// findReservedIP finds a role's reserved IP among bootstrap's tier-blind
// reserved_<subnet>_<role>_ip state outputs, matching on prefix "reserved_"
// and suffix "_<role>_ip" only — deliberately not on the exact
// "reserved_<bloc>-ocfp-<index>_<role>_ip" shape ResolveReservedIP (lb.go)
// and the ssh_config.go regexp both hardcode, since a subnet can also be
// named "<bloc>-infra-<index>" or similar. When more than one subnet's key
// matches (a role reserved on more than one subnet), a non-nil layout that
// pins the role to a single workload-subnet index wins first — the key on
// "<bloc>-ocfp-<pinned-index>" is the address the kits actually consume,
// and a stale pre-migration key on a lower-sorted subnet must not shadow it
// — otherwise the keys are sorted and the first is used; this never errors,
// an absent role simply returns "".
func findReservedIP(outputs map[string]interface{}, role, blocName string, layout netlayout.Layout) string {
	if len(outputs) == 0 {
		return ""
	}

	suffix := "_" + role + "_ip"

	matchingKeys := make([]string, 0, len(outputs))

	for key := range outputs {
		if strings.HasPrefix(key, "reserved_") && strings.HasSuffix(key, suffix) {
			matchingKeys = append(matchingKeys, key)
		}
	}

	if len(matchingKeys) == 0 {
		return ""
	}

	sort.Strings(matchingKeys)

	if layout != nil {
		if value, ok := pinnedReservedIP(outputs, role, blocName, suffix, layout); ok {
			return value
		}
	}

	value, ok := outputs[matchingKeys[0]].(string)
	if !ok {
		return ""
	}

	return value
}

// pinnedReservedIP resolves role's reserved IP from the workload subnet its
// layout pins it to. The subnet recorded at the pinned index (bootstrap's
// subnet_<name>_index outputs) is authoritative — it holds under any subnet
// naming, operator names included. Legacy states predate the index outputs;
// there the default "<bloc>-ocfp-<idx>" shape is the only remaining pin
// candidate, as an exact key rather than a suffix match — a bloc whose own
// name ends in "-ocfp-<n>" would otherwise masquerade as a subnet index.
// ok is false when the role is unpinned or neither key holds a string.
func pinnedReservedIP(
	outputs map[string]interface{}, role, blocName, suffix string, layout netlayout.Layout,
) (string, bool) {
	idx, pinned := layout.PinnedWorkloadIndex(role + "_ip")
	if !pinned {
		return "", false
	}

	if name, ok := workloadSubnetNameForIndex(outputs, idx); ok {
		if value, isString := outputs["reserved_"+name+suffix].(string); isString {
			return value, true
		}
	}

	pinnedKey := fmt.Sprintf("reserved_%s-ocfp-%d%s", blocName, idx, suffix)

	value, isString := outputs[pinnedKey].(string)

	return value, isString
}

// workloadSubnetNameForIndex returns the subnet name recorded at workload
// index idx among bootstrap's subnet_<name>_index outputs (written at subnet
// creation; values are decimal strings). ok is false when no output claims
// idx — states predating the index outputs have none. Two subnets claiming
// one index is corrupt state; the sorted-first name keeps the resolution
// deterministic rather than map-order dependent.
func workloadSubnetNameForIndex(outputs map[string]interface{}, idx int) (string, bool) {
	want := strconv.Itoa(idx)

	var names []string

	for key, value := range outputs {
		rest, found := strings.CutPrefix(key, "subnet_")
		if !found {
			continue
		}

		name, found := strings.CutSuffix(rest, "_index")
		if !found || name == "" {
			continue
		}

		if recorded, isString := value.(string); !isString || recorded != want {
			continue
		}

		names = append(names, name)
	}

	if len(names) == 0 {
		return "", false
	}

	sort.Strings(names)

	return names[0], true
}

// loadReservedOutputs loads a bloc's local state outputs map for use by
// findReservedIP. Every failure mode (no state dir resolvable, manager init
// failure, load failure) degrades to a nil map rather than an error — a
// bloc with no local state at all simply has no EXPECTED-IP facts to offer,
// which is not a coverage gap for this command to report as an error.
func loadReservedOutputs(blocName string) map[string]interface{} {
	stateDir, err := state.GetStateDir(blocName)
	if err != nil {
		return nil
	}

	stateManager, err := state.NewManager(stateDir)
	if err != nil {
		return nil
	}

	st, err := stateManager.Load(blocName)
	if err != nil {
		return nil
	}

	if st == nil {
		return nil
	}

	return st.Outputs
}

// expectedIPForService returns a role's reserved/allocation-fact IP: a plain
// fact about what was reserved for this role, independent of how traffic
// currently reaches it (that is ORIGIN's job — originForService). "bastion"
// always uses cfg.BastionIP when set, taking precedence over any
// reserved-state entry for "bastion", matching every provider's own
// precedence (pve.go, aws.go, stackit.go, gcp.go all check config.BastionIP
// before falling back to any reserved-IP lookup). Every other role, and
// bastion when cfg.BastionIP is blank, falls back to reserved-IP state.
// Never errors, never reads Cloudflare config (a Cloudflare service route is
// a routing decision, not an allocation fact). layout carries the bloc's
// resolved reserved-IP strategy for pinned-index preference (see
// findReservedIP); nil degrades to the sorted-first lookup.
func expectedIPForService(
	service string, reservedOutputs map[string]interface{}, bastionIP, blocName string, layout netlayout.Layout,
) string {
	if service == "bastion" && bastionIP != "" {
		return bastionIP
	}

	return findReservedIP(reservedOutputs, service, blocName, layout)
}

// originForService returns where traffic for a service's FQDN terminates
// today, by exact string equality only (D-05 — no heuristic, fuzzy, or
// service-name-based matching, even though this leaves ORIGIN blank on most
// blocs, since a service can carry three independent, unreconciled names at
// once: the plain derived name, an explicit fqdns.* override, and a
// cloudflare.services[] hostname).
//
// Rung O1: cfExact[fqdn], if present, wins unconditionally, tier-agnostic —
// an explicit Cloudflare service route is an explicit fact, not an
// inference. Rung O2, OCF-tier only: when cf.Origin is set and fqdn
// suffix-matches "."+cf.AppsDomain or "."+cf.SystemDomain, the CF haproxy
// static behind the wildcard is the answer. Mgmt-tier never reaches O2 —
// haproxy is allocated only under the "ocf" key in pve_reserved_ips.go's
// assignment table, so attributing the wildcard origin to a mgmt-tier
// service would assert a fact that isn't true for that tier.
//
// A blank fqdn (no base domain configured) returns "" immediately, no error.
func originForService(envType, fqdn string, cfExact map[string]string, cf *config.CloudflareConfig) string {
	if fqdn == "" {
		return ""
	}

	if origin, ok := cfExact[fqdn]; ok {
		return origin
	}

	if envType != vault.OCFEnvType {
		return ""
	}

	if cf == nil || cf.Origin == "" {
		return ""
	}

	suffixMatchesApps := cf.AppsDomain != "" && strings.HasSuffix(fqdn, "."+cf.AppsDomain)
	suffixMatchesSystem := cf.SystemDomain != "" && strings.HasSuffix(fqdn, "."+cf.SystemDomain)

	if suffixMatchesApps || suffixMatchesSystem {
		return originHost(cf.Origin)
	}

	return ""
}

// collectServiceFQDNSection builds Section 1 (Derived Service FQDNs): one
// row per known service in both the mgmt and ocf environment types, mgmt
// first, plus any explicit-override extras, showing each service's derived
// or explicit FQDN alongside its EXPECTED and ORIGIN facts. A nil cfg
// degrades to an empty, header-only section rather than a panic — every
// other guard (no base domain, no Cloudflare config, no reserved state)
// degrades per orderedServiceFQDNs/expectedIPForService/originForService's
// own contracts, never an error here either.
//
// systemScoped is computed via config.SystemScoped(cfg) — the ingress-
// provider-backed signal (D-09), not the legacy, Cloudflare-only gate —
// so a tailscale bloc's system-scoped services derive the same .system.
// infix a cloudflared bloc's do.
func collectServiceFQDNSection(cfg *config.Config) (ui.Section, []string) {
	section := ui.Section{
		Title:   "Derived Service FQDNs",
		Headers: []string{"ENV", "SERVICE", "FQDN", "EXPECTED IP", "ORIGIN", "RESOLVED IP"},
		Rows:    [][]string{},
	}

	if cfg == nil {
		return section, nil
	}

	var base string

	var explicitMgmt, explicitOCF map[string]string

	if cfg.FQDNs != nil {
		base = cfg.FQDNs.Base
		explicitMgmt = cfg.FQDNs.Mgmt
		explicitOCF = cfg.FQDNs.OCF
	}

	systemScoped := config.SystemScoped(cfg)
	reservedOutputs := loadReservedOutputs(cfg.Name)
	cfExact := cfExactHostnameOrigins(cfg.Cloudflare)

	// The bloc's resolved strategy drives pinned-index preference in the
	// EXPECTED-IP lookup; an unresolvable strategy degrades to the
	// sorted-first fallback rather than failing the section.
	layout, err := cfg.ResolveReservedIPLayout()
	if err != nil {
		layout = nil
	}

	var resolveKeys []string

	appendEnvRows := func(envType string, explicit map[string]string) {
		for _, pair := range orderedServiceFQDNs(envType, explicit, base, systemScoped) {
			expected := expectedIPForService(pair.Service, reservedOutputs, cfg.BastionIP, cfg.Name, layout)
			origin := originForService(envType, pair.FQDN, cfExact, cfg.Cloudflare)

			section.Rows = append(section.Rows, []string{
				envType,
				pair.Service,
				dashIfEmpty(pair.FQDN),
				dashIfEmpty(expected),
				dashIfEmpty(origin),
				"—",
			})

			if pair.FQDN != "" {
				resolveKeys = append(resolveKeys, pair.FQDN)
			}
		}
	}

	appendEnvRows(vault.MgmtEnvType, explicitMgmt)
	appendEnvRows(vault.OCFEnvType, explicitOCF)

	return section, resolveKeys
}
