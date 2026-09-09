package commands

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/spf13/cobra"
)

// Directory permission conventions used when creating destination parent
// directories during migration. configDataDirMode/stateDirMode match the
// XDG-class base directories elsewhere in this codebase (see
// internal/config/state.go's stateDirMode); keysDirMode is applied to the
// migrated keys directory itself, since SSH private keys warrant tighter
// permissions than the rest of the data class (see
// internal/bastion/ssh's sshDirectoryMode).
const (
	migrateDirMode os.FileMode = 0o750
	keysDirMode    os.FileMode = 0o700

	// copyMigrateDirWorkingMode is the permission a destination directory
	// is created with while copyMigrateDir is still populating it during
	// the EXDEV cross-device copy fallback. Using the source directory's
	// own mode here would break the copy outright for a read-only (e.g.
	// 0500) source: its children could never be written into the
	// newly-created destination. copyMigrateDir always chmods the
	// destination to the source's real mode once every child has been
	// copied, so this working mode is never the final on-disk permission.
	copyMigrateDirWorkingMode os.FileMode = 0o700
)

// migrateEntryClass classifies a top-level entry of the legacy ~/.ocfp
// directory into the XDG base-directory class it belongs under.
type migrateEntryClass int

const (
	migrateClassConfig migrateEntryClass = iota
	migrateClassState
	migrateClassDataKeys
	migrateClassDataOther
)

// migrateKnownDirs and migrateKnownFiles classify the legacy ~/.ocfp
// top-level entries this command recognizes. Any directory not listed here
// is treated as an unclassified per-bloc data directory (migrateClassDataOther).
// Files are matched by exact name here, or by the migrateConfigFilePrefix
// rule in classifyMigrateEntry; any other file is left in place and
// reported as a warning, since guessing at its purpose risks silently
// misplacing operator data.
var migrateKnownDirs = map[string]migrateEntryClass{
	"configs":     migrateClassConfig,
	"logs":        migrateClassState,
	"state":       migrateClassState,
	"checkpoints": migrateClassState,
	"backups":     migrateClassState,
	".active":     migrateClassState,
	"keys":        migrateClassDataKeys,
}

// migrateBlocStateSubdirs lists the state-class subdirectory names split
// out of an otherwise unclassified per-bloc directory before the rest of
// that directory moves to DataHome(). state.GetStateDir and every
// logger/vault log resolver read a bloc's state and logs from under
// StateHome()/{bloc}/, never DataHome()/{bloc}/, so a wholesale move of
// the bloc directory into DataHome() would silently orphan them: no
// resolver in the codebase ever looks there.
var migrateBlocStateSubdirs = []string{"state", "logs"}

// migrateConfigFilePrefix marks every top-level file that is an alternate
// or backed-up configuration (config.pve.yml, config.yml.bak-20260719, ...)
// as config-class. They belong beside config.yml for two reasons: operators
// pass them with -f and expect them where config.yml went, and config.yml
// itself is often a relative symlink to one of them, which would dangle if
// the link moved and its target stayed behind.
const migrateConfigFilePrefix = "config."

var migrateKnownFiles = map[string]migrateEntryClass{
	"config.yml":             migrateClassConfig,
	"state.yml":              migrateClassState,
	"state.yml.lock":         migrateClassState,
	"provisioned":            migrateClassState,
	"bastion-init-completed": migrateClassState,
}

// Sentinel errors for the migrate command.
var (
	// ErrMigrateOCFPHomeOverrideActive is returned when OCFP_HOME is set:
	// the flat legacy layout is an intentional, explicit override, so
	// there is nothing to migrate and migrating anyway would fight the
	// operator's own configuration.
	ErrMigrateOCFPHomeOverrideActive = errors.New(
		"OCFP_HOME is set: the legacy flat layout is an explicit override, nothing to migrate")

	// ErrMigrateLegacyNotDirectory is returned when the legacy path exists
	// but is not a directory.
	ErrMigrateLegacyNotDirectory = errors.New("legacy ~/.ocfp path exists but is not a directory")

	// ErrMigrateLiveProcessActive is returned when another ocfp process
	// holds a live command lock in the legacy or XDG-state .active
	// directory. Moving
	// state.yml.lock, .active/, and a bloc's state/logs directories out
	// from under a running command would corrupt its lock tracking, and,
	// if that command's own bloc is mid-migration, relocate the directory
	// it is actively reading from or writing to.
	ErrMigrateLiveProcessActive = errors.New(
		"refusing to migrate while another ocfp process is running; stop it first")
)

