package commands

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

// isolateXDGEnv points OCFP_HOME, XDG_CONFIG_HOME, XDG_STATE_HOME, and
// XDG_DATA_HOME away from any inherited value (including the package-wide
// OCFP_HOME set by TestMain) so each test resolves the legacy directory and
// the three XDG roots purely from HOME, which is set to a fresh
// t.TempDir(). No real ~/.ocfp or ~/.config/ocfp is ever touched.
func isolateXDGEnv(t *testing.T) (home string) {
	t.Helper()

	home = t.TempDir()
	t.Setenv("OCFP_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("HOME", home)

	return home
}

// skipIfRoot skips a test that proves behaviour on an unreadable file. Root
// bypasses the permission bits entirely, so on CI (which runs the suite inside
// a container as root) the read succeeds and the test proves nothing.
func skipIfRoot(t *testing.T) {
	t.Helper()

	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not restrict this process")
	}
}

func writeMigrateTestFile(t *testing.T, path, content string) {
	t.Helper()

	err := os.MkdirAll(filepath.Dir(path), 0o755)
	if err != nil {
		t.Fatalf("MkdirAll(%q): %v", filepath.Dir(path), err)
	}

	err = os.WriteFile(path, []byte(content), 0o644)
	if err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}

	return string(data)
}

func mustNotExist(t *testing.T, path string) {
	t.Helper()

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected %q not to exist, stat err = %v", path, err)
	}
}

func mustExist(t *testing.T, path string) {
	t.Helper()

	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected %q to exist, stat err = %v", path, err)
	}
}

func TestNewMigrateCmd_Registration(t *testing.T) {
	cmd := NewMigrateCmd()

	if cmd.Use != "migrate" {
		t.Errorf("Use = %q, want %q", cmd.Use, "migrate")
	}

	if cmd.Flags().Lookup("dry-run") == nil {
		t.Error("expected --dry-run flag to be registered")
	}
}

func TestRunMigrate_OCFPHomeOverrideActive_Refuses(t *testing.T) {
	override := t.TempDir()
	t.Setenv("OCFP_HOME", override)

	err := runMigrate(false)
	if err == nil {
		t.Fatal("expected error when OCFP_HOME override is active, got nil")
	}

	if !errors.Is(err, ErrMigrateOCFPHomeOverrideActive) {
		t.Errorf("error = %v, want %v", err, ErrMigrateOCFPHomeOverrideActive)
	}
}

func TestRunMigrate_NoLegacyDir_NoOp(t *testing.T) {
	home := isolateXDGEnv(t)

	err := runMigrate(false)
	if err != nil {
		t.Fatalf("runMigrate() error = %v, want nil (no-op)", err)
	}

	mustNotExist(t, filepath.Join(home, ".ocfp"))
	mustNotExist(t, config.ConfigHome())
	mustNotExist(t, config.StateHome())
	mustNotExist(t, config.DataHome())
}

func TestRunMigrate_EmptyLegacyDir_NoOp(t *testing.T) {
	home := isolateXDGEnv(t)

	legacyDir := filepath.Join(home, ".ocfp")

	err := os.MkdirAll(legacyDir, 0o750)
	if err != nil {
		t.Fatalf("MkdirAll(legacyDir): %v", err)
	}

	err = runMigrate(false)
	if err != nil {
		t.Fatalf("runMigrate() error = %v, want nil (no-op)", err)
	}

	mustExist(t, legacyDir)
}

