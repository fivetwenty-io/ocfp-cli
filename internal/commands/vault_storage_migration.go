package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"go.uber.org/zap"
)

// safeLocalRaftNodeID is the raft node ID safe gives the single node of a
// `safe local --raft` vault, raftNodeID in safe's internal/cli/local_config.go.
// A store that `operator migrate` writes records the node ID it was given,
// and safe starts the node under its own constant, so the two must be equal
// or the restarted node will not find itself in its own configuration.
const safeLocalRaftNodeID = "safe-local"

const (
	// raftMigrationTimeout bounds one `operator migrate` run. An inception
	// vault holds a few hundred keys and copies in seconds, so a run this
	// long has hung rather than slowed.
	raftMigrationTimeout = 10 * time.Minute

	// migrationFileMode keeps the journal, config, and log private.
	migrationFileMode = 0o600

	// migrationDirMode is the mode of the staging directory, which becomes
	// the vault's data directory.
	migrationDirMode = 0o700

	// migrationAsideAttempts bounds the search for a free aside name.
	migrationAsideAttempts = 100

	// migratePhaseMigrating is the journal phase while the copy runs.
	migratePhaseMigrating = "migrating"

	// migratePhaseSwapping is the journal phase while the directories swap.
	migratePhaseSwapping = "swapping"

	// The suffixes of the data directory's siblings.
	migrationJournalSuffix = ".raft-migration.json"
	migrationStagingSuffix = ".raft-migrating"
	migrationBackupSuffix  = ".file-backup-"
	migrationFailedSuffix  = ".raft-failed-"
	migrationPartialSuffix = ".raft-partial-"

	// migrateUsage is in the help of every engine that has the command.
	migrateUsage = "operator migrate [options]"
	// migrateSucceeded is the line both engines print after a full copy.
	migrateSucceeded = "Success! All of the keys have been migrated."
	// migrateLocked is in the error both engines give while a lock from an
	// interrupted migration is still set in the source storage.
	migrateLocked = "storage migration in progress"
)

var (
	// ErrMigrationPathUnsafe reports a path that cannot be written into the
	// migrate config without changing its meaning.
	ErrMigrationPathUnsafe = errors.New("path cannot be written safely into the migrate config")

	// ErrEngineCannotMigrate reports an engine without `operator migrate`.
	ErrEngineCannotMigrate = errors.New("the vault engine cannot migrate storage")

	// ErrEngineCannotReadFile reports an engine that no longer reads file
	// storage, such as OpenBao 2.8 and later.
	ErrEngineCannotReadFile = errors.New("the vault engine cannot read file storage")

	// ErrRaftMigrationFailed reports a migration that did not produce a
	// complete raft store.
	ErrRaftMigrationFailed = errors.New("the file-to-raft migration failed")

	// ErrMigrationJournalInvalid reports a migration journal that cannot be
	// read, or that does not describe a migration ocfp could have left.
	ErrMigrationJournalInvalid = errors.New("the raft migration journal cannot be trusted")

	// ErrMigrationSwapUnclear reports an interrupted swap whose directories
	// do not match any point at which the swap could have stopped.
	ErrMigrationSwapUnclear = errors.New("the interrupted raft migration cannot be finished safely")

	// ErrMigrationAsideTaken reports that no free name was found to move a
	// staging directory aside to.
	ErrMigrationAsideTaken = errors.New("no free name to move the migration staging directory aside")
)

// migrationJournal records an in-flight migration beside the data directory,
// so a run that dies part way is resumed rather than mistaken for an empty
// bloc. It never holds a secret.
type migrationJournal struct {
	Phase   string `json:"phase"`
	Started string `json:"started"`
	Backup  string `json:"backup"`
}

// migrationLayout names the files and directories one migration uses.
type migrationLayout struct {
	data, journal, staging, backup, config, log, stamp string
}

// newMigrationLayout names a migration of the data directory in paths that
// starts at now.
func newMigrationLayout(paths map[string]string, now time.Time) migrationLayout {
	data := paths["vaultDir"]
	stamp := now.Format(vaultArchiveTimeFormat)

	return migrationLayout{
		data:    data,
		journal: data + migrationJournalSuffix,
		staging: data + migrationStagingSuffix,
		backup:  data + migrationBackupSuffix + stamp,
		config:  filepath.Join(paths["logDir"], "raft-migration-"+stamp+".hcl"),
		log:     filepath.Join(paths["logDir"], "raft-migration-"+stamp+".log"),
		stamp:   stamp,
	}
}

