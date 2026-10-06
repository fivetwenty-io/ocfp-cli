package commands

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"go.uber.org/zap"
)

// inceptionPortEnvVar is the override operators use to move a bloc's
// inception vault to another API port, which also moves its cluster port.
const inceptionPortEnvVar = "OCFP_VAULT_INCEPTION_PORT"

// vaultArchiveTimeFormat stamps the directories a superseded vault moves to.
const vaultArchiveTimeFormat = "20060102-150405"

// inceptionLockFileName is the per-bloc lock that serialises every ocfp run
// that starts, migrates, or tears down the bloc's inception vault.
const inceptionLockFileName = "inception-vault.lock"

// inceptionLockTimeout bounds how long a run waits for another run on the
// same bloc. It covers a migration, which may take several minutes.
var inceptionLockTimeout = 5 * time.Minute //nolint:gochecknoglobals // tests shorten the wait

// withInceptionVaultLock runs fn while holding the bloc's inception vault
// lock. Two runs for one bloc would otherwise stop each other's vault, or
// migrate a store the other is starting on; the second waits, and usually
// then finds the vault healthy and leaves it running.
func withInceptionVaultLock(paths map[string]string, fn func() error) error {
	return config.WithFileLock(paths["lockFile"], inceptionLockTimeout, fn)
}

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

	// ErrRestartAfterMigrationFailed reports a vault that did not reopen
	// right after its storage was migrated. Nothing is archived then: the
	// keys opened the file store moments earlier, so a person has to look.
	ErrRestartAfterMigrationFailed = errors.New("the inception vault did not restart after its migration to raft")
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
	// migrate copies a stopped file vault into raft storage, swaps the raft
	// store into place, and returns where the file store was kept.
	migrate func(ctx context.Context, paths map[string]string, tools inceptionTools, log *zap.SugaredLogger) (string, error)
	// recoverKeys writes whichever key files are missing from what the bloc's
	// running vault left behind, and never replaces a key file that exists.
	recoverKeys func(ctx context.Context, paths map[string]string, log *zap.SugaredLogger) error
	// targetToken returns the root token safe holds for the bloc's own
	// target, read before a stop deletes that target.
	targetToken func(paths map[string]string) string
	// now stamps archive names.
	now func() time.Time
}

// inceptionRun is one pass of reconcileInceptionVault over one bloc.
type inceptionRun struct {
	paths map[string]string
	tools inceptionTools
	steps inceptionSteps
	log   *zap.SugaredLogger

	// targetToken is the root token safe held for the bloc's target before
	// the stop deleted the target, and targetTokenFile is the file that has
	// held it since. Both are empty when safe had no such target.
	targetToken     string
	targetTokenFile string
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

	found, err := run.preflight(ctx)
	if err != nil {
		return err
	}

	err = requireReadableKeyFiles(paths)
	if err != nil {
		return err
	}

	err = repairUnsealKeyFile(ctx, paths, run.log)
	if err != nil {
		return err
	}

	err = canonicalizeRootKeyFile(paths, run.log)
	if err != nil {
		return err
	}

	if found.healthy {
		return run.keepHealthy(ctx)
	}

	// The log and safe's target hold the keys of a vault that runs now, so
	// they are recovered before the stop. Without both keys the vault goes
	// down the archive path below and is never migrated.
	keys, err := inceptionKeysUsable(paths)
	if err != nil {
		return err
	}

	if found.running && !keys {
		err = run.steps.recoverKeys(ctx, paths, run.log)
		if err != nil {
			return fmt.Errorf("failed to recover the running inception vault's keys: %w", err)
		}
	}

	// Stopping deletes the bloc's safe target, which may hold the only copy
	// of the root token, whether or not the vault is running now.
	run.targetToken = run.steps.targetToken(paths)

	run.targetTokenFile, err = preserveTargetToken(paths, run.targetToken, run.steps.now(), run.log)
	if err != nil {
		return fmt.Errorf("failed to keep the root token from safe's target before stopping the inception vault: %w", err)
	}

	err = run.steps.stop(ctx, paths, run.log)
	if err != nil {
		return fmt.Errorf("failed to stop the inception vault before starting it: %w", err)
	}

	err = run.steps.clusterPortFree(ctx, paths["clusterPort"])
	if err != nil {
		return fmt.Errorf("%w: cluster port %s (API port %s plus 1000): %w; free it, or set %s to move both ports",
			ErrInceptionClusterPortTaken, paths["clusterPort"], paths["port"], err, inceptionPortEnvVar)
	}

	if found.journal != nil {
		return run.resumeMigration(ctx, found.journal)
	}

	return run.startFromDisk(ctx)
}

