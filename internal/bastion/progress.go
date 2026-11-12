package bastion

import (
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/fivetwenty-io/ocfp-cli/internal/logger"
	"github.com/fivetwenty-io/ocfp-cli/internal/output"
)

// No constants needed - renderers handle all configuration

// setStep records the phase the run is on and how far it has got.
func (p *ProvisioningProgress) setStep(step string, completed, total int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.CurrentStep = step
	p.CompletedSteps = completed
	p.TotalSteps = total
}

// markCompleted records that a phase finished.
func (p *ProvisioningProgress) markCompleted(phase string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.Checkpoints == nil {
		p.Checkpoints = make(map[string]bool)
	}

	p.Checkpoints[phase] = true
}

// isCompleted reports whether a phase has finished, in this run or in the
// one it resumes.
func (p *ProvisioningProgress) isCompleted(phase string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.Checkpoints[phase]
}

// addError records a phase's failure.
func (p *ProvisioningProgress) addError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.Errors = append(p.Errors, err)
}

// snapshot returns a copy that no worker writes to, with its own map and
// error list, for code that reads the whole progress at once.
func (p *ProvisioningProgress) snapshot() *ProvisioningProgress {
	p.mu.Lock()
	defer p.mu.Unlock()

	return &ProvisioningProgress{
		TotalSteps:     p.TotalSteps,
		CompletedSteps: p.CompletedSteps,
		CurrentStep:    p.CurrentStep,
		StartTime:      p.StartTime,
		Errors:         slices.Clone(p.Errors),
		Checkpoints:    maps.Clone(p.Checkpoints),
	}
}

// phaseTiming is where a started phase sits in the run and when it began.
type phaseTiming struct {
	number int
	total  int
	start  time.Time
}

// ProgressReporter handles real-time progress reporting.
//
// The parallel phase runner's workers share one reporter, and their phases
// overlap. mu guards the phase bookkeeping below it, which keeps each
// started phase's own number and start time, and the renderers hold their
// own locks.
type ProgressReporter struct {
	renderer output.Renderer
	progress *ProvisioningProgress
	log      logger.Logger

	mu             sync.Mutex
	started        map[string]phaseTiming // Phases started and not yet complete, by name
	cumulativeTime time.Duration          // Cumulative time across all completed phases
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
	pr.progress.setStep(step, completed, total)
}

// ReportPhaseStart reports the start of a new phase.
func (pr *ProgressReporter) ReportPhaseStart(phase string, index, total int) {
	if pr.renderer == nil {
		return
	}

	pr.UpdateProgress(phase, index, total)

	// Keep this phase's number and start time for its completion message
	startTime := time.Now()

	pr.mu.Lock()
	if pr.started == nil {
		pr.started = make(map[string]phaseTiming)
	}

	pr.started[phase] = phaseTiming{number: index + 1, total: total, start: startTime}
	pr.mu.Unlock()

	phaseInfo := output.PhaseInfo{
		ID:        phase,
		Name:      phase,
		Number:    index + 1,
		Total:     total,
		StartTime: startTime,
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

	pr.mu.Lock()

	timing, started := pr.started[phase]
	delete(pr.started, phase)

	// Calculate actual phase duration from this phase's own start time
	actualDuration := duration
	if started {
		actualDuration = time.Since(timing.start)
	}

	// Update cumulative time with actual duration
	pr.cumulativeTime += actualDuration

	phaseInfo := output.PhaseInfo{
		ID:                 phase,
		Name:               phase,
		Number:             timing.number,
		Total:              timing.total,
		StartTime:          timing.start,
		CumulativeDuration: pr.cumulativeTime,
	}

	pr.mu.Unlock()

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
		// Final failure - report to renderer, with the start time of
		// the phase if it was reported as started
		pr.mu.Lock()
		timing := pr.started[phase]
		delete(pr.started, phase)
		pr.mu.Unlock()

		phaseInfo := output.PhaseInfo{
			ID:        phase,
			Name:      phase,
			Number:    number,
			Total:     total,
			StartTime: timing.start,
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
	pr.finalize(finalSummary(success, duration, phases, errors))
}

// ReportDryRunSummary reports the final summary of a dry run that
// finished. The renderers say that it was a dry run and that nothing
// changed. Of the total steps, it counts the ones the run previewed as
// previewed rather than completed, and the ones a resumed run skipped as
// skipped.
func (pr *ProgressReporter) ReportDryRunSummary(duration time.Duration, total, previewed, skipped int) {
	pr.finalize(output.Summary{
		TotalPhases:     total,
		CompletedPhases: previewed,
		FailedPhases:    0,
		SkippedPhases:   skipped,
		Duration:        duration,
		Success:         true,
		DryRun:          true,
	})
}

// ReportDryRunFailure reports the final summary of a dry run that stopped
// at a step the real run would fail at. Of the total steps, it counts the
// ones the run previewed before that step, the ones a resumed run skipped,
// and the one it stopped at as failed.
func (pr *ProgressReporter) ReportDryRunFailure(duration time.Duration, total, previewed, skipped int) {
	pr.finalize(output.Summary{
		TotalPhases:     total,
		CompletedPhases: previewed,
		FailedPhases:    1,
		SkippedPhases:   skipped,
		Duration:        duration,
		Success:         false,
		DryRun:          true,
	})
}

// finalSummary builds the summary of a run with the given phase and error
// counts.
func finalSummary(success bool, duration time.Duration, phases int, errors int) output.Summary {
	return output.Summary{
		TotalPhases:     phases,
		CompletedPhases: phases - errors,
		FailedPhases:    errors,
		Duration:        duration,
		Success:         success,
	}
}

// finalize renders the summary and closes the renderer.
func (pr *ProgressReporter) finalize(summary output.Summary) {
	if pr.renderer == nil {
		return
	}

	err := pr.renderer.Finalize(summary)
	if err != nil {
		pr.log.Warnw("Failed to report final summary",
			"success", summary.Success,
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
