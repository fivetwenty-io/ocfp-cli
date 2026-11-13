package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/writeguard"
	"github.com/stretchr/testify/assert"
)

// A test process that opens a log file under the developer's real state
// directory panics before it creates the log directory.
func TestInitializePanicsUnderTheRealStateDirInATest(t *testing.T) {
	home := t.TempDir()
	t.Cleanup(writeguard.OverrideRealHome(home))
	t.Setenv(writeguard.EnvVar, "1")

	logDir := filepath.Join(home, ".local", "state", "ocfp")

	assert.Contains(t, guardPanic(func() {
		_ = Initialize(Config{Level: "info", LogDir: logDir, BlocName: "mybloc", Command: "env"})
	}), "logger file")

	_, err := os.Stat(filepath.Join(home, ".local"))
	assert.True(t, os.IsNotExist(err), "the guard must stop the logger before it creates anything")
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