// migrateFileVaultToRaft copies the stopped file-backed vault in paths into a
// raft store with the engine safe will run, swaps the raft store into place,
// and returns where the file store was kept. It never deletes anything.
//
// The engine copies the encrypted storage byte for byte, so the saved unseal
// key and root token open the raft store too. A journal beside the data
// directory records each phase, and both renames of the swap stay inside one
// directory, so each is atomic and a run that dies part way can be finished.
// When the copy fails, the staging directory is moved aside and the data
// directory is left exactly as it was.
func migrateFileVaultToRaft(
	ctx context.Context, paths map[string]string, tools inceptionTools, log *zap.SugaredLogger,
) (string, error) {
	layout := newMigrationLayout(paths, time.Now())

	err := checkEngineCanMigrate(ctx, tools.engine)
	if err != nil {
		return "", err
	}

	cfg, err := renderRaftMigrationConfig(layout.data, layout.staging, paths["clusterPort"])
	if err != nil {
		return "", err
	}

	err = prepareMigration(paths, layout, cfg, log)
	if err != nil {
		return "", err
	}

	log.Infow("Migrating the inception vault from file to raft storage",
		"data", layout.data, "engine", tools.engine.path, "log", layout.log)

	err = copyFileStoreToRaft(ctx, tools.engine, layout, log)
	if err != nil {
		return "", abandonMigration(layout, err, log)
	}

	err = swapInRaftStore(layout)
	if err != nil {
		return "", err
	}

	log.Infow("Migrated the inception vault to raft storage", "data", layout.data, "file_backup", layout.backup)

	return layout.backup, nil
}

// checkEngineCanMigrate runs `<engine> operator migrate -h` and looks for the
// command's usage, before anything on disk changes.
func checkEngineCanMigrate(ctx context.Context, engine inceptionEngine) error {
	out, _ := exec.CommandContext(ctx, engine.path, "operator", "migrate", "-h").CombinedOutput() // #nosec G204 -- engine.path is the resolved engine binary

	if !strings.Contains(string(out), migrateUsage) {
		return fmt.Errorf("%w: %s has no `operator migrate`; point %s and PATH at an engine that has it, "+
			"such as HashiCorp Vault or OpenBao 2.7 or earlier", ErrEngineCannotMigrate, engine.path, safeEngineEnvVar)
	}

	return nil
}

// prepareMigration moves any stray staging directory aside, then writes the
// journal, the staging directory, and the config, in that order, so a crash
// at any point leaves a journal that says a migration began.
func prepareMigration(paths map[string]string, layout migrationLayout, cfg string, log *zap.SugaredLogger) error {
	err := moveStagingAside(layout, migrationPartialSuffix)
	if err != nil {
		return err
	}

	err = makeVaultLogDir(paths, log)
	if err != nil {
		return err
	}

	err = writeMigrationJournal(layout, migratePhaseMigrating)
	if err != nil {
		return err
	}

	err = os.Mkdir(layout.staging, migrationDirMode)
	if err != nil {
		return fmt.Errorf("failed to create the migration staging directory %s: %w", layout.staging, err)
	}

	err = os.WriteFile(layout.config, []byte(cfg), migrationFileMode)
	if err != nil {
		return fmt.Errorf("failed to write the migrate config %s: %w", layout.config, err)
	}

	return nil
}