// ErrMigrateHasConflicts is the sentinel every conflict error returned by
// newMigrateConflictsError wraps, so callers can match it with errors.Is
// regardless of which destination paths conflicted.
var ErrMigrateHasConflicts = errors.New("refusing to migrate: destination path(s) already exist")

// newMigrateConflictsError builds an error listing every destination path
// that already exists, wrapping ErrMigrateHasConflicts, so the migration
// can refuse to start rather than risk overwriting existing content.
func newMigrateConflictsError(conflicts []string) error {
	return fmt.Errorf("%w:\n  %s", ErrMigrateHasConflicts, strings.Join(conflicts, "\n  "))
}

// migratePlanEntry is one legacy ~/.ocfp top-level entry and its resolved
// XDG-class destination.
type migratePlanEntry struct {
	name  string
	src   string
	dst   string
	class migrateEntryClass
}

// migrateDeprecationNotice is printed by the hidden top-level 'migrate'
// alias so operators with the old spelling in scripts or muscle memory
// learn the new one without the command breaking under them.
const migrateDeprecationNotice = "use 'ocfp config migrate' instead"

// NewMigrateCmd returns the hidden, deprecated top-level 'migrate' alias.
// The command lives under 'ocfp config' now (see NewConfigCmd); this alias
// keeps the original spelling working while announcing the move.
func NewMigrateCmd() *cobra.Command {
	cmd := newConfigMigrateCmd()
	cmd.Hidden = true
	cmd.Deprecated = migrateDeprecationNotice

	return cmd
}

