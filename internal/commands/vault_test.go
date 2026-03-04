package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
)

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
				"port":        "8234",
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
