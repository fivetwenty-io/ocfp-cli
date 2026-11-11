package jobexec

import (
	"encoding/json"
	"fmt"

	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
	"github.com/fivetwenty-io/ocfp-cli/internal/version"
)

// verbNoop is the only verb this host runs. It does no work, and proves the
// whole path from a signed request to a spooled stream.
const verbNoop = "noop"

// The outcomes the no-op verb's step can finish with.
const (
	outcomeSucceeded = "succeeded"
	outcomeFailed    = "failed"
)

// noopOutput is the one line the no-op verb writes on stdout.
type noopOutput struct {
	Hostname string `json:"hostname"`
	Version  string `json:"version"`
}

// runNoop runs the no-op verb and writes its records with enc: step_started
// for the step noop, one log line, step_finished, and the result. The log
// line is the JSON {"hostname":...,"version":...} on stdout. When the host
// name cannot be read, the line names the failure on stderr instead, the
// step fails, and the exit code is 1. runNoop returns the job's exit code,
// and an error only when a record could not be written.
func runNoop(enc *progress.Encoder, hostname func() (string, error)) (int, error) {
	err := enc.Encode(progress.KindEvent, progress.Event{Kind: progress.EventStepStarted, Step: verbNoop})
	if err != nil {
		return 0, err
	}

	code, outcome := 0, outcomeSucceeded
	logLine := progress.LogBody{Stream: "stdout"}

	name, err := hostname()
	if err != nil {
		code, outcome = 1, outcomeFailed
		logLine = progress.LogBody{Stream: "stderr", Line: "read host name: " + quote(err.Error())}
	} else {
		line, merr := json.Marshal(noopOutput{Hostname: name, Version: version.Get().Version})
		if merr != nil {
			return 0, fmt.Errorf("encode no-op output: %w", merr)
		}

		logLine.Line = string(line)
	}

	err = enc.Encode(progress.KindLog, logLine)
	if err != nil {
		return 0, err
	}

	err = enc.Encode(progress.KindEvent, progress.Event{Kind: progress.EventStepFinished, Step: verbNoop, Outcome: outcome})
	if err != nil {
		return 0, err
	}

	err = enc.Encode(progress.KindResult, progress.ResultBody{ExitCode: code})
	if err != nil {
		return 0, err
	}

	return code, nil
}
