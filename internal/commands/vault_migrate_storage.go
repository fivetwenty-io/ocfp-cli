package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/keyfile"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

// ErrMigrateStorageNoData reports a bloc with no vault data to migrate.
// migrate-storage never creates a vault.
var ErrMigrateStorageNoData = errors.New("the inception vault has no vault data to migrate")

// engineVersionTimeout bounds the '<engine> version' run behind the dry
// run's report, which only describes the engine.
const engineVersionTimeout = 10 * time.Second

// migrateStorageSteps are the actions 'ocfp vault migrate-storage' may take.
// The restart is vault start's own, so after the migration the vault comes
// back exactly the way 'ocfp vault start' brings it back, and nothing here
// can archive a vault, start a new one, or recover a key.
type migrateStorageSteps struct {
	// vaultStart is vault start's steps: the probe, the ownership check, the
	// stop, the cluster port check, and the restart with the saved keys.
	vaultStart vaultStartSteps
	// migrate copies the stopped file vault into raft storage, swaps the
	// raft store into place, and returns where the file store was kept.
	migrate func(ctx context.Context, paths map[string]string, log *zap.SugaredLogger) (string, error)
	// canMigrate reports why the engine cannot migrate storage, if it cannot.
	canMigrate func(ctx context.Context) error
	// engine is the engine safe will run, which also runs the migration.
	engine inceptionEngine
	// engineVersion describes the engine's version for the dry run's report.
	engineVersion func(ctx context.Context) string
	// rootTokenWorks reports why the running vault does not take the token
	// in root.key, if it does not.
	rootTokenWorks func(ctx context.Context, paths map[string]string) error
}

// newMigrateStorageSteps wires the real migrate-storage actions, using the
// safe and engine binaries the prerequisite check validated.
func newMigrateStorageSteps(tools inceptionTools) migrateStorageSteps {
	return migrateStorageSteps{
		vaultStart: newVaultStartSteps(tools),
		migrate: func(ctx context.Context, paths map[string]string, log *zap.SugaredLogger) (string, error) {
			return migrateFileVaultToRaft(ctx, paths, tools, log)
		},
		canMigrate: func(ctx context.Context) error {
			return checkEngineCanMigrateFileStorage(ctx, tools.engine)
		},
		engine: tools.engine,
		engineVersion: func(ctx context.Context) string {
			return inceptionEngineVersion(ctx, tools.engine)
		},
		rootTokenWorks: checkInceptionRootToken,
	}
}

// openBaoVersionPrefix starts the version line of every OpenBao build, even
// one installed under the name vault.
const openBaoVersionPrefix = "OpenBao v"

// The first OpenBao release that cannot read file storage is 2.8.
const (
	openBaoNoFileStorageMajor = 2
	openBaoNoFileStorageMinor = 8
)

// checkEngineCanMigrateFileStorage reports why the engine cannot migrate a
// file-storage vault, if it cannot. The engine needs 'operator migrate', and
// it must still read file storage, which OpenBao 2.8 and later do not. The
// second check reads the version the engine reports, because the copy is
// the only other step that finds out, and it runs after the vault is
// stopped. An engine whose version cannot be read is not refused here, and
// the copy still refuses it without touching the file store.
func checkEngineCanMigrateFileStorage(ctx context.Context, engine inceptionEngine) error {
	err := checkEngineCanMigrate(ctx, engine)
	if err != nil {
		return err
	}

	version := inceptionEngineVersion(ctx, engine)
	if !engineVersionDropsFileStorage(version) {
		return nil
	}

	return fmt.Errorf("%w: %s reports %s, and OpenBao 2.8 and later cannot read file storage; point %s and "+
		"PATH at HashiCorp Vault or at OpenBao 2.7 or earlier to migrate it, and nothing was stopped or changed",
		ErrEngineCannotReadFile, engine.path, version, safeEngineEnvVar)
}

// engineVersionDropsFileStorage reports whether a version line names an
// OpenBao release of 2.8 or later. A line it cannot read reports false.
func engineVersionDropsFileStorage(version string) bool {
	rest, isOpenBao := strings.CutPrefix(version, openBaoVersionPrefix)
	if !isOpenBao {
		return false
	}

	majorText, rest, found := strings.Cut(rest, ".")
	if !found {
		return false
	}

	// The minor number may run straight into a pre-release tag, as in 2.8-rc1.
	minorText := rest[:len(rest)-len(strings.TrimLeft(rest, "0123456789"))]

	major, err := strconv.Atoi(majorText)
	if err != nil {
		return false
	}

	minor, err := strconv.Atoi(minorText)
	if err != nil {
		return false
	}

	return major > openBaoNoFileStorageMajor ||
		(major == openBaoNoFileStorageMajor && minor >= openBaoNoFileStorageMinor)
}

