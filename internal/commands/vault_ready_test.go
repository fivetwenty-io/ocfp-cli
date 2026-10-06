package commands

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// readyTestPaths returns a bloc's paths with the log file in a temp dir.
func readyTestPaths(t *testing.T) map[string]string {
	t.Helper()

	paths := getVaultInceptionPaths("ocfp-lab-drgao", false)
	paths["logFile"] = filepath.Join(t.TempDir(), "vault-inception.log")

	return paths
}

func paneCommand(paths map[string]string) string {
	return "tmux capture-pane -t " + paths["tmuxSession"] + " -p -S -200"
}

func TestWaitForVaultReady_NowTargetingIsReady(t *testing.T) {
	fake := installFakeVaultOps(t)
	paths := readyTestPaths(t)
	fake.outputs[paneCommand(paths)] = []string{
		"starting\n",
		"\x1b[33mNow targeting (temporary) \x1b[0mocfp-lab-drgao-inception at http://127.0.0.1:18234\n",
	}

	require.NoError(t, waitForVaultReady(context.Background(), paths, zap.NewNop().Sugar()))
	assert.Equal(t, 1, fake.sleeps)
}

// A rejected root token means the keys on disk do not open this vault. That
// is the one failure the caller may answer by archiving, so it must come back
// as its own error, and at once rather than after the timeout.
func TestWaitForVaultReady_RejectedTokenIsAKeyFailure(t *testing.T) {
	fake := installFakeVaultOps(t)
	paths := readyTestPaths(t)
	fake.outputs[paneCommand(paths)] = []string{
		"\x1b[31m!! The root token in /x/vault/root.key was rejected by OpenBao\x1b[0m\nshutting down OpenBao...\n",
	}

	err := waitForVaultReady(context.Background(), paths, zap.NewNop().Sugar())
	require.ErrorIs(t, err, ErrVaultKeysRejected)
	assert.NotErrorIs(t, err, ErrVaultStartupError)
	assert.Contains(t, err.Error(), "was rejected by OpenBao")
	assert.Zero(t, fake.sleeps, "a key failure must be reported on the first look")
}

func TestWaitForVaultReady_KeyFailuresFromTheLog(t *testing.T) {
	for _, line := range []string{
		"!! The root token in /x/vault/root.key is not a root token",
		"!! Unable to unseal the new (temporary) OpenBao server: invalid key",
	} {
		t.Run(line, func(t *testing.T) {
			installFakeVaultOps(t)
			paths := readyTestPaths(t)
			require.NoError(t, os.WriteFile(paths["logFile"], []byte(line+"\n"), 0o600))

			err := waitForVaultReady(context.Background(), paths, zap.NewNop().Sugar())
			require.ErrorIs(t, err, ErrVaultKeysRejected)
		})
	}
}

// A busy port says nothing about the keys or the data, so it must never be
// mistaken for a key failure that would lead to archiving.
func TestWaitForVaultReady_PortInUseIsEnvironmental(t *testing.T) {
	for _, line := range []string{
		"!! port 18234 is already in use",
		"!! cluster port 19234 is already in use",
		"!! OpenBao reported its listener address was already in use and did not exit within 5s; it has been killed",
		"!! The OpenBao server reported its listener address was already in use",
		"!! no free port found for OpenBao after 10 attempts (last tried 18240)",
		"ERROR: something else went wrong",
	} {
		t.Run(line, func(t *testing.T) {
			fake := installFakeVaultOps(t)
			paths := readyTestPaths(t)
			fake.outputs[paneCommand(paths)] = []string{line + "\n"}

			err := waitForVaultReady(context.Background(), paths, zap.NewNop().Sugar())
			require.ErrorIs(t, err, ErrVaultStartupError)
			assert.False(t, errors.Is(err, ErrVaultKeysRejected))
		})
	}
}

// safe keeps running after it fails to write ~/.saferc, and says so with !!
// lines. Those lines must not end the wait, and the root token safe prints
// with them must never reach an error or the log.
func TestWaitForVaultReady_NonFatalWarningsKeepWaiting(t *testing.T) {
	fake := installFakeVaultOps(t)
	paths := readyTestPaths(t)
	warnings := "!! Unable to save the root token in ~/.saferc: read-only file system\n" +
		"!! The OpenBao server at http://127.0.0.1:18234 is still running.\n" +
		"!! Its root token is s.SECRET-ROOT-TOKEN\n" +
		"!! To reach it: safe target x http://127.0.0.1:18234 && safe auth token\n"
	fake.outputs[paneCommand(paths)] = []string{warnings, warnings + "Now targeting (temporary) x at http://127.0.0.1:18234\n"}

	require.NoError(t, waitForVaultReady(context.Background(), paths, zap.NewNop().Sugar()))
}

// The old fallback asked `vault status` whether anything answered. A vault
// that is unsealed but whose safe has not finished setting it up is not ready,
// so the fallback is gone and only "Now targeting" counts.
func TestWaitForVaultReady_UnsealedWithoutNowTargetingIsNotReady(t *testing.T) {
	bin := t.TempDir()
	writeFakeExecutable(t, bin, "vault", "exit 0")
	t.Setenv("PATH", bin)

	fake := installFakeVaultOps(t)
	paths := readyTestPaths(t)
	fake.outputs[paneCommand(paths)] = []string{"Your Vault Seal Key is SEAL-KEY-SENTINEL\n"}

	err := waitForVaultReady(context.Background(), paths, zap.NewNop().Sugar())
	require.ErrorIs(t, err, ErrVaultNotReady)
	assert.Equal(t, MaxVaultReadyAttempts, fake.sleeps, "the wait must use its whole budget")
	assert.NotContains(t, err.Error(), "SEAL-KEY-SENTINEL")
}

func TestMaxVaultReadyAttemptsCoversRaftElection(t *testing.T) {
	assert.GreaterOrEqual(t, MaxVaultReadyAttempts, 60)
}

func TestRedactVaultOutput(t *testing.T) {
	in := "Your Vault Seal Key is abc123\n!! Its root token is s.xyz\nNow targeting x\n"

	out := redactVaultOutput(in)

	assert.NotContains(t, out, "abc123")
	assert.NotContains(t, out, "s.xyz")
	assert.Contains(t, out, "Seal Key is [redacted]")
	assert.Contains(t, out, "Now targeting x")
}
