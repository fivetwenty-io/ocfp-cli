package blocinv

import (
	"os"
	"path/filepath"
	"testing"
	"time"

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

// writeState writes a state file for bloc under home in the layout
// home/<bloc>/state/<bloc>.json and returns its path.
func writeState(t *testing.T, home, bloc, content string) string {
	t.Helper()

	return writeFile(t, filepath.Join(home, bloc, "state"), bloc+".json", content)
}

func stateJSON(provider, region, updatedAt string) string {
	return `{"version":"1.0","provider":"` + provider + `","region":"` + region +
		`","createdAt":"2026-01-02T03:04:05Z","updatedAt":"` + updatedAt + `"}`
}

func TestCollect_StateOnlyBloc(t *testing.T) {
	home := t.TempDir()
	path := writeState(t, home, "old-bloc", stateJSON("aws", "us-east-1", "2026-08-01T09:00:00Z"))

	entries, err := Collect(Options{StateHomes: []string{home}})
	require.NoError(t, err)
	require.Len(t, entries, 1)

	old := entries[0]
	assert.Equal(t, "old-bloc", old.Name)
	assert.True(t, old.HasState)
	assert.False(t, old.HasConfig)
	assert.Equal(t, path, old.StatePath)
	assert.Equal(t, "aws", old.Provider, "state supplies the provider when no config does")
	assert.Equal(t, "us-east-1", old.Region)
	assert.True(t, old.UpdatedAt.Equal(time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)))
}

func TestCollect_IgnoresNonBlocStateEntries(t *testing.T) {
	home := t.TempDir()

	for _, dir := range []string{"checkpoints", "logs", "state", "empty-dir"} {
		require.NoError(t, os.MkdirAll(filepath.Join(home, dir, "nested"), 0o750))
	}

	writeFile(t, home, "state.yml", "current_bloc: real\n")
	writeFile(t, home, "state.yml.lock", "")
	writeFile(t, filepath.Join(home, "logs"), "real.log", "log line\n")
	writeState(t, home, "real", stateJSON("pve", "lab", "2026-10-25T18:02:11Z"))

	entries, err := Collect(Options{StateHomes: []string{home}})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "real", entries[0].Name)
}

func TestCollect_MergesConfigAndState(t *testing.T) {
	cfgDir := t.TempDir()
	home := t.TempDir()

	cfgPath := writeFile(t, cfgDir, "config.yml", "blocs:\n  lab:\n    provider: pve\n    region: lab\n")
	statePath := writeState(t, home, "lab", stateJSON("aws", "elsewhere", "2026-10-25T18:02:11Z"))

	entries, err := Collect(Options{ConfigDirs: []string{cfgDir}, StateHomes: []string{home}})
	require.NoError(t, err)
	require.Len(t, entries, 1)

	lab := entries[0]
	assert.True(t, lab.HasConfig)
	assert.True(t, lab.HasState)
	assert.Equal(t, cfgPath, lab.ConfigFile)
	assert.Equal(t, statePath, lab.StatePath)
	assert.Equal(t, "pve", lab.Provider, "config wins over state")
	assert.Equal(t, "lab", lab.Region)
	assert.False(t, lab.UpdatedAt.IsZero())
}

func TestCollect_ConfigWithoutStateHasZeroUpdatedAt(t *testing.T) {
	cfgDir := t.TempDir()
	writeFile(t, cfgDir, "config.yml", "blocs:\n  fresh:\n    provider: pve\n")

	entries, err := Collect(Options{ConfigDirs: []string{cfgDir}, StateHomes: []string{t.TempDir()}})
	require.NoError(t, err)

	fresh := byName(t, entries, "fresh")
	assert.False(t, fresh.HasState)
	assert.Empty(t, fresh.StatePath)
	assert.True(t, fresh.UpdatedAt.IsZero())
}

