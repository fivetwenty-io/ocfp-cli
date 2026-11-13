package cli

import (
	"os"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/stateguard"
)

// TestMain keeps the pre-run handler's log files and active-command locks
// out of the developer's own ocfp directories by pointing OCFP_HOME at a
// temporary one, and fails the run if a test still changes them.
func TestMain(m *testing.M) {
	tmpDir, err := os.MkdirTemp("", "ocfp-cli-test-*")
	if err != nil {
		os.Stderr.WriteString("cannot make a home for the tests: " + err.Error() + "\n")
		os.Exit(1)
	}

	code := stateguard.Run(homed{m: m, home: tmpDir})

	os.RemoveAll(tmpDir)
	os.Exit(code)
}

// homed sets OCFP_HOME for the run, after the guard has read the
// environment it protects.
type homed struct {
	m    *testing.M
	home string
}

func (h homed) Run() int {
	os.Setenv("OCFP_HOME", h.home)

	return h.m.Run()
}
