package bastion

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	os.Setenv("OCFP_TEST_SAFETY_GUARD", "1")

	tmpDir, err := os.MkdirTemp("", "ocfp-bastion-test-*")
	if err == nil {
		os.Setenv("OCFP_HOME", tmpDir)
	}

	code := m.Run()
	os.RemoveAll(tmpDir)
	os.Exit(code)
}
