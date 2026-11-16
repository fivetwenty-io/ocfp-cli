package jobexec

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
	"github.com/fivetwenty-io/ocfp-cli/internal/version"
)

// The environment that a test sets for a verb process it starts from this
// test binary.
const (
	// helperVerbEnv names a test verb that the process serves in place of
	// noop.
	helperVerbEnv = "OCFP_JOBEXEC_HELPER_VERB"

	// helperVersionEnv is the version the process claims to be.
	helperVersionEnv = "OCFP_JOBEXEC_HELPER_VERSION"

	// helperUndoFileEnv is the file the wait verb's undo hook writes.
	helperUndoFileEnv = "OCFP_JOBEXEC_HELPER_UNDO_FILE"
)

// helperSelfTimeout ends a helper that a failed test left behind.
const helperSelfTimeout = 30 * time.Second

// TestMain runs the tests, unless this binary was started as `job verb`, the
// way a wrapper starts the binary it is. Then it serves one verb and exits.
func TestMain(m *testing.M) {
	if isJobVerb(os.Args[1:]) {
		os.Exit(helperVerbProcess())
	}

	os.Exit(m.Run())
}

func isJobVerb(args []string) bool {
	return len(args) == 2 && args[0] == "job" && args[1] == "verb"
}

// helperVerbProcess serves one verb as `ocfp job verb` would, with the verb
// table and the version that the helper environment names.
func helperVerbProcess() int {
	cfg := verbProcessConfig()

	if v := os.Getenv(helperVersionEnv); v != "" {
		cfg.version = v
	}

	if name := os.Getenv(helperVerbEnv); name != "" {
		run, ok := helperVerbs[name]
		if !ok {
			_, _ = os.Stderr.WriteString("no helper verb " + name + "\n")

			return 3
		}

		cfg.verbs = map[string]verb{verbNoop: {name: verbNoop, cancelBudget: verbs[verbNoop].cancelBudget, run: run}}
	}

	return serveVerbProcess(context.Background(), cfg, os.Stdin, os.Stdout, os.Stderr)
}

// helperVerbs are the test verbs a helper process can serve in place of noop.
//
//nolint:gochecknoglobals // a test table
var helperVerbs = map[string]func(context.Context, Job, func() (string, error)) (int, error){
	"wait": runWaitVerb,
	"fds":  runFDsVerb,
}

// runWaitVerb starts its step, registers an undo hook, and waits for a
// cancel. The hook writes the file helperUndoFileEnv names, when it names one.
func runWaitVerb(_ context.Context, j Job, _ func() (string, error)) (int, error) {
	err := j.Emit(progress.Event{Kind: progress.EventStepStarted, Step: verbNoop})
	if err != nil {
		return 0, err
	}

	j.OnCancel("helper-undo", func(context.Context) error {
		path := os.Getenv(helperUndoFileEnv)
		if path == "" {
			return nil
		}

		return os.WriteFile(path, []byte(version.Get().Version), 0o600)
	})

	select {
	case <-j.Done():
	case <-time.After(helperSelfTimeout):
	}

	return 0, j.Checkpoint()
}
