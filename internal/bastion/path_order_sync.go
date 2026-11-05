package bastion

import "context"

// syncShellPathOrder runs the shell environment script on every init. Like
// the drop-in sync, it has to reach a bastion that is already provisioned,
// because runModeOrPhases returns before the shell_environment phase there
// and an older bastion would never get the ordering. The script is
// idempotent, so a fresh bastion that runs the phase afterwards just finds
// nothing left to change. A failure only warns, because the ordering is a
// convenience and must not block the narrow modes.
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
}
