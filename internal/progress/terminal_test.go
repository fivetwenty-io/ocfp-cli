package progress_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
)

func TestTerminalSinkRendersEvents(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	sink := progress.NewTerminalSink(&out)

	for _, ev := range []progress.Event{
		{Kind: progress.EventStepStarted, Step: "render"},
		{Kind: progress.EventStepProgress, Step: "render", Current: 2, Total: 4},
		{Kind: progress.EventWarning, Step: "render", Message: "slow disk"},
		{Kind: progress.EventArtifactProduced, Step: "render", Artifact: &progress.Artifact{Name: "n", Path: "/p"}},
		{Kind: progress.EventStepFinished, Step: "render", Outcome: "ok"},
	} {
		if err := sink.Emit(ev); err != nil {
			t.Fatal(err)
		}
	}

	got := out.String()
	for _, want := range []string{"render", "2/4", "warning: slow disk", "/p", "ok"} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
}

func ask(t *testing.T, input string, q progress.Question) (string, string, error) {
	t.Helper()

	var out bytes.Buffer

	p := progress.NewTerminalPrompter(strings.NewReader(input), &out, true)
	a, err := p.Ask(q)

	return a, out.String(), err
}

func TestTerminalPrompterConfirm(t *testing.T) {
	t.Parallel()

	q := progress.Question{ID: "c", Kind: progress.QuestionConfirm, Text: "Proceed?"}

	got, out, err := ask(t, "y\n", q)
	if err != nil || got != "yes" {
		t.Fatalf("y: got %q, %v", got, err)
	}

	if !strings.Contains(out, "Proceed?") || !strings.Contains(out, "[y/n]") {
		t.Errorf("prompt = %q", out)
	}

	got, _, err = ask(t, "n\n", q)
	if err != nil || got != "no" {
		t.Fatalf("n: got %q, %v", got, err)
	}

	def := "yes"
	q.Default = &def

	got, out, err = ask(t, "\n", q)
	if err != nil || got != "yes" {
		t.Fatalf("default: got %q, %v", got, err)
	}

	if !strings.Contains(out, "[Y/n]") {
		t.Errorf("prompt = %q", out)
	}
}

func TestTerminalPrompterChoose(t *testing.T) {
	t.Parallel()

	q := progress.Question{ID: "ch", Kind: progress.QuestionChoose, Text: "Pick", Choices: []string{"alpha", "beta", "gamma"}}

	got, out, err := ask(t, "2\n", q)
	if err != nil || got != "beta" {
		t.Fatalf("got %q, %v", got, err)
	}

	for _, want := range []string{"Pick", "1) alpha", "2) beta", "3) gamma"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestTerminalPrompterChooseRetriesOnBadInput(t *testing.T) {
	t.Parallel()

	q := progress.Question{ID: "ch", Kind: progress.QuestionChoose, Text: "Pick", Choices: []string{"a", "b"}}

	got, _, err := ask(t, "9\nx\n1\n", q)
	if err != nil || got != "a" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestTerminalPrompterText(t *testing.T) {
	t.Parallel()

	q := progress.Question{ID: "t", Kind: progress.QuestionText, Text: "Name"}

	got, _, err := ask(t, "  ada \n", q)
	if err != nil || got != "ada" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestTerminalPrompterEndOfInputIsAnError(t *testing.T) {
	t.Parallel()

	_, _, err := ask(t, "", progress.Question{ID: "t", Kind: progress.QuestionText, Text: "Name"})
	if err == nil {
		t.Fatal("want an error at end of input")
	}
}

func TestTerminalPrompterNonInteractive(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	p := progress.NewTerminalPrompter(strings.NewReader("never\n"), &out, false)

	def := "x"

	got, err := p.Ask(progress.Question{ID: "t", Kind: progress.QuestionText, Text: "Name", Default: &def})
	if err != nil || got != "x" {
		t.Fatalf("got %q, %v", got, err)
	}

	_, err = p.Ask(progress.Question{ID: "t2", Kind: progress.QuestionText, Text: "Name"})
	if !errors.Is(err, progress.ErrNoAnswerNonInteractive) {
		t.Fatalf("err = %v", err)
	}

	if out.Len() != 0 {
		t.Errorf("wrote %q", out.String())
	}
}

func TestTerminalPrompterSensitiveHidesDefault(t *testing.T) {
	t.Parallel()

	def := "topsecret"

	got, out, err := ask(t, "\n", progress.Question{ID: "p", Kind: progress.QuestionText, Text: "Password", Default: &def, Sensitive: true})
	if err != nil || got != "topsecret" {
		t.Fatalf("got %q, %v", got, err)
	}

	if strings.Contains(out, "topsecret") {
		t.Errorf("prompt leaks the default: %q", out)
	}
}

func TestTerminalPrompterSensitiveAnswerNotEchoed(t *testing.T) {
	t.Parallel()

	got, out, err := ask(t, "hunter2-secret\n", progress.Question{ID: "p", Kind: progress.QuestionText, Text: "Password", Sensitive: true})
	if err != nil || got != "hunter2-secret" {
		t.Fatalf("got %q, %v", got, err)
	}

	if strings.Contains(out, "hunter2-secret") {
		t.Errorf("output holds the answer: %q", out)
	}
}

func TestTerminalPrompterRefusesLongLine(t *testing.T) {
	t.Parallel()

	_, _, err := ask(t, strings.Repeat("a", 64<<10+1)+"\n", progress.Question{ID: "t", Kind: progress.QuestionText, Text: "Name"})
	if !errors.Is(err, progress.ErrAnswerTooLong) {
		t.Fatalf("err = %v, want ErrAnswerTooLong", err)
	}

	got, _, err := ask(t, strings.Repeat("a", 60<<10)+"\n", progress.Question{ID: "t", Kind: progress.QuestionText, Text: "Name"})
	if err != nil || len(got) != 60<<10 {
		t.Fatalf("long but allowed line: len %d, %v", len(got), err)
	}
}
