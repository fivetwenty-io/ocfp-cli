package commands

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildEndpointsTable_FourSectionsInOrder verifies the assembled table
// carries all four sections in order (Derived Service FQDNs, Cloudflare
// Service Routes, Ingress Records, Bastion), the bloc-scoped title, and that
// Section 1/2/3 headers match the revised ORIGIN-column shapes verbatim.
func TestBuildEndpointsTable_FourSectionsInOrder(t *testing.T) {
	t.Setenv("OCFP_HOME", t.TempDir())

	fake := newFakeHostResolver()
	restore := installFakeHostResolver(fake)

	defer restore()

	cfg := &config.Config{
		Name:      "table-fixture-bloc",
		BastionIP: "10.64.64.5",
		FQDNs:     &config.FQDNConfig{Base: "ocf.example.lab.internal"},
	}

	table := buildEndpointsTable(t.Context(), cfg, true)

	require.Len(t, table.Sections, 4)
	assert.Equal(t, "Endpoints — bloc table-fixture-bloc", table.Title)

	assert.Equal(t, "Derived Service FQDNs", table.Sections[0].Title)
	assert.Equal(t, []string{"ENV", "SERVICE", "FQDN", "EXPECTED IP", "ORIGIN", "RESOLVED IP"}, table.Sections[0].Headers)

	assert.Equal(t, "Cloudflare Service Routes", table.Sections[1].Title)
	assert.Equal(t, []string{"KIND", "HOSTNAME", "SERVICE URL", "ORIGIN", "RESOLVED IP"}, table.Sections[1].Headers)

	assert.Equal(t, "Ingress Records", table.Sections[2].Title)
	assert.Equal(t, []string{"RECORD", "TYPE", "EXPECTED TARGET", "ORIGIN", "RESOLVED IP"}, table.Sections[2].Headers)

	assert.Equal(t, "Bastion", table.Sections[3].Title)
	assert.Equal(t, []string{"NAME", "VALUE"}, table.Sections[3].Headers)
}

// TestBuildEndpointsTable_NoBaseDomainSummary verifies R-06: a bloc with no
// fqdns.base gets an explicit "no base domain configured" summary rather
// than a blank one, while every section still renders its full row set.
func TestBuildEndpointsTable_NoBaseDomainSummary(t *testing.T) {
	t.Setenv("OCFP_HOME", t.TempDir())

	fake := newFakeHostResolver()
	restore := installFakeHostResolver(fake)

	defer restore()

	cfg := &config.Config{Name: "no-base-fixture-bloc"}

	table := buildEndpointsTable(t.Context(), cfg, true)

	assert.Contains(t, table.Summary, "no base domain configured")
	require.Len(t, table.Sections, 4)
	assert.NotEmpty(t, table.Sections[0].Rows, "Section 1 still renders its full row set with no base domain")
}

// TestBuildEndpointsTable_HasBaseDomainEmptySummary verifies a bloc with
// fqdns.base set gets no R-06 warning in its summary.
func TestBuildEndpointsTable_HasBaseDomainEmptySummary(t *testing.T) {
	t.Setenv("OCFP_HOME", t.TempDir())

	fake := newFakeHostResolver()
	restore := installFakeHostResolver(fake)

	defer restore()

	cfg := &config.Config{
		Name:  "has-base-fixture-bloc",
		FQDNs: &config.FQDNConfig{Base: "ocf.example.lab.internal"},
	}

	table := buildEndpointsTable(t.Context(), cfg, true)

	assert.Empty(t, table.Summary)
}

// TestBuildEndpointsTable_FillsResolvedFromLiveLookup verifies the shared
// resolve pass fills the last column of a row whose own lookup key resolved,
// using each section's own hostname/FQDN column rather than assuming the
// resolve-key slices returned by the section builders are positionally
// parallel to their rows (Section 1's builder skips blank-FQDN rows when
// building its resolve-key slice, so a naive positional zip would
// misattribute resolved addresses to the wrong rows).
func TestBuildEndpointsTable_FillsResolvedFromLiveLookup(t *testing.T) {
	t.Setenv("OCFP_HOME", t.TempDir())

	base := "ocf.example.lab.internal"
	sshHostname := "ssh." + base

	fake := newFakeHostResolver()
	fake.responses[sshHostname] = []string{"10.64.64.37"}

	restore := installFakeHostResolver(fake)

	defer restore()

	tunnelEnabled := true
	cfg := &config.Config{
		Name:  "resolve-fixture-bloc",
		FQDNs: &config.FQDNConfig{Base: base},
		Cloudflare: &config.CloudflareConfig{
			Enabled:     &tunnelEnabled,
			SSHHostname: sshHostname,
			SSHOrigin:   "ssh://10.64.64.37:2222",
		},
	}

	table := buildEndpointsTable(t.Context(), cfg, false)

	var sshRow []string

	for _, row := range table.Sections[1].Rows {
		if row[0] == "ssh" {
			sshRow = row
		}
	}

	require.NotNil(t, sshRow, "expected an ssh row in the Cloudflare section")
	assert.Equal(t, "10.64.64.37", sshRow[len(sshRow)-1])
}

