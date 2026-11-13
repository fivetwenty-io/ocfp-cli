package state

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/testenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The manager is built before the guard is set, so NewManager's own check
// doesn't hide the one in Lock.
func TestLockPanicsForALockFileUnderTheRealDirectories(t *testing.T) {
	dir := t.TempDir()

	manager, err := NewManager(dir)
	require.NoError(t, err)

	lockPath := manager.getLockPath("mybloc")
	testenv.GuardOnly(t, lockPath)

	assert.Contains(t, testenv.PanicMessage(func() { _ = manager.Lock("mybloc") }), "state lock")
	assert.NoFileExists(t, lockPath, "the guard must stop Lock before it creates the file")
}

func TestAtomicStateWritePanicsForAFileUnderTheRealDirectories(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mybloc.json")
	testenv.GuardOnly(t, path)

	assert.Contains(t, testenv.PanicMessage(func() { _ = atomicStateWrite(path, []byte("{}")) }), "state write")

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries, "the guard must stop the write before it creates a temporary file")
}
