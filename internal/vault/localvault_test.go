package vault

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// isolateLocalHome points the home directory and ocfp's data and state homes
// at a temporary directory, so teardown never reads or writes a real
// ~/.saferc or bloc directory, and returns that home.
func isolateLocalHome(t *testing.T) string {
	t.Helper()

	home := t.TempDir()

	t.Setenv("HOME", home)
	t.Setenv("OCFP_HOME", filepath.Join(home, "ocfp"))

	return home
}

// captureLocalCommands swaps the runLocalCommand seam for a recorder and
// returns the recorded command lines plus a restore func.
func captureLocalCommands(t *testing.T) *[]string {
	t.Helper()

	isolateLocalHome(t)

	return stubLocalCommands(t)
}

// stubLocalCommands swaps the runLocalCommand seam like captureLocalCommands,
// for a test that has already set up its own home.
func stubLocalCommands(t *testing.T) *[]string {
	t.Helper()

	var recorded []string

	original := runLocalCommand
	runLocalCommand = func(_ context.Context, name string, args ...string) error {
		recorded = append(recorded, name+" "+strings.Join(args, " "))

		return nil
	}

	originalOutput := localCommandOutput
	localCommandOutput = func(context.Context, string, ...string) ([]byte, error) {
		return nil, errLsofFoundNothing
	}

	t.Cleanup(func() {
		runLocalCommand = original
		localCommandOutput = originalOutput
	})

	return &recorded
}

func TestTeardownLocalInception_BlocScoped(t *testing.T) {
	recorded := captureLocalCommands(t)

	err := TeardownLocalInception(context.Background(), "ocfp-lab-drgao")
	require.NoError(t, err)

	assert.Contains(t, *recorded, "tmux kill-session -t ocfp-lab-drgao-inception-vault")
	assert.Contains(t, *recorded, "safe target delete ocfp-lab-drgao-inception")

	joined := strings.Join(*recorded, "\n")
	assert.Contains(t, joined, "pkill", "must stop local safe processes")
	assert.Contains(t, joined, strconv.Itoa(config.InceptionVaultPort("ocfp-lab-drgao")),
		"safe process kill must be scoped to this bloc's port")
}

// TestTeardownLocalInception_DoesNotKillForeignBlocPort is the regression guard
// for the cross-bloc eviction bug: concurrent bootstraps for different blocs on
// one workstation must never tear down each other's inception vault.
func TestTeardownLocalInception_DoesNotKillForeignBlocPort(t *testing.T) {
	recorded := captureLocalCommands(t)

	err := TeardownLocalInception(context.Background(), "ocfp-lab-drgao")
	require.NoError(t, err)

	foreignPort := strconv.Itoa(config.InceptionVaultPort("ocfp-lab-drhu"))
	joined := strings.Join(*recorded, "\n")

	assert.NotContains(t, joined, foreignPort, "must not target a sibling bloc's port")
	assert.NotContains(t, joined, "8234", "must not target the shared legacy port")
}

func TestTeardownLocalInception_BareNamesUseLegacyPort(t *testing.T) {
	recorded := captureLocalCommands(t)

	err := TeardownLocalInception(context.Background(), "")
	require.NoError(t, err)

	joined := strings.Join(*recorded, "\n")
	assert.Contains(t, joined, strconv.Itoa(config.LegacyInceptionVaultPort))
}

func TestTeardownLocalInception_BareNamesWithoutBloc(t *testing.T) {
	recorded := captureLocalCommands(t)

	err := TeardownLocalInception(context.Background(), "")
	require.NoError(t, err)

	assert.Contains(t, *recorded, "tmux kill-session -t inception-vault")
	assert.Contains(t, *recorded, "safe target delete inception")
}

func TestTeardownLocalInception_RejectsInvalidBlocName(t *testing.T) {
	recorded := captureLocalCommands(t)

	err := TeardownLocalInception(context.Background(), "bad bloc; rm -rf /")
	require.Error(t, err)
	assert.Empty(t, *recorded, "no commands may run for an invalid bloc name")
}

func TestTeardownLocalInception_DoesNotRemoveVaultData(t *testing.T) {
	recorded := captureLocalCommands(t)

	err := TeardownLocalInception(context.Background(), "ocfp-lab-drgao")
	require.NoError(t, err)

	joined := strings.Join(*recorded, "\n")
	assert.NotContains(t, joined, "rm ", "teardown must not delete vault data")
}

