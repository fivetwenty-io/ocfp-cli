package output

import (
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// InteractiveRenderer implements the Renderer interface for interactive terminal output.
// It uses append-only output with colors - same logic as Concise but with ANSI colors.
type InteractiveRenderer struct {
	writer io.Writer
	log    logger.Logger
	config *InteractiveConfig
	mu     sync.Mutex

	// Track subtasks per phase for tree structure (same as Concise)
	phaseSubtasks   map[string][]subtaskInfo
	currentPhase    *PhaseInfo
	phaseStartTime  time.Time
	completedPhases []string
}

// InteractiveConfig holds configuration for the interactive renderer.
type InteractiveConfig struct {
	// UseColor enables ANSI color codes.
	UseColor bool

	// UseUnicode enables Unicode characters (progress bars, icons).
	UseUnicode bool

	// ProgressWidth is the width of progress bars in characters.
	ProgressWidth int

	// UpdateInterval is the minimum time between progress updates.
	UpdateInterval time.Duration
}

// NewInteractiveRenderer creates a new interactive renderer with terminal capability detection.
func NewInteractiveRenderer(w io.Writer) *InteractiveRenderer {
	log := logger.Get()

	// Detect terminal capabilities
	env := DetectEnvironment(w)

	// Create default configuration based on environment
	config := &InteractiveConfig{
		UseColor:       env.SupportsANSI,
		UseUnicode:     env.SupportsANSI, // Assume Unicode support with ANSI
		ProgressWidth:  30,
		UpdateInterval: 100 * time.Millisecond,
	}

	r := &InteractiveRenderer{
		writer:          w,
		log:             log,
		config:          config,
		phaseSubtasks:   make(map[string][]subtaskInfo),
		completedPhases: make([]string, 0),
	}

	log.Infow("Interactive renderer created",
		"use_color", config.UseColor,
		"use_unicode", config.UseUnicode,
		"progress_width", config.ProgressWidth,
		"update_interval_ms", config.UpdateInterval.Milliseconds(),
	)

	return r
}

// PhaseStart signals the beginning of a new operation phase.
func (r *InteractiveRenderer) PhaseStart(info PhaseInfo) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.currentPhase = &info
	r.phaseStartTime = time.Now()
	r.phaseSubtasks[info.ID] = make([]subtaskInfo, 0)

	r.log.Debugw("Phase started",
		"phase_id", info.ID,
		"phase_name", info.Name,
		"phase_number", info.Number,
		"total_phases", info.Total,
	)

	// Write phase start line (append-only, no cursor movement)
	line := fmt.Sprintf("[%02d/%d] Starting phase: %s\n",
		info.Number, info.Total, info.Name)

	if r.config.UseColor {
		line = Yellow(line)
	}

	if _, err := r.writer.Write([]byte(line)); err != nil {
		return fmt.Errorf("failed to write phase start: %w", err)
	}

	return nil
}

// PhaseProgress reports incremental progress within the current phase.
func (r *InteractiveRenderer) PhaseProgress(progress ProgressInfo) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.currentPhase == nil {
		return fmt.Errorf("no active phase for progress update")
	}

	phaseID := r.currentPhase.ID

	// Update or add subtask in tracking
	r.updateSubtask(phaseID, progress)

	// Log milestone progress (25%, 50%, 75%, 100%)
	if r.shouldLogMilestone(progress.Percentage) {
		r.log.Debugw("Phase progress milestone",
			"phase_id", phaseID,
			"percentage", progress.Percentage,
			"category", progress.Category,
			"current", progress.Current,
			"total", progress.Total,
			"item", progress.Item,
		)
	}

	// Calculate elapsed time
	elapsed := time.Since(r.phaseStartTime)

	// Write progress line with elapsed time
	line := fmt.Sprintf("[%02d/%d] %s %s (%s)\n",
		r.currentPhase.Number,
		r.currentPhase.Total,
		r.statusIcon(progress.Status),
		progress.Category,
		r.formatDuration(elapsed),
	)

	if r.config.UseColor {
		line = r.colorizeStatus(line, progress.Status)
	}

	if _, err := r.writer.Write([]byte(line)); err != nil {
		return fmt.Errorf("failed to write progress: %w", err)
	}

	// Write subtask tree
	if err := r.writeSubtaskTree(phaseID); err != nil {
		return err
	}

	return nil
}

