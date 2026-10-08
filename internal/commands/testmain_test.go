package commands

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	os.Setenv("OCFP_TEST_SAFETY_GUARD", "1")

	// No test may probe the machine's real ports or tmux sessions for a
	// running vault, so config migrate sees every vault stopped unless a
	// test scripts otherwise with useMigrateVaults.
	migrateVaultProbes = (&fakeMigrateVaults{}).probes()

	tmpDir, err := os.MkdirTemp("", "ocfp-commands-test-*")
	if err == nil {
		os.Setenv("OCFP_HOME", tmpDir)
	}

	code := m.Run()
	os.RemoveAll(tmpDir)
	os.Exit(code)
}
