package testenv

import (
	"fmt"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/writeguard"
)

// GuardOnly makes the write guard refuse path, and no other directory the
// test names, until the test ends. A test uses it to prove that the check
// at one write point fires. The config resolvers check the directories
// they return, so a test that guarded a whole directory would hit the
// resolver's check before the one it means to test. Guarding the exact
// file the write point names leaves the resolvers alone.
//
// It sets environment variables, so the test can't run in parallel.
func GuardOnly(tb testing.TB, path string) {
	tb.Helper()

	tb.Setenv(writeguard.EnvVar, "1")
	tb.Setenv(writeguard.RootsEnvVar, path)
}

// PanicMessage runs fn and returns what it panicked with, or "" when it
// returned.
func PanicMessage(fn func()) (message string) {
	defer func() {
		if r := recover(); r != nil {
			message = fmt.Sprint(r)
		}
	}()

	fn()

	return ""
}
