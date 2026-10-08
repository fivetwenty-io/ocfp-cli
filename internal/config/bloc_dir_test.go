package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
)

const blocDirTestBloc = "ocfp-lab-blocdir"

// blocDirRoots isolates the XDG data root and the legacy ~/.ocfp root in
// temp directories and returns the bloc's directory under each, neither of
// which exists yet.
func blocDirRoots(t *testing.T) (xdgDir, legacyDir string) {
	t.Helper()

	root := t.TempDir()
	home := filepath.Join(root, "home")
	dataHome := filepath.Join(root, "data")

	t.Setenv("OCFP_HOME", "")
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", dataHome)

	return filepath.Join(dataHome, "ocfp", blocDirTestBloc), filepath.Join(home, ".ocfp", blocDirTestBloc)
}

func writeBlocDirFile(t *testing.T, path string) {
	t.Helper()

	err := os.MkdirAll(filepath.Dir(path), 0o700)
	if err != nil {
		t.Fatalf("MkdirAll(%q): %v", filepath.Dir(path), err)
	}

	err = os.WriteFile(path, []byte("x"), 0o600)
	if err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
}

func mkBlocDir(t *testing.T, path string) {
	t.Helper()

	err := os.MkdirAll(path, 0o700)
	if err != nil {
		t.Fatalf("MkdirAll(%q): %v", path, err)
	}
}

// writeRaftVault lays out a vault with raft data and both key files.
func writeRaftVault(t *testing.T, blocDir string) {
	t.Helper()

	writeBlocDirFile(t, filepath.Join(blocDir, "vault", "data", "vault.db"))
	writeBlocDirFile(t, filepath.Join(blocDir, "vault", "root.key"))
	writeBlocDirFile(t, filepath.Join(blocDir, "vault", "unseal.keys"))
}

func requireBlocDir(t *testing.T, want string) {
	t.Helper()

	got, err := config.OcfpBlocDir(blocDirTestBloc)
	if err != nil {
		t.Fatalf("OcfpBlocDir() error = %v, want nil", err)
	}

	if got != want {
		t.Errorf("OcfpBlocDir() = %q, want %q", got, want)
	}
}

func TestOcfpBlocDir_EmptyXDGDirDoesNotHideLegacyVault(t *testing.T) {
	xdgDir, legacyDir := blocDirRoots(t)
	mkBlocDir(t, xdgDir)
	writeRaftVault(t, legacyDir)

	requireBlocDir(t, legacyDir)
}

func TestOcfpBlocDir_EmptyXDGDataDirDoesNotHideLegacyVault(t *testing.T) {
	xdgDir, legacyDir := blocDirRoots(t)
	mkBlocDir(t, filepath.Join(xdgDir, "vault", "data"))
	writeRaftVault(t, legacyDir)

	requireBlocDir(t, legacyDir)
}

// An XDG directory that holds other bloc files but no vault must not hide a
// legacy vault either, or reconcile would see no data and no keys and start
// a fresh vault.
func TestOcfpBlocDir_XDGContentWithoutVaultDoesNotHideLegacyVault(t *testing.T) {
	xdgDir, legacyDir := blocDirRoots(t)
	writeBlocDirFile(t, filepath.Join(xdgDir, "ssh", "id_ed25519"))
	writeBlocDirFile(t, filepath.Join(xdgDir, "deployments", "ocf", "x.yml"))
	writeRaftVault(t, legacyDir)

	requireBlocDir(t, legacyDir)
}

func TestOcfpBlocDir_LegacyKeyFileAloneCountsAsVault(t *testing.T) {
	xdgDir, legacyDir := blocDirRoots(t)
	writeBlocDirFile(t, filepath.Join(xdgDir, "ssh", "id_ed25519"))
	writeBlocDirFile(t, filepath.Join(legacyDir, "vault", "unseal.keys"))

	requireBlocDir(t, legacyDir)
}

func TestOcfpBlocDir_XDGVaultWins(t *testing.T) {
	xdgDir, legacyDir := blocDirRoots(t)
	writeRaftVault(t, xdgDir)
	writeBlocDirFile(t, filepath.Join(legacyDir, "ssh", "id_ed25519"))

	requireBlocDir(t, xdgDir)
}

