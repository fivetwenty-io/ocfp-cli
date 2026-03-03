package output

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

var (
	// ErrNotImplemented is returned by placeholder renderer methods.
	ErrNotImplemented = errors.New("renderer not implemented")

	// ErrInvalidMode is returned when an invalid mode string is parsed.
	ErrInvalidMode = errors.New("invalid output mode")

	// ErrNoActivePhase is returned when a progress update occurs without an active phase.
	ErrNoActivePhase = errors.New("no active phase for progress update")
)

// NewRenderer creates a new Renderer instance for the specified mode and output writer.
// It validates the mode and returns an error if the mode is unsupported.
//nolint:ireturn // returns interface by design for polymorphic renderers
func NewRenderer(w io.Writer, mode Mode) (Renderer, error) { //nolint:varnamelen
	log := logger.Get()

	// Validate mode
	switch mode {
	case ModeInteractive, ModeConcise, ModeJSON, ModeYAML:
		// Valid modes
	default:
		return nil, fmt.Errorf("%w: %d", ErrInvalidMode, mode)
	}

	log.Infow("Creating renderer",
		"mode", mode.String(),
		"writer_type", fmt.Sprintf("%T", w),
	)

	// Return appropriate renderer implementation
	switch mode {
	case ModeInteractive:
		return NewInteractiveRenderer(w), nil
	case ModeConcise:
		return NewConciseRenderer(w), nil
	case ModeJSON:
		return NewJSONRenderer(w), nil
	case ModeYAML:
		return NewYAMLRenderer(w), nil
	default:
		// Should not reach here due to earlier validation
		return nil, fmt.Errorf("%w: %d", ErrInvalidMode, mode)
	}
}

// ParseMode converts a string representation to a Mode constant.
// Valid values: "interactive", "concise", "json", "yaml".
func ParseMode(s string) (Mode, error) { //nolint:varnamelen
	normalized := strings.ToLower(strings.TrimSpace(s))

	switch normalized {
	case "interactive", "i":
		return ModeInteractive, nil
	case "concise", "c":
		return ModeConcise, nil
	case "json", "j":
		return ModeJSON, nil
	case "yaml", "yml", "y":
		return ModeYAML, nil
	default:
		return 0, fmt.Errorf("%w: %s (valid: interactive, concise, json, yaml)", ErrInvalidMode, s)
	}
}

