package output

import "fmt"

const (
	// dryRunFinishedStatus is the status a dry run reports when it finishes.
	dryRunFinishedStatus = "Dry run finished without changing anything"

	// dryRunFailedStatus is the status a dry run reports when it fails.
	dryRunFailedStatus = "Dry run failed without changing anything"
)

// statusText returns the text of the summary's status line. A dry run never
// reports success, because it did none of the work. It says that it was a
// dry run and that nothing changed.
func (s Summary) statusText() string {
	switch {
	case s.DryRun && s.Success:
		return dryRunFinishedStatus
	case s.DryRun:
		return dryRunFailedStatus
	case s.Success:
		return "Success"
	default:
		return "Failed"
	}
}

// countLine returns the line that counts the phases the run got through.
// A real run completed them, and a dry run only previewed them.
func (s Summary) countLine() string {
	if s.DryRun {
		return fmt.Sprintf("Phases previewed: %d\n", s.CompletedPhases)
	}

	return fmt.Sprintf("Phases completed: %d\n", s.CompletedPhases)
}

// eventData returns the fields of the summary event that the JSON and YAML
// renderers emit. A real run's fields are the ones they have always
// emitted. A dry run reports no completed phases, counts the phases it
// previewed instead, and adds dry_run, changed, and message fields, so a
// consumer can't mistake it for a run that did the work.
func (s Summary) eventData() map[string]interface{} {
	data := map[string]interface{}{
		eventKeyTotalPhases: s.TotalPhases,
		completedPhasesKey:  s.CompletedPhases,
		"failed_phases":     s.FailedPhases,
		"skipped_phases":    s.SkippedPhases,
		eventKeyDurationMs:  s.Duration.Milliseconds(),
		"success":           s.Success,
	}

	if s.DryRun {
		data[completedPhasesKey] = 0
		data[previewedPhasesKey] = s.CompletedPhases
		data["dry_run"] = true
		data["changed"] = false
		data["message"] = s.statusText()
	}

	if len(s.Errors) > 0 {
		data["errors"] = s.Errors
	}

	return data
}

// withDryRun adjusts a renderer's finalize log fields for a dry run, so
// they agree with the summary event. Where the fields count completed
// phases, a dry run reports none and adds the previewed count. It then
// adds the dry-run marker. A real run's fields are left alone.
func (s Summary) withDryRun(keysAndValues ...interface{}) []interface{} {
	if !s.DryRun {
		return keysAndValues
	}

	fields := make([]interface{}, 0, len(keysAndValues)+4) //nolint:mnd // two more key-value pairs at most

	var key interface{}

	for i, field := range keysAndValues {
		if i%2 == 0 {
			key = field
			fields = append(fields, field)

			continue
		}

		// A dry run reports no completed phases and adds the count it
		// previewed.
		if key == completedPhasesKey {
			fields = append(fields, 0, previewedPhasesKey, s.CompletedPhases)

			continue
		}

		fields = append(fields, field)
	}

	return append(fields, "dry_run", true)
}
