package commands

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"
)

// TestLBPersistentPreRun_ResolvesLogDirUnderStateHomeNotOcfpHome verifies the
// lb command's PersistentPreRunE initializes the file logger under the XDG
// state-class directory (config.StateHome()) rather than the pre-migration
// flat OcfpHome() directory, when neither the new nor the legacy log
// directory pre-exists.
func TestLBPersistentPreRun_ResolvesLogDirUnderStateHomeNotOcfpHome(t *testing.T) {
	legacyHome := t.TempDir()
	xdgStateBase := t.TempDir()

	t.Setenv("OCFP_HOME", "")
	t.Setenv("HOME", legacyHome)
	t.Setenv("XDG_STATE_HOME", xdgStateBase)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")

	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set("bloc", "")
	viper.Set("log_level", "info")
	viper.Set("debug", false)
	viper.Set("verbose", false)
	viper.Set("trace", false)
	viper.Set("no_log", false)

	cmd := NewLBCmd()

	if cmd.PersistentPreRunE == nil {
		t.Fatal("NewLBCmd() has no PersistentPreRunE")
	}

	if err := cmd.PersistentPreRunE(cmd, []string{}); err != nil {
		t.Fatalf("PersistentPreRunE() error = %v", err)
	}

	logFile := findLogFileUnder(t, xdgStateBase)

	legacyLogRoot := filepath.Join(legacyHome, ".ocfp")
	if strings.HasPrefix(logFile, legacyLogRoot) {
		t.Fatalf("lb log file %q resolved under legacy OcfpHome() root %q, want under XDG state home", logFile, legacyLogRoot)
	}

	wantStateRoot := filepath.Join(xdgStateBase, "ocfp")
	if !strings.HasPrefix(logFile, wantStateRoot) {
		t.Fatalf("lb log file %q not under expected StateHome() root %q", logFile, wantStateRoot)
	}
}
