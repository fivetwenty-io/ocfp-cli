package output

// Event map key constants shared across JSON and YAML renderers.
// Centralised here so goconst finds no duplicates across the package.
const (
	eventKeyPhaseID    = "phase_id"
	eventKeyTotalPhases = "total_phases"
	eventKeyDurationMs  = "duration_ms"
)
