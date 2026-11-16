package commands

import (
	"bytes"
	"strings"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/jobexec"
	"github.com/fivetwenty-io/ocfp-cli/internal/testenv"
)

// isJobVerb reports whether args, which follow the binary's name, start the
// binary as the verb process of a job, the way job exec starts it.
func isJobVerb(args []string) bool {
	return len(args) == 2 && args[0] == "job" && args[1] == "verb"
}

// runJobVerbHelper is this test binary started by job exec as the verb
// process of a job.
func runJobVerbHelper() int {
	cmd := NewJobCmd()
	cmd.SetArgs([]string{"verb"})

	return ExitCode(cmd.Execute())
}

// job verb is how job exec runs a verb in a process of its own. It is not a
// command for a person to run, so no help lists it.
func TestJobVerbIsHidden(t *testing.T) {
	t.Parallel()

	job := NewJobCmd()

	verb, _, err := job.Find([]string{"verb"})
	if err != nil || verb == job || verb.Name() != "verb" {
		t.Fatalf("job has no verb subcommand: %v", err)
	}

	if !verb.Hidden {
		t.Fatal("job verb is not hidden")
	}

	var out bytes.Buffer

	job.SetOut(&out)
	job.SetErr(&out)
	job.SetArgs([]string{"--help"})

	if err := job.Execute(); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(out.String(), "exec") || strings.Contains(out.String(), "verb") {
		t.Fatalf("job help lists exec but not verb:\n%s", out.String())
	}
}

// A job verb whose stdin ends before the start line runs nothing and exits 1,
// and stderr says why.
func TestJobVerbRefusesAMissingStartLine(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer

	cmd := NewJobCmd()
	cmd.SetIn(strings.NewReader(""))
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"verb"})

	if code := ExitCode(cmd.Execute()); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}

	if stdout.Len() != 0 {
		t.Fatalf("job verb wrote %q to stdout", stdout.String())
	}

	if !strings.Contains(stderr.String(), "start line") {
		t.Fatalf("stderr %q does not name the start line", stderr.String())
	}
}

// testenv refuses to run tests in a process that holds the mark job exec
// gives a verb's process, and names the mark apart, since it cannot import
// jobexec.
func TestTestenvKnowsTheVerbMark(t *testing.T) {
	t.Parallel()

	if jobexec.VerbChildEnv != testenv.VerbChildEnv {
		t.Fatalf("the verb mark is %q in jobexec and %q in testenv", jobexec.VerbChildEnv, testenv.VerbChildEnv)
	}
}
