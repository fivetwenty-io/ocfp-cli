package vault

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// localInceptionVaultPort is the fixed port the workstation-local inception
// vault listens on (see getVaultInceptionPaths in internal/commands).
const localInceptionVaultPort = "8234"

// runLocalCommand is the subprocess seam for local teardown operations.
// Tests override it to record invocations without spawning processes.
//
//nolint:gochecknoglobals // intentional seam for testing, mirrors sleepFn
var runLocalCommand = func(ctx context.Context, name string, args ...string) error {
	// #nosec G204 - callers pass fixed binaries (tmux/pkill/safe) and a
	// session/target name validated by isValidTmuxSession
	return exec.CommandContext(ctx, name, args...).Run()
}

// TeardownLocalInception decommissions the inception vault running on the
// host executing this process. It is called by `ocfp init bastion` (which
// runs on the operator workstation) once the bastion-side inception vault
// is up, populated, and verified — from that point the workstation vault is
// obsolete and its tmux session would otherwise linger forever, since
// `ocfp vault migrate` runs on the bastion and can never reach it.
//
// It stops the tmux session, kills local `safe local` proxy processes on
// the inception port, and deletes the now-stale local safe target. It
// deliberately does NOT remove the vault data directory: that data is the
// last local copy of bootstrap-era secrets and is kept as a snapshot.
//
// All steps are best-effort; a session or target that is already gone is
// not an error.
func TeardownLocalInception(ctx context.Context, blocName string) error {
	session := "inception-vault"
	target := "inception"

	if blocName != "" {
		session = blocName + "-inception-vault"
		target = blocName + "-inception"
	}

	if !isValidTmuxSession(session) {
		return fmt.Errorf("%w: %s", ErrInvalidTmuxSession, session)
	}

	log := logger.Get()
	log.Infow("Decommissioning local inception vault", "session", session, "target", target)

	//nolint:noinlineerr // errors are passed to the logger for context
	if err := runLocalCommand(ctx, "tmux", "kill-session", "-t", session); err != nil {
		log.Debugw("Local inception vault tmux session not running", "session", session, "error", err)
	}

	//nolint:noinlineerr // errors are passed to the logger for context
	if err := runLocalCommand(ctx, "pkill", "-f", "safe local.*--port "+localInceptionVaultPort); err != nil {
		log.Debugw("No local safe processes to kill", "port", localInceptionVaultPort, "error", err)
	}

	//nolint:noinlineerr // errors are passed to the logger for context
	if err := runLocalCommand(ctx, "safe", "target", "delete", target); err != nil {
		log.Debugw("Local safe target not present", "target", target, "error", err)
	}

	log.Infow("Local inception vault decommissioned", "session", session)

	return nil
}
