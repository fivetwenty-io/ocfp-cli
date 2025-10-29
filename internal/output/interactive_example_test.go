package output_test

import (
	"fmt"
	"os"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/output"
)

// ExampleInteractiveRenderer demonstrates the interactive renderer in action.
func ExampleInteractiveRenderer() {
	// Create renderer writing to stdout
	renderer := output.NewInteractiveRenderer(os.Stdout)
	defer renderer.Close()

	// Simulate a multi-phase operation
	phases := []struct {
		id   string
		name string
	}{
		{"ssh_agent", "SSH Agent Forwarding"},
		{"repositories", "Repository Cloning"},
		{"dependencies", "Dependency Installation"},
	}

	totalPhases := len(phases)

	// Process each phase
	for i, phase := range phases {
		// Start phase
		info := output.PhaseInfo{
			ID:        phase.id,
			Name:      phase.name,
			Number:    i + 1,
			Total:     totalPhases,
			StartTime: time.Now(),
		}

		if err := renderer.PhaseStart(info); err != nil {
			fmt.Printf("Error: %v\n", err)
			return
		}

		// Simulate progress
		steps := 10
		for step := 1; step <= steps; step++ {
			progress := output.ProgressInfo{
				Category:   "tasks",
				Current:    step,
				Total:      steps,
				Item:       fmt.Sprintf("item_%d", step),
				Percentage: float64(step) / float64(steps) * 100.0,
			}

			if step < steps {
				// Estimate ETA for remaining steps
				progress.ETA = time.Duration(steps-step) * 100 * time.Millisecond
			}

			if err := renderer.PhaseProgress(progress); err != nil {
				fmt.Printf("Error: %v\n", err)
				return
			}

			// Simulate work
			time.Sleep(50 * time.Millisecond)
		}

		// Complete phase
		if err := renderer.PhaseComplete(info); err != nil {
			fmt.Printf("Error: %v\n", err)
			return
		}
	}

	// Finalize with summary
	summary := output.Summary{
		TotalPhases:     totalPhases,
		CompletedPhases: totalPhases,
		FailedPhases:    0,
		SkippedPhases:   0,
		Duration:        2 * time.Second,
		Success:         true,
		Errors:          nil,
	}

	if err := renderer.Finalize(summary); err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
}
