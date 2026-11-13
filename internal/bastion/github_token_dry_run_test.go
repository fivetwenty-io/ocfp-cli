package bastion

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeGh puts a gh on PATH that prints token and records that it ran in
// the returned file.
func fakeGh(t *testing.T, token string) string {
	t.Helper()

	bin := t.TempDir()
	ran := filepath.Join(t.TempDir(), "gh-ran")
	script := "#!/bin/sh\necho \"$@\" >> '" + ran + "'\necho " + token + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o700)) // #nosec G306 -- test executable
	t.Setenv("PATH", bin)

	// Run it once before the test does, so a slow first start of a new
	// script under load doesn't count against the lookup's own timeout.
	require.NoError(t, exec.Command(filepath.Join(bin, "gh")).Run()) // #nosec G204 -- the script this test just wrote
	require.NoError(t, os.Remove(ran))

	return ran
}

// A real run asks the gh CLI for a GitHub token when GITHUB_TOKEN isn't
// set. A dry run runs no command, so it doesn't ask, and it still takes
// GITHUB_TOKEN from the environment.
func TestDryRunDoesNotAskGhForAGitHubToken(t *testing.T) {
	cases := []struct {
		name       string
		dryRun     bool
		envToken   string
		wantToken  string
		wantGhRuns bool
	}{
		{"real run asks gh", false, "", "gh-token", true},
		{"dry run does not ask gh", true, "", "", false},
		{"dry run takes GITHUB_TOKEN", true, "env-token", "env-token", false},
		{"real run takes GITHUB_TOKEN", false, "env-token", "env-token", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := dryRunBloc(t)
			dryRunWorkstation(t, cfg)
			ran := fakeGh(t, "gh-token")
			t.Setenv("GITHUB_TOKEN", tc.envToken)

			m := NewManager(context.Background(), cfg, &ProvisioningOptions{DryRun: tc.dryRun})
			assert.Equal(t, tc.wantToken, m.githubToken)

			_, err := os.Stat(ran)
			assert.Equal(t, tc.wantGhRuns, err == nil, "whether gh ran")
		})
	}
}
