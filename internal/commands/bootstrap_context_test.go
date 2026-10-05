package commands

import (
	"context"
	"errors"
	"testing"
)

// TestRunBootstrapForBlocs_StopsOnCancelledContext covers the interrupt path:
// once the signal-aware root context is cancelled, the bloc loop returns the
// cancellation instead of starting (or continuing into) another bloc. The
// config path does not exist, so any bloc that did start would fail with a
// config error rather than context.Canceled.
func TestRunBootstrapForBlocs_StopsOnCancelledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := runBootstrapForBlocs(ctx, "/nonexistent/ocfp.yml", []string{"alpha", "beta"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runBootstrapForBlocs() error = %v, want context.Canceled", err)
	}
}