// PhaseComplete marks successful completion of the current phase.
func (r *InteractiveRenderer) PhaseComplete(info PhaseInfo) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	duration := time.Since(r.phaseStartTime)

	line := fmt.Sprintf("[%02d/%d] %s Phase completed: %s (%s)\n",
		info.Number, info.Total, r.statusIcon(StatusCompleted),
		info.Name, r.formatDuration(duration))

	if r.config.UseColor {
		line = Green(line)
	}

	if _, err := r.writer.Write([]byte(line)); err != nil {
		return fmt.Errorf("failed to write phase complete: %w", err)
	}

	// Track completed phase
	r.completedPhases = append(r.completedPhases, info.ID)
	r.currentPhase = nil

	r.log.Infow("Phase completed",
		"phase_id", info.ID,
		"phase_name", info.Name,
		"phase_number", info.Number,
		"duration_ms", duration.Milliseconds(),
	)

	return nil
}

// PhaseFailed marks failure of the current phase with error details.
func (r *InteractiveRenderer) PhaseFailed(info PhaseInfo, err error) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	line := fmt.Sprintf("[%02d/%d] %s Phase failed: %s - %v\n",
		info.Number, info.Total, r.statusIcon(StatusFailed),
		info.Name, err)

	if r.config.UseColor {
		line = Red(line)
	}

	if _, writeErr := r.writer.Write([]byte(line)); writeErr != nil {
		return fmt.Errorf("failed to write phase failure: %w", writeErr)
	}

	r.log.Errorw("Phase failed",
		"phase_id", info.ID,
		"phase_name", info.Name,
		"phase_number", info.Number,
		"error", err.Error(),
	)

	return nil
}

// PhaseSkipped marks the current phase as skipped with reason.
func (r *InteractiveRenderer) PhaseSkipped(info PhaseInfo, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	line := fmt.Sprintf("[%02d/%d] %s Phase skipped: %s (%s)\n",
		info.Number, info.Total, r.statusIcon(StatusSkipped),
		info.Name, reason)

	if r.config.UseColor {
		line = Gray(line)
	}

	if _, err := r.writer.Write([]byte(line)); err != nil {
		return fmt.Errorf("failed to write phase skipped: %w", err)
	}

	r.log.Debugw("Phase skipped",
		"phase_id", info.ID,
		"phase_name", info.Name,
		"phase_number", info.Number,
		"reason", reason,
	)

	return nil
}

// Finalize completes the rendering process with summary information.
func (r *InteractiveRenderer) Finalize(summary Summary) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Write separator
	separator := "===== Summary =====\n"
	if _, err := r.writer.Write([]byte(separator)); err != nil {
		return fmt.Errorf("failed to write separator: %w", err)
	}

	// Status
	status := "Success"
	if !summary.Success {
		status = "Failed"
	}
	line := fmt.Sprintf("Status: %s\n", status)
	if r.config.UseColor {
		if summary.Success {
			line = Green(line)
		} else {
			line = Red(line)
		}
	}
	if _, err := r.writer.Write([]byte(line)); err != nil {
		return fmt.Errorf("failed to write status: %w", err)
	}

	// Duration
	line = fmt.Sprintf("Duration: %s\n", r.formatDuration(summary.Duration))
	if _, err := r.writer.Write([]byte(line)); err != nil {
		return fmt.Errorf("failed to write duration: %w", err)
	}

	// Phase counts
	line = fmt.Sprintf("Phases completed: %d\n", summary.CompletedPhases)
	if _, err := r.writer.Write([]byte(line)); err != nil {
		return fmt.Errorf("failed to write completed count: %w", err)
	}

	line = fmt.Sprintf("Phases failed: %d\n", summary.FailedPhases)
	if _, err := r.writer.Write([]byte(line)); err != nil {
		return fmt.Errorf("failed to write failed count: %w", err)
	}

	if summary.SkippedPhases > 0 {
		line = fmt.Sprintf("Phases skipped: %d\n", summary.SkippedPhases)
		if _, err := r.writer.Write([]byte(line)); err != nil {
			return fmt.Errorf("failed to write skipped count: %w", err)
		}
	}

	// Include errors if any
	if len(summary.Errors) > 0 {
		errHeader := "\nErrors:\n"
		if _, err := r.writer.Write([]byte(errHeader)); err != nil {
			return fmt.Errorf("failed to write error header: %w", err)
		}

		for _, errMsg := range summary.Errors {
			line = fmt.Sprintf("  - %s\n", errMsg)
			if r.config.UseColor {
				line = Red(line)
			}
			if _, err := r.writer.Write([]byte(line)); err != nil {
				return fmt.Errorf("failed to write error: %w", err)
			}
		}
	}

	r.log.Infow("Operation finalized",
		"success", summary.Success,
		"duration_ms", summary.Duration.Milliseconds(),
		"total_phases", summary.TotalPhases,
		"completed_phases", summary.CompletedPhases,
		"failed_phases", summary.FailedPhases,
		"skipped_phases", summary.SkippedPhases,
	)

	return nil
}

