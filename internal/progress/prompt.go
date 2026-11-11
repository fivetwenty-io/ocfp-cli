package progress

import (
	"errors"
	"fmt"
)

// ErrNoAnswerNonInteractive is returned when a run is not interactive and a
// question has no default, so nothing could answer it.
var ErrNoAnswerNonInteractive = errors.New("progress: question has no default and the run is not interactive")

// QuestionKind names the shape of the answer a question expects.
type QuestionKind string

// The question kinds.
const (
	QuestionConfirm QuestionKind = "confirm"
	QuestionChoose  QuestionKind = "choose"
	QuestionText    QuestionKind = "text"
)

// Question is one thing a step needs answered. A confirm answer is "yes" or
// "no", and a choose answer is one of Choices. Default is nil when the
// question has none.
type Question struct {
	ID        string       `json:"id"`
	Kind      QuestionKind `json:"kind"`
	Text      string       `json:"text"`
	Choices   []string     `json:"choices"`
	Default   *string      `json:"default"`
	Sensitive bool         `json:"sensitive"`
}

// Prompter asks a question and returns the answer.
type Prompter interface {
	Ask(q Question) (string, error)
}

// AnswerSource supplies the answer for a question id, blocking until one is
// available.
type AnswerSource interface {
	Answer(id string) (string, error)
}

// nonInteractive returns the default of q, or ErrNoAnswerNonInteractive when
// it has none.
func nonInteractive(q Question) (string, error) {
	if q.Default == nil {
		return "", fmt.Errorf("question %q: %w", q.ID, ErrNoAnswerNonInteractive)
	}

	return *q.Default, nil
}
