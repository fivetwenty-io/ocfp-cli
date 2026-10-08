package commands

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/keyfile"
	"go.uber.org/zap"
)

// inceptionPortEnvVar is the override operators use to move a bloc's
// inception vault to another API port, which also moves its cluster port.
const inceptionPortEnvVar = "OCFP_VAULT_INCEPTION_PORT"

// vaultArchiveTimeFormat stamps the directories a superseded vault moves to.
const vaultArchiveTimeFormat = keyfile.TimestampFormat

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

	// ErrInceptionKeysRefused reports a vault whose saved key the engine
	// refused. The vault still holds its data, so it is stopped and left as
	// it is, and only 'ocfp vault teardown' may replace it.
	ErrInceptionKeysRefused = errors.New("ocfp will not replace an inception vault whose saved key the engine refused")

	// ErrInceptionKeysMissing reports vault data without both keys to reopen
	// it. A key can still be restored from a backup or the vault logs, so the
	// data is left in place, and only 'ocfp vault teardown' may replace it.
	ErrInceptionKeysMissing = errors.New("ocfp will not replace an inception vault whose keys are missing")

	// ErrInceptionDataElsewhere reports key files left without data while a
	// store beside the data directory, kept or moved aside by a migration to
	// raft, may be the vault those keys open.
	ErrInceptionDataElsewhere = errors.New("the inception vault's data directory is gone, but a store beside it may hold its data")
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
	// rootTokenWorks reports why the running vault does not take the token
	// in root.key, if it does not.
	rootTokenWorks func(ctx context.Context, paths map[string]string) error
	// canMigrate reports why the engine cannot migrate file storage to
	// raft, if it cannot.
	canMigrate func(ctx context.Context) error
	// safeTarget reads and sets safe's current target, so that a run that
	// brings back the bloc's existing vault can put back the target that
	// was current before it.
	safeTarget safeCurrentTargetSteps
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

	// createdVault reports that the run started a new, empty vault, whose
	// target stays safe's current one. A new vault that failed to start
	// leaves it false, so the earlier target is put back.
	createdVault bool
}

