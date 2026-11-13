package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fivetwenty-io/ocfp-cli/internal/writeguard"
	"github.com/stretchr/testify/assert"
)

// A test process that would create, remove, list, or reap active-command
// locks under the developer's real state directory panics before it
// touches the directory, so it can neither leave a lock there nor delete
// another session's.
func TestCommandTrackerPanicsUnderTheRealStateDirInATest(t *testing.T) {
	home := t.TempDir()
	t.Cleanup(writeguard.OverrideRealHome(home))
	t.Setenv(writeguard.EnvVar, "1")

	tracker := NewCommandTracker(filepath.Join(home, ".local", "state", "ocfp"))
	now := time.Now()

	for name, call := range map[string]func(){
		"CreateLockFile":    func() { _ = tracker.CreateLockFile(ActiveCommand{PID: os.Getpid(), Timestamp: now}) },
		"RemoveLockFile":    func() { _ = tracker.RemoveLockFile(now, os.Getpid()) },
		"GetActiveCommands": func() { _, _ = tracker.GetActiveCommands() },
		"CleanStaleLocks":   func() { _ = tracker.CleanStaleLocks() },
	} {
		assert.Contains(t, guardPanic(call), "SAFETY GUARD", name)
	}

	_, err := os.Stat(filepath.Join(home, ".local"))
	assert.True(t, os.IsNotExist(err), "the guard must stop the tracker before it creates anything")
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
