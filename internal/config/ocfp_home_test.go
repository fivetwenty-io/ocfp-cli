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
