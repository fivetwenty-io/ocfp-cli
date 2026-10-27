package ssh_test

import (
	"context"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/bastion/ssh"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const fetchedKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"

// recordingFetch returns a fetcher that records each username it is asked
// for and answers with one key.
func recordingFetch(asked *[]string) ssh.KeyFetchFunc {
	return func(_ context.Context, username string) ([]string, error) {
		*asked = append(*asked, username)

		return []string{fetchedKey}, nil
	}
}

func TestResolveKeySpecsAcceptsHostAliases(t *testing.T) {
	t.Parallel()

	var github, gitlab []string

	resolved, err := ssh.ResolveKeySpecs(context.Background(), map[string]string{
		"alice": "github.com/alice",
		"bob":   "https://gitlab.com/bob.keys",
		"carol": "github/carol",
	}, recordingFetch(&github), recordingFetch(&gitlab))
	require.NoError(t, err)

	assert.ElementsMatch(t, []string{"alice", "carol"}, github)
	assert.Equal(t, []string{"bob"}, gitlab)
	assert.Len(t, resolved, 3)
}

func TestResolveKeySpecsFailsOnAnUnreadableEntry(t *testing.T) {
	t.Parallel()

	var github, gitlab []string

	resolved, err := ssh.ResolveKeySpecs(context.Background(), map[string]string{
		"alice": "github.com/alice",
		"bob":   "bitbucket.org/bob",
	}, recordingFetch(&github), recordingFetch(&gitlab))
	require.Error(t, err)
	require.ErrorIs(t, err, config.ErrBastionKeySpecInvalid)
	assert.Contains(t, err.Error(), "bob")
	assert.Nil(t, resolved)
	assert.Empty(t, github, "nothing is fetched when an entry cannot be read")
}

func TestResolveKeySpecsFailsOnAnEmptyEntry(t *testing.T) {
	t.Parallel()

	var github, gitlab []string

	_, err := ssh.ResolveKeySpecs(context.Background(), map[string]string{
		"blank": "  ",
	}, recordingFetch(&github), recordingFetch(&gitlab))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "blank")
}

// A bare name is neither a GitHub nor a GitLab user, so applying keys fails
// on it before anything is fetched.
func TestResolveKeySpecsFailsOnABareName(t *testing.T) {
	t.Parallel()

	var github, gitlab []string

	resolved, err := ssh.ResolveKeySpecs(context.Background(), map[string]string{
		"alice": "github/alice",
		"wayne": "wayne",
	}, recordingFetch(&github), recordingFetch(&gitlab))
	require.ErrorIs(t, err, config.ErrBastionKeySpecInvalid)
	assert.Contains(t, err.Error(), "keys.wayne")
	assert.Nil(t, resolved)
	assert.Empty(t, github)
	assert.Empty(t, gitlab)
}

// gitlab/alice.keys fetches the account alice.keys, as it did before the
// host forms existed, and never alice.
func TestResolveKeySpecsFetchesShortFormUsernamesVerbatim(t *testing.T) {
	t.Parallel()

	var github, gitlab []string

	_, err := ssh.ResolveKeySpecs(context.Background(), map[string]string{
		"alice": "gitlab/alice.keys",
		"bob":   "github/bob.keys",
		"carol": "https://gitlab.com/carol.keys",
	}, recordingFetch(&github), recordingFetch(&gitlab))
	require.NoError(t, err)

	assert.ElementsMatch(t, []string{"alice.keys", "carol"}, gitlab)
	assert.Equal(t, []string{"bob.keys"}, github)
}
