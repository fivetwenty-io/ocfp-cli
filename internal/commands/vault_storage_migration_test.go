package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestRenderRaftMigrationConfig_Golden(t *testing.T) {
	cfg, err := renderRaftMigrationConfig(
		"/home/me/.local/share/ocfp/ocfp-lab-drgao/vault/data",
		"/home/me/.local/share/ocfp/ocfp-lab-drgao/vault/data.raft-migrating",
		"19534",
	)
	require.NoError(t, err)

	assert.Equal(t, `storage_source "file" {
  path = "/home/me/.local/share/ocfp/ocfp-lab-drgao/vault/data"
}

storage_destination "raft" {
  path    = "/home/me/.local/share/ocfp/ocfp-lab-drgao/vault/data.raft-migrating"
  node_id = "safe-local"
}

cluster_addr = "https://127.0.0.1:19534"
`, cfg)
}

func TestRenderRaftMigrationConfig_QuotesPaths(t *testing.T) {
	cfg, err := renderRaftMigrationConfig(`/tmp/it's a "bloc"\x/data`, `/tmp/it's a "bloc"\x/data.raft-migrating`, "19534")
	require.NoError(t, err)

	assert.Contains(t, cfg, `path = "/tmp/it's a \"bloc\"\\x/data"`)
	assert.Contains(t, cfg, `path    = "/tmp/it's a \"bloc\"\\x/data.raft-migrating"`)
}

// HCL reads ${ and %{ inside a string as a template, and a control character
// has no safe spelling in every engine's parser, so such a path is refused
// rather than rendered into a config that might point somewhere else.
func TestRenderRaftMigrationConfig_RefusesTemplatesAndControlCharacters(t *testing.T) {
	for _, path := range []string{"/tmp/${HOME}/data", "/tmp/%{x}/data", "/tmp/a\nb/data", "/tmp/a\tb/data"} {
		_, err := renderRaftMigrationConfig(path, "/tmp/staging", "19534")
		require.ErrorIs(t, err, ErrMigrationPathUnsafe, path)

		_, err = renderRaftMigrationConfig("/tmp/data", path, "19534")
		require.ErrorIs(t, err, ErrMigrationPathUnsafe, path)
	}
}

// The node ID is a contract with safe: a migrated raft store records it, and
// safe starts the node under its own constant, so the two must match or the
// single node will not find itself in its own configuration.
func TestSafeLocalRaftNodeIDMatchesSafe(t *testing.T) {
	assert.Equal(t, "safe-local", safeLocalRaftNodeID)
}

// fakeEngineScript is a stand-in for vault or bao that answers `operator
// migrate` the way the real engines do. The behaviour for each migrate run
// comes from the lines of plan, one per run, and the last line repeats:
//
//	ok      writes vault.db and raft/ into the destination and succeeds
//	empty   claims success but writes nothing
//	locked  writes a partial store, then reports a migration lock
//	nofile  reports, as OpenBao does, that it has no file storage backend
//	nofile-vault  reports the same the way HashiCorp Vault does
//	broken  fails with an unrelated error
//
// Every invocation is appended to calls, so a test can see -reset and -h.
const fakeEngineScript = `
dir=$(dirname "$0")
printf '%s\n' "$*" >> "$dir/calls"
case "$*" in
  "operator migrate -h")
    printf 'Usage: %s operator migrate [options]\n\n  This command starts a storage backend migration.\n' "$(basename "$0")"
    exit 0 ;;
  "operator migrate -reset -config "*)
    printf 'Success! Migration lock reset (if it was set).\n'
    exit 0 ;;
  "operator migrate -config "*)
    cfg=$4
    dest=$(sed -n '/storage_destination/,/}/s/^  path *= "\(.*\)"$/\1/p' "$cfg")
    n=$(cat "$dir/runs" 2>/dev/null || echo 0)
    n=$((n + 1))
    printf '%s' "$n" > "$dir/runs"
    step=$(sed -n "${n}p" "$dir/plan")
    [ -n "$step" ] || step=$(tail -n 1 "$dir/plan")
    case "$step" in
      ok)
        mkdir -p "$dest/raft" && printf 'db' > "$dest/vault.db" && printf 'raft' > "$dest/raft/raft.db"
        printf '2026-10-06T12:00:00.000Z [INFO]  copied key: path=core/keyring\n'
        printf 'Success! All of the keys have been migrated.\n'
        exit 0 ;;
      empty)
        printf 'Success! All of the keys have been migrated.\n'
        exit 0 ;;
      locked)
        mkdir -p "$dest/raft" && printf 'db' > "$dest/vault.db"
        printf 'Error migrating: storage migration in progress (started: 2026-10-06T11:00:00Z)\n' >&2
        exit 2 ;;
      nofile)
        printf 'Error migrating: error creating source backend: no Vault storage backend named: "file"\n' >&2
        exit 2 ;;
      nofile-vault)
        printf 'Error migrating: error creating source backend: unknown storage type file\n' >&2
        exit 2 ;;
      *)
        printf 'Error migrating: something else broke\n' >&2
        exit 2 ;;
    esac ;;
esac
printf 'Unknown command\n' >&2
exit 1
`

