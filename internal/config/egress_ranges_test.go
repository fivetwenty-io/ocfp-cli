package config

import (
	"net/netip"
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseEgressRangesAcceptsCIDRs checks that each valid range parses to
// its masked prefix, in the order the operator wrote it.
func TestParseEgressRangesAcceptsCIDRs(t *testing.T) {
	t.Parallel()

	got, err := ParseEgressRanges([]string{"203.0.113.0/24", " 198.51.100.7/32 ", "2001:db8::/48"})
	require.NoError(t, err)
	assert.Equal(t, []netip.Prefix{
		netip.MustParsePrefix("203.0.113.0/24"),
		netip.MustParsePrefix("198.51.100.7/32"),
		netip.MustParsePrefix("2001:db8::/48"),
	}, got)
}

// TestParseEgressRangesEmptyMeansUnset checks that an absent setting is not
// an error, because the populate step falls back to the trace endpoint.
func TestParseEgressRangesEmptyMeansUnset(t *testing.T) {
	t.Parallel()

	got, err := ParseEgressRanges(nil)
	require.NoError(t, err)
	assert.Empty(t, got)
}

// TestParseEgressRangesRefusesBadRanges checks every malformed or
// unfiltered range, and that the refusal names the setting and the entry.
func TestParseEgressRangesRefusesBadRanges(t *testing.T) {
	t.Parallel()

	for _, bad := range []string{
		"",
		"   ",
		"not-a-cidr",
		"203.0.113.9",
		"203.0.113.0/33",
		"203.0.113.0/",
		"2001:db8::/129",
		"0.0.0.0/0",
		"::/0",
	} {
		t.Run(bad, func(t *testing.T) {
			t.Parallel()

			_, err := ParseEgressRanges([]string{"198.51.100.0/24", bad})
			require.ErrorIs(t, err, ErrEgressRangeInvalid)
			assert.Contains(t, err.Error(), "egress_ranges")
		})
	}
}

// TestEgressRangesYAMLKey checks the YAML key and that a malformed range
// stops the config from validating.
func TestEgressRangesYAMLKey(t *testing.T) {
	t.Parallel()

	var parsed Config

	require.NoError(t, yaml.Unmarshal([]byte("egress_ranges:\n  - 203.0.113.0/24\n  - 198.51.100.7/32\n"), &parsed))
	assert.Equal(t, []string{"203.0.113.0/24", "198.51.100.7/32"}, parsed.EgressRanges)

	cfg := minimalPVEConfig("root@pam!ocfp-bosh=abc123", "secret-uuid", "", "")
	cfg.EgressRanges = parsed.EgressRanges
	require.NoError(t, validate(cfg))

	cfg.EgressRanges = []string{"203.0.113.0/24", "bogus"}
	require.ErrorIs(t, validate(cfg), ErrEgressRangeInvalid)
}

// TestEgressRangesOmittedWhenUnset checks that an unset setting leaves no
// key in a written config.
func TestEgressRangesOmittedWhenUnset(t *testing.T) {
	t.Parallel()

	out, err := yaml.Marshal(Config{Provider: "pve"})
	require.NoError(t, err)
	assert.NotContains(t, string(out), "egress_ranges")
}
