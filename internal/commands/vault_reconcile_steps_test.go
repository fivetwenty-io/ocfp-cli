package commands

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/config"
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
		[]byte("Your Vault Seal Key is "+testSealKey+"\nNow targeting x\n"), 0o600))
	writeSafeRC(t, "current: other\nvaults:\n"+
		"  other:\n    url: "+url+"\n    token: s.OTHER-TOKEN\n"+
		"  "+paths["vaultName"]+":\n    url: "+url+"\n    token: s.BLOC-TOKEN\n")

	require.NoError(t, recoverInceptionKeys(context.Background(), paths, zap.NewNop().Sugar()))
	assert.Equal(t, testSealKey+"\n", readKey(t, paths["unsealKeysFile"]))
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
	assert.Equal(t, testSealKey+"\n", readKey(t, paths["unsealKeysFile"]))
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
	fake.outputs[historyCommand(paths)] = []string{
		"Your Vault Seal Key is " + testSealKey + "\n",
	}

	require.NoError(t, recoverInceptionKeys(context.Background(), paths, zap.NewNop().Sugar()))
	assert.Equal(t, testSealKey+"\n", readKey(t, paths["unsealKeysFile"]))
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
	require.NoError(t, os.WriteFile(paths["logFile"], []byte("Your Vault Seal Key is "+testSealKey+"\n"), 0o600))

	require.NoError(t, recoverInceptionKeys(context.Background(), paths, zap.NewNop().Sugar()))
	assert.NoFileExists(t, paths["rootKeyFile"])
}

// Each bloc locks its own file under the state home, outside <bloc>/vault,
// because the archive renames that directory away while the lock is held.
func TestInceptionLockFilePerBloc(t *testing.T) {
	t.Setenv("OCFP_HOME", t.TempDir())

	a := getVaultInceptionPaths("ocfp-lab-a", false)
	b := getVaultInceptionPaths("ocfp-lab-b", false)

	assert.Equal(t, filepath.Join(config.StateHome(), "ocfp-lab-a", "inception-vault.lock"), a["lockFile"])
	assert.NotEqual(t, a["lockFile"], b["lockFile"])
	assert.NotContains(t, a["lockFile"], filepath.Dir(a["vaultDir"]))
	assert.NotEqual(t, a["lockFile"], getVaultInceptionPaths("", false)["lockFile"])
	assert.NotEqual(t, getVaultInceptionPaths("", false)["lockFile"], getVaultInceptionPaths("", true)["lockFile"])
}

// A second run for the same bloc waits for the first and then gives up
// without doing anything, while another bloc goes ahead at once.
func TestWithInceptionVaultLock(t *testing.T) {
	t.Setenv("OCFP_HOME", t.TempDir())

	orig := inceptionLockTimeout
	inceptionLockTimeout = 100 * time.Millisecond

	t.Cleanup(func() { inceptionLockTimeout = orig })

	a := getVaultInceptionPaths("ocfp-lab-a", false)
	b := getVaultInceptionPaths("ocfp-lab-b", false)

	err := withInceptionVaultLock(a, func() error {
		ran := false

		require.ErrorIs(t, withInceptionVaultLock(a, func() error {
			ran = true

			return nil
		}), config.ErrFileLockTimeout)
		assert.False(t, ran, "a second run for the same bloc must not run")

		require.NoError(t, withInceptionVaultLock(b, func() error {
			ran = true

			return nil
		}))
		assert.True(t, ran, "another bloc must not wait")

		return nil
	})
	require.NoError(t, err)
}

// testSealKey has the shape of the key safe local prints: one 32-byte share,
// hex-encoded. It is not a real key.
var testSealKey = strings.Repeat("0123456789abcdef", 4)

// safe names the engine in the line that carries the seal key, so a vault
// run on OpenBao prints "Your OpenBao Seal Key is", not "Your Vault ...".
func TestExtractSealKey_ReadsEveryEngineTitle(t *testing.T) {
	for _, title := range []string{"Vault", "OpenBao"} {
		out := "Storing data (encrypted) in /x\nYour " + title + " Seal Key is " + testSealKey + "\nCtrl-C to shut down\n"
		assert.Equal(t, testSealKey, extractSealKey(out), title)
	}
}

// historyCommand is the capture of a bloc's whole pane history. -J joins
// lines tmux wrapped at the pane's width back into the line safe printed.
func historyCommand(paths map[string]string) string {
	return "tmux capture-pane -t " + paths["tmuxSession"] + " -p -J -S -"
}

// wrapAt breaks every line of s at width columns, the way a tmux pane shows
// a long line and the way capture-pane prints it without -J.
func wrapAt(s string, width int) string {
	var b strings.Builder

	for line := range strings.Lines(s) {
		line = strings.TrimSuffix(line, "\n")
		for len(line) > width {
			b.WriteString(line[:width] + "\n")
			line = line[width:]
		}

		b.WriteString(line + "\n")
	}

	return b.String()
}

