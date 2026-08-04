package reservedip_test

import (
	"fmt"
	"net"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/reservedip"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Minimum supported CIDR per provider. Neither provider names a single
// "minimum subnet size" constant in production code, so each floor below
// pins to the narrowest CIDR that provider's own test suite documents as a
// supported, currently-passing case — the closest thing to a written sizing
// contract that exists in the repo today.
//
// AWS has no entry here: its flat offset table (calculateSystemIPs) was
// deleted in favor of the shared netlayout strategy engine (see
// internal/vault/aws_reserved_ips_test.go), so there is no
// provider-specific AWS offset table left to audit.
const (
	// stackitMinSupportedNetworkBits: internal/vault/stackit_reserved_ips_test.go,
	// internal/vault/stackit_contract_test.go, and
	// internal/vault/stackit_mock_test.go all exercise
	// getDefaultReservedIPAssignments() against /24 workload subnets (e.g.
	// "10.10.1.0/24"); no narrower CIDR is exercised against this table
	// anywhere in the repo. STACKIT's documented DEFAULT parent network
	// ("10.4.0.0/20" split into /22 children, internal/config/config.go:1479
	// and :1488) is wider than /24 — /24 is used here as the more
	// conservative floor since it is the narrowest size the table is
	// actually proven against by an existing test.
	stackitMinSupportedNetworkBits = 24
)

// TestAudit_ProviderTables_NoExistingOutOfBoundsEntries is an audit probe:
// it proves whether the STACKIT default reserved-IP table, AS IT EXISTS
// TODAY, already contains any offset or range endpoint past
// reservedip.CalculateLastHostOffset for that provider's minimum supported
// CIDR — a precondition for the enforced ErrOffsetBeyondSubnet check in the
// shared reservedip.Calculate engine. If this test ever fails, the
// violating entries reported here must be fixed before that check can stay
// enforced.
//
// Test-only: this file adds no bounds-checking to internal/reservedip or
// internal/vault, and changes no production code.
func TestAudit_ProviderTables_NoExistingOutOfBoundsEntries(t *testing.T) {
	t.Run("STACKIT", func(t *testing.T) {
		auditAssignmentTable(t, "STACKIT", stackitMinSupportedNetworkBits, stackitDefaultReservedIPAssignmentsSnapshot())
	})
}

// auditAssignmentTable walks a reservedip.AssignmentTable (STACKIT's and
// PVE's shape) and, for every populated Assignment, asserts each resolved
// [start, end] pair satisfies start <= end and end <= the last usable host
// offset for networkBits.
func auditAssignmentTable(t *testing.T, provider string, networkBits int, table reservedip.AssignmentTable) {
	t.Helper()

	lastHost := reservedip.CalculateLastHostOffset(networkBits)

	for assignmentType, envMap := range table {
		for envType, assignment := range envMap {
			name := fmt.Sprintf("%s/%s", assignmentType, envType)

			t.Run(name, func(t *testing.T) {
				starts, ends := assignmentBounds(t, assignment, networkBits)
				require.NotEmptyf(t, ends, "%s: %s has none of Offset/SubnetMapping/RangeSpec/SubnetRanges populated", provider, name)

				for i := range ends {
					assert.GreaterOrEqualf(t, starts[i], 0, "%s: %s start %d must not be negative", provider, name, starts[i])
					assert.LessOrEqualf(t, starts[i], ends[i], "%s: %s range start %d must be <= end %d", provider, name, starts[i], ends[i])
					assert.LessOrEqualf(t, ends[i], lastHost,
						"%s: %s end %d exceeds last usable host offset %d for /%d", provider, name, ends[i], lastHost, networkBits)
				}
			})
		}
	}
}

// assignmentBounds resolves one Assignment (exactly one of its four forms is
// expected to be populated, per the Assignment doc comment) to parallel
// starts/ends offset slices, one pair per discrete offset or range the
// assignment can produce.
func assignmentBounds(t *testing.T, assignment *reservedip.Assignment, networkBits int) (starts, ends []int) {
	t.Helper()

	switch {
	case assignment.Offset > 0:
		starts = append(starts, assignment.Offset)
		ends = append(ends, assignment.Offset)
	case len(assignment.SubnetMapping) > 0:
		for offset := range assignment.SubnetMapping {
			starts = append(starts, offset)
			ends = append(ends, offset)
		}
	case assignment.RangeSpec != "":
		s, e := parseOffsetRange(t, assignment.RangeSpec, networkBits)
		starts = append(starts, s...)
		ends = append(ends, e...)
	case len(assignment.SubnetRanges) > 0:
		for spec := range assignment.SubnetRanges {
			s, e := parseOffsetRange(t, spec, networkBits)
			starts = append(starts, s...)
			ends = append(ends, e...)
		}
	}

	return starts, ends
}

// parseOffsetRange resolves a range spec (e.g. "11-29", "0-10,30->") to its
// numeric [start, end] offset pairs by running it through the real
// reservedip.ParseIPRangeSpec against a 0.0.0.0 base, then converting the
// resulting dotted-decimal IPs back to raw offsets. Reusing the production
// parser (rather than re-implementing the "N-M" / "N->" / "N" grammar here)
// means this audit cannot drift from how Calculate actually resolves the
// same spec.
func parseOffsetRange(t *testing.T, spec string, networkBits int) (starts, ends []int) {
	t.Helper()

	ranges, err := reservedip.ParseIPRangeSpec(spec, "0.0.0.0", networkBits)
	require.NoErrorf(t, err, "range spec %q failed to parse", spec)

	for _, r := range ranges {
		starts = append(starts, ipToOffset(t, r.Start))
		ends = append(ends, ipToOffset(t, r.End))
	}

	return starts, ends
}

// ipToOffset converts a dotted-decimal IPv4 string produced by
// reservedip.ParseIPRangeSpec (relative to the "0.0.0.0" base used by
// parseOffsetRange above) back to its raw offset.
func ipToOffset(t *testing.T, ip string) int {
	t.Helper()

	parsed := net.ParseIP(ip)
	require.NotNilf(t, parsed, "unparseable IP %q", ip)

	v4 := parsed.To4()
	require.NotNilf(t, v4, "non-IPv4 IP %q", ip)

	return int(v4[0])<<24 | int(v4[1])<<16 | int(v4[2])<<8 | int(v4[3])
}

// stackitDefaultReservedIPAssignmentsSnapshot is a test-only, hand-verified
// transcription of the private table returned by
// internal/vault/stackit_provider.go:1684-1787's getDefaultReservedIPAssignments,
// as of this session. It cannot be imported directly: that function is
// unexported, and package reservedip sits BELOW package vault in the import
// graph (vault imports reservedip), so reservedip's test package can import
// vault for its exported symbols (see the AWS offsets above) but not reach
// vault's unexported getDefaultReservedIPAssignments. If STACKIT's real
// table changes, this snapshot must be updated to match, or this audit
// silently stops covering production reality.
func stackitDefaultReservedIPAssignmentsSnapshot() reservedip.AssignmentTable {
	return reservedip.AssignmentTable{
		"bosh": {
			"mgmt":  {SubnetMapping: map[int][]int{4: {0}}},
			"ocf":   {SubnetMapping: map[int][]int{31: {0}}},
			"other": {Offset: 10},
		},
		"vault": {
			"mgmt": {Offset: 5},
			"ocf":  {Offset: 32},
		},
		"jumpbox": {
			"mgmt": {Offset: 6},
			"ocf":  {Offset: 33},
		},
		"concourse": {
			"mgmt": {Offset: 7},
			"ocf":  {Offset: 34},
		},
		"prometheus": {
			"mgmt": {Offset: 8},
			"ocf":  {Offset: 35},
		},
		"shield": {
			"mgmt": {SubnetMapping: map[int][]int{9: {0}}},
			"ocf":  {SubnetMapping: map[int][]int{36: {0}}},
		},
		"doomsday": {
			"mgmt": {SubnetMapping: map[int][]int{9: {1}}},
		},
		"ocfp_ui": {
			"mgmt": {SubnetMapping: map[int][]int{9: {2}}},
			"ocf":  {SubnetMapping: map[int][]int{36: {2}}},
		},
		"bastion": {
			"mgmt":  {SubnetMapping: map[int][]int{3: {0}}},
			"ocf":   {SubnetMapping: map[int][]int{37: {0}}},
			"other": {Offset: 3},
		},
		"blacksmith": {
			"mgmt":  {SubnetMapping: map[int][]int{10: {0}}},
			"ocf":   {SubnetMapping: map[int][]int{36: {1}}},
			"other": {Offset: 10},
		},
		"shout": {
			"mgmt": {SubnetMapping: map[int][]int{10: {1}}},
		},
		"available": {
			"mgmt": {RangeSpec: "11-29"},
			"ocf": {
				SubnetRanges: map[string][]int{
					"38->": {0},
					"37->": {1, 2},
				},
			},
		},
		"reserved": {
			"mgmt": {RangeSpec: "0-10,30->"},
			"ocf": {
				SubnetRanges: map[string][]int{
					"0-36": {1, 2},
					"0-37": {0},
				},
			},
			"other": {RangeSpec: "0-15"},
		},
	}
}
