package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClassifyVaultData(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		dirs  []string
		files []string
		noDir bool
		want  vaultDataState
	}{
		{name: "missing dir", noDir: true, want: vaultDataAbsent},
		{name: "empty dir", want: vaultDataAbsent},
		{name: "only unrelated files", files: []string{"vault.pid", "notes.txt"}, dirs: []string{"logs"}, want: vaultDataAbsent},
		{name: "core alone", dirs: []string{"core"}, want: vaultDataFile},
		{name: "core with file-backend siblings", dirs: []string{"core", "logical", "sys"}, want: vaultDataFile},
		{name: "vault.db alone", files: []string{"vault.db"}, want: vaultDataRaft},
		{name: "raft alone", dirs: []string{"raft"}, want: vaultDataRaft},
		{name: "vault.db with raft", files: []string{"vault.db"}, dirs: []string{"raft"}, want: vaultDataRaft},
		{name: "core with vault.db", files: []string{"vault.db"}, dirs: []string{"core"}, want: vaultDataMixed},
		{name: "core with raft", dirs: []string{"core", "raft"}, want: vaultDataMixed},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dir := filepath.Join(t.TempDir(), "data")

			if !tc.noDir {
				require.NoError(t, os.MkdirAll(dir, 0o700))
			}

			for _, sub := range tc.dirs {
				require.NoError(t, os.MkdirAll(filepath.Join(dir, sub), 0o700))
			}

			for _, file := range tc.files {
				require.NoError(t, os.WriteFile(filepath.Join(dir, file), []byte("x"), 0o600))
			}

			assert.Equal(t, tc.want, classifyVaultData(dir))
		})
	}
}

// A core/ that is a plain file is not the file backend's keyring directory,
// so it must not be mistaken for a file-backed vault.
func TestClassifyVaultData_CoreMustBeADirectory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "core"), []byte("x"), 0o600))

	assert.Equal(t, vaultDataAbsent, classifyVaultData(dir))
}

// When an entry cannot be examined, the classifier cannot prove the data is
// absent. It must refuse rather than report Absent, because Absent leads to a
// fresh vault started over whatever is really there. Self-referencing links
// make the lookups fail the same way for every user, root included.
func TestClassifyVaultData_UnverifiableEntriesAreRefused(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.Symlink("vault.db", filepath.Join(dir, "vault.db")))
	require.NoError(t, os.Symlink("core", filepath.Join(dir, "core")))

	assert.Equal(t, vaultDataMixed, classifyVaultData(dir))
}

func TestVaultDataState_String(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "absent", vaultDataAbsent.String())
	assert.Equal(t, "file", vaultDataFile.String())
	assert.Equal(t, "raft", vaultDataRaft.String())
	assert.Equal(t, "mixed", vaultDataMixed.String())
}
