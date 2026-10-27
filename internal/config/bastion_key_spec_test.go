package config_test

import (
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testEd25519Key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl wayne@example"

func TestParseBastionKeySpecAcceptsEveryForm(t *testing.T) {
	t.Parallel()

	cases := []struct {
		spec   string
		source config.BastionKeySource
		value  string
	}{
		{"github/alice", config.BastionKeyGitHub, "alice"},
		{"gitlab/bob", config.BastionKeyGitLab, "bob"},
		{"github.com/alice", config.BastionKeyGitHub, "alice"},
		{"gitlab.com/bob", config.BastionKeyGitLab, "bob"},
		{"https://github.com/alice", config.BastionKeyGitHub, "alice"},
		{"https://gitlab.com/bob", config.BastionKeyGitLab, "bob"},
		{"github.com/alice.keys", config.BastionKeyGitHub, "alice"},
		{"https://github.com/alice.keys", config.BastionKeyGitHub, "alice"},
		{"https://gitlab.com/bob.smith.keys", config.BastionKeyGitLab, "bob.smith"},
		{"  github.com/alice  ", config.BastionKeyGitHub, "alice"},
		{testEd25519Key, config.BastionKeyLiteral, testEd25519Key},
	}

	for _, tc := range cases {
		got, err := config.ParseBastionKeySpec(tc.spec)
		require.NoError(t, err, tc.spec)
		assert.Equal(t, tc.source, got.Source, tc.spec)
		assert.Equal(t, tc.value, got.Value, tc.spec)
	}
}

func TestParseBastionKeySpecRejectsWhatItCannotRead(t *testing.T) {
	t.Parallel()

	for _, spec := range []string{
		"",
		"   ",
		"github.com/",
		"github/",
		"github.com/.keys",
		"bitbucket.org/alice",
		"github.com/alice/extra",
		"https://github/alice",
		"http://github.com/alice",
		"alice",
		"ssh-dss AAAAB3NzaC1kc3MAAACBAP",
		"-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAA\n-----END OPENSSH PRIVATE KEY-----",
	} {
		_, err := config.ParseBastionKeySpec(spec)
		require.Error(t, err, "%q must not parse", spec)
		require.ErrorIs(t, err, config.ErrBastionKeySpecInvalid, spec)
	}
}

// A bastion.keys entry that cannot be read must not stop the bloc from
// loading, because read-only commands such as bloc show need it. Validate
// accepts it, and KeyWarnings names it instead.
func TestBastionValidateAcceptsAnUnreadableKeyEntry(t *testing.T) {
	t.Parallel()

	b := config.Bastion{Keys: map[string]string{
		"alice": "github.com/alice",
		"wayne": "wayne",
		"bob":   "bitbucket.org/bob",
	}}

	require.NoError(t, b.Validate())
}

func TestBastionKeyWarningsNameTheBlocTheLabelAndTheReason(t *testing.T) {
	t.Parallel()

	b := config.Bastion{Keys: map[string]string{
		"alice": "github.com/alice",
		"wayne": "wayne",
		"bob":   "bitbucket.org/bob",
	}}

	warnings := b.KeyWarnings("lab1")
	require.Len(t, warnings, 2)

	assert.Contains(t, warnings[0], "bloc lab1")
	assert.Contains(t, warnings[0], "keys.bob")
	assert.Contains(t, warnings[0], "bitbucket.org/bob")
	assert.Contains(t, warnings[0], "want github/<user>")

	assert.Contains(t, warnings[1], "bloc lab1")
	assert.Contains(t, warnings[1], "keys.wayne")
	assert.Contains(t, warnings[1], "names no key source")
	assert.Contains(t, warnings[1], "ocfp init bastion")

	for _, w := range warnings {
		assert.NotContains(t, w, "alice")
	}
}

func TestBastionKeyWarningsAreEmptyForReadableEntries(t *testing.T) {
	t.Parallel()

	b := config.Bastion{Keys: map[string]string{
		"a": "github/alice",
		"d": testEd25519Key,
	}}

	assert.Empty(t, b.KeyWarnings("lab1"))
}

func TestBastionKeyWarningsNeverEchoAPrivateKey(t *testing.T) {
	t.Parallel()

	b := config.Bastion{Keys: map[string]string{
		"oops": "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW\n-----END OPENSSH PRIVATE KEY-----",
	}}

	warnings := b.KeyWarnings("lab1")
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "keys.oops")
	assert.NotContains(t, warnings[0], "b3BlbnNzaC1rZXktdjEA")
	assert.False(t, strings.Contains(warnings[0], "\n"), "one line: %q", warnings[0])
}

// A bare name is not read as a GitHub or GitLab user.
func TestParseBastionKeySpecRejectsABareName(t *testing.T) {
	t.Parallel()

	_, err := config.ParseBastionKeySpec("wayneeseguin")
	require.ErrorIs(t, err, config.ErrBastionKeySpecInvalid)
	assert.Contains(t, err.Error(), "names no key source")
}

func TestBastionValidateAcceptsEveryKeyForm(t *testing.T) {
	t.Parallel()

	b := config.Bastion{Keys: map[string]string{
		"a": "github/alice",
		"b": "gitlab.com/bob",
		"c": "https://github.com/carol.keys",
		"d": testEd25519Key,
	}}

	require.NoError(t, b.Validate())
}

