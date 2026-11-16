package jobexec

import (
	"context"
	"os"
	"slices"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
)

// markerAtStart is what VerbChildEnv held when this binary started, before
// it served a verb.
var markerAtStart = os.Getenv(VerbChildEnv) //nolint:gochecknoglobals // read once at start

// runMarkerVerb logs what VerbChildEnv held when the process started, and
// what it holds while the verb runs.
func runMarkerVerb(_ context.Context, j Job, _ func() (string, error)) (int, error) {
	return 0, j.Log("stdout", "start="+markerAtStart+" now="+os.Getenv(VerbChildEnv))
}

// The wrapper marks the process it starts for a verb, so that a test binary
// whose TestMain does not serve the verb refuses to run its tests in it. The
// verb's process clears the mark before the verb runs, so the processes a
// verb starts are not taken for one.
func TestTheVerbProcessIsMarked(t *testing.T) {
	t.Parallel()

	root := spoolRoot(t)
	rc := newRun(t, storeWith(t, liveRecord()), root, signedRequest(t, goodRequest()))
	rc.opts.start = reexec{env: []string{helperVerbEnv + "=marker"}}.start

	if err := rc.run(t); err != nil {
		t.Fatalf("Run = %v", err)
	}

	var lines []string

	for _, r := range records(t, rc.stdout.Bytes()) {
		if r.Kind == progress.KindLog {
			lines = append(lines, bodyOf[progress.LogBody](t, r).Line)
		}
	}

	if want := []string{"start=1 now="}; !slices.Equal(lines, want) {
		t.Fatalf("the verb logged %q, want %q", lines, want)
	}
}
