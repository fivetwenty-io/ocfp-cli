package progress_test

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
)

func TestQuestionKindsAreTheThree(t *testing.T) {
	t.Parallel()

	want := map[progress.QuestionKind]string{
		progress.QuestionConfirm: "confirm",
		progress.QuestionChoose:  "choose",
		progress.QuestionText:    "text",
	}

	for k, v := range want {
		if string(k) != v {
			t.Errorf("kind %q, want %q", k, v)
		}
	}
}

func TestErrNoAnswerNonInteractiveIsReturned(t *testing.T) {
	t.Parallel()

	p := progress.NewTerminalPrompter(strings.NewReader(""), io.Discard, false)

	_, err := p.Ask(progress.Question{ID: "q", Kind: progress.QuestionText, Text: "x"})
	if !errors.Is(err, progress.ErrNoAnswerNonInteractive) {
		t.Fatalf("err = %v, want ErrNoAnswerNonInteractive", err)
	}
}