func TestRunMigrate_FullLayout_MigratesAllClasses(t *testing.T) {
	home := isolateXDGEnv(t)
	legacyDir := filepath.Join(home, ".ocfp")

	writeMigrateTestFile(t, filepath.Join(legacyDir, "config.yml"), "name: test\n")
	writeMigrateTestFile(t, filepath.Join(legacyDir, "configs", "extra.yml"), "extra: true\n")
	writeMigrateTestFile(t, filepath.Join(legacyDir, "state.yml"), "current_bloc: test\n")
	writeMigrateTestFile(t, filepath.Join(legacyDir, "state.yml.lock"), "")
	writeMigrateTestFile(t, filepath.Join(legacyDir, "logs", "bootstrap", "20260101-000000.log"), "{}\n")
	writeMigrateTestFile(t, filepath.Join(legacyDir, "checkpoints", "phase1.json"), "{}\n")
	writeMigrateTestFile(t, filepath.Join(legacyDir, "backups", "1.json"), "{}\n")
	writeMigrateTestFile(t, filepath.Join(legacyDir, ".active", "bootstrap.lock"), "pid: 1\n")
	writeMigrateTestFile(t, filepath.Join(legacyDir, "provisioned"), "true\n")
	writeMigrateTestFile(t, filepath.Join(legacyDir, "bastion-init-completed"), "true\n")
	writeMigrateTestFile(t, filepath.Join(legacyDir, "keys", "mybloc-bastion", "id_rsa"), "PRIVATEKEY\n")
	writeMigrateTestFile(t, filepath.Join(legacyDir, "mybloc", "ssh", "id_ed25519"), "PRIVATEKEY2\n")

	err := runMigrate(false)
	if err != nil {
		t.Fatalf("runMigrate() error = %v, want nil", err)
	}

	// Config class.
	if got := mustReadFile(t, filepath.Join(config.ConfigHome(), "config.yml")); got != "name: test\n" {
		t.Errorf("config.yml content = %q, want %q", got, "name: test\n")
	}

	mustExist(t, filepath.Join(config.ConfigHome(), "configs", "extra.yml"))

	// State class.
	mustExist(t, filepath.Join(config.StateHome(), "state.yml"))
	mustExist(t, filepath.Join(config.StateHome(), "state.yml.lock"))
	mustExist(t, filepath.Join(config.StateHome(), "logs", "bootstrap", "20260101-000000.log"))
	mustExist(t, filepath.Join(config.StateHome(), "checkpoints", "phase1.json"))
	mustExist(t, filepath.Join(config.StateHome(), "backups", "1.json"))
	mustExist(t, filepath.Join(config.StateHome(), ".active", "bootstrap.lock"))
	mustExist(t, filepath.Join(config.StateHome(), "provisioned"))
	mustExist(t, filepath.Join(config.StateHome(), "bastion-init-completed"))

	// Data class: keys (0700) and unclassified per-bloc directory.
	keysDir := filepath.Join(config.DataHome(), "keys")
	mustExist(t, filepath.Join(keysDir, "mybloc-bastion", "id_rsa"))

	info, statErr := os.Stat(keysDir)
	if statErr != nil {
		t.Fatalf("stat keys dir: %v", statErr)
	}

	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("keys dir mode = %o, want %o", perm, 0o700)
	}

	mustExist(t, filepath.Join(config.DataHome(), "mybloc", "ssh", "id_ed25519"))

	// Legacy dir fully migrated -> removed.
	mustNotExist(t, legacyDir)
}

