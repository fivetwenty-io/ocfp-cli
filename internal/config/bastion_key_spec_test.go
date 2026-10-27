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