func TestCollect_FirstStateHomeWinsCollision(t *testing.T) {
	xdg := t.TempDir()
	legacy := t.TempDir()

	xdgPath := writeState(t, xdg, "both", stateJSON("pve", "xdg-region", "2026-10-25T00:00:00Z"))
	writeState(t, legacy, "both", stateJSON("aws", "legacy-region", "2026-01-01T00:00:00Z"))
	legacyOnly := writeState(t, legacy, "legacy-only", stateJSON("aws", "us-east-1", "2026-02-01T00:00:00Z"))

	entries, err := Collect(Options{StateHomes: []string{xdg, legacy}})
	require.NoError(t, err)
	require.Len(t, entries, 2)

	both := byName(t, entries, "both")
	assert.Equal(t, xdgPath, both.StatePath)
	assert.Equal(t, "xdg-region", both.Region)

	only := byName(t, entries, "legacy-only")
	assert.Equal(t, legacyOnly, only.StatePath)
}

func TestCollect_SameStateHomeListedTwice(t *testing.T) {
	home := t.TempDir()
	writeState(t, home, "solo", stateJSON("pve", "lab", "2026-10-25T00:00:00Z"))

	entries, err := Collect(Options{StateHomes: []string{home, home}})
	require.NoError(t, err)
	assert.Len(t, entries, 1)
}

func TestCollect_MissingStateHomeIsNotAnError(t *testing.T) {
	entries, err := Collect(Options{StateHomes: []string{filepath.Join(t.TempDir(), "nope"), ""}})
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestCollect_UnparsableStateIsBroken(t *testing.T) {
	home := t.TempDir()

	writeState(t, home, "alpha", stateJSON("pve", "lab", "2026-10-25T00:00:00Z"))
	badPath := writeState(t, home, "bad", `{"provider": "FAKE-TOKEN-DO-NOT-USE",,, [unterminated`)
	writeState(t, home, "zulu", stateJSON("pve", "lab", "2026-10-25T00:00:00Z"))

	entries, err := Collect(Options{StateHomes: []string{home}})
	require.NoError(t, err)
	require.Len(t, entries, 3)

	names := []string{entries[0].Name, entries[1].Name, entries[2].Name}
	assert.Equal(t, []string{"alpha", "bad", "zulu"}, names, "a broken bloc stays in sorted order")

	bad := entries[1]
	assert.True(t, bad.Broken)
	assert.True(t, bad.HasState)
	assert.Equal(t, badPath, bad.StatePath)
	assert.NotEmpty(t, bad.BrokenError)
	assert.NotContains(t, bad.BrokenError, "FAKE-TOKEN-DO-NOT-USE", "the error never carries the file's contents")
	assert.Equal(t, "broken", bad.Source())

	assert.False(t, entries[0].Broken)
	assert.False(t, entries[2].Broken)
}

func TestCollect_BrokenStateKeepsConfigFields(t *testing.T) {
	cfgDir := t.TempDir()
	home := t.TempDir()

	writeFile(t, cfgDir, "config.yml", "blocs:\n  lab:\n    provider: pve\n    region: lab\n")
	writeState(t, home, "lab", "{{{ not state")

	entries, err := Collect(Options{ConfigDirs: []string{cfgDir}, StateHomes: []string{home}})
	require.NoError(t, err)

	lab := byName(t, entries, "lab")
	assert.True(t, lab.Broken)
	assert.True(t, lab.HasConfig)
	assert.Equal(t, "pve", lab.Provider)
	assert.True(t, lab.UpdatedAt.IsZero())
}

func TestCollect_UnreadableStateIsBroken(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("file modes do not restrict root")
	}

	home := t.TempDir()
	path := writeState(t, home, "locked", stateJSON("pve", "lab", "2026-10-25T00:00:00Z"))
	require.NoError(t, os.Chmod(path, 0o000))
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	entries, err := Collect(Options{StateHomes: []string{home}})
	require.NoError(t, err)

	locked := byName(t, entries, "locked")
	assert.True(t, locked.Broken)
	assert.Contains(t, locked.BrokenError, "cannot be read")
}

func TestEntrySource(t *testing.T) {
	tests := []struct {
		name  string
		entry Entry
		want  string
	}{
		{"config only", Entry{HasConfig: true}, "config"},
		{"state only", Entry{HasState: true}, "state"},
		{"both", Entry{HasConfig: true, HasState: true}, "both"},
		{"broken wins", Entry{HasConfig: true, HasState: true, Broken: true}, "broken"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.entry.Source())
		})
	}
}
