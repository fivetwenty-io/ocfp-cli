package progresstest_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
	"github.com/fivetwenty-io/ocfp-cli/internal/progress/progresstest"
)

type fixedClock struct{}

func (fixedClock) Now() time.Time { return time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC) }

type eventBody struct {
	Kind    string `json:"kind"`
	Step    string `json:"step"`
	Outcome string `json:"outcome,omitempty"`
}

func header(t *testing.T, enc *progress.Encoder) {
	t.Helper()

	h := progress.Header{Producer: "ocfp", Version: "0.0.0-fixture", JobID: "job-fixture-01"}
	if err := enc.Header(h); err != nil {
		t.Fatal(err)
	}
}

func must(t *testing.T, err error) {
	t.Helper()

	if err != nil {
		t.Fatal(err)
	}
}

func TestFixturesMatchEncoder(t *testing.T) {
	t.Parallel()

	t.Run("noop", func(t *testing.T) {
		t.Parallel()

		var buf bytes.Buffer

		enc := progress.NewEncoder(&buf, fixedClock{})
		header(t, enc)
		must(t, enc.Encode(progress.KindEvent, eventBody{Kind: "step_started", Step: "noop"}))
		must(t, enc.Encode(progress.KindEvent, eventBody{Kind: "step_finished", Step: "noop", Outcome: "ok"}))
		must(t, enc.Encode(progress.KindLog, progress.LogBody{Stream: "stdout", Line: "noop complete"}))
		must(t, enc.Encode(progress.KindResult, progress.ResultBody{ExitCode: 0}))

		if got, want := buf.Bytes(), progresstest.Fixture("stream-noop-v1.ndjson"); !bytes.Equal(got, want) {
			t.Errorf("noop stream differs from fixture\ngot:\n%s\nwant:\n%s", got, want)
		}
	})

	t.Run("refused", func(t *testing.T) {
		t.Parallel()

		var buf bytes.Buffer

		enc := progress.NewEncoder(&buf, fixedClock{})
		header(t, enc)
		must(t, enc.Encode(progress.KindError, progress.ErrorBody{
			Code:    progress.CodeOwnerRecordMissing,
			Message: "no owner record is present",
		}))
		must(t, enc.Encode(progress.KindResult, progress.ResultBody{ExitCode: 2}))

		if got, want := buf.Bytes(), progresstest.Fixture("stream-refused-v1.ndjson"); !bytes.Equal(got, want) {
			t.Errorf("refused stream differs from fixture\ngot:\n%s\nwant:\n%s", got, want)
		}
	})
}

func TestFixturePanicsOnUnknownName(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Error("Fixture did not panic for an unknown name")
		}
	}()

	progresstest.Fixture("missing.ndjson")
}

func TestFixturesDecode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		count   int
		lastSeq int64
	}{
		{"stream-noop-v1.ndjson", 5, 4},
		{"stream-refused-v1.ndjson", 3, 2},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dec := progress.NewDecoder(bytes.NewReader(progresstest.Fixture(tc.name)))

			h, err := dec.Header()
			if err != nil {
				t.Fatal(err)
			}

			if h.WireContract != 1 {
				t.Errorf("wireContract = %d, want 1", h.WireContract)
			}

			count, last := 1, int64(0)

			for {
				rec, err := dec.Decode()
				if errors.Is(err, io.EOF) {
					break
				}

				if err != nil {
					t.Fatal(err)
				}

				count++
				last = rec.Seq
			}

			if count != tc.count || last != tc.lastSeq {
				t.Errorf("count = %d last seq = %d, want %d and %d", count, last, tc.count, tc.lastSeq)
			}
		})
	}
}

type askNever struct{}

func (askNever) Answer(string) (string, error) { return "", io.EOF }

func questionBody(t *testing.T, q progress.Question) []byte {
	t.Helper()

	var buf bytes.Buffer

	enc := progress.NewEncoder(&buf, fixedClock{})
	header(t, enc)

	// The source has no answer, so Ask returns after it writes the question.
	_, _ = progress.NewNDJSONPrompter(enc, askNever{}, true).Ask(q)

	dec := progress.NewDecoder(&buf)

	rec, err := dec.Decode()
	if err != nil {
		t.Fatal(err)
	}

	if rec.Kind != progress.KindQuestion {
		t.Fatalf("record kind = %q, want question", rec.Kind)
	}

	return rec.Body
}

