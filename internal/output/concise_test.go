package output

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewConciseRenderer(t *testing.T) {
	var buf bytes.Buffer
	renderer := NewConciseRenderer(&buf)

	assert.NotNil(t, renderer)
	assert.NotNil(t, renderer.writer)
	assert.NotNil(t, renderer.log)
	assert.NotNil(t, renderer.phaseSubtasks)
	assert.NotNil(t, renderer.completedPhases)
}

func TestConciseRenderer_PhaseStart(t *testing.T) {
	var buf bytes.Buffer
	renderer := NewConciseRenderer(&buf)

	info := PhaseInfo{
		ID:        "test_phase",
		Name:      "Test Phase",
		Number:    8,
		Total:     25,
		StartTime: time.Now(),
	}

	err := renderer.PhaseStart(info)
	require.NoError(t, err)

	output := buf.String()

	// Verify format: [NN/Total] Starting phase: phase_name
	assert.Contains(t, output, "[08/25]")
	assert.Contains(t, output, "Starting phase: Test Phase")
	assert.True(t, strings.HasSuffix(output, "\n"))

	// Verify NO ANSI codes
	assert.NotContains(t, output, "\033[")
	assert.NotContains(t, output, "\r")
}

func TestConciseRenderer_PhaseProgress(t *testing.T) {
	var buf bytes.Buffer
	renderer := NewConciseRenderer(&buf)

	// Start a phase first
	phaseInfo := PhaseInfo{
		ID:        "repos",
		Name:      "Repositories",
		Number:    8,
		Total:     25,
		StartTime: time.Now(),
	}
	err := renderer.PhaseStart(phaseInfo)
	require.NoError(t, err)

	buf.Reset() // Clear phase start output

	// Report progress
	progress := ProgressInfo{
		Category:   "snap_packages",
		Item:       "kubectl",
		Current:    1,
		Total:      2,
		Status:     StatusRunning,
		Percentage: 50.0,
	}

	err = renderer.PhaseProgress(progress)
	require.NoError(t, err)

	output := buf.String()

	// Verify progress line format
	assert.Contains(t, output, "[08/25]")
	assert.Contains(t, output, "snap_packages")
	assert.Contains(t, output, "⟳") // Running icon

	// Verify subtask tree (should have tree characters - either ├─ or └─)
	hasTreeChars := strings.Contains(output, "├─") || strings.Contains(output, "└─")
	assert.True(t, hasTreeChars, "Output should contain tree characters")
	assert.Contains(t, output, "kubectl")
	assert.Contains(t, output, "1/2")

	// Verify NO ANSI codes
	assert.NotContains(t, output, "\033[")
	assert.NotContains(t, output, "\r")
}

func TestConciseRenderer_PhaseProgress_MultipleSubtasks(t *testing.T) {
	var buf bytes.Buffer
	renderer := NewConciseRenderer(&buf)

	// Start phase
	phaseInfo := PhaseInfo{
		ID:     "repos",
		Name:   "Repositories",
		Number: 8,
		Total:  25,
	}
	err := renderer.PhaseStart(phaseInfo)
	require.NoError(t, err)

	// Add multiple subtasks
	subtasks := []ProgressInfo{
		{
			Category: "snap_packages",
			Item:     "kubectl",
			Current:  1,
			Total:    2,
			Status:   StatusRunning,
		},
		{
			Category: "cpan_modules",
			Item:     "Smart::Comments",
			Current:  2,
			Total:    3,
			Status:   StatusCompleted,
		},
		{
			Category: "binary_tools",
			Item:     "nvim",
			Current:  14,
			Total:    15,
			Status:   StatusRunning,
		},
	}

	for _, progress := range subtasks {
		buf.Reset()
		err = renderer.PhaseProgress(progress)
		require.NoError(t, err)

		output := buf.String()

		// Each should have tree structure
		assert.Contains(t, output, progress.Category)
		assert.Contains(t, output, progress.Item)

		// Verify status icons
		if progress.Status == StatusRunning {
			assert.Contains(t, output, "⟳")
		} else if progress.Status == StatusCompleted {
			assert.Contains(t, output, "✓")
		}
	}
}