// The short github/ and gitlab/ forms keep the username exactly as written,
// as they always have, so gitlab/alice.keys still names the account
// alice.keys and never alice. Only the host and URL forms drop a trailing
// .keys.
func TestParseBastionKeySpecKeepsShortFormUsernamesVerbatim(t *testing.T) {
	t.Parallel()

	cases := []struct {
		spec   string
		source config.BastionKeySource
		value  string
	}{
		{"gitlab/alice.keys", config.BastionKeyGitLab, "alice.keys"},
		{"github/alice.keys", config.BastionKeyGitHub, "alice.keys"},
		{"gitlab/bob.smith", config.BastionKeyGitLab, "bob.smith"},
		{"gitlab.com/alice.keys", config.BastionKeyGitLab, "alice"},
		{"https://gitlab.com/alice.keys", config.BastionKeyGitLab, "alice"},
		{"github.com/alice.keys.keys", config.BastionKeyGitHub, "alice.keys"},
	}

	for _, tc := range cases {
		got, err := config.ParseBastionKeySpec(tc.spec)
		require.NoError(t, err, tc.spec)
		assert.Equal(t, tc.source, got.Source, tc.spec)
		assert.Equal(t, tc.value, got.Value, tc.spec)
	}
}

// A username ends up in a fetch URL, so one that could reach a different
// path than the user's keys is rejected in every form.
func TestParseBastionKeySpecRejectsUsernamesThatLeaveThePath(t *testing.T) {
	t.Parallel()

	prefixes := []string{
		"github/", "gitlab/",
		"github.com/", "gitlab.com/",
		"https://github.com/", "https://gitlab.com/",
	}
	users := []string{
		"alice/extra",
		"../alice",
		"alice/..",
		"a..b",
		"alice..keys",
		"..",
		"alice?x=1",
		"alice#frag",
		"al ice",
		"al\tice",
		"alice%2F..",
	}

	for _, prefix := range prefixes {
		for _, user := range users {
			spec := prefix + user

			_, err := config.ParseBastionKeySpec(spec)
			require.ErrorIs(t, err, config.ErrBastionKeySpecInvalid, "%q must not parse", spec)
		}
	}
}

// Security-key public keys built for tests: no private key exists for
// either one.
const (
	testSKEd25519Key = "sk-ssh-ed25519@openssh.com AAAAGnNrLXNzaC1lZDI1NTE5QG9wZW5zc2guY29tAAAAIAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8gAAAABHNzaDo= yubikey"
	testSKECDSAKey   = "sk-ecdsa-sha2-nistp256@openssh.com AAAAInNrLWVjZHNhLXNoYTItbmlzdHAyNTZAb3BlbnNzaC5jb20AAAAIbmlzdHAyNTYAAABBBGsX0fLhLEJH+Lzm5WOkQPJ3A32BLeszoPShOUXYmMKWT+NC4v4af5uO5+tKfA+eFivOM1drMV7Oy7ZAaDe/UfUAAAAEc3NoOg=="
)

func TestParseBastionKeySpecAcceptsSecurityKeys(t *testing.T) {
	t.Parallel()

	for _, key := range []string{testSKEd25519Key, testSKECDSAKey} {
		got, err := config.ParseBastionKeySpec(key)
		require.NoError(t, err, key)
		assert.Equal(t, config.BastionKeyLiteral, got.Source, key)
		assert.Equal(t, key, got.Value, key)
	}
}

func TestParseBastionKeySpecRejectsMalformedLiteralKeys(t *testing.T) {
	t.Parallel()

	for _, spec := range []string{
		"ssh-ed25519 not-a-key",
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6",
		"ssh-rsa AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl",
		"sk-ssh-ed25519@openssh.com not-a-key",
		`command="/bin/true" ` + testEd25519Key,
		"garbage line\n" + testEd25519Key,
	} {
		_, err := config.ParseBastionKeySpec(spec)
		require.ErrorIs(t, err, config.ErrBastionKeySpecInvalid, "%q must not parse", spec)
	}
}

// A malformed literal key is a warning at load, like any other entry that
// cannot be read.
func TestBastionKeyWarningsNameAMalformedLiteralKey(t *testing.T) {
	t.Parallel()

	b := config.Bastion{Keys: map[string]string{
		"bad": "ssh-ed25519 not-a-key",
		"sk":  testSKEd25519Key,
	}}

	require.NoError(t, b.Validate())

	warnings := b.KeyWarnings("lab1")
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "keys.bad")
	assert.Contains(t, warnings[0], "not a valid OpenSSH public key")
}

// An entry holding several public keys, as a YAML block scalar does, is
// accepted when every line is a valid key, and keeps every line.
func TestParseBastionKeySpecAcceptsMultipleKeyLines(t *testing.T) {
	t.Parallel()

	spec := "\n" + testEd25519Key + "\n\n  " + testSKEd25519Key + "  \r\n"

	got, err := config.ParseBastionKeySpec(spec)
	require.NoError(t, err)
	assert.Equal(t, config.BastionKeyLiteral, got.Source)
	assert.Equal(t, testEd25519Key+"\n"+testSKEd25519Key, got.Value)
}

// One bad line fails the whole entry.
func TestParseBastionKeySpecRejectsAnyBadKeyLine(t *testing.T) {
	t.Parallel()

	for _, spec := range []string{
		testEd25519Key + "\nssh-ed25519 not-a-key",
		`command="/bin/true" ` + testEd25519Key + "\n" + testSKEd25519Key,
		testSKEd25519Key + "\n" + `from="10.0.0.1" ` + testEd25519Key,
	} {
		_, err := config.ParseBastionKeySpec(spec)
		require.ErrorIs(t, err, config.ErrBastionKeySpecInvalid, "%q must not parse", spec)
	}
}