// preflightFindings is what preflight learned about the bloc.
type preflightFindings struct {
	// journal is the migration in flight, if any.
	journal *migrationJournal
	// running reports that this bloc's own vault answers on the port.
	running bool
	// healthy reports that the running vault can be left as it is.
	healthy bool
}

// preflight runs every check that can refuse before anything is stopped or
// moved, and reports what it found.
func (run *inceptionRun) preflight(ctx context.Context) (preflightFindings, error) {
	paths := run.paths

	probe := run.steps.probe(ctx, "http://127.0.0.1:"+paths["port"])
	if probe.state == vaultProbeStranger {
		return preflightFindings{}, inceptionPortTakenError(paths, "something that is not a vault answers there")
	}

	journal, err := readMigrationJournal(paths["vaultDir"])
	if err != nil {
		return preflightFindings{}, err
	}

	data := classifyVaultData(paths["vaultDir"])
	if data == vaultDataMixed {
		return preflightFindings{}, fmt.Errorf("%w: %s; inspect it by hand, ocfp will not touch it", ErrVaultDataMixed, paths["vaultDir"])
	}

	if journal != nil && journal.Phase == migratePhaseMigrating && data != vaultDataFile {
		return preflightFindings{}, fmt.Errorf("%w: it records an unfinished copy, but %s is %s storage rather than file; "+
			"inspect it by hand", ErrMigrationJournalInvalid, paths["vaultDir"]+migrationJournalSuffix, data)
	}

	if probe.state != vaultProbeVault {
		return preflightFindings{journal: journal}, nil
	}

	owned, err := run.steps.ownsVault(ctx, paths, data)
	if err != nil {
		return preflightFindings{}, err
	}

	if !owned {
		return preflightFindings{}, inceptionPortTakenError(paths, "a vault answers there that does not hold this bloc's data")
	}

	return preflightFindings{
		journal: journal,
		running: true,
		healthy: journal == nil && run.isHealthy(ctx, probe, data),
	}, nil
}

// resumeMigration picks up a migration that an earlier run left part way.
// An unfinished copy never touched the data, so it is set aside and the
// migration runs again. An unfinished swap is completed and the vault is
// restarted, and its missing data directory is never read as an empty bloc.
func (run *inceptionRun) resumeMigration(ctx context.Context, journal *migrationJournal) error {
	paths := run.paths

	if journal.Phase == migratePhaseMigrating {
		err := abandonInterruptedCopy(paths, journal, run.log)
		if err != nil {
			return err
		}

		return run.startFromDisk(ctx)
	}

	backup, err := finishInterruptedSwap(paths, journal, run.log)
	if err != nil {
		return err
	}

	keys, err := inceptionKeysUsable(paths)
	if err != nil {
		return err
	}

	if !keys {
		return fmt.Errorf("%w: the raft data is in %s and the file store it came from is kept in %s, "+
			"but %s and %s do not hold both keys", ErrRestartAfterMigrationFailed, paths["vaultDir"], backup,
			paths["rootKeyFile"], paths["unsealKeysFile"])
	}

	return run.restartAfterMigration(ctx, backup)
}

// inceptionPortTakenError says why the API port cannot be used.
func inceptionPortTakenError(paths map[string]string, why string) error {
	return fmt.Errorf("%w: port %s: %s; free it, or set %s to another port",
		ErrInceptionPortTaken, paths["port"], why, inceptionPortEnvVar)
}

