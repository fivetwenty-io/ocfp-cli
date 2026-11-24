//go:build unix

package jobexec

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
)

// fakeGenesis is a stand-in for the genesis executable. It reports on the
// environment it is given and the repository it runs in, fails for the
// environment "broken", and waits for a signal for the environment "slow".
const fakeGenesis = `#!/bin/sh
case "$1" in
broken) echo "partial" ; echo "genesis: cannot read $1" >&2 ; exit 3 ;;
slow) echo started > "$0.started" ; exec sleep 30 ;;
esac
echo "$1 $2 in $(basename "$PWD")"
echo "note for $1" >&2
`

// deploymentsWith builds a deployments directory with the repositories and
// environment files named, as repo to env names, and a fake genesis, and
// returns the genesisInfo that uses them.
func deploymentsWith(t *testing.T, repos map[string][]string) genesisInfo {
	t.Helper()

	root := t.TempDir()
	bin := filepath.Join(root, "genesis")
	deployments := filepath.Join(root, "deployments")

	must(t, os.WriteFile(bin, []byte(fakeGenesis), 0o700)) //nolint:gosec // a test executable

	for repo, envs := range repos {
		must(t, os.MkdirAll(filepath.Join(deployments, repo, ".genesis"), 0o700))

		for _, e := range envs {
			must(t, os.WriteFile(filepath.Join(deployments, repo, e+".yml"), []byte("---\n"), 0o600))
		}
	}

	// Things that are not environments.
	must(t, os.MkdirAll(filepath.Join(deployments, "not-a-repo"), 0o700))
	must(t, os.WriteFile(filepath.Join(deployments, "not-a-repo", "x.yml"), nil, 0o600))
	must(t, os.WriteFile(filepath.Join(deployments, "stray.yml"), nil, 0o600))

	return genesisInfo{
		lookPath:    func(string) (string, error) { return bin, nil },
		deployments: deployments,
	}
}

// outputOf returns the records of a test job after its header, as "stream:
// line" for a log and the step and outcome for an event.
func outputOf(t *testing.T, buf *bytes.Buffer) []string {
	t.Helper()

	var out []string

	for _, r := range records(t, append(headerLine(t), buf.Bytes()...))[1:] {
		switch r.Kind { //nolint:exhaustive // a test reads two kinds
		case progress.KindLog:
			l := bodyOf[progress.LogBody](t, r)
			out = append(out, l.Stream+": "+l.Line)
		case progress.KindEvent:
			e := bodyOf[progress.Event](t, r)
			out = append(out, string(e.Kind)+" "+e.Step+" "+e.Outcome)
		}
	}

	return out
}

func TestGenesisInfoReportsOneEnvironment(t *testing.T) {
	t.Parallel()

	g := deploymentsWith(t, map[string][]string{"ocf": {"lab-a", "lab-b"}})
	g.env = "lab-b"

	j, buf := newTestJob(t)

	code, err := g.run(t.Context(), j)
	if err != nil || code != 0 {
		t.Fatalf("run = %d, %v, want 0", code, err)
	}

	// Genesis's two streams are read side by side, so their order between
	// them is not fixed.
	want := []string{
		"stderr: note for lab-b",
		"stdout: == lab-b ==",
		"stdout: lab-b info in ocf",
		"step_finished genesis.info succeeded",
		"step_started genesis.info ",
	}

	got := outputOf(t, buf)
	slices.Sort(got)

	if !slices.Equal(got, want) {
		t.Fatalf("output\n%q\nwant\n%q", got, want)
	}
}

func TestGenesisInfoReportsEveryEnvironment(t *testing.T) {
	t.Parallel()

	g := deploymentsWith(t, map[string][]string{"mgmt": {"b-mgmt"}, "ocf": {"a-ocf"}})

	j, buf := newTestJob(t)

	code, err := g.run(t.Context(), j)
	if err != nil || code != 0 {
		t.Fatalf("run = %d, %v, want 0", code, err)
	}

	var headers []string

	for _, l := range outputOf(t, buf) {
		if strings.HasPrefix(l, "stdout: == ") {
			headers = append(headers, l)
		}
	}

	if want := []string{"stdout: == b-mgmt ==", "stdout: == a-ocf =="}; !slices.Equal(headers, want) {
		t.Fatalf("environments reported %q, want %q in repository order", headers, want)
	}
}

func TestGenesisInfoReturnsGenesisExitCode(t *testing.T) {
	t.Parallel()

	g := deploymentsWith(t, map[string][]string{"ocf": {"broken"}})
	g.env = "broken"

	j, buf := newTestJob(t)

	code, err := g.run(t.Context(), j)
	if err != nil || code != 3 {
		t.Fatalf("run = %d, %v, want 3 and no error", code, err)
	}

	got := outputOf(t, buf)
	if !slices.Contains(got, "stderr: genesis: cannot read broken") || !slices.Contains(got, "stdout: partial") {
		t.Errorf("output %q lacks Genesis's own lines", got)
	}

	if last := got[len(got)-1]; last != "step_finished genesis.info failed" {
		t.Errorf("last record %q, want a failed step", last)
	}
}

func TestGenesisInfoKeepsGoingAfterAFailure(t *testing.T) {
	t.Parallel()

	g := deploymentsWith(t, map[string][]string{"ocf": {"broken", "fine"}})

	j, buf := newTestJob(t)

	code, err := g.run(t.Context(), j)
	if err != nil || code != 3 {
		t.Fatalf("run = %d, %v, want the first failure, 3", code, err)
	}

	if !slices.Contains(outputOf(t, buf), "stdout: fine info in ocf") {
		t.Errorf("the environment after the failure was not reported:\n%s", buf)
	}
}

