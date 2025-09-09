package integration_test

import (
	"os"
	"path/filepath"
	"testing"
)

// createScript creates a script under baseDir/subdir with the given name, content and mode.
func createScript(t *testing.T, baseDir, subdir, name, content string, mode os.FileMode) {
	t.Helper()

	dir := filepath.Join(baseDir, subdir)

	err := os.MkdirAll(dir, 0750)
	if err != nil {
		t.Fatalf("failed to create script dir: %v", err)
	}

	path := filepath.Join(dir, name)

	err = os.WriteFile(path, []byte(content), mode)
	if err != nil {
		t.Fatalf("failed to write script: %v", err)
	}
}

// chdir changes to dir and returns a cleanup function to restore the previous working directory.
func chdir(t *testing.T, dir string) func() {
	t.Helper()

	t.Chdir(dir)

	return func() {} // t.Chdir automatically restores
}

// withEnv sets an environment variable and returns a cleanup to unset/restore it.
func withEnv(t *testing.T, key, value string) func() {
	t.Helper()

	t.Setenv(key, value)

	return func() {} // t.Setenv automatically restores
}
