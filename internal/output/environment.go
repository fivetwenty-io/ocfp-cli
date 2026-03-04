package output

import (
	"io"
	"os"

	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"golang.org/x/term"
)

// Environment describes the execution environment for output rendering.
// It detects terminal capabilities, CI systems, and piped execution contexts.
type Environment struct {
	// IsTTY indicates whether output is connected to a terminal.
	IsTTY bool

	// IsCI indicates whether running in a CI environment.
	IsCI bool

	// IsPiped indicates whether output is being piped to another command.
	IsPiped bool

	// TermWidth is the terminal width in columns (0 if not a TTY).
	TermWidth int

	// SupportsANSI indicates whether ANSI escape codes are supported.
	SupportsANSI bool

	// CIProvider identifies the CI system (e.g., "github", "gitlab", "jenkins").
	CIProvider string
}

// DetectEnvironment analyzes the execution context and returns environment details.
// It checks for TTY capabilities, CI systems, and terminal dimensions.
func DetectEnvironment(w io.Writer) Environment { //nolint:varnamelen
	log := logger.Get()

	env := Environment{
		IsTTY:        false,
		IsCI:         false,
		IsPiped:      false,
		TermWidth:    0,
		SupportsANSI: false,
		CIProvider:   "",
	}

	// Detect TTY and terminal dimensions
	detectTTY(&env, w)

	// Detect CI environment
	env.IsCI, env.CIProvider = detectCI()

	// CI environments typically don't support ANSI unless explicitly enabled
	if env.IsCI {
		// Some CI systems support ANSI colors
		if env.CIProvider == "github" || env.CIProvider == "gitlab" {
			env.SupportsANSI = true
		} else {
			env.SupportsANSI = false
		}
	}

	log.Debugw("Environment detected",
		"is_tty", env.IsTTY,
		"is_ci", env.IsCI,
		"ci_provider", env.CIProvider,
		"is_piped", env.IsPiped,
		"term_width", env.TermWidth,
		"supports_ansi", env.SupportsANSI,
	)

	return env
}

// detectTTY detects TTY capabilities and terminal dimensions.
func detectTTY(env *Environment, w io.Writer) {
	file, ok := w.(*os.File)
	if !ok {
		return
	}

	if !term.IsTerminal(int(file.Fd())) { //nolint:gosec // file descriptor fits in int
		env.IsPiped = true

		return
	}

	env.IsTTY = true
	env.SupportsANSI = true

	width, _, err := term.GetSize(int(file.Fd())) //nolint:gosec // file descriptor fits in int
	if err == nil {
		env.TermWidth = width
	} else {
		env.TermWidth = 80
	}
}

// detectCI checks for common CI environment variables and returns provider information.
func detectCI() (bool, string) {
	// Check for common CI environment variables
	ciChecks := map[string]string{
		"GITHUB_ACTIONS": "github",
		"GITLAB_CI":      "gitlab",
		"JENKINS_URL":    "jenkins",
		"CIRCLECI":       "circleci",
		"TRAVIS":         "travis",
		"BUILDKITE":      "buildkite",
		"CI":             "generic", // Generic CI indicator
	}

	for envVar, provider := range ciChecks {
		if os.Getenv(envVar) != "" {
			// Return the most specific provider found (not generic)
			if provider != "generic" {
				return true, provider
			}
		}
	}

	// If only generic CI was found
	if os.Getenv("CI") != "" {
		return true, "generic"
	}

	return false, ""
}

// DefaultMode returns the recommended output mode based on the environment.
// Interactive mode for TTY, concise for CI/piped contexts.
func (e Environment) DefaultMode() Mode {
	log := logger.Get()

	var mode Mode

	switch {
	case e.IsTTY && !e.IsCI:
		// Interactive terminal, use rich output
		mode = ModeInteractive

		log.Debugw("Selected interactive mode", "reason", "tty_detected")

	case e.IsCI:
		// CI environment, use concise output
		mode = ModeConcise

		log.Debugw("Selected concise mode", "reason", "ci_detected", "provider", e.CIProvider)

	case e.IsPiped:
		// Piped output, use concise
		mode = ModeConcise

		log.Debugw("Selected concise mode", "reason", "piped_output")

	default:
		// Fallback to concise for unknown contexts
		mode = ModeConcise

		log.Debugw("Selected concise mode", "reason", "default_fallback")
	}

	return mode
}
