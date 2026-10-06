package commands

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"go.uber.org/zap"
)

// inceptionPortEnvVar is the override operators use to move a bloc's
// inception vault to another API port, which also moves its cluster port.
const inceptionPortEnvVar = "OCFP_VAULT_INCEPTION_PORT"

// vaultArchiveTimeFormat stamps the directories a superseded vault moves to.
const vaultArchiveTimeFormat = "20060102-150405"

var (
	// ErrInceptionPortTaken reports an API port held by something other than
	// this bloc's inception vault.
	ErrInceptionPortTaken = errors.New("the inception vault port is taken")

	// ErrInceptionClusterPortTaken reports a cluster port that is already in
	// use, so a vault started now would fail to bind it.
	ErrInceptionClusterPortTaken = errors.New("the inception vault cluster port is taken")

	// ErrVaultDataMixed reports a data directory that holds both raft and
	// file storage, which only a person can sort out.
	ErrVaultDataMixed = errors.New("the inception vault data holds both raft and file storage")

	// ErrInceptionTargetLost reports a healthy vault that safe has no target
	// for and that ocfp has no root token to re-target.
	ErrInceptionTargetLost = errors.New("the inception vault is running but cannot be re-targeted")

	// ErrFileVaultNotMigrated reports a file-backed vault that this ocfp
	// leaves as it is rather than restarting it on raft.
	ErrFileVaultNotMigrated = errors.New("the inception vault still uses file storage")
)

// inceptionSteps are the actions reconcileInceptionVault takes. Production
// code wires the real ones through newInceptionSteps, and tests script them,
// so every path through the state machine can be driven without tmux, safe,
// or an engine while the data directory and keys stay real files on disk.
type inceptionSteps struct {
	// probe asks the API port for seal status.
	probe func(ctx context.Context, addr string) vaultProbe
	// hasSession reports whether the bloc's tmux session exists.
	hasSession func(ctx context.Context, paths map[string]string) bool
	// ownsVault reports whether the vault answering on the port is this
	// bloc's own, as opposed to a sibling's that landed on the same port.
	ownsVault func(ctx context.Context, paths map[string]string, data vaultDataState) (bool, error)
	// targetRegistered reports whether safe has the bloc's target.
	targetRegistered func(ctx context.Context, paths map[string]string) bool
	// retarget registers the bloc's target again and authenticates it.
	retarget func(ctx context.Context, paths map[string]string, log *zap.SugaredLogger) error
	// stop stops the bloc's vault and waits until the port and data are free.
	stop func(ctx context.Context, paths map[string]string, log *zap.SugaredLogger) error
	// clusterPortFree reports why the cluster port cannot be bound, if it
	// cannot.
	clusterPortFree func(ctx context.Context, port string) error
	// start launches safe local in the given mode and waits until it is ready.
	start func(ctx context.Context, paths map[string]string, tools inceptionTools, mode safeLocalMode,
		log *zap.SugaredLogger) error
	// finish targets the started vault and, for a new vault, saves its keys.
	finish func(ctx context.Context, paths map[string]string, mode safeLocalMode, log *zap.SugaredLogger) error
	// now stamps archive names.
	now func() time.Time
}

// inceptionRun is one pass of reconcileInceptionVault over one bloc.
type inceptionRun struct {
	paths map[string]string
	tools inceptionTools
	steps inceptionSteps
	log   *zap.SugaredLogger
}