// reconcileInceptionVault brings a bloc's inception vault from whatever state
// it is in to a running raft vault, changing as little on disk as it can.
//
// Every check that can refuse runs before anything is stopped or moved: a
// stranger on the port, someone else's vault on the port, a data directory
// holding both kinds of storage, and a cluster port in use all return an
// error with the disk exactly as it was, and so do an engine that cannot
// migrate file storage when the run would, and an open vault whose keys
// cannot be saved, or one the run would migrate that does not take the
// token in root.key, which is left running. A healthy vault is left
// running. A vault with its data and both keys is restarted in place. Data
// without both keys, and keys the engine refused, stop the run with the data
// and keys left in place. Only key files left with no data anywhere lead to
// an archive, and the archive renames them aside rather than deleting them.
func reconcileInceptionVault(ctx context.Context, run *inceptionRun) error {
	paths := run.paths

	found, err := run.preflight(ctx)
	if err != nil {
		return err
	}

	err = prepareInceptionKeyFiles(ctx, paths, run.log)
	if err != nil {
		return err
	}

	// Registering the bloc's target again, and starting its vault, both
	// make the bloc's target safe's current one. Whatever was current
	// before is put back however the run ends, unless the run created a new
	// vault, which stays current as it always has.
	earlier := rememberSafeCurrentTarget(ctx, run.steps.safeTarget)
	defer func() {
		if !run.createdVault {
			putBackSafeCurrentTarget(ctx, run.steps.safeTarget, earlier, paths["vaultName"], run.log)
		}
	}()

	if found.healthy {
		return run.keepHealthy(ctx)
	}

	err = run.recoverRunningKeys(ctx, found)
	if err != nil {
		return err
	}

	err = run.requireEngineForMigration(ctx, found)
	if err != nil {
		return err
	}

	err = run.requireWorkingRootToken(ctx, found)
	if err != nil {
		return err
	}

	// Stopping deletes the bloc's safe target, which may hold the only copy
	// of the root token, whether or not the vault is running now.
	run.targetToken = run.steps.targetToken(paths)

	run.targetTokenFile, err = keyfile.PreserveTargetToken(paths["rootKeyFile"], paths["vaultName"], run.targetToken,
		run.steps.now(), run.log)
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

// recoverRunningKeys recovers the keys of the bloc's running vault before
// anything stops it, because the log and safe's target hold the keys of a
// vault that runs now.
//
// An open vault, initialized and unsealed, still serves its secrets, and
// once it stops it can be reopened only with both keys. Its keys must be
// saved in a valid shape, as for a healthy vault, or the run fails with
// ErrInceptionKeysNotSaved before any token is kept and before anything is
// stopped, archived, or started, and the vault keeps running.
//
// A sealed or never-initialized engine holds nothing in memory that its
// data and key files do not, so stopping it loses nothing. Its missing keys
// are recovered when they can be, and without both the run refuses once it
// has stopped, and the vault is never migrated.
func (run *inceptionRun) recoverRunningKeys(ctx context.Context, found preflightFindings) error {
	if found.open {
		return run.requireSavedKeys(ctx)
	}

	if !found.running {
		return nil
	}

	keys, err := inceptionKeysUsable(run.paths)
	if err != nil || keys {
		return err
	}

	err = run.steps.recoverKeys(ctx, run.paths, run.log)
	if err != nil {
		return fmt.Errorf("failed to recover the running inception vault's keys: %w", err)
	}

	return nil
}

// requireEngineForMigration refuses, before anything is stopped or written,
// a run that would copy file storage to raft with an engine that cannot,
// because it has no 'operator migrate' or reports a version that cannot
// read file storage. A running file vault keeps serving rather than being
// stopped for a copy that cannot succeed. A run that only finishes a swap,
// or has raft data, copies nothing and needs no such engine.
func (run *inceptionRun) requireEngineForMigration(ctx context.Context, found preflightFindings) error {
	copies := found.data == vaultDataFile
	if found.journal != nil {
		copies = found.journal.Phase == migratePhaseMigrating
	}

	if !copies {
		return nil
	}

	return run.steps.canMigrate(ctx)
}

// requireWorkingRootToken refuses to stop an open vault that the run would
// migrate when that vault does not take the token in root.key, or cannot say
// whether it does. The restart after a migration opens the vault with that
// token alone, so such a vault would stay down, while left running it still
// serves its secrets. A sealed vault holds nothing a stop could lose, and an
// open raft vault is restarted by restart, which retries safe's token.
func (run *inceptionRun) requireWorkingRootToken(ctx context.Context, found preflightFindings) error {
	if !found.open || (found.data != vaultDataFile && found.journal == nil) {
		return nil
	}

	err := run.steps.rootTokenWorks(ctx, run.paths)
	if err != nil {
		return fmt.Errorf("%w%s", err, rootTokenRefusedAdvice(run.paths, "vault inception", run.steps.targetToken(run.paths)))
	}

	return nil
}

// rootTokenRefusedAdvice explains why command stopped nothing when an open
// vault does not take the token in root.key, and how to go on. It names
// safe's target when that target holds a token other than root.key's, and
// every copy of a token ocfp kept beside root.key, since one of them may be
// the token the vault takes. It never quotes a token.
func rootTokenRefusedAdvice(paths map[string]string, command, targetToken string) string {
	var b strings.Builder

	fmt.Fprintf(&b, "; the restart after the migration opens the vault with that token, so %s stopped nothing, "+
		"and the vault is still running", command)

	held, _ := keyfile.Read(paths["rootKeyFile"])
	if targetToken != "" && targetToken != held {
		fmt.Fprintf(&b, "; safe's target %s holds a different token, which may be the one the vault takes",
			paths["vaultName"])
	}

	kept, _ := keyfile.KeptTokenCopies(paths["rootKeyFile"])
	if len(kept) > 0 {
		fmt.Fprintf(&b, "; ocfp kept tokens that safe's target held earlier in %s, and one of them may be the "+
			"one the vault takes", strings.Join(kept, " and "))
	}

	fmt.Fprintf(&b, "; once %s holds the token the vault takes, at mode 0600, run the command again",
		paths["rootKeyFile"])

	return b.String()
}

// preflightFindings is what preflight learned about the bloc.
type preflightFindings struct {
	// journal is the migration in flight, if any.
	journal *migrationJournal
	// data is the kind of storage the data directory holds.
	data vaultDataState
	// running reports that this bloc's own vault answers on the port.
	running bool
	// open reports that the running vault is initialized and unsealed, so
	// its secrets can still be read through it.
	open bool
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
		return preflightFindings{journal: journal, data: data}, nil
	}

	owned, err := run.steps.ownsVault(ctx, paths, data)
	if err != nil {
		return preflightFindings{}, err
	}

	if !owned {
		return preflightFindings{}, inceptionPortTakenError(paths, "a vault answers there that this bloc cannot prove is its own")
	}

	return preflightFindings{
		journal: journal,
		data:    data,
		running: true,
		open:    probe.initialized && !probe.sealed,
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

// teardownSteps are the actions tearDownInceptionVault takes. Production code
// wires the real ones through newTeardownSteps, and tests script the probe,
// the ownership check, and key recovery the way they do for reconcile.
type teardownSteps struct {
	// probe asks the API port for seal status.
	probe func(ctx context.Context, addr string) vaultProbe
	// ownsVault reports whether the vault answering on the port is this
	// bloc's own.
	ownsVault func(ctx context.Context, paths map[string]string, data vaultDataState) (bool, error)
	// recoverKeys writes whichever key files are missing from what the bloc's
	// running vault left behind, and never replaces a key file that exists.
	recoverKeys func(ctx context.Context, paths map[string]string, log *zap.SugaredLogger) error
	// disableBootUnit disables the unit that brings the bloc's vault back
	// after a reboot.
	disableBootUnit func(ctx context.Context, log *zap.SugaredLogger) error
	// cleanup stops the bloc's vault and archives its data and keys.
	cleanup func(ctx context.Context, paths map[string]string, log *zap.SugaredLogger) error
}

// newTeardownSteps wires the real teardown actions for bloc.
func newTeardownSteps(bloc string) teardownSteps {
	return teardownSteps{
		probe:       probeInceptionVault,
		ownsVault:   ownsInceptionVault,
		recoverKeys: recoverInceptionKeys,
		disableBootUnit: func(ctx context.Context, log *zap.SugaredLogger) error {
			return disableVaultBootUnit(ctx, bloc, log)
		},
		cleanup: cleanupExistingVault,
	}
}

// tearDownInceptionVault stops the bloc's own inception vault and archives
// its data and keys.
//
// An open vault, initialized and unsealed, still serves its secrets, and its
// archive can be reopened only with both keys. Teardown therefore holds an
// open vault to the same rule reconcile does: its key files are prepared the
// way reconcile prepares them, so a cut-short unseal key is restored from the
// vault's output, its missing keys are recovered from what it left behind,
// and when both still are not saved, teardown refuses before it keeps a
// token, stops, or archives anything, and the vault keeps running. Only a
// refusal for keys that are not saved offers --force; any other failure, such
// as a key file that cannot be read, has its own fix. With force, a failed
// check is logged as a warning and the vault is archived anyway. A sealed,
// never-initialized, or stopped vault holds nothing in memory that its files
// do not, so it is archived as it is.
//
// Only after the vault has been stopped and archived does teardown disable
// the bloc's boot unit. A refused teardown leaves the vault running, and a
// cleanup that fails can leave it in place, so both leave the unit enabled.
// A disable that fails is only a warning, because the unit only runs 'ocfp
// vault start', which refuses once the vault is archived.
func tearDownInceptionVault(
	ctx context.Context, paths map[string]string, steps teardownSteps, force bool, log *zap.SugaredLogger,
) error {
	// The ownership guard and the key check judge one answer from the port,
	// so the probe runs once and its result is kept here.
	var found vaultProbe

	probe := func(ctx context.Context, addr string) vaultProbe {
		found = steps.probe(ctx, addr)

		return found
	}

	err := guardInceptionTeardown(ctx, paths, probe, steps.ownsVault)
	if err != nil {
		return fmt.Errorf("teardown refused: %w", err)
	}

	open := found.state == vaultProbeVault && found.initialized && !found.sealed
	if open {
		err = requireOpenVaultKeys(ctx, paths, steps.recoverKeys, log)
	}

	switch {
	case err == nil:
	case force:
		log.Warnw("Archiving the open inception vault anyway because --force was given; "+
			"without both keys its archive can never be unsealed", "error", err)
	case errors.Is(err, ErrInceptionKeysNotSaved), errors.Is(err, ErrUnsealKeyFileMalformed):
		return fmt.Errorf("teardown refused, and the vault was left running: %w; "+
			"'ocfp vault teardown --force' archives it anyway, but without both keys the archive can never be unsealed",
			err)
	default:
		return fmt.Errorf("teardown refused, and the vault was left running: %w", err)
	}

	err = steps.cleanup(ctx, paths, log)
	if err != nil {
		return fmt.Errorf("teardown failed, and the boot unit was left enabled: %w", err)
	}

	err = steps.disableBootUnit(ctx, log)
	if err != nil {
		log.Warnw("The inception vault is archived, but its boot unit could not be disabled; "+
			"the unit only runs 'ocfp vault start', which refuses once the vault is archived", "error", err)
	}

	return nil
}

var (
	// ErrVaultStartRefused is wrapped by every refusal of 'ocfp vault start'.
	// A refusal never archives the vault and never starts a new one.
	ErrVaultStartRefused = errors.New("ocfp vault start refused to restart the inception vault")

	// ErrVaultStartNoData reports a bloc with no raft data to restart.
	ErrVaultStartNoData = errors.New("the inception vault has no raft data to restart")

	// ErrVaultStartKeyMissing reports a missing or empty root.key or
	// unseal.keys, without which a vault cannot be reopened.
	ErrVaultStartKeyMissing = errors.New("an inception vault key file is missing or empty")

	// ErrVaultStartNeedsInception reports data that only 'ocfp vault
	// inception' may change, such as file storage or an unfinished migration.
	ErrVaultStartNeedsInception = errors.New("the inception vault needs 'ocfp vault inception' run by hand")

	// ErrVaultStartKeysRejected reports an engine that refused a saved key
	// during the restart. What was started has been stopped.
	ErrVaultStartKeysRejected = errors.New("the engine refused a saved inception vault key")
)

// vaultStartSteps are the only actions 'ocfp vault start' may take. They are
// a narrower set than inceptionSteps on purpose: there is no migrate step,
// and reopen takes no safe local mode, so the only start it can run is a
// restart with the saved keys. Nothing vault start runs can archive a vault
// or start a new one, and a test walks its calls to keep it that way.
type vaultStartSteps struct {
	// probe asks the API port for seal status.
	probe func(ctx context.Context, addr string) vaultProbe
	// hasSession reports whether the bloc's tmux session exists.
	hasSession func(ctx context.Context, paths map[string]string) bool
	// ownsVault reports whether the vault answering on the port is this
	// bloc's own.
	ownsVault func(ctx context.Context, paths map[string]string, data vaultDataState) (bool, error)
	// stop stops the bloc's vault and waits until the port and data are free.
	stop func(ctx context.Context, paths map[string]string, log *zap.SugaredLogger) error
	// clusterPortFree reports why the cluster port cannot be bound, if it
	// cannot.
	clusterPortFree func(ctx context.Context, port string) error
	// reopen runs safe local in restart mode with the saved keys and waits
	// until the vault is ready.
	reopen func(ctx context.Context, paths map[string]string, log *zap.SugaredLogger) error
	// finish targets the reopened vault.
	finish func(ctx context.Context, paths map[string]string, log *zap.SugaredLogger) error
	// targetToken returns the root token safe holds for the bloc's target,
	// read before a stop deletes that target.
	targetToken func(paths map[string]string) string
	// safeTarget reads and sets safe's current target, so that the restart
	// can put back the target that was current before it.
	safeTarget safeCurrentTargetSteps
	// now stamps the copy of a kept target token.
	now func() time.Time
}

// newVaultStartSteps wires the real vault start actions, using the safe and
// engine binaries the prerequisite check validated.
func newVaultStartSteps(tools inceptionTools) vaultStartSteps {
	return wireVaultStartSteps(tools, startInceptionVault, finishReopenedVault)
}

// wireVaultStartSteps is newVaultStartSteps with the start and the finish
// passed in, so a test can run the wiring production uses and see the mode
// it hands safe local. The reopen always asks for a restart with the saved
// keys, and the finish takes no mode at all.
func wireVaultStartSteps(
	tools inceptionTools,
	start func(ctx context.Context, paths map[string]string, tools inceptionTools, mode safeLocalMode,
		log *zap.SugaredLogger) error,
	finish func(ctx context.Context, safePath string, paths map[string]string, log *zap.SugaredLogger) error,
) vaultStartSteps {
	return vaultStartSteps{
		probe:           probeInceptionVault,
		hasSession:      inceptionSessionExists,
		ownsVault:       ownsInceptionVault,
		stop:            stopInceptionVault,
		clusterPortFree: inceptionClusterPortFree,
		reopen: func(ctx context.Context, paths map[string]string, log *zap.SugaredLogger) error {
			return start(ctx, paths, tools, safeLocalRestart, log)
		},
		finish: func(ctx context.Context, paths map[string]string, log *zap.SugaredLogger) error {
			return finish(ctx, tools.safe, paths, log)
		},
		targetToken: blocTargetToken,
		safeTarget:  newSafeCurrentTargetSteps(tools.safe),
		now:         time.Now,
	}
}

// vaultStartRun is one pass of restartInceptionVaultInPlace over one bloc.
type vaultStartRun struct {
	paths map[string]string
	steps vaultStartSteps
	log   *zap.SugaredLogger
}

// vaultStartFindings is what vault start's checks learned about the bloc.
type vaultStartFindings struct {
	// healthy reports a running vault that is left as it is.
	healthy bool
	// running reports that this bloc's own vault answers on the port.
	running bool
	// open reports that the running vault is initialized and unsealed, so
	// it is left running too.
	open bool
}

// restartInceptionVaultInPlace is what 'ocfp vault start' does, and it runs
// unattended at boot, so it only ever brings back the vault the bloc already
// has.
//
// This bloc's own vault, when it is open and serving, is left running
// whether it is healthy or not, before its data or keys are checked or
// touched. Every check that can refuse runs
// before anything is stopped or written: a stranger or a sibling's vault on
// the API port, something on the cluster port, data that is missing or that
// only 'ocfp vault inception' may change, and a missing or empty key file. A
// refusal wraps ErrVaultStartRefused and leaves the disk as it was.
// Otherwise the port is probed again, and nothing is stopped when what
// answers is not this bloc's own. Only then do the key files get the
// value-preserving repairs reconcile gives them, which restore a cut-short
// unseal key to the whole key that begins with it and strip whitespace from
// around a key, and no other write. The vault is then stopped and reopened
// in place with its saved keys. When the engine refuses a key, what was
// started is stopped, after the port is probed again, and the run refuses.
// Nothing here archives a vault, starts a new one, migrates one, or
// recovers or replaces a key.
func restartInceptionVaultInPlace(ctx context.Context, run *vaultStartRun) error {
	paths := run.paths

	found, err := run.check(ctx)
	if err != nil {
		return err
	}

	if found.healthy || found.open {
		run.leaveRunning(found)

		return nil
	}

	// The port is probed again before the key files are repaired, so a
	// refusal because it changed hands follows no write.
	err = run.requireOwnPortHolder(ctx)
	if err != nil {
		return refuseVaultStart(err)
	}

	err = prepareInceptionKeyFiles(ctx, paths, run.log)
	if err != nil {
		return refuseVaultStart(err)
	}

	// From the stop on, safe's current target can move, because the stop
	// deletes the bloc's target and the restart registers it again and makes
	// it current. Whatever was current before is put back however the run
	// ends.
	earlier := rememberSafeCurrentTarget(ctx, run.steps.safeTarget)
	defer putBackSafeCurrentTarget(ctx, run.steps.safeTarget, earlier, paths["vaultName"], run.log)

	// Stopping deletes the bloc's safe target, which may hold the only copy
	// of the root token, so it is kept first, as reconcile keeps it.
	_, err = keyfile.PreserveTargetToken(paths["rootKeyFile"], paths["vaultName"], run.steps.targetToken(paths),
		run.steps.now(), run.log)
	if err != nil {
		return fmt.Errorf("failed to keep the root token from safe's target before stopping the inception vault: %w", err)
	}

	err = run.steps.stop(ctx, paths, run.log)
	if err != nil {
		return fmt.Errorf("failed to stop the inception vault before restarting it: %w", err)
	}

	if found.running {
		// A running vault holds its own cluster port, so the port can be
		// judged only once that vault is down.
		err = run.requireClusterPortFree(ctx)
		if err != nil {
			return fmt.Errorf("the inception vault was stopped and not restarted: %w", err)
		}
	}

	return run.reopenInPlace(ctx)
}

// check runs every check that can refuse before anything is stopped or
// written, and reports what it found.
func (run *vaultStartRun) check(ctx context.Context) (vaultStartFindings, error) {
	paths := run.paths

	probe := run.steps.probe(ctx, "http://127.0.0.1:"+paths["port"])
	if probe.state == vaultProbeStranger {
		return vaultStartFindings{}, refuseVaultStart(inceptionPortTakenError(paths, "something that is not a vault answers there"))
	}

	data := classifyVaultData(paths["vaultDir"])

	journal, err := readMigrationJournal(paths["vaultDir"])
	if err != nil {
		return vaultStartFindings{}, refuseVaultStart(err)
	}

	running := probe.state == vaultProbeVault
	if running {
		owned, ownErr := run.steps.ownsVault(ctx, paths, data)
		if ownErr != nil {
			return vaultStartFindings{}, refuseVaultStart(ownErr)
		}

		if !owned {
			return vaultStartFindings{}, refuseVaultStart(inceptionPortTakenError(paths,
				"a vault answers there that this bloc cannot prove is its own"))
		}

		if journal == nil && inceptionVaultHealthy(ctx, paths, probe, data, run.steps.hasSession) {
			return vaultStartFindings{healthy: true}, nil
		}

		// An open vault is serving, and vault start only starts a stopped
		// one, so nothing about its data or keys can change what happens
		// next.
		if probe.initialized && !probe.sealed {
			return vaultStartFindings{running: true, open: true}, nil
		}
	}

	err = requireRestartableData(paths, data, journal)
	if err != nil {
		return vaultStartFindings{}, refuseVaultStart(err)
	}

	err = requireStartKeys(paths)
	if err != nil {
		return vaultStartFindings{}, refuseVaultStart(err)
	}

	if !running {
		err = run.requireClusterPortFree(ctx)
		if err != nil {
			return vaultStartFindings{}, err
		}
	}

	return vaultStartFindings{running: running}, nil
}

// leaveRunning leaves an open vault running and changes nothing, because
// vault start only starts a stopped vault. An open vault that is not
// healthy, for example one that lost its tmux session, would be lost if it
// stopped and its saved keys no longer opened it, so that is a warning. A vault
// whose keys are not both saved cannot be reopened once it stops, so that is
// a warning too, and recovering them is left to 'ocfp vault inception',
// which can do it while the vault runs.
func (run *vaultStartRun) leaveRunning(found vaultStartFindings) {
	paths := run.paths

	if !found.healthy {
		run.log.Warnw("The inception vault is open and serving but is not healthy, for example because its "+
			"tmux session is gone; vault start only starts a stopped vault, so it left this one running. "+
			"Run 'ocfp vault inception' by hand to bring it back to health",
			"tmux_session", paths["tmuxSession"], "data", paths["vaultDir"])
	}

	saved, err := inceptionKeysSaved(paths)
	if err != nil || !saved {
		run.log.Warnw("The inception vault is running, but its keys are not both saved; "+
			"run 'ocfp vault inception' while it runs to recover them",
			"root_token", paths["rootKeyFile"], "unseal_key", paths["unsealKeysFile"], "error", err)
	}

	run.log.Infow("Inception vault is already running; vault start left it alone",
		"url", "http://127.0.0.1:"+paths["port"])
}

// requireClusterPortFree refuses when the cluster port cannot be bound.
func (run *vaultStartRun) requireClusterPortFree(ctx context.Context) error {
	paths := run.paths

	err := run.steps.clusterPortFree(ctx, paths["clusterPort"])
	if err != nil {
		return refuseVaultStart(fmt.Errorf("%w: cluster port %s (API port %s plus 1000): %w; free it, or set %s to move both ports",
			ErrInceptionClusterPortTaken, paths["clusterPort"], paths["port"], err, inceptionPortEnvVar))
	}

	return nil
}

// reopenInPlace restarts the stopped vault with its saved keys. Any failure
// stops what was started, once a fresh probe shows that what holds the port
// is this bloc's own. A key the engine refused is a refusal, and any other
// failure says nothing about the keys; either way the data and keys are left
// where they are.
func (run *vaultStartRun) reopenInPlace(ctx context.Context) error {
	paths := run.paths

	run.log.Infow("Restarting the inception vault in place with its saved keys", "data", paths["vaultDir"])

	err := run.steps.reopen(ctx, paths, run.log)
	if err == nil {
		return run.steps.finish(ctx, paths, run.log)
	}

	stopErr := run.requireOwnPortHolder(ctx)
	if stopErr == nil {
		stopErr = wrapStopAfterFailure(run.steps.stop(ctx, paths, run.log))
	}

	if errors.Is(err, ErrVaultKeysRejected) {
		return refuseVaultStart(errors.Join(fmt.Errorf("%w: %w; %s", ErrVaultStartKeysRejected, err,
			keysRefusedAdvice(paths, err, "")), stopErr))
	}

	return errors.Join(fmt.Errorf("failed to restart the inception vault, and its data in %s and its keys were left "+
		"as they were: %w", paths["vaultDir"], err), stopErr)
}

// requireOwnPortHolder probes the API port again right before a stop, and
// fails unless nothing answers there or the vault that answers is this
// bloc's own. A stop kills whatever listens on the port and every safe local
// for it, and a bloc whose port collides with this one's can start its own
// vault between vault start's checks and its stop, as two boot units do
// when they run at once. What answers then is left running.
func (run *vaultStartRun) requireOwnPortHolder(ctx context.Context) error {
	return requireOwnInceptionPortHolder(ctx, run.paths, run.steps.probe, run.steps.ownsVault, "vault start")
}

// requireOwnInceptionPortHolder is the check behind requireOwnPortHolder,
// shared by every command that stops the bloc's vault only after checks that
// ran a while before. actor names the command in the error, which says that
// it stopped nothing.
func requireOwnInceptionPortHolder(
	ctx context.Context, paths map[string]string,
	probe func(ctx context.Context, addr string) vaultProbe,
	ownsVault func(ctx context.Context, paths map[string]string, data vaultDataState) (bool, error),
	actor string,
) error {
	found := probe(ctx, "http://127.0.0.1:"+paths["port"])

	switch found.state {
	case vaultProbeStopped:
		return nil
	case vaultProbeStranger:
		return inceptionPortTakenError(paths,
			"something that is not a vault answers there now, so "+actor+" stopped nothing and left it running")
	case vaultProbeVault:
	}

	owned, err := ownsVault(ctx, paths, classifyVaultData(paths["vaultDir"]))
	if err != nil {
		return fmt.Errorf("%s stopped nothing, because it cannot tell whose vault answers on port %s now: %w",
			actor, paths["port"], err)
	}

	if !owned {
		return inceptionPortTakenError(paths, "a vault answers there now that this bloc cannot prove is its own, "+
			"so "+actor+" stopped nothing and left it running")
	}

	return nil
}

// refuseVaultStart marks err as a refusal of vault start.
func refuseVaultStart(err error) error {
	return fmt.Errorf("%w: %w", ErrVaultStartRefused, err)
}

// requireRestartableData refuses data that vault start cannot reopen as it
// is. Raft data with no migration in flight is the only kind it restarts.
func requireRestartableData(paths map[string]string, data vaultDataState, journal *migrationJournal) error {
	dir := paths["vaultDir"]

	switch {
	case data == vaultDataMixed:
		return fmt.Errorf("%w: %s; inspect it by hand, ocfp will not touch it", ErrVaultDataMixed, dir)
	case journal != nil:
		return fmt.Errorf("%w: %s records a migration to raft that has not finished, and only that command resumes it",
			ErrVaultStartNeedsInception, dir+migrationJournalSuffix)
	case data == vaultDataFile:
		return fmt.Errorf("%w: %s holds file storage, and only that command migrates it to raft",
			ErrVaultStartNeedsInception, dir)
	case data == vaultDataAbsent:
		return fmt.Errorf("%w: %s holds no vault data, and vault start never creates a vault; "+
			"'ocfp vault inception' starts a new one", ErrVaultStartNoData, dir)
	}

	return nil
}

// requireStartKeys refuses a restart unless root.key and unseal.keys are
// separate files that both hold something. vault start never recovers or
// replaces a missing key, and leaves that to 'ocfp vault inception', which
// recovers it from the vault logs or refuses the vault.
func requireStartKeys(paths map[string]string) error {
	if paths["rootKeyFile"] == paths["unsealKeysFile"] {
		return fmt.Errorf("%w: the root token and the unseal key share %s, which cannot hold both",
			ErrVaultStartKeyMissing, paths["rootKeyFile"])
	}

	var missing []string

	for _, keyFile := range []string{paths["rootKeyFile"], paths["unsealKeysFile"]} {
		held, err := keyFileHasValue(keyFile)
		if err != nil {
			return err
		}

		if !held {
			missing = append(missing, keyFile)
		}
	}

	if len(missing) > 0 {
		return fmt.Errorf("%w: %s; vault start never recovers or replaces a key, so run 'ocfp vault inception' by hand",
			ErrVaultStartKeyMissing, strings.Join(missing, " and "))
	}

	return nil
}

// isHealthy reports whether the running vault can be left exactly as it is:
// its session exists, and it is an initialized, unsealed vault on raft data.
// An engine that does not report its storage is judged by the data alone.
func (run *inceptionRun) isHealthy(ctx context.Context, probe vaultProbe, data vaultDataState) bool {
	return inceptionVaultHealthy(ctx, run.paths, probe, data, run.steps.hasSession)
}

// inceptionVaultHealthy is the check behind isHealthy, shared with vault
// start, which judges a running vault by the same rule.
func inceptionVaultHealthy(
	ctx context.Context, paths map[string]string, probe vaultProbe, data vaultDataState,
	hasSession func(ctx context.Context, paths map[string]string) bool,
) bool {
	raft := probe.storageType == "raft" || probe.storageType == ""

	return probe.initialized && !probe.sealed && raft && data == vaultDataRaft && hasSession(ctx, paths)
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
	return requireSavedInceptionKeys(ctx, run.paths, run.steps.recoverKeys, run.log)
}

// prepareInceptionKeyFiles readies the bloc's key files before anything judges
// them, and runs the same way for reconcile and teardown. A key file that
// cannot be read is an error, since it may hold the key. An unseal key file
// that holds only the start of a key is restored to the whole key from the
// vault's output, and a key file that holds its key with stray whitespace
// around it is rewritten to the key and a newline. A missing or blank key
// file is left for recovery to fill.
func prepareInceptionKeyFiles(ctx context.Context, paths map[string]string, log *zap.SugaredLogger) error {
	err := requireReadableKeyFiles(paths)
	if err != nil {
		return err
	}

	err = repairUnsealKeyFile(ctx, paths, log)
	if err != nil {
		return err
	}

	return canonicalizeRootKeyFile(paths, log)
}

// requireOpenVaultKeys holds an open vault to reconcile's rule before
// teardown stops it: its key files are prepared the way reconcile prepares
// them, and then both keys must be saved, recovering whichever is missing.
func requireOpenVaultKeys(
	ctx context.Context, paths map[string]string,
	recoverKeys func(ctx context.Context, paths map[string]string, log *zap.SugaredLogger) error,
	log *zap.SugaredLogger,
) error {
	err := prepareInceptionKeyFiles(ctx, paths, log)
	if err != nil {
		return err
	}

	return requireSavedInceptionKeys(ctx, paths, recoverKeys, log)
}

// requireSavedInceptionKeys is the check behind requireSavedKeys, shared with
// teardown. It recovers whichever key the running vault left behind with
// recoverKeys, and fails with ErrInceptionKeysNotSaved, naming the logs that
// may still hold the keys, when both are still not saved.
func requireSavedInceptionKeys(
	ctx context.Context, paths map[string]string,
	recoverKeys func(ctx context.Context, paths map[string]string, log *zap.SugaredLogger) error,
	log *zap.SugaredLogger,
) error {
	saved, err := inceptionKeysSaved(paths)
	if err != nil || saved {
		return err
	}

	err = recoverKeys(ctx, paths, log)
	if err != nil {
		return fmt.Errorf("failed to recover the running inception vault's keys: %w", err)
	}

	saved, err = inceptionKeysSaved(paths)
	if err != nil || saved {
		return err
	}

	log.Errorw("The running inception vault's keys are not both saved; it cannot be reopened after it stops",
		"root_token", paths["rootKeyFile"], "unseal_key", paths["unsealKeysFile"])

	return fmt.Errorf("%w: the vault at %s is still running, but %s and %s do not both hold a valid key; %s",
		ErrInceptionKeysNotSaved, "http://127.0.0.1:"+paths["port"], paths["rootKeyFile"], paths["unsealKeysFile"],
		vaultLogHint(paths))
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
	case data == vaultDataAbsent:
		return run.startWithoutData(ctx, anyKey)
	default:
		return run.refuseDataWithoutKeys(data)
	}
}

// startWithoutData starts a new vault for a bloc whose data directory holds
// no store, archiving any key files left behind first. The archive is the
// one path from reconcile to an archive. Nothing starts when a store that a
// migration to raft kept or moved aside still sits beside the data
// directory, because that store may be the vault, opened by the keys left
// behind or by keys restored from a backup, so the run refuses with
// ErrInceptionDataElsewhere and changes nothing.
func (run *inceptionRun) startWithoutData(ctx context.Context, anyKey bool) error {
	paths := run.paths

	stores := migrationStoresBeside(paths["vaultDir"])
	switch {
	case len(stores) == 0 && !anyKey:
		return run.fresh(ctx)
	case len(stores) == 0:
		return run.archiveAndStartFresh(ctx, "key files were left with no vault data")
	}

	run.log.Errorw("The inception vault's data directory is gone, but stores beside it may hold its data; "+
		"nothing was changed", "data", paths["vaultDir"], "stores", stores)

	keys := fmt.Sprintf("%s and %s are still in place", paths["rootKeyFile"], paths["unsealKeysFile"])
	if !anyKey {
		keys = fmt.Sprintf("neither %s nor %s holds a key", paths["rootKeyFile"], paths["unsealKeysFile"])
	}

	return fmt.Errorf("%w: %s does not exist and %s, but %s may hold the vault, so no new vault was started "+
		"beside it; inspect them by hand, and to bring a store back, rename it to %s, make sure both key files "+
		"hold its keys, and run 'ocfp vault inception'; %s",
		ErrInceptionDataElsewhere, paths["vaultDir"], keys, strings.Join(stores, " and "), paths["vaultDir"],
		replaceVaultAdvice)
}

// refuseDataWithoutKeys refuses a stopped vault whose data has no usable
// root token and unseal key. The data may still be opened once a key is
// restored, so it is left in place with whichever key file it has.
func (run *inceptionRun) refuseDataWithoutKeys(data vaultDataState) error {
	paths := run.paths

	run.log.Errorw("The inception vault has data but not both keys to reopen it; nothing was changed",
		"data", paths["vaultDir"], "root_token", paths["rootKeyFile"], "unseal_key", paths["unsealKeysFile"])

	return fmt.Errorf("%w: %s holds %s storage, but %s and %s do not both hold a key of their own; "+
		"restore the missing key from a backup or from the vault logs, %s, and run 'ocfp vault inception' again; %s",
		ErrInceptionKeysMissing, paths["vaultDir"], data, paths["rootKeyFile"], paths["unsealKeysFile"],
		vaultLogHint(paths), replaceVaultAdvice)
}

// replaceVaultAdvice is how an operator replaces an inception vault with a
// new, empty one on purpose, which reconcile never does on its own.
const replaceVaultAdvice = "to replace it with a new, empty vault, run 'ocfp vault teardown' and then " +
	"'ocfp vault inception', and teardown moves the old vault aside rather than deleting it"

// migrationStoresBeside lists the directories beside the data directory that
// a migration to raft kept or moved aside, and that still hold file or raft
// storage: data.file-backup-*, data.raft-failed-*, data.raft-partial-*, and
// data.raft-migrating. A directory that cannot be read counts as holding a
// store, the way classifyVaultData counts it.
func migrationStoresBeside(dataDir string) []string {
	parent, base := filepath.Dir(dataDir), filepath.Base(dataDir)

	entries, err := os.ReadDir(parent)
	if err != nil {
		return []string{parent}
	}

	var stores []string

	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || !isMigrationStoreName(base, name) {
			continue
		}

		dir := filepath.Join(parent, name)
		if classifyVaultData(dir) != vaultDataAbsent {
			stores = append(stores, dir)
		}
	}

	slices.Sort(stores)

	return stores
}

