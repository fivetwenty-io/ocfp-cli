package cli

import (
	"os"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/testenv"
)

// TestMain gives the tests a home of their own, which keeps the pre-run
// handler's log files and active-command locks out of the developer's
// real ocfp directories.
//
// Job exec runs each verb in a process of its own, from this binary, as
// `job verb`, and that run is the whole command line alone.
func TestMain(m *testing.M) {
	if len(os.Args) == 3 && os.Args[1] == "job" && os.Args[2] == "verb" {
		Execute()

		return
	}

	testenv.Main(m)
}
