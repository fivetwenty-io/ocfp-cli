package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
)

func requireSSHKeyDir(t *testing.T, want string) {
	t.Helper()

	got := config.OcfpSSHKeyDir(blocDirTestBloc)
	if got != want {
		t.Fatalf("OcfpSSHKeyDir() = %q, want %q", got, want)
	}
}

func writeKey(t *testing.T, dir, name, body string) {
	t.Helper()

	mkBlocDir(t, dir)

	err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600)
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func TestOcfpSSHKeyDir_EmptyXDGDirDoesNotHideLegacyKeys(t *testing.T) {
	xdg, legacy := blocDirRoots(t)
	mkBlocDir(t, filepath.Join(xdg, "ssh"))
	writeKey(t, filepath.Join(legacy, "ssh"), "id_ed25519", "legacy")

	requireSSHKeyDir(t, filepath.Join(legacy, "ssh"))
}

func TestOcfpSSHKeyDir_XDGDirWithOtherFilesDoesNotHideLegacyKeys(t *testing.T) {
	xdg, legacy := blocDirRoots(t)
	writeKey(t, filepath.Join(xdg, "ssh"), "known_hosts", "x")
	writeKey(t, filepath.Join(legacy, "ssh"), "id_rsa", "legacy")

	requireSSHKeyDir(t, filepath.Join(legacy, "ssh"))
}

func TestOcfpSSHKeyDir_XDGKeysWinOverLegacyKeys(t *testing.T) {
	xdg, legacy := blocDirRoots(t)
	writeKey(t, filepath.Join(xdg, "ssh"), "id_ed25519", "new")
	writeKey(t, filepath.Join(legacy, "ssh"), "id_ed25519", "old")

	requireSSHKeyDir(t, filepath.Join(xdg, "ssh"))
}

func TestOcfpSSHKeyDir_XDGKeysWithNoLegacyDir(t *testing.T) {
	xdg, _ := blocDirRoots(t)
	writeKey(t, filepath.Join(xdg, "ssh"), "id_rsa", "new")

	requireSSHKeyDir(t, filepath.Join(xdg, "ssh"))
}

func TestOcfpSSHKeyDir_OnlyLegacyKeys(t *testing.T) {
	_, legacy := blocDirRoots(t)
	writeKey(t, filepath.Join(legacy, "ssh"), "id_ed25519", "old")

	requireSSHKeyDir(t, filepath.Join(legacy, "ssh"))
}

func TestOcfpSSHKeyDir_NoKeysPrefersXDGWithContent(t *testing.T) {
	xdg, legacy := blocDirRoots(t)
	writeKey(t, filepath.Join(xdg, "ssh"), "known_hosts", "x")
	writeKey(t, filepath.Join(legacy, "ssh"), "known_hosts", "y")

	requireSSHKeyDir(t, filepath.Join(xdg, "ssh"))
}

func TestOcfpSSHKeyDir_NoKeysFallsToLegacyWithContent(t *testing.T) {
	xdg, legacy := blocDirRoots(t)
	mkBlocDir(t, filepath.Join(xdg, "ssh"))
	writeKey(t, filepath.Join(legacy, "ssh"), "known_hosts", "y")

	requireSSHKeyDir(t, filepath.Join(legacy, "ssh"))
}

func TestOcfpSSHKeyDir_NothingAnywhereIsXDG(t *testing.T) {
	xdg, _ := blocDirRoots(t)

	requireSSHKeyDir(t, filepath.Join(xdg, "ssh"))
}

func TestOcfpSSHKeyDir_EmptyXDGAndNoLegacyIsXDG(t *testing.T) {
	xdg, _ := blocDirRoots(t)
	mkBlocDir(t, filepath.Join(xdg, "ssh"))

	requireSSHKeyDir(t, filepath.Join(xdg, "ssh"))
}

func TestOcfpSSHKeyDir_LinkedDirsUseXDG(t *testing.T) {
	xdg, legacy := blocDirRoots(t)
	writeKey(t, filepath.Join(legacy, "ssh"), "id_ed25519", "old")
	mkBlocDir(t, xdg)

	err := os.Symlink(filepath.Join(legacy, "ssh"), filepath.Join(xdg, "ssh"))
	if err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	requireSSHKeyDir(t, filepath.Join(xdg, "ssh"))
}