// isMigrationStoreName reports whether name is one a migration to raft gives
// a store beside the data directory named base.
func isMigrationStoreName(base, name string) bool {
	if name == base+migrationStagingSuffix {
		return true
	}

	for _, suffix := range []string{migrationBackupSuffix, migrationFailedSuffix, migrationPartialSuffix} {
		if strings.HasPrefix(name, base+suffix) && len(name) > len(base+suffix) {
			return true
		}
	}

	return false
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

	advice := ""
	if errors.Is(err, ErrVaultRootTokenRejected) {
		advice = keptTokenCopiesAdvice(paths, "")
	}

	err = fmt.Errorf("%w: the raft data is in %s and the file store it came from is kept in %s, "+
		"and neither was changed after the migration: %w%s", ErrRestartAfterMigrationFailed, paths["vaultDir"], backup,
		err, advice)

	return errors.Join(err, wrapStopAfterFailure(run.steps.stop(ctx, paths, run.log)))
}

// restart reopens the vault with its saved keys. Any failure stops what was
// started and leaves the data and keys where they are. Nothing here archives
// the vault or starts a new one, because a vault whose key the engine refuses
// still holds its data, and the keys may be what is wrong.
//
// When the engine refuses the token in root.key and safe's target held a
// different one before the stop, the vault is reopened once with that token.
// When the engine refuses a key and that retry does not open the vault, the
// run refuses with ErrInceptionKeysRefused, and the error names the refused
// key file and how to replace the vault on purpose.
func (run *inceptionRun) restart(ctx context.Context) error {
	paths := run.paths

	run.log.Infow("Restarting the inception vault with its saved keys", "data", paths["vaultDir"])

	err := run.steps.start(ctx, paths, run.tools, safeLocalRestart, run.log)
	if err == nil {
		return run.steps.finish(ctx, paths, safeLocalRestart, run.log)
	}

	refused, err := run.stopAfterFailedRestart(ctx, err)
	if !refused {
		return err
	}

	tokenFile := run.retryTokenFile(err)
	if tokenFile != "" {
		retryErr := run.restartWithTargetToken(ctx, tokenFile)
		if retryErr == nil {
			return nil
		}

		refused, retryErr = run.stopAfterFailedRestart(ctx, retryErr)
		if !refused {
			return retryErr
		}

		err = fmt.Errorf("%w; the token from safe's target, kept in %s, was refused too: %w", err, tokenFile, retryErr)
	}

	run.log.Errorw("The engine refused a saved key of the inception vault; it was stopped and left as it was",
		"data", paths["vaultDir"], "root_token", paths["rootKeyFile"], "unseal_key", paths["unsealKeysFile"])

	return fmt.Errorf("%w: %w; %s", ErrInceptionKeysRefused, err, keysRefusedAdvice(paths, err, tokenFile))
}