// copyFileStoreToRaft runs the migration, resetting a lock left by an
// interrupted run once, and checks that the staging directory now holds a
// raft store.
func copyFileStoreToRaft(ctx context.Context, engine inceptionEngine, layout migrationLayout, log *zap.SugaredLogger) error {
	out, err := runEngineMigrate(ctx, engine, layout, "-config", layout.config)

	if err != nil && strings.Contains(out, migrateLocked) {
		log.Warnw("Resetting a migration lock left by an earlier, interrupted migration", "data", layout.data)

		// The locked attempt may already have written raft state, and the
		// engine will not migrate into a store that has state.
		err = moveStagingAside(layout, migrationPartialSuffix)
		if err == nil {
			err = os.Mkdir(layout.staging, migrationDirMode)
		}

		if err != nil {
			return fmt.Errorf("failed to clear the staging directory after a migration lock: %w", err)
		}

		_, _ = runEngineMigrate(ctx, engine, layout, "-reset", "-config", layout.config)
		out, err = runEngineMigrate(ctx, engine, layout, "-config", layout.config)
	}

	switch {
	case err != nil && isUnknownFileStorage(out):
		return fmt.Errorf("%w: %s cannot read the file-backed data in %s, which was left as it is; "+
			"point %s and PATH at HashiCorp Vault or at OpenBao 2.7 or earlier to migrate it",
			ErrEngineCannotReadFile, engine.path, layout.data, safeEngineEnvVar)
	case err != nil:
		return fmt.Errorf("%w: %s failed (%w), and the file-backed data in %s was left as it is; see %s",
			ErrRaftMigrationFailed, engine.path, err, layout.data, layout.log)
	case !strings.Contains(out, migrateSucceeded) || !completeRaftStore(layout.staging):
		return fmt.Errorf("%w: %s did not leave a complete raft store, and the file-backed data in %s was left as it is; see %s",
			ErrRaftMigrationFailed, engine.path, layout.data, layout.log)
	}

	return nil
}

// completeRaftStore reports whether dir holds both parts of a raft store,
// the vault.db that holds the data and the raft/ directory that holds the
// log, and nothing of file storage.
func completeRaftStore(dir string) bool {
	return classifyVaultData(dir) == vaultDataRaft &&
		entryMayExist(filepath.Join(dir, vaultRaftDBFile)) && dirMayExist(filepath.Join(dir, "raft"))
}

// runEngineMigrate runs one `operator migrate` and appends its output, which
// names storage keys and never their values, to the migration log.
func runEngineMigrate(ctx context.Context, engine inceptionEngine, layout migrationLayout, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, raftMigrationTimeout)
	defer cancel()

	var out bytes.Buffer

	cmd := exec.CommandContext(ctx, engine.path, append([]string{"operator", "migrate"}, args...)...) // #nosec G204 -- engine.path is the resolved engine binary and the args are fixed
	cmd.Stdout = &out
	cmd.Stderr = &out

	err := cmd.Run()

	logFile, openErr := os.OpenFile(layout.log, os.O_CREATE|os.O_WRONLY|os.O_APPEND, migrationFileMode) // #nosec G304 -- the log path is built from the bloc's log dir
	if openErr == nil {
		_, _ = fmt.Fprintf(logFile, "$ %s operator migrate %s\n%s\n", engine.path, strings.Join(args, " "), out.String())
		_ = logFile.Close()
	}

	if err != nil {
		return out.String(), fmt.Errorf("operator migrate %s: %w", strings.Join(args, " "), err)
	}

	return out.String(), nil
}

// isUnknownFileStorage reports whether migrate output says the engine has
// no file storage backend. OpenBao says `no Vault storage backend named:
// "file"`, and HashiCorp Vault says `unknown storage type file`.
func isUnknownFileStorage(out string) bool {
	return strings.Contains(out, `storage backend named: "file"`) ||
		strings.Contains(out, "unknown storage type file\n") ||
		strings.HasSuffix(strings.TrimSpace(out), "unknown storage type file")
}

// abandonMigration undoes a failed copy without touching the data directory:
// the staging directory moves aside and the journal goes. A failure to clean
// up is added to the error and leaves the journal for the next run to resume.
func abandonMigration(layout migrationLayout, cause error, log *zap.SugaredLogger) error {
	err := moveStagingAside(layout, migrationFailedSuffix)
	if err == nil {
		err = removeMigrationJournal(layout)
	}

	if err != nil {
		return errors.Join(cause, fmt.Errorf("failed to clean up after the migration: %w", err))
	}

	log.Errorw("The file-to-raft migration failed; the file-backed data was left as it is",
		"data", layout.data, "log", layout.log)

	return cause
}

// swapInRaftStore moves the file store to the backup name and the raft store
// into its place. The journal says swapping first, so a crash between the two
// renames is finished by the next run rather than read as an empty bloc.
func swapInRaftStore(layout migrationLayout) error {
	err := writeMigrationJournal(layout, migratePhaseSwapping)
	if err != nil {
		return err
	}

	err = renameRefusingExisting(layout.data, layout.backup)
	if err != nil {
		return fmt.Errorf("failed to keep the file store as %s: %w", layout.backup, err)
	}

	err = renameRefusingExisting(layout.staging, layout.data)
	if err != nil {
		return fmt.Errorf("failed to move the raft store from %s to %s: %w", layout.staging, layout.data, err)
	}

	return removeMigrationJournal(layout)
}

