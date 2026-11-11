package progress_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
	"github.com/fivetwenty-io/ocfp-cli/internal/progress/progresstest"
)

func TestDecodeNewerRecordWithoutError(t *testing.T) {
	t.Parallel()

	dec := progress.NewDecoder(bytes.NewReader(progresstest.Fixture("stream-newer-fields-v1.ndjson")))

	h, err := dec.Header()
	if err != nil {
		t.Fatalf("header: %v", err)
	}

	if h.WireContract != 1 || h.Producer != "ocfp" || h.Version != "9.9.9-fixture" || h.JobID != "job-fixture-01" {
		t.Errorf("header = %+v, known fields were not all read", h)
	}

	var recs []progress.Record

	for {
		rec, err := dec.Decode()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			t.Fatalf("decode: %v", err)
		}

		recs = append(recs, rec)
	}

	if len(recs) != 4 {
		t.Fatalf("got %d records after the header, want 4", len(recs))
	}

	if recs[0].Kind != progress.Kind("telemetry") || recs[0].Seq != 1 {
		t.Errorf("record 1 = kind %q seq %d, want telemetry seq 1", recs[0].Kind, recs[0].Seq)
	}

	var tel struct {
		CPU    float64 `json:"cpu"`
		Window int     `json:"window"`
	}
	if err := json.Unmarshal(recs[0].Body, &tel); err != nil || tel.CPU != 0.25 || tel.Window != 5 {
		t.Errorf("telemetry body = %s (%+v, %v), want it intact", recs[0].Body, tel, err)
	}

	if recs[1].Kind != progress.KindEvent {
		t.Fatalf("record 2 kind = %q, want event", recs[1].Kind)
	}

	var ev progress.Event
	if err := json.Unmarshal(recs[1].Body, &ev); err != nil {
		t.Fatalf("event body: %v", err)
	}

	if ev.Kind != progress.EventStepFinished || ev.Step != "noop" || ev.Outcome != "ok" {
		t.Errorf("event = %+v, known fields were not all read", ev)
	}

	var log progress.LogBody
	if err := json.Unmarshal(recs[2].Body, &log); err != nil || log.Stream != "stdout" || log.Line != "noop complete" {
		t.Errorf("log = %+v (%v)", log, err)
	}

	var res progress.ResultBody
	if err := json.Unmarshal(recs[3].Body, &res); err != nil || res.ExitCode != 0 {
		t.Errorf("result = %+v (%v)", res, err)
	}

	if recs[3].Kind != progress.KindResult || recs[3].Seq != 4 {
		t.Errorf("last record = kind %q seq %d, want result seq 4", recs[3].Kind, recs[3].Seq)
	}
}
