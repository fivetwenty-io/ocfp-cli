package config

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// sshWarnSetup points the ocfp roots at temp dirs, resets the once-only
// warning, and returns the XDG and legacy ssh dirs.
func sshWarnSetup(t *testing.T) (xdgSSH, legacySSH string) {
	t.Helper()

	root := t.TempDir()
	t.Setenv("OCFP_HOME", "")
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))

	sshKeysInBothDirsOnce = sync.Once{}
	legacyWarnOnce = sync.Once{}

	return filepath.Join(root, "data", "ocfp", "warnbloc", "ssh"),
		filepath.Join(root, "home", ".ocfp", "warnbloc", "ssh")
}

func putSSHFile(t *testing.T, dir, name, body string) {
	t.Helper()

	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

// captureStderr runs fn and returns what it wrote to os.Stderr.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()

	orig := os.Stderr

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe: %v", err)
	}

	os.Stderr = w

	defer func() { os.Stderr = orig }()

	fn()

	_ = w.Close()

	out, _ := io.ReadAll(r)

	return string(out)
}

func TestOcfpSSHKeyDir_DifferingKeysWarnNamingBothDirs(t *testing.T) {
	cases := map[string]struct {
		xdg, legacy map[string]string
		wantWarn    bool
	}{
		"same name, different contents": {
			xdg:      map[string]string{"id_ed25519": "SECRET-ONE"},
			legacy:   map[string]string{"id_ed25519": "SECRET-TWO"},
			wantWarn: true,
		},
		"different key names": {
			xdg:      map[string]string{"id_ed25519": "SECRET-ONE"},
			legacy:   map[string]string{"id_rsa": "SECRET-TWO"},
			wantWarn: true,
		},
		"identical keys": {
			xdg:      map[string]string{"id_ed25519": "SAME"},
			legacy:   map[string]string{"id_ed25519": "SAME"},
			wantWarn: false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			xdgSSH, legacySSH := sshWarnSetup(t)

			for file, body := range tc.xdg {
				putSSHFile(t, xdgSSH, file, body)
			}

			for file, body := range tc.legacy {
				putSSHFile(t, legacySSH, file, body)
			}

			out := captureStderr(t, func() { _ = OcfpSSHKeyDir("warnbloc") })

			if strings.Contains(out, "SECRET") {
				t.Fatalf("warning leaked key contents: %q", out)
			}

			if !tc.wantWarn {
				if out != "" {
					t.Fatalf("unexpected output: %q", out)
				}

				return
			}

			if !strings.Contains(out, xdgSSH) || !strings.Contains(out, legacySSH) {
				t.Fatalf("warning must name both dirs, got %q", out)
			}
		})
	}
}

func TestOcfpSSHKeyDir_LegacyOnlyWarnsAboutTheLegacyPath(t *testing.T) {
	cases := map[string]string{
		"legacy holds a key":                 "id_ed25519",
		"legacy holds a file that is no key": "known_hosts",
	}

	for name, file := range cases {
		t.Run(name, func(t *testing.T) {
			xdgSSH, legacySSH := sshWarnSetup(t)
			putSSHFile(t, legacySSH, file, "SECRET")

			var got string

			out := captureStderr(t, func() { got = OcfpSSHKeyDir("warnbloc") })

			if got != legacySSH {
				t.Fatalf("OcfpSSHKeyDir() = %q, want %q", got, legacySSH)
			}

			if !strings.Contains(out, "legacy path "+legacySSH) || !strings.Contains(out, xdgSSH) {
				t.Fatalf("warning must name the legacy and new dirs, got %q", out)
			}

			if strings.Contains(out, "SECRET") {
				t.Fatalf("warning leaked file contents: %q", out)
			}
		})
	}
}

func TestOcfpSSHKeyDir_XDGKeysDoNotWarnAboutTheLegacyPath(t *testing.T) {
	xdgSSH, _ := sshWarnSetup(t)
	putSSHFile(t, xdgSSH, "id_ed25519", "SECRET")

	out := captureStderr(t, func() { _ = OcfpSSHKeyDir("warnbloc") })

	if out != "" {
		t.Fatalf("unexpected output: %q", out)
	}
}
