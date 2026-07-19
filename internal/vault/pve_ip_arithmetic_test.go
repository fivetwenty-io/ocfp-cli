package vault

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
)

// TestPveOffsetIP_CrossesOctetBoundary verifies pveOffsetIP now uses full
// 32-bit IP arithmetic: an offset that pushes the last octet past 255 must
// carry into the next octet instead of being capped/rejected at 254, and an
// offset large enough to cross more than one octet must carry correctly too.
func TestPveOffsetIP_CrossesOctetBoundary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		base string
		off  int
		want string
	}{
		{name: "no crossing", base: "10.64.64.1", off: 19, want: "10.64.64.20"},
		{name: "crosses one octet boundary", base: "10.64.64.250", off: 10, want: "10.64.65.4"},
		{name: "offset beyond historic 254 cap", base: "10.64.64.0", off: 300, want: "10.64.65.44"},
		{name: "crosses two octet boundaries", base: "10.64.255.250", off: 20, want: "10.65.0.14"},
		{name: "negative offset within range", base: "10.64.64.20", off: -10, want: "10.64.64.10"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := pveOffsetIP(tt.base, tt.off)
			if got != tt.want {
				t.Errorf("pveOffsetIP(%q, %d) = %q, want %q", tt.base, tt.off, got, tt.want)
			}
		})
	}
}

// TestPveOffsetIP_InvalidInput verifies the documented empty-string failure
// contract: an unparseable base, or an offset that would push the result
// outside the representable unsigned 32-bit IPv4 space, returns "" rather
// than panicking or wrapping around.
func TestPveOffsetIP_InvalidInput(t *testing.T) {
	t.Parallel()

	if got := pveOffsetIP("not-an-ip", 5); got != "" {
		t.Errorf("pveOffsetIP(unparseable base) = %q, want \"\"", got)
	}

	// 0.0.0.5 - 10 underflows the entire 32-bit address space (not just the
	// last octet), so this must be rejected rather than wrapping.
	if got := pveOffsetIP("0.0.0.5", -10); got != "" {
		t.Errorf("pveOffsetIP(result < 0) = %q, want \"\"", got)
	}

	if got := pveOffsetIP("255.255.255.255", 1); got != "" {
		t.Errorf("pveOffsetIP(result > max uint32) = %q, want \"\"", got)
	}
}

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

	const gateway = "10.64.64.1"

	wantSlices := [3][2]string{
		{"10.64.64.200", "10.64.65.43"},
		{"10.64.65.44", "10.64.65.143"},
		{"10.64.65.144", "10.64.65.243"},
	}

	var prevEndVal uint32

	for i := range 3 {
		start, end := pveFallbackSubnetBand(cfg, gateway, i)

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
		start, end := pveFallbackSubnetBand(cfg, "10.64.64.1", i)
		if start != "10.64.64.100" || end != "10.64.64.50" {
			t.Errorf("subnet %d = [%s,%s], want unsliced shared band [10.64.64.100,10.64.64.50]", i, start, end)
		}
	}
}