// moveStagingAside renames a staging directory, if there is one, to a free
// name with the given suffix. It never deletes one.
func moveStagingAside(layout migrationLayout, suffix string) error {
	_, err := os.Lstat(layout.staging)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("failed to inspect %s: %w", layout.staging, err)
	}

	aside, err := uniqueAsidePath(layout.data + suffix + layout.stamp)
	if err != nil {
		return err
	}

	return renameRefusingExisting(layout.staging, aside)
}

// uniqueAsidePath returns base, or base with the first free -N appended.
func uniqueAsidePath(base string) (string, error) {
	candidate := base

	for n := 2; n <= migrationAsideAttempts; n++ {
		_, err := os.Lstat(candidate)
		if errors.Is(err, fs.ErrNotExist) {
			return candidate, nil
		}

		if err != nil {
			return "", fmt.Errorf("failed to inspect %s: %w", candidate, err)
		}

		candidate = base + "-" + strconv.Itoa(n)
	}

	return "", fmt.Errorf("%w: %s", ErrMigrationAsideTaken, base)
}

// renameRefusingExisting renames from to to, refusing to replace anything at
// to, since rename(2) quietly replaces an empty directory.
func renameRefusingExisting(from, to string) error {
	_, err := os.Lstat(to)
	if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: %s", ErrVaultArchiveExists, to)
	}

	err = os.Rename(from, to)
	if err != nil {
		return fmt.Errorf("failed to rename %s to %s: %w", from, to, err)
	}

	return nil
}

// writeMigrationJournal records phase atomically, through a temporary file
// renamed over the journal.
func writeMigrationJournal(layout migrationLayout, phase string) error {
	data, err := json.Marshal(migrationJournal{Phase: phase, Started: layout.stamp, Backup: layout.backup})
	if err != nil {
		return fmt.Errorf("failed to encode the migration journal: %w", err)
	}

	tmp := layout.journal + ".tmp"

	err = os.WriteFile(tmp, append(data, '\n'), migrationFileMode)
	if err == nil {
		err = os.Rename(tmp, layout.journal)
	}

	if err != nil {
		return fmt.Errorf("failed to write the migration journal %s: %w", layout.journal, err)
	}

	return nil
}

// removeMigrationJournal removes the journal once the migration it records
// is finished or abandoned. It is ocfp's own note and holds no vault data.
func removeMigrationJournal(layout migrationLayout) error {
	err := os.Remove(layout.journal)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("failed to remove the migration journal %s: %w", layout.journal, err)
	}

	return nil
}

// renderRaftMigrationConfig renders the config `operator migrate` reads to
// copy the file storage at source into a new raft store at dest. The cluster
// address uses the bloc's cluster port, so the raft configuration records the
// address the restarted vault will listen on.
func renderRaftMigrationConfig(source, dest, clusterPort string) (string, error) {
	for _, path := range []string{source, dest} {
		err := checkMigrationPath(path)
		if err != nil {
			return "", err
		}
	}

	var b strings.Builder

	fmt.Fprintf(&b, "storage_source \"file\" {\n  path = %s\n}\n\n", strconv.Quote(source))
	fmt.Fprintf(&b, "storage_destination \"raft\" {\n  path    = %s\n  node_id = %s\n}\n\n",
		strconv.Quote(dest), strconv.Quote(safeLocalRaftNodeID))
	fmt.Fprintf(&b, "cluster_addr = %s\n", strconv.Quote("https://127.0.0.1:"+clusterPort))

	return b.String(), nil
}

// checkMigrationPath refuses a path the engines' HCL parsers could read as
// something else. HCL treats ${ and %{ inside a string as the start of a
// template, and a control character or invalid UTF-8 has no spelling every
// parser reads back the same way. A quote or backslash is fine, because
// strconv.Quote escapes both.
func checkMigrationPath(path string) error {
	if strings.Contains(path, "${") || strings.Contains(path, "%{") {
		return fmt.Errorf("%w: %q holds a template sequence", ErrMigrationPathUnsafe, path)
	}

	if !utf8.ValidString(path) || strings.ContainsFunc(path, unicode.IsControl) {
		return fmt.Errorf("%w: %q holds a control character or invalid UTF-8", ErrMigrationPathUnsafe, path)
	}

	return nil
}

