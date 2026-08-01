package vault

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/netlayout"
	"github.com/stretchr/testify/require"
)

// The PVE offset table is a schema for running systems: every provisioned
// bloc's addresses were derived from whatever generation of it was compiled
// in at the time. Editing an offset is therefore a migration, not a constant
// change — and the 2026-07-28 incident happened because such an edit shipped
// as an ordinary one-line diff nobody read as address-moving.
//
// This test pins both halves of the table, for EVERY registered netlayout
// strategy: the assignments themselves, and the addresses they produce on a
// reference subnet. Adding a new role appends lines; moving an existing one
// rewrites them, which is the signal reviewers need. Regenerate deliberately
// with:
//
//	go test ./internal/vault/ -run TestPVEOffsetTableGolden -update
//
// and bump the strategy's own SchemeVersion (see internal/netlayout)
// whenever the regenerated diff moves an address rather than only adding
// one. To regenerate a single strategy's fixture without touching the
// others, scope -run to its subtest, e.g.
// -run 'TestPVEOffsetTableGolden/compact'.
const (
	// offsetTableGoldenFile is the wide strategy's fixture path. It predates
	// per-strategy fixtures and keeps its original name (no "_wide" suffix)
	// so the file stays byte-identical across the loop's introduction.
	offsetTableGoldenFile = "testdata/pve_offset_table.golden"

	// goldenReferenceCIDR is an arbitrary workload /22 chosen so every
	// rendered address is unambiguous about its offset, and wide enough to
	// satisfy every registered strategy's MinPrefix.
	goldenReferenceCIDR = "10.0.0.0/22"
)

// offsetTableGoldenFileFor returns the fixture path for strategyName: wide
// keeps offsetTableGoldenFile's pre-existing name, every other strategy gets
// "testdata/pve_offset_table_<name>.golden".
func offsetTableGoldenFileFor(strategyName string) string {
	if strategyName == "wide" {
		return offsetTableGoldenFile
	}

	return fmt.Sprintf("testdata/pve_offset_table_%s.golden", strategyName)
}

func TestPVEOffsetTableGolden(t *testing.T) {
	for _, strategyName := range netlayout.Names() {
		t.Run(strategyName, func(t *testing.T) {
			got := renderPVEOffsetTable(t, strategyName)
			file := offsetTableGoldenFileFor(strategyName)

			if *updateGolden {
				require.NoError(t, os.WriteFile(file, got, 0o600))
				t.Logf("updated fixture %s", file)

				return
			}

			want, err := os.ReadFile(filepath.Clean(file))
			require.NoErrorf(t, err, "read fixture %s — regenerate with -update", file)

			if !bytes.Equal(want, got) {
				t.Errorf("the PVE reserved-IP offset table for strategy %q changed.\n"+
					"Every provisioned bloc derived its addresses from the previous table, so confirm this diff\n"+
					"only ADDS roles before regenerating with -update. If it MOVES one, bump the strategy's\n"+
					"SchemeVersion and plan the migration.\n--- want ---\n%s\n--- got ---\n%s",
					strategyName, want, got)
			}
		})
	}
}

// renderPVEOffsetTable renders strategyName's assignment table and the
// addresses it derives on the reference subnet, in a stable order.
func renderPVEOffsetTable(t *testing.T, strategyName string) []byte {
	t.Helper()

	layout, err := netlayout.Lookup(strategyName)
	require.NoError(t, err)

	var out strings.Builder

	out.WriteString("# PVE reserved-IP offset table\n")
	out.WriteString("# scheme_version: " + layout.SchemeVersion() + "\n")
	out.WriteString("# reference subnet: " + goldenReferenceCIDR + "\n\n")

	out.WriteString("[assignments]\n")

	assignments, err := layout.WorkloadTable(goldenReferenceCIDR)
	require.NoError(t, err)

	roles := make([]string, 0, len(assignments))
	for role := range assignments {
		roles = append(roles, role)
	}

	sort.Strings(roles)

	for _, role := range roles {
		tiers := make([]string, 0, len(assignments[role]))
		for tier := range assignments[role] {
			tiers = append(tiers, tier)
		}

		sort.Strings(tiers)

		for _, tier := range tiers {
			assignment := assignments[role][tier]

			spec := fmt.Sprintf("offset=%d", assignment.Offset)
			if assignment.RangeSpec != "" {
				spec = "range=" + assignment.RangeSpec
			}

			line := fmt.Sprintf("%-14s %-5s %s", role, tier, spec)
			if assignment.IPKey != "" {
				line += " key=" + assignment.IPKey
			}

			out.WriteString(line + "\n")
		}
	}

	for _, envType := range []string{MgmtEnvType, OCFEnvType} {
		derived, err := pveReservedIPsForSubnet(
			goldenReferenceCIDR, envType, 0, config.NetworkConfig{Strategy: strategyName}, logger.Get())
		require.NoError(t, err)

		out.WriteString("\n[" + envType + " " + goldenReferenceCIDR + "]\n")

		keys := make([]string, 0, len(derived))
		for key := range derived {
			keys = append(keys, key)
		}

		sort.Strings(keys)

		for _, key := range keys {
			out.WriteString(fmt.Sprintf("%-22s %v\n", key, derived[key]))
		}
	}

	return []byte(out.String())
}