// engineVersionUnknown stands for the version of an engine that does not say
// what it is.
const engineVersionUnknown = "version unknown"

// inceptionEngineVersion returns the first line of '<engine> version', or
// engineVersionUnknown when the engine does not say.
func inceptionEngineVersion(ctx context.Context, engine inceptionEngine) string {
	ctx, cancel := context.WithTimeout(ctx, engineVersionTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, engine.path, "version").Output() // #nosec G204 -- engine.path is the resolved engine binary
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")

	if err != nil || line == "" {
		return engineVersionUnknown
	}

	return strings.TrimSpace(line)
}

// migrateStorageAction is what migrate-storage decided to do.
type migrateStorageAction int

const (
	// migrateStorageRefuse means a check refused, and nothing is done.
	migrateStorageRefuse migrateStorageAction = iota
	// migrateStorageNothingToDo means the vault is already on raft storage.
	migrateStorageNothingToDo
	// migrateStorageCopy means the file store is migrated to raft.
	migrateStorageCopy
	// migrateStorageRetryCopy means an unfinished copy is set aside and the
	// file store is migrated again.
	migrateStorageRetryCopy
	// migrateStorageFinishSwap means an unfinished swap is completed.
	migrateStorageFinishSwap
)

// migrateStorageFindings is what migrate-storage's checks learned about the
// bloc. The dry run reports all of it.
type migrateStorageFindings struct {
	probe   vaultProbe
	data    vaultDataState
	journal *migrationJournal

	// ownerChecked reports that a vault answered and its owner was judged,
	// and owned that it is this bloc's own.
	ownerChecked bool
	owned        bool

	// keysChecked reports that the key files were read. rootKey and
	// unsealKey report a key file that holds something, and sharedKeyFile
	// that both keys point at one file.
	keysChecked   bool
	rootKey       bool
	unsealKey     bool
	sharedKeyFile bool

	// rootTokenChecked reports that the running vault was asked whether it
	// takes the token in root.key, and rootTokenErr is why it does not.
	rootTokenChecked bool
	rootTokenErr     error

	canMigrateChecked  bool
	canMigrateErr      error
	clusterPortChecked bool
	clusterPortErr     error

	action migrateStorageAction
}

// running reports whether a vault answers on the API port.
func (found *migrateStorageFindings) running() bool {
	return found.probe.state == vaultProbeVault
}

// open reports whether the vault that answers is initialized and unsealed.
func (found *migrateStorageFindings) open() bool {
	return found.running() && found.probe.initialized && !found.probe.sealed
}

// ownedByBloc reports that the vault's owner was checked and is this bloc.
func (found *migrateStorageFindings) ownedByBloc() bool {
	return found.ownerChecked && found.owned
}

// needsEngineMigrate reports whether what was found needs the engine's
// 'operator migrate', which only finishing an unfinished swap does not.
func (found *migrateStorageFindings) needsEngineMigrate() bool {
	if found.journal != nil {
		return found.journal.Phase == migratePhaseMigrating
	}

	return found.data == vaultDataFile
}

// migrateStorageRun is one pass of migrate-storage over one bloc.
type migrateStorageRun struct {
	paths map[string]string
	steps migrateStorageSteps
	log   *zap.SugaredLogger
	// out is where the command speaks to the operator: the dry run's report
	// and the outcome of a run that changed nothing.
	out io.Writer
}

