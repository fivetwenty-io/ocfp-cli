package ssh

import (
	"os"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/knownhostsguard"
	"github.com/fivetwenty-io/ocfp-cli/internal/testenv"
)

// TestMain gives the tests a home of their own, and fails the run if a
// test touches the developer's own known_hosts. Tests here run the system
// ssh, which finds ~ from the passwd entry rather than $HOME.
func TestMain(m *testing.M) {
	os.Exit(testenv.Run(func() int { return knownhostsguard.Run(m) }))
}