// installFakeEngine writes a fake engine named name with the given plan and
// returns the tools that use it.
func installFakeEngine(t *testing.T, name string, plan ...string) (inceptionTools, string) {
	t.Helper()

	dir := t.TempDir()
	path := writeFakeExecutable(t, dir, name, fakeEngineScript)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plan"), []byte(strings.Join(plan, "\n")+"\n"), 0o600))

	return inceptionTools{safe: "/opt/safe/bin/safe", engine: inceptionEngine{name: name, path: path}}, dir
}

func engineCalls(t *testing.T, dir string) []string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(dir, "calls")) // #nosec G304 -- test reads its own record file
	if err != nil {
		return nil
	}

	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// migrationPaths returns a bloc layout holding a stopped file vault and both
// keys, with its log directory beside it.
func migrationPaths(t *testing.T) map[string]string {
	t.Helper()

	paths := reconcilePaths(t)
	writeFileData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)

	return paths
}

func migrateForTest(t *testing.T, paths map[string]string, tools inceptionTools) (string, error) {
	t.Helper()

	return migrateFileVaultToRaft(context.Background(), paths, tools, zap.NewNop().Sugar())
}

func globOne(t *testing.T, pattern string) string {
	t.Helper()

	matches, err := filepath.Glob(pattern)
	require.NoError(t, err)
	require.Len(t, matches, 1, pattern)

	return matches[0]
}

func TestMigrateFileVaultToRaft_Succeeds(t *testing.T) {
	for _, engine := range []string{"bao", "vault"} {
		t.Run(engine, func(t *testing.T) {
			paths := migrationPaths(t)
			original := treeDigest(t, paths["vaultDir"])
			tools, dir := installFakeEngine(t, engine, "ok")

			backup, err := migrateForTest(t, paths, tools)
			require.NoError(t, err)

			assert.Equal(t, vaultDataRaft, classifyVaultData(paths["vaultDir"]))
			assert.Equal(t, globOne(t, paths["vaultDir"]+".file-backup-*"), backup)
			assert.Equal(t, original, treeDigest(t, backup), "the backup is the original file store, untouched")
			assert.NoFileExists(t, paths["vaultDir"]+".raft-migration.json")
			assert.NoDirExists(t, paths["vaultDir"]+".raft-migrating")

			info, err := os.Stat(paths["vaultDir"])
			require.NoError(t, err)
			assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())

			calls := engineCalls(t, dir)
			require.Len(t, calls, 2)
			assert.Equal(t, "operator migrate -h", calls[0])
			assert.True(t, strings.HasPrefix(calls[1], "operator migrate -config "+paths["logDir"]+"/"), calls[1])

			cfg, err := os.ReadFile(strings.TrimPrefix(calls[1], "operator migrate -config ")) // #nosec G304 -- test reads the config the migration wrote
			require.NoError(t, err)
			assert.Contains(t, string(cfg), `storage_source "file" {`+"\n  path = \""+paths["vaultDir"]+`"`)
			assert.Contains(t, string(cfg), `path    = "`+paths["vaultDir"]+`.raft-migrating"`)

			cfgInfo, err := os.Stat(strings.TrimPrefix(calls[1], "operator migrate -config "))
			require.NoError(t, err)
			assert.Equal(t, os.FileMode(0o600), cfgInfo.Mode().Perm())

			migrationLog := globOne(t, filepath.Join(paths["logDir"], "raft-migration-*.log"))
			logged, err := os.ReadFile(migrationLog) // #nosec G304 -- test reads the log the migration wrote
			require.NoError(t, err)
			assert.Contains(t, string(logged), "All of the keys have been migrated")
		})
	}
}

