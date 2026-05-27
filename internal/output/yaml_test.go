package output

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestYAMLRenderer_PhaseStart(t *testing.T) {
	var buf bytes.Buffer
	r := NewYAMLRenderer(&buf)

	info := PhaseInfo{
		ID:        "test_phase",
		Name:      "Test Phase",
		Number:    1,
		Total:     5,
		StartTime: time.Now(),
	}

	err := r.PhaseStart(info)
	require.NoError(t, err)

	// Parse YAML output
	output := buf.String()
	docs := splitYAMLDocs(output)
	require.Len(t, docs, 1)

	var event map[string]interface{}
	err = yaml.Unmarshal([]byte(docs[0]), &event)
	require.NoError(t, err)

	// Verify event structure
	assert.Equal(t, "phase_start", event["event"])
	assert.EqualValues(t, 1, event["sequence"])
	assert.NotEmpty(t, event["timestamp"])
	assert.Equal(t, "test_phase", event["phase_id"])
	assert.Equal(t, "Test Phase", event["phase_name"])
	assert.EqualValues(t, 1, event["phase_number"])
	assert.EqualValues(t, 5, event["total_phases"])

	// Verify timestamp is ISO8601
	timestamp, ok := event["timestamp"].(string)
	require.True(t, ok)
	_, err = time.Parse(time.RFC3339, timestamp)
	assert.NoError(t, err, "timestamp should be valid ISO8601")
}

func TestYAMLRenderer_PhaseProgress(t *testing.T) {
	var buf bytes.Buffer
	r := NewYAMLRenderer(&buf)

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

	// Parse YAML output
	output := buf.String()
	docs := splitYAMLDocs(output)
	require.Len(t, docs, 1)

	var event map[string]interface{}
	err = yaml.Unmarshal([]byte(docs[0]), &event)
	require.NoError(t, err)

	// Verify event structure
	assert.Equal(t, "phase_progress", event["event"])
	assert.EqualValues(t, 1, event["sequence"])
	assert.Equal(t, "files", event["category"])
	assert.EqualValues(t, 3, event["current"])
	assert.EqualValues(t, 10, event["total"])
	assert.Equal(t, "config.yaml", event["item"])
	assert.Equal(t, "running", event["status"])
	assert.EqualValues(t, 30, event["percentage"])
	assert.EqualValues(t, 5000, event["eta_ms"])
}

func TestYAMLRenderer_PhaseProgressWithoutETA(t *testing.T) {
	var buf bytes.Buffer
	r := NewYAMLRenderer(&buf)

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

	// Parse YAML output
	output := buf.String()
	docs := splitYAMLDocs(output)
	require.Len(t, docs, 1)

	var event map[string]interface{}
	err = yaml.Unmarshal([]byte(docs[0]), &event)
	require.NoError(t, err)

	// Verify ETA is not present when zero
	_, hasETA := event["eta_ms"]
	assert.False(t, hasETA, "ETA should not be present when zero")
}

func TestYAMLRenderer_PhaseComplete(t *testing.T) {
	var buf bytes.Buffer
	r := NewYAMLRenderer(&buf)

	startTime := time.Now().Add(-2 * time.Second)
	info := PhaseInfo{
		ID:        "test_phase",
		Name:      "Test Phase",
		Number:    1,
		Total:     5,
		StartTime: startTime,
	}

	err := r.PhaseComplete(info)
	require.NoError(t, err)

	// Parse YAML output
	output := buf.String()
	docs := splitYAMLDocs(output)
	require.Len(t, docs, 1)

	var event map[string]interface{}
	err = yaml.Unmarshal([]byte(docs[0]), &event)
	require.NoError(t, err)

	// Verify event structure
	assert.Equal(t, "phase_complete", event["event"])
	assert.Equal(t, "test_phase", event["phase_id"])

	// Verify duration is approximately 2 seconds (allow some tolerance)
	durationMS, ok := event["duration_ms"].(uint64)
	require.True(t, ok)
	assert.InDelta(t, 2000, durationMS, 100, "duration should be ~2000ms")
}

