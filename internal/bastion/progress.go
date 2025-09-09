package bastion

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

const (
	// Progress reporting configuration.
	progressUpdateInterval = 500 * time.Millisecond
	progressBarWidth       = 30
	subtaskBarWidth        = 20

	// Time thresholds.
	elapsedTimeDisplayThreshold = 30 * time.Second
)

// ProgressReporter handles real-time progress reporting.
type ProgressReporter struct {
	output         io.Writer
	progress       *ProvisioningProgress
	log            logger.Logger
	lastUpdate     time.Time
	updateInterval time.Duration
}

// NewProgressReporter creates a new progress reporter.
func NewProgressReporter(output io.Writer, progress *ProvisioningProgress) *ProgressReporter {
	return &ProgressReporter{
		output:         output,
		progress:       progress,
		log:            logger.Get(),
		lastUpdate:     time.Time{},
		updateInterval: progressUpdateInterval,
	}
}

// Start begins progress reporting.
func (pr *ProgressReporter) Start(ctx context.Context) {
	go pr.reportLoop(ctx)
}

// UpdateProgress updates the current progress state.
func (pr *ProgressReporter) UpdateProgress(step string, completed int, total int) {
	pr.progress.CurrentStep = step
	pr.progress.CompletedSteps = completed
	pr.progress.TotalSteps = total

	// Force immediate update for significant progress
	pr.reportProgress()
}

// ReportPhaseStart reports the start of a new phase.
func (pr *ProgressReporter) ReportPhaseStart(phase string, index, total int) {
	if pr.output == nil {
		return
	}

	pr.UpdateProgress(phase, index, total)

	message := fmt.Sprintf("\n[%d/%d] Starting phase: %s\n",
		index+1, total, phase)
	_, _ = pr.output.Write([]byte(message))
}

// ReportPhaseComplete reports completion of a phase.
func (pr *ProgressReporter) ReportPhaseComplete(phase string, duration time.Duration) {
	if pr.output == nil {
		return
	}

	message := fmt.Sprintf("\n✓ Phase completed: %s (%s)\n",
		phase, duration.Round(time.Millisecond))
	_, _ = pr.output.Write([]byte(message))
}

// ReportSubtaskProgress reports subtask progress within a phase.
func (pr *ProgressReporter) ReportSubtaskProgress(phase string, current, total int, label string) {
	if pr.output == nil {
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

	barWidth := subtaskBarWidth

	filled := int(float64(current) / float64(total) * float64(barWidth))
	if filled > barWidth {
		filled = barWidth
	}

	bar := strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)

	msg := fmt.Sprintf("  [%s] %d/%d %s (%s)\n", bar, current, total, label, phase)
	_, _ = pr.output.Write([]byte(msg))
}

// ReportPhaseSkipped reports that a phase was skipped.
func (pr *ProgressReporter) ReportPhaseSkipped(phase string, reason string) {
	if pr.output == nil {
		return
	}

	message := fmt.Sprintf("\n⤷ Phase skipped: %s (%s)\n", phase, reason)
	_, _ = pr.output.Write([]byte(message))
}

// ReportError reports an error with context.
func (pr *ProgressReporter) ReportError(phase string, err error, attempt, maxAttempts int) {
	if pr.output == nil {
		return
	}

	var message string
	if attempt < maxAttempts {
		message = fmt.Sprintf("\n⚠ Phase failed (attempt %d/%d): %s - %s (retrying...)\n",
			attempt, maxAttempts, phase, err.Error())
	} else {
		message = fmt.Sprintf("\n✗ Phase failed: %s - %s\n", phase, err.Error())
	}

	_, _ = pr.output.Write([]byte(message))
}

// ReportFinalSummary reports the final summary.
func (pr *ProgressReporter) ReportFinalSummary(success bool, duration time.Duration, phases int, errors int) {
	if pr.output == nil {
		return
	}

	_, _ = pr.output.Write([]byte("\n"))

	if success {
		message := "🎉 Bastion initialization completed successfully!\n"
		message += fmt.Sprintf("   Duration: %s\n", duration.Round(time.Second))

		message += fmt.Sprintf("   Phases completed: %d\n", phases)
		if errors > 0 {
			message += fmt.Sprintf("   Errors encountered: %d (resolved)\n", errors)
		}

		_, _ = pr.output.Write([]byte(message))
	} else {
		message := "❌ Bastion initialization failed\n"
		message += fmt.Sprintf("   Duration: %s\n", duration.Round(time.Second))
		message += fmt.Sprintf("   Errors: %d\n", errors)
		_, _ = pr.output.Write([]byte(message))
	}

	_, _ = pr.output.Write([]byte("\n"))
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

// reportLoop runs the progress reporting loop.
func (pr *ProgressReporter) reportLoop(ctx context.Context) {
	ticker := time.NewTicker(pr.updateInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if time.Since(pr.lastUpdate) >= pr.updateInterval {
				pr.reportProgress()
			}
		}
	}
}

// reportProgress outputs current progress information.
func (pr *ProgressReporter) reportProgress() {
	if pr.output == nil {
		return
	}

	now := time.Now()
	elapsed := now.Sub(pr.progress.StartTime)

	// Calculate progress percentage
	var progressPercent float64
	if pr.progress.TotalSteps > 0 {
		progressPercent = float64(pr.progress.CompletedSteps) / float64(pr.progress.TotalSteps) * percentageMultiplier
	}

	// Estimate remaining time
	var eta string

	if progressPercent > 0 && progressPercent < percentageMultiplier {
		rate := progressPercent / elapsed.Seconds()
		remainingSeconds := (percentageMultiplier - progressPercent) / rate
		eta = fmt.Sprintf(" (ETA: %s)", time.Duration(remainingSeconds*float64(time.Second)).Round(time.Second))
	}

	// Create progress bar
	progressBar := pr.createProgressBar(progressPercent)

	// Format current step
	currentStep := pr.progress.CurrentStep
	if currentStep == "" {
		currentStep = "initializing"
	}

	// Create status line
	statusLine := fmt.Sprintf("\r[%s] %.1f%% - %s%s",
		progressBar,
		progressPercent,
		currentStep,
		eta)

	// Add timing information
	if elapsed > elapsedTimeDisplayThreshold {
		statusLine += fmt.Sprintf(" [%s]", elapsed.Round(time.Second))
	}

	_, _ = pr.output.Write([]byte(statusLine))
	pr.lastUpdate = now
}

// createProgressBar creates a visual progress bar.
func (pr *ProgressReporter) createProgressBar(percent float64) string {
	barWidth := progressBarWidth
	filled := int(percent / percentageMultiplier * float64(barWidth))

	if filled > barWidth {
		filled = barWidth
	}

	if filled < 0 {
		filled = 0
	}

	bar := strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)

	return bar
}
