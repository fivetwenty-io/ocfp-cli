package bastion

import (
	"context"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/bastion/provision"
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
