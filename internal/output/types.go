// Package output provides a multi-mode output rendering system for CLI operations.
// It supports Interactive, Concise, JSON, and YAML output modes with environment detection.
package output

import (
	"time"
)

// Renderer defines the interface for output rendering across different modes.
// Implementations handle phase-based progress tracking and final result summarization.
type Renderer interface {
	// PhaseStart signals the beginning of a new operation phase.
	PhaseStart(info PhaseInfo) error

	// PhaseProgress reports incremental progress within the current phase.
	PhaseProgress(progress ProgressInfo) error

	// PhaseComplete marks successful completion of the current phase.
	PhaseComplete(info PhaseInfo) error

	// PhaseFailed marks failure of the current phase with error details.
	PhaseFailed(info PhaseInfo, err error) error

	// PhaseSkipped marks the current phase as skipped with reason.
	PhaseSkipped(info PhaseInfo, reason string) error

	// Finalize completes the rendering process with summary information.
	Finalize(summary Summary) error

	// Close releases any resources held by the renderer.
	Close() error
}

// PhaseInfo contains metadata about an operation phase.
type PhaseInfo struct {
	// ID is a unique identifier for this phase.
	ID string

	// Name is the human-readable phase name.
	Name string

	// Number is the sequential position (1-indexed).
	Number int

	// Total is the total number of phases in the operation.
	Total int

	// StartTime records when this phase began.
	StartTime time.Time
}

// ProgressInfo describes incremental progress within a phase.
type ProgressInfo struct {
	// Category groups related progress items (e.g., "Files", "Servers").
	Category string

	// Current is the number of completed items.
	Current int

	// Total is the total number of items to process.
	Total int

	// Item is the name/description of the current item being processed.
	Item string

	// Status indicates the current state of this progress item.
	Status Status

	// Percentage is the completion percentage (0-100).
	Percentage float64

	// ETA is the estimated time to completion.
	ETA time.Duration
}

// Summary provides overall operation results for final output.
type Summary struct {
	// TotalPhases is the total number of phases in the operation.
	TotalPhases int

	// CompletedPhases is the number of successfully completed phases.
	CompletedPhases int

	// FailedPhases is the number of failed phases.
	FailedPhases int

	// SkippedPhases is the number of skipped phases.
	SkippedPhases int

	// Duration is the total operation time.
	Duration time.Duration

	// Success indicates whether the overall operation succeeded.
	Success bool

	// Errors contains error messages from failed phases.
	Errors []string
}

// Status represents the state of a phase or progress item.
type Status int

const (
	// StatusPending indicates the item has not yet started.
	StatusPending Status = iota

	// StatusRunning indicates the item is currently processing.
	StatusRunning

	// StatusCompleted indicates the item finished successfully.
	StatusCompleted

	// StatusFailed indicates the item encountered an error.
	StatusFailed

	// StatusSkipped indicates the item was intentionally skipped.
	StatusSkipped
)

// String returns the string representation of a Status.
func (s Status) String() string {
	switch s {
	case StatusPending:
		return "pending"
	case StatusRunning:
		return "running"
	case StatusCompleted:
		return "completed"
	case StatusFailed:
		return "failed"
	case StatusSkipped:
		return "skipped"
	default:
		return "unknown"
	}
}

// Mode represents the output rendering mode.
type Mode int

const (
	// ModeInteractive provides rich, animated terminal output with progress indicators.
	ModeInteractive Mode = iota

	// ModeConcise provides minimal, single-line status updates.
	ModeConcise

	// ModeJSON outputs structured JSON for programmatic consumption.
	ModeJSON

	// ModeYAML outputs structured YAML for programmatic consumption.
	ModeYAML
)

// String returns the string representation of a Mode.
func (m Mode) String() string {
	switch m {
	case ModeInteractive:
		return "interactive"
	case ModeConcise:
		return "concise"
	case ModeJSON:
		return "json"
	case ModeYAML:
		return "yaml"
	default:
		return "unknown"
	}
}