// stopAfterFailedRestart stops a vault that failed to restart, and reports
// whether the engine refused a saved key once the vault is known to be down.
// Any other failure, or a stop that failed, comes back as the error.
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

// keysRefusedAdvice explains what to do about a vault whose saved key the
// engine refused with startErr. It names the refused key file, any copy of
// safe's token kept beside root.key other than tried when the root token was
// refused, every file store a migration to raft kept beside the data with
// how to roll back to the newest of them, and the two commands that replace
// the vault on purpose. It says nothing about the stop, which the caller
// reports, and it never quotes a key.
func keysRefusedAdvice(paths map[string]string, startErr error, tried string) string {
	refused := paths["unsealKeysFile"]
	if errors.Is(startErr, ErrVaultRootTokenRejected) {
		refused = paths["rootKeyFile"]
	}

	var b strings.Builder

	fmt.Fprintf(&b, "the engine refused the key in %s, and the vault's data in %s and its keys were left as "+
		"they were", refused, paths["vaultDir"])

	if refused == paths["rootKeyFile"] {
		b.WriteString(keptTokenCopiesAdvice(paths, tried))
	}

	backups := fileStoreBackups(paths["vaultDir"])
	if len(backups) > 0 {
		newest := backups[len(backups)-1]

		fmt.Fprintf(&b, "; the file store from before its migration to raft is kept in %s, and to roll back to it, "+
			"move %s aside and rename %s to %s, and then run 'ocfp vault inception', which migrates it to raft again",
			strings.Join(backups, " and "), paths["vaultDir"], newest, paths["vaultDir"])
	}

	b.WriteString("; " + replaceVaultAdvice)

	return b.String()
}

