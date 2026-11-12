package commands

import (
	"errors"
	"path/filepath"
	"testing"

	bastionssh "github.com/fivetwenty-io/ocfp-cli/internal/bastion/ssh"
	"github.com/fivetwenty-io/ocfp-cli/internal/bootstrap"
)

// The helpers below call a command builder and fail the test when it
// returns an error, for tests that only look at the command.

func mustBastionSSHCommand(t *testing.T, host, user, keyPath, extraOptions string, sshArgs, command []string) []string {
	t.Helper()

	got, err := buildBastionSSHCommand(host, user, keyPath, extraOptions, sshArgs, command)
	if err != nil {
		t.Fatalf("buildBastionSSHCommand: %v", err)
	}

	return got
}

func mustRouteSSHCommand(t *testing.T, route sshRoute, user, keyPath, extraOptions string, sshArgs, command []string) []string {
	t.Helper()

	got, err := buildSSHCommandForRoute(route, user, keyPath, extraOptions, sshArgs, command)
	if err != nil {
		t.Fatalf("buildSSHCommandForRoute: %v", err)
	}

	return got
}

func mustRouteSSHCommandWith(t *testing.T, route sshRoute, user, keyPath, extraOptions string, sshArgs, command []string, stdinTerminal bool) []string {
	t.Helper()

	got, err := buildSSHCommandForRouteWith(route, user, keyPath, extraOptions, sshArgs, command, stdinTerminal)
	if err != nil {
		t.Fatalf("buildSSHCommandForRouteWith: %v", err)
	}

	return got
}

func mustProxyCommand(t *testing.T, keyPath, user, bastionHost string, batchMode bool) string {
	t.Helper()

	got, err := bootstrap.BastionProxyCommand(keyPath, user, bastionHost, batchMode)
	if err != nil {
		t.Fatalf("BastionProxyCommand: %v", err)
	}

	return got
}

func mustSCPCommand(t *testing.T, cfg *scpConfig, bastionIP, keyPath string) []string {
	t.Helper()

	got, err := prepareSCPCommand(cfg, bastionIP, keyPath)
	if err != nil {
		t.Fatalf("prepareSCPCommand: %v", err)
	}

	return got
}

func mustRSyncCommand(t *testing.T, config *rsyncConfig, source, destination, keyPath string) []string {
	t.Helper()

	got, err := buildRSyncCommand(config, source, destination, keyPath)
	if err != nil {
		t.Fatalf("buildRSyncCommand: %v", err)
	}

	return got
}

// System ssh would expand "${" in the known_hosts path and check a
// different file, so every command that checks the bastion's host key
// refuses to run instead.
func TestBastionCommandsRefuseAKnownHostsPathSSHWouldExpand(t *testing.T) {
	t.Setenv("HOME", filepath.Join(t.TempDir(), "a${HOME}b"))

	route := sshRoute{Host: "10.64.64.11", Bastion: "100.109.226.53"}
	scp := &scpConfig{Source: "f", Destination: "bastion:/tmp/f"} //nolint:exhaustruct // only the paths matter
	rsync := &rsyncConfig{}                                       //nolint:exhaustruct // defaults suffice

	builds := map[string]func() error{
		"ssh to the bastion": func() error {
			_, err := buildBastionSSHCommand("100.109.226.53", "ubuntu", "", "", nil, nil)

			return err
		},
		"ssh through the bastion": func() error {
			_, err := buildSSHCommandForRouteWith(route, "ubuntu", "", "", nil, nil, false)

			return err
		},
		"scp": func() error {
			_, err := prepareSCPCommand(scp, "100.109.226.53", "")

			return err
		},
		"rsync": func() error {
			_, err := buildRSyncCommand(rsync, "a", "ubuntu@100.109.226.53:b", "")

			return err
		},
	}

	for name, build := range builds {
		err := build()
		if !errors.Is(err, bastionssh.ErrKnownHostsPathUnusable) {
			t.Errorf("%s: got %v, want the unusable known_hosts path error", name, err)
		}
	}
}