func TestQuestionFixturesMatchPrompter(t *testing.T) {
	t.Parallel()

	no := "no"
	secretDefault := "made-up-default-value"

	cases := []struct {
		fixture string
		q       progress.Question
	}{
		{"question-confirm-v1.json", progress.Question{
			ID: "confirm-apply", Kind: progress.QuestionConfirm,
			Text: "Apply the change to host-a.example?", Default: &no,
		}},
		{"question-sensitive-v1.json", progress.Question{
			ID: "admin-passphrase", Kind: progress.QuestionText,
			Text: "Enter the admin passphrase for host-a.internal", Default: &secretDefault, Sensitive: true,
		}},
	}

	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			t.Parallel()

			want := progresstest.Fixture(tc.fixture)
			if got := questionBody(t, tc.q); !bytes.Equal(got, bytes.TrimSuffix(want, []byte("\n"))) {
				t.Errorf("question differs from fixture\ngot:\n%s\nwant:\n%s", got, want)
			}

			if !bytes.HasSuffix(want, []byte("\n")) {
				t.Error("fixture does not end in a newline")
			}

			if tc.q.Sensitive && strings.Contains(string(want), secretDefault) {
				t.Error("the sensitive fixture holds the default")
			}
		})
	}

	t.Run("sensitive fixture holds no answer or default", func(t *testing.T) {
		t.Parallel()

		var m map[string]any
		if err := json.Unmarshal(progresstest.Fixture("question-sensitive-v1.json"), &m); err != nil {
			t.Fatal(err)
		}

		if m["default"] != nil || m["sensitive"] != true {
			t.Errorf("default = %v sensitive = %v, want null and true", m["default"], m["sensitive"])
		}

		if _, ok := m["answer"]; ok {
			t.Error("the sensitive fixture holds an answer")
		}
	})
}

// The jobexec package writes these two files. Its types are mirrored here so
// the fixtures are held to the exact field set and spelling it must produce.
type statusShape struct {
	State     string    `json:"state"`
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"startedAt"`
	ExitCode  *int      `json:"exitCode"`
}

type exitShape struct {
	ExitCode   int       `json:"exitCode"`
	FinishedAt time.Time `json:"finishedAt"`
}

func decodeStrict(t *testing.T, b []byte, v any) {
	t.Helper()

	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()

	if err := dec.Decode(v); err != nil {
		t.Fatal(err)
	}
}

func TestStatusAndExitFixturesDecode(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)

	t.Run("status", func(t *testing.T) {
		t.Parallel()

		raw := progresstest.Fixture("status-v1.json")

		var got statusShape

		decodeStrict(t, raw, &got)

		want := statusShape{State: "running", PID: 4242, StartedAt: start}
		if got.State != want.State || got.PID != want.PID || !got.StartedAt.Equal(want.StartedAt) || got.ExitCode != nil {
			t.Errorf("status = %+v, want %+v", got, want)
		}

		out, err := json.Marshal(want)
		if err != nil {
			t.Fatal(err)
		}

		if !bytes.Equal(append(out, '\n'), raw) {
			t.Errorf("status fixture is not what the shape marshals to\ngot:\n%s\nwant:\n%s", out, raw)
		}
	})

	t.Run("exit", func(t *testing.T) {
		t.Parallel()

		raw := progresstest.Fixture("exit-v1.json")

		var got exitShape

		decodeStrict(t, raw, &got)

		want := exitShape{ExitCode: 0, FinishedAt: start.Add(2 * time.Second)}
		if got.ExitCode != want.ExitCode || !got.FinishedAt.Equal(want.FinishedAt) {
			t.Errorf("exit = %+v, want %+v", got, want)
		}

		out, err := json.Marshal(want)
		if err != nil {
			t.Fatal(err)
		}

		if !bytes.Equal(append(out, '\n'), raw) {
			t.Errorf("exit fixture is not what the shape marshals to\ngot:\n%s\nwant:\n%s", out, raw)
		}
	})
}
