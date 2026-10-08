package provision

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// configureHarness runs the deployments-repository and kit-checkout blocks
// of the configure script in bash, under the same `set -euo pipefail` the
// bastion wraps them in, against temp directories and real git repositories
// that stand in for the upstreams.
type configureHarness struct {
	t        *testing.T
	root     string
	deploys  string
	kits     string
	upstream string
	env      []string
}

func newConfigureHarness(t *testing.T) *configureHarness {
	t.Helper()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}

	root := t.TempDir()

	return &configureHarness{
		t:        t,
		root:     root,
		deploys:  filepath.Join(root, "ocfp", "deployments"),
		kits:     filepath.Join(root, "ocfp", "kits"),
		upstream: filepath.Join(root, "upstream"),
		env: append(os.Environ(),
			"HOME="+root,
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@example.com",
			"GIT_TERMINAL_PROMPT=0",
		),
	}
}

// git runs git in dir and returns its trimmed output.
func (h *configureHarness) git(dir string, args ...string) string {
	h.t.Helper()

	cmd := exec.CommandContext(h.t.Context(), "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = h.env

	out, err := cmd.CombinedOutput()
	require.NoError(h.t, err, "git %v in %s: %s", args, dir, out)

	return strings.TrimSpace(string(out))
}

// commit writes file with content in repo and commits it.
func (h *configureHarness) commit(repo, file, content string) {
	h.t.Helper()

	require.NoError(h.t, os.WriteFile(filepath.Join(repo, file), []byte(content), 0o600))
	h.git(repo, "add", file)
	h.git(repo, "commit", "--quiet", "-m", "change "+file)
}

// newUpstream makes a repository named name with one commit on main and,
// for each extra branch, a branch off it.
func (h *configureHarness) newUpstream(name string, branches ...string) string {
	h.t.Helper()

	repo := filepath.Join(h.upstream, name)
	require.NoError(h.t, os.MkdirAll(repo, 0o755))
	h.git(repo, "init", "--quiet", "-b", "main")
	h.commit(repo, "README", "first\n")

	for _, branch := range branches {
		h.git(repo, "branch", branch)
	}

	return repo
}

// clone checks out upstream at dest on branch, as an earlier init would have.
func (h *configureHarness) clone(upstream, dest, branch string) {
	h.t.Helper()

	require.NoError(h.t, os.MkdirAll(filepath.Dir(dest), 0o755))
	h.git(h.root, "clone", "--quiet", "-b", branch, upstream, dest)
}

func (h *configureHarness) path(rel string) string {
	return filepath.Join(h.root, rel)
}

// run executes the two blocks for the given dev deployments, with kit repos
// and branches keyed by deployment, and returns bash's output and error.
func (h *configureHarness) run(deploymentsURL string, dev []string, kitRepos, kitBranches map[string]string) (string, error) {
	h.t.Helper()

	script := strings.Join([]string{
		"set -euo pipefail",
		`log_info() { echo "INFO: $1"; }`,
		`log_success() { echo "OK: $1"; }`,
		`log_warning() { echo "WARN: $1"; }`,
		`log_error() { echo "ERROR: $1"; }`,
		"GLOBAL_DEPLOYMENTS_URL=" + shellSingleQuote(deploymentsURL),
		"DEPLOYMENTS_ROOT=" + shellSingleQuote(h.deploys),
		"KITS_ROOT=" + shellSingleQuote(h.kits),
		"DEV_DEPLOYMENTS=" + formatShellArray(dev),
		`mkdir -p "${DEPLOYMENTS_ROOT}"`,
		`mkdir -p "${KITS_ROOT}"`,
		strings.Join(generateDeploymentsRepoBlock(), "\n"),
		strings.Join(generateKitCheckoutBlock(kitRepos, kitBranches), "\n"),
	}, "\n")

	cmd := exec.CommandContext(h.t.Context(), "bash", "-c", script)
	cmd.Env = h.env

	out, err := cmd.CombinedOutput()

	return string(out), err
}

// runKit runs the blocks for a single dev deployment named bosh.
func (h *configureHarness) runKit(upstream, branch string) string {
	h.t.Helper()

	out, err := h.run("", []string{"bosh"},
		map[string]string{"bosh": upstream}, map[string]string{"bosh": branch})
	require.NoError(h.t, err, out)

	return out
}

// --- the deployments repository ---------------------------------------------

// A deployments directory with files in it but no .git is an operator's
// work, and init used to rm -rf it and clone over it.
func TestDeploymentsRepo_NonEmptyWithoutGitIsLeftAlone(t *testing.T) {
	t.Parallel()

	h := newConfigureHarness(t)
	upstream := h.newUpstream("deployments")

	envFile := filepath.Join(h.deploys, "bosh", "ocfp-lab-mgmt.yml")
	require.NoError(t, os.MkdirAll(filepath.Dir(envFile), 0o755))
	require.NoError(t, os.WriteFile(envFile, []byte("hand placed\n"), 0o600))

	out, err := h.run(upstream, nil, nil, nil)
	require.NoError(t, err, out)

	data, readErr := os.ReadFile(envFile)
	require.NoError(t, readErr, "the hand-placed env file must survive:\n%s", out)
	assert.Equal(t, "hand placed\n", string(data))
	assert.NoDirExists(t, filepath.Join(h.deploys, ".git"), "init must not clone over hand-placed deployments")
	assert.NotContains(t, out, upstream, "the warning must not print the repository URL, which can carry a token")
	assert.Contains(t, out, "WARN: ")
}

// An empty deployments directory, which init itself creates, is cloned into.
func TestDeploymentsRepo_EmptyIsCloned(t *testing.T) {
	t.Parallel()

	h := newConfigureHarness(t)
	upstream := h.newUpstream("deployments")

	out, err := h.run(upstream, nil, nil, nil)
	require.NoError(t, err, out)

	assert.FileExists(t, filepath.Join(h.deploys, "README"), out)
	assert.DirExists(t, filepath.Join(h.deploys, ".git"))
}

// A deployments checkout is still fast-forwarded.
func TestDeploymentsRepo_CheckoutIsFastForwarded(t *testing.T) {
	t.Parallel()

	h := newConfigureHarness(t)
	upstream := h.newUpstream("deployments")
	h.clone(upstream, h.deploys, "main")
	h.commit(upstream, "NEW", "second\n")

	out, err := h.run(upstream, nil, nil, nil)
	require.NoError(t, err, out)

	assert.FileExists(t, filepath.Join(h.deploys, "NEW"), out)
}

// --- kit checkouts -----------------------------------------------------------

// A missing kit is cloned and its dev link is created.
func TestKitCheckout_MissingKitIsClonedAndLinked(t *testing.T) {
	t.Parallel()

	h := newConfigureHarness(t)
	upstream := h.newUpstream("bosh-genesis-kit", "develop")

	out := h.runKit(upstream, "develop")

	kitDir := filepath.Join(h.kits, "bosh")
	assert.Equal(t, "develop", h.git(kitDir, "rev-parse", "--abbrev-ref", "HEAD"), out)

	target, err := os.Readlink(filepath.Join(h.deploys, "bosh", "dev"))
	require.NoError(t, err, out)
	assert.Equal(t, kitDir, target)
}

// A clean kit checkout on the configured branch is fast-forwarded.
func TestKitCheckout_CleanCheckoutOnTheBranchIsFastForwarded(t *testing.T) {
	t.Parallel()

	h := newConfigureHarness(t)
	upstream := h.newUpstream("bosh-genesis-kit")
	kitDir := filepath.Join(h.kits, "bosh")
	h.clone(upstream, kitDir, "main")
	h.commit(upstream, "NEW", "second\n")

	out := h.runKit(upstream, "main")

	assert.FileExists(t, filepath.Join(kitDir, "NEW"), out)
}

// With no branch configured, a clean checkout of the upstream's default
// branch is fast-forwarded too.
func TestKitCheckout_DefaultBranchIsFastForwardedWhenNoneIsConfigured(t *testing.T) {
	t.Parallel()

	h := newConfigureHarness(t)
	upstream := h.newUpstream("bosh-genesis-kit")
	kitDir := filepath.Join(h.kits, "bosh")
	h.clone(upstream, kitDir, "main")
	h.commit(upstream, "NEW", "second\n")

	out := h.runKit(upstream, "")

	assert.FileExists(t, filepath.Join(kitDir, "NEW"), out)
}

// A kit checkout that an operator switched to another branch is theirs, and
// init used to switch it back and pull.
func TestKitCheckout_OtherBranchIsLeftAlone(t *testing.T) {
	t.Parallel()

	for _, configured := range []string{"main", ""} {
		t.Run("configured="+configured, func(t *testing.T) {
			t.Parallel()

			h := newConfigureHarness(t)
			upstream := h.newUpstream("bosh-genesis-kit", "feature")
			kitDir := filepath.Join(h.kits, "bosh")
			h.clone(upstream, kitDir, "feature")
			before := h.git(kitDir, "rev-parse", "HEAD")
			h.commit(upstream, "NEW", "second\n")

			out := h.runKit(upstream, configured)

			assert.Equal(t, "feature", h.git(kitDir, "rev-parse", "--abbrev-ref", "HEAD"), out)
			assert.Equal(t, before, h.git(kitDir, "rev-parse", "HEAD"))
			assert.Contains(t, out, "WARN: ")
		})
	}
}

// A kit checkout with local changes on the configured branch is left as it
// is, without a pull that could merge into the operator's edits.
func TestKitCheckout_LocalChangesAreLeftAlone(t *testing.T) {
	t.Parallel()

	h := newConfigureHarness(t)
	upstream := h.newUpstream("bosh-genesis-kit")
	kitDir := filepath.Join(h.kits, "bosh")
	h.clone(upstream, kitDir, "main")
	before := h.git(kitDir, "rev-parse", "HEAD")
	require.NoError(t, os.WriteFile(filepath.Join(kitDir, "README"), []byte("operator edit\n"), 0o600))
	h.commit(upstream, "NEW", "second\n")

	out := h.runKit(upstream, "main")

	assert.Equal(t, before, h.git(kitDir, "rev-parse", "HEAD"), out)

	data, err := os.ReadFile(filepath.Join(kitDir, "README"))
	require.NoError(t, err)
	assert.Equal(t, "operator edit\n", string(data))
	assert.Contains(t, out, "WARN: ")
}

// --- dev links ---------------------------------------------------------------

// A dev link that points somewhere else is the operator's choice, and init
// used to repoint it with ln -sfn.
func TestDevLink_PointingElsewhereIsLeftAlone(t *testing.T) {
	t.Parallel()

	h := newConfigureHarness(t)
	upstream := h.newUpstream("bosh-genesis-kit")
	elsewhere := h.path("my-kit")
	require.NoError(t, os.MkdirAll(elsewhere, 0o755))

	devLink := filepath.Join(h.deploys, "bosh", "dev")
	require.NoError(t, os.MkdirAll(filepath.Dir(devLink), 0o755))
	require.NoError(t, os.Symlink(elsewhere, devLink))

	out := h.runKit(upstream, "main")

	target, err := os.Readlink(devLink)
	require.NoError(t, err, out)
	assert.Equal(t, elsewhere, target)
	assert.Contains(t, out, "WARN: ")
}

// A dev that is a real directory is left as it is, and init must not drop a
// link inside it either, which is what ln -sfn does to a directory.
func TestDevLink_RealDirectoryIsLeftAlone(t *testing.T) {
	t.Parallel()

	h := newConfigureHarness(t)
	upstream := h.newUpstream("bosh-genesis-kit")

	devDir := filepath.Join(h.deploys, "bosh", "dev")
	require.NoError(t, os.MkdirAll(devDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(devDir, "kit.yml"), []byte("hand placed\n"), 0o600))

	out := h.runKit(upstream, "main")

	info, err := os.Lstat(devDir)
	require.NoError(t, err)
	assert.True(t, info.IsDir(), "dev must still be a real directory:\n%s", out)

	entries, err := os.ReadDir(devDir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "nothing may be added inside the operator's dev directory:\n%s", out)
	assert.Equal(t, "kit.yml", entries[0].Name())
	assert.Contains(t, out, "WARN: ")
}

// A dev link that already points at the kit checkout is left as it is, with
// no warning.
func TestDevLink_AlreadyLinkedIsQuiet(t *testing.T) {
	t.Parallel()

	h := newConfigureHarness(t)
	upstream := h.newUpstream("bosh-genesis-kit")
	kitDir := filepath.Join(h.kits, "bosh")
	h.clone(upstream, kitDir, "main")

	devLink := filepath.Join(h.deploys, "bosh", "dev")
	require.NoError(t, os.MkdirAll(filepath.Dir(devLink), 0o755))
	require.NoError(t, os.Symlink(kitDir, devLink))

	out := h.runKit(upstream, "main")

	target, err := os.Readlink(devLink)
	require.NoError(t, err)
	assert.Equal(t, kitDir, target)
	assert.NotContains(t, out, "WARN: ")
}

// A relative dev link that resolves to the kit checkout counts as linked.
func TestDevLink_RelativeLinkToTheKitIsQuiet(t *testing.T) {
	t.Parallel()

	h := newConfigureHarness(t)
	upstream := h.newUpstream("bosh-genesis-kit")
	h.clone(upstream, filepath.Join(h.kits, "bosh"), "main")

	devLink := filepath.Join(h.deploys, "bosh", "dev")
	require.NoError(t, os.MkdirAll(filepath.Dir(devLink), 0o755))
	require.NoError(t, os.Symlink("../../kits/bosh", devLink))

	out := h.runKit(upstream, "main")

	target, err := os.Readlink(devLink)
	require.NoError(t, err)
	assert.Equal(t, "../../kits/bosh", target)
	assert.NotContains(t, out, "WARN: ")
}

// A dangling dev link is still the operator's, even when the kit checkout
// is a hand-placed tree that init cannot vouch for.
func TestDevLink_DanglingLinkIsLeftAlone(t *testing.T) {
	t.Parallel()

	h := newConfigureHarness(t)
	upstream := h.newUpstream("bosh-genesis-kit")
	gone := h.path("gone")

	devLink := filepath.Join(h.deploys, "bosh", "dev")
	require.NoError(t, os.MkdirAll(filepath.Dir(devLink), 0o755))
	require.NoError(t, os.Symlink(gone, devLink))

	out := h.runKit(upstream, "main")

	target, err := os.Readlink(devLink)
	require.NoError(t, err, out)
	assert.Equal(t, gone, target)
	assert.Contains(t, out, "WARN: ")
}

// The full configure script must carry the guarded blocks, not a forced
// relink or a recursive delete of the deployments root.
func TestGenerateOCFPConfigureScript_NeverForcesOverOperatorWork(t *testing.T) {
	t.Parallel()

	om := NewOCFPManager("pve", &config.Config{Name: "ocfp-lab"}, nil)
	script := om.GenerateOCFPConfigureScript(t.Context())

	for _, forbidden := range []string{"ln -sfn", "ln -sf ", `rm -rf "${DEPLOYMENTS_ROOT}"`, "checkout --quiet"} {
		assert.NotContains(t, script, forbidden)
	}

	assert.Contains(t, script, strings.Join(generateDeploymentsRepoBlock(), "\n"))
}