func TestYAMLRenderer_PhaseFailed(t *testing.T) {
	var buf bytes.Buffer
	r := NewYAMLRenderer(&buf)

	startTime := time.Now().Add(-500 * time.Millisecond)
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

	// Parse YAML output
	output := buf.String()
	docs := splitYAMLDocs(output)
	require.Len(t, docs, 1)

	var event map[string]interface{}
	err = yaml.Unmarshal([]byte(docs[0]), &event)
	require.NoError(t, err)

	// Verify event structure
	assert.Equal(t, "phase_failed", event["event"])
	assert.Equal(t, "test_phase", event["phase_id"])
	assert.Contains(t, event["error"], "assert.AnError")

	// Verify duration
	durationMS, ok := event["duration_ms"].(uint64)
	require.True(t, ok)
	assert.Greater(t, durationMS, uint64(400), "duration should be at least 400ms")
}

func TestYAMLRenderer_PhaseSkipped(t *testing.T) {
	var buf bytes.Buffer
	r := NewYAMLRenderer(&buf)

	info := PhaseInfo{
		ID:        "test_phase",
		Name:      "Test Phase",
		Number:    3,
		Total:     5,
		StartTime: time.Now(),
	}

	err := r.PhaseSkipped(info, "not applicable")
	require.NoError(t, err)

	// Parse YAML output
	output := buf.String()
	docs := splitYAMLDocs(output)
	require.Len(t, docs, 1)

	var event map[string]interface{}
	err = yaml.Unmarshal([]byte(docs[0]), &event)
	require.NoError(t, err)

	// Verify event structure
	assert.Equal(t, "phase_skipped", event["event"])
	assert.Equal(t, "test_phase", event["phase_id"])
	assert.Equal(t, "not applicable", event["reason"])
}

func TestYAMLRenderer_Finalize_Success(t *testing.T) {
	var buf bytes.Buffer
	r := NewYAMLRenderer(&buf)

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

	// Parse YAML output
	output := buf.String()
	docs := splitYAMLDocs(output)
	require.Len(t, docs, 1)

	var event map[string]interface{}
	err = yaml.Unmarshal([]byte(docs[0]), &event)
	require.NoError(t, err)

	// Verify event structure
	assert.Equal(t, "summary", event["event"])
	assert.EqualValues(t, 5, event["total_phases"])
	assert.EqualValues(t, 5, event["completed_phases"])
	assert.EqualValues(t, 0, event["failed_phases"])
	assert.EqualValues(t, 0, event["skipped_phases"])
	assert.EqualValues(t, 10000, event["duration_ms"])
	assert.True(t, event["success"].(bool))

	// Verify errors field is not present when empty
	_, hasErrors := event["errors"]
	assert.False(t, hasErrors, "errors field should not be present when empty")
}

func TestYAMLRenderer_Finalize_WithErrors(t *testing.T) {
	var buf bytes.Buffer
	r := NewYAMLRenderer(&buf)

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

	// Parse YAML output
	output := buf.String()
	docs := splitYAMLDocs(output)
	require.Len(t, docs, 1)

	var event map[string]interface{}
	err = yaml.Unmarshal([]byte(docs[0]), &event)
	require.NoError(t, err)

	// Verify errors are included
	errors, ok := event["errors"].([]interface{})
	require.True(t, ok)
	assert.Len(t, errors, 2)
	assert.Equal(t, "error 1", errors[0])
	assert.Equal(t, "error 2", errors[1])
	assert.False(t, event["success"].(bool))
}

