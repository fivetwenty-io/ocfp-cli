package commands

import (
	"sort"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/state"
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
// matches (a role reserved on more than one subnet), the keys are sorted and
// the first is used; this never errors, an absent role simply returns "".
func findReservedIP(outputs map[string]interface{}, role string) string {
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

	value, ok := outputs[matchingKeys[0]].(string)
	if !ok {
		return ""
	}

	return value
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
