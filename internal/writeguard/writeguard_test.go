package writeguard_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/writeguard"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recovered runs fn and returns what it panicked with, or nil.
func recovered(fn func()) (value any) {
	defer func() { value = recover() }()

	fn()

	return nil
}

// fakeRealHome makes a temporary directory the real home for one test, and
// marks the process as a test process.
func fakeRealHome(t *testing.T) string {
	t.Helper()

	home := t.TempDir()
	t.Cleanup(writeguard.OverrideRealHome(home))
	t.Setenv(writeguard.EnvVar, "1")
	t.Setenv(writeguard.RootsEnvVar, "")

	return home
}

func TestCheckPanicsUnderEveryRealDirectory(t *testing.T) {
	home := fakeRealHome(t)

	for _, dir := range []string{
		filepath.Join(home, ".ocfp"),
		filepath.Join(home, ".config", "ocfp"),
		filepath.Join(home, ".local", "state", "ocfp"),
		filepath.Join(home, ".local", "share", "ocfp"),
		filepath.Join(home, ".cache", "ocfp"),
	} {
		for _, path := range []string{dir, filepath.Join(dir, "mybloc", "state"), dir + "/x/../y"} {
			value := recovered(func() { writeguard.Check("test write", path) })
			require.NotNil(t, value, "a write to %s must panic", path)

			message, ok := value.(string)
			require.True(t, ok)
			assert.Contains(t, message, "test write would write "+path)
			assert.Contains(t, message, dir)
			assert.Contains(t, message, "writeguard_test.go", "the message carries the stack of the write")
		}
	}
}

func TestCheckAllowsEverythingElse(t *testing.T) {
	home := fakeRealHome(t)

	for _, path := range []string{
		"",
		home,
		filepath.Join(home, ".ocfp-old"),
		filepath.Join(home, ".config", "ocfpx"),
		filepath.Join(home, ".local", "state"),
		filepath.Join(home, ".ssh", "known_hosts"),
		filepath.Join(t.TempDir(), ".local", "state", "ocfp", "mybloc"),
	} {
		assert.Nil(t, recovered(func() { writeguard.Check("test write", path) }), path)
	}
}

func TestCheckDoesNothingOutsideATestProcess(t *testing.T) {
	home := fakeRealHome(t)
	t.Setenv(writeguard.EnvVar, "")

	assert.Nil(t, recovered(func() { writeguard.Check("real write", filepath.Join(home, ".ocfp", "state.yml")) }))
}

func TestCheckGuardsTheRootsTheEnvironmentNamed(t *testing.T) {
	fakeRealHome(t)

	elsewhere := t.TempDir()
	t.Setenv(writeguard.RootsEnvVar, strings.Join([]string{"", elsewhere}, string(os.PathListSeparator)))

	assert.NotNil(t, recovered(func() { writeguard.Check("test write", filepath.Join(elsewhere, "logs")) }))
}

func TestCheckFollowsASymbolicLinkToARealDirectory(t *testing.T) {
	home := fakeRealHome(t)

	elsewhere := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".local", "state"), 0o750))
	require.NoError(t, os.Symlink(elsewhere, filepath.Join(home, ".local", "state", "ocfp")))

	assert.NotNil(t, recovered(func() { writeguard.Check("test write", filepath.Join(elsewhere, "mybloc")) }),
		"a write to the directory the real state directory links to must panic")
}

func TestRootsUsesThePasswdHomeNotHOME(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(writeguard.RootsEnvVar, "")

	roots, err := writeguard.Roots()
	require.NoError(t, err)

	for _, root := range roots {
		assert.False(t, strings.HasPrefix(root, os.Getenv("HOME")), "%s comes from HOME", root)
	}
}