func TestOcfpSSHKeyDir_UninspectableXDGDirIsChosen(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission checks do not apply to root")
	}

	xdg, legacy := blocDirRoots(t)
	writeKey(t, filepath.Join(xdg, "ssh"), "id_ed25519", "new")
	writeKey(t, filepath.Join(legacy, "ssh"), "id_ed25519", "old")

	err := os.Chmod(filepath.Join(xdg, "ssh"), 0)
	if err != nil {
		t.Fatalf("Chmod: %v", err)
	}

	t.Cleanup(func() { _ = os.Chmod(filepath.Join(xdg, "ssh"), 0o700) })

	requireSSHKeyDir(t, filepath.Join(xdg, "ssh"))
}

func TestOcfpSSHKeyDir_Ed25519LegacyBeatsRSAOnlyXDG(t *testing.T) {
	xdg, legacy := blocDirRoots(t)
	writeKey(t, filepath.Join(xdg, "ssh"), "id_rsa", "rsa")
	writeKey(t, filepath.Join(legacy, "ssh"), "id_ed25519", "old")

	requireSSHKeyDir(t, filepath.Join(legacy, "ssh"))
}

func TestOcfpSSHKeyDir_Ed25519XDGBeatsRSAOnlyLegacy(t *testing.T) {
	xdg, legacy := blocDirRoots(t)
	writeKey(t, filepath.Join(xdg, "ssh"), "id_ed25519", "new")
	writeKey(t, filepath.Join(legacy, "ssh"), "id_rsa", "rsa")

	requireSSHKeyDir(t, filepath.Join(xdg, "ssh"))
}

func TestOcfpSSHKeyDir_RSAOnlyInBothUsesXDG(t *testing.T) {
	xdg, legacy := blocDirRoots(t)
	writeKey(t, filepath.Join(xdg, "ssh"), "id_rsa", "new")
	writeKey(t, filepath.Join(legacy, "ssh"), "id_rsa", "old")

	requireSSHKeyDir(t, filepath.Join(xdg, "ssh"))
}

func TestOcfpSSHKeyDir_DanglingXDGKeyLinkDoesNotCount(t *testing.T) {
	xdg, legacy := blocDirRoots(t)
	mkBlocDir(t, filepath.Join(xdg, "ssh"))
	writeKey(t, filepath.Join(legacy, "ssh"), "id_ed25519", "old")

	err := os.Symlink(filepath.Join(xdg, "nowhere"), filepath.Join(xdg, "ssh", "id_ed25519"))
	if err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	requireSSHKeyDir(t, filepath.Join(legacy, "ssh"))
}

// makeUninspectable removes all access to dir so a stat of anything inside it
// fails with a permission error, and restores access when the test ends.
func makeUninspectable(t *testing.T, dir string) {
	t.Helper()

	if os.Geteuid() == 0 {
		t.Skip("permission checks do not apply to root")
	}

	err := os.Chmod(dir, 0)
	if err != nil {
		t.Fatalf("Chmod: %v", err)
	}

	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
}

func TestOcfpSSHKeyDir_UninspectableLegacyDirBeatsRSAOnlyXDG(t *testing.T) {
	xdg, legacy := blocDirRoots(t)
	writeKey(t, filepath.Join(xdg, "ssh"), "id_rsa", "rsa")
	writeKey(t, filepath.Join(legacy, "ssh"), "id_ed25519", "old")
	makeUninspectable(t, filepath.Join(legacy, "ssh"))

	requireSSHKeyDir(t, filepath.Join(legacy, "ssh"))
}

func TestOcfpSSHKeyDir_UninspectableLegacyDirBeatsEmptyXDG(t *testing.T) {
	xdg, legacy := blocDirRoots(t)
	mkBlocDir(t, filepath.Join(xdg, "ssh"))
	writeKey(t, filepath.Join(legacy, "ssh"), "id_ed25519", "old")
	makeUninspectable(t, filepath.Join(legacy, "ssh"))

	requireSSHKeyDir(t, filepath.Join(legacy, "ssh"))
}

func TestOcfpSSHKeyDir_Ed25519XDGBeatsUninspectableLegacyDir(t *testing.T) {
	xdg, legacy := blocDirRoots(t)
	writeKey(t, filepath.Join(xdg, "ssh"), "id_ed25519", "new")
	writeKey(t, filepath.Join(legacy, "ssh"), "id_ed25519", "old")
	makeUninspectable(t, filepath.Join(legacy, "ssh"))

	requireSSHKeyDir(t, filepath.Join(xdg, "ssh"))
}