// TestRunMigrate_BlocStateAndLogsSplitFromDataDir is the regression test
// for the CRITICAL silent-data-orphaning defect: a per-bloc legacy
// directory containing state/, logs/, and an unrelated ssh/ subdirectory
// must have state/ and logs/ split out to StateHome()/{bloc}/ (where
// state.GetStateDir and every log resolver actually look), while the rest
// of the bloc directory (ssh/ here) still lands under DataHome()/{bloc}/.
// It also asserts post-migrate resolution parity: state.GetStateDir must
// resolve to the migrated location, not a fresh empty directory.
func TestRunMigrate_BlocStateAndLogsSplitFromDataDir(t *testing.T) {
	home := isolateXDGEnv(t)
	legacyDir := filepath.Join(home, ".ocfp")

	writeMigrateTestFile(t, filepath.Join(legacyDir, "mybloc", "state", "mybloc.json"), `{"blocName":"mybloc"}`+"\n")
	writeMigrateTestFile(t, filepath.Join(legacyDir, "mybloc", "logs", "bootstrap", "20260101-000000.log"), "{}\n")
	writeMigrateTestFile(t, filepath.Join(legacyDir, "mybloc", "ssh", "id_ed25519"), "PRIVATEKEY\n")

	err := runMigrate(false)
	if err != nil {
		t.Fatalf("runMigrate() error = %v, want nil", err)
	}

	// state/ and logs/ split to StateHome()/mybloc/.
	mustExist(t, filepath.Join(config.StateHome(), "mybloc", "state", "mybloc.json"))
	mustExist(t, filepath.Join(config.StateHome(), "mybloc", "logs", "bootstrap", "20260101-000000.log"))

	// Remainder (ssh/) lands under DataHome()/mybloc/, but state/ and
	// logs/ must NOT have been carried along with it.
	mustExist(t, filepath.Join(config.DataHome(), "mybloc", "ssh", "id_ed25519"))
	mustNotExist(t, filepath.Join(config.DataHome(), "mybloc", "state"))
	mustNotExist(t, filepath.Join(config.DataHome(), "mybloc", "logs"))

	mustNotExist(t, legacyDir)

	// Post-migrate resolution parity: state.GetStateDir must find the
	// migrated state directory, not resolve to a fresh, empty one that no
	// data was ever written to.
	resolvedStateDir, stateErr := state.GetStateDir("mybloc")
	if stateErr != nil {
		t.Fatalf("state.GetStateDir(%q) error = %v", "mybloc", stateErr)
	}

	wantStateDir := filepath.Join(config.StateHome(), "mybloc", "state")
	if resolvedStateDir != wantStateDir {
		t.Errorf("state.GetStateDir(%q) = %q, want %q", "mybloc", resolvedStateDir, wantStateDir)
	}

	mustExist(t, filepath.Join(resolvedStateDir, "mybloc.json"))

	// Post-migrate resolution parity for logs: the dual-read helper every
	// log resolver (logger.go's fallback, root.go's getBaseDir,
	// vault.go's inception log dir) uses must resolve the migrated
	// StateHome() location, not the now-removed legacy directory.
	newLogsDir := filepath.Join(config.StateHome(), "mybloc", "logs")
	legacyLogsDir := filepath.Join(config.OcfpHome(), "mybloc", "logs")

	resolvedLogsDir, usedLegacy := config.ResolveExisting(newLogsDir, legacyLogsDir)
	if usedLegacy {
		t.Errorf("ResolveExisting(logs) fell back to legacy path %q after migration", legacyLogsDir)
	}

	if resolvedLogsDir != newLogsDir {
		t.Errorf("ResolveExisting(logs) = %q, want %q", resolvedLogsDir, newLogsDir)
	}

	mustExist(t, filepath.Join(resolvedLogsDir, "bootstrap", "20260101-000000.log"))
}

// writeMigrateLockFile writes a command-tracker lock file directly (bypassing
// CommandTracker.CreateLockFile, since these tests need full control over
// the embedded PID) at legacyDir/.active/{timestamp}-{pid}.lock, matching
// the format logs_tracker.go's CreateLockFile produces.
func writeMigrateLockFile(t *testing.T, legacyDir string, pid int) {
	t.Helper()

	timestamp := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	info := ActiveCommand{
		Timestamp:  timestamp,
		PID:        pid,
		Bloc:       "mybloc",
		Command:    "bootstrap",
		Subcommand: "",
		LogPath:    filepath.Join(legacyDir, "mybloc", "logs", "bootstrap", "20260101-000000.log"),
	}

	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		t.Fatalf("marshal lock info: %v", err)
	}

	lockPath := filepath.Join(legacyDir, ".active", timestamp.Format("20060102-150405")+"-"+strconv.Itoa(pid)+".lock")
	writeMigrateTestFile(t, lockPath, string(data))
}

