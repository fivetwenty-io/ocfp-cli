package cli

import (
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/testenv"
)

// TestMain gives the tests a home of their own, which keeps the pre-run
// handler's log files and active-command locks out of the developer's
// real ocfp directories.
func TestMain(m *testing.M) {
	testenv.Main(m)
}
