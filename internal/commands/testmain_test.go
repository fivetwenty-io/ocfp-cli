package commands

import (
	"os"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/knownhostsguard"
	"github.com/fivetwenty-io/ocfp-cli/internal/testenv"
)

func TestMain(m *testing.M) {
	// A test that needs job exec in a process of its own runs this binary
	// again with jobExecHelperEnv set, and that run is job exec alone.
	if os.Getenv(jobExecHelperEnv) == "1" {
		os.Exit(runJobExecHelper())
	}

	// No test may probe the machine's real ports or tmux sessions for a
	// running vault, so config migrate sees every vault stopped unless a
	// test scripts otherwise with useMigrateVaults.
	migrateVaultProbes = (&fakeMigrateVaults{}).probes()

	// The tests get a home of their own. They run the system ssh, so the
	// run fails if they touch the developer's own known_hosts.
	os.Exit(testenv.Run(func() int { return knownhostsguard.Run(m) }))
}