// fakeLocalOutputs scripts localCommandOutput. Each command line maps to the
// outputs it returns on successive calls, the last one repeating; a command
// with no entry exits 1 with no output, as lsof does when it finds nothing.
type fakeLocalOutputs struct {
	outputs map[string][]string
	errs    map[string]error
}

func installLocalOutputs(t *testing.T) *fakeLocalOutputs {
	t.Helper()

	fake := &fakeLocalOutputs{outputs: map[string][]string{}, errs: map[string]error{}}
	original := localCommandOutput
	localCommandOutput = func(_ context.Context, name string, args ...string) ([]byte, error) {
		line := name + " " + strings.Join(args, " ")
		if err, ok := fake.errs[line]; ok {
			return nil, err
		}

		queue, ok := fake.outputs[line]
		if !ok || len(queue) == 0 {
			return nil, errLsofFoundNothing
		}

		out := queue[0]
		if len(queue) > 1 {
			fake.outputs[line] = queue[1:]
		}

		return []byte(out), nil
	}

	restoreSleep := SetSleepFn(func(time.Duration) {})

	t.Cleanup(func() {
		localCommandOutput = original

		restoreSleep()
	})

	return fake
}

// exitWith returns the error a command exiting with code returns.
func exitWith(code int) error {
	return exec.Command("sh", "-c", "exit "+strconv.Itoa(code)).Run() // #nosec G204 -- fixed shell, integer code
}

// errLsofFoundNothing is how lsof reports that nothing matched.
var errLsofFoundNothing = exitWith(1) //nolint:gochecknoglobals // shared test fixture

// killed lists the kill commands among the recorded command lines.
func killed(recorded []string) []string {
	var kills []string

	for _, line := range recorded {
		if strings.HasPrefix(line, "kill ") {
			kills = append(kills, line)
		}
	}

	return kills
}

func listenerLine(port string) string {
	return "lsof -nP -t -iTCP:" + port + " -sTCP:LISTEN"
}

func holderLine(t *testing.T, blocName string) string {
	t.Helper()

	return "lsof -t " + filepath.Join(mustBlocDir(t, blocName), "vault", "data", "vault.db")
}

// An engine whose safe was killed keeps the port and the raft data. Teardown
// stops it, but only the listener that holds this bloc's vault.db: a sibling
// bloc's vault on a colliding port, or anything else listening there, is
// never touched, and neither is this process.
func TestTeardownLocalInception_KillsTheOrphanedEngineOnly(t *testing.T) {
	t.Setenv("OCFP_HOME", t.TempDir())

	recorded := captureLocalCommands(t)
	fake := installLocalOutputs(t)
	port := localInceptionVaultPort("ocfp-lab-drgao")
	self := strconv.Itoa(os.Getpid())

	fake.outputs[listenerLine(port)] = []string{"4242\n5151\n" + self + "\n"}
	fake.outputs[holderLine(t, "ocfp-lab-drgao")] = []string{"4242\n" + self + "\n", ""}

	require.NoError(t, TeardownLocalInception(context.Background(), "ocfp-lab-drgao"))

	assert.Contains(t, *recorded, "kill -TERM 4242")
	joined := strings.Join(*recorded, "\n")
	assert.NotContains(t, joined, "5151", "a listener that does not hold this bloc's data is not this bloc's engine")
	assert.NotContains(t, joined, "kill -TERM "+self)
	assert.NotContains(t, joined, "kill -KILL", "an engine that let go of its data needs no SIGKILL")
	assert.NotContains(t, joined, strconv.Itoa(config.InceptionVaultPort("ocfp-lab-drhu")))
}

// An engine that ignores SIGTERM is given a grace period and then killed.
func TestTeardownLocalInception_EscalatesAfterAGracePeriod(t *testing.T) {
	t.Setenv("OCFP_HOME", t.TempDir())

	recorded := captureLocalCommands(t)
	fake := installLocalOutputs(t)
	port := localInceptionVaultPort("ocfp-lab-drgao")

	var slept time.Duration

	restore := SetSleepFn(func(d time.Duration) { slept += d })
	t.Cleanup(restore)

	fake.outputs[listenerLine(port)] = []string{"4242\n"}
	fake.outputs[holderLine(t, "ocfp-lab-drgao")] = []string{"4242\n"}

	require.NoError(t, TeardownLocalInception(context.Background(), "ocfp-lab-drgao"))

	assert.Contains(t, *recorded, "kill -TERM 4242")
	assert.Contains(t, *recorded, "kill -KILL 4242")
	assert.GreaterOrEqual(t, slept, orphanEngineGracePeriod, "SIGKILL must wait out the grace period")
}

