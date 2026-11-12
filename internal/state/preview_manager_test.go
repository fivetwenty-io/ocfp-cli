package state

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// PreviewManager gives the error that NewManager gives for the same
// directory, and writes nothing, so a dry run refuses where a real run
// would and changes nothing where a real run would create the directory.
func TestPreviewManagerFailsWhereNewManagerFails(t *testing.T) {
	cases := map[string]func(t *testing.T, base string) string{
		"directory exists": func(t *testing.T, base string) string {
			t.Helper()
			dir := filepath.Join(base, "state")
			require.NoError(t, os.Mkdir(dir, 0o750))

			return dir
		},
		"directory missing": func(_ *testing.T, base string) string {
			return filepath.Join(base, "bloc", "state")
		},
		"directory missing with a trailing separator": func(_ *testing.T, base string) string {
			return filepath.Join(base, "bloc", "state") + string(filepath.Separator)
		},
		"path is a file": func(t *testing.T, base string) string {
			t.Helper()
			path := filepath.Join(base, "state")
			require.NoError(t, os.WriteFile(path, []byte("x"), 0o600))

			return path
		},
		"parent is a file": func(t *testing.T, base string) string {
			t.Helper()
			parent := filepath.Join(base, "bloc")
			require.NoError(t, os.WriteFile(parent, []byte("x"), 0o600))

			return filepath.Join(parent, "state")
		},
		"directory is a dangling symbolic link": func(t *testing.T, base string) string {
			t.Helper()
			dir := filepath.Join(base, "state")
			require.NoError(t, os.Symlink(filepath.Join(base, "gone"), dir))

			return dir
		},
		"parent is a dangling symbolic link": func(t *testing.T, base string) string {
			t.Helper()
			parent := filepath.Join(base, "bloc")
			require.NoError(t, os.Symlink(filepath.Join(base, "gone"), parent))

			return filepath.Join(parent, "state")
		},
		"directory is a symbolic link loop": func(t *testing.T, base string) string {
			t.Helper()
			dir := filepath.Join(base, "state")
			require.NoError(t, os.Symlink(dir, dir))

			return dir
		},
		"grandparent is a file": func(t *testing.T, base string) string {
			t.Helper()
			grandparent := filepath.Join(base, "home")
			require.NoError(t, os.WriteFile(grandparent, []byte("x"), 0o600))

			return filepath.Join(grandparent, "bloc", "state")
		},
	}

	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			previewBase, realBase := t.TempDir(), t.TempDir()
			previewDir := setup(t, previewBase)
			realDir := setup(t, realBase)

			before := listTree(t, previewBase)
			preview, previewErr := PreviewManager(previewDir)
			assert.Equal(t, before, listTree(t, previewBase), "PreviewManager must write nothing")

			_, realErr := NewManager(realDir)

			if realErr == nil {
				require.NoError(t, previewErr)
				require.NotNil(t, preview)

				return
			}

			require.Error(t, previewErr)
			assert.Nil(t, preview)
			assert.Equal(t, realErr.Error(), strings.ReplaceAll(previewErr.Error(), previewBase, realBase))
		})
	}
}

// A manager that PreviewManager returns for a missing directory loads an
// empty state, as the real run's manager does once it has made the
// directory.
func TestPreviewManagerLoadsAMissingDirectoryAsEmpty(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bloc", "state")

	mgr, err := PreviewManager(dir)
	require.NoError(t, err)

	loaded, err := mgr.Load("bloc")
	require.NoError(t, err)
	assert.Empty(t, loaded.Outputs)

	_, err = os.Stat(dir)
	assert.ErrorIs(t, err, fs.ErrNotExist)
}

func listTree(t *testing.T, root string) []string {
	t.Helper()

	var paths []string

	require.NoError(t, filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		paths = append(paths, path)

		return err
	}))

	return paths
}
