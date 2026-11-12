package output

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// finalizeLogFields finalizes a text renderer of the given mode with the
// summary and returns the fields of its "Operation finalized" log line.
func finalizeLogFields(t *testing.T, mode Mode, summary Summary) map[string]any {
	t.Helper()

	core, logs := observer.New(zapcore.InfoLevel)
	log := zap.New(core).Sugar()

	var buf bytes.Buffer

	var renderer Renderer

	switch mode {
	case ModeConcise:
		r := NewConciseRenderer(&buf)
		r.log = log
		renderer = r
	case ModeInteractive:
		r := NewInteractiveRenderer(&buf)
		r.log = log
		renderer = r
	default:
		t.Fatalf("no text renderer for mode %s", mode)
	}

	require.NoError(t, renderer.Finalize(summary))

	entries := logs.FilterMessage("Operation finalized").All()
	require.Len(t, entries, 1)

	return entries[0].ContextMap()
}

// In a dry run, the finalize log line of the text renderers agrees with
// the summary event. It reports no completed phases and counts the
// previewed ones, next to the dry-run marker.
func TestDryRunFinalizeLogMatchesTheSummaryEvent(t *testing.T) {
	for _, mode := range []Mode{ModeConcise, ModeInteractive} {
		for _, summary := range []Summary{dryRunSuccess, dryRunFailure} {
			fields := finalizeLogFields(t, mode, summary)
			event := summary.eventData()

			assert.EqualValues(t, event["completed_phases"], fields["completed_phases"], mode)
			assert.EqualValues(t, event["previewed_phases"], fields["previewed_phases"], mode)
			assert.EqualValues(t, 0, fields["completed_phases"], mode)
			assert.EqualValues(t, summary.CompletedPhases, fields["previewed_phases"], mode)
			assert.Equal(t, true, fields["dry_run"], mode)
		}
	}
}

// A real run's finalize log line keeps its fields.
func TestRealRunFinalizeLogIsUnchanged(t *testing.T) {
	for _, mode := range []Mode{ModeConcise, ModeInteractive} {
		for _, summary := range []Summary{realSuccess, realFailure} {
			fields := finalizeLogFields(t, mode, summary)

			assert.Equal(t, map[string]any{
				"success":          summary.Success,
				"duration_ms":      summary.Duration.Milliseconds(),
				"total_phases":     int64(summary.TotalPhases),
				"completed_phases": int64(summary.CompletedPhases),
				"failed_phases":    int64(summary.FailedPhases),
				"skipped_phases":   int64(summary.SkippedPhases),
			}, fields, mode)
		}
	}
}
