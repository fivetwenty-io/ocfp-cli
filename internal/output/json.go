package output

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// JSONRenderer implements the Renderer interface for JSON Lines output.
// It emits one JSON object per line for easy streaming and parsing.
// This format is ideal for programmatic consumption and log aggregation systems.
type JSONRenderer struct {
	writer   io.Writer
	log      logger.Logger
	encoder  *json.Encoder
	mu       sync.Mutex
	sequence int
}

// NewJSONRenderer creates a new JSON renderer.
func NewJSONRenderer(w io.Writer) *JSONRenderer {
	log := logger.Get()

	encoder := json.NewEncoder(w)
	// Don't use indentation for JSON Lines format
	encoder.SetIndent("", "")

	r := &JSONRenderer{
		writer:   w,
		log:      log,
		encoder:  encoder,
		sequence: 0,
	}

	log.Infow("JSON renderer created",
		"format", "json_lines",
	)

	return r
}

// PhaseStart signals the beginning of a new operation phase.
func (r *JSONRenderer) PhaseStart(info PhaseInfo) error {
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
func (r *JSONRenderer) PhaseProgress(progress ProgressInfo) error {
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
func (r *JSONRenderer) PhaseComplete(info PhaseInfo) error {
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
func (r *JSONRenderer) PhaseFailed(info PhaseInfo, err error) error {
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
func (r *JSONRenderer) PhaseSkipped(info PhaseInfo, reason string) error {
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
func (r *JSONRenderer) Finalize(summary Summary) error {
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
func (r *JSONRenderer) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.log.Debugw("JSON renderer closed")

	return nil
}

// emitEvent writes a JSON event with automatic sequencing and timestamps.
func (r *JSONRenderer) emitEvent(eventType string, data map[string]interface{}) error {
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

	// Encode and write
	if err := r.encoder.Encode(evt); err != nil {
		return fmt.Errorf("failed to encode JSON event: %w", err)
	}

	return nil
}