func TestOcfpBlocDir_XDGContentWinsWhenNeitherHoldsVault(t *testing.T) {
	xdgDir, legacyDir := blocDirRoots(t)
	writeBlocDirFile(t, filepath.Join(xdgDir, "ssh", "id_ed25519"))
	writeBlocDirFile(t, filepath.Join(legacyDir, "ssh", "id_ed25519"))

	requireBlocDir(t, xdgDir)
}

func TestOcfpBlocDir_LegacyContentWinsOverEmptyXDGDir(t *testing.T) {
	xdgDir, legacyDir := blocDirRoots(t)
	mkBlocDir(t, xdgDir)
	writeBlocDirFile(t, filepath.Join(legacyDir, "ssh", "id_ed25519"))

	requireBlocDir(t, legacyDir)
}

func TestOcfpBlocDir_NeitherExistsReturnsXDGDir(t *testing.T) {
	xdgDir, _ := blocDirRoots(t)

	requireBlocDir(t, xdgDir)
}

func TestOcfpBlocDir_EmptyDirsReturnXDGDir(t *testing.T) {
	xdgDir, legacyDir := blocDirRoots(t)
	mkBlocDir(t, xdgDir)
	mkBlocDir(t, legacyDir)

	requireBlocDir(t, xdgDir)
}

// Two vaults are a choice only a person can make, so the resolution refuses
// and names both directories rather than picking one.
func TestOcfpBlocDir_VaultInBothDirsIsAnError(t *testing.T) {
	cases := map[string]func(t *testing.T, xdgDir, legacyDir string){
		"raft in both": func(t *testing.T, xdgDir, legacyDir string) {
			t.Helper()
			writeRaftVault(t, xdgDir)
			writeRaftVault(t, legacyDir)
		},
		"file storage in XDG and raft in legacy": func(t *testing.T, xdgDir, legacyDir string) {
			t.Helper()
			writeBlocDirFile(t, filepath.Join(xdgDir, "vault", "data", "core", "_keyring"))
			writeRaftVault(t, legacyDir)
		},
		"keys only in XDG and data only in legacy": func(t *testing.T, xdgDir, legacyDir string) {
			t.Helper()
			writeBlocDirFile(t, filepath.Join(xdgDir, "vault", "root.key"))
			writeBlocDirFile(t, filepath.Join(legacyDir, "vault", "data", "vault.db"))
		},
	}

	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			xdgDir, legacyDir := blocDirRoots(t)
			setup(t, xdgDir, legacyDir)

			got, err := config.OcfpBlocDir(blocDirTestBloc)
			if !errors.Is(err, config.ErrBlocVaultInBothDirs) {
				t.Fatalf("OcfpBlocDir() = %q, %v; want ErrBlocVaultInBothDirs", got, err)
			}

			if got != "" {
				t.Errorf("OcfpBlocDir() path = %q, want empty on error", got)
			}

			for _, want := range []string{xdgDir, legacyDir, blocDirTestBloc + "-inception-vault"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not name %q", err, want)
				}
			}
		})
	}
}

// Both directories are untouched by a refusal.
func TestOcfpBlocDir_VaultInBothDirsChangesNothing(t *testing.T) {
	xdgDir, legacyDir := blocDirRoots(t)
	writeRaftVault(t, xdgDir)
	writeRaftVault(t, legacyDir)

	_, err := config.OcfpBlocDir(blocDirTestBloc)
	if err == nil {
		t.Fatal("OcfpBlocDir() error = nil, want a refusal")
	}

	for _, dir := range []string{xdgDir, legacyDir} {
		for _, name := range []string{"data/vault.db", "root.key", "unseal.keys"} {
			_, statErr := os.Stat(filepath.Join(dir, "vault", name))
			if statErr != nil {
				t.Errorf("%s/vault/%s: %v", dir, name, statErr)
			}
		}
	}
}

// With OCFP_HOME set, the XDG and legacy roots are one directory, so there is
// nothing to choose between.
func TestOcfpBlocDir_OCFPHomeOverride(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("OCFP_HOME", tmpDir)
	writeRaftVault(t, filepath.Join(tmpDir, blocDirTestBloc))

	got, err := config.OcfpBlocDir(blocDirTestBloc)
	if err != nil {
		t.Fatalf("OcfpBlocDir() error = %v", err)
	}

	if want := filepath.Join(tmpDir, blocDirTestBloc); got != want {
		t.Errorf("OcfpBlocDir() = %q, want %q", got, want)
	}
}