// keptTokenCopiesAdvice names the copies of safe's token that ocfp kept
// beside root.key, leaving out skip, and says how to use one that the vault
// takes. It says nothing when there are none, or when they cannot be
// listed, since it only feeds advice.
func keptTokenCopiesAdvice(paths map[string]string, skip string) string {
	all, err := keyfile.KeptTokenCopies(paths["rootKeyFile"])
	if err != nil {
		return ""
	}

	var kept []string

	for _, copyFile := range all {
		if copyFile != skip {
			kept = append(kept, copyFile)
		}
	}

	if len(kept) == 0 {
		return ""
	}

	if len(kept) == 1 {
		return fmt.Sprintf("; safe's target held a different root token, which ocfp kept in %s, and when the "+
			"vault takes it, copy it over %s at mode 0600 and run the command again", kept[0], paths["rootKeyFile"])
	}

	return fmt.Sprintf("; safe's target held other root tokens, which ocfp kept in %s, and when the vault takes "+
		"one of them, copy it over %s at mode 0600 and run the command again", strings.Join(kept, " and "),
		paths["rootKeyFile"])
}

// fileStoreBackups lists the data.file-backup-* directories beside the data
// directory, which hold file stores that a migration to raft kept, sorted by
// name so the newest timestamp comes last. It lists nothing when the parent
// cannot be read, since it only feeds advice.
func fileStoreBackups(dataDir string) []string {
	entries, err := os.ReadDir(filepath.Dir(dataDir))
	if err != nil {
		return nil
	}

	prefix := filepath.Base(dataDir) + migrationBackupSuffix

	var backups []string

	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), prefix) && len(entry.Name()) > len(prefix) {
			backups = append(backups, filepath.Join(filepath.Dir(dataDir), entry.Name()))
		}
	}

	slices.Sort(backups)

	return backups
}