// deadPID spawns and waits for a trivial child process, returning its PID
// once it has exited. Reaped PIDs are not immediately reused by the OS, so
// this reliably yields a PID that IsProcessRunning reports as dead without
// hardcoding a magic number that risks colliding with a real process.
func deadPID(t *testing.T) int {
	t.Helper()

	cmd := exec.Command("true")

	err := cmd.Start()
	if err != nil {
		t.Fatalf("starting throwaway process: %v", err)
	}

	pid := cmd.Process.Pid

	err = cmd.Wait()
	if err != nil {
		t.Fatalf("waiting for throwaway process: %v", err)
	}

	return pid
}

// TestRunMigrate_LiveProcessLockRefuses is the regression test for
// F-W6-05: a command lock in .active/ belonging to a live PID must refuse
// the migration outright, before any file is moved, since relocating
// state.yml.lock/.active/ and a bloc's state/logs directories out from
// under a running command would corrupt its tracking mid-run.
func TestRunMigrate_LiveProcessLockRefuses(t *testing.T) {
	home := isolateXDGEnv(t)
	legacyDir := filepath.Join(home, ".ocfp")

	writeMigrateTestFile(t, filepath.Join(legacyDir, "config.yml"), "name: test\n")
	writeMigrateLockFile(t, legacyDir, os.Getpid())

	err := runMigrate(false)
	if err == nil {
		t.Fatal("expected error when a live process lock is present, got nil")
	}

	if !errors.Is(err, ErrMigrateLiveProcessActive) {
		t.Errorf("error = %v, want wrapping %v", err, ErrMigrateLiveProcessActive)
	}

	// Nothing moved.
	mustExist(t, filepath.Join(legacyDir, "config.yml"))
	mustNotExist(t, filepath.Join(config.ConfigHome(), "config.yml"))
}

// TestRunMigrate_StaleLockDoesNotBlock asserts a lock file whose PID is no
// longer running (a crashed or already-completed command) does NOT block
// migration: only genuinely live processes should.
func TestRunMigrate_StaleLockDoesNotBlock(t *testing.T) {
	home := isolateXDGEnv(t)
	legacyDir := filepath.Join(home, ".ocfp")

	writeMigrateTestFile(t, filepath.Join(legacyDir, "config.yml"), "name: test\n")
	writeMigrateLockFile(t, legacyDir, deadPID(t))

	err := runMigrate(false)
	if err != nil {
		t.Fatalf("runMigrate() error = %v, want nil (stale lock must not block)", err)
	}

	mustExist(t, filepath.Join(config.ConfigHome(), "config.yml"))
}

// TestRunMigrate_LiveProcessLockInStateHomeRefuses asserts the guard also
// sees locks a command has already written under the XDG state root: a
// mixed-phase machine (partial prior migration, or a fresh XDG-side write
// while the legacy dir still exists) must refuse just like a legacy-side
// lock does.
func TestRunMigrate_LiveProcessLockInStateHomeRefuses(t *testing.T) {
	home := isolateXDGEnv(t)
	legacyDir := filepath.Join(home, ".ocfp")

	writeMigrateTestFile(t, filepath.Join(legacyDir, "config.yml"), "name: test\n")
	writeMigrateLockFile(t, config.StateHome(), os.Getpid())

	err := runMigrate(false)
	if err == nil {
		t.Fatal("expected error when a live process lock is present under StateHome, got nil")
	}

	if !errors.Is(err, ErrMigrateLiveProcessActive) {
		t.Errorf("error = %v, want wrapping %v", err, ErrMigrateLiveProcessActive)
	}

	// Nothing moved.
	mustExist(t, filepath.Join(legacyDir, "config.yml"))
	mustNotExist(t, filepath.Join(config.ConfigHome(), "config.yml"))
}

// TestRunMigrate_LiveProcessRefusalPrintsNoPlan asserts the guard fires
// before the move plan is printed: an operator refused for a live process
// should not see a plan that is not going to run.
func TestRunMigrate_LiveProcessRefusalPrintsNoPlan(t *testing.T) {
	home := isolateXDGEnv(t)
	legacyDir := filepath.Join(home, ".ocfp")

	writeMigrateTestFile(t, filepath.Join(legacyDir, "config.yml"), "name: test\n")
	writeMigrateLockFile(t, legacyDir, os.Getpid())

	var err error

	out := captureStdout(t, func() {
		err = runMigrate(false)
	})

	if !errors.Is(err, ErrMigrateLiveProcessActive) {
		t.Fatalf("error = %v, want wrapping %v", err, ErrMigrateLiveProcessActive)
	}

	if strings.Contains(out, "Migration plan") {
		t.Errorf("stdout contains the migration plan despite refusal:\n%s", out)
	}
}

