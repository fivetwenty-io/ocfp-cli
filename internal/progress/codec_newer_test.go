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

	if len(recs) != 5 {
		t.Fatalf("got %d records after the header, want 5", len(recs))
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

	if recs[1].Kind != progress.KindEvent || recs[3].Kind != progress.KindEvent {
		t.Fatalf("record kinds = %q and %q, want event for both", recs[1].Kind, recs[3].Kind)
	}

	var started progress.Event
	if err := json.Unmarshal(recs[1].Body, &started); err != nil || started.Kind != progress.EventStepStarted || started.Step != "noop" {
		t.Errorf("first event = %+v (%v), want step_started for noop", started, err)
	}

	var ev progress.Event
	if err := json.Unmarshal(recs[3].Body, &ev); err != nil {
		t.Fatalf("event body: %v", err)
	}

	if ev.Kind != progress.EventStepFinished || ev.Step != "noop" || ev.Outcome != "succeeded" {
		t.Errorf("event = %+v, known fields were not all read", ev)
	}

	var log progress.LogBody
	if err := json.Unmarshal(recs[2].Body, &log); err != nil || log.Stream != "stdout" || log.Line != `{"hostname":"host-fixture","version":"0.0.0-fixture"}` {
		t.Errorf("log = %+v (%v)", log, err)
	}

	var res progress.ResultBody
	if err := json.Unmarshal(recs[4].Body, &res); err != nil || res.ExitCode != 0 {
		t.Errorf("result = %+v (%v)", res, err)
	}

	if recs[4].Kind != progress.KindResult || recs[4].Seq != 5 {
		t.Errorf("last record = kind %q seq %d, want result seq 5", recs[4].Kind, recs[4].Seq)
	}
}
