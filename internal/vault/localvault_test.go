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

// captureLocalCommands swaps the runLocalCommand seam for a recorder and
// returns the recorded command lines plus a restore func.
func captureLocalCommands(t *testing.T) *[]string {
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

func holderLine(blocName string) string {
	return "lsof -t " + filepath.Join(config.OcfpBlocDir(blocName), "vault", "data", "vault.db")
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
	fake.outputs[holderLine("ocfp-lab-drgao")] = []string{"4242\n" + self + "\n", ""}

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
	fake.outputs[holderLine("ocfp-lab-drgao")] = []string{"4242\n"}

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
	fake.outputs[holderLine("ocfp-lab-drgao")] = []string{"4242\n"}

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