// TestBuildEndpointsTable_NoResolveLeavesResolvedBlank verifies --no-resolve
// (noResolve=true) never invokes the resolver and leaves every RESOLVED IP
// cell at its dashed placeholder.
func TestBuildEndpointsTable_NoResolveLeavesResolvedBlank(t *testing.T) {
	t.Setenv("OCFP_HOME", t.TempDir())

	base := "ocf.example.lab.internal"
	sshHostname := "ssh." + base

	fake := newFakeHostResolver()
	fake.responses[sshHostname] = []string{"10.64.64.37"}

	restore := installFakeHostResolver(fake)

	defer restore()

	tunnelEnabled := true
	cfg := &config.Config{
		Name:  "no-resolve-fixture-bloc",
		FQDNs: &config.FQDNConfig{Base: base},
		Cloudflare: &config.CloudflareConfig{
			Enabled:     &tunnelEnabled,
			SSHHostname: sshHostname,
			SSHOrigin:   "ssh://10.64.64.37:2222",
		},
	}

	table := buildEndpointsTable(t.Context(), cfg, true)

	for _, row := range table.Sections[1].Rows {
		assert.Equal(t, "—", row[len(row)-1])
	}

	assert.Equal(t, 0, fake.callCount())
}

// TestBuildEndpointsTable_NilConfigDegrades verifies the nil-*config.Config
// contract: no panic anywhere, all four sections still present in order,
// Sections 1-3 header-only, the Bastion section's always-present IP row
// dashed, a bloc-less title, the no-base-domain summary, and no DNS lookup
// attempted for an empty host set.
//
// This exercises the explicit nil guards in collectServiceFQDNSection,
// collectCloudflareSection, collectBastionSection, blocName, and
// buildEndpointsTable itself — removing any one of them fails this test.
// collectIngressSection's own cfg == nil guard is deliberately not claimed:
// it is defensive but redundant, since config.ResolveIngressProvider already
// returns "" for a nil config and the switch falls through to its empty
// default, so removing that guard changes no observable behavior.
func TestBuildEndpointsTable_NilConfigDegrades(t *testing.T) {
	t.Setenv("OCFP_HOME", t.TempDir())

	fake := newFakeHostResolver()
	restore := installFakeHostResolver(fake)

	defer restore()

	table := buildEndpointsTable(t.Context(), nil, false)

	require.NotNil(t, table)
	require.Len(t, table.Sections, 4)

	assert.Equal(t, "Endpoints — bloc ", table.Title)
	assert.Equal(t, noBaseDomainSummary, table.Summary)

	assert.Equal(t, "Derived Service FQDNs", table.Sections[0].Title)
	assert.Equal(t, "Cloudflare Service Routes", table.Sections[1].Title)
	assert.Equal(t, "Ingress Records", table.Sections[2].Title)
	assert.Equal(t, "Bastion", table.Sections[3].Title)

	assert.Empty(t, table.Sections[0].Rows)
	assert.Empty(t, table.Sections[1].Rows)
	assert.Empty(t, table.Sections[2].Rows)

	require.Len(t, table.Sections[3].Rows, 1)
	assert.Equal(t, []string{"Bastion IP", "—"}, table.Sections[3].Rows[0])

	assert.Equal(t, 0, fake.callCount())
}

// TestCollectBastionSection_NilConfig verifies the Bastion builder is
// independently nil-safe: reading cfg.BastionIP and resolving the ingress
// provider both have to tolerate a nil config, since buildEndpointsTable
// calls this builder on the nil path without a guard of its own.
func TestCollectBastionSection_NilConfig(t *testing.T) {
	section := collectBastionSection(nil)

	assert.Equal(t, "Bastion", section.Title)
	require.Len(t, section.Rows, 1)
	assert.Equal(t, []string{"Bastion IP", "—"}, section.Rows[0])
}
