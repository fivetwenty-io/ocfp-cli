package output

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// renderSummary finalizes one renderer of the given mode with the summary
// and returns what it wrote. The structured renderers get a fixed clock and
// the interactive renderer gets color, so every byte is predictable.
func renderSummary(t *testing.T, mode Mode, summary Summary) string {
	t.Helper()

	var buf bytes.Buffer

	var renderer Renderer

	switch mode {
	case ModeConcise:
		renderer = NewConciseRenderer(&buf)
	case ModeInteractive:
		r := NewInteractiveRenderer(&buf)
		r.config.UseColor = true
		renderer = r
	case ModeJSON:
		r := NewJSONRenderer(&buf)
		r.now = func() time.Time { return fixedTime }
		renderer = r
	case ModeYAML:
		r := NewYAMLRenderer(&buf)
		r.now = func() time.Time { return fixedTime }
		renderer = r
	default:
		t.Fatalf("no renderer for mode %s", mode)
	}

	require.NoError(t, renderer.Finalize(summary))

	return buf.String()
}

var (
	realSuccess = Summary{TotalPhases: 5, CompletedPhases: 5, Duration: 150 * time.Second, Success: true}
	realFailure = Summary{
		TotalPhases: 5, CompletedPhases: 4, FailedPhases: 1, SkippedPhases: 2,
		Duration: 150 * time.Second, Errors: []string{"boom"},
	}
	dryRunSuccess = Summary{TotalPhases: 5, CompletedPhases: 5, Duration: 2 * time.Second, Success: true, DryRun: true}
	dryRunFailure = Summary{
		TotalPhases: 5, CompletedPhases: 4, FailedPhases: 1,
		Duration: 2 * time.Second, Errors: []string{"boom"}, DryRun: true,
	}
)

// A real run's summary stays exactly what every renderer has always
// written, byte for byte.
func TestRealRunSummaryIsUnchanged(t *testing.T) {
	t.Parallel()

	cases := []struct {
		mode    Mode
		summary Summary
		want    string
	}{
		{ModeConcise, realSuccess,
			"===== Summary =====\nStatus: Success\nDuration: 2m30s\nPhases completed: 5\nPhases failed: 0\n"},
		{ModeConcise, realFailure,
			"===== Summary =====\nStatus: Failed\nDuration: 2m30s\nPhases completed: 4\nPhases failed: 1\n" +
				"Phases skipped: 2\n\nErrors:\n  - boom\n"},
		{ModeInteractive, realSuccess,
			"===== Summary =====\n\x1b[32mStatus: Success\n\x1b[0mDuration: 2m30s\nPhases completed: 5\nPhases failed: 0\n"},
		{ModeInteractive, realFailure,
			"===== Summary =====\n\x1b[31mStatus: Failed\n\x1b[0mDuration: 2m30s\nPhases completed: 4\nPhases failed: 1\n" +
				"Phases skipped: 2\n\nErrors:\n\x1b[31m  - boom\n\x1b[0m"},
		{ModeJSON, realSuccess,
			`{"completed_phases":5,"duration_ms":150000,"event":"summary","failed_phases":0,"sequence":1,` +
				`"skipped_phases":0,"success":true,"timestamp":"2026-01-15T10:30:00Z","total_phases":5}` + "\n"},
		{ModeJSON, realFailure,
			`{"completed_phases":4,"duration_ms":150000,"errors":["boom"],"event":"summary","failed_phases":1,` +
				`"sequence":1,"skipped_phases":2,"success":false,"timestamp":"2026-01-15T10:30:00Z","total_phases":5}` + "\n"},
		{ModeYAML, realSuccess,
			"---\ncompleted_phases: 5\nduration_ms: 150000\nevent: summary\nfailed_phases: 0\nsequence: 1\n" +
				"skipped_phases: 0\nsuccess: true\ntimestamp: \"2026-01-15T10:30:00Z\"\ntotal_phases: 5\n\n"},
		{ModeYAML, realFailure,
			"---\ncompleted_phases: 4\nduration_ms: 150000\nerrors:\n- boom\nevent: summary\nfailed_phases: 1\n" +
				"sequence: 1\nskipped_phases: 2\nsuccess: false\ntimestamp: \"2026-01-15T10:30:00Z\"\ntotal_phases: 5\n\n"},
	}

	for _, tc := range cases {
		assert.Equal(t, tc.want, renderSummary(t, tc.mode, tc.summary), "%s success=%t", tc.mode, tc.summary.Success)
	}
}

