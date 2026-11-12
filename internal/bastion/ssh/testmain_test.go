package ssh

import (
	"os"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/knownhostsguard"
)

// TestMain fails the run if a test touches the developer's own
// known_hosts. Tests here run the system ssh, which finds ~ from the
// passwd entry rather than $HOME.
func TestMain(m *testing.M) {
	os.Exit(knownhostsguard.Run(m))
}
