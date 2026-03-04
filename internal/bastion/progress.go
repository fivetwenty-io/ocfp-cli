package bastion

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/output"
)

// No constants needed - renderers handle all configuration

// ProgressReporter handles real-time progress reporting.
type ProgressReporter struct {
	renderer       output.Renderer
	progress       *ProvisioningProgress
	log            logger.Logger
	currentNumber  int           // Current phase number for completion messages
	currentTotal   int           // Total phases for completion messages
	cumulativeTime time.Duration // Cumulative time across all completed phases
	phaseStartTime time.Time     // Start time of current phase for accurate duration
}

// SelectOutputMode determines the appropriate output mode based on environment.
// It checks for explicit mode flags first, then falls back to environment detection.
func SelectOutputMode(w io.Writer) output.Mode {
	env := output.DetectEnvironment(w)

	// Check for explicit mode flag from environment
	if modeStr := os.Getenv("OUTPUT_MODE"); modeStr != "" {
		mode, err := output.ParseMode(modeStr)
		if err == nil {
			return mode
		}
	}

	// Use environment default
	return env.DefaultMode()
}

// NewProgressReporter creates a new progress reporter with the specified output mode.
func NewProgressReporter(w io.Writer, mode output.Mode, progress *ProvisioningProgress) *ProgressReporter { //nolint:varnamelen
	log := logger.Get()

	renderer, err := output.NewRenderer(w, mode)
	if err != nil {
		// Fallback to concise mode on error
		log.Warnw("Failed to create renderer, falling back to concise mode",
			"error", err,
			"requested_mode", mode.String(),
		)

		renderer, _ = output.NewRenderer(w, output.ModeConcise)
	}

	return &ProgressReporter{
		renderer: renderer,
		progress: progress,
		log:      log,
	}
}

// NewProgressReporterCompat creates a reporter with auto-detected mode.
//
// Deprecated: Use NewProgressReporter with explicit mode for better control.
func NewProgressReporterCompat(w io.Writer, progress *ProvisioningProgress) *ProgressReporter {
	mode := SelectOutputMode(w)

	return NewProgressReporter(w, mode, progress)
}

// Start begins progress reporting.
//
// Deprecated: Progress updates are now handled by the renderer automatically.
func (pr *ProgressReporter) Start(_ctx context.Context) {
	// No-op: Renderers handle their own update timing
}

// UpdateProgress updates the current progress state.
func (pr *ProgressReporter) UpdateProgress(step string, completed int, total int) {
	pr.progress.CurrentStep = step
	pr.progress.CompletedSteps = completed
	pr.progress.TotalSteps = total
}

// ReportPhaseStart reports the start of a new phase.
func (pr *ProgressReporter) ReportPhaseStart(phase string, index, total int) {
	if pr.renderer == nil {
		return
	}

	pr.UpdateProgress(phase, index, total)

	// Store current phase info for completion message and track start time
	pr.currentNumber = index + 1
	pr.currentTotal = total
	pr.phaseStartTime = time.Now()

	phaseInfo := output.PhaseInfo{
		ID:        phase,
		Name:      phase,
		Number:    index + 1,
		Total:     total,
		StartTime: pr.phaseStartTime,
	}

	err := pr.renderer.PhaseStart(phaseInfo)
	if err != nil {
		pr.log.Warnw("Failed to report phase start",
			"phase", phase,
			"error", err,
		)
	}
}

// ReportPhaseComplete reports completion of a phase.
func (pr *ProgressReporter) ReportPhaseComplete(phase string, duration time.Duration) {
	if pr.renderer == nil {
		return
	}

	// Calculate actual phase duration from tracked start time
	actualDuration := duration
	if !pr.phaseStartTime.IsZero() {
		actualDuration = time.Since(pr.phaseStartTime)
	}

	// Update cumulative time with actual duration
	pr.cumulativeTime += actualDuration

	phaseInfo := output.PhaseInfo{
		ID:                 phase,
		Name:               phase,
		Number:             pr.currentNumber,
		Total:              pr.currentTotal,
		StartTime:          pr.phaseStartTime,
		CumulativeDuration: pr.cumulativeTime,
	}

	err := pr.renderer.PhaseComplete(phaseInfo)
	if err != nil {
		pr.log.Warnw("Failed to report phase complete",
			"phase", phase,
			"error", err,
		)
	}
}

// ReportSubtaskProgress reports subtask progress within a phase.
func (pr *ProgressReporter) ReportSubtaskProgress(phase string, current, total int, label string) {
	if pr.renderer == nil {
		return
	}

	// Clamp values
	if total <= 0 {
		total = 1
	}

	if current < 0 {
		current = 0
	}

	if current > total {
		current = total
	}

	percentage := float64(current) / float64(total) * 100.0 //nolint:mnd

	progressInfo := output.ProgressInfo{
		Category:   phase,
		Current:    current,
		Total:      total,
		Item:       label,
		Status:     output.StatusRunning,
		Percentage: percentage,
	}

	err := pr.renderer.PhaseProgress(progressInfo)
	if err != nil {
		pr.log.Warnw("Failed to report phase progress",
			"phase", phase,
			"error", err,
		)
	}
}

// ReportPhaseSkipped reports that a phase was skipped.
func (pr *ProgressReporter) ReportPhaseSkipped(phase string, reason string) {
	if pr.renderer == nil {
		return
	}

	phaseInfo := output.PhaseInfo{
		ID:   phase,
		Name: phase,
	}

	err := pr.renderer.PhaseSkipped(phaseInfo, reason)
	if err != nil {
		pr.log.Warnw("Failed to report phase skipped",
			"phase", phase,
			"reason", reason,
			"error", err,
		)
	}
}

