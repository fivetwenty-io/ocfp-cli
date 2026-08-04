package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
)

func TestOcfpHome_UsesEnvVar(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("OCFP_HOME", tmpDir)

	got := config.OcfpHome()
	if got != tmpDir {
		t.Errorf("OcfpHome() = %q, want %q", got, tmpDir)
	}
}

func TestOcfpHome_FallsBackToUserHome(t *testing.T) {
	t.Setenv("OCFP_HOME", "")

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("failed to get user home dir: %v", err)
	}

	want := filepath.Join(home, ".ocfp")

	got := config.OcfpHome()
	if got != want {
		t.Errorf("OcfpHome() = %q, want %q", got, want)
	}
}

func TestOcfpBlocDir(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("OCFP_HOME", tmpDir)

	got := config.OcfpBlocDir("mybloc")
	want := filepath.Join(tmpDir, "mybloc")

	if got != want {
		t.Errorf("OcfpBlocDir() = %q, want %q", got, want)
	}
}

func TestOcfpSSHKeyDir(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("OCFP_HOME", tmpDir)

	got := config.OcfpSSHKeyDir("mybloc")
	want := filepath.Join(tmpDir, "mybloc", "ssh")

	if got != want {
		t.Errorf("OcfpSSHKeyDir() = %q, want %q", got, want)
	}
}

// TestConfigHome_DefaultXDGPath verifies ConfigHome() resolves to the XDG
// config-class default (~/.config/ocfp), not the pre-migration ~/.ocfp,
// when neither OCFP_HOME nor XDG_CONFIG_HOME is set.
func TestConfigHome_DefaultXDGPath(t *testing.T) {
	t.Setenv("OCFP_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	want := filepath.Join(tmpHome, ".config", "ocfp")

	got := config.ConfigHome()
	if got != want {
		t.Errorf("ConfigHome() = %q, want %q (must be the XDG default, not ~/.ocfp)", got, want)
	}
}

// TestOcfpHomeOverride_CollapsesConfigStateDataHome verifies that setting
// OCFP_HOME collapses ConfigHome, StateHome, and DataHome to the same
// single directory -- today's pre-XDG-migration flat layout, which every
// existing OCFP_HOME-setting test in this package (and 26 others) relies
// on continuing to work unmodified.
func TestOcfpHomeOverride_CollapsesConfigStateDataHome(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("OCFP_HOME", tmpDir)

	if got := config.ConfigHome(); got != tmpDir {
		t.Errorf("ConfigHome() = %q, want %q", got, tmpDir)
	}

	if got := config.StateHome(); got != tmpDir {
		t.Errorf("StateHome() = %q, want %q", got, tmpDir)
	}

	if got := config.DataHome(); got != tmpDir {
		t.Errorf("DataHome() = %q, want %q", got, tmpDir)
	}
}

// TestDetermineConfigPath_LegacyFallback verifies the config.yml
// dual-read migration path end to end, through the exported
// ListBlocNames("") entry point (determineConfigPath itself is
// unexported): when only the pre-migration ~/.ocfp/config.yml exists and
// the new ConfigHome()/config.yml does not, config resolution falls back
// to the legacy file rather than reporting no config found.
func TestDetermineConfigPath_LegacyFallback(t *testing.T) {
	t.Setenv("OCFP_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	legacyDir := filepath.Join(tmpHome, ".ocfp")

	err := os.MkdirAll(legacyDir, 0o750)
	if err != nil {
		t.Fatalf("failed to create legacy dir: %v", err)
	}

	legacyConfig := "blocs:\n  legacy-bloc:\n    name: legacy-bloc\n    provider: pve\n"

	err = os.WriteFile(filepath.Join(legacyDir, "config.yml"), []byte(legacyConfig), 0o600)
	if err != nil {
		t.Fatalf("failed to write legacy config: %v", err)
	}

	names, err := config.ListBlocNames("")
	if err != nil {
		t.Fatalf("ListBlocNames() error: %v", err)
	}

	if len(names) != 1 || names[0] != "legacy-bloc" {
		t.Errorf("ListBlocNames() = %v, want [legacy-bloc] (legacy config.yml fallback)", names)
	}
}

// TestSafetyGuard_PanicsForStateHomeXDGDefault verifies the safety guard
// covers the new XDG state-class default directory, not just the legacy
// ~/.ocfp root: a test that sets OCFP_TEST_SAFETY_GUARD but forgets to
// isolate OCFP_HOME/XDG_STATE_HOME must still panic before it can write
// into what resolves as the real state home.
func TestSafetyGuard_PanicsForStateHomeXDGDefault(t *testing.T) {
	t.Setenv("OCFP_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("OCFP_TEST_SAFETY_GUARD", "1")

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected SaveState() to panic when StateHome() resolves to the real (simulated) XDG state default")
		}
	}()

	_ = config.SaveState(&config.StateFile{CurrentBloc: "x"})
}
