package keyfile

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// replaceSyncs swaps the file and directory syncs for the rest of the test.
func replaceSyncs(t *testing.T, file func(*os.File) error, dir func(string) error) {
	t.Helper()

	origFile, origDir := syncFile, syncDir
	syncFile, syncDir = file, dir

	t.Cleanup(func() { syncFile, syncDir = origFile, origDir })
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path) // #nosec G304 -- the test's own key file
	require.NoError(t, err)

	return string(data)
}

// tempFilesIn lists what a failed write could leave behind beside path.
func tempFilesIn(t *testing.T, path string) []string {
	t.Helper()

	leftovers, err := filepath.Glob(filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".*"))
	require.NoError(t, err)

	return leftovers
}

// A rename can reach the disk before the data it points at, so a power loss
// right after it can leave the only copy of a key empty. The temporary file
// is flushed before it takes the key file's name, and the directory after,
// for both a replacing write and one that must not replace anything.
func TestWrite_SyncsTheFileBeforeItsNameAndTheDirectoryAfter(t *testing.T) {
	for name, write := range map[string]func(path, value string) error{
		"WriteRecovered": WriteRecovered,
		"WriteNew":       WriteNew,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "keys", "root.key")

			var calls []string

			replaceSyncs(t,
				func(f *os.File) error {
					calls = append(calls, "file")

					assert.NoFileExists(t, path, "the file is synced before it takes the key file's name")
					assert.Equal(t, "s.NEW-TOKEN\n", readFile(t, f.Name()))

					return f.Sync()
				},
				func(dir string) error {
					calls = append(calls, "dir")

					assert.Equal(t, filepath.Dir(path), dir)
					assert.Equal(t, "s.NEW-TOKEN\n", readFile(t, path), "the directory is synced after the rename")
					assert.Empty(t, tempFilesIn(t, path), "the temporary name is gone before the directory is synced")

					return syncDirectory(dir)
				})

			require.NoError(t, write(path, "s.NEW-TOKEN"))
			assert.Equal(t, []string{"file", "dir"}, calls)
			assert.Equal(t, "s.NEW-TOKEN\n", readFile(t, path))

			info, err := os.Stat(path)
			require.NoError(t, err)
			assert.Equal(t, os.FileMode(FileMode), info.Mode().Perm())
		})
	}
}

// When the data cannot be flushed, the key file is never replaced, and the
// temporary copy is removed.
func TestWriteRecovered_FileSyncFailureLeavesTheKeyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unseal.keys")
	require.NoError(t, os.WriteFile(path, []byte("old-key\n"), FileMode))

	syncErr := errors.New("disk said no")
	dirSynced := false

	replaceSyncs(t,
		func(*os.File) error { return syncErr },
		func(string) error {
			dirSynced = true

			return nil
		})

	err := WriteRecovered(path, "new-key")
	require.ErrorIs(t, err, syncErr)
	assert.Contains(t, err.Error(), path)
	assert.NotContains(t, err.Error(), "new-key")
	assert.Equal(t, "old-key\n", readFile(t, path))
	assert.Empty(t, tempFilesIn(t, path))
	assert.False(t, dirSynced)
}

// A directory that cannot be flushed leaves the new key in place, since the
// rename already happened, but the write still fails so the caller stops.
func TestWrite_DirectorySyncFailureIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "root.key")
	syncErr := errors.New("directory flush failed")

	replaceSyncs(t, (*os.File).Sync, func(string) error { return syncErr })

	err := WriteNew(path, "s.NEW-TOKEN")
	require.ErrorIs(t, err, syncErr)
	assert.NotErrorIs(t, err, ErrExists)
	assert.Contains(t, err.Error(), filepath.Dir(path))
	assert.Equal(t, "s.NEW-TOKEN\n", readFile(t, path))
	assert.Empty(t, tempFilesIn(t, path))
}

// syncDirectory flushes a real directory, and treats a filesystem that
// cannot sync a directory at all as nothing to do.
func TestSyncDirectory(t *testing.T) {
	require.NoError(t, syncDirectory(t.TempDir()))

	missing := filepath.Join(t.TempDir(), "gone")
	require.Error(t, syncDirectory(missing))

	for _, unsupported := range []error{syscall.EINVAL, syscall.ENOTSUP} {
		assert.True(t, dirSyncUnsupported(&os.PathError{Op: "sync", Path: "x", Err: unsupported}))
	}

	assert.False(t, dirSyncUnsupported(&os.PathError{Op: "sync", Path: "x", Err: syscall.EIO}))
}