// guardInceptionTeardown refuses a teardown that would stop something other
// than this bloc's own vault. Teardown kills whatever listens on the bloc's
// API port, and derived ports can collide, so the listener must be this
// bloc's vault by the same evidence startup uses before it stops one.
func guardInceptionTeardown(
	ctx context.Context, paths map[string]string,
	probe func(ctx context.Context, addr string) vaultProbe,
	owns func(ctx context.Context, paths map[string]string, data vaultDataState) (bool, error),
) error {
	found := probe(ctx, "http://127.0.0.1:"+paths["port"])

	switch found.state {
	case vaultProbeStopped:
		return nil
	case vaultProbeStranger:
		return inceptionPortTakenError(paths, "something that is not a vault answers there, so teardown leaves it alone")
	case vaultProbeVault:
	}

	owned, err := owns(ctx, paths, classifyVaultData(paths["vaultDir"]))
	if err != nil {
		return err
	}

	if !owned {
		return inceptionPortTakenError(paths,
			"a vault answers there that this bloc cannot prove is its own, so teardown leaves it alone; "+
				"stop it by hand if it is this bloc's")
	}

	return nil
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
//
// A vault whose keys are not both saved is not healthy, however well it runs:
// once it stops it cannot be reopened. Its keys are recovered while it runs,
// and when that fails the run fails with the vault left running.
func (run *inceptionRun) keepHealthy(ctx context.Context) error {
	paths := run.paths

	registered := run.steps.targetRegistered(ctx, paths)
	if !registered {
		token, err := keyFileHasValue(paths["rootKeyFile"])
		if err != nil {
			return err
		}

		if !token {
			return fmt.Errorf("%w: safe has no %s target and %s holds no root token; the vault at %s keeps running",
				ErrInceptionTargetLost, paths["vaultName"], paths["rootKeyFile"], "http://127.0.0.1:"+paths["port"])
		}
	}

	err := run.requireSavedKeys(ctx)
	if err != nil {
		return err
	}

	if registered {
		run.log.Info("Inception vault is already running and accessible")
		printVaultInfo(paths, run.log)

		return nil
	}

	err = run.steps.retarget(ctx, paths, run.log)
	if err != nil {
		return fmt.Errorf("failed to re-target the running inception vault: %w", err)
	}

	run.log.Info("Inception vault is already running; its safe target was registered again")
	printVaultInfo(paths, run.log)

	return nil
}

// requireSavedKeys makes sure a running vault's keys are both saved in a
// valid shape, recovering whichever is missing from what the vault left
// behind, and fails with ErrInceptionKeysNotSaved when they still are not.
func (run *inceptionRun) requireSavedKeys(ctx context.Context) error {
	paths := run.paths

	saved, err := inceptionKeysSaved(paths)
	if err != nil || saved {
		return err
	}

	err = run.steps.recoverKeys(ctx, paths, run.log)
	if err != nil {
		return fmt.Errorf("failed to recover the running inception vault's keys: %w", err)
	}

	saved, err = inceptionKeysSaved(paths)
	if err != nil || saved {
		return err
	}

	run.log.Errorw("The running inception vault's keys are not both saved; it cannot be reopened after it stops",
		"root_token", paths["rootKeyFile"], "unseal_key", paths["unsealKeysFile"])

	return fmt.Errorf("%w: the vault at %s is still running, but %s and %s do not both hold a valid key; "+
		"its full keys are in the vault log at %s", ErrInceptionKeysNotSaved, "http://127.0.0.1:"+paths["port"],
		paths["rootKeyFile"], paths["unsealKeysFile"], paths["logFile"])
}

// startFromDisk starts a stopped vault from what its data directory and key
// files hold.
func (run *inceptionRun) startFromDisk(ctx context.Context) error {
	paths := run.paths
	data := classifyVaultData(paths["vaultDir"])

	if data == vaultDataRaft || data == vaultDataFile {
		err := recoverUnsealKeyFromLogs(paths, run.log)
		if err != nil {
			return err
		}
	}

	keys, err := inceptionKeysUsable(paths)
	if err != nil {
		return err
	}

	anyKey, err := anyInceptionKeyPresent(paths)
	if err != nil {
		return err
	}

	switch {
	case data == vaultDataMixed:
		return fmt.Errorf("%w: %s; inspect it by hand, ocfp will not touch it", ErrVaultDataMixed, paths["vaultDir"])
	case data == vaultDataRaft && keys:
		return run.restart(ctx)
	case data == vaultDataFile && keys:
		return run.migrateAndRestart(ctx)
	case data == vaultDataAbsent && !anyKey:
		return run.fresh(ctx)
	case data == vaultDataAbsent:
		return run.archiveAndStartFresh(ctx, "key files were left with no vault data")
	default:
		return run.archiveAndStartFresh(ctx, "the vault data has no usable root token and unseal key to reopen it")
	}
}

// migrateAndRestart moves a stopped file vault onto raft storage and reopens
// it with the same keys. A restart that fails right after the migration stops
// the vault and returns an error naming both directories, and never archives.
func (run *inceptionRun) migrateAndRestart(ctx context.Context) error {
	paths := run.paths

	backup, err := run.steps.migrate(ctx, paths, run.tools, run.log)
	if err != nil {
		return err
	}

	return run.restartAfterMigration(ctx, backup)
}

// restartAfterMigration reopens a vault whose storage was just moved to raft.
// The keys opened the file store moments earlier, so a failure here is the
// migration's and not the keys': the vault is stopped and an error names both
// directories, and nothing is archived.
func (run *inceptionRun) restartAfterMigration(ctx context.Context, backup string) error {
	paths := run.paths

	err := run.steps.start(ctx, paths, run.tools, safeLocalRestart, run.log)
	if err == nil {
		return run.steps.finish(ctx, paths, safeLocalRestart, run.log)
	}

	err = fmt.Errorf("%w: the raft data is in %s and the file store it came from is kept in %s, "+
		"and neither was changed after the migration: %w", ErrRestartAfterMigrationFailed, paths["vaultDir"], backup, err)

	return errors.Join(err, wrapStopAfterFailure(run.steps.stop(ctx, paths, run.log)))
}

// restart reopens the vault with its saved keys. Only the engine refusing a
// key leads to an archive. Any other failure says nothing about the keys, so
// it stops what was started and leaves the data and keys where they are.
//
// When the engine refuses the token in root.key and safe's target held a
// different one before the stop, the vault is reopened once with that token
// before anything is archived.
func (run *inceptionRun) restart(ctx context.Context) error {
	paths := run.paths

	run.log.Infow("Restarting the inception vault with its saved keys", "data", paths["vaultDir"])

	err := run.steps.start(ctx, paths, run.tools, safeLocalRestart, run.log)
	if err == nil {
		return run.steps.finish(ctx, paths, safeLocalRestart, run.log)
	}

	archive, err := run.stopAfterFailedRestart(ctx, err)
	if !archive {
		return err
	}

	tokenFile := run.retryTokenFile(err)
	if tokenFile != "" {
		retryErr := run.restartWithTargetToken(ctx, tokenFile)
		if retryErr == nil {
			return nil
		}

		archive, retryErr = run.stopAfterFailedRestart(ctx, retryErr)
		if !archive {
			return retryErr
		}

		err = fmt.Errorf("%w; the token from safe's target, kept in %s, was refused too: %w", err, tokenFile, retryErr)
	}

	return run.archiveAndStartFresh(ctx, err.Error())
}

// stopAfterFailedRestart stops a vault that failed to restart, and reports
// whether the failure allows an archive: only a refused key does, and only
// once the vault is known to be down.
func (run *inceptionRun) stopAfterFailedRestart(ctx context.Context, startErr error) (bool, error) {
	paths := run.paths
	stopErr := run.steps.stop(ctx, paths, run.log)

	if !errors.Is(startErr, ErrVaultKeysRejected) {
		err := fmt.Errorf("failed to restart the inception vault, and its data in %s and its keys were left as they were: %w",
			paths["vaultDir"], startErr)

		return false, errors.Join(err, wrapStopAfterFailure(stopErr))
	}

	if stopErr != nil {
		return false, fmt.Errorf("the saved keys were rejected, but the vault did not stop, so nothing was archived: %w",
			errors.Join(startErr, stopErr))
	}

	return true, startErr
}

// retryTokenFile returns the file holding safe's token when a restart that
// failed with err is worth one more try with it: the engine refused the root
// token, and safe's target held a different, token-shaped one.
func (run *inceptionRun) retryTokenFile(err error) string {
	if !errors.Is(err, ErrVaultRootTokenRejected) || run.targetTokenFile == "" ||
		run.targetTokenFile == run.paths["rootKeyFile"] || checkRootToken(run.targetToken) != nil {
		return ""
	}

	return run.targetTokenFile
}

// restartWithTargetToken reopens the vault with the token kept in tokenFile.
// When the engine takes it, root.key is made to hold it, with the refused
// token moved aside, and the vault is targeted as after any restart.
func (run *inceptionRun) restartWithTargetToken(ctx context.Context, tokenFile string) error {
	paths := run.paths

	run.log.Warnw("The engine refused the root token in root.key; trying once more with the token safe's target held",
		"root_key", paths["rootKeyFile"], "token_file", tokenFile)

	retry := maps.Clone(paths)
	retry["rootKeyFile"] = tokenFile

	err := run.steps.start(ctx, retry, run.tools, safeLocalRestart, run.log)
	if err != nil {
		return err
	}

	err = promoteTargetToken(paths, run.targetToken, run.steps.now(), run.log)
	if err != nil {
		return fmt.Errorf("the inception vault reopened with the root token kept in %s and is running, "+
			"but %s could not be updated to hold it: %w", tokenFile, paths["rootKeyFile"], err)
	}

	return run.steps.finish(ctx, paths, safeLocalRestart, run.log)
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

// readKeyFileBytes returns exactly what a key file holds. A file that does
// not exist, or that is empty, holds nothing, and an empty file counts as
// empty even when its mode would keep it from being read. A file that exists
// with content but cannot be read is ErrInceptionKeyFileUnreadable, because
// it may hold the only copy of its key: treating it as missing would archive
// the vault or write a recovered key over it.
func readKeyFileBytes(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, keyFileUnreadableError(path, err)
	}

	if info.Mode().IsRegular() && info.Size() == 0 {
		return nil, nil
	}

	data, err := os.ReadFile(path) // #nosec G304 -- path is the bloc's own key file from getVaultInceptionPaths()
	if err != nil {
		return nil, keyFileUnreadableError(path, err)
	}

	return data, nil
}

// readKeyFile returns what a key file holds with surrounding whitespace
// trimmed, or "" when it is missing or blank.
func readKeyFile(path string) (string, error) {
	data, err := readKeyFileBytes(path)
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(data)), nil
}