// When lsof cannot answer, ocfp cannot tell whose process holds the port, so
// it kills nothing.
func TestTeardownLocalInception_LsofFailureKillsNothing(t *testing.T) {
	t.Setenv("OCFP_HOME", t.TempDir())

	recorded := captureLocalCommands(t)
	fake := installLocalOutputs(t)
	port := localInceptionVaultPort("ocfp-lab-drgao")

	fake.errs[listenerLine(port)] = exitWith(2)
	fake.outputs[holderLine(t, "ocfp-lab-drgao")] = []string{"4242\n"}

	require.NoError(t, TeardownLocalInception(context.Background(), "ocfp-lab-drgao"))
	assert.Empty(t, killed(*recorded))
}

// With no raft data there is no lock that ties a listener to this bloc, so
// nothing is killed beyond what the session and pkill already stop.
func TestTeardownLocalInception_NoRaftDataKillsNoListener(t *testing.T) {
	t.Setenv("OCFP_HOME", t.TempDir())

	recorded := captureLocalCommands(t)
	fake := installLocalOutputs(t)
	port := localInceptionVaultPort("ocfp-lab-drgao")

	fake.outputs[listenerLine(port)] = []string{"4242\n"}

	require.NoError(t, TeardownLocalInception(context.Background(), "ocfp-lab-drgao"))
	assert.Empty(t, killed(*recorded))
}

const (
	teardownTestBloc  = "ocfp-lab-drgao"
	teardownTestToken = "hvs.teardowntesttoken0123456789"
)

// writeSafeRC writes a ~/.saferc under home holding one target.
func writeSafeRC(t *testing.T, home, target, url, token string) {
	t.Helper()

	rc := "vaults:\n  " + target + ":\n    url: " + url + "\n    token: " + token + "\n"

	require.NoError(t, os.WriteFile(filepath.Join(home, ".saferc"), []byte(rc), 0o600))
}

// blocPortURL returns the URL of the bloc's local inception vault.
func blocPortURL(bloc string) string {
	return "http://127.0.0.1:" + strconv.Itoa(config.InceptionVaultPort(bloc))
}

// recordLocalCommands records every command and runs onDelete just before
// the safe target is deleted, so a test can see what was on disk then.
func recordLocalCommands(t *testing.T, onDelete func()) *[]string {
	t.Helper()

	recorded := stubLocalCommands(t)
	record := runLocalCommand

	runLocalCommand = func(ctx context.Context, name string, args ...string) error {
		if name == "safe" && len(args) > 1 && args[0] == "target" && args[1] == "delete" && onDelete != nil {
			onDelete()
		}

		return record(ctx, name, args...)
	}

	return recorded
}

