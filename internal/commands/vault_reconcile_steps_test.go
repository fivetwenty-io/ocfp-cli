package commands

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func listenerCommand(paths map[string]string) string {
	return "lsof -nP -t -iTCP:" + paths["port"] + " -sTCP:LISTEN"
}

// A raft vault is this bloc's only when the process listening on the port is
// the one holding this bloc's vault.db.
func TestOwnsInceptionVault_RaftNeedsTheListenerToHoldTheData(t *testing.T) {
	for name, tc := range map[string]struct {
		listeners string
		holders   string
		want      bool
	}{
		"same process":        {listeners: "4242\n", holders: "4242\n", want: true},
		"different processes": {listeners: "5151\n", holders: "4242\n", want: false},
		"data not held":       {listeners: "5151\n", want: false},
	} {
		t.Run(name, func(t *testing.T) {
			fake := installFakeVaultOps(t)
			paths := stopTestPaths(t, "ocfp-lab-drgao")

			fake.outputs[listenerCommand(paths)] = []string{tc.listeners}
			if tc.holders != "" {
				fake.outputs[lsofDataCommand(paths)] = []string{tc.holders}
			} else {
				fake.errs[lsofDataCommand(paths)] = exitError(t, lsofNoMatchExit)
			}

			owned, err := ownsInceptionVault(context.Background(), paths, vaultDataRaft)
			require.NoError(t, err)
			assert.Equal(t, tc.want, owned)
		})
	}
}

func TestOwnsInceptionVault_LsofFailureIsNotAnAnswer(t *testing.T) {
	fake := installFakeVaultOps(t)
	paths := stopTestPaths(t, "ocfp-lab-drgao")
	fake.errs[listenerCommand(paths)] = exitError(t, 2)

	_, err := ownsInceptionVault(context.Background(), paths, vaultDataRaft)
	require.ErrorIs(t, err, ErrVaultOwnerUnknown)
}

// Without raft data there is no lock to compare, so the bloc's own tmux
// session is the evidence that the vault on the port is this bloc's.
func TestOwnsInceptionVault_WithoutRaftDataTheSessionDecides(t *testing.T) {
	fake := installFakeVaultOps(t)
	paths := stopTestPaths(t, "ocfp-lab-drgao")
	hasSession := "tmux has-session -t " + paths["tmuxSession"]

	owned, err := ownsInceptionVault(context.Background(), paths, vaultDataFile)
	require.NoError(t, err)
	assert.True(t, owned)

	fake.errs[hasSession] = exitError(t, 1)

	owned, err = ownsInceptionVault(context.Background(), paths, vaultDataFile)
	require.NoError(t, err)
	assert.False(t, owned)
	assert.False(t, fake.ran("lsof"), "lsof has nothing to check without raft data")
}

func TestInceptionClusterPortFree(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	_, port, err := net.SplitHostPort(ln.Addr().String())
	require.NoError(t, err)

	require.Error(t, inceptionClusterPortFree(context.Background(), port), "a held port is not free")
	require.NoError(t, ln.Close())
	require.NoError(t, inceptionClusterPortFree(context.Background(), port))
}

// The fast path reads every registered target rather than the current one,
// which any sibling bloc can move.
func TestInceptionTargetRegistered(t *testing.T) {
	fake := installFakeVaultOps(t)
	paths := stopTestPaths(t, "ocfp-lab-drgao")
	tools := testTools()
	targets := tools.safe + " targets --json"

	url := "http://127.0.0.1:" + paths["port"]
	fake.outputs[targets] = []string{
		`[{"name":"other-inception","url":"` + url + `"},{"name":"` + paths["vaultName"] + `","url":"` + url + `"}]`,
		`[{"name":"` + paths["vaultName"] + `","url":"http://127.0.0.1:1"}]`,
		`not json`,
	}

	assert.True(t, inceptionTargetRegistered(context.Background(), tools.safe, paths))
	assert.False(t, inceptionTargetRegistered(context.Background(), tools.safe, paths), "a target at another URL is not this vault's")
	assert.False(t, inceptionTargetRegistered(context.Background(), tools.safe, paths))
}

