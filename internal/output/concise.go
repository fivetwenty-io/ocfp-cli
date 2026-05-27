package output

import (
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// ConciseRenderer implements the Renderer interface for CI/CD environments.
// It provides append-only output with no ANSI codes, full subtask trees,
// and grep-friendly formatting suitable for log aggregation systems.
type ConciseRenderer struct {
	writer io.Writer
	log    logger.Logger
	mu     sync.Mutex

	// Track subtasks per phase for tree structure
	phaseSubtasks   map[string][]subtaskInfo
	currentPhase    *PhaseInfo
	phaseStartTime  time.Time
	completedPhases []string

	// Track written subtask states to prevent repetition (phaseID -> category:item -> state)
	writtenSubtasks map[string]map[string]subtaskState
}

// NewConciseRenderer creates a new concise renderer for CI/CD output.
func NewConciseRenderer(w io.Writer) *ConciseRenderer {
	log := logger.Get()

	r := &ConciseRenderer{ //nolint:varnamelen
		writer:          w,
		log:             log,
		phaseSubtasks:   make(map[string][]subtaskInfo),
		completedPhases: make([]string, 0),
		writtenSubtasks: make(map[string]map[string]subtaskState),
	}

	log.Infow("Concise renderer created",
		"output_mode", "concise",
		"ansi_codes", false,
		"append_only", true,
	)

	return r
}

// PhaseStart signals the beginning of a new operation phase.
func (r *ConciseRenderer) PhaseStart(info PhaseInfo) error {
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

	// Write phase start line
	line := fmt.Sprintf("[%02d/%d] Starting phase: %s\n",
		info.Number, info.Total, info.Name)

	_, err := r.writer.Write([]byte(line))
	if err != nil {
		return fmt.Errorf("failed to write phase start: %w", err)
	}

	return nil
}

// PhaseProgress reports incremental progress within the current phase.
func (r *ConciseRenderer) PhaseProgress(progress ProgressInfo) error {
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
func (r *ConciseRenderer) PhaseComplete(info PhaseInfo) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	duration := time.Since(r.phaseStartTime)

	// Format: [N/Total] ✓ Phase completed: name (phase_duration) (cumulative_duration)
	line := fmt.Sprintf("[%02d/%d] %s Phase completed: %s (%s) (%s)\n",
		info.Number, info.Total, statusIcon(StatusCompleted),
		info.Name, formatDuration(duration), formatDuration(info.CumulativeDuration))

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
func (r *ConciseRenderer) PhaseFailed(info PhaseInfo, err error) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	line := fmt.Sprintf("[%02d/%d] %s Phase failed: %s - %v\n",
		info.Number, info.Total, statusIcon(StatusFailed),
		info.Name, err)

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
func (r *ConciseRenderer) PhaseSkipped(info PhaseInfo, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	line := fmt.Sprintf("[%02d/%d] %s Phase skipped: %s (%s)\n",
		info.Number, info.Total, statusIcon(StatusSkipped),
		info.Name, reason)

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
//
//nolint:funlen // summary output with multiple write operations must remain together
func (r *ConciseRenderer) Finalize(summary Summary) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Write separator
	separator := "===== Summary =====\n"

	_, err := r.writer.Write([]byte(separator))
	if err != nil {
		return fmt.Errorf("failed to write separator: %w", err)
	}

	// Status
	status := "Success"
	if !summary.Success {
		status = "Failed"
	}

	line := fmt.Sprintf("Status: %s\n", status)

	_, err = r.writer.Write([]byte(line))
	if err != nil {
		return fmt.Errorf("failed to write status: %w", err)
	}

	// Duration
	line = fmt.Sprintf("Duration: %s\n", formatDuration(summary.Duration))

	_, err = r.writer.Write([]byte(line))
	if err != nil {
		return fmt.Errorf("failed to write duration: %w", err)
	}

	// Phase counts
	line = fmt.Sprintf("Phases completed: %d\n", summary.CompletedPhases)

	_, err = r.writer.Write([]byte(line))
	if err != nil {
		return fmt.Errorf("failed to write completed count: %w", err)
	}

	line = fmt.Sprintf("Phases failed: %d\n", summary.FailedPhases)

	_, err = r.writer.Write([]byte(line))
	if err != nil {
		return fmt.Errorf("failed to write failed count: %w", err)
	}

	if summary.SkippedPhases > 0 {
		line = fmt.Sprintf("Phases skipped: %d\n", summary.SkippedPhases)

		_, err = r.writer.Write([]byte(line))
		if err != nil {
			return fmt.Errorf("failed to write skipped count: %w", err)
		}
	}

	// Include errors if any
	if len(summary.Errors) > 0 {
		errHeader := "\nErrors:\n"

		_, err = r.writer.Write([]byte(errHeader))
		if err != nil {
			return fmt.Errorf("failed to write error header: %w", err)
		}

		for _, errMsg := range summary.Errors {
			line = fmt.Sprintf("  - %s\n", errMsg)

			_, err = r.writer.Write([]byte(line))
			if err != nil {
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
func (r *ConciseRenderer) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.log.Debugw("Concise renderer closed")

	return nil
}

// Helper methods

// updateSubtask updates or adds a subtask to the phase's subtask list.
func (r *ConciseRenderer) updateSubtask(phaseID string, progress ProgressInfo) {
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

// writeSubtaskTree writes only new or changed subtasks to prevent repetition.
//
//nolint:funlen // subtask tree rendering with change tracking and tree characters
func (r *ConciseRenderer) writeSubtaskTree(phaseID string) error {
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

			// Format: [08/25]   ├─ icon category: item (current/total)
			line := fmt.Sprintf("[%02d/%d]   %s %s %s: %s (%d/%d)\n",
				r.currentPhase.Number,
				r.currentPhase.Total,
				treeChar,
				statusIcon(item.status),
				category,
				item.item,
				item.current,
				item.total,
			)

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