// reconcileInceptionVault brings a bloc's inception vault from whatever state
// it is in to a running raft vault, changing as little on disk as it can.
//
// Every check that can refuse runs before anything is stopped or moved: a
// stranger on the port, someone else's vault on the port, a data directory
// holding both kinds of storage, and a cluster port in use all return an
// error with the disk exactly as it was. A healthy vault is left running. A
// vault with its data and both keys is restarted in place. Only missing keys,
// or keys the engine itself refused, lead to an archive, and the archive
// renames the old vault aside rather than deleting anything.
func reconcileInceptionVault(ctx context.Context, run *inceptionRun) error {
	paths := run.paths

	probe := run.steps.probe(ctx, "http://127.0.0.1:"+paths["port"])
	if probe.state == vaultProbeStranger {
		return inceptionPortTakenError(paths, "something that is not a vault answers there")
	}

	data := classifyVaultData(paths["vaultDir"])
	if data == vaultDataMixed {
		return fmt.Errorf("%w: %s; inspect it by hand, ocfp will not touch it", ErrVaultDataMixed, paths["vaultDir"])
	}

	if probe.state == vaultProbeVault {
		owned, err := run.steps.ownsVault(ctx, paths, data)
		if err != nil {
			return err
		}

		if !owned {
			return inceptionPortTakenError(paths, "a vault answers there that does not hold this bloc's data")
		}

		if run.isHealthy(ctx, probe, data) {
			return run.keepHealthy(ctx)
		}
	}

	err := run.steps.stop(ctx, paths, run.log)
	if err != nil {
		return fmt.Errorf("failed to stop the inception vault before starting it: %w", err)
	}

	err = run.steps.clusterPortFree(ctx, paths["clusterPort"])
	if err != nil {
		return fmt.Errorf("%w: cluster port %s (API port %s plus 1000): %w; free it, or set %s to move both ports",
			ErrInceptionClusterPortTaken, paths["clusterPort"], paths["port"], err, inceptionPortEnvVar)
	}

	return run.startFromDisk(ctx)
}

// inceptionPortTakenError says why the API port cannot be used.
func inceptionPortTakenError(paths map[string]string, why string) error {
	return fmt.Errorf("%w: port %s: %s; free it, or set %s to another port",
		ErrInceptionPortTaken, paths["port"], why, inceptionPortEnvVar)
}

// isHealthy reports whether the running vault can be left exactly as it is:
// its session exists, and it is an initialized, unsealed vault on raft data.
// An engine that does not report its storage is judged by the data alone.
func (run *inceptionRun) isHealthy(ctx context.Context, probe vaultProbe, data vaultDataState) bool {
	raft := probe.storageType == "raft" || probe.storageType == ""

	return probe.initialized && !probe.sealed && raft && data == vaultDataRaft &&
		run.steps.hasSession(ctx, run.paths)
}

// keepHealthy leaves a healthy vault running. The current safe target is
// global and any sibling bloc can move it, so it proves nothing; the bloc's
// own entry among safe's targets is what must exist. When it is missing it is
// registered again from root.key rather than restarting a working vault.
func (run *inceptionRun) keepHealthy(ctx context.Context) error {
	paths := run.paths

	if run.steps.targetRegistered(ctx, paths) {
		run.log.Info("Inception vault is already running and accessible")
		printVaultInfo(paths, run.log)

		return nil
	}

	if !keyFileHasValue(paths["rootKeyFile"]) {
		return fmt.Errorf("%w: safe has no %s target and %s holds no root token; the vault at %s keeps running",
			ErrInceptionTargetLost, paths["vaultName"], paths["rootKeyFile"], "http://127.0.0.1:"+paths["port"])
	}

	err := run.steps.retarget(ctx, paths, run.log)
	if err != nil {
		return fmt.Errorf("failed to re-target the running inception vault: %w", err)
	}

	run.log.Info("Inception vault is already running; its safe target was registered again")
	printVaultInfo(paths, run.log)

	return nil
}

// startFromDisk starts a stopped vault from what its data directory and key
// files hold.
func (run *inceptionRun) startFromDisk(ctx context.Context) error {
	paths := run.paths
	data := classifyVaultData(paths["vaultDir"])
	keys := inceptionKeysUsable(paths)

	switch {
	case data == vaultDataMixed:
		return fmt.Errorf("%w: %s; inspect it by hand, ocfp will not touch it", ErrVaultDataMixed, paths["vaultDir"])
	case data == vaultDataRaft && keys:
		return run.restart(ctx)
	case data == vaultDataFile && keys:
		return fmt.Errorf("%w: %s was left as it is, with its keys", ErrFileVaultNotMigrated, paths["vaultDir"])
	case data == vaultDataAbsent && !anyInceptionKeyPresent(paths):
		return run.fresh(ctx)
	case data == vaultDataAbsent:
		return run.archiveAndStartFresh(ctx, "key files were left with no vault data")
	default:
		return run.archiveAndStartFresh(ctx, "the vault data has no usable root token and unseal key to reopen it")
	}
}

