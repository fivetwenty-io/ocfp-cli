package bootstrap_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fivetwenty-io/ocfp-cli/internal/bootstrap"
	"github.com/fivetwenty-io/ocfp-cli/internal/knownhostsguard"
	"github.com/fivetwenty-io/ocfp-cli/internal/testenv"
)

func TestMain(m *testing.M) {
	os.Setenv("OCFP_TEST_SAFETY_GUARD", "1")

	// Replace the STACKIT eventual-consistency sleep with a no-op for all tests.
	// Called here, before m.Run(), so parallel tests never race on the var.
	bootstrap.SetSleepFn(func(time.Duration) {})

	// Give the tests a home of their own. Tests that use t.Parallel()
	// can't use t.Setenv, so the whole package shares it.
	os.Exit(testenv.Run(func() int {
		// Keep bastion creation away from the real ~/.ssh/known_hosts.
		bootstrap.SetKnownHostsFile(filepath.Join(os.Getenv("OCFP_HOME"), "ssh", "known_hosts"))

		// Tests here run the system ssh, so the run fails if they touch the
		// developer's own known_hosts.
		return knownhostsguard.Run(m)
	}))
}
