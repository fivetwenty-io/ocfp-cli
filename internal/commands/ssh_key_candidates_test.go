package commands

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The bloc key candidates resolveOperatorSSHKey looks at, in order.
var blocKeyCandidates = []string{"id_ed25519", "id_rsa"}

// otherBlocKeyCandidate returns the candidate that is not name.
func otherBlocKeyCandidate(name string) string {
	if name == blocKeyCandidates[0] {
		return blocKeyCandidates[1]
	}

	return blocKeyCandidates[0]
}

// requireNoFallback asserts the resolver failed rather than handing the
// login to the user's own keys.
func requireNoFallback(t *testing.T, blocName string) {
	t.Helper()

	var notice bytes.Buffer

	got, err := resolveOperatorSSHKey("", blocName, nil, &notice)
	require.Error(t, err, "a broken bloc key is not an absent one")
	assert.Empty(t, got)
	assert.Empty(t, notice.String(), "no fallback notice")
}

// An empty candidate is broken, whichever one it is, and the other being
// absent does not turn that into a fallback.
func TestResolveOperatorSSHKeyEmptyCandidateIsAnError(t *testing.T) { //nolint:paralleltest // t.Setenv
	for _, name := range blocKeyCandidates {
		t.Run(name, func(t *testing.T) {
			dir := isolateKeyDirs(t, "ocfp-lab-x")
			require.NoError(t, os.MkdirAll(dir, 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(dir, name), nil, 0o600))

			requireNoFallback(t, "ocfp-lab-x")
		})
	}
}

// A candidate stat cannot inspect for a reason other than not existing,
// here a symlink loop, is an error, whichever one it is.
func TestResolveOperatorSSHKeyUninspectableCandidateIsAnError(t *testing.T) { //nolint:paralleltest // t.Setenv
	for _, name := range blocKeyCandidates {
		t.Run(name, func(t *testing.T) {
			dir := isolateKeyDirs(t, "ocfp-lab-x")
			require.NoError(t, os.MkdirAll(dir, 0o700))

			path := filepath.Join(dir, name)
			require.NoError(t, os.Symlink(path, path))

			requireNoFallback(t, "ocfp-lab-x")
		})
	}
}

// A candidate that exists but cannot be read is an error, whichever one it
// is.
func TestResolveOperatorSSHKeyUnreadableCandidateIsAnError(t *testing.T) { //nolint:paralleltest // t.Setenv
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode 000 file")
	}

	for _, name := range blocKeyCandidates {
		t.Run(name, func(t *testing.T) {
			dir := isolateKeyDirs(t, "ocfp-lab-x")
			require.NoError(t, os.MkdirAll(dir, 0o700))

			path := filepath.Join(dir, name)
			writeTestEd25519Key(t, path)
			require.NoError(t, os.Chmod(path, 0))

			t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

			requireNoFallback(t, "ocfp-lab-x")
		})
	}
}

// Each candidate on its own is used when it is sound.
func TestResolveOperatorSSHKeyUsesEitherCandidateAlone(t *testing.T) { //nolint:paralleltest // t.Setenv
	for _, name := range blocKeyCandidates {
		t.Run(name, func(t *testing.T) {
			dir := isolateKeyDirs(t, "ocfp-lab-x")
			require.NoError(t, os.MkdirAll(dir, 0o700))

			want := filepath.Join(dir, name)
			writeTestEd25519Key(t, want)

			var notice bytes.Buffer

			got, err := resolveOperatorSSHKey("", "ocfp-lab-x", nil, &notice)
			require.NoError(t, err)
			assert.Equal(t, want, got)
			assert.Empty(t, notice.String())
		})
	}
}

// A sound candidate is still used when the other one is broken, as it was
// before the fallback existed.
func TestResolveOperatorSSHKeyUsesTheSoundCandidateBesideABrokenOne(t *testing.T) { //nolint:paralleltest // t.Setenv
	for _, name := range blocKeyCandidates {
		t.Run(name, func(t *testing.T) {
			dir := isolateKeyDirs(t, "ocfp-lab-x")
			require.NoError(t, os.MkdirAll(dir, 0o700))

			want := filepath.Join(dir, name)
			writeTestEd25519Key(t, want)
			require.NoError(t, os.WriteFile(filepath.Join(dir, otherBlocKeyCandidate(name)), nil, 0o600))

			got, err := resolveOperatorSSHKey("", "ocfp-lab-x", nil, &bytes.Buffer{})
			require.NoError(t, err)
			assert.Equal(t, want, got)
		})
	}
}

// A symlink whose target is gone is a broken bloc key, not an absent one,
// even when the other candidate is missing too.
func TestResolveOperatorSSHKeyDanglingSymlinkIsAnError(t *testing.T) { //nolint:paralleltest // t.Setenv
	for _, name := range blocKeyCandidates {
		t.Run(name, func(t *testing.T) {
			dir := isolateKeyDirs(t, "ocfp-lab-x")
			require.NoError(t, os.MkdirAll(dir, 0o700))
			require.NoError(t, os.Symlink(filepath.Join(dir, "gone"), filepath.Join(dir, name)))

			requireNoFallback(t, "ocfp-lab-x")
		})
	}
}

// A candidate that disappears between the stat and the open is unusable,
// and the error never claims the key was absent.
func TestFindSSHKeyFailureAfterStatIsUnusableNotAbsent(t *testing.T) { //nolint:paralleltest // t.Setenv
	dir := isolateKeyDirs(t, "ocfp-lab-x")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "id_ed25519"), []byte("key"), 0o600))

	orig := openSSHKeyFile
	openSSHKeyFile = func(string) (*os.File, error) { return nil, fs.ErrNotExist }

	t.Cleanup(func() { openSSHKeyFile = orig })

	got, err := findSSHKey("ocfp-lab-x", nil)
	require.Error(t, err)
	assert.Empty(t, got)
	assert.ErrorIs(t, err, ErrSSHKeyUnusable)
	assert.NotErrorIs(t, err, fs.ErrNotExist)
}
