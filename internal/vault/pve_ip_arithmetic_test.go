package vault

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
)

// pveOffsetIP's own full-32-bit-arithmetic coverage (octet-boundary
// crossing, negative-offset underflow, malformed-input handling) moved to
// internal/reservedip.TestAddOffsetToIP: pveOffsetIP was retired once
// writeStateReservedBand/writeFallbackSubnet/pveAvailableBand were replaced
// by the shared reservedip engine (see pve_reserved_ips.go), which now does
// this arithmetic for every PVE reserved-IP computation.

// TestPveFallbackSubnetBand_SlicesOctetCrossingBand verifies that a band
// spanning an octet boundary (300 IPs, 10.64.64.200-10.64.65.243) still
// slices into 3 disjoint, contiguous, fully-covering parts, rather than the
// pre-fix behavior of collapsing to the whole shared band once start/end
// differ in their third octet.
func TestPveFallbackSubnetBand_SlicesOctetCrossingBand(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Network: config.NetworkConfig{
			AvailableIPStart: "10.64.64.200",
			AvailableIPEnd:   "10.64.65.243",
		},
	}

	wantSlices := [3][2]string{
		{"10.64.64.200", "10.64.65.43"},
		{"10.64.65.44", "10.64.65.143"},
		{"10.64.65.144", "10.64.65.243"},
	}

	var prevEndVal uint32

	for i := range 3 {
		// defaultStart/defaultEnd are irrelevant here: AvailableIPStart/End
		// is set on cfg, and the config override always wins over the
		// caller-supplied tier default.
		start, end := pveFallbackSubnetBand(cfg, i, "", "")

		if start != wantSlices[i][0] || end != wantSlices[i][1] {
			t.Errorf("slice %d = [%s,%s], want [%s,%s]", i, start, end, wantSlices[i][0], wantSlices[i][1])
		}

		startVal, ok := vaultIPToUint32(start)
		if !ok {
			t.Fatalf("slice %d start %q did not parse", i, start)
		}

		endVal, ok := vaultIPToUint32(end)
		if !ok {
			t.Fatalf("slice %d end %q did not parse", i, end)
		}

		if endVal < startVal {
			t.Fatalf("slice %d end %q is before start %q", i, end, start)
		}

		if i > 0 && startVal != prevEndVal+1 {
			t.Errorf("slice %d starts at %s, want contiguous with previous slice's end (%s)",
				i, start, vaultUint32ToIP(prevEndVal))
		}

		prevEndVal = endVal
	}

	// Full coverage: the last slice's end must equal the configured band end.
	if vaultUint32ToIP(prevEndVal) != cfg.Network.AvailableIPEnd {
		t.Errorf("last slice ends at %s, want band end %s", vaultUint32ToIP(prevEndVal), cfg.Network.AvailableIPEnd)
	}
}

// TestPveFallbackSubnetBand_UnsliceableFallsBackToSharedBand preserves the
// documented graceful-degradation contract: when the band cannot be reasoned
// about (here, an inverted end<=start), the whole band is returned unsliced
// to every subnet index rather than an empty/panicking result.
func TestPveFallbackSubnetBand_UnsliceableFallsBackToSharedBand(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Network: config.NetworkConfig{
			AvailableIPStart: "10.64.64.100",
			AvailableIPEnd:   "10.64.64.50", // inverted: end before start
		},
	}

	for i := range 3 {
		start, end := pveFallbackSubnetBand(cfg, i, "", "")
		if start != "10.64.64.100" || end != "10.64.64.50" {
			t.Errorf("subnet %d = [%s,%s], want unsliced shared band [10.64.64.100,10.64.64.50]", i, start, end)
		}
	}
}

// TestPveCIDRGateway verifies the gateway is the true first host (network
// base + 1), matching internal/bootstrap's CIDRGatewayIP. The non-octet-
// aligned /26 cases guard against the old last-octet string replacement,
// which wrote 10.61.148.1 for every /26 carved from a /24 — an address
// outside each child subnet.
func TestPveCIDRGateway(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cidr string
		want string
	}{
		{"octet-aligned /24", "10.61.148.0/24", "10.61.148.1"},
		{"/26 first child", "10.61.148.64/26", "10.61.148.65"},
		{"/26 second child", "10.61.148.128/26", "10.61.148.129"},
		{"/26 third child", "10.61.148.192/26", "10.61.148.193"},
		{"octet-aligned /22", "10.4.0.0/22", "10.4.0.1"},
		{"host bits set are masked to the base", "10.64.64.7/18", "10.64.64.1"},
		{"bare IP without prefix", "10.61.148.64", ""},
		{"empty", "", ""},
		{"garbage", "not-a-cidr", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := pveCIDRGateway(tt.cidr)
			if got != tt.want {
				t.Errorf("pveCIDRGateway(%q) = %q, want %q", tt.cidr, got, tt.want)
			}
		})
	}
}
