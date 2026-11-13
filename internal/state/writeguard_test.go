package state_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/state"
	"github.com/fivetwenty-io/ocfp-cli/internal/writeguard"
	"github.com/stretchr/testify/assert"
)

// A test process that opens state under the developer's real state
// directory panics before it creates the directory.
func TestNewManagerPanicsUnderTheRealStateDirInATest(t *testing.T) {
	home := t.TempDir()
	t.Cleanup(writeguard.OverrideRealHome(home))
	t.Setenv(writeguard.EnvVar, "1")

	stateDir := filepath.Join(home, ".local", "state", "ocfp", "mybloc", "state")

	assert.Contains(t, guardPanic(func() { _, _ = state.NewManager(stateDir) }), "state.NewManager")

	_, err := os.Stat(filepath.Join(home, ".local"))
	assert.True(t, os.IsNotExist(err), "the guard must stop NewManager before it creates anything")
}

// guardPanic runs fn and returns what it panicked with, or "" when it
// returned.
func guardPanic(fn func()) (message string) {
	defer func() {
		if r := recover(); r != nil {
			message = fmt.Sprint(r)
		}
	}()

	fn()

	return ""
}
