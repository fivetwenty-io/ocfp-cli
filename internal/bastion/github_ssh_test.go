package bastion

import (
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
)

// TestGitHubSSHClientConfig pins the ~/.ssh/config stanza for both supported
// ports. On 443 the stanza must redirect github.com to ssh.github.com, and on
// 22 it must not mention either override.
func TestGitHubSSHClientConfig(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		port    int
		want    []string
		forbid  []string
		ordered []string
	}{
		{
			name:   "port 22 leaves the stanza direct",
			port:   config.GitHubSSHPortDefault,
			want:   []string{"Host github.com\n", "    ForwardAgent yes\n", "    StrictHostKeyChecking accept-new\n"},
			forbid: []string{"HostName", "Port 443", "ssh.github.com"},
		},
		{
			name:    "port 443 redirects to ssh.github.com",
			port:    config.GitHubSSHPortHTTPS,
			want:    []string{"Host github.com\n", "    HostName ssh.github.com\n", "    Port 443\n", "    ForwardAgent yes\n", "    StrictHostKeyChecking accept-new\n"},
			ordered: []string{"Host github.com", "HostName ssh.github.com", "Port 443"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := gitHubSSHClientConfig(tc.port)

			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q in:\n%s", w, got)
				}
			}

			for _, f := range tc.forbid {
				if strings.Contains(got, f) {
					t.Errorf("unexpected %q in:\n%s", f, got)
				}
			}

			last := -1

			for _, o := range tc.ordered {
				idx := strings.Index(got, o)
				if idx < last {
					t.Errorf("%q appears before the line that must precede it in:\n%s", o, got)
				}

				last = idx
			}

			if strings.Count(got, "Host github.com") != 1 {
				t.Errorf("stanza must open with exactly one Host github.com line:\n%s", got)
			}
		})
	}
}

// TestGitHubKeyscanCommand pins the scan target. ssh-keyscan keys its output
// as [ssh.github.com]:443 when given -p 443, which is how OpenSSH looks the
// key up under the redirected stanza, so the command must scan that name and
// port rather than github.com.
func TestGitHubKeyscanCommand(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		port   int
		want   string
		forbid string
	}{
		{
			name:   "port 22 scans github.com directly",
			port:   config.GitHubSSHPortDefault,
			want:   "ssh-keyscan -t rsa,ecdsa,ed25519 github.com >> ~/.ssh/known_hosts",
			forbid: "-p 443",
		},
		{
			name:   "port 443 scans ssh.github.com on 443",
			port:   config.GitHubSSHPortHTTPS,
			want:   "ssh-keyscan -t rsa,ecdsa,ed25519 -p 443 ssh.github.com >> ~/.ssh/known_hosts",
			forbid: " github.com ",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := gitHubKeyscanCommand(tc.port)

			if !strings.Contains(got, tc.want) {
				t.Errorf("missing %q in:\n%s", tc.want, got)
			}

			if strings.Contains(got, tc.forbid) {
				t.Errorf("unexpected %q in:\n%s", tc.forbid, got)
			}

			if !strings.HasPrefix(got, "mkdir -p ~/.ssh && ") || !strings.HasSuffix(got, "chmod 600 ~/.ssh/known_hosts") {
				t.Errorf("command lost its directory setup or permissions step:\n%s", got)
			}
		})
	}
}
