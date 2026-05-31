package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJSONRenderer_PhaseStart(t *testing.T) {
	var buf bytes.Buffer
	r := NewJSONRenderer(&buf)

	info := PhaseInfo{
		ID:        "test_phase",
		Name:      "Test Phase",
		Number:    1,
		Total:     5,
		StartTime: fixedTime,
	}

	err := r.PhaseStart(info)
	require.NoError(t, err)

	// Parse JSON output
	var event map[string]interface{}
	decoder := json.NewDecoder(&buf)
	err = decoder.Decode(&event)
	require.NoError(t, err)

	// Verify event structure
	assert.Equal(t, "phase_start", event["event"])
	assert.Equal(t, float64(1), event["sequence"]) // JSON numbers are float64
	assert.NotEmpty(t, event["timestamp"])
	assert.Equal(t, "test_phase", event["phase_id"])
	assert.Equal(t, "Test Phase", event["phase_name"])
	assert.Equal(t, float64(1), event["phase_number"])
	assert.Equal(t, float64(5), event["total_phases"])

	// Verify timestamp is ISO8601
	timestamp, ok := event["timestamp"].(string)
	require.True(t, ok)
	_, err = time.Parse(time.RFC3339, timestamp)
	assert.NoError(t, err, "timestamp should be valid ISO8601")
}

func TestJSONRenderer_PhaseProgress(t *testing.T) {
	var buf bytes.Buffer
	r := NewJSONRenderer(&buf)

	progress := ProgressInfo{
		Category:   "files",
		Current:    3,
		Total:      10,
		Item:       "config.yaml",
		Status:     StatusRunning,
		Percentage: 30.0,
		ETA:        5 * time.Second,
	}

	err := r.PhaseProgress(progress)
	require.NoError(t, err)

	// Parse JSON output
	var event map[string]interface{}
	decoder := json.NewDecoder(&buf)
	err = decoder.Decode(&event)
	require.NoError(t, err)

	// Verify event structure
	assert.Equal(t, "phase_progress", event["event"])
	assert.Equal(t, float64(1), event["sequence"])
	assert.Equal(t, "files", event["category"])
	assert.Equal(t, float64(3), event["current"])
	assert.Equal(t, float64(10), event["total"])
	assert.Equal(t, "config.yaml", event["item"])
	assert.Equal(t, "running", event["status"])
	assert.Equal(t, 30.0, event["percentage"])
	assert.Equal(t, float64(5000), event["eta_ms"]) // 5 seconds in milliseconds
}

func TestJSONRenderer_PhaseProgressWithoutETA(t *testing.T) {
	var buf bytes.Buffer
	r := NewJSONRenderer(&buf)

	progress := ProgressInfo{
		Category:   "servers",
		Current:    1,
		Total:      3,
		Item:       "web-01",
		Status:     StatusRunning,
		Percentage: 33.3,
		ETA:        0, // No ETA
	}

	err := r.PhaseProgress(progress)
	require.NoError(t, err)

	// Parse JSON output
	var event map[string]interface{}
	decoder := json.NewDecoder(&buf)
	err = decoder.Decode(&event)
	require.NoError(t, err)

	// Verify ETA is not present when zero
	_, hasETA := event["eta_ms"]
	assert.False(t, hasETA, "ETA should not be present when zero")
}

func TestJSONRenderer_PhaseComplete(t *testing.T) {
	var buf bytes.Buffer
	r := NewJSONRenderer(&buf)

	// Use a fixed "now" so duration is exactly 2000ms regardless of scheduler jitter.
	fixedNow := time.Date(2024, 1, 1, 0, 0, 2, 0, time.UTC)
	r.now = func() time.Time { return fixedNow }

	startTime := fixedNow.Add(-2 * time.Second)
	info := PhaseInfo{
		ID:        "test_phase",
		Name:      "Test Phase",
		Number:    1,
		Total:     5,
		StartTime: startTime,
	}

	err := r.PhaseComplete(info)
	require.NoError(t, err)

	// Parse JSON output
	var event map[string]interface{}
	decoder := json.NewDecoder(&buf)
	err = decoder.Decode(&event)
	require.NoError(t, err)

	// Verify event structure
	assert.Equal(t, "phase_complete", event["event"])
	assert.Equal(t, "test_phase", event["phase_id"])

	// Duration is exactly 2000ms — deterministic via fixed clock.
	durationMS, ok := event["duration_ms"].(float64)
	require.True(t, ok)
	assert.Equal(t, float64(2000), durationMS)
}

