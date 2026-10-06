package provision

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/stretchr/testify/require"
)

// fakeGenesis stands in for `genesis repo-init`: it records its arguments
// and lays out ./<name> the way repo-init does, with .genesis/config, a
// .genesis/kits directory, an empty dev/ (when no kit is linked), and a
// README. Setting FAKE_GENESIS_FAIL makes it exit non-zero.
const fakeGenesis = `#!/usr/bin/env bash
echo "$*" >> "${FAKE_GENESIS_LOG}"
if [ -n "${FAKE_GENESIS_FAIL:-}" ]; then
    exit 1
fi
name="${@: -1}"
mkdir -p "${name}/.genesis/kits" "${name}/dev"
echo "deployment_type: ${name}" > "${name}/.genesis/config"
echo "readme" > "${name}/README.md"
`

// repoInitHarness runs the generated repo-init block in bash against a
// fake genesis, with the deployment and kit trees under a temp directory.
type repoInitHarness struct {
	t        *testing.T
	root     string
	deploys  string
	kits     string
	argsFile string
}

func newRepoInitHarness(t *testing.T) *repoInitHarness {
	t.Helper()

	root := t.TempDir()
	h := &repoInitHarness{
		t:        t,
		root:     root,
		deploys:  filepath.Join(root, "deployments"),
		kits:     filepath.Join(root, "kits"),
		argsFile: filepath.Join(root, "genesis-args.log"),
	}

	bin := filepath.Join(root, "bin")
	require.NoError(t, os.MkdirAll(bin, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bin, "genesis"), []byte(fakeGenesis), 0o755)) //nolint:gosec // test stub must be executable

	return h
}

func (h *repoInitHarness) mkdir(rel string) {
	h.t.Helper()
	require.NoError(h.t, os.MkdirAll(filepath.Join(h.root, rel), 0o755))
}

func (h *repoInitHarness) write(rel, content string) {
	h.t.Helper()
	h.mkdir(filepath.Dir(rel))
	require.NoError(h.t, os.WriteFile(filepath.Join(h.root, rel), []byte(content), 0o600))
}

func (h *repoInitHarness) exists(rel string) bool {
	_, err := os.Lstat(filepath.Join(h.root, rel))

	return err == nil
}

func (h *repoInitHarness) read(rel string) string {
	h.t.Helper()

	data, err := os.ReadFile(filepath.Join(h.root, rel))
	require.NoError(h.t, err)

	return string(data)
}

// run executes the repo-init block with the given deployment lists and
// returns the combined output and the error from bash.
func (h *repoInitHarness) run(dev, release []string, extraEnv ...string) (string, error) {
	h.t.Helper()

	om := NewOCFPManager("pve", &config.Config{Name: "ocfp-test"}, nil)

	script := strings.Join([]string{
		`log_info() { echo "INFO: $*"; }`,
		`log_success() { echo "OK: $*"; }`,
		`log_warning() { echo "WARN: $*"; }`,
		`log_error() { echo "ERROR: $*"; }`,
		"DEPLOYMENTS_ROOT=" + shellSingleQuote(h.deploys),
		"KITS_ROOT=" + shellSingleQuote(h.kits),
		"DEV_DEPLOYMENTS=" + formatShellArray(dev),
		"RELEASE_DEPLOYMENTS=" + formatShellArray(release),
		strings.Join(om.generateGenesisRepoInitScript(), "\n"),
	}, "\n")

	cmd := exec.CommandContext(h.t.Context(), "bash", "-c", script)
	cmd.Env = append(os.Environ(),
		"PATH="+filepath.Join(h.root, "bin")+":"+os.Getenv("PATH"),
		"FAKE_GENESIS_LOG="+h.argsFile,
	)
	cmd.Env = append(cmd.Env, extraEnv...)

	out, err := cmd.CombinedOutput()

	return string(out), err
}

