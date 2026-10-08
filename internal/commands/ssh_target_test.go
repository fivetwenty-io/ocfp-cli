package commands

import (
	"context"
	"errors"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/bootstrap"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withViperBloc sets the global bloc for one test and restores the previous
// value afterwards.
func withViperBloc(t *testing.T, bloc string) {
	t.Helper()

	prev := viper.Get("bloc")

	viper.Set("bloc", bloc)
	t.Cleanup(func() { viper.Set("bloc", prev) })
}

func TestClassifySSHTarget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		target   string
		expected sshTargetKind
	}{
		{name: "Bastion", target: "bastion", expected: sshTargetBastion},
		{name: "Artifacts", target: "artifacts", expected: sshTargetArtifacts},
		{name: "IPv4Address", target: "10.64.64.11", expected: sshTargetAddress},
		{name: "IPv6Address", target: "fd00::11", expected: sshTargetAddress},
		{name: "DottedHostname", target: "bastion.lab.example.org", expected: sshTargetAddress},
		{name: "DottedHostnameStartingWithArtifacts", target: "artifacts.lab.example.org", expected: sshTargetAddress},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			kind, err := classifySSHTarget(tt.target)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, kind)
		})
	}
}

// A bare word that is neither a named target nor an address must fail
// before anything is handed to ssh, where DNS would reject it with an
// unhelpful "Could not resolve hostname" error.
func TestClassifySSHTargetRejectsUnknownBareWord(t *testing.T) {
	t.Parallel()

	for _, target := range []string{"artifact", "artifactz", "hostname", "localhost", "Bastion"} {
		t.Run(target, func(t *testing.T) {
			t.Parallel()

			_, err := classifySSHTarget(target)
			require.Error(t, err)
			require.ErrorIs(t, err, ErrUnknownSSHTarget)
			assert.Contains(t, err.Error(), `"`+target+`"`)
			assert.Contains(t, err.Error(), "bastion, artifacts")
		})
	}
}

func TestGetSSHConfigRejectsUnknownTarget(t *testing.T) { //nolint:paralleltest // mutates global viper state
	withViperBloc(t, "ocfp-cf1-lab")

	_, err := getSSHConfig([]string{"artifactz", "chronyc", "tracking"})
	require.ErrorIs(t, err, ErrUnknownSSHTarget)
}

func TestGetSSHConfigClassifiesArtifactsTarget(t *testing.T) { //nolint:paralleltest // mutates global viper state
	withViperBloc(t, "ocfp-cf1-lab")

	cfg, err := getSSHConfig([]string{"artifacts", "chronyc tracking"})
	require.NoError(t, err)
	assert.Equal(t, "artifacts", cfg.Target)
	assert.Equal(t, sshTargetArtifacts, cfg.Kind)
	assert.Equal(t, []string{"chronyc tracking"}, cfg.Command)
}

func TestResolveSSHRoute(t *testing.T) {
	t.Parallel()

	resolver := sshRouteResolver{
		bastionIP:   func(context.Context) (string, error) { return "100.109.226.53", nil },
		artifactsIP: func(context.Context) (string, error) { return "10.64.64.11", nil },
	}

	tests := []struct {
		name        string
		kind        sshTargetKind
		target      string
		noProxyJump bool
		expected    sshRoute
	}{
		{
			name:     "BastionIsDirect",
			kind:     sshTargetBastion,
			target:   "bastion",
			expected: sshRoute{Host: "100.109.226.53"},
		},
		{
			name:     "ArtifactsHopsThroughBastion",
			kind:     sshTargetArtifacts,
			target:   "artifacts",
			expected: sshRoute{Host: "10.64.64.11", Bastion: "100.109.226.53"},
		},
		{
			name:        "ArtifactsNoProxyJumpIsDirect",
			kind:        sshTargetArtifacts,
			target:      "artifacts",
			noProxyJump: true,
			expected:    sshRoute{Host: "10.64.64.11"},
		},
		{
			name:     "IPAddressPassesThrough",
			kind:     sshTargetAddress,
			target:   "10.64.64.20",
			expected: sshRoute{Host: "10.64.64.20"},
		},
		{
			name:     "DottedHostnamePassesThrough",
			kind:     sshTargetAddress,
			target:   "jump.lab.example.org",
			expected: sshRoute{Host: "jump.lab.example.org"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			route, err := resolveSSHRoute(context.Background(), tt.kind, tt.target, tt.noProxyJump, resolver)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, route)
		})
	}
}

// An address target never consults the bloc: no bastion discovery and no
// artifacts lookup run, so it behaves exactly as it did before named targets.
func TestResolveSSHRouteAddressSkipsLookups(t *testing.T) {
	t.Parallel()

	fail := func(context.Context) (string, error) {
		t.Fatal("address targets must not trigger a lookup")

		return "", nil
	}

	route, err := resolveSSHRoute(context.Background(), sshTargetAddress, "10.64.64.20", false,
		sshRouteResolver{bastionIP: fail, artifactsIP: fail})
	require.NoError(t, err)
	assert.Equal(t, sshRoute{Host: "10.64.64.20"}, route)
}

func TestResolveSSHRouteArtifactsErrors(t *testing.T) {
	t.Parallel()

	errLookup := errors.New("lookup failed")

	_, err := resolveSSHRoute(context.Background(), sshTargetArtifacts, "artifacts", false, sshRouteResolver{
		bastionIP:   func(context.Context) (string, error) { return "100.109.226.53", nil },
		artifactsIP: func(context.Context) (string, error) { return "", errLookup },
	})
	require.ErrorIs(t, err, errLookup)

	_, err = resolveSSHRoute(context.Background(), sshTargetArtifacts, "artifacts", false, sshRouteResolver{
		bastionIP:   func(context.Context) (string, error) { return "", errLookup },
		artifactsIP: func(context.Context) (string, error) { return "10.64.64.11", nil },
	})
	require.ErrorIs(t, err, errLookup)
	assert.Contains(t, err.Error(), "bastion")
}

