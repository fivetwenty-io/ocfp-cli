// Package knownhostsguard fails a test run that changes the developer's
// own known_hosts file.
//
// OpenSSH finds "~" from the passwd entry, not from $HOME, so a test that
// sets HOME to a temporary directory still writes the developer's real
// file if it lets ssh fall back to its default known_hosts. A package
// whose tests run the system ssh calls Run from its TestMain.
package knownhostsguard

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
)

// Run runs the package's tests through m and returns their exit code. If
// the known_hosts file in the passwd home directory changed while they ran,
// it says so on stderr and the run fails.
func Run(m interface{ Run() int }) int {
	account, err := user.Current()
	if err != nil || account.HomeDir == "" {
		return m.Run()
	}

	return guard(filepath.Join(account.HomeDir, ".ssh", "known_hosts"), os.Stderr, m.Run)
}

// guard runs run and returns its code, or a failing code when the file at
// path differs afterwards, reporting the change to report.
func guard(path string, report io.Writer, run func() int) int {
	before := snapshot(path)
	code := run()

	if after := snapshot(path); !bytes.Equal(before, after) {
		_, _ = fmt.Fprintf(report, "FAIL: the tests changed %s. A test ran ssh without its own "+
			"UserKnownHostsFile; ssh finds ~ from the passwd entry, not $HOME.\n", path)

		if code == 0 {
			code = 1
		}
	}

	return code
}

// snapshot returns a digest of the file at path, or nil when there is no
// such file. An unreadable file gets a digest of the error, so a change
// in what can be read still shows.
func snapshot(path string) []byte {
	data, err := os.ReadFile(path) // #nosec G304 -- the developer's known_hosts, read only
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}

	if err != nil {
		data = []byte("unreadable: " + err.Error())
	}

	sum := sha256.Sum256(data)

	return sum[:]
}