// An engine that can no longer read file storage, such as OpenBao 2.8, must
// leave the file-backed data exactly as it was, so every later start fails
// the same way instead of starting an empty vault over unmigrated secrets.
func TestMigrateFileVaultToRaft_EngineWithoutFileStorage(t *testing.T) {
	for name, output := range map[string]string{
		"bao":   "nofile",
		"vault": "nofile-vault",
	} {
		t.Run(name, func(t *testing.T) {
			paths := migrationPaths(t)
			before := treeDigest(t, paths["vaultDir"])
			tools, _ := installFakeEngine(t, name, output)

			_, err := migrateForTest(t, paths, tools)
			require.ErrorIs(t, err, ErrEngineCannotReadFile)
			assert.Contains(t, err.Error(), paths["vaultDir"])
			assert.Contains(t, err.Error(), tools.engine.path)
			assert.Contains(t, err.Error(), "SAFE_ENGINE")

			assert.Equal(t, before, treeDigest(t, paths["vaultDir"]), "data must be byte-for-byte unchanged")
			assert.Equal(t, vaultDataFile, classifyVaultData(paths["vaultDir"]))
			globOne(t, paths["vaultDir"]+".raft-failed-*")
			assert.NoDirExists(t, paths["vaultDir"]+".raft-migrating")
			assert.NoFileExists(t, paths["vaultDir"]+".raft-migration.json")
		})
	}
}

func TestIsUnknownFileStorage(t *testing.T) {
	assert.True(t, isUnknownFileStorage(`Error migrating: no Vault storage backend named: "file"`))
	assert.True(t, isUnknownFileStorage("Error migrating: unknown storage type file"))
	assert.False(t, isUnknownFileStorage(`no Vault storage backend named: "raft"`))
	assert.False(t, isUnknownFileStorage("unknown storage type filesystem"))
}

func TestMigrateFileVaultToRaft_FailureKeepsTheData(t *testing.T) {
	for _, plan := range []string{"broken", "empty"} {
		t.Run(plan, func(t *testing.T) {
			paths := migrationPaths(t)
			before := treeDigest(t, filepath.Dir(paths["vaultDir"]))
			tools, _ := installFakeEngine(t, "bao", plan)

			_, err := migrateForTest(t, paths, tools)
			require.ErrorIs(t, err, ErrRaftMigrationFailed)
			assert.Contains(t, err.Error(), paths["vaultDir"])
			assert.Contains(t, err.Error(), paths["logDir"], "the error points at the migration log")

			failed := globOne(t, paths["vaultDir"]+".raft-failed-*")
			after := treeDigest(t, filepath.Dir(paths["vaultDir"]))

			for rel, sum := range before {
				assert.Equal(t, sum, after[rel], rel)
			}

			for rel := range after {
				_, existed := before[rel]
				assert.True(t, existed || strings.HasPrefix(rel, filepath.Base(failed)), "unexpected %s", rel)
			}
		})
	}
}