func TestConciseRenderer_PhaseComplete(t *testing.T) {
	var buf bytes.Buffer
	renderer := NewConciseRenderer(&buf)

	// Start phase
	phaseInfo := PhaseInfo{
		ID:     "test_phase",
		Name:   "Test Phase",
		Number: 8,
		Total:  25,
	}
	err := renderer.PhaseStart(phaseInfo)
	require.NoError(t, err)

	// Simulate some time passing
	time.Sleep(10 * time.Millisecond)

	buf.Reset() // Clear previous output

	// Complete phase
	err = renderer.PhaseComplete(phaseInfo)
	require.NoError(t, err)

	output := buf.String()

	// Verify format: [NN/Total] ✓ Phase completed: phase_name (duration)
	assert.Contains(t, output, "[08/25]")
	assert.Contains(t, output, "✓")
	assert.Contains(t, output, "Phase completed: Test Phase")
	assert.Contains(t, output, "s)") // Duration should be there

	// Verify NO ANSI codes
	assert.NotContains(t, output, "\033[")
}

func TestConciseRenderer_PhaseFailed(t *testing.T) {
	var buf bytes.Buffer
	renderer := NewConciseRenderer(&buf)

	phaseInfo := PhaseInfo{
		ID:     "test_phase",
		Name:   "Test Phase",
		Number: 8,
		Total:  25,
	}

	testErr := errors.New("connection timeout")
	err := renderer.PhaseFailed(phaseInfo, testErr)
	require.NoError(t, err)

	output := buf.String()

	// Verify format: [NN/Total] ✗ Phase failed: phase_name - error
	assert.Contains(t, output, "[08/25]")
	assert.Contains(t, output, "✗")
	assert.Contains(t, output, "Phase failed: Test Phase")
	assert.Contains(t, output, "connection timeout")

	// Verify NO ANSI codes
	assert.NotContains(t, output, "\033[")
}

func TestConciseRenderer_PhaseSkipped(t *testing.T) {
	var buf bytes.Buffer
	renderer := NewConciseRenderer(&buf)

	phaseInfo := PhaseInfo{
		ID:     "test_phase",
		Name:   "Test Phase",
		Number: 8,
		Total:  25,
	}

	err := renderer.PhaseSkipped(phaseInfo, "already configured")
	require.NoError(t, err)

	output := buf.String()

	// Verify format: [NN/Total] ⤷ Phase skipped: phase_name (reason)
	assert.Contains(t, output, "[08/25]")
	assert.Contains(t, output, "⤷")
	assert.Contains(t, output, "Phase skipped: Test Phase")
	assert.Contains(t, output, "already configured")

	// Verify NO ANSI codes
	assert.NotContains(t, output, "\033[")
}

func TestConciseRenderer_Finalize_Success(t *testing.T) {
	var buf bytes.Buffer
	renderer := NewConciseRenderer(&buf)

	summary := Summary{
		TotalPhases:     25,
		CompletedPhases: 25,
		FailedPhases:    0,
		SkippedPhases:   0,
		Duration:        3*time.Minute + 45*time.Second,
		Success:         true,
		Errors:          []string{},
	}

	err := renderer.Finalize(summary)
	require.NoError(t, err)

	output := buf.String()

	// Verify summary structure
	assert.Contains(t, output, "===== Summary =====")
	assert.Contains(t, output, "Status: Success")
	assert.Contains(t, output, "Duration:")
	assert.Contains(t, output, "Phases completed: 25")
	assert.Contains(t, output, "Phases failed: 0")

	// Verify NO ANSI codes
	assert.NotContains(t, output, "\033[")
}