func TestRunMigrate_ConflictRefusesAllMoves(t *testing.T) {
	home := isolateXDGEnv(t)
	legacyDir := filepath.Join(home, ".ocfp")

	writeMigrateTestFile(t, filepath.Join(legacyDir, "config.yml"), "name: legacy\n")
	writeMigrateTestFile(t, filepath.Join(legacyDir, "state.yml"), "current_bloc: legacy\n")

	// Pre-existing destination for config.yml only; state.yml destination is free.
	writeMigrateTestFile(t, filepath.Join(config.ConfigHome(), "config.yml"), "name: existing\n")

	err := runMigrate(false)
	if err == nil {
		t.Fatal("expected conflict error, got nil")
	}

	if !errors.Is(err, ErrMigrateHasConflicts) {
		t.Errorf("error = %v, want wrapping %v", err, ErrMigrateHasConflicts)
	}

	// Nothing moved: the non-conflicting state.yml must still be at the
	// legacy location, and the pre-existing config.yml destination must be
	// untouched.
	mustExist(t, filepath.Join(legacyDir, "config.yml"))
	mustExist(t, filepath.Join(legacyDir, "state.yml"))
	mustNotExist(t, filepath.Join(config.StateHome(), "state.yml"))

	if got := mustReadFile(t, filepath.Join(config.ConfigHome(), "config.yml")); got != "name: existing\n" {
		t.Errorf("pre-existing destination content = %q, want unchanged %q", got, "name: existing\n")
	}
}

func TestRunMigrate_DryRun_TouchesNothing(t *testing.T) {
	home := isolateXDGEnv(t)
	legacyDir := filepath.Join(home, ".ocfp")

	writeMigrateTestFile(t, filepath.Join(legacyDir, "config.yml"), "name: test\n")
	writeMigrateTestFile(t, filepath.Join(legacyDir, "state.yml"), "current_bloc: test\n")

	err := runMigrate(true)
	if err != nil {
		t.Fatalf("runMigrate(dryRun=true) error = %v, want nil", err)
	}

	mustExist(t, filepath.Join(legacyDir, "config.yml"))
	mustExist(t, filepath.Join(legacyDir, "state.yml"))
	mustNotExist(t, config.ConfigHome())
	mustNotExist(t, config.StateHome())
}

func TestRunMigrate_UnknownLooseFile_LeftBehindLegacyDirRetained(t *testing.T) {
	home := isolateXDGEnv(t)
	legacyDir := filepath.Join(home, ".ocfp")

	writeMigrateTestFile(t, filepath.Join(legacyDir, "config.yml"), "name: test\n")
	writeMigrateTestFile(t, filepath.Join(legacyDir, "notes.txt"), "operator scratch notes\n")

	err := runMigrate(false)
	if err != nil {
		t.Fatalf("runMigrate() error = %v, want nil", err)
	}

	mustExist(t, filepath.Join(config.ConfigHome(), "config.yml"))
	mustNotExist(t, filepath.Join(legacyDir, "config.yml"))

	// Unknown file left in place; legacy dir retained because it isn't empty.
	mustExist(t, filepath.Join(legacyDir, "notes.txt"))
	mustExist(t, legacyDir)
}

