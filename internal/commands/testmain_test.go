package commands

import (
	"os"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/knownhostsguard"
)

func TestMain(m *testing.M) {
	// A test that needs job exec in a process of its own runs this binary
	// again with jobExecHelperEnv set, and that run is job exec alone.
	if os.Getenv(jobExecHelperEnv) == "1" {
		os.Exit(runJobExecHelper())
	}

	os.Setenv("OCFP_TEST_SAFETY_GUARD", "1")

	// No test may probe the machine's real ports or tmux sessions for a
	// running vault, so config migrate sees every vault stopped unless a
	// test scripts otherwise with useMigrateVaults.
	migrateVaultProbes = (&fakeMigrateVaults{}).probes()

	tmpDir, err := os.MkdirTemp("", "ocfp-commands-test-*")
	if err == nil {
		os.Setenv("OCFP_HOME", tmpDir)
	}

	// Tests here run the system ssh, so the run fails if they touch the
	// developer's own known_hosts.
	code := knownhostsguard.Run(m)
	os.RemoveAll(tmpDir)
	os.Exit(code)
}
