package commands

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
)

// TestGetVaultInceptionPaths_PortIsPerBloc guards concurrent bootstraps: two
// blocs on one workstation must not resolve to the same listener port, or the
// second `safe local` fails with EADDRINUSE and the winner's data directory
// silently accumulates both blocs' secrets.
func TestGetVaultInceptionPaths_PortIsPerBloc(t *testing.T) {
	t.Parallel()

	first := getVaultInceptionPaths("ocfp-lab-drgao", false)
	second := getVaultInceptionPaths("ocfp-lab-drhu", false)

	if first["port"] == second["port"] {
		t.Errorf("blocs share inception port %q", first["port"])
	}

	if first["port"] == "8234" || second["port"] == "8234" {
		t.Errorf("bloc-scoped inception must not use the shared legacy port 8234")
	}
}

// TestGetVaultInceptionPaths_LogFileIsPerBloc guards readiness detection: the
// log file is tee'd into by `safe local` and read back to decide whether the
// vault came up. A shared file makes one bloc read another bloc's startup
// output, reporting a bogus failure — or a bogus success.
func TestGetVaultInceptionPaths_LogFileIsPerBloc(t *testing.T) {
	t.Parallel()

	first := getVaultInceptionPaths("ocfp-lab-drgao", false)
	second := getVaultInceptionPaths("ocfp-lab-drhu", false)

	if first["logFile"] == second["logFile"] {
		t.Errorf("blocs share inception log file %q", first["logFile"])
	}

	if filepath.Dir(first["logFile"]) != first["logDir"] {
		t.Errorf("logFile %q is not inside logDir %q", first["logFile"], first["logDir"])
	}
}

func TestGetVaultInceptionPaths(t *testing.T) {
	t.Parallel()

	homeDir := os.Getenv("HOME")

	tests := []struct {
		name     string
		blocName string
		testMode bool
		checks   map[string]string
	}{
		{
			name:     "no bloc",
			blocName: "",
			testMode: false,
			checks: map[string]string{
				"tmuxSession": "inception-vault",
				"vaultName":   "inception",
				"port":        "8234",
				"vaultDir":    filepath.Join(homeDir, ".vault"),
			},
		},
		{
			name:     "with bloc",
			blocName: "520-aws-wayne",
			testMode: false,
			checks: map[string]string{
				"tmuxSession": "520-aws-wayne-inception-vault",
				"vaultName":   "520-aws-wayne-inception",
				"port":        strconv.Itoa(config.InceptionVaultPort("520-aws-wayne")),
				"vaultDir":    filepath.Join(config.OcfpBlocDir("520-aws-wayne"), "vault", "data"),
				"rootKeyFile": filepath.Join(config.OcfpBlocDir("520-aws-wayne"), "vault", "root.key"),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			paths := getVaultInceptionPaths(tc.blocName, tc.testMode)

			for key, expected := range tc.checks {
				if paths[key] != expected {
					t.Errorf("paths[%q] = %q, want %q", key, paths[key], expected)
				}
			}
		})
	}
}

func TestGetVaultInceptionPaths_TestMode(t *testing.T) {
	t.Parallel()

	homeDir := os.Getenv("HOME")
	paths := getVaultInceptionPaths("any-bloc", true)

	// Test mode should override bloc-specific paths
	if paths["tmuxSession"] != "test-inception-vault" {
		t.Errorf("tmuxSession = %q, want %q", paths["tmuxSession"], "test-inception-vault")
	}

	if paths["vaultName"] != "test-inception" {
		t.Errorf("vaultName = %q, want %q", paths["vaultName"], "test-inception")
	}

	if paths["port"] != "8235" {
		t.Errorf("port = %q, want %q", paths["port"], "8235")
	}

	if paths["vaultDir"] != filepath.Join(homeDir, ".test-vault") {
		t.Errorf("vaultDir = %q, want %q", paths["vaultDir"], filepath.Join(homeDir, ".test-vault"))
	}
}

// TestGetVaultInceptionPaths_NoBlocLogDirUnderStateHomeNotOcfpHome verifies
// the no-bloc inception log directory resolves under the XDG state-class
// root (config.GetLogDir()) rather than a hardcoded config.OcfpHome() join,
// when neither the new nor the legacy log directory pre-exists.
func TestGetVaultInceptionPaths_NoBlocLogDirUnderStateHomeNotOcfpHome(t *testing.T) {
	legacyHome := t.TempDir()
	xdgStateBase := t.TempDir()

	t.Setenv("OCFP_HOME", "")
	t.Setenv("HOME", legacyHome)
	t.Setenv("XDG_STATE_HOME", xdgStateBase)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")

	wantLogDir := filepath.Join(config.GetLogDir(), "vault")

	paths := getVaultInceptionPaths("", false)

	if paths["logDir"] != wantLogDir {
		t.Errorf("logDir = %q, want %q", paths["logDir"], wantLogDir)
	}

	if strings.Contains(paths["logDir"], legacyHome) {
		t.Errorf("logDir = %q, must not resolve under legacy HOME %q", paths["logDir"], legacyHome)
	}
}

// TestGetVaultInceptionPaths_BlocLogDirUnderStateHomeNotDataHome verifies
// the bloc-scoped inception log directory resolves under the XDG
// state-class root rather than config.OcfpBlocDir() (data-class), when
// neither the new nor the legacy log directory pre-exists.
func TestGetVaultInceptionPaths_BlocLogDirUnderStateHomeNotDataHome(t *testing.T) {
	legacyHome := t.TempDir()
	xdgStateBase := t.TempDir()
	xdgDataBase := t.TempDir()

	t.Setenv("OCFP_HOME", "")
	t.Setenv("HOME", legacyHome)
	t.Setenv("XDG_STATE_HOME", xdgStateBase)
	t.Setenv("XDG_DATA_HOME", xdgDataBase)
	t.Setenv("XDG_CONFIG_HOME", "")

	const blocName = "xdg-log-bloc"

	wantLogDir := filepath.Join(xdgStateBase, "ocfp", blocName, VaultInceptionLogDir)

	paths := getVaultInceptionPaths(blocName, false)

	if paths["logDir"] != wantLogDir {
		t.Errorf("logDir = %q, want %q", paths["logDir"], wantLogDir)
	}

	if strings.Contains(paths["logDir"], xdgDataBase) {
		t.Errorf("logDir = %q, must not resolve under XDG data home %q", paths["logDir"], xdgDataBase)
	}

	if strings.Contains(paths["logDir"], legacyHome) {
		t.Errorf("logDir = %q, must not resolve under legacy HOME %q", paths["logDir"], legacyHome)
	}
}