// migrateInceptionVaultStorage is what 'ocfp vault migrate-storage' does. It
// moves the bloc's file-storage vault onto raft and brings it back, and it
// does nothing else.
//
// Every check that can refuse runs before anything is stopped or written: a
// stranger, or a vault this bloc cannot prove is its own, on the API port,
// data that is mixed or missing, a key file that is missing, empty, or
// shared, an open vault that does not take the token in root.key, an engine
// without 'operator migrate' or one whose version cannot read file storage,
// and a cluster port in use while the vault is stopped. A vault already on
// raft storage is left as it is. Otherwise the port is probed again, and
// nothing is stopped when what answers is not this bloc's own. The key files
// get vault start's value-preserving repairs, an open vault must have both
// keys saved in a valid shape, and the root token in safe's target is kept
// before the stop.
// The vault is then stopped and migrated with node_id safe-local, and
// restarted by vault start's restart. When the engine refuses a key after
// the migration, what was started is stopped and the run refuses, naming
// the file store to roll back to. Nothing here archives a vault, starts a
// new one, or recovers or replaces a key.
func migrateInceptionVaultStorage(ctx context.Context, run *migrateStorageRun) error {
	paths := run.paths
	start := run.steps.vaultStart

	found, err := run.plan(ctx)
	if err != nil {
		return err
	}

	if found.action == migrateStorageNothingToDo {
		_, _ = fmt.Fprintln(run.out, alreadyOnRaftMessage(paths, found))

		return nil
	}

	// The port is probed again before the key files are repaired, so a
	// refusal because it changed hands follows no write.
	err = requireOwnInceptionPortHolder(ctx, paths, start.probe, start.ownsVault, "migrate-storage")
	if err != nil {
		return err
	}

	err = prepareInceptionKeyFiles(ctx, paths, run.log)
	if err != nil {
		return err
	}

	err = requireOpenVaultKeysSaved(paths, found)
	if err != nil {
		return err
	}

	// Stopping deletes the bloc's safe target, which may hold the only copy
	// of the root token, so it is kept first, as vault start keeps it.
	_, err = keyfile.PreserveTargetToken(paths["rootKeyFile"], paths["vaultName"], start.targetToken(paths),
		start.now(), run.log)
	if err != nil {
		return fmt.Errorf("failed to keep the root token from safe's target before stopping the inception vault: %w", err)
	}

	err = start.stop(ctx, paths, run.log)
	if err != nil {
		return fmt.Errorf("failed to stop the inception vault before migrating it: %w", err)
	}

	if found.running() {
		// A running vault holds its own cluster port, so the port can be
		// judged only once that vault is down.
		err = start.clusterPortFree(ctx, paths["clusterPort"])
		if err != nil {
			return fmt.Errorf("the inception vault was stopped and not migrated: %w", clusterPortTakenError(paths, err))
		}
	}

	backup, err := run.moveToRaft(ctx, found)
	if err != nil {
		return err
	}

	err = restartInceptionVaultInPlace(ctx, &vaultStartRun{paths: paths, steps: start, log: run.log})
	if err != nil {
		return fmt.Errorf("%w: the raft data is in %s and the file store it came from is kept in %s, and the "+
			"restart, the same one 'ocfp vault start' runs, failed: %w",
			ErrRestartAfterMigrationFailed, paths["vaultDir"], backup, err)
	}

	_, _ = fmt.Fprintf(run.out, "The inception vault in %s is on raft storage now and running. Its file store is "+
		"kept in %s, and ocfp never removes it.\n", paths["vaultDir"], backup)

	return nil
}

// plan runs every check that can refuse before anything is stopped or
// written, and decides what to do. It reports what it found even when a
// check refuses, so the dry run can describe it.
func (run *migrateStorageRun) plan(ctx context.Context) (*migrateStorageFindings, error) {
	paths := run.paths
	found := &migrateStorageFindings{data: classifyVaultData(paths["vaultDir"])}
	found.probe = run.steps.vaultStart.probe(ctx, "http://127.0.0.1:"+paths["port"])

	journal, err := readMigrationJournal(paths["vaultDir"])
	if err != nil {
		return found, err
	}

	found.journal = journal

	err = requireMigratableData(paths, found)
	if err != nil || found.action == migrateStorageNothingToDo {
		return found, err
	}

	err = run.requireOwnVault(ctx, found)
	if err != nil {
		return found, err
	}

	err = requireMigrationKeys(paths, found)
	if err != nil {
		return found, err
	}

	err = run.requireWorkingRootToken(ctx, found)
	if err != nil {
		return found, err
	}

	switch {
	case journal == nil:
		found.action = migrateStorageCopy
	case journal.Phase == migratePhaseMigrating:
		found.action = migrateStorageRetryCopy
	default:
		found.action = migrateStorageFinishSwap
	}

	if found.needsEngineMigrate() {
		found.canMigrateChecked = true
		found.canMigrateErr = run.steps.canMigrate(ctx)

		if found.canMigrateErr != nil {
			found.action = migrateStorageRefuse

			return found, found.canMigrateErr
		}
	}

	if !found.running() {
		found.clusterPortChecked = true
		found.clusterPortErr = run.steps.vaultStart.clusterPortFree(ctx, paths["clusterPort"])

		if found.clusterPortErr != nil {
			found.action = migrateStorageRefuse

			return found, clusterPortTakenError(paths, found.clusterPortErr)
		}
	}

	return found, nil
}

