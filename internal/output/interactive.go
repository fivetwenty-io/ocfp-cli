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

	// Track written subtask states to prevent repetition (phaseID -> category:item -> state)
	writtenSubtasks map[string]map[string]subtaskState
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
func NewInteractiveRenderer(w io.Writer) *InteractiveRenderer { //nolint:varnamelen
	log := logger.Get()

	// Detect terminal capabilities
	env := DetectEnvironment(w)

	// Create default configuration based on environment
	config := &InteractiveConfig{
		UseColor:       env.SupportsANSI,
		UseUnicode:     env.SupportsANSI,       // Assume Unicode support with ANSI
		ProgressWidth:  30,                     //nolint:mnd
		UpdateInterval: 100 * time.Millisecond, //nolint:mnd
	}

	r := &InteractiveRenderer{ //nolint:varnamelen
		writer:          w,
		log:             log,
		config:          config,
		phaseSubtasks:   make(map[string][]subtaskInfo),
		completedPhases: make([]string, 0),
		writtenSubtasks: make(map[string]map[string]subtaskState),
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
	r.writtenSubtasks[info.ID] = make(map[string]subtaskState)

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

	_, err := r.writer.Write([]byte(line))
	if err != nil {
		return fmt.Errorf("failed to write phase start: %w", err)
	}

	return nil
}

// PhaseProgress reports incremental progress within the current phase.
func (r *InteractiveRenderer) PhaseProgress(progress ProgressInfo) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.currentPhase == nil {
		return ErrNoActivePhase
	}

	phaseID := r.currentPhase.ID

	// Update or add subtask in tracking
	r.updateSubtask(phaseID, progress)

	// Log milestone progress (25%, 50%, 75%, 100%)
	if shouldLogMilestone(progress.Percentage) {
		r.log.Debugw("Phase progress milestone",
			"phase_id", phaseID,
			"percentage", progress.Percentage,
			"category", progress.Category,
			"current", progress.Current,
			"total", progress.Total,
			"item", progress.Item,
		)
	}

	// Write subtask tree (phase status line is written at PhaseStart and PhaseComplete only)
	err := r.writeSubtaskTree(phaseID)
	if err != nil {
		return err
	}

	return nil
}

// PhaseComplete marks successful completion of the current phase.
func (r *InteractiveRenderer) PhaseComplete(info PhaseInfo) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	duration := time.Since(r.phaseStartTime)

	// Format: [N/Total] ✓ Phase completed: name (phase_duration) (cumulative_duration)
	line := fmt.Sprintf("[%02d/%d] %s Phase completed: %s (%s) (%s)\n",
		info.Number, info.Total, statusIcon(StatusCompleted),
		info.Name, formatDuration(duration), formatDuration(info.CumulativeDuration))

	if r.config.UseColor {
		line = Green(line)
	}

	_, err := r.writer.Write([]byte(line))
	if err != nil {
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
		"cumulative_ms", info.CumulativeDuration.Milliseconds(),
	)

	return nil
}

