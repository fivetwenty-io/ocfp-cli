package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
)

// setupBackupTestHome points HOME at a fresh temp dir and clears OCFP_HOME
// and XDG_STATE_HOME so each test controls its own path-resolution inputs
// independent of TestMain's package-wide OCFP_HOME default.
func setupBackupTestHome(t *testing.T) string {
	t.Helper()

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("OCFP_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")

	return tmpHome
}

// TestSaveBackupMetadata_UsesXDGStateHome asserts backup metadata is written
// under StateHome() (XDG_STATE_HOME/ocfp/backups) rather than the legacy
// OcfpHome()/backups path, when no legacy directory exists.
func TestSaveBackupMetadata_UsesXDGStateHome(t *testing.T) {
	tmpHome := setupBackupTestHome(t)

	tmpState := filepath.Join(tmpHome, "xdg-state")
	t.Setenv("XDG_STATE_HOME", tmpState)

	backup := &BackupMetadata{ID: "backup-xdg-state-test"}

	_ = saveBackupMetadata(backup)

	wantPath := filepath.Join(config.StateHome(), "backups", backup.ID+".json")

	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("expected backup metadata at %s under StateHome(), got stat error: %v", wantPath, err)
	}
}

// TestSaveBackupMetadata_DualReadsLegacyOcfpHomeBackups asserts that when a
// pre-migration OcfpHome()/backups directory already exists and the new
// StateHome()/backups directory does not, saveBackupMetadata falls back to
// writing into the legacy directory rather than starting a fresh new one.
func TestSaveBackupMetadata_DualReadsLegacyOcfpHomeBackups(t *testing.T) {
	tmpHome := setupBackupTestHome(t)

	legacyDir := filepath.Join(tmpHome, ".ocfp", "backups")
	if err := os.MkdirAll(legacyDir, BackupDirPerm); err != nil {
		t.Fatalf("failed to seed legacy backups dir: %v", err)
	}

	backup := &BackupMetadata{ID: "backup-legacy-test"}

	_ = saveBackupMetadata(backup)

	legacyFile := filepath.Join(legacyDir, backup.ID+".json")
	if _, err := os.Stat(legacyFile); err != nil {
		t.Fatalf("expected backup metadata to fall back to legacy dir %s, got stat error: %v", legacyDir, err)
	}

	newFile := filepath.Join(tmpHome, ".local", "state", "ocfp", "backups", backup.ID+".json")
	if _, err := os.Stat(newFile); err == nil {
		t.Fatalf("expected no metadata written under new StateHome() path %s while legacy dir exists", newFile)
	}
}