func TestJSONRenderer_PhaseFailed(t *testing.T) {
	var buf bytes.Buffer
	r := NewJSONRenderer(&buf)

	// Fix clock so duration is exactly 500ms — no scheduler jitter.
	fixedNow := fixedTime
	r.now = func() time.Time { return fixedNow }

	startTime := fixedNow.Add(-500 * time.Millisecond)
	info := PhaseInfo{
		ID:        "test_phase",
		Name:      "Test Phase",
		Number:    2,
		Total:     5,
		StartTime: startTime,
	}

	testErr := assert.AnError

	err := r.PhaseFailed(info, testErr)
	require.NoError(t, err)

	// Parse JSON output
	var event map[string]interface{}
	decoder := json.NewDecoder(&buf)
	err = decoder.Decode(&event)
	require.NoError(t, err)

	// Verify event structure
	assert.Equal(t, "phase_failed", event["event"])
	assert.Equal(t, "test_phase", event["phase_id"])
	assert.Contains(t, event["error"], "assert.AnError")

	// Duration is exactly 500ms — deterministic via fixed clock.
	durationMS, ok := event["duration_ms"].(float64)
	require.True(t, ok)
	assert.Equal(t, float64(500), durationMS)
}

func TestJSONRenderer_PhaseSkipped(t *testing.T) {
	var buf bytes.Buffer
	r := NewJSONRenderer(&buf)

	info := PhaseInfo{
		ID:        "test_phase",
		Name:      "Test Phase",
		Number:    3,
		Total:     5,
		StartTime: fixedTime,
	}

	err := r.PhaseSkipped(info, "not applicable")
	require.NoError(t, err)

	// Parse JSON output
	var event map[string]interface{}
	decoder := json.NewDecoder(&buf)
	err = decoder.Decode(&event)
	require.NoError(t, err)

	// Verify event structure
	assert.Equal(t, "phase_skipped", event["event"])
	assert.Equal(t, "test_phase", event["phase_id"])
	assert.Equal(t, "not applicable", event["reason"])
}

func TestJSONRenderer_Finalize_Success(t *testing.T) {
	var buf bytes.Buffer
	r := NewJSONRenderer(&buf)

	summary := Summary{
		TotalPhases:     5,
		CompletedPhases: 5,
		FailedPhases:    0,
		SkippedPhases:   0,
		Duration:        10 * time.Second,
		Success:         true,
		Errors:          nil,
	}

	err := r.Finalize(summary)
	require.NoError(t, err)

	// Parse JSON output
	var event map[string]interface{}
	decoder := json.NewDecoder(&buf)
	err = decoder.Decode(&event)
	require.NoError(t, err)

	// Verify event structure
	assert.Equal(t, "summary", event["event"])
	assert.Equal(t, float64(5), event["total_phases"])
	assert.Equal(t, float64(5), event["completed_phases"])
	assert.Equal(t, float64(0), event["failed_phases"])
	assert.Equal(t, float64(0), event["skipped_phases"])
	assert.Equal(t, float64(10000), event["duration_ms"])
	assert.True(t, event["success"].(bool))

	// Verify errors field is not present when empty
	_, hasErrors := event["errors"]
	assert.False(t, hasErrors, "errors field should not be present when empty")
}

func TestJSONRenderer_Finalize_WithErrors(t *testing.T) {
	var buf bytes.Buffer
	r := NewJSONRenderer(&buf)

	summary := Summary{
		TotalPhases:     5,
		CompletedPhases: 3,
		FailedPhases:    2,
		SkippedPhases:   0,
		Duration:        8 * time.Second,
		Success:         false,
		Errors:          []string{"error 1", "error 2"},
	}

	err := r.Finalize(summary)
	require.NoError(t, err)

	// Parse JSON output
	var event map[string]interface{}
	decoder := json.NewDecoder(&buf)
	err = decoder.Decode(&event)
	require.NoError(t, err)

	// Verify errors are included
	errors, ok := event["errors"].([]interface{})
	require.True(t, ok)
	assert.Len(t, errors, 2)
	assert.Equal(t, "error 1", errors[0])
	assert.Equal(t, "error 2", errors[1])
	assert.False(t, event["success"].(bool))
}

