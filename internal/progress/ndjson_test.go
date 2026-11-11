package progress_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
)

type mapAnswers struct {
	answers map[string]string
	asked   []string
}

func (m *mapAnswers) Answer(id string) (string, error) {
	m.asked = append(m.asked, id)

	a, ok := m.answers[id]
	if !ok {
		return "", io.EOF
	}

	return a, nil
}

func newEncoder(t *testing.T) (*progress.Encoder, *bytes.Buffer) {
	t.Helper()

	var buf bytes.Buffer

	enc := progress.NewEncoder(&buf, newClock())
	if err := enc.Header(progress.Header{Producer: "ocfp", Version: "test"}); err != nil {
		t.Fatal(err)
	}

	return enc, &buf
}

func records(t *testing.T, buf *bytes.Buffer) []progress.Record {
	t.Helper()

	dec := progress.NewDecoder(bytes.NewReader(buf.Bytes()))

	var out []progress.Record

	for {
		rec, err := dec.Decode()
		if errors.Is(err, io.EOF) {
			return out
		}

		if err != nil {
			t.Fatal(err)
		}

		out = append(out, rec)
	}
}

func TestNDJSONSinkWritesEventRecords(t *testing.T) {
	t.Parallel()

	enc, buf := newEncoder(t)
	sink := progress.NewNDJSONSink(enc)

	events := []progress.Event{
		{Kind: progress.EventStepStarted, Step: "render"},
		{Kind: progress.EventStepFinished, Step: "render", Outcome: "ok"},
	}
	for _, ev := range events {
		if err := sink.Emit(ev); err != nil {
			t.Fatal(err)
		}
	}

	recs := records(t, buf)
	if len(recs) != 2 {
		t.Fatalf("got %d records, want 2", len(recs))
	}

	for i, rec := range recs {
		if rec.Kind != progress.KindEvent {
			t.Errorf("record %d kind %q, want event", i, rec.Kind)
		}

		var got progress.Event
		if err := json.Unmarshal(rec.Body, &got); err != nil {
			t.Fatal(err)
		}

		if got != events[i] {
			t.Errorf("record %d body %+v, want %+v", i, got, events[i])
		}
	}
}

func TestNDJSONPrompterAsksThenWaits(t *testing.T) {
	t.Parallel()

	enc, buf := newEncoder(t)
	src := &mapAnswers{answers: map[string]string{"q1": "b"}}
	p := progress.NewNDJSONPrompter(enc, src, true)

	def := "a"
	q := progress.Question{
		ID: "q1", Kind: progress.QuestionChoose, Text: "Pick one",
		Choices: []string{"a", "b"}, Default: &def,
	}

	got, err := p.Ask(q)
	if err != nil {
		t.Fatal(err)
	}

	if got != "b" {
		t.Fatalf("answer %q, want b", got)
	}

	recs := records(t, buf)
	if len(recs) != 1 || recs[0].Kind != progress.KindQuestion {
		t.Fatalf("records = %+v, want one question", recs)
	}

	var body map[string]any
	if err := json.Unmarshal(recs[0].Body, &body); err != nil {
		t.Fatal(err)
	}

	for _, key := range []string{"id", "kind", "text", "choices", "default", "sensitive"} {
		if _, ok := body[key]; !ok {
			t.Errorf("question body lacks %q: %v", key, body)
		}
	}

	if body["id"] != "q1" || body["kind"] != "choose" || body["default"] != "a" {
		t.Errorf("question body = %v", body)
	}

	if len(src.asked) != 1 || src.asked[0] != "q1" {
		t.Errorf("answer source asked %v, want [q1]", src.asked)
	}
}

func TestNonInteractiveUsesDefault(t *testing.T) {
	t.Parallel()

	enc, buf := newEncoder(t)
	src := &mapAnswers{}
	p := progress.NewNDJSONPrompter(enc, src, false)

	def := "yes"

	got, err := p.Ask(progress.Question{ID: "q", Kind: progress.QuestionConfirm, Text: "ok?", Default: &def})
	if err != nil {
		t.Fatal(err)
	}

	if got != "yes" {
		t.Fatalf("answer %q, want yes", got)
	}

	if len(src.asked) != 0 {
		t.Errorf("answer source was called: %v", src.asked)
	}

	if n := len(records(t, buf)); n != 0 {
		t.Errorf("wrote %d records, want 0", n)
	}
}

func TestNonInteractiveWithoutDefaultErrors(t *testing.T) {
	t.Parallel()

	enc, _ := newEncoder(t)
	src := &mapAnswers{}
	p := progress.NewNDJSONPrompter(enc, src, false)

	_, err := p.Ask(progress.Question{ID: "q", Kind: progress.QuestionText, Text: "name?"})
	if !errors.Is(err, progress.ErrNoAnswerNonInteractive) {
		t.Fatalf("err = %v, want ErrNoAnswerNonInteractive", err)
	}

	if len(src.asked) != 0 {
		t.Errorf("answer source was called: %v", src.asked)
	}
}

func TestSensitiveAnswerNeverWritten(t *testing.T) {
	t.Parallel()

	enc, buf := newEncoder(t)
	src := &mapAnswers{answers: map[string]string{"pw": "hunter2-secret"}}
	p := progress.NewNDJSONPrompter(enc, src, true)

	def := "default-secret"

	got, err := p.Ask(progress.Question{ID: "pw", Kind: progress.QuestionText, Text: "password?", Default: &def, Sensitive: true})
	if err != nil {
		t.Fatal(err)
	}

	if got != "hunter2-secret" {
		t.Fatalf("answer %q", got)
	}

	recs := records(t, buf)
	if len(recs) != 1 || recs[0].Kind != progress.KindQuestion {
		t.Fatalf("records = %+v, want only the question", recs)
	}

	out := buf.String()
	if strings.Contains(out, "hunter2-secret") || strings.Contains(out, "default-secret") {
		t.Errorf("output leaks a secret: %s", out)
	}
}

func TestNDJSONPrompterPropagatesAnswerError(t *testing.T) {
	t.Parallel()

	enc, _ := newEncoder(t)
	p := progress.NewNDJSONPrompter(enc, &mapAnswers{}, true)

	if _, err := p.Ask(progress.Question{ID: "missing", Kind: progress.QuestionText, Text: "x"}); !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v, want io.EOF", err)
	}
}
