package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"
)

// TestInitializeScaleLogger_ResolvesUnderStateHomeNotOcfpHome verifies the
// scale command's file logger writes under the XDG state-class directory
// (config.StateHome()) rather than the pre-migration flat OcfpHome()
// directory, when neither the new nor the legacy log directory pre-exists.
func TestInitializeScaleLogger_ResolvesUnderStateHomeNotOcfpHome(t *testing.T) {
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

	log, err := initializeScaleLogger()
	if err != nil {
		t.Fatalf("initializeScaleLogger() error = %v", err)
	}

	if log == nil {
		t.Fatal("initializeScaleLogger() returned nil logger")
	}

	logFile := findLogFileUnder(t, xdgStateBase)

	legacyLogRoot := filepath.Join(legacyHome, ".ocfp")
	if strings.HasPrefix(logFile, legacyLogRoot) {
		t.Fatalf("scale log file %q resolved under legacy OcfpHome() root %q, want under XDG state home", logFile, legacyLogRoot)
	}

	wantStateRoot := filepath.Join(xdgStateBase, "ocfp")
	if !strings.HasPrefix(logFile, wantStateRoot) {
		t.Fatalf("scale log file %q not under expected StateHome() root %q", logFile, wantStateRoot)
	}
}

// findLogFileUnder walks root looking for a *.log file created by a file
// logger and returns its path, failing the test if none is found. Shared by
// the scale and lb logger-path tests.
func findLogFileUnder(t *testing.T, root string) string {
	t.Helper()

	var found string

	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if info.IsDir() {
			return nil
		}

		if strings.HasSuffix(path, ".log") {
			found = path
		}

		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	if found == "" {
		t.Fatalf("no .log file found under %s", root)
	}

	return found
}