// newConfigMigrateCmd creates the 'config migrate' command, which moves an
// existing legacy ~/.ocfp layout into the three XDG base directories
// (config, state, data) so path resolution lands on the XDG layout
// naturally afterward.
func newConfigMigrateCmd() *cobra.Command {
	var dryRun bool

	cmd := &cobra.Command{
		Use:          "migrate",
		Short:        "Migrate legacy ~/.ocfp layout to XDG directories",
		SilenceUsage: true,
		Long: `Migrate moves the contents of the legacy ~/.ocfp directory into the three
XDG base directories OCFP now reads from and writes to:

  config.yml, other config.* files, configs/                 -> XDG_CONFIG_HOME/ocfp
  state.yml, state.yml.lock, state/, logs/, checkpoints/,
  backups/, .active/, provisioned, bastion-init-completed      -> XDG_STATE_HOME/ocfp
  keys/, and any other per-bloc directory                      -> XDG_DATA_HOME/ocfp

A per-bloc directory's own state/ and logs/ subdirectories are split out to
XDG_STATE_HOME/ocfp/<bloc>/ first, since that is where state and log lookups
resolve them from; the rest of the bloc directory (keys, vault data, etc.)
moves to XDG_DATA_HOME/ocfp/<bloc>.

After a successful migration the legacy directory is removed if it is
empty. Any file at the top level of ~/.ocfp that this command does not
recognize is left in place and reported; the legacy directory is retained
in that case so nothing is lost.

The command refuses to run when OCFP_HOME is set, since that variable is
an explicit request for the legacy flat layout. It also refuses to run
while another ocfp command is still running, because that command's lock
and log files are among the things being moved.

Destination directories that already exist are merged: ordinary ocfp use
writes command locks and per-bloc logs under the XDG state directory long
before a migration, and that content is kept alongside what moves in. The
command refuses to move anything only when a file would be overwritten,
listing every such conflict so you can resolve them manually; no files are
moved when a conflict is found.`,
		Example: `  # Preview what would move, without changing anything
  ocfp config migrate --dry-run

  # Migrate the legacy ~/.ocfp layout to XDG directories
  ocfp config migrate`,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runMigrate(dryRun)
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the move plan without changing anything on disk")

	return cmd
}

// runMigrate is the migrate command's execution logic, separated from the
// cobra.Command wiring so it can be exercised directly in tests.
func runMigrate(dryRun bool) error {
	if os.Getenv("OCFP_HOME") != "" {
		return ErrMigrateOCFPHomeOverrideActive
	}

	legacyDir := config.LegacyHome()
	if legacyDir == "" {
		return config.ErrOcfpHomeNotFound
	}

	info, err := os.Stat(legacyDir)
	if err != nil {
		if os.IsNotExist(err) {
			_, _ = fmt.Fprintf(os.Stdout, "nothing to migrate: %s does not exist\n", legacyDir)

			return nil
		}

		return fmt.Errorf("checking legacy directory %s: %w", legacyDir, err)
	}

	if !info.IsDir() {
		return fmt.Errorf("%w: %s", ErrMigrateLegacyNotDirectory, legacyDir)
	}

	plan, warnings, err := buildMigratePlan(legacyDir)
	if err != nil {
		return err
	}

	if len(plan) == 0 && len(warnings) == 0 {
		_, _ = fmt.Fprintf(os.Stdout, "nothing to migrate: %s is empty\n", legacyDir)

		return nil
	}

	// The guard runs before the conflict check and before the plan is
	// printed: it prunes stale command locks on both sides as it scans, so
	// they never surface as conflicts, and a refused operator is not shown
	// moves that will not happen. It never runs on dry-run, because that
	// pruning is a change on disk; the conflict walk below recognises
	// stale locks read-only instead (see migrateDiscardable).
	if !dryRun {
		err = refuseIfLiveProcessesActive(legacyDir, config.StateHome())
		if err != nil {
			return err
		}
	}

	conflicts := conflictingMigrateDestinations(plan)
	if len(conflicts) > 0 {
		return newMigrateConflictsError(conflicts)
	}

	printMigratePlan(plan, warnings, dryRun)

	if dryRun {
		return nil
	}

	for _, entry := range plan {
		verb, err := moveMigrateEntry(entry)
		if err != nil {
			return fmt.Errorf("migration failed partway through (some entries may already be moved): %w", err)
		}

		_, _ = fmt.Fprintf(os.Stdout, "%s %s -> %s\n", verb, entry.src, entry.dst)
	}

	return finalizeMigrateLegacyDir(legacyDir, warnings)
}

// buildMigratePlan reads the top-level entries of legacyDir and classifies
// each into a migratePlanEntry (known destination) or, for an unrecognized
// file, a warning naming it so it can be left in place rather than moved on
// a guess.
func buildMigratePlan(legacyDir string) ([]migratePlanEntry, []string, error) {
	dirEntries, err := os.ReadDir(legacyDir)
	if err != nil {
		return nil, nil, fmt.Errorf("reading legacy directory %s: %w", legacyDir, err)
	}

	var (
		plan     []migratePlanEntry
		warnings []string
	)

	for _, de := range dirEntries {
		name := de.Name()
		src := filepath.Join(legacyDir, name)
		isDir := de.IsDir()

		class, known := classifyMigrateEntry(name, isDir)
		if !known {
			warnings = append(warnings, name)

			continue
		}

		if isDir && class == migrateClassDataOther {
			plan = append(plan, planUnclassifiedBlocDir(name, src)...)

			continue
		}

		dst := migrateDestinationFor(class, name)
		plan = append(plan, migratePlanEntry{name: name, src: src, dst: dst, class: class})
	}

	// Sort by class first so every state-class entry (including a per-bloc
	// directory's split state/logs subdirectories) executes before any
	// data-class entry (including that same bloc directory's remainder
	// move): planUnclassifiedBlocDir relies on the state/logs subdirectory
	// renames happening before the remainder-of-bloc-directory rename, or
	// the remainder move would carry state/logs along with it again.
	sort.Slice(plan, func(i, j int) bool {
		if plan[i].class != plan[j].class {
			return plan[i].class < plan[j].class
		}

		return plan[i].name < plan[j].name
	})
	sort.Strings(warnings)

	return plan, warnings, nil
}

// planUnclassifiedBlocDir builds the migratePlanEntry set for one
// unrecognized top-level legacy directory, treated as a per-bloc data
// directory (see classifyMigrateEntry). Any migrateBlocStateSubdirs
// subdirectory present is split out to its StateHome()-class destination;
// the remainder of the bloc directory (everything else, e.g. keys/ or
// vault/) is planned as a single DataHome()-class move, executed after the
// split subdirectories are gone (see the class-ordered sort in
// buildMigratePlan) so the wholesale rename doesn't carry them along again.
func planUnclassifiedBlocDir(name, src string) []migratePlanEntry {
	var entries []migratePlanEntry

	for _, sub := range migrateBlocStateSubdirs {
		subSrc := filepath.Join(src, sub)

		info, err := os.Stat(subSrc)
		if err != nil || !info.IsDir() {
			continue
		}

		entries = append(entries, migratePlanEntry{
			name:  filepath.Join(name, sub),
			src:   subSrc,
			dst:   filepath.Join(config.StateHome(), name, sub),
			class: migrateClassState,
		})
	}

	entries = append(entries, migratePlanEntry{
		name:  name,
		src:   src,
		dst:   filepath.Join(config.DataHome(), name),
		class: migrateClassDataOther,
	})

	return entries
}

// classifyMigrateEntry resolves a legacy top-level entry to its XDG class.
// Known files and directories use the migrateKnownFiles/migrateKnownDirs
// tables. An unrecognized directory is treated as an unclassified per-bloc
// data directory (migrateClassDataOther) rather than left behind, since bloc
// directories are the majority of ~/.ocfp's content and cannot be
// enumerated by name in advance. An unrecognized file is reported as
// unknown (known=false) so the caller leaves it in place rather than
// guessing.
func classifyMigrateEntry(name string, isDir bool) (class migrateEntryClass, known bool) {
	if isDir {
		if class, ok := migrateKnownDirs[name]; ok {
			return class, true
		}

		return migrateClassDataOther, true
	}

	if class, ok := migrateKnownFiles[name]; ok {
		return class, true
	}

	if strings.HasPrefix(name, migrateConfigFilePrefix) {
		return migrateClassConfig, true
	}

	return migrateClassDataOther, false
}

// migrateDestinationFor resolves the XDG-class destination path for a
// legacy entry of the given name and class.
func migrateDestinationFor(class migrateEntryClass, name string) string {
	switch class {
	case migrateClassConfig:
		return filepath.Join(config.ConfigHome(), name)
	case migrateClassState:
		return filepath.Join(config.StateHome(), name)
	case migrateClassDataKeys, migrateClassDataOther:
		return filepath.Join(config.DataHome(), name)
	default:
		return filepath.Join(config.DataHome(), name)
	}
}

// conflictingMigrateDestinations returns a sorted, human-readable list of
// every planned source path whose destination is already occupied. A
// destination directory that already exists is not a conflict on its own:
// ordinary ocfp use writes command locks under StateHome()/.active and
// per-bloc logs under StateHome()/{bloc}/logs long before anyone runs the
// migration, so refusing on those would make the command impossible to run
// on exactly the machines it exists for. Such directories are merged by
// moveMigrateEntry; only a file that would be overwritten, or a directory
// that would land on a non-directory, counts as a conflict.
func conflictingMigrateDestinations(plan []migratePlanEntry) []string {
	conflicts := make([]string, 0, len(plan))

	for _, entry := range plan {
		conflicts = append(conflicts, migrateConflictsBetween(entry.src, entry.dst)...)
	}

	sort.Strings(conflicts)

	return conflicts
}

// migrateConflictsBetween walks src and reports every path under it that
// cannot move onto dst without overwriting something already there.
// Symlinks are not followed: a symlink at either end is treated as a file.
func migrateConflictsBetween(src, dst string) []string {
	dstInfo, err := os.Lstat(dst)
	if err != nil {
		return nil
	}

	srcInfo, err := os.Lstat(src)
	if err != nil {
		return nil
	}

	if !srcInfo.IsDir() || !dstInfo.IsDir() {
		if migrateDiscardable(src, dst) {
			return nil
		}

		return []string{fmt.Sprintf("%s (would receive %s)", dst, src)}
	}

	children, err := os.ReadDir(src)
	if err != nil {
		return nil
	}

	conflicts := make([]string, 0, len(children))

	for _, child := range children {
		conflicts = append(conflicts, migrateConflictsBetween(
			filepath.Join(src, child.Name()), filepath.Join(dst, child.Name()))...)
	}

	return conflicts
}

// printMigratePlan prints the resolved move plan and any unrecognized
// files that will be left in place.
func printMigratePlan(plan []migratePlanEntry, warnings []string, dryRun bool) {
	if dryRun {
		_, _ = fmt.Fprintln(os.Stdout, "Migration plan (dry run, nothing will change):")
	} else {
		_, _ = fmt.Fprintln(os.Stdout, "Migration plan:")
	}

	for _, entry := range plan {
		if bothMigrateDirs(entry.src, entry.dst) {
			_, _ = fmt.Fprintf(os.Stdout, "  %s -> %s (merging into existing directory)\n", entry.src, entry.dst)

			continue
		}

		_, _ = fmt.Fprintf(os.Stdout, "  %s -> %s\n", entry.src, entry.dst)
	}

	for _, name := range warnings {
		_, _ = fmt.Fprintf(os.Stdout, "  leave in place (unrecognized): %s\n", name)
	}
}

// moveMigrateEntry moves one plan entry from its legacy location to its
// XDG-class destination (see moveMigratePath) and reports what happened
// as a past-tense verb for the progress line: "moved", "merged" (into an
// existing directory), or "discarded" (an ephemeral file dst already had).
// Keys-class destinations are always chmod'd to keysDirMode after the
// move, regardless of which path was taken.
func moveMigrateEntry(entry migratePlanEntry) (string, error) {
	err := os.MkdirAll(filepath.Dir(entry.dst), migrateDirMode)
	if err != nil {
		return "", fmt.Errorf("creating destination directory for %s: %w", entry.name, err)
	}

	verb := "moved"

	switch {
	case migrateDiscardable(entry.src, entry.dst):
		verb = "discarded"
	case bothMigrateDirs(entry.src, entry.dst):
		verb = "merged"
	}

	err = moveMigratePath(entry.src, entry.dst)
	if err != nil {
		return "", err
	}

	if entry.class == migrateClassDataKeys {
		err = os.Chmod(entry.dst, keysDirMode)
		if err != nil {
			return "", fmt.Errorf("restricting permissions on %s: %w", entry.dst, err)
		}
	}

	warnIfDanglingMigrateSymlink(entry.dst)

	return verb, nil
}

// warnIfDanglingMigrateSymlink prints a warning when a moved entry is a
// symlink whose target no longer resolves from its new location. Relative
// links between recognized entries (config.yml -> ./config.pve.yml) keep
// working because both land in the same directory; a link to something
// unrecognized, or outside ~/.ocfp, may not, and silently shipping a
// dangling config.yml is exactly the failure this guards against.
func warnIfDanglingMigrateSymlink(dst string) {
	info, err := os.Lstat(dst)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return
	}

	_, err = os.Stat(dst)
	if err == nil {
		return
	}

	target, _ := os.Readlink(dst)

	_, _ = fmt.Fprintf(os.Stderr, "warning: %s is a symlink to %s, which does not resolve from its new location; fix the link by hand\n",
		dst, target)
}

