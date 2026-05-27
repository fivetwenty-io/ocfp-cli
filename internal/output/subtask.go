package output

import (
	"fmt"
	"time"
)

// subtaskState tracks the last written state of a subtask to suppress unchanged lines.
type subtaskState struct {
	current int
	total   int
	status  Status
}

// subtaskInfo tracks subtask state for tree rendering.
type subtaskInfo struct {
	category string
	item     string
	current  int
	total    int
	status   Status
}

// statusIcon returns the Unicode icon for a given status.
func statusIcon(status Status) string {
	switch status {
	case StatusRunning:
		return "⟳"
	case StatusCompleted:
		return IconCheck
	case StatusFailed:
		return IconCross
	case StatusSkipped:
		return "⤷"
	case StatusPending:
		return "⏳"
	default:
		return "?"
	}
}

// shouldLogMilestone reports whether percentage is within ±1 of 25/50/75/100.
func shouldLogMilestone(percentage float64) bool {
	for _, milestone := range []float64{25.0, 50.0, 75.0, 100.0} {
		if percentage >= milestone-1.0 && percentage <= milestone+1.0 {
			return true
		}
	}

	return false
}

// formatDuration formats d rounded to the nearest second.
func formatDuration(d time.Duration) string { //nolint:varnamelen
	d = d.Round(time.Second)

	if d < time.Minute {
		return fmt.Sprintf("%.0fs", d.Seconds())
	}

	minutes := int(d.Minutes())
	seconds := int(d.Seconds()) - (minutes * 60) //nolint:mnd

	if minutes < 60 { //nolint:mnd
		return fmt.Sprintf("%dm%02ds", minutes, seconds)
	}

	hours := minutes / 60 //nolint:mnd
	minutes %= 60

	return fmt.Sprintf("%dh%02dm%02ds", hours, minutes, seconds)
}
