package commands

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// fakeVaultOps records every command the inception vault code runs and
// answers port and lock checks from scripted sequences, so the stop and start
// plans can be tested without tmux, lsof, safe, or a real engine.
type fakeVaultOps struct {
	mu       sync.Mutex
	commands []string
	outputs  map[string][]string // command line -> successive outputs; the last repeats
	errs     map[string]error    // command line -> error returned with its output
	portOpen []bool              // successive answers; the last repeats
	portAsks int
	sleeps   int
}

func (f *fakeVaultOps) run(_ context.Context, spec cleanupCommand) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	line := spec.name + " " + strings.Join(spec.args, " ")
	f.commands = append(f.commands, line)

	err := f.errs[line]

	if err == nil && spec.name == "safe" && len(spec.args) == 3 && spec.args[0] == "target" && spec.args[1] == "delete" {
		err = deleteScratchSafeTarget(spec.args[2])
	}

	seq := f.outputs[line]
	if len(seq) == 0 {
		return nil, err
	}

	out := seq[0]
	if len(seq) > 1 {
		f.outputs[line] = seq[1:]
	}

	return []byte(out), err
}

func (f *fakeVaultOps) isPortOpen(_ context.Context, _ string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.portAsks++

	if len(f.portOpen) == 0 {
		return false
	}

	open := f.portOpen[0]
	if len(f.portOpen) > 1 {
		f.portOpen = f.portOpen[1:]
	}

	return open
}

func (f *fakeVaultOps) sleep(time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.sleeps++
}

func (f *fakeVaultOps) commandText() string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return strings.Join(f.commands, "\n")
}

// ran reports whether any recorded command line starts with prefix.
func (f *fakeVaultOps) ran(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, line := range f.commands {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}

	return false
}

// deleteScratchSafeTarget does what safe target delete does to ~/.saferc: it
// removes the named target, so a token read after a stop is gone, as it is in
// production. It edits ~/.saferc only when HOME lies under the temp directory,
// so a test that forgot to point HOME at a scratch directory can never change
// the real file. Without a ~/.saferc there is nothing to delete.
func deleteScratchSafeTarget(name string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("fake safe target delete: %w", err)
	}

	if !strings.HasPrefix(filepath.Clean(home)+string(os.PathSeparator), filepath.Clean(os.TempDir())+string(os.PathSeparator)) {
		return nil
	}

	rcFile := filepath.Join(home, ".saferc")

	data, err := os.ReadFile(rcFile) // #nosec G304 -- the scratch ~/.saferc of a test
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("fake safe target delete: %w", err)
	}

	var rc map[string]any

	err = yaml.Unmarshal(data, &rc)
	if err != nil {
		return fmt.Errorf("fake safe target delete: %w", err)
	}

	vaults, ok := rc["vaults"].(map[string]any)
	if !ok {
		return nil
	}

	delete(vaults, name)

	out, err := yaml.Marshal(rc)
	if err != nil {
		return fmt.Errorf("fake safe target delete: %w", err)
	}

	return os.WriteFile(rcFile, out, 0o600)
}

// installFakeVaultOps swaps the inception vault seam for a fake and restores
// it when the test ends.
func installFakeVaultOps(t *testing.T) *fakeVaultOps {
	t.Helper()

	fake := &fakeVaultOps{outputs: map[string][]string{}, errs: map[string]error{}}
	orig := vaultOps
	vaultOps = inceptionVaultOps{run: fake.run, portOpen: fake.isPortOpen, sleep: fake.sleep}

	t.Cleanup(func() { vaultOps = orig })

	return fake
}