func TestBuildSSHCommandForRouteArtifactsViaBastion(t *testing.T) { //nolint:paralleltest // t.Setenv
	t.Setenv("SSH_AUTH_SOCK", "/tmp/agent.sock")

	key := "/home/op/.local/share/ocfp/ocfp-cf1-lab/ssh/id_ed25519"
	route := sshRoute{Host: "10.64.64.11", Bastion: "100.109.226.53"}

	got := buildSSHCommandForRoute(route, "ubuntu", key, "", []string{}, []string{"chronyc tracking"})

	expected := []string{
		"ssh",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "StrictHostKeyChecking=no",
		"-o", "LogLevel=ERROR",
		"-o", "IdentitiesOnly=yes",
		"-i", key,
		"-A",
		"-o", "ProxyCommand=" + bootstrap.BastionProxyCommand(key, "ubuntu", "100.109.226.53"),
		"-o", "ServerAliveInterval=30",
		"-o", "ServerAliveCountMax=6",
		"ubuntu@10.64.64.11",
		"bash", "-lc", "'chronyc tracking'",
	}
	assert.Equal(t, expected, got)

	joined := ""
	for _, a := range got {
		joined += a + " "
	}

	assert.Contains(t, joined, "-W %h:%p ubuntu@100.109.226.53")
	assert.NotContains(t, joined, "ProxyJump")
}

func TestBuildSSHCommandForRouteArtifactsKeepsFlagsAndOptions(t *testing.T) { //nolint:paralleltest // t.Setenv
	t.Setenv("SSH_AUTH_SOCK", "")

	key := "/home/op/.local/share/ocfp/ocfp-cf1-lab/ssh/id_ed25519"
	route := sshRoute{Host: "10.64.64.11", Bastion: "100.109.226.53"}

	got := buildSSHCommandForRoute(route, "admin", key, "-o ServerAliveInterval=30",
		[]string{"-t", "-L", "9001:localhost:9001", "-D", "1080", "-X"}, []string{})

	expected := []string{
		"ssh",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "StrictHostKeyChecking=no",
		"-o", "LogLevel=ERROR",
		"-o", "IdentitiesOnly=yes",
		"-i", key,
		"-o", "ProxyCommand=" + bootstrap.BastionProxyCommand(key, "admin", "100.109.226.53"),
		"-o", "ServerAliveInterval=30",
		"-t", "-L", "9001:localhost:9001", "-D", "1080",
		"-o", "ServerAliveInterval=30",
		"-o", "ServerAliveCountMax=6",
		"admin@10.64.64.11",
	}
	assert.Equal(t, expected, got)
}

// Every session sends keepalives, so a quiet shell is not dropped by an
// idle timeout between the operator and the bastion.
func TestBuildSSHCommandSendsKeepalives(t *testing.T) { //nolint:paralleltest // t.Setenv
	t.Setenv("SSH_AUTH_SOCK", "")

	got := buildSSHCommandForRoute(sshRoute{Host: "100.109.226.53"}, "ubuntu", "", "", []string{}, []string{})

	assert.Equal(t, []string{
		"-o", "ServerAliveInterval=30",
		"-o", "ServerAliveCountMax=6",
		"ubuntu@100.109.226.53",
	}, got[len(got)-5:])
}

// ssh keeps the first value it sees for an option, so the keepalives go
// after the operator's own options and an operator's value wins.
func TestBuildSSHCommandLetsOperatorKeepaliveWin(t *testing.T) { //nolint:paralleltest // t.Setenv
	t.Setenv("SSH_AUTH_SOCK", "")

	got := buildSSHCommandForRoute(sshRoute{Host: "100.109.226.53"}, "ubuntu", "",
		"-o ServerAliveInterval=120", []string{}, []string{})

	operator := indexOf(got, "ServerAliveInterval=120")
	ours := indexOf(got, "ServerAliveInterval=30")

	assert.GreaterOrEqual(t, operator, 0)
	assert.Greater(t, ours, operator)
}

// A bastion address that fails host validation must never reach the
// ProxyCommand, which ssh runs through a shell.
func TestBuildSSHCommandForRouteRejectsInvalidBastion(t *testing.T) {
	t.Parallel()

	route := sshRoute{Host: "10.64.64.11", Bastion: "1.2.3.4;touch /tmp/x"}

	got := buildSSHCommandForRoute(route, "ubuntu", "/tmp/key", "", []string{}, []string{})
	assert.Equal(t, []string{"ssh", "--help"}, got)
}

// Direct routes (bastion, IPs, dotted hostnames, and artifacts with
// --no-proxy-jump) produce exactly the vector buildSSHCommand always has.
func TestBuildSSHCommandForRouteDirectMatchesBuildSSHCommand(t *testing.T) { //nolint:paralleltest // t.Setenv
	t.Setenv("SSH_AUTH_SOCK", "/tmp/agent.sock")

	for _, host := range []string{"100.109.226.53", "10.64.64.11", "jump.lab.example.org"} {
		sshArgs := []string{"-L", "8080:localhost:80"}
		command := []string{"hostname"}

		got := buildSSHCommandForRoute(sshRoute{Host: host}, "ubuntu", "/tmp/key", "-v", sshArgs, command)
		assert.Equal(t, buildSSHCommand(host, "ubuntu", "/tmp/key", "-v", sshArgs, command), got)
		assert.NotContains(t, got, "ProxyCommand")
	}
}
