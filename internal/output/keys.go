package output

// Event map key constants shared across JSON and YAML renderers.
// Centralised here so goconst finds no duplicates across the package.
const (
	eventKeyPhaseID     = "phase_id"
	eventKeyTotalPhases = "total_phases"
	eventKeyDurationMs  = "duration_ms"

	// completedPhasesKey and previewedPhasesKey count a summary's phases,
	// in the summary event and in the finalize log line alike.
	completedPhasesKey = "completed_phases"
	previewedPhasesKey = "previewed_phases"
)
