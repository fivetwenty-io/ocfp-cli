package config

import "testing"

// TestIsValidCIDR verifies isValidCIDR against known-good and known-bad inputs.
//
// Host-bit policy: inputs with host bits set (e.g. "10.0.0.1/24") are accepted
// when net.ParseCIDR succeeds, because ParseCIDR masks off host bits and returns
// the network address. The function therefore treats any string parseable by
// net.ParseCIDR as valid — callers that require a strict network address must
// enforce that constraint separately.
func TestIsValidCIDR(t *testing.T) {
	t.Parallel()

	cases := []struct {
		input string
		want  bool
	}{
		{"10.0.0.0/24", true},
		{"10.0.0.0/16", true},
		{"10.0.0.0/33", false}, // invalid mask
		{"10.0.0/24", false},   // malformed — missing octet
		{"foo", false},
		{"", false},
		// Host bits set: net.ParseCIDR succeeds (masks to network), so accepted.
		{"10.0.0.1/24", true},
	}

	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()

			got := isValidCIDR(tc.input)
			if got != tc.want {
				t.Errorf("isValidCIDR(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}
