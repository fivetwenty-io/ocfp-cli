package commands

import (
	"sort"

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