// TestGenesisRepoInit_ReleaseModeGetsConfigWithoutKit covers release-mode
// deployments cloned from a deployments repo that gitignores
// */.genesis/config: the directory arrives with .genesis/kits holding the
// pinned kit tarballs but no config. repo-init must run without a kit (no
// -k download, no -l link), and only its config may land beside the kits.
func TestGenesisRepoInit_ReleaseModeGetsConfigWithoutKit(t *testing.T) {
	t.Parallel()

	h := newRepoInitHarness(t)
	h.write("deployments/bosh/.genesis/kits/bosh-4.1.0.tar.gz", "pinned kit")
	h.write("deployments/bosh/lab.yml", "kit: {name: bosh, version: 4.1.0}")
	h.write("deployments/cf/.genesis/config", "deployment_type: cf-existing")

	out, err := h.run(nil, []string{"bosh", "cf", "autoscaler"})
	require.NoError(t, err, out)

	require.Equal(t, "deployment_type: bosh\n", h.read("deployments/bosh/.genesis/config"))
	require.Equal(t, "pinned kit", h.read("deployments/bosh/.genesis/kits/bosh-4.1.0.tar.gz"))
	require.False(t, h.exists("deployments/bosh/.genesis/.genesis"), "staged .genesis nested inside the existing one")
	require.False(t, h.exists("deployments/bosh/dev"), "release-mode deployment gained a dev directory")
	require.False(t, h.exists("deployments/bosh/README.md"), "repo-init scaffolding leaked into the deployment")

	require.Equal(t, "deployment_type: cf-existing", h.read("deployments/cf/.genesis/config"), "existing config was replaced")
	require.False(t, h.exists("deployments/autoscaler"), "missing release deployment was created")

	args := h.read("genesis-args.log")
	require.Equal(t, "repo-init --skip-vault --no-commit bosh\n", args, "repo-init should run once, for bosh only, with no kit")
}

// TestGenesisRepoInit_DevModeKeepsExistingGenesisDir covers a dev-mode
// deployment whose directory already carries .genesis (kits or manifests
// from a deployments repo) but no config: the config is added in place
// rather than the staged .genesis being moved inside the existing one.
func TestGenesisRepoInit_DevModeKeepsExistingGenesisDir(t *testing.T) {
	t.Parallel()

	h := newRepoInitHarness(t)
	h.mkdir("kits/shield")
	h.mkdir("kits/jumpbox")
	h.write("deployments/shield/.genesis/manifests/lab.yml", "manifest")

	out, err := h.run([]string{"shield", "jumpbox"}, nil)
	require.NoError(t, err, out)

	require.Equal(t, "deployment_type: shield\n", h.read("deployments/shield/.genesis/config"))
	require.Equal(t, "manifest", h.read("deployments/shield/.genesis/manifests/lab.yml"))
	require.False(t, h.exists("deployments/shield/.genesis/.genesis"), "staged .genesis nested inside the existing one")

	require.Equal(t, "deployment_type: jumpbox\n", h.read("deployments/jumpbox/.genesis/config"))
	require.True(t, h.exists("deployments/jumpbox/.genesis/kits"), "fresh deployment should get repo-init's whole .genesis")
	require.True(t, h.exists("deployments/jumpbox/dev"), "dev deployment should link its kit")
}

// TestGenesisRepoInit_ReleaseModeFailureFailsThePhase verifies a failed
// release-mode repo-init stops the script with a non-zero exit instead of
// leaving a green init with no genesis repo.
func TestGenesisRepoInit_ReleaseModeFailureFailsThePhase(t *testing.T) {
	t.Parallel()

	h := newRepoInitHarness(t)
	h.mkdir("deployments/bosh/.genesis/kits")

	out, err := h.run(nil, []string{"bosh"}, "FAKE_GENESIS_FAIL=1")
	require.Error(t, err, out)
	require.Contains(t, out, "ERROR: genesis repo-init failed for bosh")
	require.False(t, h.exists("deployments/bosh/.genesis/config"))
}
