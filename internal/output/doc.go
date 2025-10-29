// Package output provides a multi-mode output rendering system for CLI operations.
//
// The output package supports four rendering modes:
//   - Interactive: Rich, animated terminal output with progress indicators
//   - Concise: Minimal, single-line status updates
//   - JSON: Structured JSON for programmatic consumption
//   - YAML: Structured YAML for programmatic consumption
//
// # Environment Detection
//
// The package automatically detects the execution environment including TTY
// capabilities, CI systems, and terminal dimensions. This information is used
// to select appropriate default rendering modes.
//
// Example usage:
//
//	env := output.DetectEnvironment(os.Stdout)
//	mode := env.DefaultMode()
//	renderer, err := output.NewRenderer(os.Stdout, mode)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer renderer.Close()
//
// # Phase-Based Progress Tracking
//
// The Renderer interface provides phase-based progress tracking with support
// for incremental progress updates within each phase:
//
//	// Start a new phase
//	err = renderer.PhaseStart(output.PhaseInfo{
//	    ID:        "provision",
//	    Name:      "Provisioning Resources",
//	    Number:    1,
//	    Total:     3,
//	    StartTime: time.Now(),
//	})
//
//	// Report progress within the phase
//	err = renderer.PhaseProgress(output.ProgressInfo{
//	    Category:   "Servers",
//	    Current:    5,
//	    Total:      10,
//	    Item:       "web-server-01",
//	    Status:     output.StatusRunning,
//	    Percentage: 50.0,
//	})
//
//	// Complete the phase
//	err = renderer.PhaseComplete(info)
//
// # ANSI Utilities
//
// The package provides ANSI escape code utilities for terminal styling:
//
//	colored := output.Red("Error:")
//	bold := output.Bold("Important")
//	combined := output.Green(output.Bold("✓ Success"))
//
// For non-terminal contexts, use StripANSI to remove formatting:
//
//	plain := output.StripANSI(colored)
//
// # Mode Selection
//
// Output modes can be explicitly specified or auto-detected:
//
//	// Auto-detect based on environment
//	env := output.DetectEnvironment(os.Stdout)
//	mode := env.DefaultMode()
//
//	// Explicit mode selection
//	mode, err := output.ParseMode("json")
//
//	// Create renderer
//	renderer, err := output.NewRenderer(os.Stdout, mode)
package output
