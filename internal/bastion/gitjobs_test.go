package bastion

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/bastion/provision"
	"github.com/ocfp/ocfp-cli-go/internal/bastion/ssh"
	"github.com/ocfp/ocfp-cli-go/internal/config"
)

// collectGitJobs runs createGitJobs and returns the commands it enqueued.
func collectGitJobs(t *testing.T, repos []provision.GitRepository) []string {
	t.Helper()

	m := NewManager(context.Background(), &config.Config{Name: "ocfp-lab-drgao"}, &ProvisioningOptions{})
	jobs := make(chan job, len(repos))
	m.createGitJobs(repos, jobs)

	var cmds []string
	for j := range jobs {
		cmds = append(cmds, j.cmd)
	}

	return cmds
}

func TestCreateGitJobs_ReconcilesOriginOnExistingCheckout(t *testing.T) {
	t.Parallel()

	repos := []provision.GitRepository{{
		Name:   "genesis",
		URL:    "https://github.com/RubidiumStudios/genesis",
		Dest:   "/home/ubuntu/ocfp/genesis",
		Branch: "v3.2.x-dev",
		Depth:  1,
	}}

	cmds := collectGitJobs(t, repos)
	if len(cmds) != 1 {
		t.Fatalf("expected 1 job, got %d", len(cmds))
	}

	cmd := cmds[0]
	if !strings.Contains(cmd, "git remote set-url origin 'https://github.com/RubidiumStudios/genesis'") {
		t.Errorf("existing checkout is not repointed at the configured URL, so a moved upstream never reaches a provisioned bastion:\n%s", cmd)
	}

	if !strings.Contains(cmd, "git remote add origin") {
		t.Errorf("no fallback for a checkout with no origin remote:\n%s", cmd)
	}

	// The reconcile must happen before the fetch, or the fetch still reads the old remote.
	if strings.Index(cmd, "set-url origin") > strings.Index(cmd, "git fetch") {
		t.Errorf("origin is reconciled after the fetch, which fetches the old URL:\n%s", cmd)
	}
}

// TestCreateGitJobs_ResetsBranchOntoRemote pins that an existing checkout
// fetches the configured branch by explicit refspec and resets onto the
// remote tip. A single-branch clone never fetches a branch it was not
// cloned with, and a fast-forward pull fails once origin points at a fork
// whose history diverged from the old one.
func TestCreateGitJobs_ResetsBranchOntoRemote(t *testing.T) {
	t.Parallel()

	repos := []provision.GitRepository{{
		Name:   "genesis",
		URL:    "git@github.com:RubidiumStudios/genesis",
		Dest:   "/home/ubuntu/ocfp/genesis",
		Branch: "v3.2.x-dev",
	}}

	cmd := collectGitJobs(t, repos)[0]

	for _, want := range []string{
		"git fetch --prune origin '+refs/heads/v3.2.x-dev:refs/remotes/origin/v3.2.x-dev'",
		"git checkout -q -B 'v3.2.x-dev' 'origin/v3.2.x-dev'",
		"git clone 'git@github.com:RubidiumStudios/genesis' -b 'v3.2.x-dev'",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("missing %q in:\n%s", want, cmd)
		}
	}

	if strings.Contains(cmd, "pull --ff-only") {
		t.Errorf("existing checkout still relies on a fast-forward pull:\n%s", cmd)
	}
}

func TestCreateGitJobs_BranchlessRepoAlsoReconcilesOrigin(t *testing.T) {
	t.Parallel()

	repos := []provision.GitRepository{{
		Name: "kit",
		URL:  "https://github.com/genesis-community/bosh-genesis-kit",
		Dest: "/home/ubuntu/kits/bosh",
	}}

	cmd := collectGitJobs(t, repos)[0]
	if !strings.Contains(cmd, "git remote set-url origin") {
		t.Errorf("branchless repo does not reconcile origin:\n%s", cmd)
	}
}

// TestCreateGitJobs_CarriesRepositoryURL pins that each job records the URL it
// clones, which buildGitJobError needs to decide whether the SSH hint applies.
func TestCreateGitJobs_CarriesRepositoryURL(t *testing.T) {
	t.Parallel()

	m := NewManager(context.Background(), &config.Config{Name: "ocfp-lab-drgao"}, &ProvisioningOptions{})
	jobs := make(chan job, 1)
	m.createGitJobs([]provision.GitRepository{{
		Name: "deployments",
		URL:  "git@github.com:example/ocfp-deployments.git",
		Dest: "/home/ubuntu/ocfp/deployments",
	}}, jobs)

	j := <-jobs
	if j.url != "git@github.com:example/ocfp-deployments.git" {
		t.Errorf("job.url = %q, want the configured repository URL", j.url)
	}
}

// TestBuildGitJobError_SSHHint pins that a failed clone over SSH names the two
// usual causes, while an HTTPS clone keeps the plain git error.
func TestBuildGitJobError_SSHHint(t *testing.T) {
	t.Parallel()

	m := NewManager(context.Background(), &config.Config{Name: "ocfp-lab-drgao"}, &ProvisioningOptions{})
	cause := errors.New("exit status 128")

	cases := []struct {
		name     string
		url      string
		result   *ssh.CommandResult
		wantHint bool
	}{
		{"scp style ssh url", "git@github.com:example/repo.git", &ssh.CommandResult{Stderr: "Connection timed out"}, true},
		{"ssh scheme url", "ssh://git@github.com/example/repo.git", nil, true},
		{"https url", "https://github.com/example/repo.git", &ssh.CommandResult{Stderr: "not found"}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := m.buildGitJobError(&job{name: "repo", url: tc.url}, tc.result, cause)
			if err == nil {
				t.Fatal("expected an error")
			}

			if !errors.Is(err, cause) {
				t.Errorf("hint wrapping lost the underlying error: %v", err)
			}

			if !strings.Contains(err.Error(), "git op failed for repo") {
				t.Errorf("missing git failure prefix: %v", err)
			}

			if tc.result != nil && !strings.Contains(err.Error(), tc.result.Stderr) {
				t.Errorf("stderr dropped from the error: %v", err)
			}

			gotHint := strings.Contains(err.Error(), "bastion.githubSshPort to 443") &&
				strings.Contains(err.Error(), "ssh-add")
			if gotHint != tc.wantHint {
				t.Errorf("hint present = %v, want %v: %v", gotHint, tc.wantHint, err)
			}
		})
	}
}

// TestBuildGitJobError_NilOnSuccess pins that a successful job yields no error.
func TestBuildGitJobError_NilOnSuccess(t *testing.T) {
	t.Parallel()

	m := NewManager(context.Background(), &config.Config{Name: "ocfp-lab-drgao"}, &ProvisioningOptions{})
	if err := m.buildGitJobError(&job{name: "repo", url: "git@github.com:x/y.git"}, nil, nil); err != nil {
		t.Errorf("buildGitJobError() = %v, want nil", err)
	}
}