// Re-targeting hands safe the root token on stdin from root.key, never on
// the command line.
func TestRetargetInceptionVault_FeedsTheTokenOnStdin(t *testing.T) {
	paths := stopTestPaths(t, "ocfp-lab-drgao")
	record := filepath.Join(t.TempDir(), "calls")

	safe := writeFakeExecutable(t, t.TempDir(), "safe",
		`printf 'args:%s\n' "$*" >> '`+record+`'
case "$*" in
  *"auth token"*) printf 'stdin:%s\n' "$(cat)" >> '`+record+`' ;;
esac
`)

	require.NoError(t, retargetInceptionVault(context.Background(), safe, paths, zap.NewNop().Sugar()))

	calls, err := os.ReadFile(record) // #nosec G304 -- test reads its own record file
	require.NoError(t, err)

	lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
	assert.Equal(t, []string{
		"args:target " + paths["vaultName"] + " http://127.0.0.1:" + paths["port"],
		"args:-T " + paths["vaultName"] + " auth token",
		"stdin:test-root-token",
	}, lines)
}

func TestRetargetInceptionVault_FailureNeverShowsTheToken(t *testing.T) {
	paths := stopTestPaths(t, "ocfp-lab-drgao")
	safe := writeFakeExecutable(t, t.TempDir(), "safe", `case "$*" in
  *"auth token"*) cat; exit 1 ;;
esac
`)

	err := retargetInceptionVault(context.Background(), safe, paths, zap.NewNop().Sugar())
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "test-root-token")
}

// A start must never read the last run's output as its own, where an old
// "Now targeting" would pass for ready and an old rejected-token line would
// archive a vault whose keys are fine. The old log is kept beside it.
func TestSetAsidePreviousVaultLog(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "vault-inception.log")
	require.NoError(t, setAsidePreviousVaultLog(logFile), "a missing log is fine")

	require.NoError(t, os.WriteFile(logFile, []byte("Now targeting old\n"), 0o600))
	require.NoError(t, setAsidePreviousVaultLog(logFile))

	assert.NoFileExists(t, logFile)

	kept, err := os.ReadFile(logFile + ".previous") // #nosec G304 -- test reads the file it set aside
	require.NoError(t, err)
	assert.Equal(t, "Now targeting old\n", string(kept))
}

func TestInceptionKeysUsable(t *testing.T) {
	paths := reconcilePaths(t)
	assert.False(t, inceptionKeysUsable(paths))
	assert.False(t, anyInceptionKeyPresent(paths))

	writeKeys(t, paths, true, false)
	assert.False(t, inceptionKeysUsable(paths))
	assert.True(t, anyInceptionKeyPresent(paths))

	writeKeys(t, paths, true, true)
	assert.True(t, inceptionKeysUsable(paths))

	require.NoError(t, os.WriteFile(paths["unsealKeysFile"], []byte("\n\t \n"), 0o600))
	assert.False(t, inceptionKeysUsable(paths), "a blank key file counts as missing")

	shared := reconcilePaths(t)
	writeKeys(t, shared, true, false)
	shared["unsealKeysFile"] = shared["rootKeyFile"]
	assert.False(t, inceptionKeysUsable(shared), "one file cannot hold both keys")
}

// keyRecoveryPaths is a bloc with a log file and a scratch HOME whose
// .saferc the test writes.
func keyRecoveryPaths(t *testing.T) map[string]string {
	t.Helper()

	paths := reconcilePaths(t)
	require.NoError(t, os.MkdirAll(paths["logDir"], 0o700))
	t.Setenv("HOME", t.TempDir())

	return paths
}

func writeSafeRC(t *testing.T, body string) {
	t.Helper()

	home, err := os.UserHomeDir()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(home, ".saferc"), []byte(body), 0o600))
}

func readKey(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path) // #nosec G304 -- test reads its own key file
	require.NoError(t, err)

	return string(data)
}

