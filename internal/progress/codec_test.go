package progress_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
)

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

func newClock() progress.Clock {
	return fixedClock{t: time.Date(2026, 11, 10, 9, 0, 0, 123456789, time.UTC)}
}

func TestEncoderWritesHeaderFirst(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	enc := progress.NewEncoder(&buf, newClock())

	err := enc.Encode(progress.KindLog, progress.LogBody{Stream: "stdout", Line: "x"})
	if !errors.Is(err, progress.ErrHeaderFirst) {
		t.Fatalf("Encode before Header = %v, want ErrHeaderFirst", err)
	}

	if buf.Len() != 0 {
		t.Fatalf("a refused Encode wrote %q", buf.String())
	}

	if err := enc.Header(progress.Header{Producer: "ocfp", Version: "1.2.3"}); err != nil {
		t.Fatal(err)
	}

	first := strings.SplitN(buf.String(), "\n", 2)[0]

	var rec struct {
		Kind string
		Seq  int64
		Body progress.Header
	}

	if err := json.Unmarshal([]byte(first), &rec); err != nil {
		t.Fatalf("first line %q: %v", first, err)
	}

	if rec.Kind != "header" || rec.Seq != 0 || rec.Body.WireContract != 2 {
		t.Errorf("header line = %q", first)
	}

	if !strings.Contains(first, `"wireContract":2`) {
		t.Errorf("header body lacks wireContract: %q", first)
	}
}

func TestEncoderNumbersRecordsFromOne(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	enc := progress.NewEncoder(&buf, newClock())

	if err := enc.Header(progress.Header{Producer: "ocfp", Version: "v"}); err != nil {
		t.Fatal(err)
	}

	for range 3 {
		if err := enc.Encode(progress.KindLog, progress.LogBody{Stream: "stdout", Line: "l"}); err != nil {
			t.Fatal(err)
		}
	}

	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("got %d lines, want 4", len(lines))
	}

	for i, line := range lines[1:] {
		var rec struct {
			Seq  int64  `json:"seq"`
			Time string `json:"time"`
		}

		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatal(err)
		}

		if rec.Seq != int64(i+1) {
			t.Errorf("line %d seq = %d, want %d", i+1, rec.Seq, i+1)
		}

		if rec.Time != "2026-11-10T09:00:00.123456789Z" {
			t.Errorf("line %d time = %q", i+1, rec.Time)
		}
	}
}

func TestEncoderKeepsLinesWhole(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	enc := progress.NewEncoder(&buf, newClock())
	if err := enc.Header(progress.Header{Producer: "ocfp", Version: "v"}); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup

	for range 8 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for range 20 {
				_ = enc.Encode(progress.KindLog, progress.LogBody{Stream: "stdout", Line: "line"})
			}
		}()
	}

	wg.Wait()

	dec := progress.NewDecoder(&buf)
	if _, err := dec.Header(); err != nil {
		t.Fatal(err)
	}

	seen := map[int64]bool{}

	for range 160 {
		rec, err := dec.Decode()
		if err != nil {
			t.Fatal(err)
		}

		seen[rec.Seq] = true
	}

	if len(seen) != 160 {
		t.Errorf("got %d distinct seq values, want 160", len(seen))
	}
}

func TestDecoderRefusesStreamWithoutHeader(t *testing.T) {
	t.Parallel()

	in := `{"kind":"log","seq":1,"time":"2026-11-10T09:00:00Z","body":{"stream":"stdout","line":"x"}}` + "\n"

	if _, err := progress.NewDecoder(strings.NewReader(in)).Header(); !errors.Is(err, progress.ErrNoHeader) {
		t.Errorf("Header = %v, want ErrNoHeader", err)
	}

	if _, err := progress.NewDecoder(strings.NewReader(in)).Decode(); !errors.Is(err, progress.ErrNoHeader) {
		t.Errorf("Decode = %v, want ErrNoHeader", err)
	}

	if _, err := progress.NewDecoder(strings.NewReader("")).Header(); !errors.Is(err, progress.ErrNoHeader) {
		t.Errorf("empty stream Header = %v, want ErrNoHeader", err)
	}
}
