package pve

import (
	"strings"
	"testing"
)

func TestBuildPVEAPITokenHeader(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		cfg         *Config
		want        string
	}{
		{"both fields", &Config{TokenID: "root@pam!foo", TokenSecret: "abc"}, "PVEAPIToken=root@pam!foo=abc"},
		{"missing id", &Config{TokenSecret: "abc"}, ""},
		{"missing secret", &Config{TokenID: "root@pam!foo"}, ""},
		{"whitespace trimmed", &Config{TokenID: " root@pam!foo ", TokenSecret: " abc "}, "PVEAPIToken=root@pam!foo=abc"},
	}

	for _, tc := range tests {
		tc := tc

		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := buildPVEAPITokenHeader(tc.cfg); got != tc.want {
				t.Errorf("buildPVEAPITokenHeader = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestShellPromptRegex_DoesNotMatchMidLineDollar(t *testing.T) {
	t.Parallel()

	// Real shell prompt at end-of-buffer should match
	if !shellPromptRe.MatchString("ubuntu@host:~$ ") {
		t.Error("should match real prompt")
	}

	// "$" in mid-line shouldn't match (no end-anchor satisfaction)
	if shellPromptRe.MatchString("PRICE: $100 USD") {
		t.Error("should not match mid-line $")
	}

	// "#" at end (sudo prompt) should match
	if !shellPromptRe.MatchString("root@host:~# ") {
		t.Error("should match sudo prompt")
	}
}

func TestSeedConstants_PresentAndNonEmpty(t *testing.T) {
	t.Parallel()

	if templateSeedCIUser == "" {
		t.Error("templateSeedCIUser empty")
	}

	if templateSeedCIPassword == "" {
		t.Error("templateSeedCIPassword empty")
	}

	// Password must be long enough that random guessing is impractical
	// during the few minutes the template VM is alive.
	if len(templateSeedCIPassword) < 16 {
		t.Errorf("templateSeedCIPassword too short: %d chars", len(templateSeedCIPassword))
	}

	// Sentinel: catch if someone replaces the password with a real secret.
	if !strings.Contains(templateSeedCIPassword, "OcfpSeed") {
		t.Error("templateSeedCIPassword must keep the OcfpSeed prefix so the ephemeral nature is obvious")
	}
}