// requireMigratableData refuses data that migrate-storage cannot move, and
// marks a vault already on raft storage as having nothing to do.
func requireMigratableData(paths map[string]string, found *migrateStorageFindings) error {
	dir := paths["vaultDir"]

	switch {
	case found.data == vaultDataMixed:
		return fmt.Errorf("%w: %s; inspect it by hand, ocfp will not touch it", ErrVaultDataMixed, dir)
	case found.journal != nil && found.journal.Phase == migratePhaseMigrating && found.data != vaultDataFile:
		return fmt.Errorf("%w: it records an unfinished copy, but %s is %s storage rather than file; "+
			"inspect it by hand", ErrMigrationJournalInvalid, dir+migrationJournalSuffix, found.data)
	case found.journal != nil:
		return nil
	case found.data == vaultDataRaft:
		found.action = migrateStorageNothingToDo

		return nil
	case found.data == vaultDataAbsent:
		return fmt.Errorf("%w: %s holds no vault data, and migrate-storage never creates a vault; "+
			"'ocfp vault inception' starts a new one", ErrMigrateStorageNoData, dir)
	}

	return nil
}

// requireOwnVault refuses a port held by anything but this bloc's own vault.
// Without raft data, a vault is this bloc's only when it runs under a pane of
// the bloc's tmux session, so one started by hand outside that session is
// refused, and the operator stops it first.
func (run *migrateStorageRun) requireOwnVault(ctx context.Context, found *migrateStorageFindings) error {
	paths := run.paths

	switch found.probe.state {
	case vaultProbeStopped:
		return nil
	case vaultProbeStranger:
		return inceptionPortTakenError(paths, "something that is not a vault answers there, so migrate-storage "+
			"stopped nothing")
	case vaultProbeVault:
	}

	stopFirst := fmt.Sprintf("a vault that does not run under tmux session %s, as one started by hand does not, "+
		"cannot be proven to be this bloc's; if it is, stop it yourself and run the command again, and "+
		"migrate-storage stopped nothing", paths["tmuxSession"])

	owned, err := run.steps.vaultStart.ownsVault(ctx, paths, found.data)
	if err != nil {
		return fmt.Errorf("cannot tell whose vault answers on port %s; %s: %w", paths["port"], stopFirst, err)
	}

	found.ownerChecked = true
	found.owned = owned

	if !owned {
		return inceptionPortTakenError(paths, "a vault answers there that this bloc cannot prove is its own; "+stopFirst)
	}

	return nil
}

// requireMigrationKeys refuses a migration unless root.key and unseal.keys
// are separate files that both hold something, because the vault is
// reopened with them right after it. migrate-storage never recovers or
// replaces a key.
func requireMigrationKeys(paths map[string]string, found *migrateStorageFindings) error {
	const advice = "migrate-storage never recovers or replaces a key, so restore it from a backup or from the " +
		"vault logs and run the command again, or run 'ocfp vault inception', which can recover it from the " +
		"vault logs; nothing was stopped or changed"

	found.keysChecked = true

	if paths["rootKeyFile"] == paths["unsealKeysFile"] {
		found.sharedKeyFile = true

		return fmt.Errorf("%w: the root token and the unseal key share %s, which cannot hold both; %s",
			ErrVaultStartKeyMissing, paths["rootKeyFile"], advice)
	}

	var err error

	found.rootKey, err = keyFileHasValue(paths["rootKeyFile"])
	if err != nil {
		return err
	}

	found.unsealKey, err = keyFileHasValue(paths["unsealKeysFile"])
	if err != nil {
		return err
	}

	var missing []string

	if !found.rootKey {
		missing = append(missing, paths["rootKeyFile"])
	}

	if !found.unsealKey {
		missing = append(missing, paths["unsealKeysFile"])
	}

	if len(missing) > 0 {
		return fmt.Errorf("%w: %s; %s", ErrVaultStartKeyMissing, strings.Join(missing, " and "), advice)
	}

	return nil
}

