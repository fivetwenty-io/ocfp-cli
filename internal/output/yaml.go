package output

import (
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"gopkg.in/yaml.v3"
)

// YAMLRenderer implements the Renderer interface for YAML stream output.
// It emits separate YAML documents with --- separators for easy parsing.
// This format is ideal for human-readable structured output and configuration systems.
type YAMLRenderer struct {
	writer   io.Writer
	log      logger.Logger
	mu       sync.Mutex
	sequence int
}

// NewYAMLRenderer creates a new YAML renderer.
func NewYAMLRenderer(w io.Writer) *YAMLRenderer {
	log := logger.Get()

	r := &YAMLRenderer{ //nolint:varnamelen
		writer:   w,
		log:      log,
		sequence: 0,
	}

	log.Infow("YAML renderer created",
		"format", "yaml_stream",
	)

	return r
}

// PhaseStart signals the beginning of a new operation phase.
func (r *YAMLRenderer) PhaseStart(info PhaseInfo) error {
	r.log.Debugw("Phase started",
		"phase_id", info.ID,
		"phase_name", info.Name,
	)

	return r.emitEvent("phase_start", map[string]interface{}{
		"phase_id":     info.ID,
		"phase_name":   info.Name,
		"phase_number": info.Number,
		"total_phases": info.Total,
	})
}

// PhaseProgress reports incremental progress within the current phase.
func (r *YAMLRenderer) PhaseProgress(progress ProgressInfo) error {
	data := map[string]interface{}{
		"category":   progress.Category,
		"current":    progress.Current,
		"total":      progress.Total,
		"item":       progress.Item,
		"status":     progress.Status.String(),
		"percentage": progress.Percentage,
	}

	// Add optional ETA if available
	if progress.ETA > 0 {
		data["eta_ms"] = progress.ETA.Milliseconds()
	}

	return r.emitEvent("phase_progress", data)
}

// PhaseComplete marks successful completion of the current phase.
func (r *YAMLRenderer) PhaseComplete(info PhaseInfo) error {
	duration := time.Since(info.StartTime)

	r.log.Infow("Phase completed",
		"phase_id", info.ID,
		"duration_ms", duration.Milliseconds(),
	)

	return r.emitEvent("phase_complete", map[string]interface{}{
		"phase_id":    info.ID,
		"duration_ms": duration.Milliseconds(),
	})
}

// PhaseFailed marks failure of the current phase with error details.
func (r *YAMLRenderer) PhaseFailed(info PhaseInfo, err error) error {
	duration := time.Since(info.StartTime)

	r.log.Errorw("Phase failed",
		"phase_id", info.ID,
		"error", err.Error(),
	)

	return r.emitEvent("phase_failed", map[string]interface{}{
		"phase_id":    info.ID,
		"error":       err.Error(),
		"duration_ms": duration.Milliseconds(),
	})
}

// PhaseSkipped marks the current phase as skipped with reason.
func (r *YAMLRenderer) PhaseSkipped(info PhaseInfo, reason string) error {
	r.log.Debugw("Phase skipped",
		"phase_id", info.ID,
		"reason", reason,
	)

	return r.emitEvent("phase_skipped", map[string]interface{}{
		"phase_id": info.ID,
		"reason":   reason,
	})
}

// Finalize completes the rendering process with summary information.
func (r *YAMLRenderer) Finalize(summary Summary) error {
	r.log.Infow("Operation finalized",
		"success", summary.Success,
		"duration_ms", summary.Duration.Milliseconds(),
	)

	data := map[string]interface{}{
		"total_phases":     summary.TotalPhases,
		"completed_phases": summary.CompletedPhases,
		"failed_phases":    summary.FailedPhases,
		"skipped_phases":   summary.SkippedPhases,
		"duration_ms":      summary.Duration.Milliseconds(),
		"success":          summary.Success,
	}

	// Add errors if present
	if len(summary.Errors) > 0 {
		data["errors"] = summary.Errors
	}

	return r.emitEvent("summary", data)
}

// Close releases any resources held by the renderer.
func (r *YAMLRenderer) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.log.Debugw("YAML renderer closed")

	return nil
}

// emitEvent writes a YAML document with automatic sequencing and timestamps.
func (r *YAMLRenderer) emitEvent(eventType string, data map[string]interface{}) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.sequence++

	// Build complete event
	evt := map[string]interface{}{
		"event":     eventType,
		"sequence":  r.sequence,
		"timestamp": time.Now().Format(time.RFC3339),
	}

	// Merge data fields
	for k, v := range data {
		evt[k] = v
	}

	// Write document separator
	_, err := r.writer.Write([]byte("---\n"))
	if err != nil {
		return fmt.Errorf("failed to write YAML separator: %w", err)
	}

	// Marshal and write YAML
	yamlBytes, err := yaml.Marshal(evt)
	if err != nil {
		return fmt.Errorf("failed to marshal YAML event: %w", err)
	}

	_, err = r.writer.Write(yamlBytes)
	if err != nil {
		return fmt.Errorf("failed to write YAML event: %w", err)
	}

	// Add blank line after document for readability
	_, err = r.writer.Write([]byte("\n"))
	if err != nil {
		return fmt.Errorf("failed to write YAML trailing newline: %w", err)
	}

	return nil
}