// PhaseFailed marks failure of the current phase with error details.
func (r *InteractiveRenderer) PhaseFailed(info PhaseInfo, err error) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	line := fmt.Sprintf("[%02d/%d] %s Phase failed: %s - %v\n",
		info.Number, info.Total, statusIcon(StatusFailed),
		info.Name, err)

	if r.config.UseColor {
		line = Red(line)
	}

	_, writeErr := r.writer.Write([]byte(line))
	if writeErr != nil {
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
		info.Number, info.Total, statusIcon(StatusSkipped),
		info.Name, reason)

	if r.config.UseColor {
		line = Gray(line)
	}

	_, err := r.writer.Write([]byte(line))
	if err != nil {
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

	err := r.writeLinef("===== Summary =====\n")
	if err != nil {
		return err
	}

	err = r.writeSummaryStatus(summary)
	if err != nil {
		return err
	}

	err = r.writeSummaryErrors(summary.Errors)
	if err != nil {
		return err
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

// writeLinef writes a pre-formatted line to the renderer's writer.
func (r *InteractiveRenderer) writeLinef(line string) error {
	_, err := r.writer.Write([]byte(line))
	if err != nil {
		return fmt.Errorf("failed to write output: %w", err)
	}

	return nil
}

// writeSummaryErrors writes error details to the summary output.
func (r *InteractiveRenderer) writeSummaryErrors(errors []string) error {
	if len(errors) == 0 {
		return nil
	}

	err := r.writeLinef("\nErrors:\n")
	if err != nil {
		return err
	}

	for _, errMsg := range errors {
		line := fmt.Sprintf("  - %s\n", errMsg)
		if r.config.UseColor {
			line = Red(line)
		}

		err = r.writeLinef(line)
		if err != nil {
			return err
		}
	}

	return nil
}

// writeSummaryStatus writes the status, duration, and phase count lines.
func (r *InteractiveRenderer) writeSummaryStatus(summary Summary) error {
	statusText := "Success"
	if !summary.Success {
		statusText = "Failed"
	}

	statusLine := fmt.Sprintf("Status: %s\n", statusText)

	if r.config.UseColor {
		if summary.Success {
			statusLine = Green(statusLine)
		} else {
			statusLine = Red(statusLine)
		}
	}

	err := r.writeLinef(statusLine)
	if err != nil {
		return err
	}

	err = r.writeLinef(fmt.Sprintf("Duration: %s\n", formatDuration(summary.Duration)))
	if err != nil {
		return err
	}

	err = r.writeLinef(fmt.Sprintf("Phases completed: %d\n", summary.CompletedPhases))
	if err != nil {
		return err
	}

	err = r.writeLinef(fmt.Sprintf("Phases failed: %d\n", summary.FailedPhases))
	if err != nil {
		return err
	}

	if summary.SkippedPhases > 0 {
		err = r.writeLinef(fmt.Sprintf("Phases skipped: %d\n", summary.SkippedPhases))
		if err != nil {
			return err
		}
	}

	return nil
}

// Helper methods

// updateSubtask updates or adds a subtask to the phase's subtask list.
func (r *InteractiveRenderer) updateSubtask(phaseID string, progress ProgressInfo) {
	subtasks := r.phaseSubtasks[phaseID]

	// Subtasks always show as running (white), never completed (green)
	// Only the "Phase completed" line should be green
	actualStatus := progress.Status
	if actualStatus == StatusCompleted {
		actualStatus = StatusRunning
	}

	// Find existing subtask by category and item
	found := false

	for i, st := range subtasks {
		if st.category == progress.Category && st.item == progress.Item {
			// Update existing
			subtasks[i].current = progress.Current
			subtasks[i].total = progress.Total
			subtasks[i].status = actualStatus
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
			status:   actualStatus,
		})
	}

	r.phaseSubtasks[phaseID] = subtasks
}

// writeSubtaskTree writes only new or changed subtasks to prevent repetition.
//
//nolint:funlen // subtask tree rendering with change tracking, colors, and tree characters
func (r *InteractiveRenderer) writeSubtaskTree(phaseID string) error {
	if r.currentPhase == nil {
		return nil
	}

	subtasks := r.phaseSubtasks[phaseID]
	if len(subtasks) == 0 {
		return nil
	}

	writtenStates := r.writtenSubtasks[phaseID]

	// Group by category
	categoryMap := make(map[string][]subtaskInfo)
	for _, st := range subtasks {
		categoryMap[st.category] = append(categoryMap[st.category], st)
	}

	// Write only new or changed subtasks
	for category, items := range categoryMap {
		// First pass: collect items that need to be written
		var itemsToWrite []subtaskInfo

		for _, item := range items {
			key := category + ":" + item.item

			// Check if this subtask has changed since last write
			lastWritten, exists := writtenStates[key]
			hasChanged := !exists ||
				lastWritten.current != item.current ||
				lastWritten.total != item.total ||
				lastWritten.status != item.status

			if hasChanged {
				itemsToWrite = append(itemsToWrite, item)
			}
		}

		// Second pass: write items with correct tree characters
		for i, item := range itemsToWrite {
			key := category + ":" + item.item

			// Determine tree character based on items actually being written
			treeChar := "├─"
			if i == len(itemsToWrite)-1 {
				treeChar = "└─"
			}

			// Format: [08/25]   ├─ category: item (current/total)
			line := fmt.Sprintf("[%02d/%d]   %s %s: %s (%d/%d)\n",
				r.currentPhase.Number,
				r.currentPhase.Total,
				treeChar,
				category,
				item.item,
				item.current,
				item.total,
			)

			// Colorize based on status
			if r.config.UseColor {
				line = r.colorizeStatus(line, item.status)
			}

			_, err := r.writer.Write([]byte(line))
			if err != nil {
				return fmt.Errorf("failed to write subtask: %w", err)
			}

			// Update written state
			writtenStates[key] = subtaskState{
				current: item.current,
				total:   item.total,
				status:  item.status,
			}
		}
	}

	return nil
}

// colorizeStatus applies color to a line based on status.
func (r *InteractiveRenderer) colorizeStatus(line string, status Status) string {
	switch status {
	case StatusRunning:
		return line // White (no color) for running subtasks
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