// keyFileUnreadableError names the key file and why it could not be read,
// never what it holds.
func keyFileUnreadableError(path string, err error) error {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		err = pathErr.Err
	}

	return fmt.Errorf("%w: %s: %w; make it readable by this user and run this again",
		ErrInceptionKeyFileUnreadable, path, err)
}

// keyFileHasValue reports whether path holds anything besides whitespace.
// safe refuses an empty token file, and an empty unseal key opens nothing.
func keyFileHasValue(path string) (bool, error) {
	value, err := readKeyFile(path)

	return value != "", err
}

// requireReadableKeyFiles refuses a run whose key files cannot be read,
// before anything is stopped or moved.
func requireReadableKeyFiles(paths map[string]string) error {
	for _, keyFile := range []string{paths["rootKeyFile"], paths["unsealKeysFile"]} {
		_, err := readKeyFileBytes(keyFile)
		if err != nil {
			return fmt.Errorf("%w; nothing was started, stopped, or changed", err)
		}
	}

	return nil
}

// inceptionKeysUsable reports whether the bloc has both keys a restart needs.
// The legacy and test layouts keep one file for both, and that file ends up
// holding only the root token, so a single shared file is never a pair.
func inceptionKeysUsable(paths map[string]string) (bool, error) {
	if paths["rootKeyFile"] == paths["unsealKeysFile"] {
		return false, nil
	}

	root, err := keyFileHasValue(paths["rootKeyFile"])
	if err != nil {
		return false, err
	}

	unseal, err := keyFileHasValue(paths["unsealKeysFile"])
	if err != nil {
		return false, err
	}

	return root && unseal, nil
}

// inceptionKeysSaved reports whether both key files hold a key of the right
// shape, which is what a vault needs to be reopened after it stops.
func inceptionKeysSaved(paths map[string]string) (bool, error) {
	if paths["rootKeyFile"] == paths["unsealKeysFile"] {
		return false, nil
	}

	root, err := readKeyFile(paths["rootKeyFile"])
	if err != nil {
		return false, err
	}

	unseal, err := readKeyFile(paths["unsealKeysFile"])
	if err != nil {
		return false, err
	}

	return checkRootToken(root) == nil && checkSealKey(unseal) == nil, nil
}

// anyInceptionKeyPresent reports whether either key file exists. A lookup
// that fails for a reason other than absence is an error, because it says
// nothing about the key.
func anyInceptionKeyPresent(paths map[string]string) (bool, error) {
	for _, keyFile := range []string{paths["rootKeyFile"], paths["unsealKeysFile"]} {
		_, err := os.Lstat(keyFile)
		if err == nil {
			return true, nil
		}

		if !errors.Is(err, fs.ErrNotExist) {
			return false, keyFileUnreadableError(keyFile, err)
		}
	}

	return false, nil
}