// readMigrationJournal reads the journal beside the data directory, or
// returns nil when there is none. A journal that cannot be read, or whose
// phase or backup path is not one ocfp writes, is an error: ignoring it could
// read a half-swapped bloc as empty and start a fresh vault over it.
func readMigrationJournal(data string) (*migrationJournal, error) {
	path := data + migrationJournalSuffix

	raw, err := os.ReadFile(path) // #nosec G304 -- the journal sits beside the bloc's own data dir
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil //nolint:nilnil // no journal means no migration in flight
	}

	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrMigrationJournalInvalid, path, err)
	}

	var journal migrationJournal

	err = json.Unmarshal(raw, &journal)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrMigrationJournalInvalid, path, err)
	}

	if journal.Phase != migratePhaseMigrating && journal.Phase != migratePhaseSwapping {
		return nil, fmt.Errorf("%w: %s: unknown phase %q", ErrMigrationJournalInvalid, path, journal.Phase)
	}

	backupPrefix := filepath.Base(data) + migrationBackupSuffix

	if journal.Backup != filepath.Clean(journal.Backup) || filepath.Dir(journal.Backup) != filepath.Dir(data) ||
		!strings.HasPrefix(filepath.Base(journal.Backup), backupPrefix) ||
		len(filepath.Base(journal.Backup)) == len(backupPrefix) {
		return nil, fmt.Errorf("%w: %s: backup path %q is not a %s* sibling of %s",
			ErrMigrationJournalInvalid, path, journal.Backup, backupPrefix, data)
	}

	return &journal, nil
}

// abandonInterruptedCopy clears up after a copy that never finished. Such a
// run never touched the data directory, so its staging directory is kept as
// data.raft-partial-<ts> and the journal goes, and the next migration starts
// clean.
func abandonInterruptedCopy(paths map[string]string, journal *migrationJournal, log *zap.SugaredLogger) error {
	layout := newMigrationLayout(paths, time.Now())

	err := moveStagingAside(layout, migrationPartialSuffix)
	if err != nil {
		return err
	}

	log.Warnw("Moved aside an interrupted file-to-raft copy; the migration will run again",
		"data", layout.data, "started", journal.Started)

	return removeMigrationJournal(layout)
}

// finishInterruptedSwap completes a swap that a crash cut short and returns
// where the file store is kept. A crash can stop it at three points: before
// either rename, after the file store became the backup, or after both
// renames. Each is recognised by what sits where, and any other arrangement
// is refused with every path named and nothing moved.
func finishInterruptedSwap(paths map[string]string, journal *migrationJournal, log *zap.SugaredLogger) (string, error) {
	layout := newMigrationLayout(paths, time.Now())
	layout.backup = journal.Backup

	_, dataErr := os.Lstat(layout.data)
	dataGone := errors.Is(dataErr, fs.ErrNotExist)
	data := classifyVaultData(layout.data)
	staged := completeRaftStore(layout.staging)
	stagingThere := entryMayExist(layout.staging)
	backupThere := entryMayExist(layout.backup)
	backupIsFile := classifyVaultData(layout.backup) == vaultDataFile

	var err error

	switch {
	case data == vaultDataFile && staged && !backupThere:
		err = renameRefusingExisting(layout.data, layout.backup)
		if err == nil {
			err = renameRefusingExisting(layout.staging, layout.data)
		}
	case dataGone && staged && backupIsFile:
		err = renameRefusingExisting(layout.staging, layout.data)
	case data == vaultDataRaft && !stagingThere && backupIsFile:
	default:
		return "", fmt.Errorf("%w: data %s is %s, staging %s is %s, and backup %s is %s; inspect them by hand",
			ErrMigrationSwapUnclear, layout.data, data, layout.staging, stagingState(stagingThere, staged),
			layout.backup, classifyVaultData(layout.backup))
	}

	if err != nil {
		return "", err
	}

	log.Warnw("Finished an interrupted file-to-raft migration", "data", layout.data, "file_backup", layout.backup)

	return layout.backup, removeMigrationJournal(layout)
}

// stagingState describes a staging directory for an error message.
func stagingState(there, complete bool) string {
	switch {
	case complete:
		return "a complete raft store"
	case there:
		return "an incomplete raft store"
	default:
		return "absent"
	}
}
