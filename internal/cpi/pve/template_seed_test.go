package pve

import (
	"strings"
	"testing"
)

func TestBuildPVEAPITokenHeader(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  *Config
		want string
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

	if templateSeedPasswordPrefix == "" {
		t.Error("templateSeedPasswordPrefix empty")
	}

	if templateSeedPasswordBytes < 16 {
		t.Errorf("templateSeedPasswordBytes too small: %d", templateSeedPasswordBytes)
	}
}

func TestGenerateSeedPassword(t *testing.T) {
	t.Parallel()

	pw, err := generateSeedPassword()
	if err != nil {
		t.Fatalf("generateSeedPassword: %v", err)
	}

	// Password must keep the prefix so an accidental log capture is
	// recognisable as the ephemeral seed credential.
	if !strings.HasPrefix(pw, templateSeedPasswordPrefix) {
		t.Errorf("generateSeedPassword = %q, want prefix %q", pw, templateSeedPasswordPrefix)
	}

	// Length: prefix + 2 hex chars per random byte.
	wantLen := len(templateSeedPasswordPrefix) + 2*templateSeedPasswordBytes
	if len(pw) != wantLen {
		t.Errorf("generateSeedPassword len = %d, want %d", len(pw), wantLen)
	}

	// Two calls must not collide.
	pw2, err := generateSeedPassword()
	if err != nil {
		t.Fatalf("generateSeedPassword (second call): %v", err)
	}

	if pw == pw2 {
		t.Error("generateSeedPassword returned identical passwords across two calls")
	}
}