// checkRootToken asks an open vault whether it takes the token in root.key,
// once, and records the answer. A sealed or stopped vault cannot be asked,
// and a root.key that is not the shape of a token is left to the check of
// the saved keys, which refuses it before any stop. The token goes only to a
// vault whose ownership was checked and found to be this bloc's, because
// anything else that answers on the port could be any local process.
func (run *migrateStorageRun) checkRootToken(ctx context.Context, found *migrateStorageFindings) {
	if found.rootTokenChecked || !found.open() || !found.rootKey || !found.ownedByBloc() {
		return
	}

	token, err := keyfile.Read(run.paths["rootKeyFile"])
	if err != nil || keyfile.CheckRootToken(token) != nil {
		return
	}

	found.rootTokenChecked = true
	found.rootTokenErr = run.steps.rootTokenWorks(ctx, run.paths)
}

// requireWorkingRootToken refuses to stop an open vault that does not take
// the token in root.key, or cannot say whether it does. The restart after
// the migration opens the vault with that token, so such a vault would stay
// down. The error names safe's target when it holds a different token, and
// every copy of a token ocfp kept beside root.key, since one of them may be
// the token the vault takes. Nothing is written, and no token is quoted.
func (run *migrateStorageRun) requireWorkingRootToken(ctx context.Context, found *migrateStorageFindings) error {
	run.checkRootToken(ctx, found)

	if found.rootTokenErr == nil {
		return nil
	}

	return fmt.Errorf("%w%s", found.rootTokenErr,
		rootTokenRefusedAdvice(run.paths, "migrate-storage", run.steps.vaultStart.targetToken(run.paths)))
}

// requireOpenVaultKeysSaved refuses to stop an open vault unless both keys
// are saved in a valid shape. An open vault serves its secrets, and once it
// stops it can be reopened only with both keys.
func requireOpenVaultKeysSaved(paths map[string]string, found *migrateStorageFindings) error {
	if !found.open() {
		return nil
	}

	saved, err := inceptionKeysSaved(paths)
	if err != nil || saved {
		return err
	}

	return fmt.Errorf("%w: the vault at %s is still running, but %s and %s do not both hold a valid key, so "+
		"migrate-storage stopped nothing; run 'ocfp vault inception' while it runs, which can recover them; %s",
		ErrInceptionKeysNotSaved, "http://127.0.0.1:"+paths["port"], paths["rootKeyFile"], paths["unsealKeysFile"],
		vaultLogHint(paths))
}

// moveToRaft does the migration the plan chose, once the vault is stopped,
// and returns where the file store is kept. A failure leaves the vault
// stopped and the file store where the migration code left it.
func (run *migrateStorageRun) moveToRaft(ctx context.Context, found *migrateStorageFindings) (string, error) {
	paths := run.paths

	if found.action == migrateStorageFinishSwap {
		backup, err := finishInterruptedSwap(paths, found.journal, run.log)
		if err != nil {
			return "", fmt.Errorf("the inception vault was stopped and not restarted: %w", err)
		}

		return backup, nil
	}

	if found.action == migrateStorageRetryCopy {
		err := abandonInterruptedCopy(paths, found.journal, run.log)
		if err != nil {
			return "", fmt.Errorf("the inception vault was stopped and not restarted: %w", err)
		}
	}

	backup, err := run.steps.migrate(ctx, paths, run.log)
	if err != nil {
		return "", fmt.Errorf("the inception vault was stopped for its migration and not restarted, and its "+
			"file-backed data in %s was left as it is: %w", paths["vaultDir"], err)
	}

	return backup, nil
}

// clusterPortTakenError says why the cluster port cannot be used.
func clusterPortTakenError(paths map[string]string, err error) error {
	return fmt.Errorf("%w: cluster port %s (API port %s plus 1000): %w; free it, or set %s to move both ports",
		ErrInceptionClusterPortTaken, paths["clusterPort"], paths["port"], err, inceptionPortEnvVar)
}

// alreadyOnRaftMessage says that a vault on raft storage has nothing to
// migrate, and how to start it when it is not serving.
func alreadyOnRaftMessage(paths map[string]string, found *migrateStorageFindings) string {
	msg := fmt.Sprintf("The inception vault in %s is already on raft storage, so there is nothing to migrate, "+
		"and nothing was changed.", paths["vaultDir"])

	if !found.open() {
		msg += " It is not serving, and 'ocfp vault start' for this bloc brings it back with its saved keys."
	}

	return msg
}

