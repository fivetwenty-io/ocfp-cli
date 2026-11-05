package bastion

import "context"

// syncShellPathOrder runs the shell and system environment scripts, which
// keep PATH in order, on every init. Like the drop-in sync, it has to reach
// a bastion that is already provisioned, because runModeOrPhases returns
// before the environment phases there and an older bastion would never get
// the ordering. Both scripts are idempotent, so a fresh bastion that runs
// the phases afterwards just finds nothing left to change. A failure only
// warns, because the ordering is a convenience and must not block the
// narrow modes.
func (m *Manager) syncShellPathOrder(ctx context.Context) {
	if m.options != nil && m.options.DryRun {
		m.log.Infow("DRY RUN: Would converge the shell PATH order on the bastion")

		return
	}

	err := m.setupShellEnvironment(ctx)
	if err != nil {
		m.log.Warnw("Could not converge the shell PATH order, continuing without it",
			"error", err)
	}

	// The system script gets OCFP_BLOC from the same preamble the phases use,
	// and it runs even when the shell script failed.
	err = m.setupSystemEnvironment(ctx)
	if err != nil {
		m.log.Warnw("Could not converge the system PATH order, continuing without it",
			"error", err)
	}
}