func TestJSONRenderer_SequenceMonotonicity(t *testing.T) {
	var buf bytes.Buffer
	r := NewJSONRenderer(&buf)

	// Emit multiple events
	info := PhaseInfo{
		ID:        "phase1",
		Name:      "Phase 1",
		Number:    1,
		Total:     3,
		StartTime: fixedTime,
	}

	err := r.PhaseStart(info)
	require.NoError(t, err)

	progress := ProgressInfo{
		Category:   "items",
		Current:    1,
		Total:      2,
		Status:     StatusRunning,
		Percentage: 50.0,
	}

	err = r.PhaseProgress(progress)
	require.NoError(t, err)

	err = r.PhaseComplete(info)
	require.NoError(t, err)

	// Parse all events and verify sequence numbers
	decoder := json.NewDecoder(&buf)
	sequences := []int{}

	for {
		var event map[string]interface{}
		if err := decoder.Decode(&event); err != nil {
			break
		}
		seq := int(event["sequence"].(float64))
		sequences = append(sequences, seq)
	}

	assert.Len(t, sequences, 3)
	assert.Equal(t, 1, sequences[0])
	assert.Equal(t, 2, sequences[1])
	assert.Equal(t, 3, sequences[2])
}

func TestJSONRenderer_JSONLinesFormat(t *testing.T) {
	var buf bytes.Buffer
	r := NewJSONRenderer(&buf)

	// Emit multiple events
	info1 := PhaseInfo{ID: "p1", Name: "Phase 1", Number: 1, Total: 2, StartTime: fixedTime}
	info2 := PhaseInfo{ID: "p2", Name: "Phase 2", Number: 2, Total: 2, StartTime: fixedTime}

	err := r.PhaseStart(info1)
	require.NoError(t, err)

	err = r.PhaseStart(info2)
	require.NoError(t, err)

	// Verify output is JSON Lines (one object per line)
	output := buf.String()
	lines := strings.Split(strings.TrimSpace(output), "\n")

	assert.Len(t, lines, 2, "should have exactly 2 lines")

	// Each line should be valid JSON
	for i, line := range lines {
		var event map[string]interface{}
		err := json.Unmarshal([]byte(line), &event)
		assert.NoError(t, err, "line %d should be valid JSON", i+1)
		assert.Equal(t, "phase_start", event["event"])
	}
}

func TestJSONRenderer_ThreadSafety(t *testing.T) {
	var buf bytes.Buffer
	r := NewJSONRenderer(&buf)

	// Concurrent writes
	var wg sync.WaitGroup
	numGoroutines := 10

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()

			info := PhaseInfo{
				ID:        "phase",
				Name:      "Test Phase",
				Number:    n,
				Total:     numGoroutines,
				StartTime: fixedTime,
			}

			err := r.PhaseStart(info)
			assert.NoError(t, err)
		}(i)
	}

	wg.Wait()

	// Parse all events
	decoder := json.NewDecoder(&buf)
	eventCount := 0

	for {
		var event map[string]interface{}
		if err := decoder.Decode(&event); err != nil {
			break
		}
		eventCount++
	}

	assert.Equal(t, numGoroutines, eventCount, "all events should be written")
}

func TestJSONRenderer_Close(t *testing.T) {
	var buf bytes.Buffer
	r := NewJSONRenderer(&buf)

	err := r.Close()
	assert.NoError(t, err)

	// Verify no output from Close
	assert.Empty(t, buf.String())
}

func TestJSONRenderer_AllFields(t *testing.T) {
	var buf bytes.Buffer
	r := NewJSONRenderer(&buf)

	info := PhaseInfo{
		ID:        "comprehensive_test",
		Name:      "Comprehensive Test",
		Number:    1,
		Total:     1,
		StartTime: fixedTime,
	}

	err := r.PhaseStart(info)
	require.NoError(t, err)

	// Parse and verify all required fields are present
	var event map[string]interface{}
	decoder := json.NewDecoder(&buf)
	err = decoder.Decode(&event)
	require.NoError(t, err)

	// Required fields for all events
	requiredFields := []string{"event", "sequence", "timestamp"}
	for _, field := range requiredFields {
		assert.Contains(t, event, field, "event should contain %s", field)
	}

	// Event-specific fields
	eventFields := []string{"phase_id", "phase_name", "phase_number", "total_phases"}
	for _, field := range eventFields {
		assert.Contains(t, event, field, "phase_start event should contain %s", field)
	}
}