// An engine without operator migrate must be refused before the journal or
// the staging directory exists.
func TestMigrateFileVaultToRaft_EngineWithoutMigrate(t *testing.T) {
	paths := migrationPaths(t)
	before := treeDigest(t, filepath.Dir(paths["vaultDir"]))

	dir := t.TempDir()
	path := writeFakeExecutable(t, dir, "bao", "printf 'Unknown command\\n' >&2\nexit 1\n")
	tools := inceptionTools{safe: "/opt/safe/bin/safe", engine: inceptionEngine{name: "bao", path: path}}

	_, err := migrateForTest(t, paths, tools)
	require.ErrorIs(t, err, ErrEngineCannotMigrate)
	assert.Contains(t, err.Error(), path)
	assert.Equal(t, before, treeDigest(t, filepath.Dir(paths["vaultDir"])))
}

// A lock left by an interrupted run is reset once. The failed attempt leaves
// raft state in the staging directory, and the engine refuses to migrate into
// a store that already has state, so that attempt is moved aside first.
func TestMigrateFileVaultToRaft_ResetsALeftoverLockOnce(t *testing.T) {
	paths := migrationPaths(t)
	tools, dir := installFakeEngine(t, "bao", "locked", "ok")

	_, err := migrateForTest(t, paths, tools)
	require.NoError(t, err)

	calls := engineCalls(t, dir)
	require.Len(t, calls, 4)
	assert.True(t, strings.HasPrefix(calls[2], "operator migrate -reset -config "), calls[2])
	assert.Equal(t, vaultDataRaft, classifyVaultData(paths["vaultDir"]))

	partial := globOne(t, paths["vaultDir"]+".raft-partial-*")
	assert.FileExists(t, filepath.Join(partial, "vault.db"), "the failed attempt is kept, not deleted")
}

func TestMigrateFileVaultToRaft_LockThatSurvivesTheResetFails(t *testing.T) {
	paths := migrationPaths(t)
	before := treeDigest(t, paths["vaultDir"])
	tools, dir := installFakeEngine(t, "bao", "locked")

	_, err := migrateForTest(t, paths, tools)
	require.ErrorIs(t, err, ErrRaftMigrationFailed)

	resets := 0

	for _, call := range engineCalls(t, dir) {
		if strings.Contains(call, "-reset") {
			resets++
		}
	}

	assert.Equal(t, 1, resets, "the lock is reset once, never in a loop")
	assert.Equal(t, before, treeDigest(t, paths["vaultDir"]))
}

// A staging directory left by some earlier run without a journal is moved
// aside, never deleted and never migrated into.
func TestMigrateFileVaultToRaft_MovesAStrayStagingDirAside(t *testing.T) {
	paths := migrationPaths(t)
	stray := paths["vaultDir"] + ".raft-migrating"
	require.NoError(t, os.MkdirAll(stray, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(stray, "junk"), []byte("junk"), 0o600))

	tools, _ := installFakeEngine(t, "bao", "ok")

	_, err := migrateForTest(t, paths, tools)
	require.NoError(t, err)

	partial := globOne(t, paths["vaultDir"]+".raft-partial-*")
	assert.FileExists(t, filepath.Join(partial, "junk"))
	assert.NoFileExists(t, filepath.Join(paths["vaultDir"], "junk"))
}

func TestUniqueAsidePath(t *testing.T) {
	base := filepath.Join(t.TempDir(), "data.raft-failed-20261006-120000")

	first, err := uniqueAsidePath(base)
	require.NoError(t, err)
	assert.Equal(t, base, first)

	require.NoError(t, os.Mkdir(base, 0o700))

	second, err := uniqueAsidePath(base)
	require.NoError(t, err)
	assert.Equal(t, base+"-2", second)
}