// Close releases any resources held by the renderer.
func (r *InteractiveRenderer) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.log.Debugw("Interactive renderer closed")

	return nil
}

// Helper methods

// updateSubtask updates or adds a subtask to the phase's subtask list.
func (r *InteractiveRenderer) updateSubtask(phaseID string, progress ProgressInfo) {
	subtasks := r.phaseSubtasks[phaseID]

	// Find existing subtask by category and item
	found := false
	for i, st := range subtasks {
		if st.category == progress.Category && st.item == progress.Item {
			// Update existing
			subtasks[i].current = progress.Current
			subtasks[i].total = progress.Total
			subtasks[i].status = progress.Status
			found = true
			break
		}
	}

	if !found {
		// Add new subtask
		subtasks = append(subtasks, subtaskInfo{
			category: progress.Category,
			item:     progress.Item,
			current:  progress.Current,
			total:    progress.Total,
			status:   progress.Status,
		})
	}

	r.phaseSubtasks[phaseID] = subtasks
}

// writeSubtaskTree writes the subtask tree with proper indentation and tree characters.
func (r *InteractiveRenderer) writeSubtaskTree(phaseID string) error {
	if r.currentPhase == nil {
		return nil
	}

	subtasks := r.phaseSubtasks[phaseID]
	if len(subtasks) == 0 {
		return nil
	}

	// Group by category
	categoryMap := make(map[string][]subtaskInfo)
	for _, st := range subtasks {
		categoryMap[st.category] = append(categoryMap[st.category], st)
	}

	// Write each category's subtasks
	for category, items := range categoryMap {
		for i, item := range items {
			// Determine tree character
			treeChar := "├─"
			if i == len(items)-1 {
				treeChar = "└─"
			}

			// Format: [08/25]   ├─ category: item (⟳ current/total)
			line := fmt.Sprintf("[%02d/%d]   %s %s: %s (%s %d/%d)\n",
				r.currentPhase.Number,
				r.currentPhase.Total,
				treeChar,
				category,
				item.item,
				r.statusIcon(item.status),
				item.current,
				item.total,
			)

			// Colorize based on status
			if r.config.UseColor {
				line = r.colorizeStatus(line, item.status)
			}

			if _, err := r.writer.Write([]byte(line)); err != nil {
				return fmt.Errorf("failed to write subtask: %w", err)
			}
		}
	}

	return nil
}

// statusIcon returns the Unicode icon for a given status.
func (r *InteractiveRenderer) statusIcon(status Status) string {
	switch status {
	case StatusRunning:
		return "⟳"
	case StatusCompleted:
		return "✓"
	case StatusFailed:
		return "✗"
	case StatusSkipped:
		return "⤷"
	case StatusPending:
		return "⏳"
	default:
		return "?"
	}
}

// colorizeStatus applies color to a line based on status.
func (r *InteractiveRenderer) colorizeStatus(line string, status Status) string {
	switch status {
	case StatusRunning:
		return Yellow(line)
	case StatusCompleted:
		return Green(line)
	case StatusFailed:
		return Red(line)
	case StatusSkipped:
		return Gray(line)
	case StatusPending:
		return Gray(line)
	default:
		return line
	}
}

// shouldLogMilestone determines if this percentage represents a milestone worth logging.
func (r *InteractiveRenderer) shouldLogMilestone(percentage float64) bool {
	// Log at 25%, 50%, 75%, 100%
	milestones := []float64{25.0, 50.0, 75.0, 100.0}
	for _, milestone := range milestones {
		if percentage >= milestone-1.0 && percentage <= milestone+1.0 {
			return true
		}
	}
	return false
}

// formatDuration formats a duration in a human-readable format.
func (r *InteractiveRenderer) formatDuration(d time.Duration) string {
	// Round to nearest second for readability
	d = d.Round(time.Second)

	if d < time.Minute {
		return fmt.Sprintf("%.0fs", d.Seconds())
	}

	minutes := int(d.Minutes())
	seconds := int(d.Seconds()) - (minutes * 60)

	if minutes < 60 {
		return fmt.Sprintf("%dm%02ds", minutes, seconds)
	}

	hours := minutes / 60
	minutes = minutes % 60

	return fmt.Sprintf("%dh%02dm%02ds", hours, minutes, seconds)
}
