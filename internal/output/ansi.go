package output

import (
	"fmt"
	"regexp"
)

// ANSI color codes for terminal output styling.
const (
	// ColorReset resets all text formatting to default.
	ColorReset = "\033[0m"

	// ColorRed sets text color to red.
	ColorRed = "\033[31m"

	// ColorGreen sets text color to green.
	ColorGreen = "\033[32m"

	// ColorYellow sets text color to yellow.
	ColorYellow = "\033[33m"

	// ColorBlue sets text color to blue.
	ColorBlue = "\033[34m"

	// ColorCyan sets text color to cyan.
	ColorCyan = "\033[36m"

	// ColorGray sets text color to gray.
	ColorGray = "\033[90m"

	// ColorBold makes text bold.
	ColorBold = "\033[1m"

	// ColorDim makes text dimmed.
	ColorDim = "\033[2m"
)

// ANSI cursor control sequences.
const (
	// ClearLine clears the current line.
	ClearLine = "\033[2K"

	// CursorUp moves cursor up by one line.
	CursorUp = "\033[A"

	// CursorDown moves cursor down by one line.
	CursorDown = "\033[B"

	// HideCursor hides the cursor.
	HideCursor = "\033[?25l"

	// ShowCursor shows the cursor.
	ShowCursor = "\033[?25h"

	// ClearToEnd clears from cursor to end of line.
	ClearToEnd = "\033[K"
)

// ansiPattern matches ANSI escape sequences for stripping.
var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// ClearCurrentLine returns the escape sequence to clear the current line.
func ClearCurrentLine() string {
	return ClearLine + "\r"
}

// MoveCursorUp returns the escape sequence to move cursor up n lines.
func MoveCursorUp(n int) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf("\033[%dA", n)
}

// MoveCursorDown returns the escape sequence to move cursor down n lines.
func MoveCursorDown(n int) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf("\033[%dB", n)
}

// SetColor returns the given text wrapped with the specified color code.
func SetColor(text, color string) string {
	return color + text + ColorReset
}

// Bold returns the text wrapped with bold formatting.
func Bold(text string) string {
	return ColorBold + text + ColorReset
}

// Red returns the text colored red.
func Red(text string) string {
	return SetColor(text, ColorRed)
}

// Green returns the text colored green.
func Green(text string) string {
	return SetColor(text, ColorGreen)
}

// Yellow returns the text colored yellow.
func Yellow(text string) string {
	return SetColor(text, ColorYellow)
}

// Blue returns the text colored blue.
func Blue(text string) string {
	return SetColor(text, ColorBlue)
}

// Cyan returns the text colored cyan.
func Cyan(text string) string {
	return SetColor(text, ColorCyan)
}

// Gray returns the text colored gray.
func Gray(text string) string {
	return SetColor(text, ColorGray)
}

// Dim returns the text with dimmed formatting.
func Dim(text string) string {
	return ColorDim + text + ColorReset
}

// StripANSI removes all ANSI escape codes from the given string.
// This is useful for getting the display width of colored text or
// for outputting to contexts that don't support ANSI codes.
func StripANSI(s string) string {
	return ansiPattern.ReplaceAllString(s, "")
}

// ColorForStatus returns an appropriate color for the given status.
func ColorForStatus(status Status) string {
	switch status {
	case StatusCompleted:
		return ColorGreen
	case StatusFailed:
		return ColorRed
	case StatusRunning:
		return ColorCyan
	case StatusSkipped:
		return ColorYellow
	case StatusPending:
		return ColorGray
	default:
		return ColorReset
	}
}

// IconForStatus returns a Unicode icon appropriate for the status.
func IconForStatus(status Status) string {
	switch status {
	case StatusCompleted:
		return "✓"
	case StatusFailed:
		return "✗"
	case StatusRunning:
		return "→"
	case StatusSkipped:
		return "⊘"
	case StatusPending:
		return "○"
	default:
		return "?"
	}
}