func TestTeardownLocalInception_SavesTheTargetTokenBeforeDeletingTheTarget(t *testing.T) {
	home := isolateLocalHome(t)
	writeSafeRC(t, home, teardownTestBloc+"-inception", blocPortURL(teardownTestBloc), teardownTestToken)

	rootKey := filepath.Join(mustBlocDir(t, teardownTestBloc), "vault", "root.key")

	var heldAtDelete string

	recorded := recordLocalCommands(t, func() {
		data, err := os.ReadFile(rootKey)
		if err == nil {
			heldAtDelete = strings.TrimSpace(string(data))
		}
	})

	require.NoError(t, TeardownLocalInception(context.Background(), teardownTestBloc))

	assert.Equal(t, teardownTestToken, heldAtDelete, "root.key must hold the token before the target is deleted")
	assert.Contains(t, *recorded, "safe target delete "+teardownTestBloc+"-inception")

	info, err := os.Stat(rootKey)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestTeardownLocalInception_FillsABlankRootKey(t *testing.T) {
	home := isolateLocalHome(t)
	writeSafeRC(t, home, teardownTestBloc+"-inception", blocPortURL(teardownTestBloc), teardownTestToken)
	stubLocalCommands(t)

	rootKey := filepath.Join(mustBlocDir(t, teardownTestBloc), "vault", "root.key")
	require.NoError(t, os.MkdirAll(filepath.Dir(rootKey), 0o700))
	require.NoError(t, os.WriteFile(rootKey, []byte("  \n"), 0o600))

	require.NoError(t, TeardownLocalInception(context.Background(), teardownTestBloc))

	data, err := os.ReadFile(rootKey)
	require.NoError(t, err)
	assert.Equal(t, teardownTestToken, strings.TrimSpace(string(data)))
}

func TestTeardownLocalInception_KeepsADifferentTokenBesideRootKey(t *testing.T) {
	home := isolateLocalHome(t)
	writeSafeRC(t, home, teardownTestBloc+"-inception", blocPortURL(teardownTestBloc), teardownTestToken)
	stubLocalCommands(t)

	rootKey := filepath.Join(mustBlocDir(t, teardownTestBloc), "vault", "root.key")
	require.NoError(t, os.MkdirAll(filepath.Dir(rootKey), 0o700))
	require.NoError(t, os.WriteFile(rootKey, []byte("hvs.someothertoken9876543210\n"), 0o600))

	require.NoError(t, TeardownLocalInception(context.Background(), teardownTestBloc))

	held, err := os.ReadFile(rootKey)
	require.NoError(t, err)
	assert.Equal(t, "hvs.someothertoken9876543210", strings.TrimSpace(string(held)), "root.key must not be replaced")

	copies, err := filepath.Glob(rootKey + ".saferc-*")
	require.NoError(t, err)
	require.Len(t, copies, 1)

	kept, err := os.ReadFile(copies[0])
	require.NoError(t, err)
	assert.Equal(t, teardownTestToken, strings.TrimSpace(string(kept)))
}

func TestTeardownLocalInception_DeletesNothingWhenTheTokenCannotBeKept(t *testing.T) {
	home := isolateLocalHome(t)
	writeSafeRC(t, home, teardownTestBloc+"-inception", blocPortURL(teardownTestBloc), teardownTestToken)

	recorded := stubLocalCommands(t)

	// A root.key that is not a readable file cannot be compared with the
	// token, so the token cannot be kept.
	rootKey := filepath.Join(mustBlocDir(t, teardownTestBloc), "vault", "root.key")
	require.NoError(t, os.MkdirAll(rootKey, 0o700))

	err := TeardownLocalInception(context.Background(), teardownTestBloc)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to keep the root token")
	assert.NotContains(t, err.Error(), teardownTestToken, "the error must never carry the token")
	assert.Empty(t, *recorded, "nothing may be stopped or deleted when the token was not kept")
}

func TestTeardownLocalInception_IgnoresATargetOnAnotherPort(t *testing.T) {
	home := isolateLocalHome(t)
	writeSafeRC(t, home, teardownTestBloc+"-inception", "http://127.0.0.1:1", teardownTestToken)
	recorded := stubLocalCommands(t)

	require.NoError(t, TeardownLocalInception(context.Background(), teardownTestBloc))

	assert.Contains(t, *recorded, "safe target delete "+teardownTestBloc+"-inception")

	_, err := os.Stat(filepath.Join(mustBlocDir(t, teardownTestBloc), "vault", "root.key"))
	assert.ErrorIs(t, err, os.ErrNotExist, "a target that is not the bloc's own must not be saved")
}

// Without a bloc the token goes to ~/vault.root.key, the root key file of
// that layout. The ~/vault.key an older release wrote stays exactly as it was.
func TestTeardownLocalInception_BareNamesKeepTheTokenInHomeVaultRootKey(t *testing.T) {
	home := isolateLocalHome(t)
	writeSafeRC(t, home, "inception", blocPortURL(""), teardownTestToken)
	stubLocalCommands(t)

	oldKeyFile := filepath.Join(home, "vault.key")
	require.NoError(t, os.WriteFile(oldKeyFile, []byte("older-release-key\n"), 0o600))

	require.NoError(t, TeardownLocalInception(context.Background(), ""))

	data, err := os.ReadFile(filepath.Join(home, "vault.root.key"))
	require.NoError(t, err)
	assert.Equal(t, teardownTestToken, strings.TrimSpace(string(data)))

	kept, err := os.ReadFile(oldKeyFile)
	require.NoError(t, err)
	assert.Equal(t, "older-release-key\n", string(kept))

	saferc, err := filepath.Glob(oldKeyFile + ".*")
	require.NoError(t, err)
	assert.Empty(t, saferc, "nothing is written beside the old key file")
}

// mustBlocDir returns config.OcfpBlocDir for a bloc whose directory
// resolves, failing the test when it does not.
func mustBlocDir(t *testing.T, blocName string) string {
	t.Helper()

	dir, err := config.OcfpBlocDir(blocName)
	if err != nil {
		t.Fatalf("config.OcfpBlocDir(%q) error = %v", blocName, err)
	}

	return dir
}
