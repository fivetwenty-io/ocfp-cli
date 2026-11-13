package config_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/config"
	"github.com/fivetwenty-io/ocfp-cli/internal/writeguard"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

	got, err := config.OcfpBlocDir("mybloc")
	if err != nil {
		t.Fatalf("OcfpBlocDir() error = %v", err)
	}

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

// guardedRealHome makes a temporary directory the developer's real home
// for the safety guard, and points HOME at it as well, so the config
// resolves its default directories there. It clears every override, so
// each default directory is the real one the guard protects.
func guardedRealHome(t *testing.T) string {
	t.Helper()

	home := t.TempDir()
	t.Cleanup(writeguard.OverrideRealHome(home))
	t.Setenv(writeguard.EnvVar, "1")
	t.Setenv(writeguard.RootsEnvVar, "")
	t.Setenv("HOME", home)
	t.Setenv("OCFP_HOME", "")

	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME"} {
		t.Setenv(key, "")
	}

	return home
}

// panicMessage runs fn and returns what it panicked with, or "" when it
// returned.
func panicMessage(fn func()) (message string) {
	defer func() {
		if r := recover(); r != nil {
			message = fmt.Sprint(r)
		}
	}()

	fn()

	return ""
}

// A test process that resolves any of ocfp's directories to the
// developer's real one panics, naming the resolver, before anything can
// read or write there.
func TestSafetyGuard_PanicsForEveryRealDefault(t *testing.T) {
	for name, resolve := range map[string]func() string{
		"config.OcfpHome":   config.OcfpHome,
		"config.LegacyHome": config.LegacyHome,
		"ConfigHome":        config.ConfigHome,
		"StateHome":         config.StateHome,
		"DataHome":          config.DataHome,
	} {
		t.Run(name, func(t *testing.T) {
			home := guardedRealHome(t)

			message := panicMessage(func() { resolve() })
			assert.Contains(t, message, "SAFETY GUARD", "resolving a real default must panic")
			assert.Contains(t, message, home)
		})
	}
}

// OCFP_HOME set to a real directory panics as well.
func TestSafetyGuard_PanicsForOcfpHomeUnderTheRealHome(t *testing.T) {
	home := guardedRealHome(t)
	t.Setenv("OCFP_HOME", filepath.Join(home, ".ocfp"))

	assert.Contains(t, panicMessage(func() { config.StateHome() }), "config.OcfpHome")
}

// SaveState in a test process panics before it writes the real state file,
// and leaves no file behind.
func TestSafetyGuard_SaveStateWritesNothingUnderTheRealHome(t *testing.T) {
	home := guardedRealHome(t)

	assert.Contains(t, panicMessage(func() { _ = config.SaveState(&config.StateFile{CurrentBloc: "x"}) }), "SAFETY GUARD")

	entries, err := os.ReadDir(home)
	require.NoError(t, err)
	assert.Empty(t, entries, "the guard must stop the write before it creates anything")
}

// The guard lets the same calls through when the directories are
// somewhere other than the real home.
func TestSafetyGuard_AllowsATemporaryHome(t *testing.T) {
	guardedRealHome(t)
	t.Setenv("HOME", t.TempDir())

	assert.Empty(t, panicMessage(func() { require.NoError(t, config.SaveState(&config.StateFile{CurrentBloc: "x"})) }))
	assert.Empty(t, panicMessage(func() { config.LegacyHome() }))
}

// A real run never sets the guard's variable, so it resolves and writes
// its real directories as it always has.
func TestSafetyGuard_IsOffOutsideATestProcess(t *testing.T) {
	home := guardedRealHome(t)
	t.Setenv(writeguard.EnvVar, "")

	assert.Equal(t, filepath.Join(home, ".local", "state", "ocfp"), config.StateHome())
}
