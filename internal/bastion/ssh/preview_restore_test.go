package ssh

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// previewRestoreHome points every directory the key restore uses at a
// fresh temp dir and returns it.
func previewRestoreHome(t *testing.T) string {
	t.Helper()

	base := t.TempDir()
	t.Setenv("HOME", base)
	t.Setenv("OCFP_HOME", "")
	t.Setenv("XDG_DATA_HOME", filepath.Join(base, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(base, "state"))

	return base
}

// filesUnder lists every path under root.
func filesUnder(t *testing.T, root string) []string {
	t.Helper()

	var paths []string

	err := filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		paths = append(paths, path)

		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	return paths
}

// The preview returns the path that the restore writes the key to, writes
// nothing, and logs the restore that it would make in its place.
func TestPreviewRestoreKeyFromConfigWritesNothing(t *testing.T) {
	base := previewRestoreHome(t)

	core, logs := observer.New(zapcore.InfoLevel)
	km := &KeyManager{log: zap.New(core).Sugar()}

	tmpKeyPath := filepath.Join(t.TempDir(), "source_key")
	writeTestEd25519Key(t, tmpKeyPath)

	pemBytes, err := os.ReadFile(tmpKeyPath) // #nosec G304 -- test-generated fixture path
	if err != nil {
		t.Fatal(err)
	}

	before := filesUnder(t, base)

	got, err := km.PreviewRestoreKeyFromConfig("bloc", string(pemBytes))
	if err != nil {
		t.Fatalf("preview returned error: %v", err)
	}

	if after := filesUnder(t, base); len(after) != len(before) {
		t.Fatalf("preview wrote files: before %v, after %v", before, after)
	}

	restores := logs.FilterMessage(
		"DRY RUN: Would restore the SSH private key from the config, and did not check that it can be written there")
	if restores.Len() != 1 {
		t.Fatalf("preview logged %d restores, want 1", restores.Len())
	}

	if logged := restores.All()[0].ContextMap()["path"]; logged != got {
		t.Errorf("preview logged path %v, returned %q", logged, got)
	}

	want, err := NewKeyManager().RestoreKeyFromConfig("bloc", string(pemBytes))
	if err != nil {
		t.Fatal(err)
	}

	if got != want {
		t.Errorf("preview returned %q, the restore wrote %q", got, want)
	}

	if km.IsKeyDataPasswordProtected(pemBytes) {
		t.Error("an unprotected key reads as protected")
	}
}

// The preview refuses the bloc names the restore refuses, with the same
// error, and returns an empty path for an empty key as the restore does.
func TestPreviewRestoreKeyFromConfigFailsWhereTheRestoreFails(t *testing.T) {
	for _, bloc := range []string{"../escape", "nested/bloc", "/absolute", ".", "..", "", `nested\bloc`} {
		t.Run(bloc, func(t *testing.T) {
			previewRestoreHome(t)

			_, realErr := NewKeyManager().RestoreKeyFromConfig(bloc, "private-key")
			path, previewErr := NewKeyManager().PreviewRestoreKeyFromConfig(bloc, "private-key")

			if realErr == nil || previewErr == nil || path != "" {
				t.Fatalf("bloc %q: restore error %v, preview path %q and error %v", bloc, realErr, path, previewErr)
			}

			if realErr.Error() != previewErr.Error() {
				t.Errorf("preview error %q, restore error %q", previewErr, realErr)
			}
		})
	}

	t.Run("empty key", func(t *testing.T) {
		base := previewRestoreHome(t)

		path, err := NewKeyManager().PreviewRestoreKeyFromConfig("bloc", "")
		if path != "" || err != nil {
			t.Fatalf("empty key: path %q, error %v", path, err)
		}

		if entries := filesUnder(t, base); len(entries) != 1 {
			t.Fatalf("empty key wrote files: %v", entries)
		}
	})
}
