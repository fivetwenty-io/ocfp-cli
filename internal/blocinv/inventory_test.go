package blocinv

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeFile writes content to dir/name, creating dir, and returns the path.
func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()

	require.NoError(t, os.MkdirAll(dir, 0o750))

	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	return path
}

// byName returns the entry called name, failing the test when it is absent.
func byName(t *testing.T, entries []Entry, name string) Entry {
	t.Helper()

	entry, ok := Find(entries, name)
	require.True(t, ok, "entry %q not found in %d entries", name, len(entries))

	return entry
}

func TestCollect_BlocsMap(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "config.yml", `
debug: true
blocs:
  lab-one:
    provider: pve
    region: lab
  lab-two:
    provider: aws
    region: us-east-1
`)

	entries, err := Collect(Options{ConfigDirs: []string{dir}})
	require.NoError(t, err)
	require.Len(t, entries, 2)

	one := byName(t, entries, "lab-one")
	assert.True(t, one.HasConfig)
	assert.Equal(t, "pve", one.Provider)
	assert.Equal(t, "lab", one.Region)
	assert.Equal(t, path, one.ConfigFile)
	assert.Equal(t, []string{path}, one.ConfigFiles)

	two := byName(t, entries, "lab-two")
	assert.Equal(t, "aws", two.Provider)
	assert.Equal(t, "us-east-1", two.Region)
}

func TestCollect_FlatFile(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "flat.yaml", "name: flat-bloc\nprovider: stackit\nregion: eu01\n")

	entries, err := Collect(Options{ConfigDirs: []string{dir}})
	require.NoError(t, err)
	require.Len(t, entries, 1)

	flat := byName(t, entries, "flat-bloc")
	assert.True(t, flat.HasConfig)
	assert.Equal(t, "stackit", flat.Provider)
	assert.Equal(t, "eu01", flat.Region)
	assert.Equal(t, path, flat.ConfigFile)
}

func TestCollect_FlatFileNeedsNameAndProvider(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "nameless.yml", "provider: pve\n")
	writeFile(t, dir, "providerless.yml", "name: providerless\n")

	entries, err := Collect(Options{ConfigDirs: []string{dir}})
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestCollect_SameBlocInTwoFiles(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()

	firstPath := writeFile(t, first, "config.yml", "blocs:\n  shared:\n    provider: pve\n    region: lab\n")
	secondPath := writeFile(t, second, "other.yml", "name: shared\nprovider: pve\nregion: elsewhere\n")

	entries, err := Collect(Options{ConfigDirs: []string{first, second}})
	require.NoError(t, err)
	require.Len(t, entries, 1)

	shared := entries[0]
	assert.Equal(t, "shared", shared.Name)
	assert.Equal(t, firstPath, shared.ConfigFile, "the first file in search order is the source")
	assert.Equal(t, []string{firstPath, secondPath}, shared.ConfigFiles)
	assert.Equal(t, "lab", shared.Region, "the first definition supplies the region")
}

func TestCollect_ExplicitFileComesFirst(t *testing.T) {
	dir := t.TempDir()
	dirPath := writeFile(t, dir, "config.yml", "blocs:\n  shared:\n    provider: pve\n")
	explicit := writeFile(t, t.TempDir(), "mine.yml", "blocs:\n  shared:\n    provider: aws\n")

	entries, err := Collect(Options{ConfigFiles: []string{explicit}, ConfigDirs: []string{dir}})
	require.NoError(t, err)

	shared := byName(t, entries, "shared")
	assert.Equal(t, explicit, shared.ConfigFile)
	assert.Equal(t, []string{explicit, dirPath}, shared.ConfigFiles)
	assert.Equal(t, "aws", shared.Provider)
}

func TestCollect_ListsAFileOnceWhenSearchedTwice(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "config.yml", "blocs:\n  only:\n    provider: pve\n")

	entries, err := Collect(Options{ConfigFiles: []string{path}, ConfigDirs: []string{dir, dir}})
	require.NoError(t, err)

	only := byName(t, entries, "only")
	assert.Equal(t, []string{path}, only.ConfigFiles)
}

func TestCollect_SkipsUnreadableAndMissingConfig(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "broken.yml", "blocs: [unterminated\n")
	writeFile(t, dir, "good.yml", "blocs:\n  good:\n    provider: pve\n")

	entries, err := Collect(Options{
		ConfigFiles: []string{filepath.Join(dir, "does-not-exist.yml")},
		ConfigDirs:  []string{dir, filepath.Join(dir, "no-such-dir")},
	})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "good", entries[0].Name)
}

func TestCollect_SortedByName(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "config.yml", "blocs:\n  zulu:\n    provider: pve\n  alpha:\n    provider: pve\n  mike:\n    provider: pve\n")

	entries, err := Collect(Options{ConfigDirs: []string{dir}})
	require.NoError(t, err)

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name)
	}

	assert.Equal(t, []string{"alpha", "mike", "zulu"}, names)
}

func TestCollect_BlocWithEmptyBody(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "config.yml", "blocs:\n  bare:\n")

	entries, err := Collect(Options{ConfigDirs: []string{dir}})
	require.NoError(t, err)

	bare := byName(t, entries, "bare")
	assert.True(t, bare.HasConfig)
	assert.Empty(t, bare.Provider)
}

func TestCollect_SkipsCLIStateFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "state.yml", "current_bloc: lab\nblocs:\n  lab:\n    keys:\n      ssh: FAKE-TOKEN-DO-NOT-USE\n")

	entries, err := Collect(Options{ConfigDirs: []string{dir}})
	require.NoError(t, err)
	assert.Empty(t, entries, "state.yml belongs to the CLI and defines no blocs")
}

func TestFind(t *testing.T) {
	entries := []Entry{{Name: "a"}, {Name: "b"}}

	got, ok := Find(entries, "b")
	assert.True(t, ok)
	assert.Equal(t, "b", got.Name)

	_, ok = Find(entries, "missing")
	assert.False(t, ok)

	_, ok = Find(entries, "")
	assert.False(t, ok)
}
