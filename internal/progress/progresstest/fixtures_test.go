package progresstest_test

import (
	"bytes"
	"errors"
	"io"
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