// reportInceptionVaultStorage is migrate-storage's dry run. It runs the same
// checks as the real run and reports what they found and what the real run
// would do. It stops, starts, and writes nothing, and it never prints a key.
// It fails with the same error the real run would refuse with.
func reportInceptionVaultStorage(ctx context.Context, run *migrateStorageRun) error {
	paths := run.paths

	found, planErr := run.plan(ctx)

	// A refusal can come before the keys, the engine, or the cluster port
	// was looked at, and the report covers each of them whenever it matters.
	// Reading the key files only asks whether each one holds something.
	if !found.keysChecked && found.data != vaultDataAbsent {
		_ = requireMigrationKeys(paths, found)
	}

	run.checkRootToken(ctx, found)

	if !found.canMigrateChecked && found.needsEngineMigrate() {
		found.canMigrateChecked = true
		found.canMigrateErr = run.steps.canMigrate(ctx)
	}

	if !found.clusterPortChecked && !found.running() && found.action != migrateStorageNothingToDo {
		found.clusterPortChecked = true
		found.clusterPortErr = run.steps.vaultStart.clusterPortFree(ctx, paths["clusterPort"])
	}

	var b strings.Builder

	b.WriteString("Inception vault storage, dry run: nothing was stopped, started, or written.\n\n")

	for _, line := range run.reportLines(ctx, found) {
		fmt.Fprintf(&b, "  %s\n", line)
	}

	b.WriteString("\n")
	b.WriteString(planSentence(paths, found, planErr, run.steps.engine))
	b.WriteString("\n")

	_, _ = io.WriteString(run.out, b.String())

	return planErr
}

// reportLines describes, one line each, what the dry run found.
func (run *migrateStorageRun) reportLines(ctx context.Context, found *migrateStorageFindings) []string {
	paths := run.paths
	version := run.steps.engineVersion(ctx)

	lines := []string{
		"data: " + paths["vaultDir"] + " (" + describeVaultData(found.data) + ")",
		"journal: " + describeJournal(paths, found.journal),
		"root token: " + paths["rootKeyFile"] + " (" + describeRootKey(found) + ")",
		"unseal key: " + paths["unsealKeysFile"] + " (" + describeKey(found, found.unsealKey) + ")",
		fmt.Sprintf("engine: %s at %s, %s", run.steps.engine.name, run.steps.engine.path, version),
		"can migrate: " + describeCanMigrate(found, version),
		"API port: " + paths["port"] + ", " + describePort(found),
		"tmux session: " + paths["tmuxSession"] + " (" + describeSession(ctx, run, paths) + ")",
		"cluster port: " + paths["clusterPort"] + ", " + describeClusterPort(found),
	}

	switch found.action {
	case migrateStorageCopy, migrateStorageRetryCopy:
		lines = append(lines, "file backup: "+newMigrationLayout(paths, run.steps.vaultStart.now()).backup+
			" (would be created, and never removed)")
	case migrateStorageFinishSwap:
		lines = append(lines, "file backup: "+found.journal.Backup+" (named by the journal)")
	case migrateStorageRefuse, migrateStorageNothingToDo:
	}

	return lines
}

func describeVaultData(data vaultDataState) string {
	switch data {
	case vaultDataAbsent:
		return "no vault data"
	case vaultDataMixed:
		return "both file and raft storage, or an entry that could not be examined"
	case vaultDataFile, vaultDataRaft:
	}

	return data.String() + " storage"
}

func describeJournal(paths map[string]string, journal *migrationJournal) string {
	if journal == nil {
		return "none"
	}

	if journal.Phase == migratePhaseMigrating {
		return paths["vaultDir"] + migrationJournalSuffix + " records an unfinished copy"
	}

	return paths["vaultDir"] + migrationJournalSuffix + " records an unfinished swap"
}

func describeKey(found *migrateStorageFindings, held bool) string {
	switch {
	case !found.keysChecked:
		return "not checked"
	case found.sharedKeyFile:
		return "shared by both keys, which cannot be"
	case held:
		return "present"
	default:
		return "missing or empty"
	}
}

// describeRootKey describes root.key, and what the running vault said about
// its token when it was asked.
func describeRootKey(found *migrateStorageFindings) string {
	held := describeKey(found, found.rootKey)

	switch {
	case !found.rootTokenChecked && found.open() && found.rootKey && !found.ownedByBloc():
		return held + ", and the token was not checked, because the vault that answers is not proven to be this bloc's"
	case !found.rootTokenChecked:
		return held
	case found.rootTokenErr == nil:
		return held + ", and the running vault takes it"
	case errors.Is(found.rootTokenErr, ErrInceptionRootTokenRefused):
		return held + ", but the running vault refuses it"
	default:
		return held + ", but the running vault could not say whether it takes it"
	}
}