func TestGenesisInfoStopsRunningGenesisOnCancel(t *testing.T) {
	t.Parallel()

	g := deploymentsWith(t, map[string][]string{"ocf": {"slow"}})
	g.env = "slow"

	j, _ := newTestJob(t)

	go func() {
		// The fake leaves a file beside itself and then waits, so a cancel
		// after that file appears reaches a Genesis that is running.
		bin, _ := g.lookPath("genesis")

		for {
			_, err := os.Stat(bin + ".started")
			if err == nil {
				break
			}

			time.Sleep(10 * time.Millisecond)
		}

		j.cancel()
	}()

	start := time.Now()

	_, err := g.run(t.Context(), j)
	if !errors.Is(err, ErrCanceled) {
		t.Fatalf("run = %v, want ErrCanceled", err)
	}

	if took := time.Since(start); took > verbs[verbGenesisInfo].cancelBudget {
		t.Fatalf("the cancel took %s, longer than the %s budget", took, verbs[verbGenesisInfo].cancelBudget)
	}
}

func TestGenesisInfoRefusesWithoutGenesis(t *testing.T) {
	t.Parallel()

	g := deploymentsWith(t, map[string][]string{"ocf": {"lab-a"}})
	g.lookPath = func(string) (string, error) { return "", exec.ErrNotFound }

	j, buf := newTestJob(t)

	code, err := g.run(t.Context(), j)
	if err != nil || code != 1 {
		t.Fatalf("run = %d, %v, want 1 and no error", code, err)
	}

	got := outputOf(t, buf)
	if len(got) != 3 || !strings.HasPrefix(got[1], "stderr: genesis is not on this host's PATH") {
		t.Fatalf("output %q does not refuse for a missing genesis", got)
	}

	if got[2] != "step_finished genesis.info failed" {
		t.Errorf("last record %q, want a failed step", got[2])
	}
}

func TestGenesisInfoRefusesAnUnknownEnvironment(t *testing.T) {
	t.Parallel()

	for _, env := range []string{"nope", "-x", "../ocf/lab-a", "lab-a;ls"} {
		g := deploymentsWith(t, map[string][]string{"ocf": {"lab-a"}})
		g.env = env

		j, buf := newTestJob(t)

		code, err := g.run(t.Context(), j)
		if err != nil || code != 1 {
			t.Fatalf("env %q: run = %d, %v, want 1", env, code, err)
		}

		for _, l := range outputOf(t, buf) {
			if strings.HasPrefix(l, "stdout: ") {
				t.Errorf("env %q: Genesis ran: %s", env, l)
			}
		}
	}
}

func TestGenesisInfoRefusesAnEmptyDeployments(t *testing.T) {
	t.Parallel()

	g := deploymentsWith(t, nil)

	j, _ := newTestJob(t)

	if code, err := g.run(t.Context(), j); err != nil || code != 1 {
		t.Fatalf("run = %d, %v, want 1", code, err)
	}

	g.deployments = filepath.Join(t.TempDir(), "missing")

	if code, err := g.run(t.Context(), j); err != nil || code != 1 {
		t.Fatalf("run with no directory = %d, %v, want 1", code, err)
	}
}

func TestOnlyTheGenesisEnvironmentPasses(t *testing.T) {
	t.Parallel()

	got := passedEnv(map[string]string{"PATH": "/evil", "LD_PRELOAD": "x", envGenesisEnv: "lab-a"})
	if want := []string{envGenesisEnv + "=lab-a"}; !slices.Equal(got, want) {
		t.Fatalf("passedEnv = %q, want %q", got, want)
	}

	if got := passedEnv(nil); len(got) != 0 {
		t.Fatalf("passedEnv of nothing = %q", got)
	}
}

// A signed request for genesis.info runs through the wrapper, into a verb
// process, and out as the stream, with the environment the request names.
func TestGenesisInfoRunsThroughTheWrapper(t *testing.T) {
	t.Parallel()

	g := deploymentsWith(t, map[string][]string{"ocf": {"lab-a", "lab-b"}})
	bin, _ := g.lookPath("genesis")

	req := goodRequest()
	req.Verb = verbGenesisInfo
	req.Env = map[string]string{envGenesisEnv: "lab-a", "PATH": "/evil"}

	rc := newRun(t, storeWith(t, liveRecord()), spoolRoot(t), signedRequest(t, req))
	rc.opts.start = reexec{env: []string{
		envDeploymentsDir + "=" + g.deployments,
		"PATH=" + filepath.Dir(bin) + ":" + os.Getenv("PATH"),
	}}.start

	if err := rc.run(t); err != nil {
		t.Fatalf("Run = %v:\n%s", err, rc.stdout)
	}

	var lines []string

	for _, r := range records(t, rc.stdout.Bytes()) {
		if r.Kind == progress.KindLog {
			lines = append(lines, bodyOf[progress.LogBody](t, r).Line)
		}
	}

	// Genesis's two streams are read side by side, so their order between
	// them is not fixed.
	slices.Sort(lines)

	if want := []string{"== lab-a ==", "lab-a info in ocf", "note for lab-a"}; !slices.Equal(lines, want) {
		t.Fatalf("the job logged %q, want %q", lines, want)
	}
}
