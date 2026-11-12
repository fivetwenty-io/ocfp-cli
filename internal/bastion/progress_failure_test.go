package bastion

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fivetwenty-io/ocfp-cli/internal/output"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failedPhaseDurations returns the duration_ms of every phase_failed event
// in the JSON event stream.
func failedPhaseDurations(t *testing.T, stream string) []float64 {
	t.Helper()

	var durations []float64

	for _, line := range strings.Split(strings.TrimSpace(stream), "\n") {
		var event map[string]interface{}
		if json.Unmarshal([]byte(line), &event) != nil || event["event"] != "phase_failed" {
			continue
		}

		ms, ok := event["duration_ms"].(float64)
		require.True(t, ok, "phase_failed event has no numeric duration_ms: %s", line)

		durations = append(durations, ms)
	}

	return durations
}

// A failed phase reports how long it ran, measured from when it started.
func TestFailedPhaseReportsItsElapsedTime(t *testing.T) {
	var out bytes.Buffer

	reporter := NewProgressReporter(&out, output.ModeJSON, &ProvisioningProgress{})

	reporter.ReportPhaseStart("config_files", 1, 3)
	time.Sleep(25 * time.Millisecond)
	reporter.ReportError("config_files", errors.New("boom"), 3, 3, 1, 3)

	durations := failedPhaseDurations(t, out.String())
	require.Len(t, durations, 1)
	assert.GreaterOrEqual(t, durations[0], float64(25))
	assert.Less(t, durations[0], float64(time.Minute.Milliseconds()))
}

// A phase that fails before it was reported as started has no elapsed time
// to report, so it reports none rather than the time since the year 1.
func TestFailedPhaseWithoutAStartReportsNoElapsedTime(t *testing.T) {
	var out bytes.Buffer

	reporter := NewProgressReporter(&out, output.ModeJSON, &ProvisioningProgress{})

	reporter.ReportError("config_files", errors.New("boom"), 3, 3, 1, 3)

	durations := failedPhaseDurations(t, out.String())
	require.Len(t, durations, 1)
	assert.Zero(t, durations[0])
}