// describeCanMigrate says whether the engine can migrate. An engine whose
// version cannot be read is not refused, because the version check exists
// only to refuse the releases known to drop file storage, so the report says
// a real run will try and that the copy is the step that finds out.
func describeCanMigrate(found *migrateStorageFindings, version string) string {
	switch {
	case !found.canMigrateChecked:
		return "not needed"
	case found.canMigrateErr != nil:
		return "no, " + found.canMigrateErr.Error()
	case version == engineVersionUnknown:
		return "it has 'operator migrate', but ocfp cannot read its version, so a real run will try the copy, " +
			"and an engine that cannot read file storage then fails the copy and leaves the vault stopped on its unchanged file store"
	default:
		return "yes, it has 'operator migrate', and its version is not OpenBao 2.8 or later, " +
			"which cannot read file storage"
	}
}

func describePort(found *migrateStorageFindings) string {
	switch found.probe.state {
	case vaultProbeStopped:
		return "no vault answers"
	case vaultProbeStranger:
		return "something that is not a vault answers"
	case vaultProbeVault:
	}

	if !found.ownerChecked {
		return "a vault answers, whose owner was not checked"
	}

	if !found.owned {
		return "a vault answers that this bloc cannot prove is its own"
	}

	switch {
	case !found.probe.initialized:
		return "this bloc's own vault, not initialized"
	case found.probe.sealed:
		return "this bloc's own vault, sealed"
	default:
		return "this bloc's own vault, open and serving"
	}
}

func describeSession(ctx context.Context, run *migrateStorageRun, paths map[string]string) string {
	if run.steps.vaultStart.hasSession(ctx, paths) {
		return "exists"
	}

	return "absent"
}

func describeClusterPort(found *migrateStorageFindings) string {
	switch {
	case found.clusterPortChecked && found.clusterPortErr != nil:
		return "in use, " + found.clusterPortErr.Error()
	case found.clusterPortChecked:
		return "free"
	case found.running():
		return "held by the running vault until it stops, so it is checked after the stop"
	default:
		return "not checked"
	}
}

// planSentence says what the real run would do with what the dry run found.
func planSentence(paths map[string]string, found *migrateStorageFindings, planErr error, engine inceptionEngine) string {
	if planErr != nil {
		return "migrate-storage would refuse: " + planErr.Error()
	}

	if found.action == migrateStorageNothingToDo {
		return alreadyOnRaftMessage(paths, found)
	}

	stop := "migrate-storage would keep the root token from safe's target, clear what is left of this bloc's " +
		"stopped vault, such as its tmux session and safe target,"
	if found.running() {
		stop = "migrate-storage would stop this bloc's vault on port " + paths["port"] +
			" once it has kept the root token from safe's target,"
	}

	then := "and then restart the vault with its saved keys, the way 'ocfp vault start' does."

	switch found.action {
	case migrateStorageFinishSwap:
		return fmt.Sprintf("%s finish the unfinished swap so that the raft store is in %s and the file store in %s, %s",
			stop, paths["vaultDir"], found.journal.Backup, then)
	case migrateStorageRetryCopy:
		stop += " set aside the unfinished copy in " + paths["vaultDir"] + migrationStagingSuffix + ","
	case migrateStorageCopy, migrateStorageRefuse, migrateStorageNothingToDo:
	}

	return fmt.Sprintf("%s migrate %s to raft storage with '%s operator migrate' and node_id %s, keep the file "+
		"store beside it, %s", stop, paths["vaultDir"], engine.path, safeLocalRaftNodeID, then)
}

// migrateStorageDryRunFlag makes migrate-storage report what it would do
// without stopping, starting, or writing anything.
const migrateStorageDryRunFlag = "dry-run"