// moveMigratePath moves src onto dst. When both are directories the
// children are moved one at a time, recursively, so whatever ordinary use
// has already written under dst stays alongside the legacy content;
// conflictingMigrateDestinations has already ruled out file collisions by
// the time this runs. Otherwise it prefers os.Rename and, on a
// cross-device rename error (EXDEV, e.g. legacy and XDG roots on different
// filesystems), falls back to a recursive copy that preserves file modes
// before removing the source.
func moveMigratePath(src, dst string) error {
	if migrateDiscardable(src, dst) {
		err := os.Remove(src)
		if err != nil {
			return fmt.Errorf("discarding %s: %w", src, err)
		}

		return nil
	}

	if bothMigrateDirs(src, dst) {
		return mergeMigrateDir(src, dst)
	}

	err := os.Rename(src, dst)
	if err == nil {
		return nil
	}

	if !isCrossDeviceMigrateError(err) {
		return fmt.Errorf("moving %s to %s: %w", src, dst, err)
	}

	err = copyMigrateEntryWithCleanup(src, dst)
	if err != nil {
		return err
	}

	err = os.RemoveAll(src)
	if err != nil {
		return fmt.Errorf("removing source %s after copy: %w", src, err)
	}

	return nil
}

// mergeMigrateDir moves every child of src into the existing directory
// dst, then removes the emptied src.
func mergeMigrateDir(src, dst string) error {
	children, err := os.ReadDir(src)
	if err != nil {
		return fmt.Errorf("reading directory %s: %w", src, err)
	}

	for _, child := range children {
		childSrc := filepath.Join(src, child.Name())
		childDst := filepath.Join(dst, child.Name())

		if migrateDiscardable(childSrc, childDst) {
			_, _ = fmt.Fprintf(os.Stdout, "  discarded %s (%s already exists)\n", childSrc, childDst)
		}

		err = moveMigratePath(childSrc, childDst)
		if err != nil {
			return err
		}
	}

	err = os.Remove(src)
	if err != nil {
		return fmt.Errorf("removing merged directory %s: %w", src, err)
	}

	return nil
}

