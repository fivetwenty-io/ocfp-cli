package bootstrap_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fivetwenty-io/ocfp-cli/internal/bastion/ssh"
)

// On pve the artifacts VM is provisioned over ssh through the bastion. When
// the bastion's host key has changed, no amount of waiting will provision
// it, so bootstrap stops at once with the mismatch and how to clear it,
// rather than waiting out the readiness probe and carrying on.
func TestCreateArtifactsStopsOnABastionHostKeyRefusal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	bin := t.TempDir()
	refuse := "#!/bin/sh\n" +
		"echo '@    WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!     @' >&2\n" +
		"echo 'Host key verification failed.' >&2\n" +
		"exit 255\n"

	err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(refuse), 0o700) //nolint:gosec // test executable
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", bin)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	manager, _, _ := newArtifactsOrderManagerOn(t, "pve", "100.109.226.53", &dataVolStorage{})

	err = manager.CreateArtifacts(ctx)
	if !errors.Is(err, ssh.ErrHostKeyMismatch) {
		t.Fatalf("CreateArtifacts() = %v, want an error matching ErrHostKeyMismatch", err)
	}

	hint := "ssh-keygen -R 100.109.226.53 -f " + filepath.Join(home, ".ssh", "known_hosts")
	if !strings.Contains(err.Error(), hint) {
		t.Errorf("error %q does not say how to clear the stale entry (%s)", err, hint)
	}
}
