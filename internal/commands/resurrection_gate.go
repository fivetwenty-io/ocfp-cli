package commands

import (
	"context"
	"fmt"

	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// WithResurrectionGate wraps deployFn by toggling the BOSH director's
// per-deployment resurrection flag off before the deploy and back on
// after (via defer). This prevents the Health Monitor's resurrector from
// interfering with in-flight instance replacements during a deploy.
//
// # Why two resurrection controls exist
//
// The HM resurrector is disabled lab-wide by the hm-tuning.yml ops file
// (resurrector_enabled: false), which is baked into the director VM.
// The per-deployment CLI toggle — bosh update-resurrection on/off — is a
// separate flag that survives director restarts and is honoured by bosh
// recreate and the scan-and-fix plugin. Wrapping the deploy with off/on
// ensures that any recreates triggered during the deploy also run without
// resurrection interference, and restores the operator-friendly default
// on both success and failure.
//
// # SIGKILL caveat
//
// If the process is killed with SIGKILL while deployFn is running, the
// deferred restoration of resurrection does not execute and the flag
// remains off. Operators must check `bosh resurrection` status at the
// start of any subsequent deploy. Integration harnesses should assert
// this state on start-of-run.
//
// # Error handling
//
// Errors from runBosh("update-resurrection", "off") and the deferred
// runBosh("update-resurrection", "on") are WARNING-only and do not abort
// the deploy or mask deployFn's error. deployFn is always called even
// when the "off" toggle fails, because the deploy itself is the
// load-bearing operation.
func WithResurrectionGate(ctx context.Context, runBosh func(args ...string) error, deployFn func() error) error {
	log := logger.Get()

	// Toggle resurrection off; warn on failure but proceed — deployFn is load-bearing.
	err := runBosh("update-resurrection", "off")
	if err != nil {
		log.Warnw("update-resurrection off failed; continuing with deploy", "error", err)
	}

	// Restore resurrection on regardless of deployFn outcome.
	defer func() {
		err := runBosh("update-resurrection", "on")
		if err != nil {
			log.Warnw("update-resurrection on failed; check 'bosh resurrection' before next deploy", "error", err)
		}
	}()

	// ctx is threaded through for callers that need cancellation awareness,
	// but the gate itself is not context-sensitive — BOSH CLI invocations
	// are synchronous and context cancellation is the caller's concern.
	_ = ctx

	err = deployFn()
	if err != nil {
		return fmt.Errorf("deploy failed: %w", err)
	}

	return nil
}