// stopTestPaths returns the bloc's real paths with the key and data files
// redirected into a temp bloc layout that holds a raft vault and both keys.
func stopTestPaths(t *testing.T, bloc string) map[string]string {
	t.Helper()

	paths := mustInceptionPaths(t, bloc, false)

	// Teardown reads the bloc's target from ~/.saferc, which must never be
	// the real one.
	t.Setenv("HOME", t.TempDir())

	vaultRoot := filepath.Join(t.TempDir(), bloc, "vault")
	paths["vaultDir"] = filepath.Join(vaultRoot, "data")
	paths["rootKeyFile"] = filepath.Join(vaultRoot, "root.key")
	paths["unsealKeysFile"] = filepath.Join(vaultRoot, "unseal.keys")
	paths["pidFile"] = filepath.Join(paths["vaultDir"], "vault.pid")

	require.NoError(t, os.MkdirAll(filepath.Join(paths["vaultDir"], "raft"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(paths["vaultDir"], "vault.db"), []byte("raft-data"), 0o600))
	require.NoError(t, os.WriteFile(paths["rootKeyFile"], []byte("test-root-token\n"), 0o600))
	require.NoError(t, os.WriteFile(paths["unsealKeysFile"], []byte("test-unseal-key\n"), 0o600))

	return paths
}

func lsofDataCommand(paths map[string]string) string {
	return "lsof -t " + filepath.Join(paths["vaultDir"], "vault.db")
}

func TestStopInceptionVault_TouchesOnlyOwnBloc(t *testing.T) {
	fake := installFakeVaultOps(t)
	paths := stopTestPaths(t, "ocfp-lab-drgao")

	require.NoError(t, stopInceptionVault(context.Background(), paths, zap.NewNop().Sugar()))

	text := fake.commandText()
	ownPort := strconv.Itoa(config.InceptionVaultPort("ocfp-lab-drgao"))
	foreignPort := strconv.Itoa(config.InceptionVaultPort("ocfp-lab-drhu"))

	assert.Contains(t, text, "tmux kill-session -t ocfp-lab-drgao-inception-vault")
	assert.Contains(t, text, "lsof -ti :"+ownPort+" -sTCP:LISTEN")
	assert.Contains(t, text, "pkill -f safe local.*--port "+ownPort)
	assert.Contains(t, text, "safe target delete ocfp-lab-drgao-inception")
	assert.NotContains(t, text, foreignPort)
	assert.NotContains(t, text, "ocfp-lab-drhu")
}

func TestStopInceptionVault_PollsUntilThePortCloses(t *testing.T) {
	fake := installFakeVaultOps(t)
	fake.portOpen = []bool{true, true, true, false}
	paths := stopTestPaths(t, "ocfp-lab-drgao")

	require.NoError(t, stopInceptionVault(context.Background(), paths, zap.NewNop().Sugar()))

	assert.Equal(t, 4, fake.portAsks, "the stop must keep asking until the port refuses connections")
	assert.GreaterOrEqual(t, fake.sleeps, 3)
	assert.False(t, fake.ran("kill "), "nothing held the data, so nothing needed SIGKILL")
}

// An engine orphaned by a killed safe keeps the bbolt lock on vault.db. The
// stop must find it through lsof and SIGKILL it once the grace period ends.
func TestStopInceptionVault_EscalatesWhenTheDataStaysLocked(t *testing.T) {
	fake := installFakeVaultOps(t)
	paths := stopTestPaths(t, "ocfp-lab-drgao")

	held := make([]string, vaultStopEscalateAfter+1)
	for i := range held {
		held[i] = "4242\n"
	}

	fake.outputs[lsofDataCommand(paths)] = append(held, "")

	sleepsAtKill := -1
	run := vaultOps.run
	vaultOps.run = func(ctx context.Context, spec cleanupCommand) ([]byte, error) {
		if spec.name == "kill" && sleepsAtKill < 0 {
			sleepsAtKill = fake.sleeps
		}

		return run(ctx, spec)
	}

	require.NoError(t, stopInceptionVault(context.Background(), paths, zap.NewNop().Sugar()))

	text := fake.commandText()
	assert.Contains(t, text, "kill -9 4242")
	assert.Less(t, strings.Index(text, "pkill -f"), strings.Index(text, "kill -9 4242"),
		"SIGKILL comes only after the ordinary stop")
	assert.Equal(t, vaultStopEscalateAfter, sleepsAtKill,
		"the engine gets the whole grace period to let go of the data before SIGKILL")
	assert.Equal(t, 1, strings.Count(text, "kill -9 4242"), "one SIGKILL is enough once the lock clears")
}

func TestStopInceptionVault_FailsWhenTheDataNeverUnlocks(t *testing.T) {
	fake := installFakeVaultOps(t)
	paths := stopTestPaths(t, "ocfp-lab-drgao")
	fake.outputs[lsofDataCommand(paths)] = []string{"4242\n"}

	err := stopInceptionVault(context.Background(), paths, zap.NewNop().Sugar())
	require.ErrorIs(t, err, ErrVaultWouldNotStop)
	assert.Contains(t, err.Error(), filepath.Join(paths["vaultDir"], "vault.db"))
	assert.False(t, fake.ran("safe target delete"),
		"a vault that is still running keeps its target")
}

func TestStopInceptionVault_FailsWhenThePortStaysOpen(t *testing.T) {
	fake := installFakeVaultOps(t)
	fake.portOpen = []bool{true}
	paths := stopTestPaths(t, "ocfp-lab-drgao")

	err := stopInceptionVault(context.Background(), paths, zap.NewNop().Sugar())
	require.ErrorIs(t, err, ErrVaultWouldNotStop)
	assert.Contains(t, err.Error(), paths["port"])
}

// Lines that are not PIDs, such as lsof complaining that a file-backed vault
// has no vault.db, must not be taken for a lock holder.
func TestStopInceptionVault_IgnoresLsofNoise(t *testing.T) {
	fake := installFakeVaultOps(t)
	paths := stopTestPaths(t, "ocfp-lab-drgao")
	fake.outputs[lsofDataCommand(paths)] = []string{"lsof: status error on " + paths["vaultDir"] + "/vault.db: No such file or directory\n"}

	require.NoError(t, stopInceptionVault(context.Background(), paths, zap.NewNop().Sugar()))
	assert.False(t, fake.ran("kill "))
}

// lsof exits 1 when nobody has the file open, which is the answer a stop
// waits for. It must not be mistaken for a failure.
func TestStopInceptionVault_LsofFindingNobodyIsNotAFailure(t *testing.T) {
	fake := installFakeVaultOps(t)
	paths := stopTestPaths(t, "ocfp-lab-drgao")
	fake.errs[lsofDataCommand(paths)] = fmt.Errorf("lsof failed: %w", exitError(t, 1))

	require.NoError(t, stopInceptionVault(context.Background(), paths, zap.NewNop().Sugar()))
	assert.True(t, fake.ran("safe target delete"))
}

// A missing or failing lsof says nothing about who holds vault.db. Taking
// that silence for "nobody" would let a restart or an archive run under an
// orphaned engine, so the stop must fail and keep the target.
func TestStopInceptionVault_FailsWhenLsofCannotCheckTheData(t *testing.T) {
	for name, lsofErr := range map[string]error{
		"lsof missing":     fmt.Errorf("lsof failed: %w", exec.ErrNotFound),
		"lsof broke":       fmt.Errorf("lsof failed: %w", exitError(t, 2)),
		"lsof was stopped": errors.New("lsof failed: signal: killed"),
	} {
		t.Run(name, func(t *testing.T) {
			fake := installFakeVaultOps(t)
			paths := stopTestPaths(t, "ocfp-lab-drgao")
			fake.errs[lsofDataCommand(paths)] = lsofErr

			err := stopInceptionVault(context.Background(), paths, zap.NewNop().Sugar())
			require.ErrorIs(t, err, ErrVaultLockUnknown)
			assert.Contains(t, err.Error(), filepath.Join(paths["vaultDir"], "vault.db"))
			assert.False(t, fake.ran("safe target delete"))
			assert.False(t, fake.ran("kill -9"), "an unknown answer is no reason to kill anything")
		})
	}
}

// exitError returns a real *exec.ExitError carrying code.
func exitError(t *testing.T, code int) error {
	t.Helper()

	err := exec.CommandContext(context.Background(), "sh", "-c", "exit "+strconv.Itoa(code)).Run()

	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr)

	return exitErr
}