// ReportError reports an error with context.
func (pr *ProgressReporter) ReportError(phase string, err error, attempt, maxAttempts, number, total int) {
	if pr.renderer == nil {
		return
	}

	if attempt < maxAttempts {
		// Still retrying - just log, don't report as failed yet
		pr.log.Warnw("Phase error, retrying",
			"phase", phase,
			"error", err,
			"attempt", attempt,
			"max_attempts", maxAttempts,
		)
	} else {
		// Final failure - report to renderer
		phaseInfo := output.PhaseInfo{
			ID:     phase,
			Name:   phase,
			Number: number,
			Total:  total,
		}

		renderErr := pr.renderer.PhaseFailed(phaseInfo, err)
		if renderErr != nil {
			pr.log.Errorw("Failed to report phase failure",
				"phase", phase,
				"error", err,
				"render_error", renderErr,
			)
		}
	}
}

// ReportFinalSummary reports the final summary.
func (pr *ProgressReporter) ReportFinalSummary(success bool, duration time.Duration, phases int, errors int) {
	if pr.renderer == nil {
		return
	}

	summary := output.Summary{
		TotalPhases:     phases,
		CompletedPhases: phases - errors,
		FailedPhases:    errors,
		Duration:        duration,
		Success:         success,
	}

	err := pr.renderer.Finalize(summary)
	if err != nil {
		pr.log.Warnw("Failed to report final summary",
			"success", success,
			"error", err,
		)
	}

	// Close renderer to release resources
	err = pr.renderer.Close()
	if err != nil {
		pr.log.Warnw("Failed to close renderer", "error", err)
	}
}

// StatusReporter provides detailed status information.
type StatusReporter struct {
	checkpointManager *CheckpointManager
	log               logger.Logger
}

// NewStatusReporter creates a new status reporter.
func NewStatusReporter(checkpointMgr *CheckpointManager) *StatusReporter {
	return &StatusReporter{
		checkpointManager: checkpointMgr,
		log:               logger.Get(),
	}
}

// GetCurrentStatus returns current bastion initialization status.
func (sr *StatusReporter) GetCurrentStatus() (map[string]interface{}, error) {
	checkpoint, err := sr.checkpointManager.Load()
	if err != nil {
		return nil, fmt.Errorf("failed to load checkpoint: %w", err)
	}

	status := sr.checkpointManager.GetCompletionStatus(checkpoint)

	// Add execution environment information
	status["execution_mode"] = "remote" // Default, would be detected
	status["timestamp"] = time.Now()

	if checkpoint != nil {
		status["failed_phases"] = checkpoint.FailedPhases
		status["completed_phases"] = checkpoint.CompletedPhases
		status["metadata"] = checkpoint.Metadata
	}

	return status, nil
}

// PrintStatus prints formatted status information.
func (sr *StatusReporter) PrintStatus(output io.Writer) error {
	status, err := sr.GetCurrentStatus()
	if err != nil {
		return err
	}

	// Safely extract typed values from status map
	completed, _ := status["completed"].(bool)
	prog, _ := status["progress"].(float64)
	completedSteps, _ := status["completed_steps"].(int)
	totalSteps, _ := status["total_steps"].(int)

	_, _ = output.Write([]byte("Bastion Initialization Status\n"))
	_, _ = output.Write([]byte("============================\n\n"))

	switch {
	case completed:
		_, _ = output.Write([]byte("✅ Status: COMPLETED\n"))
	case completedSteps > 0:
		_, _ = output.Write([]byte("🔄 Status: IN PROGRESS\n"))
	default:
		_, _ = output.Write([]byte("⏳ Status: NOT STARTED\n"))
	}

	if totalSteps > 0 {
		_, _ = fmt.Fprintf(output, "📊 Progress: %.1f%% (%d/%d phases)\n", prog, completedSteps, totalSteps)
	}

	if startTime, ok := status["start_time"].(time.Time); ok && !startTime.IsZero() {
		_, _ = fmt.Fprintf(output, "⏰ Started: %s\n",
			startTime.Format("2006-01-02 15:04:05"))

		if duration, ok := status["duration"].(string); ok {
			_, _ = fmt.Fprintf(output, "⌛ Duration: %s\n", duration)
		}
	}

	if currentStep, ok := status["current_step"].(string); ok && currentStep != "" {
		_, _ = fmt.Fprintf(output, "🔧 Current phase: %s\n", currentStep)
	}

	return nil
}

// PrintDetailedStatus prints detailed status with phase information.
func (sr *StatusReporter) PrintDetailedStatus(output io.Writer) error {
	err := sr.PrintStatus(output)
	if err != nil {
		return err
	}

	checkpoint, err := sr.checkpointManager.Load()
	if err != nil {
		return err
	}

	if checkpoint == nil {
		return nil
	}

	_, _ = output.Write([]byte("\nPhase Details:\n"))
	_, _ = output.Write([]byte("--------------\n"))

	// Show completed phases
	if len(checkpoint.CompletedPhases) > 0 {
		_, _ = output.Write([]byte("\n✅ Completed phases:\n"))
		for phase := range checkpoint.CompletedPhases {
			_, _ = fmt.Fprintf(output, "   • %s\n", phase)
		}
	}

	// Show failed phases
	if len(checkpoint.FailedPhases) > 0 {
		_, _ = output.Write([]byte("\n❌ Failed phases:\n"))
		for phase, errMsg := range checkpoint.FailedPhases {
			_, _ = fmt.Fprintf(output, "   • %s: %s\n", phase, errMsg)
		}
	}

	// Show metadata if available
	if len(checkpoint.Metadata) > 0 {
		_, _ = output.Write([]byte("\n📋 Additional information:\n"))
		for key, value := range checkpoint.Metadata {
			_, _ = fmt.Fprintf(output, "   %s: %v\n", key, value)
		}
	}

	return nil
}