// A dry run's summary says it was a dry run and that nothing changed, and
// it never claims that a phase was completed.
func TestDryRunSummarySaysNothingChanged(t *testing.T) {
	t.Parallel()

	cases := []struct {
		mode    Mode
		summary Summary
		want    string
	}{
		{ModeConcise, dryRunSuccess,
			"===== Summary =====\nStatus: Dry run finished without changing anything\nDuration: 2s\n" +
				"Phases previewed: 5\nPhases failed: 0\n"},
		{ModeConcise, dryRunFailure,
			"===== Summary =====\nStatus: Dry run failed without changing anything\nDuration: 2s\n" +
				"Phases previewed: 4\nPhases failed: 1\n\nErrors:\n  - boom\n"},
		{ModeInteractive, dryRunSuccess,
			"===== Summary =====\n\x1b[33mStatus: Dry run finished without changing anything\n\x1b[0mDuration: 2s\n" +
				"Phases previewed: 5\nPhases failed: 0\n"},
		{ModeInteractive, dryRunFailure,
			"===== Summary =====\n\x1b[31mStatus: Dry run failed without changing anything\n\x1b[0mDuration: 2s\n" +
				"Phases previewed: 4\nPhases failed: 1\n\nErrors:\n\x1b[31m  - boom\n\x1b[0m"},
		{ModeJSON, dryRunSuccess,
			`{"changed":false,"completed_phases":0,"dry_run":true,"duration_ms":2000,"event":"summary",` +
				`"failed_phases":0,"message":"Dry run finished without changing anything","previewed_phases":5,` +
				`"sequence":1,"skipped_phases":0,"success":true,"timestamp":"2026-01-15T10:30:00Z","total_phases":5}` + "\n"},
		{ModeJSON, dryRunFailure,
			`{"changed":false,"completed_phases":0,"dry_run":true,"duration_ms":2000,"errors":["boom"],` +
				`"event":"summary","failed_phases":1,"message":"Dry run failed without changing anything",` +
				`"previewed_phases":4,"sequence":1,"skipped_phases":0,"success":false,` +
				`"timestamp":"2026-01-15T10:30:00Z","total_phases":5}` + "\n"},
		{ModeYAML, dryRunSuccess,
			"---\nchanged: false\ncompleted_phases: 0\ndry_run: true\nduration_ms: 2000\nevent: summary\n" +
				"failed_phases: 0\nmessage: Dry run finished without changing anything\npreviewed_phases: 5\n" +
				"sequence: 1\nskipped_phases: 0\nsuccess: true\ntimestamp: \"2026-01-15T10:30:00Z\"\ntotal_phases: 5\n\n"},
		{ModeYAML, dryRunFailure,
			"---\nchanged: false\ncompleted_phases: 0\ndry_run: true\nduration_ms: 2000\nerrors:\n- boom\n" +
				"event: summary\nfailed_phases: 1\nmessage: Dry run failed without changing anything\n" +
				"previewed_phases: 4\nsequence: 1\nskipped_phases: 0\nsuccess: false\n" +
				"timestamp: \"2026-01-15T10:30:00Z\"\ntotal_phases: 5\n\n"},
	}

	for _, tc := range cases {
		got := renderSummary(t, tc.mode, tc.summary)
		assert.Equal(t, tc.want, got, "%s success=%t", tc.mode, tc.summary.Success)
		assert.NotContains(t, got, "Success", "%s: a dry run never reports success as the status", tc.mode)
		assert.NotContains(t, got, "Phases completed", "%s: a dry run completes no phase", tc.mode)
	}
}