func TestStopInceptionVault_NeverTouchesKeysOrData(t *testing.T) {
	installFakeVaultOps(t)
	paths := stopTestPaths(t, "ocfp-lab-drgao")

	require.NoError(t, stopInceptionVault(context.Background(), paths, zap.NewNop().Sugar()))

	assert.FileExists(t, paths["rootKeyFile"])
	assert.FileExists(t, paths["unsealKeysFile"])
	assert.FileExists(t, filepath.Join(paths["vaultDir"], "vault.db"))
	assert.DirExists(t, filepath.Join(paths["vaultDir"], "raft"))
}

// cleanupExistingVault is what teardown runs. When the vault will not stop,
// nothing may be archived from under it.
func TestCleanupExistingVault_StopFailureLeavesTheDiskAlone(t *testing.T) {
	fake := installFakeVaultOps(t)
	fake.portOpen = []bool{true}
	paths := stopTestPaths(t, "ocfp-lab-drgao")

	err := cleanupExistingVault(context.Background(), paths, zap.NewNop().Sugar())
	require.ErrorIs(t, err, ErrVaultWouldNotStop)

	assert.FileExists(t, filepath.Join(paths["vaultDir"], "vault.db"))
	assert.FileExists(t, paths["rootKeyFile"])
	assert.FileExists(t, paths["unsealKeysFile"])
}

