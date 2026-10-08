package commands

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

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

// Each vault command refuses before it touches either vault when the bloc
// has a vault in both directories. Every real run resolves the bloc's
// directory only once it holds the bloc's lock, so it has written the lock
// file by then, and the dry run, which takes no lock, writes nothing.
func TestVaultCommands_VaultInBothDirsRefuseBeforeTouchingAnything(t *testing.T) {
	commands := map[string]struct {
		run   func() error
		locks bool
	}{
		"inception": {run: func() error { return ensureInceptionVault(vaultBlocDirTestBloc, false) }, locks: true},
		"start":     {run: runVaultStart, locks: true},
		"teardown":  {run: func() error { return runVaultTeardown(true) }, locks: true},
		"migrate-storage": {run: func() error {
			return runVaultMigrateStorage(io.Discard, false)
		}, locks: true},
		"migrate-storage dry run": {run: func() error {
			return runVaultMigrateStorage(io.Discard, true)
		}},
	}

	for name, command := range commands {
		t.Run(name, func(t *testing.T) {
			xdgDir, legacyDir := vaultBlocDirs(t)
			writeTestVault(t, xdgDir)
			writeTestVault(t, legacyDir)

			viper.Reset()
			t.Cleanup(viper.Reset)
			viper.Set("bloc", vaultBlocDirTestBloc)

			err := command.run()
			if !errors.Is(err, config.ErrBlocVaultInBothDirs) {
				t.Fatalf("error = %v, want ErrBlocVaultInBothDirs", err)
			}

			if name == "start" && !errors.Is(err, ErrVaultStartRefused) {
				t.Errorf("vault start error = %v, want it to wrap ErrVaultStartRefused", err)
			}

			lockFile := filepath.Join(config.StateHome(), vaultBlocDirTestBloc, inceptionLockFileName)
			if command.locks {
				mustExist(t, lockFile)
			} else {
				mustNotExist(t, lockFile)
			}

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

// A vault command builds its lock's path from the bloc name alone and
// resolves the bloc's directory only once it holds the lock. A run that
// waited while 'ocfp config migrate' moved the bloc therefore works on the
// directory the bloc moved to, rather than starting over in the one it left.
func TestLockedInceptionPaths_ResolveWhereTheBlocIsOnceTheLockIsFree(t *testing.T) {
	xdgDir, legacyDir := vaultBlocDirs(t)
	writeTestVault(t, legacyDir)

	resolved := make(chan string, 1)

	err := config.WithFileLock(inceptionLockPath(vaultBlocDirTestBloc, false), time.Second, func() error {
		go func() {
			runErr := withLockedInceptionPaths(vaultBlocDirTestBloc, false, nil, func(paths map[string]string) error {
				resolved <- paths["vaultDir"]

				return nil
			})
			if runErr != nil {
				resolved <- "error: " + runErr.Error()
			}
		}()

		// A run that resolved before it waited would see only the legacy
		// directory, and it has had time to do so by now.
		time.Sleep(200 * time.Millisecond)

		mkErr := os.MkdirAll(filepath.Dir(xdgDir), 0o700)
		if mkErr != nil {
			return mkErr
		}

		return os.Rename(legacyDir, xdgDir)
	})
	if err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-resolved:
		if want := filepath.Join(xdgDir, "vault", "data"); got != want {
			t.Errorf("the waiting run resolved %q, want %q", got, want)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the waiting run never took the lock")
	}
}

// The lock's path depends only on the bloc name and the test mode, and it is
// the path getVaultInceptionPaths reports.
func TestInceptionLockPath_MatchesTheResolvedPaths(t *testing.T) {
	_, legacyDir := vaultBlocDirs(t)
	writeTestVault(t, legacyDir)

	for _, bloc := range []string{"", vaultBlocDirTestBloc} {
		for _, testMode := range []bool{false, true} {
			paths := mustInceptionPaths(t, bloc, testMode)
			if got := inceptionLockPath(bloc, testMode); got != paths["lockFile"] {
				t.Errorf("inceptionLockPath(%q, %v) = %q, want %q", bloc, testMode, got, paths["lockFile"])
			}
		}
	}
}