// fakePaneHistory makes every capture of the bloc's pane return out, wrapped
// at 80 columns unless the capture asks tmux to join wrapped lines.
func fakePaneHistory(fake *fakeVaultOps, paths map[string]string, out string) {
	fake.outputs[historyCommand(paths)] = []string{out}
	for _, start := range []string{"-", "-50", "-200"} {
		fake.outputs["tmux capture-pane -t "+paths["tmuxSession"]+" -p -S "+start] = []string{wrapAt(out, 80)}
	}
}

// The seal key line is longer than an 80-column pane, so a capture without -J
// cuts the key at the pane's edge. The saved key must be the whole key.
func TestSaveVaultKeys_KeepsAWrappedKeyWhole(t *testing.T) {
	for name, logged := range map[string]bool{"from the log": true, "from the pane": false} {
		t.Run(name, func(t *testing.T) {
			fake := installFakeVaultOps(t)
			paths := keyRecoveryPaths(t)
			out := "Now targeting (temporary) x at http://127.0.0.1:1\nYour OpenBao Seal Key is " + testSealKey + "\n"
			fakePaneHistory(fake, paths, out)

			if logged {
				require.NoError(t, os.WriteFile(paths["logFile"], []byte(out), 0o600))
			}

			require.NoError(t, saveVaultKeys(context.Background(), paths, zap.NewNop().Sugar()))
			assert.Equal(t, testSealKey+"\n", readKey(t, paths["unsealKeysFile"]))
		})
	}
}

// Recovery from a running vault's pane reads the same long line.
func TestRecoverInceptionKeys_KeepsAWrappedKeyWhole(t *testing.T) {
	fake := installFakeVaultOps(t)
	paths := keyRecoveryPaths(t)
	writeKeys(t, paths, true, false)
	fakePaneHistory(fake, paths, "Your Vault Seal Key is "+testSealKey+"\n")

	require.NoError(t, recoverInceptionKeys(context.Background(), paths, zap.NewNop().Sugar()))
	assert.Equal(t, testSealKey+"\n", readKey(t, paths["unsealKeysFile"]))
}

// A key that is not 64 hexadecimal digits cannot be the key safe printed, so
// it is never written: a partial key would be fed to the engine on the next
// start and look like a key the engine refused.
func TestSaveVaultKeys_RefusesAMalformedKey(t *testing.T) {
	for name, key := range map[string]string{
		"cut short": testSealKey[:57],
		"not hex":   strings.Repeat("z", sealKeyHexLength),
		"overlong":  testSealKey + "00",
	} {
		t.Run(name, func(t *testing.T) {
			installFakeVaultOps(t)
			paths := keyRecoveryPaths(t)
			url := "http://127.0.0.1:" + paths["port"]
			require.NoError(t, os.WriteFile(paths["logFile"], []byte("Your OpenBao Seal Key is "+key+"\n"), 0o600))
			writeSafeRC(t, "vaults:\n  "+paths["vaultName"]+":\n    url: "+url+"\n    token: s.BLOC-TOKEN\n")

			err := saveVaultKeys(context.Background(), paths, zap.NewNop().Sugar())
			require.ErrorIs(t, err, ErrSealKeyMalformed)
			assert.NotContains(t, err.Error(), key)
			assert.NoFileExists(t, paths["unsealKeysFile"])
			assert.Equal(t, "s.BLOC-TOKEN\n", readKey(t, paths["rootKeyFile"]), "a good root token is still saved")
		})
	}
}

func TestRecoverInceptionKeys_RefusesAMalformedKey(t *testing.T) {
	installFakeVaultOps(t)

	paths := keyRecoveryPaths(t)
	writeKeys(t, paths, true, false)
	require.NoError(t, os.WriteFile(paths["logFile"], []byte("Your Vault Seal Key is "+testSealKey[:56]+"\n"), 0o600))
	before := treeDigest(t, blocDir(paths))

	err := recoverInceptionKeys(context.Background(), paths, zap.NewNop().Sugar())
	require.ErrorIs(t, err, ErrSealKeyMalformed)
	assert.NotContains(t, err.Error(), testSealKey[:56])
	assert.Equal(t, before, treeDigest(t, blocDir(paths)))
}

// A token with whitespace or other characters no engine issues is not
// written either, whether the new vault saves it or recovery does.
func TestRootTokenWritesRefuseAMalformedToken(t *testing.T) {
	installFakeVaultOps(t)

	paths := keyRecoveryPaths(t)
	url := "http://127.0.0.1:" + paths["port"]
	writeSafeRC(t, "vaults:\n  "+paths["vaultName"]+":\n    url: "+url+"\n    token: \"s.BAD TOKEN\"\n")

	err := saveRootTokenFromSafeRC(paths, zap.NewNop().Sugar())
	require.ErrorIs(t, err, ErrRootTokenMalformed)
	assert.NotContains(t, err.Error(), "BAD TOKEN")
	assert.NoFileExists(t, paths["rootKeyFile"])

	writeKeys(t, paths, false, true)
	err = recoverInceptionKeys(context.Background(), paths, zap.NewNop().Sugar())
	require.ErrorIs(t, err, ErrRootTokenMalformed)
	assert.NoFileExists(t, paths["rootKeyFile"])
}