func TestCleanupExistingVault_ArchivesKeysAndDataTogether(t *testing.T) {
	installFakeVaultOps(t)
	paths := stopTestPaths(t, "ocfp-lab-drgao")
	vaultRoot := filepath.Dir(paths["vaultDir"])

	require.NoError(t, cleanupExistingVault(context.Background(), paths, zap.NewNop().Sugar()))

	assert.NoDirExists(t, vaultRoot)

	archives, err := filepath.Glob(vaultRoot + VaultArchiveSuffix + "*")
	require.NoError(t, err)
	require.Len(t, archives, 1)

	assert.FileExists(t, filepath.Join(archives[0], "data", "vault.db"))
	assert.FileExists(t, filepath.Join(archives[0], "root.key"))
	assert.FileExists(t, filepath.Join(archives[0], "unseal.keys"))
}

// Outside a bloc's vault directory the archive renames only the data
// directory. The key file must move aside with it, not be deleted, or the archived data could
// never be opened again; and it must not stay in place, where the next fresh
// vault would overwrite it.
func TestArchiveAndForgetVault_LegacyLayoutKeepsTheKeyBesideTheArchive(t *testing.T) {
	home := t.TempDir()
	paths := map[string]string{
		"vaultDir":       filepath.Join(home, ".vault"),
		"rootKeyFile":    filepath.Join(home, "vault.key"),
		"unsealKeysFile": filepath.Join(home, "vault.key"),
		"pidFile":        filepath.Join(home, ".vault", "vault.pid"),
		"vaultName":      "inception",
	}

	require.NoError(t, os.MkdirAll(filepath.Join(paths["vaultDir"], "core"), 0o700))
	require.NoError(t, os.WriteFile(paths["rootKeyFile"], []byte("legacy-key\n"), 0o600))

	archived, err := archiveAndForgetVault(paths, "20261006-120000", zap.NewNop().Sugar())
	require.NoError(t, err)

	assert.Equal(t, paths["vaultDir"]+VaultArchiveSuffix+"20261006-120000", archived)
	assert.DirExists(t, filepath.Join(archived, "core"))
	assert.NoFileExists(t, paths["rootKeyFile"])

	key, err := os.ReadFile(paths["rootKeyFile"] + VaultArchiveSuffix + "20261006-120000")
	require.NoError(t, err)
	assert.Equal(t, "legacy-key\n", string(key))
}

// Teardown stops the vault, which deletes its safe target, and then archives
// it. The target's root token goes into the archive with the data: as
// root.key when root.key is missing, and beside root.key when root.key holds
// a different value.
func TestCleanupExistingVault_KeepsTheTargetTokenInTheArchive(t *testing.T) {
	for name, tc := range map[string]struct {
		rootKey  string // "" removes root.key
		wantRoot string
		wantKept bool
	}{
		"root.key missing":   {wantRoot: "s.SAFERC-TOKEN-SENTINEL\n"},
		"root.key different": {rootKey: "test-root-token\n", wantRoot: "test-root-token\n", wantKept: true},
		"root.key the same":  {rootKey: "s.SAFERC-TOKEN-SENTINEL\n", wantRoot: "s.SAFERC-TOKEN-SENTINEL\n"},
	} {
		t.Run(name, func(t *testing.T) {
			installFakeVaultOps(t)
			paths := stopTestPaths(t, "ocfp-lab-drgao")
			vaultRoot := filepath.Dir(paths["vaultDir"])

			require.NoError(t, os.Remove(paths["rootKeyFile"]))

			if tc.rootKey != "" {
				require.NoError(t, os.WriteFile(paths["rootKeyFile"], []byte(tc.rootKey), 0o600))
			}

			writeSafeRC(t, "vaults:\n  "+paths["vaultName"]+":\n    url: http://127.0.0.1:"+paths["port"]+
				"\n    token: s.SAFERC-TOKEN-SENTINEL\n")

			require.NoError(t, cleanupExistingVault(context.Background(), paths, zap.NewNop().Sugar()))

			archives, err := filepath.Glob(vaultRoot + VaultArchiveSuffix + "*")
			require.NoError(t, err)
			require.Len(t, archives, 1)
			assert.Equal(t, tc.wantRoot, readKey(t, filepath.Join(archives[0], "root.key")))

			kept, err := filepath.Glob(filepath.Join(archives[0], "root.key.saferc-*"))
			require.NoError(t, err)

			if !tc.wantKept {
				assert.Empty(t, kept)

				return
			}

			require.Len(t, kept, 1)
			assert.Equal(t, "s.SAFERC-TOKEN-SENTINEL\n", readKey(t, kept[0]))
		})
	}
}
