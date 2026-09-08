package config_test

import (
	"testing"

	"github.com/goccy/go-yaml"

	"github.com/ocfp/ocfp-cli-go/internal/config"
)

// TestBastionGitHubSSHPortAcceptsBothKeyStyles guards githubSshPort against
// the snake_case binding gap that once swallowed ssh_user.
func TestBastionGitHubSSHPortAcceptsBothKeyStyles(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		doc  string
	}{
		{"camelCase", "githubSshPort: 443\n"},
		{"snake_case", "github_ssh_port: 443\n"},
	}

	for _, tc := range cases {
		var b config.Bastion
		if err := yaml.Unmarshal([]byte(tc.doc), &b); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}

		if b.GitHubSSHPort != 443 {
			t.Errorf("%s: GitHubSSHPort = %d, want 443", tc.name, b.GitHubSSHPort)
		}
	}
}

// TestBastionValidateGitHubSSHPort pins the accepted values and the rejection
// message for anything else.
func TestBastionValidateGitHubSSHPort(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		port    int
		wantErr string
	}{
		{"unset", 0, ""},
		{"default 22", 22, ""},
		{"https 443", 443, ""},
		{"rejects 2222", 2222, "bastion config: githubSshPort must be 22 or 443, got 2222"},
		{"rejects negative", -1, "bastion config: githubSshPort must be 22 or 443, got -1"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			b := config.Bastion{GitHubSSHPort: tc.port}
			err := b.Validate()

			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}

				return
			}

			if err == nil || err.Error() != tc.wantErr {
				t.Fatalf("Validate() = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

// TestBastionEffectiveGitHubSSHPort pins that the zero value behaves as 22.
func TestBastionEffectiveGitHubSSHPort(t *testing.T) {
	t.Parallel()

	cases := map[int]int{0: 22, 22: 22, 443: 443}

	for set, want := range cases {
		b := config.Bastion{GitHubSSHPort: set}
		if got := b.EffectiveGitHubSSHPort(); got != want {
			t.Errorf("GitHubSSHPort %d: EffectiveGitHubSSHPort() = %d, want %d", set, got, want)
		}
	}
}
