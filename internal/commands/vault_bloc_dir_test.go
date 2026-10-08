package commands

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/spf13/viper"
)

const vaultBlocDirTestBloc = "ocfp-lab-blocdir"

// vaultBlocDirs isolates HOME and returns the bloc's XDG data directory and
// its legacy ~/.ocfp directory, neither of which exists yet.
func vaultBlocDirs(t *testing.T) (xdgDir, legacyDir string) {
	t.Helper()

	home := isolateXDGEnv(t)

	return filepath.Join(home, ".local", "share", "ocfp", vaultBlocDirTestBloc),
		filepath.Join(home, ".ocfp", vaultBlocDirTestBloc)
}

func writeTestVault(t *testing.T, blocDir string) {
	t.Helper()

	writeMigrateTestFile(t, filepath.Join(blocDir, "vault", "data", "vault.db"), "raft")
	writeMigrateTestFile(t, filepath.Join(blocDir, "vault", "root.key"), "root")
	writeMigrateTestFile(t, filepath.Join(blocDir, "vault", "unseal.keys"), "unseal")
}

// An empty XDG bloc directory used to win over a legacy one, so reconcile
// saw no data and no keys there and started a fresh vault.
func TestGetVaultInceptionPaths_EmptyXDGDirKeepsLegacyVault(t *testing.T) {
	xdgDir, legacyDir := vaultBlocDirs(t)

	err := os.MkdirAll(xdgDir, 0o700)
	if err != nil {
		t.Fatal(err)
	}

	writeTestVault(t, legacyDir)

	paths := mustInceptionPaths(t, vaultBlocDirTestBloc, false)

	want := map[string]string{
		"vaultDir":       filepath.Join(legacyDir, "vault", "data"),
		"rootKeyFile":    filepath.Join(legacyDir, "vault", "root.key"),
		"unsealKeysFile": filepath.Join(legacyDir, "vault", "unseal.keys"),
	}

	for key, path := range want {
		if paths[key] != path {
			t.Errorf("paths[%q] = %q, want %q", key, paths[key], path)
		}
	}

	if data := classifyVaultData(paths["vaultDir"]); data != vaultDataRaft {
		t.Errorf("classifyVaultData(%q) = %v, want raft", paths["vaultDir"], data)
	}
}

func TestGetVaultInceptionPaths_VaultInBothDirsRefuses(t *testing.T) {
	xdgDir, legacyDir := vaultBlocDirs(t)
	writeTestVault(t, xdgDir)
	writeTestVault(t, legacyDir)

	_, err := getVaultInceptionPaths(vaultBlocDirTestBloc, false)
	if !errors.Is(err, config.ErrBlocVaultInBothDirs) {
		t.Fatalf("getVaultInceptionPaths() error = %v, want ErrBlocVaultInBothDirs", err)
	}

	// A test-mode vault lives apart from the bloc's, so the bloc's two
	// directories do not stop it.
	_, err = getVaultInceptionPaths(vaultBlocDirTestBloc, true)
	if err != nil {
		t.Fatalf("getVaultInceptionPaths(test mode) error = %v, want nil", err)
	}
}

// Each vault command refuses before it takes the lock or touches either
// vault when the bloc has a vault in both directories.
func TestVaultCommands_VaultInBothDirsRefuseBeforeTouchingAnything(t *testing.T) {
	commands := map[string]func() error{
		"inception": func() error { return ensureInceptionVault(vaultBlocDirTestBloc, false) },
		"start":     runVaultStart,
		"teardown":  func() error { return runVaultTeardown(true) },
	}

	for name, run := range commands {
		t.Run(name, func(t *testing.T) {
			xdgDir, legacyDir := vaultBlocDirs(t)
			writeTestVault(t, xdgDir)
			writeTestVault(t, legacyDir)

			viper.Reset()
			t.Cleanup(viper.Reset)
			viper.Set("bloc", vaultBlocDirTestBloc)

			err := run()
			if !errors.Is(err, config.ErrBlocVaultInBothDirs) {
				t.Fatalf("error = %v, want ErrBlocVaultInBothDirs", err)
			}

			if name == "start" && !errors.Is(err, ErrVaultStartRefused) {
				t.Errorf("vault start error = %v, want it to wrap ErrVaultStartRefused", err)
			}

			mustNotExist(t, filepath.Join(config.StateHome(), vaultBlocDirTestBloc, inceptionLockFileName))

			for _, dir := range []string{xdgDir, legacyDir} {
				entries, readErr := os.ReadDir(filepath.Join(dir, "vault"))
				if readErr != nil {
					t.Fatal(readErr)
				}

				if len(entries) != 3 {
					t.Errorf("%s/vault holds %d entries, want the 3 it started with", dir, len(entries))
				}

				if got := mustReadFile(t, filepath.Join(dir, "vault", "root.key")); got != "root" {
					t.Errorf("%s/vault/root.key changed", dir)
				}
			}
		})
	}
}