func TestConciseRenderer_Finalize_Failure(t *testing.T) {
	var buf bytes.Buffer
	renderer := NewConciseRenderer(&buf)

	summary := Summary{
		TotalPhases:     25,
		CompletedPhases: 20,
		FailedPhases:    3,
		SkippedPhases:   2,
		Duration:        2*time.Minute + 30*time.Second,
		Success:         false,
		Errors: []string{
			"phase 8: connection timeout",
			"phase 15: permission denied",
			"phase 22: file not found",
		},
	}

	err := renderer.Finalize(summary)
	require.NoError(t, err)

	output := buf.String()

	// Verify failure summary
	assert.Contains(t, output, "Status: Failed")
	assert.Contains(t, output, "Phases completed: 20")
	assert.Contains(t, output, "Phases failed: 3")
	assert.Contains(t, output, "Phases skipped: 2")

	// Verify errors section
	assert.Contains(t, output, "Errors:")
	assert.Contains(t, output, "connection timeout")
	assert.Contains(t, output, "permission denied")
	assert.Contains(t, output, "file not found")

	// Verify NO ANSI codes
	assert.NotContains(t, output, "\033[")
}

func TestConciseRenderer_NoANSICodes(t *testing.T) {
	var buf bytes.Buffer
	renderer := NewConciseRenderer(&buf)

	// Run through complete lifecycle
	phaseInfo := PhaseInfo{
		ID:     "test",
		Name:   "Test Phase",
		Number: 1,
		Total:  3,
	}

	// Phase start
	err := renderer.PhaseStart(phaseInfo)
	require.NoError(t, err)

	// Progress
	progress := ProgressInfo{
		Category:   "items",
		Item:       "test_item",
		Current:    1,
		Total:      5,
		Status:     StatusRunning,
		Percentage: 20.0,
	}
	err = renderer.PhaseProgress(progress)
	require.NoError(t, err)

	// Complete
	err = renderer.PhaseComplete(phaseInfo)
	require.NoError(t, err)

	// Summary
	summary := Summary{
		TotalPhases:     3,
		CompletedPhases: 1,
		Duration:        5 * time.Second,
		Success:         true,
	}
	err = renderer.Finalize(summary)
	require.NoError(t, err)

	output := buf.String()

	// Verify NO ANSI escape codes anywhere
	assert.NotContains(t, output, "\033[")
	assert.NotContains(t, output, "\x1b[")

	// Verify NO carriage returns (append-only)
	assert.NotContains(t, output, "\r")
}

func TestConciseRenderer_GrepFriendly(t *testing.T) {
	var buf bytes.Buffer
	renderer := NewConciseRenderer(&buf)

	// Create multiple phases
	for i := 1; i <= 3; i++ {
		phaseInfo := PhaseInfo{
			ID:     "phase" + string(rune('0'+i)),
			Name:   "Phase " + string(rune('0'+i)),
			Number: i,
			Total:  3,
		}

		err := renderer.PhaseStart(phaseInfo)
		require.NoError(t, err)

		err = renderer.PhaseComplete(phaseInfo)
		require.NoError(t, err)
	}

	output := buf.String()
	lines := strings.Split(output, "\n")

	// Every line should start with phase number for grep-ability
	for _, line := range lines {
		if line == "" {
			continue
		}

		// Should start with [NN/Total]
		assert.True(t,
			strings.HasPrefix(line, "[01/") ||
				strings.HasPrefix(line, "[02/") ||
				strings.HasPrefix(line, "[03/"),
			"Line should start with phase number: "+line,
		)
	}
}

func TestConciseRenderer_StatusIcons(t *testing.T) {
	var buf bytes.Buffer
	renderer := NewConciseRenderer(&buf)

	tests := []struct {
		status       Status
		expectedIcon string
	}{
		{StatusRunning, "⟳"},
		{StatusCompleted, "✓"},
		{StatusFailed, "✗"},
		{StatusSkipped, "⤷"},
		{StatusPending, "⏳"},
	}

	for _, tt := range tests {
		t.Run(tt.status.String(), func(t *testing.T) {
			icon := renderer.statusIcon(tt.status)
			assert.Equal(t, tt.expectedIcon, icon)
		})
	}
}

