package vault_test

import (
	"fmt"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	os.Setenv("OCFP_TEST_SAFETY_GUARD", "1")
	defer os.Unsetenv("OCFP_TEST_SAFETY_GUARD")

	tmpDir, err := os.MkdirTemp("", "ocfp-vault-test-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "vault testmain: MkdirTemp failed:", err)
		os.Exit(1)
	}
	os.Setenv("OCFP_HOME", tmpDir)
	defer os.Unsetenv("OCFP_HOME")

	code := m.Run()
	os.RemoveAll(tmpDir)
	os.Exit(code)
}
