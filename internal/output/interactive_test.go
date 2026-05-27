package output

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestNewInteractiveRenderer(t *testing.T) {
	buf := &bytes.Buffer{}
	r := NewInteractiveRenderer(buf)

	if r == nil {
		t.Fatal("Expected renderer to be created, got nil")
	}

	if r.config == nil {
		t.Fatal("Expected config to be initialized")
	}

	if r.config.ProgressWidth != 30 {
		t.Errorf("Expected progress width 30, got %d", r.config.ProgressWidth)
	}

	if r.config.UpdateInterval != 100*time.Millisecond {
		t.Errorf("Expected update interval 100ms, got %v", r.config.UpdateInterval)
	}
}

func TestPhaseStart(t *testing.T) {
	buf := &bytes.Buffer{}
	r := NewInteractiveRenderer(buf)

	info := PhaseInfo{
		ID:     "test_phase",
		Name:   "Test Phase",
		Number: 1,
		Total:  5,
	}

	err := r.PhaseStart(info)
	if err != nil {
		t.Fatalf("PhaseStart failed: %v", err)
	}

	output := buf.String()
	// New format: [01/5] Starting phase: Test Phase
	if !strings.Contains(output, "Test Phase") {
		t.Errorf("Expected phase name 'Test Phase', got: %s", output)
	}

	// Check for phase counter [01/5] (with zero-padding)
	if !strings.Contains(output, "[01/5]") {
		t.Errorf("Expected phase counter '[01/5]', got: %s", output)
	}

	// Check for "Starting phase"
	if !strings.Contains(output, "Starting phase") {
		t.Errorf("Expected 'Starting phase', got: %s", output)
	}
}

func TestPhaseProgress(t *testing.T) {
	buf := &bytes.Buffer{}
	r := NewInteractiveRenderer(buf)

	// Start a phase first
	info := PhaseInfo{
		ID:     "test_phase",
		Name:   "Test Phase",
		Number: 1,
		Total:  5,
	}
	_ = r.PhaseStart(info)

	// Clear buffer to test progress output
	buf.Reset()

	progress := ProgressInfo{
		Category:   "files",
		Current:    5,
		Total:      10,
		Item:       "test.txt",
		Percentage: 50.0,
		ETA:        15 * time.Second,
	}

	err := r.PhaseProgress(progress)
	if err != nil {
		t.Fatalf("PhaseProgress failed: %v", err)
	}

	output := buf.String()
	// New format shows progress line and subtask tree
	// Format: [01/5] ⏳ files (0s)
	//         [01/5]   └─ files: test.txt (⏳ 5/10)

	// Check for category in output
	if !strings.Contains(output, "files") {
		t.Errorf("Expected category 'files', got: %s", output)
	}

	// Check for item name in tree
	if !strings.Contains(output, "test.txt") {
		t.Errorf("Expected item 'test.txt', got: %s", output)
	}

	// Check for current/total count in subtask tree
	if !strings.Contains(output, "5/10") {
		t.Errorf("Expected progress count '5/10', got: %s", output)
	}
}