// newVaultMigrateStorageCmd creates the vault migrate-storage subcommand.
func newVaultMigrateStorageCmd() *cobra.Command {
	var dryRun bool

	cmd := &cobra.Command{ //nolint:exhaustruct // Using zero values for optional fields
		Use:   "migrate-storage",
		Short: "Move this bloc's inception vault from file storage to raft",
		Long: `Move this bloc's existing inception vault from file storage to raft
storage, and bring it back with its saved keys.

The command does by itself what an operator would otherwise do by hand. It
stops the bloc's vault, migrates its data with the engine's 'operator
migrate' and node_id safe-local, keeps the file store beside the data
directory as data.file-backup-<timestamp>, and restarts the vault the same
way 'ocfp vault start' does. The data directory, the key files, the ports,
and the tmux session all stay where they were.

  - A vault that is already on raft storage is left as it is, and the
    command says so and exits 0.
  - Every check that can refuse runs before anything is stopped. The command
    refuses when the bloc has no vault data or has both file and raft data,
    when either key file is missing, empty, or shared with the other, when
    the engine has no 'operator migrate' or reports a version that cannot
    read file storage, as OpenBao 2.8 and later cannot, and when the cluster
    port is in use. An engine whose version cannot be read is not refused,
    so the copy is the step that finds out, and a failed copy leaves the
    vault stopped on its unchanged file store.
  - The command stops a running vault only when it can prove that the vault
    is this bloc's own, which for file storage means that it runs under the
    bloc's tmux session. It refuses a vault that was started by hand outside
    that session, and the operator stops that vault first.
  - An open vault is stopped only when both of its keys are saved in a valid
    shape and the vault takes the token in root.key, because the restart
    opens it with that token. The root token in safe's target is kept
    before the stop.
  - When the engine refuses a saved key after the migration, the command
    stops what it started and refuses. It never archives the vault or starts
    a new one, and its message names the file store to roll back to.
  - A migration that an earlier run left unfinished is completed. An
    unfinished copy is set aside and made again, and an unfinished swap is
    finished.

With --dry-run, the command stops, starts, and writes nothing. It reports
the storage, the paths, the engine and whether it can migrate, the ports,
whether both keys are present, and whether an open vault takes the token in
root.key, and then says what a real run would do.
It exits non-zero when a real run would refuse.

docs/inception-vault.md describes the migration and how to roll it back.`,
		Example: `  # See what the migration would do, without changing anything
  ocfp vault migrate-storage --bloc production --dry-run

  # Move the bloc's inception vault to raft storage
  ocfp vault migrate-storage --bloc production`,
		RunE: func(cmd *cobra.Command, _args []string) error {
			return runVaultMigrateStorage(cmd.OutOrStdout(), dryRun)
		},
	}

	cmd.Flags().BoolVar(&dryRun, migrateStorageDryRunFlag, false,
		"report what the migration would do, and stop, start, or write nothing")

	return cmd
}

// runVaultMigrateStorage executes the vault migrate-storage command. A real
// run holds the bloc's inception vault lock throughout. A dry run takes no
// lock, because taking it would write the lock file. A bloc whose directory
// does not resolve, such as one with a vault in both of its directories, is
// refused before either run takes the lock or looks at a vault.
func runVaultMigrateStorage(out io.Writer, dryRun bool) error {
	blocName := viper.GetString("bloc")

	paths, err := getVaultInceptionPaths(blocName, viper.GetBool("test"))
	if err != nil {
		return fmt.Errorf("migrate-storage refused, and nothing was stopped or changed: %w", err)
	}

	if dryRun {
		return migrateInceptionVaultStorageWith(blocName, paths, out, reportInceptionVaultStorage)
	}

	return withInceptionVaultLock(paths, func() error {
		return migrateInceptionVaultStorageWith(blocName, paths, out, migrateInceptionVaultStorage)
	})
}

// migrateInceptionVaultStorageWith checks the prerequisites and runs one
// migrate-storage pass, the real one or the dry run.
func migrateInceptionVaultStorageWith(blocName string, paths map[string]string, out io.Writer,
	pass func(ctx context.Context, run *migrateStorageRun) error,
) error {
	log := logger.Get()

	log.Infow("=== Migrating the OCFP inception vault's storage ===",
		"bloc", blocName,
		"vault_name", paths["vaultName"],
		"port", paths["port"],
		"cluster_port", paths["clusterPort"],
		"tmux_session", paths["tmuxSession"],
		"vault_dir", paths["vaultDir"],
	)

	err := requireClusterPort(paths)
	if err != nil {
		return err
	}

	tools, err := checkVaultInceptionPrerequisites(context.TODO(), log)
	if err != nil {
		return fmt.Errorf("prerequisite check failed: %w", err)
	}

	return pass(context.TODO(), &migrateStorageRun{
		paths: paths,
		steps: newMigrateStorageSteps(tools),
		log:   log,
		out:   out,
	})
}