// migrateDiscardable reports whether src is an ephemeral file that should
// be dropped rather than moved because dst is already occupied. Two kinds
// qualify: the content-free state.yml.lock flock target, and a command
// lock under .active/ whose holder is no longer running (a crashed
// command's leftover, or a PID since recycled). Neither carries anything
// worth keeping, and treating them as conflicts would block the migration
// on files ocfp itself would discard at the next scan.
func migrateDiscardable(src, dst string) bool {
	_, err := os.Lstat(dst)
	if err != nil {
		return false
	}

	if filepath.Base(src) == "state.yml.lock" {
		return true
	}

	return isStaleCommandLock(src)
}

// isStaleCommandLock reports whether path is a command lock file under an
// .active directory whose owning process is gone. It reads the lock
// without removing it, so it is safe on a dry run. An unparseable lock
// counts as stale, matching CommandTracker.CleanStaleLocks.
func isStaleCommandLock(path string) bool {
	if filepath.Base(filepath.Dir(path)) != ".active" || !strings.HasSuffix(path, ".lock") {
		return false
	}

	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return false
	}

	var cmd ActiveCommand

	err = json.Unmarshal(data, &cmd)
	if err != nil {
		return true
	}

	return !lockHolderRunning(cmd)
}

// bothMigrateDirs reports whether src and dst both exist and are real
// directories (symlinks are not followed), the precondition for merging.
func bothMigrateDirs(src, dst string) bool {
	srcInfo, err := os.Lstat(src)
	if err != nil || !srcInfo.IsDir() {
		return false
	}

	dstInfo, err := os.Lstat(dst)

	return err == nil && dstInfo.IsDir()
}