func TestPhaseComplete(t *testing.T) {
	buf := &bytes.Buffer{}
	r := NewInteractiveRenderer(buf)

	// Start a phase first
	info := PhaseInfo{
		ID:     "test_phase",
		Name:   "Test Phase",
		Number: 1,
		Total:  5,
	}
	_ = r.PhaseStart(info)

	// Wait a bit to have measurable duration
	time.Sleep(10 * time.Millisecond)

	// Clear buffer to test complete output
	buf.Reset()

	err := r.PhaseComplete(info)
	if err != nil {
		t.Fatalf("PhaseComplete failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "✓") {
		t.Errorf("Expected completion checkmark, got: %s", output)
	}

	if !strings.Contains(output, "Test Phase") {
		t.Errorf("Expected phase name, got: %s", output)
	}

	// Check that output contains completion marker
	if !strings.Contains(output, "✓") {
		t.Errorf("Expected completion marker ✓ in output, got: %s", output)
	}
}

func TestPhaseFailed(t *testing.T) {
	buf := &bytes.Buffer{}
	r := NewInteractiveRenderer(buf)

	info := PhaseInfo{
		ID:     "test_phase",
		Name:   "Test Phase",
		Number: 1,
		Total:  5,
	}

	testErr := &mockError{msg: "test error"}
	err := r.PhaseFailed(info, testErr)
	if err != nil {
		t.Fatalf("PhaseFailed failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "✗") {
		t.Errorf("Expected failure mark, got: %s", output)
	}

	if !strings.Contains(output, "test error") {
		t.Errorf("Expected error message, got: %s", output)
	}
}

func TestPhaseSkipped(t *testing.T) {
	buf := &bytes.Buffer{}
	r := NewInteractiveRenderer(buf)

	info := PhaseInfo{
		ID:     "test_phase",
		Name:   "Test Phase",
		Number: 1,
		Total:  5,
	}

	err := r.PhaseSkipped(info, "not needed")
	if err != nil {
		t.Fatalf("PhaseSkipped failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "skipped") {
		t.Errorf("Expected skipped message, got: %s", output)
	}

	if !strings.Contains(output, "not needed") {
		t.Errorf("Expected reason, got: %s", output)
	}
}

func TestFinalize(t *testing.T) {
	buf := &bytes.Buffer{}
	r := NewInteractiveRenderer(buf)

	summary := Summary{
		TotalPhases:     5,
		CompletedPhases: 5,
		FailedPhases:    0,
		Duration:        2*time.Minute + 30*time.Second,
		Success:         true,
	}

	err := r.Finalize(summary)
	if err != nil {
		t.Fatalf("Finalize failed: %v", err)
	}

	output := buf.String()
	// New format matches Concise: "Status: Success"
	if !strings.Contains(output, "Success") {
		t.Errorf("Expected success message, got: %s", output)
	}

	if !strings.Contains(output, "2m30s") {
		t.Errorf("Expected duration 2m30s, got: %s", output)
	}

	// New format: "Phases completed: 5"
	if !strings.Contains(output, "Phases completed: 5") {
		t.Errorf("Expected phases count, got: %s", output)
	}
}

func TestFinalizeWithFailure(t *testing.T) {
	buf := &bytes.Buffer{}
	r := NewInteractiveRenderer(buf)

	summary := Summary{
		TotalPhases:     5,
		CompletedPhases: 3,
		FailedPhases:    2,
		Duration:        1 * time.Minute,
		Success:         false,
		Errors:          []string{"error 1", "error 2"},
	}

	err := r.Finalize(summary)
	if err != nil {
		t.Fatalf("Finalize failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "Failed") {
		t.Errorf("Expected failure message, got: %s", output)
	}

	if !strings.Contains(output, "error 1") {
		t.Errorf("Expected error details, got: %s", output)
	}
}

// TestDrawProgressBar removed - progressive append strategy doesn't use progress bars
// The interactive renderer now uses a simpler progressive display without full-screen redraws

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		duration time.Duration
		want     string
	}{
		{5 * time.Second, "5s"},
		{30 * time.Second, "30s"},
		{1*time.Minute + 15*time.Second, "1m15s"},
		{2*time.Minute + 30*time.Second, "2m30s"},
		{1*time.Hour + 5*time.Minute + 20*time.Second, "1h05m20s"},
	}

	for _, tt := range tests {
		got := formatDuration(tt.duration)
		if got != tt.want {
			t.Errorf("formatDuration(%v) = %s, want %s", tt.duration, got, tt.want)
		}
	}
}

func TestThrottling(t *testing.T) {
	buf := &bytes.Buffer{}
	r := NewInteractiveRenderer(buf)

	// NOTE: The new append-only renderer doesn't throttle updates anymore
	// All progress updates produce output for simplicity and reliability

	// Start a phase
	info := PhaseInfo{
		ID:     "test_phase",
		Name:   "Test Phase",
		Number: 1,
		Total:  5,
	}
	_ = r.PhaseStart(info)

	// Clear buffer
	buf.Reset()

	progress := ProgressInfo{
		Category:   "test_category",
		Item:       "test_item",
		Current:    1,
		Total:      4,
		Percentage: 25.0,
		Status:     StatusRunning,
	}

	// First update should produce output
	err := r.PhaseProgress(progress)
	if err != nil {
		t.Fatalf("First progress update failed: %v", err)
	}

	firstOutput := buf.String()
	if firstOutput == "" {
		t.Error("Expected first update to produce output")
	}

	// Clear buffer
	buf.Reset()

	// Second update should also produce output (no throttling in new design)
	progress.Current = 2
	progress.Item = "test_item_2"
	progress.Percentage = 50.0
	err = r.PhaseProgress(progress)
	if err != nil {
		t.Fatalf("Second progress update failed: %v", err)
	}

	secondOutput := buf.String()
	if secondOutput == "" {
		t.Error("Expected second update to produce output (no throttling)")
	}

	// Third update should also produce output
	buf.Reset()
	progress.Current = 3
	progress.Item = "test_item_3"
	progress.Percentage = 75.0
	err = r.PhaseProgress(progress)
	if err != nil {
		t.Fatalf("Third progress update failed: %v", err)
	}

	thirdOutput := buf.String()
	if thirdOutput == "" {
		t.Error("Expected third update to produce output")
	}
}

// Mock error for testing
type mockError struct {
	msg string
}

func (e *mockError) Error() string {
	return e.msg
}