func TestRecoverInceptionKeys_WritesOnlyTheMissingFiles(t *testing.T) {
	installFakeVaultOps(t)

	paths := keyRecoveryPaths(t)
	url := "http://127.0.0.1:" + paths["port"]
	require.NoError(t, os.WriteFile(paths["logFile"],
		[]byte("Your Vault Seal Key is LOGGED-SEAL-KEY\nNow targeting x\n"), 0o600))
	writeSafeRC(t, "current: other\nvaults:\n"+
		"  other:\n    url: "+url+"\n    token: s.OTHER-TOKEN\n"+
		"  "+paths["vaultName"]+":\n    url: "+url+"\n    token: s.BLOC-TOKEN\n")

	require.NoError(t, recoverInceptionKeys(context.Background(), paths, zap.NewNop().Sugar()))
	assert.Equal(t, "LOGGED-SEAL-KEY\n", readKey(t, paths["unsealKeysFile"]))
	assert.Equal(t, "s.BLOC-TOKEN\n", readKey(t, paths["rootKeyFile"]), "the token comes from this bloc's own target")

	for _, keyFile := range []string{paths["rootKeyFile"], paths["unsealKeysFile"]} {
		info, err := os.Stat(keyFile)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}

	// A key that is already there is never replaced, even by a different value.
	require.NoError(t, os.WriteFile(paths["rootKeyFile"], []byte("s.KEPT\n"), 0o600))
	require.NoError(t, os.Remove(paths["unsealKeysFile"]))
	require.NoError(t, recoverInceptionKeys(context.Background(), paths, zap.NewNop().Sugar()))
	assert.Equal(t, "s.KEPT\n", readKey(t, paths["rootKeyFile"]))
	assert.Equal(t, "LOGGED-SEAL-KEY\n", readKey(t, paths["unsealKeysFile"]))
}

// A target with this bloc's name but another URL belongs to some other vault,
// and the global current target is never consulted.
func TestRecoverInceptionKeys_IgnoresTargetsForOtherVaults(t *testing.T) {
	installFakeVaultOps(t)

	paths := keyRecoveryPaths(t)
	writeKeys(t, paths, false, true)
	url := "http://127.0.0.1:" + paths["port"]
	writeSafeRC(t, "current: other\nvaults:\n"+
		"  other:\n    url: "+url+"\n    token: s.OTHER-TOKEN\n"+
		"  "+paths["vaultName"]+":\n    url: http://127.0.0.1:1\n    token: s.STALE-TOKEN\n")

	require.NoError(t, recoverInceptionKeys(context.Background(), paths, zap.NewNop().Sugar()))
	assert.NoFileExists(t, paths["rootKeyFile"])
}

// The log is the first place to look, and the tmux pane's history the next.
func TestRecoverInceptionKeys_FallsBackToThePane(t *testing.T) {
	fake := installFakeVaultOps(t)

	paths := keyRecoveryPaths(t)
	writeKeys(t, paths, true, false)
	fake.outputs["tmux capture-pane -t "+paths["tmuxSession"]+" -p -S -"] = []string{
		"Your Vault Seal Key is PANE-SEAL-KEY\n",
	}

	require.NoError(t, recoverInceptionKeys(context.Background(), paths, zap.NewNop().Sugar()))
	assert.Equal(t, "PANE-SEAL-KEY\n", readKey(t, paths["unsealKeysFile"]))
}

func TestRecoverInceptionKeys_NothingToFindChangesNothing(t *testing.T) {
	installFakeVaultOps(t)

	paths := keyRecoveryPaths(t)
	writeKeys(t, paths, false, true)
	before := treeDigest(t, blocDir(paths))

	require.NoError(t, recoverInceptionKeys(context.Background(), paths, zap.NewNop().Sugar()))
	assert.Equal(t, before, treeDigest(t, blocDir(paths)))
}

// One shared key file can never hold both keys, so recovery leaves it alone
// rather than write one key over the other.
func TestRecoverInceptionKeys_LeavesASharedKeyFileAlone(t *testing.T) {
	installFakeVaultOps(t)

	paths := keyRecoveryPaths(t)
	paths["unsealKeysFile"] = paths["rootKeyFile"]
	require.NoError(t, os.WriteFile(paths["logFile"], []byte("Your Vault Seal Key is LOGGED-SEAL-KEY\n"), 0o600))

	require.NoError(t, recoverInceptionKeys(context.Background(), paths, zap.NewNop().Sugar()))
	assert.NoFileExists(t, paths["rootKeyFile"])
}
