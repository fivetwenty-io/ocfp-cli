package bastion

import (
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
)

// TestGitIdentityWarning pins that init warns, naming both config keys, when
// the bloc config leaves the bastion without a git identity, and stays quiet
// when both keys are set.
func TestGitIdentityWarning(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		user     config.GitUser
		wantWarn bool
	}{
		{"both set", config.GitUser{Name: "Sinéad O'Connor", Email: "sinead@example.com"}, false},
		{"neither set", config.GitUser{}, true},
		{"name only", config.GitUser{Name: "Only Name"}, true},
		{"email only", config.GitUser{Email: "only@example.com"}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := &config.Config{Name: "ocfp-cf1-lab"}
			cfg.Bastion.Git.User = tc.user

			got := gitIdentityWarning(cfg)
			if (got != "") != tc.wantWarn {
				t.Fatalf("warning = %q, want warning %v", got, tc.wantWarn)
			}

			if tc.wantWarn {
				for _, key := range []string{"bastion.git.user.name", "bastion.git.user.email"} {
					if !strings.Contains(got, key) {
						t.Errorf("warning does not name %s: %q", key, got)
					}
				}
			}
		})
	}
}