// retryTokenFile returns the file holding safe's token when a restart that
// failed with err is worth one more try with it: the engine refused the root
// token, and safe's target held a different, token-shaped one.
func (run *inceptionRun) retryTokenFile(err error) string {
	if !errors.Is(err, ErrVaultRootTokenRejected) || run.targetTokenFile == "" ||
		run.targetTokenFile == run.paths["rootKeyFile"] || keyfile.CheckRootToken(run.targetToken) != nil {
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
// any keys, and starts a new, empty vault in its place. Its one caller is
// startWithoutData, which reaches it only when there is no data to lose,
// and neither vault start nor vault migrate-storage ever reaches it. It
// succeeds with an error-level warning, because the bloc now runs on a vault
// without the old secrets and a person has to know where they went.
func (run *inceptionRun) archiveAndStartFresh(ctx context.Context, reason string) error {
	paths := run.paths

	archive, err := archiveAndForgetVault(paths, run.steps.now().Format(vaultArchiveTimeFormat), run.log)
	if err != nil {
		return fmt.Errorf("failed to archive the inception vault (moved so far: %q): %w", archive, err)
	}

	err = run.fresh(ctx)
	if errors.Is(err, ErrInceptionKeysNotSaved) {
		run.log.Errorw("Started a new, empty inception vault, but its keys were not saved; the previous one was kept, not deleted",
			"reason", reason, "archive", archive)

		return fmt.Errorf("the previous inception vault was kept at %s, and a new vault is running in its place: %w",
			archive, err)
	}

	if err != nil {
		return fmt.Errorf("the previous inception vault was kept at %s, but a new one could not be set up: %w", archive, err)
	}

	run.log.Errorw("Started a new, empty inception vault; the previous one was kept, not deleted",
		"reason", reason, "archive", archive)

	return nil
}

// fresh starts a new, empty vault, and stops it again if it fails to start.
//
// safe may have initialized the new vault and saved its root token in the
// bloc's target before the start failed, and the stop deletes that target.
// So the token is kept first, as before every other stop. When it cannot be
// kept, the new vault is left running and nothing is stopped.
func (run *inceptionRun) fresh(ctx context.Context) error {
	paths := run.paths

	err := run.steps.start(ctx, paths, run.tools, safeLocalFresh, run.log)
	if err == nil {
		run.createdVault = true

		return run.steps.finish(ctx, paths, safeLocalFresh, run.log)
	}

	err = fmt.Errorf("failed to start a new inception vault: %w", err)

	_, keepErr := keyfile.PreserveTargetToken(paths["rootKeyFile"], paths["vaultName"], run.steps.targetToken(paths),
		run.steps.now(), run.log)
	if keepErr != nil {
		return errors.Join(err, fmt.Errorf("the new vault was left running, because the root token in safe's target "+
			"could not be kept before stopping it: %w", keepErr))
	}

	return errors.Join(err, wrapStopAfterFailure(run.steps.stop(ctx, paths, run.log)))
}

// keyFileHasValue reports whether path holds anything besides whitespace.
// safe refuses an empty token file, and an empty unseal key opens nothing.
func keyFileHasValue(path string) (bool, error) {
	value, err := keyfile.Read(path)

	return value != "", err
}

// requireReadableKeyFiles refuses a run whose key files cannot be read,
// before anything is stopped or moved.
func requireReadableKeyFiles(paths map[string]string) error {
	for _, keyFile := range []string{paths["rootKeyFile"], paths["unsealKeysFile"]} {
		_, err := keyfile.ReadBytes(keyFile)
		if err != nil {
			return fmt.Errorf("%w; nothing was started, stopped, or changed", err)
		}
	}

	return nil
}

// inceptionKeysUsable reports whether the bloc has both keys a restart needs.
// Older releases kept both keys in one file without a bloc, and that file
// ended up holding only the root token, so a single shared file is never a
// pair.
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

	root, err := keyfile.Read(paths["rootKeyFile"])
	if err != nil {
		return false, err
	}

	unseal, err := keyfile.Read(paths["unsealKeysFile"])
	if err != nil {
		return false, err
	}

	return keyfile.CheckRootToken(root) == nil && checkSealKey(unseal) == nil, nil
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
			return false, keyfile.UnreadableError(keyFile, err)
		}
	}

	return false, nil
}