func TestConciseRenderer_TreeStructure(t *testing.T) {
	var buf bytes.Buffer
	renderer := NewConciseRenderer(&buf)

	// Start phase
	phaseInfo := PhaseInfo{
		ID:     "repos",
		Name:   "Repositories",
		Number: 8,
		Total:  25,
	}
	err := renderer.PhaseStart(phaseInfo)
	require.NoError(t, err)

	buf.Reset()

	// Add subtasks - last one should get └─
	subtasks := []ProgressInfo{
		{Category: "packages", Item: "item1", Current: 1, Total: 3, Status: StatusCompleted},
		{Category: "packages", Item: "item2", Current: 2, Total: 3, Status: StatusRunning},
		{Category: "packages", Item: "item3", Current: 3, Total: 3, Status: StatusPending},
	}

	for _, progress := range subtasks {
		buf.Reset()
		err = renderer.PhaseProgress(progress)
		require.NoError(t, err)
	}

	output := buf.String()

	// Should have tree characters
	hasTreeChars := strings.Contains(output, "├─") || strings.Contains(output, "└─")
	assert.True(t, hasTreeChars, "Output should contain tree characters")

	// Should have proper indentation
	assert.Contains(t, output, "   ") // Indentation for tree
}

func TestConciseRenderer_PhaseNumberFormatting(t *testing.T) {
	var buf bytes.Buffer
	renderer := NewConciseRenderer(&buf)

	tests := []struct {
		number   int
		total    int
		expected string
	}{
		{1, 25, "[01/25]"},
		{8, 25, "[08/25]"},
		{25, 25, "[25/25]"},
	}

	for _, tt := range tests {
		buf.Reset()

		phaseInfo := PhaseInfo{
			ID:     "test",
			Name:   "Test",
			Number: tt.number,
			Total:  tt.total,
		}

		err := renderer.PhaseStart(phaseInfo)
		require.NoError(t, err)

		output := buf.String()
		assert.Contains(t, output, tt.expected)
	}
}

func TestConciseRenderer_FormatDuration(t *testing.T) {
	var buf bytes.Buffer
	renderer := NewConciseRenderer(&buf)

	tests := []struct {
		duration time.Duration
		expected string
	}{
		{5 * time.Second, "5s"},
		{65 * time.Second, "1m05s"},
		{3*time.Minute + 45*time.Second, "3m45s"},
		{2*time.Hour + 15*time.Minute + 30*time.Second, "2h15m30s"},
	}

	for _, tt := range tests {
		result := renderer.formatDuration(tt.duration)
		assert.Equal(t, tt.expected, result)
	}
}

func TestConciseRenderer_ThreadSafety(t *testing.T) {
	var buf bytes.Buffer
	renderer := NewConciseRenderer(&buf)

	phaseInfo := PhaseInfo{
		ID:     "test",
		Name:   "Test Phase",
		Number: 1,
		Total:  1,
	}

	err := renderer.PhaseStart(phaseInfo)
	require.NoError(t, err)

	// Concurrent progress updates
	done := make(chan bool)

	for i := 0; i < 10; i++ {
		go func(idx int) {
			progress := ProgressInfo{
				Category:   "items",
				Item:       "item",
				Current:    idx,
				Total:      10,
				Status:     StatusRunning,
				Percentage: float64(idx * 10),
			}
			_ = renderer.PhaseProgress(progress)
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	// Should not panic and should produce output
	assert.NotEmpty(t, buf.String())
}

func TestConciseRenderer_Close(t *testing.T) {
	var buf bytes.Buffer
	renderer := NewConciseRenderer(&buf)

	err := renderer.Close()
	assert.NoError(t, err)

	// Should not write anything on close
	assert.Empty(t, buf.String())
}