func TestYAMLRenderer_SequenceMonotonicity(t *testing.T) {
	var buf bytes.Buffer
	r := NewYAMLRenderer(&buf)

	// Emit multiple events
	info := PhaseInfo{
		ID:        "phase1",
		Name:      "Phase 1",
		Number:    1,
		Total:     3,
		StartTime: time.Now(),
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

	// Parse all documents and verify sequence numbers
	output := buf.String()
	docs := splitYAMLDocs(output)
	assert.Len(t, docs, 3)

	sequences := []uint64{}
	for _, doc := range docs {
		var event map[string]interface{}
		err := yaml.Unmarshal([]byte(doc), &event)
		require.NoError(t, err)
		seq := event["sequence"].(uint64)
		sequences = append(sequences, seq)
	}

	assert.EqualValues(t, 1, sequences[0])
	assert.EqualValues(t, 2, sequences[1])
	assert.EqualValues(t, 3, sequences[2])
}

func TestYAMLRenderer_DocumentSeparators(t *testing.T) {
	var buf bytes.Buffer
	r := NewYAMLRenderer(&buf)

	// Emit multiple events
	info1 := PhaseInfo{ID: "p1", Name: "Phase 1", Number: 1, Total: 2, StartTime: time.Now()}
	info2 := PhaseInfo{ID: "p2", Name: "Phase 2", Number: 2, Total: 2, StartTime: time.Now()}

	err := r.PhaseStart(info1)
	require.NoError(t, err)

	err = r.PhaseStart(info2)
	require.NoError(t, err)

	// Verify output has document separators
	output := buf.String()
	assert.Contains(t, output, "---\n", "output should contain document separators")

	// Count separators (should be 2, one per document)
	separatorCount := strings.Count(output, "---\n")
	assert.Equal(t, 2, separatorCount, "should have one separator per document")

	// Verify documents can be parsed
	docs := splitYAMLDocs(output)
	assert.Len(t, docs, 2)

	for i, doc := range docs {
		var event map[string]interface{}
		err := yaml.Unmarshal([]byte(doc), &event)
		assert.NoError(t, err, "document %d should be valid YAML", i+1)
		assert.Equal(t, "phase_start", event["event"])
	}
}

func TestYAMLRenderer_ThreadSafety(t *testing.T) {
	var buf bytes.Buffer
	r := NewYAMLRenderer(&buf)

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
				StartTime: time.Now(),
			}

			err := r.PhaseStart(info)
			assert.NoError(t, err)
		}(i)
	}

	wg.Wait()

	// Parse all documents
	output := buf.String()
	docs := splitYAMLDocs(output)

	assert.Equal(t, numGoroutines, len(docs), "all events should be written")
}

func TestYAMLRenderer_Close(t *testing.T) {
	var buf bytes.Buffer
	r := NewYAMLRenderer(&buf)

	err := r.Close()
	assert.NoError(t, err)

	// Verify no output from Close
	assert.Empty(t, buf.String())
}

func TestYAMLRenderer_AllFields(t *testing.T) {
	var buf bytes.Buffer
	r := NewYAMLRenderer(&buf)

	info := PhaseInfo{
		ID:        "comprehensive_test",
		Name:      "Comprehensive Test",
		Number:    1,
		Total:     1,
		StartTime: time.Now(),
	}

	err := r.PhaseStart(info)
	require.NoError(t, err)

	// Parse and verify all required fields are present
	output := buf.String()
	docs := splitYAMLDocs(output)
	require.Len(t, docs, 1)

	var event map[string]interface{}
	err = yaml.Unmarshal([]byte(docs[0]), &event)
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

func TestYAMLRenderer_OutputFormat(t *testing.T) {
	var buf bytes.Buffer
	r := NewYAMLRenderer(&buf)

	info := PhaseInfo{
		ID:        "test",
		Name:      "Test",
		Number:    1,
		Total:     1,
		StartTime: time.Now(),
	}

	err := r.PhaseStart(info)
	require.NoError(t, err)

	output := buf.String()

	// Verify format starts with ---
	assert.True(t, strings.HasPrefix(output, "---\n"), "output should start with document separator")

	// Verify it's valid YAML
	var event map[string]interface{}
	docs := splitYAMLDocs(output)
	err = yaml.Unmarshal([]byte(docs[0]), &event)
	assert.NoError(t, err, "output should be valid YAML")
}

// Helper function to split YAML documents by --- separator
func splitYAMLDocs(yamlStr string) []string {
	// Split by document separator and filter empty strings
	parts := strings.Split(yamlStr, "---\n")
	docs := []string{}

	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			docs = append(docs, trimmed)
		}
	}

	return docs
}
