package pve

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// capturePVELog points the global logger at a temporary directory at debug
// level and returns a func that reads back everything written to it.
func capturePVELog(t *testing.T) func() string {
	t.Helper()

	dir := t.TempDir()

	err := logger.Initialize(logger.Config{Level: "debug", Debug: true, LogDir: dir, BlocName: "logcheck", Command: "test"})
	if err != nil {
		t.Fatalf("initializing the logger: %v", err)
	}

	t.Cleanup(func() { _ = logger.Initialize(logger.Config{NoLog: true}) })

	return func() string {
		_ = logger.Sync()

		var all strings.Builder

		walkErr := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}

			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}

			all.Write(data)

			return nil
		})
		if walkErr != nil {
			t.Fatalf("reading the log: %v", walkErr)
		}

		return all.String()
	}
}

// TestReadPaths_NeverLogProviderErrorText drives the reads bloc show --live
// makes through the real compute manager, with every failure the reads log
// carrying a fake secret, and checks that only a safe description reaches the
// log while the callers still get the full error.
func TestReadPaths_NeverLogProviderErrorText(t *testing.T) {
	readLog := capturePVELog(t)

	fake := &readFakePVE{
		fakePVEClient: fakePVEClient{getResponses: map[string]interface{}{
			"/nodes": twoNodes(),
			"/nodes/pve2/qemu": []interface{}{
				map[string]interface{}{"vmid": float64(102), "name": "lab-bastion", "status": "running", "tags": "bloc--lab;managed-by--ocfp"},
			},
			"/nodes/pve2/qemu/102/status/current": map[string]interface{}{"status": "running"},
			"/nodes/pve2/qemu/102/config":         map[string]interface{}{"name": "lab-bastion"},
		}},
		errs: map[string]error{
			"/nodes/pve1/qemu":                                  pveAPIError(500, "SECRET123 listing refused"),
			"/nodes/pve1/qemu/102/status/current":               configMissing("pve1", 102),
			"/nodes/pve2/qemu/102/agent/network-get-interfaces": errors.New("dial tcp: token SECRET123 rejected"),
		},
	}
	manager := newReadTestManager(fake, "")

	listed, err := manager.ListInstances(context.Background(), map[string]string{"label.bloc": "lab"})
	if err != nil {
		t.Fatalf("ListInstances: %v", err)
	}

	if len(listed) != 1 || listed[0].ID != "102" {
		t.Fatalf("ListInstances = %v, want VM 102 from the node that answered", listed)
	}

	if _, err := manager.GetInstance(context.Background(), "102"); err != nil {
		t.Fatalf("GetInstance: %v", err)
	}

	logged := readLog()

	if strings.Contains(logged, "SECRET123") {
		t.Fatalf("the log carries provider error text:\n%s", logged)
	}

	for _, want := range []string{"listing VMs on node pve1 failed (HTTP 500)", "guest agent read for VM 102 on node pve2 failed (unclassified error)"} {
		if !strings.Contains(logged, want) {
			t.Fatalf("the log lacks %q:\n%s", want, logged)
		}
	}
}

// TestReadPaths_ReturnTheFullErrorToCallers checks that a read that fails
// still hands its caller the provider's error unchanged, since callers
// classify it.
func TestReadPaths_ReturnTheFullErrorToCallers(t *testing.T) {
	t.Parallel()

	secret := pveAPIError(500, "SECRET123 status refused")
	fake := &readFakePVE{errs: map[string]error{"/nodes/pve1/qemu/100/status/current": secret}}

	_, err := newReadTestManager(fake, "pve1").GetInstance(context.Background(), "100")
	if err == nil || !strings.Contains(err.Error(), "SECRET123") {
		t.Fatalf("GetInstance error = %v, want the provider's error unchanged", err)
	}
}