func TestRunMigrate_MarkerFilesLandInStateHome(t *testing.T) {
	home := isolateXDGEnv(t)
	legacyDir := filepath.Join(home, ".ocfp")

	writeMigrateTestFile(t, filepath.Join(legacyDir, "provisioned"), "true\n")
	writeMigrateTestFile(t, filepath.Join(legacyDir, "bastion-init-completed"), "true\n")

	err := runMigrate(false)
	if err != nil {
		t.Fatalf("runMigrate() error = %v, want nil", err)
	}

	if got := mustReadFile(t, filepath.Join(config.StateHome(), "provisioned")); got != "true\n" {
		t.Errorf("provisioned content = %q, want %q", got, "true\n")
	}

	if got := mustReadFile(t, filepath.Join(config.StateHome(), "bastion-init-completed")); got != "true\n" {
		t.Errorf("bastion-init-completed content = %q, want %q", got, "true\n")
	}

	mustNotExist(t, legacyDir)
}

// TestCopyMigrateDir_ReadOnlySourceModeSucceeds is the regression test for
// F-W6-09: copyMigrateDir (the EXDEV cross-device copy fallback) must be
// able to populate a destination directory whose source had a read-only
// mode (no write bit), since it creates the destination at a fixed
// writable working mode and only chmods it to match the source once every
// child has been copied in.
func TestCopyMigrateDir_ReadOnlySourceModeSucceeds(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	dst := filepath.Join(tmp, "dst")

	writeMigrateTestFile(t, filepath.Join(src, "file.txt"), "hello\n")

	const readOnlyDirMode = 0o500

	err := os.Chmod(src, readOnlyDirMode)
	if err != nil {
		t.Fatalf("Chmod(src, read-only): %v", err)
	}

	// t.TempDir()'s own cleanup needs to remove src's and dst's contents;
	// restore a writable mode on both once this test is done so cleanup
	// can proceed (dst ends this test chmod'd to the same read-only mode
	// as src, by design -- see the assertion below).
	t.Cleanup(func() {
		_ = os.Chmod(src, 0o755)
		_ = os.Chmod(dst, 0o755)
	})

	srcInfo, statErr := os.Stat(src)
	if statErr != nil {
		t.Fatalf("stat src: %v", statErr)
	}

	err = copyMigrateDir(src, dst, srcInfo.Mode())
	if err != nil {
		t.Fatalf("copyMigrateDir(read-only source) error = %v, want nil", err)
	}

	if got := mustReadFile(t, filepath.Join(dst, "file.txt")); got != "hello\n" {
		t.Errorf("dst file.txt content = %q, want %q", got, "hello\n")
	}

	dstInfo, dstStatErr := os.Stat(dst)
	if dstStatErr != nil {
		t.Fatalf("stat dst: %v", dstStatErr)
	}

	if perm := dstInfo.Mode().Perm(); perm != readOnlyDirMode {
		t.Errorf("dst mode = %o, want %o (restored to source mode)", perm, readOnlyDirMode)
	}
}

// TestCopyMigrateEntryWithCleanup_PartialFailureRemovesDst is the
// regression test for F-W6-04: a mid-copy failure during the EXDEV
// cross-device fallback must remove the partially-copied destination and
// name the risk in the returned error, so a retried migrate never sees a
// stale partial destination that looks like an ordinary conflict.
func TestCopyMigrateEntryWithCleanup_PartialFailureRemovesDst(t *testing.T) {
	skipIfRoot(t)

	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	dst := filepath.Join(tmp, "dst")

	writeMigrateTestFile(t, filepath.Join(src, "a-ok.txt"), "fine\n")
	writeMigrateTestFile(t, filepath.Join(src, "b-unreadable.txt"), "boom\n")

	unreadable := filepath.Join(src, "b-unreadable.txt")

	err := os.Chmod(unreadable, 0o000)
	if err != nil {
		t.Fatalf("Chmod(unreadable, 0o000): %v", err)
	}

	t.Cleanup(func() { _ = os.Chmod(unreadable, 0o644) })

	err = copyMigrateEntryWithCleanup(src, dst)
	if err == nil {
		t.Fatal("expected copy error from unreadable source file, got nil")
	}

	if !strings.Contains(err.Error(), "destination may be partial; remove it before retrying") {
		t.Errorf("error = %q, want it to contain the partial-destination hint", err.Error())
	}

	mustNotExist(t, dst)
}
