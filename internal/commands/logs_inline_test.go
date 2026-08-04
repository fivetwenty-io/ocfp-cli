package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// setLogDirTestEnv points config.OcfpHome() (legacy) and config.StateHome()
// (new) at two distinct temp roots so a test can tell which one a log-dir
// resolver actually used.
func setLogDirTestEnv(t *testing.T) (stateRoot, legacyRoot string) {
	t.Helper()

	legacyHome := t.TempDir()
	xdgStateHome := t.TempDir()

	t.Setenv("OCFP_HOME", "")
	t.Setenv("HOME", legacyHome)
	t.Setenv("XDG_STATE_HOME", xdgStateHome)

	viper.Set("no_log", false)
	t.Cleanup(func() { viper.Set("no_log", false) })

	return config.StateHome(), config.OcfpHome()
}

// logDirHasEntries reports whether dir exists and contains at least one
// file, i.e. the logger actually wrote a log file there.
func logDirHasEntries(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}

	return len(entries) > 0
}

// assertLogLandedUnderStateHome runs setup, then run, then checks a log
// file exists under stateRoot/subdirParts... and none exists under the
// equivalent legacyRoot/subdirParts... path.
func assertLogLandedUnderStateHome(t *testing.T, subdirParts []string, run func()) {
	t.Helper()

	stateRoot, legacyRoot := setLogDirTestEnv(t)

	run()

	wantDir := filepath.Join(append([]string{stateRoot}, subdirParts...)...)
	if !logDirHasEntries(wantDir) {
		t.Fatalf("expected log file under StateHome()-derived path %s, found none", wantDir)
	}

	legacyDir := filepath.Join(append([]string{legacyRoot}, subdirParts...)...)
	if logDirHasEntries(legacyDir) {
		t.Fatalf("found log file under legacy OcfpHome()-derived path %s: resolver bypassed StateHome()", legacyDir)
	}
}

func TestBootstrapLogDir_ResolvesUnderStateHome(t *testing.T) {
	assertLogLandedUnderStateHome(t, []string{"logdir-test-bootstrap", "logs", "bootstrap"}, func() {
		if err := initializeBlocLogger("logdir-test-bootstrap"); err != nil {
			t.Fatalf("initializeBlocLogger: %v", err)
		}
	})
}

func TestTeardownLogDir_ResolvesUnderStateHome(t *testing.T) {
	assertLogLandedUnderStateHome(t, []string{"logdir-test-teardown", "logs", "teardown"}, func() {
		if _, err := initializeTeardownLogger("logdir-test-teardown"); err != nil {
			t.Fatalf("initializeTeardownLogger: %v", err)
		}
	})
}

func TestStateLogDir_ResolvesUnderStateHome(t *testing.T) {
	assertLogLandedUnderStateHome(t, []string{"logdir-test-state", "logs", "state", "sync"}, func() {
		if err := initializeStateLogger("logdir-test-state", "sync"); err != nil {
			t.Fatalf("initializeStateLogger: %v", err)
		}
	})
}

func TestVaultLogDir_PersistentPreRunResolvesUnderStateHome(t *testing.T) {
	// Standalone NewVaultCmd() has no parent/root "bloc" flag, so BlocName
	// stays empty: the logger builds {root}/logs/vault with no bloc segment.
	assertLogLandedUnderStateHome(t, []string{"logs", "vault"}, func() {
		cmd := NewVaultCmd()
		if err := cmd.PersistentPreRunE(cmd, nil); err != nil {
			t.Fatalf("vault PersistentPreRunE: %v", err)
		}
	})
}

func TestVaultLogDir_MigrateResolvesUnderStateHome(t *testing.T) {
	assertLogLandedUnderStateHome(t, []string{"logdir-test-migrate", "logs", "vault", "migrate"}, func() {
		viper.Set("bloc", "logdir-test-migrate")
		t.Cleanup(func() { viper.Set("bloc", "") })

		cmd := &cobra.Command{Use: "migrate"} //nolint:exhaustruct // test double, only Use is needed
		cmd.Flags().Bool("force", false, "skip confirmation prompts")

		// runVaultMigrate fails past logger init in this test environment
		// (no vault config/manager available) -- that failure is expected;
		// the log-dir side effect under test happens before it, on the
		// same call, so the error is intentionally discarded.
		_ = runVaultMigrate(cmd, "", "", true)
	})
}
