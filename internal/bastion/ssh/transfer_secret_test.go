package ssh

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// transferSecret stands in for a secret a file holds, such as a join key's
// seed.
const transferSecret = "SAAAOCAJBIFQYDIOB4IBCEQTCQKRMFYYDENBWHA5DYPSAIJCEMSCKJWJX4"

var errTransportDown = errors.New("the transport is down")

// commandRecorder stands in for the remote shell and records every command
// a transfer sends it.
type commandRecorder struct {
	mu       sync.Mutex
	commands []string
}

func (r *commandRecorder) execute(_ context.Context, cmd string) (*CommandResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.commands = append(r.commands, cmd)

	return &CommandResult{}, nil
}

func (r *commandRecorder) recorded() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]string(nil), r.commands...)
}

// failingTransportManager builds a TransferManager whose SFTP and SCP
// uploads both fail, whose remote commands go to a recorder, and whose log
// goes to an observer, so a test can see where a file's bytes went.
func failingTransportManager(opts TransferOptions) (*TransferManager, *commandRecorder, *observer.ObservedLogs) {
	core, logs := observer.New(zapcore.DebugLevel)
	rec := &commandRecorder{}

	tm := &TransferManager{
		client:  newTestClient(""),
		options: opts,
		log:     zap.New(core).Sugar(),
		execute: rec.execute,
		viaSFTP: func(context.Context, string, string, os.FileInfo) error { return errTransportDown },
		viaSCP:  func(context.Context, string, string, os.FileInfo) error { return errTransportDown },
	}

	return tm, rec, logs
}

func writeSecretFile(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "join.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"joinKey":"`+transferSecret+`"}`), 0o600))

	return path
}

// The fallback after SFTP and SCP writes the file through a command line,
// which is what a secret must never reach. Without Secret the fallback is
// still taken, so this pins that the recorder sees what it must refuse.
func TestUpload_WithoutSecretFallsBackToACommandLine(t *testing.T) {
	t.Parallel()

	local := writeSecretFile(t)
	tm, rec, _ := failingTransportManager(TransferOptions{})

	require.NoError(t, tm.upload(context.Background(), local, "/data/w/.join.json.upload"))

	body, err := os.ReadFile(local)
	require.NoError(t, err)

	encoded := base64.StdEncoding.EncodeToString(body)
	assert.True(t, containsAny(rec.recorded(), encoded), "the base64 fallback carries the file in a command")
}

// A file that holds a secret may travel only over SFTP or SCP. When both
// fail, the upload refuses with an error that says why, and no command,
// log line, or error carries the secret in any form.
func TestUpload_ASecretNeverReachesACommand(t *testing.T) {
	t.Parallel()

	local := writeSecretFile(t)
	tm, rec, logs := failingTransportManager(TransferOptions{Secret: true, Verify: true})

	err := tm.upload(context.Background(), local, "/data/w/.join.json.upload")
	require.ErrorIs(t, err, ErrSecretTransferRefused)
	require.ErrorIs(t, err, errTransportDown, "the refusal keeps the transport's own error")
	assert.Contains(t, err.Error(), "SFTP")
	assert.Contains(t, err.Error(), "SCP")

	body, readErr := os.ReadFile(local)
	require.NoError(t, readErr)

	forms := []string{
		transferSecret,
		transferSecret[:12],
		base64.StdEncoding.EncodeToString(body),
		base64.StdEncoding.EncodeToString([]byte(transferSecret)),
	}

	for _, cmd := range rec.recorded() {
		for _, form := range forms {
			assert.NotContains(t, cmd, form, "a secret never reaches a command")
		}
	}

	assert.Empty(t, rec.recorded(), "no fallback runs any command for a secret file")

	for _, entry := range logs.All() {
		line := entry.Message
		for k, v := range entry.ContextMap() {
			line += " " + k + "=" + strings.TrimSpace(strings.ReplaceAll(fmt.Sprint(v), "\n", " "))
		}

		for _, form := range forms {
			assert.NotContains(t, line, form)
		}
	}

	for _, form := range forms {
		assert.NotContains(t, err.Error(), form)
	}
}

// A secret file still goes over SFTP when SFTP works.
func TestUpload_ASecretStillGoesOverSFTP(t *testing.T) {
	t.Parallel()

	local := writeSecretFile(t)
	tm, rec, _ := failingTransportManager(TransferOptions{Secret: true})

	sent := 0
	tm.viaSFTP = func(context.Context, string, string, os.FileInfo) error {
		sent++

		return nil
	}

	require.NoError(t, tm.upload(context.Background(), local, "/data/w/.join.json.upload"))
	assert.Equal(t, 1, sent)
	assert.Empty(t, rec.recorded())
}

// When SFTP fails, a secret file still goes over SCP.
func TestUpload_ASecretFallsBackToSCP(t *testing.T) {
	t.Parallel()

	local := writeSecretFile(t)
	tm, rec, _ := failingTransportManager(TransferOptions{Secret: true})

	sent := 0
	tm.viaSCP = func(context.Context, string, string, os.FileInfo) error {
		sent++

		return nil
	}

	require.NoError(t, tm.upload(context.Background(), local, "/data/w/.join.json.upload"))
	assert.Equal(t, 1, sent)
	assert.Empty(t, rec.recorded())
}

func containsAny(commands []string, s string) bool {
	for _, cmd := range commands {
		if strings.Contains(cmd, s) {
			return true
		}
	}

	return false
}
