package commands

import (
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/bootstrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// proxyCommandOf returns the ProxyCommand value in an ssh argument vector.
func proxyCommandOf(t *testing.T, args []string) string {
	t.Helper()

	for i, arg := range args {
		if i > 0 && args[i-1] == "-o" && strings.HasPrefix(arg, "ProxyCommand=") {
			return strings.TrimPrefix(arg, "ProxyCommand=")
		}
	}

	require.Fail(t, "no ProxyCommand", "args: %v", args)

	return ""
}

// batchModeOptions returns the BatchMode settings an argument vector hands
// the outer ssh, in order, leaving out the ProxyCommand string.
func batchModeOptions(args []string) []string {
	var found []string

	for i, arg := range args {
		if i > 0 && args[i-1] == "-o" && strings.HasPrefix(strings.ToLower(strings.TrimSpace(arg)), "batchmode") {
			found = append(found, arg)
		}
	}

	return found
}

// In fallback mode, the jump hop to the artifacts VM may prompt only when
// the run is interactive: the outer options must not ask for batch mode and
// stdin must be a terminal. Each case checks the outer command and the
// proxy hop together.
func TestBuildSSHCommandForRouteProxyBatchMode(t *testing.T) { //nolint:paralleltest // t.Setenv
	t.Setenv("SSH_AUTH_SOCK", "")

	route := sshRoute{Host: "10.64.64.11", Bastion: "100.109.226.53"}
	quiet := bootstrap.BastionProxyCommand("", "ubuntu", route.Bastion, true)
	prompting := bootstrap.BastionProxyCommand("", "ubuntu", route.Bastion, false)

	cases := []struct {
		name         string
		extraOptions string
		sshArgs      []string
		terminal     bool
		wantOuter    []string
		wantProxy    string
	}{
		{"interactive", "", nil, true, nil, prompting},
		{"stdin is not a terminal", "", nil, false, nil, quiet},
		{"--ssh-options sets BatchMode", "-o BatchMode=yes", nil, true, []string{"BatchMode=yes"}, quiet},
		{"--ssh-options sets BatchMode in any case", "-o batchmode=YES", nil, true, []string{"batchmode=YES"}, quiet},
		{"ssh args set BatchMode", "", []string{"-o", "BatchMode=yes"}, true, []string{"BatchMode=yes"}, quiet},
		{"BatchMode=no on a terminal", "-o BatchMode=no", nil, true, []string{"BatchMode=no"}, prompting},
		{"BatchMode=no without a terminal", "-o BatchMode=no", nil, false, []string{"BatchMode=no"}, quiet},
		{"first BatchMode wins", "-o BatchMode=no -o BatchMode=yes", nil, true, []string{"BatchMode=no", "BatchMode=yes"}, prompting},
		{"tab-separated BatchMode", "", []string{"-o", "BatchMode\tyes"}, true, []string{"BatchMode\tyes"}, quiet},
		{"mixed whitespace BatchMode", "", []string{"-o", " \tbatchmode \t yes"}, true, []string{" \tbatchmode \t yes"}, quiet},
		{"spaced equals BatchMode", "", []string{"-o", "BatchMode \t=\t yes"}, true, []string{"BatchMode \t=\t yes"}, quiet},
		{"tab-separated first value wins", "", []string{"-o", "BatchMode\tno", "-o", "BatchMode=yes"}, true, []string{"BatchMode\tno", "BatchMode=yes"}, prompting},
		{"tab-separated BatchMode no", "", []string{"-o", "BatchMode\t \tno"}, true, []string{"BatchMode\t \tno"}, prompting},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildSSHCommandForRouteWith(route, "ubuntu", "", tc.extraOptions, tc.sshArgs, nil, tc.terminal)

			assert.Equal(t, tc.wantProxy, proxyCommandOf(t, got))
			assert.Equal(t, tc.wantOuter, batchModeOptions(got),
				"the outer ssh gets the caller's BatchMode options and no others")
			assert.Equal(t, "ubuntu@10.64.64.11", got[len(got)-1])
		})
	}
}

// With a bloc key, the jump hop is in batch mode whether or not stdin is a
// terminal, as it always was.
func TestBuildSSHCommandForRouteKeyedProxyIsAlwaysBatch(t *testing.T) { //nolint:paralleltest // t.Setenv
	t.Setenv("SSH_AUTH_SOCK", "")

	route := sshRoute{Host: "10.64.64.11", Bastion: "100.109.226.53"}
	key := "/home/op/.local/share/ocfp/ocfp-cf1-lab/ssh/id_ed25519"

	for _, terminal := range []bool{true, false} {
		got := buildSSHCommandForRouteWith(route, "ubuntu", key, "", nil, nil, terminal)
		assert.Contains(t, proxyCommandOf(t, got), "-o BatchMode=yes")
	}
}
