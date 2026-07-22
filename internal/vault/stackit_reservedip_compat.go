package vault

import "github.com/ocfp/ocfp-cli-go/internal/reservedip"

// This file holds thin package-vault-level delegating wrappers around
// internal/reservedip that exist ONLY so stackit_reserved_ips_test.go's
// pre-extraction test suite keeps calling parseCIDR/parseIPRangeSpec/etc.
// unqualified within package vault, unchanged, after the shared assignment
// engine was extracted into internal/reservedip (see
// plans/pve-tiered-reserved-ip-map.md, "Extract the stackit assignment model
// into a shared package"). None of these wrappers has a production caller —
// production code calls the reservedip package directly (see
// calculateReservedIPs and addOffsetToIP in stackit_provider.go, which ARE
// production-reachable and stay there). With the repo's golangci-lint
// tests:false setting the `unused` linter cannot see the test-only callers
// here and would otherwise falsely report this file dead, mirroring the
// existing internal/vault/genesis(_envs)?\.go exclusion in .golangci.yml —
// this file has the same `unused` exclusion.

// ipRange is an alias for reservedip.IPRange.
type ipRange = reservedip.IPRange

// parseCIDR parses a CIDR notation and returns base IP and network bits.
// Delegates to reservedip.ParseCIDR (see internal/reservedip).
func parseCIDR(cidr string) (string, int, error) {
	return reservedip.ParseCIDR(cidr)
}

// parseIPRangeSpec parses a range specification like "11-29" or "0-10,30->".
// Delegates to reservedip.ParseIPRangeSpec (see internal/reservedip).
func parseIPRangeSpec(rangeSpec string, baseIP string, networkBits int) ([]ipRange, error) {
	return reservedip.ParseIPRangeSpec(rangeSpec, baseIP, networkBits)
}

// calculateLastHostOffset calculates the last usable host offset for a
// network. Delegates to reservedip.CalculateLastHostOffset.
func calculateLastHostOffset(networkBits int) int {
	return reservedip.CalculateLastHostOffset(networkBits)
}

// containsInt checks if a slice contains an integer. Delegates to
// reservedip.ContainsInt.
func containsInt(slice []int, value int) bool {
	return reservedip.ContainsInt(slice, value)
}

// sortAssignmentTypes sorts assignment types for deterministic output using
// STACKIT's historical priority order. Delegates to
// reservedip.SortAssignmentTypes.
func sortAssignmentTypes(types []string) {
	reservedip.SortAssignmentTypes(types, stackitAssignmentPriority)
}