// A directory that cannot be inspected might hold a vault, so the
// resolution fails rather than guessing past it.
func TestOcfpBlocDir_UninspectableDirIsAnError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not restrict this process")
	}

	xdgDir, legacyDir := blocDirRoots(t)
	writeRaftVault(t, legacyDir)
	mkBlocDir(t, filepath.Join(xdgDir, "vault"))

	err := os.Chmod(filepath.Join(xdgDir, "vault"), 0o000)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = os.Chmod(filepath.Join(xdgDir, "vault"), 0o700) })

	got, err := config.OcfpBlocDir(blocDirTestBloc)
	if err == nil {
		t.Fatalf("OcfpBlocDir() = %q, nil; want an error", got)
	}
}

func symlinkBlocDir(t *testing.T, target, link string) {
	t.Helper()

	mkBlocDir(t, filepath.Dir(link))

	err := os.Symlink(target, link)
	if err != nil {
		t.Fatalf("Symlink(%q, %q): %v", target, link, err)
	}
}

// A link between the two directories makes one vault reachable through both
// paths. That is one vault, not two, so the resolution uses it, through the
// XDG path, and never refuses or tells anyone to move it aside.
func TestOcfpBlocDir_LinkedDirsHoldOneVault(t *testing.T) {
	cases := map[string]func(t *testing.T, xdgDir, legacyDir string){
		"XDG bloc dir links to the legacy one": func(t *testing.T, xdgDir, legacyDir string) {
			t.Helper()
			writeRaftVault(t, legacyDir)
			symlinkBlocDir(t, legacyDir, xdgDir)
		},
		"legacy bloc dir links to the XDG one": func(t *testing.T, xdgDir, legacyDir string) {
			t.Helper()
			writeRaftVault(t, xdgDir)
			symlinkBlocDir(t, xdgDir, legacyDir)
		},
		"XDG ocfp root links to the legacy root": func(t *testing.T, xdgDir, legacyDir string) {
			t.Helper()
			writeRaftVault(t, legacyDir)
			symlinkBlocDir(t, filepath.Dir(legacyDir), filepath.Dir(xdgDir))
		},
		"XDG vault dir links to the legacy one": func(t *testing.T, xdgDir, legacyDir string) {
			t.Helper()
			writeRaftVault(t, legacyDir)
			writeBlocDirFile(t, filepath.Join(xdgDir, "ssh", "id_rsa"))
			symlinkBlocDir(t, filepath.Join(legacyDir, "vault"), filepath.Join(xdgDir, "vault"))
		},
		"legacy vault dir links to the XDG one": func(t *testing.T, xdgDir, legacyDir string) {
			t.Helper()
			writeRaftVault(t, xdgDir)
			writeBlocDirFile(t, filepath.Join(legacyDir, "ssh", "id_rsa"))
			symlinkBlocDir(t, filepath.Join(xdgDir, "vault"), filepath.Join(legacyDir, "vault"))
		},
	}

	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			xdgDir, legacyDir := blocDirRoots(t)
			setup(t, xdgDir, legacyDir)

			requireBlocDir(t, xdgDir)

			_, err := os.Stat(filepath.Join(xdgDir, "vault", "data", "vault.db"))
			if err != nil {
				t.Errorf("the vault is not reachable through the chosen directory: %v", err)
			}
		})
	}
}

// The refusal's advice moves a vault directory aside. Through a link that
// could move the only vault, so the advice says to check for links first and
// to move nothing when both paths lead to one place.
func TestOcfpBlocDir_VaultInBothDirsAdviceWarnsAboutLinks(t *testing.T) {
	xdgDir, legacyDir := blocDirRoots(t)
	writeRaftVault(t, xdgDir)
	writeRaftVault(t, legacyDir)

	_, err := config.OcfpBlocDir(blocDirTestBloc)
	if !errors.Is(err, config.ErrBlocVaultInBothDirs) {
		t.Fatalf("OcfpBlocDir() error = %v, want ErrBlocVaultInBothDirs", err)
	}

	for _, want := range []string{
		"realpath " + filepath.Join(xdgDir, "vault") + " " + filepath.Join(legacyDir, "vault"),
		"link",
		"move nothing",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not say %q", err, want)
		}
	}
}