// restart reopens the vault with its saved keys. Only the engine refusing a
// key leads to an archive. Any other failure says nothing about the keys, so
// it stops what was started and leaves the data and keys where they are.
func (run *inceptionRun) restart(ctx context.Context) error {
	paths := run.paths

	run.log.Infow("Restarting the inception vault with its saved keys", "data", paths["vaultDir"])

	err := run.steps.start(ctx, paths, run.tools, safeLocalRestart, run.log)
	if err == nil {
		return run.steps.finish(ctx, paths, safeLocalRestart, run.log)
	}

	stopErr := run.steps.stop(ctx, paths, run.log)

	if !errors.Is(err, ErrVaultKeysRejected) {
		err = fmt.Errorf("failed to restart the inception vault, and its data in %s and its keys were left as they were: %w",
			paths["vaultDir"], err)

		return errors.Join(err, wrapStopAfterFailure(stopErr))
	}

	if stopErr != nil {
		return fmt.Errorf("the saved keys were rejected, but the vault did not stop, so nothing was archived: %w",
			errors.Join(err, stopErr))
	}

	return run.archiveAndStartFresh(ctx, err.Error())
}

// wrapStopAfterFailure explains a stop that failed while cleaning up after a
// failed start, or returns nil when the stop worked.
func wrapStopAfterFailure(stopErr error) error {
	if stopErr == nil {
		return nil
	}

	return fmt.Errorf("the failed vault may still be running: %w", stopErr)
}

// archiveAndStartFresh moves the stopped vault aside, keeping its data and
// any keys, and starts a new, empty vault in its place. It succeeds with an
// error-level warning, because the bloc now runs on a vault without the old
// secrets and a person has to know where they went.
func (run *inceptionRun) archiveAndStartFresh(ctx context.Context, reason string) error {
	paths := run.paths

	archive, err := archiveAndForgetVault(paths, run.steps.now().Format(vaultArchiveTimeFormat), run.log)
	if err != nil {
		return fmt.Errorf("failed to archive the inception vault (moved so far: %q): %w", archive, err)
	}

	err = run.fresh(ctx)
	if err != nil {
		return fmt.Errorf("the previous inception vault was kept at %s, but a new one did not start: %w", archive, err)
	}

	run.log.Errorw("Started a new, empty inception vault; the previous one was kept, not deleted",
		"reason", reason, "archive", archive)

	return nil
}

// fresh starts a new, empty vault, and stops it again if it fails to start.
func (run *inceptionRun) fresh(ctx context.Context) error {
	paths := run.paths

	err := run.steps.start(ctx, paths, run.tools, safeLocalFresh, run.log)
	if err != nil {
		return errors.Join(fmt.Errorf("failed to start a new inception vault: %w", err),
			wrapStopAfterFailure(run.steps.stop(ctx, paths, run.log)))
	}

	return run.steps.finish(ctx, paths, safeLocalFresh, run.log)
}

// keyFileHasValue reports whether path holds anything besides whitespace.
// safe refuses an empty token file, and an empty unseal key opens nothing.
func keyFileHasValue(path string) bool {
	data, err := os.ReadFile(path) // #nosec G304 -- path is the bloc's own key file from getVaultInceptionPaths()
	if err != nil {
		return false
	}

	return strings.TrimSpace(string(data)) != ""
}

// inceptionKeysUsable reports whether the bloc has both keys a restart needs.
// The legacy and test layouts keep one file for both, and that file ends up
// holding only the root token, so a single shared file is never a pair.
func inceptionKeysUsable(paths map[string]string) bool {
	if paths["rootKeyFile"] == paths["unsealKeysFile"] {
		return false
	}

	return keyFileHasValue(paths["rootKeyFile"]) && keyFileHasValue(paths["unsealKeysFile"])
}

// anyInceptionKeyPresent reports whether either key file exists. A lookup
// that fails for a reason other than absence counts as present, so a key
// that cannot be read is archived rather than reused.
func anyInceptionKeyPresent(paths map[string]string) bool {
	for _, keyFile := range []string{paths["rootKeyFile"], paths["unsealKeysFile"]} {
		_, err := os.Lstat(keyFile)
		if err == nil || !errors.Is(err, fs.ErrNotExist) {
			return true
		}
	}

	return false
}
