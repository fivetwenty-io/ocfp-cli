package bootstrap_test

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMain(m *testing.M) {
	os.Setenv("OCFP_TEST_SAFETY_GUARD", "1")

	// Set OCFP_HOME to a temp dir for all tests in this package.
	// This prevents tests that use t.Parallel() (which can't use
	// t.Setenv) from writing to the real ~/.ocfp directory.
	tmpDir, err := os.MkdirTemp("", "ocfp-bootstrap-test-*")
	if err == nil {
		os.Setenv("OCFP_HOME", tmpDir)

		defer os.RemoveAll(tmpDir)
	}

	// Also create the directory structure that tests expect
	_ = os.MkdirAll(filepath.Join(tmpDir, "config"), 0o750)

	os.Exit(m.Run())
}