// isCrossDeviceMigrateError reports whether err is an os.LinkError wrapping
// syscall.EXDEV, the "invalid cross-device link" error os.Rename returns
// when source and destination are on different filesystems/devices.
func isCrossDeviceMigrateError(err error) bool {
	var linkErr *os.LinkError

	if errors.As(err, &linkErr) {
		return errors.Is(linkErr.Err, syscall.EXDEV)
	}

	return false
}

// copyMigrateEntryWithCleanup runs moveMigrateEntry's EXDEV copy fallback:
// it copies src to dst via copyMigrateEntryRecursive, and on failure,
// best-effort removes any partially-copied dst before returning. Without
// this, a mid-copy failure leaves dst partially populated while src
// (untouched) is still the authoritative copy; a retried migrate would
// then see dst already exists and refuse via
// conflictingMigrateDestinations with no indication it's incomplete --
// risking an operator deleting the actual source instead of the partial
// destination.
func copyMigrateEntryWithCleanup(src, dst string) error {
	err := copyMigrateEntryRecursive(src, dst)
	if err != nil {
		_ = os.RemoveAll(dst)

		return fmt.Errorf("copying %s to %s: %w (destination may be partial; remove it before retrying)",
			src, dst, err)
	}

	return nil
}

// copyMigrateEntryRecursive copies src to dst, preserving file modes,
// handling regular files, directories, and symlinks. Used as the
// cross-device fallback for moveMigrateEntry.
func copyMigrateEntryRecursive(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return fmt.Errorf("stat %s: %w", src, err)
	}

	switch {
	case info.Mode()&os.ModeSymlink != 0:
		return copyMigrateSymlink(src, dst)
	case info.IsDir():
		return copyMigrateDir(src, dst, info.Mode())
	default:
		return copyMigrateFile(src, dst, info.Mode())
	}
}

func copyMigrateDir(src, dst string, mode os.FileMode) error {
	err := os.MkdirAll(dst, copyMigrateDirWorkingMode)
	if err != nil {
		return fmt.Errorf("creating directory %s: %w", dst, err)
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return fmt.Errorf("reading directory %s: %w", src, err)
	}

	for _, e := range entries {
		srcPath := filepath.Join(src, e.Name())
		dstPath := filepath.Join(dst, e.Name())

		err = copyMigrateEntryRecursive(srcPath, dstPath)
		if err != nil {
			return err
		}
	}

	err = os.Chmod(dst, mode)
	if err != nil {
		return fmt.Errorf("restoring mode on directory %s: %w", dst, err)
	}

	return nil
}

func copyMigrateFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src) // #nosec G304 -- path is built from the operator's own legacy directory listing
	if err != nil {
		return fmt.Errorf("opening %s: %w", src, err)
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode) // #nosec G304 -- destination derived from trusted XDG-class roots
	if err != nil {
		return fmt.Errorf("creating %s: %w", dst, err)
	}

	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()

	if copyErr != nil {
		return fmt.Errorf("copying content to %s: %w", dst, copyErr)
	}

	if closeErr != nil {
		return fmt.Errorf("closing %s: %w", dst, closeErr)
	}

	err = os.Chmod(dst, mode)
	if err != nil {
		return fmt.Errorf("restoring mode on %s: %w", dst, err)
	}

	return nil
}

func copyMigrateSymlink(src, dst string) error {
	target, err := os.Readlink(src)
	if err != nil {
		return fmt.Errorf("reading symlink %s: %w", src, err)
	}

	err = os.Symlink(target, dst)
	if err != nil {
		return fmt.Errorf("creating symlink %s: %w", dst, err)
	}

	return nil
}

// refuseIfLiveProcessesActive scans each baseDir/.active for command locks
// belonging to a still-running process and returns ErrMigrateLiveProcessActive
// (wrapped with the offending commands) when any are found. Both the legacy
// dir and the XDG state root are scanned: a command on a mixed-phase machine
// may have written its lock under either.
//
// CommandTracker.GetActiveCommands removes locks whose owner is gone as it
// scans, including locks whose PID has since been recycled by an unrelated
// process, so only a genuinely live ocfp command blocks a migration. A
// lock naming this very process is skipped too: the CLI does not normally
// track the config commands, but if it ever does, migrate must not refuse
// on account of itself.
func refuseIfLiveProcessesActive(baseDirs ...string) error {
	var active []ActiveCommand

	self := os.Getpid()

	for _, baseDir := range baseDirs {
		tracker := NewCommandTracker(baseDir)

		found, err := tracker.GetActiveCommands()
		if err != nil {
			return fmt.Errorf("checking for active ocfp processes: %w", err)
		}

		for _, cmd := range found {
			if cmd.PID == self {
				continue
			}

			active = append(active, cmd)
		}
	}

	if len(active) == 0 {
		return nil
	}

	lines := make([]string, 0, len(active))

	for _, cmd := range active {
		label := cmd.Command
		if cmd.Subcommand != "" {
			label += " " + cmd.Subcommand
		}

		bloc := cmd.Bloc
		if bloc == "" {
			bloc = "(no bloc)"
		}

		lines = append(lines, fmt.Sprintf("pid %d: %s (bloc %s, started %s)",
			cmd.PID, label, bloc, cmd.Timestamp.Format(time.RFC3339)))
	}

	return fmt.Errorf("%w:\n  %s", ErrMigrateLiveProcessActive, strings.Join(lines, "\n  "))
}

// finalizeMigrateLegacyDir removes the now-empty legacy directory after a
// successful migration, or reports what remains (unrecognized files, or
// any unexpected leftover) and leaves the directory in place.
func finalizeMigrateLegacyDir(legacyDir string, warnings []string) error {
	if len(warnings) > 0 {
		_, _ = fmt.Fprintf(os.Stdout, "\n%d file(s) left in %s (unrecognized, review and move manually):\n", len(warnings), legacyDir)

		for _, name := range warnings {
			_, _ = fmt.Fprintf(os.Stdout, "  %s\n", filepath.Join(legacyDir, name))
		}

		_, _ = fmt.Fprintf(os.Stdout, "legacy directory retained: %s\n", legacyDir)

		return nil
	}

	remaining, err := os.ReadDir(legacyDir)
	if err != nil {
		return fmt.Errorf("checking legacy directory %s after migration: %w", legacyDir, err)
	}

	if len(remaining) > 0 {
		_, _ = fmt.Fprintf(os.Stdout, "legacy directory retained (unexpected leftover entries): %s\n", legacyDir)

		return nil
	}

	err = os.Remove(legacyDir)
	if err != nil {
		return fmt.Errorf("removing empty legacy directory %s: %w", legacyDir, err)
	}

	_, _ = fmt.Fprintf(os.Stdout, "migration complete; removed empty legacy directory %s\n", legacyDir)

	return nil
}
