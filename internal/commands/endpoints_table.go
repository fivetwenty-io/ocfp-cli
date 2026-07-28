package commands

import (
	"context"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/ui"
)

// noBaseDomainSummary is the R-06 explicit warning shown when a bloc has no
// fqdns.base configured: every derived FQDN in Section 1 is blank, and
// Section 3's tailscale branch never renders a row, but the table itself is
// never empty or silently misleading about why.
const noBaseDomainSummary = "no base domain configured (fqdns.base); derived FQDNs and tailscale ingress rows are blank"

// buildEndpointsTable assembles the complete four-section *ui.Table for a
// bloc: Derived Service FQDNs, Cloudflare Service Routes, Ingress Records,
// and Bastion, in that fixed order. Every section's own builder is
// responsible for its own degrade paths (nil cfg, no base domain, nil
// Cloudflare config, etc.) — this function's only job is running one shared
// resolve pass across every section's lookup keys and filling in each row's
// RESOLVED IP cell from it.
//
// The resolve pass fills a row from the *union* of every section's own
// per-row lookup key (Section 1's FQDN column, Section 2/3's hostname/record
// column), not by a positional zip against each builder's returned
// resolve-key slice: Section 1's slice skips rows with a blank FQDN, so it is
// not guaranteed to be index-parallel to its rows, and a positional zip would
// silently misattribute a resolved address to the wrong row whenever that
// happens. Looking a row's own key up directly in the resolved map is
// correct regardless of how any builder's returned slice is shaped, and Task
// 6's own contract already promises the same fill logic ("column-position-
// agnostic — always fills the last column of every row whose resolve-key was
// non-empty").
func buildEndpointsTable(ctx context.Context, cfg *config.Config, noResolve bool) *ui.Table {
	fqdnSection, fqdnKeys := collectServiceFQDNSection(cfg)

	var cf *config.CloudflareConfig
	if cfg != nil {
		cf = cfg.Cloudflare
	}

	cloudflareSection, cloudflareKeys := collectCloudflareSection(cf)
	ingressSection, ingressKeys := collectIngressSection(cfg)
	bastionSection := collectBastionSection(cfg)

	hosts := make([]string, 0, len(fqdnKeys)+len(cloudflareKeys)+len(ingressKeys))
	hosts = append(hosts, fqdnKeys...)
	hosts = append(hosts, cloudflareKeys...)
	hosts = append(hosts, ingressKeys...)

	resolved := resolveAll(ctx, hosts, noResolve)

	fillResolvedColumn(fqdnSection.Rows, fqdnColumnIndex, resolved)
	fillResolvedColumn(cloudflareSection.Rows, hostnameColumnIndex, resolved)
	fillResolvedColumn(ingressSection.Rows, recordColumnIndex, resolved)

	table := &ui.Table{
		Title: "Endpoints — bloc " + endpointsBlocName(cfg),
		Sections: []ui.Section{
			fqdnSection,
			cloudflareSection,
			ingressSection,
			bastionSection,
		},
	}

	if cfg == nil || cfg.FQDNs == nil || cfg.FQDNs.Base == "" {
		table.Summary = noBaseDomainSummary
	}

	return table
}

// Column indices of each section's own lookup key, used by
// fillResolvedColumn to look each row's resolved address up directly rather
// than via a positional zip against a separately-returned key slice.
const (
	fqdnColumnIndex     = 2 // Section 1: ENV, SERVICE, FQDN, ...
	hostnameColumnIndex = 1 // Section 2: KIND, HOSTNAME, ...
	recordColumnIndex   = 0 // Section 3: RECORD, TYPE, ...
)

// fillResolvedColumn overwrites the last cell of every row in rows with its
// resolved address, looked up in resolved by the value in keyColumn. Rows
// whose key column value has no entry in resolved (blank, wildcard, or
// simply unresolved) are left at their existing dashed placeholder.
func fillResolvedColumn(rows [][]string, keyColumn int, resolved map[string]string) {
	for _, row := range rows {
		if keyColumn >= len(row) {
			continue
		}

		addr, ok := resolved[row[keyColumn]]
		if !ok || addr == "" {
			continue
		}

		row[len(row)-1] = addr
	}
}

// endpointsBlocName returns cfg.Name, or "" for a nil cfg —
// buildEndpointsTable never panics on a nil config, matching every other
// section builder's own nil-safety contract. Named with the command's prefix
// because plain blocName is this package's established local-variable name
// (provider.go, endpoints.go:83), and a package-level function sharing it
// would be shadowed in most of the places a reader would expect to call it.
func endpointsBlocName(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}

	return cfg.Name
}
