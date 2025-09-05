package integration

import (
	"os"
	"path/filepath"
	"testing"
)

// createScript creates a script under baseDir/subdir with the given name, content and mode.
func createScript(t *testing.T, baseDir, subdir, name, content string, mode os.FileMode) string {
	t.Helper()

	dir := filepath.Join(baseDir, subdir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("failed to create script dir: %v", err)
	}

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("failed to write script: %v", err)
	}

	return path
}

// chdir changes to dir and returns a cleanup function to restore the previous working directory.
func chdir(t *testing.T, dir string) func() {
	t.Helper()

	oldWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}

	return func() { _ = os.Chdir(oldWd) }
}

// withEnv sets an environment variable and returns a cleanup to unset/restore it.
func withEnv(t *testing.T, key, value string) func() {
	t.Helper()

	old, had := os.LookupEnv(key)
	if err := os.Setenv(key, value); err != nil {
		t.Fatalf("failed to set env %s: %v", key, err)
	}

	return func() {
		if had {
			_ = os.Setenv(key, old)
		} else {
			_ = os.Unsetenv(key)
		}
	}
}
