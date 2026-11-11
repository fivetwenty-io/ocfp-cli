package bastion

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fivetwenty-io/ocfp-cli/internal/bastion/ssh"
	"github.com/fivetwenty-io/ocfp-cli/internal/guestboot"
)

// dropInMockSSHClient answers each checksum probe with the checksum held for
// that path, and records every command.
type dropInMockSSHClient struct {
	commands   []string
	checksums  map[string]string
	modes      map[string]string // "mode owner:group" per path, default "644 root:root"
	installErr error
}

func (c *dropInMockSSHClient) Connect(_ context.Context) error { return nil }

func (c *dropInMockSSHClient) TransferFile(_ context.Context, _, _ string, _ ssh.TransferOptions) error {
	return nil
}

func (c *dropInMockSSHClient) ExecuteCommand(_ context.Context, cmd string) (*ssh.CommandResult, error) {
	c.commands = append(c.commands, cmd)

	if strings.HasPrefix(cmd, "sha256sum ") {
		for path, sum := range c.checksums {
			if strings.Contains(cmd, path) {
				mode := "644 root:root"
				if m, ok := c.modes[path]; ok {
					mode = m
				}

				return &ssh.CommandResult{Stdout: sum + "\n" + mode + "\n"}, nil
			}
		}

		return &ssh.CommandResult{}, nil
	}

	if c.installErr != nil {
		return &ssh.CommandResult{ExitCode: 1, Stderr: "sudo: a password is required"}, c.installErr
	}

	return &ssh.CommandResult{}, nil
}

func (c *dropInMockSSHClient) CreateTunnel(_ context.Context, _, _ int) error { return nil }

func (c *dropInMockSSHClient) Close() error { return nil }

func dropInManager(provider string, mock *dropInMockSSHClient, dryRun bool) *Manager {
	m := newMinimalManager(newBaseConfig("ocfp-lab-drgao", provider))
	m.sshClient = mock
	m.options = &ProvisioningOptions{DryRun: dryRun}

	return m
}

const (
	keepHostKeysPath       = "/etc/cloud/cloud.cfg.d/99-ocfp-keep-host-keys.cfg"
	trustInstanceCachePath = "/etc/cloud/cloud.cfg.d/99-ocfp-trust-instance-cache.cfg"
)

// installsOf returns the commands that are not checksum probes.
func installsOf(commands []string) []string {
	var installs []string

	for _, cmd := range commands {
		if !strings.HasPrefix(cmd, "sha256sum ") {
			installs = append(installs, cmd)
		}
	}

	return installs
}

// A PVE bastion built from an older template lacks one or both drop-ins, and
// nothing else will ever put them there. Init writes each with the exact
// content the template seed now ships, and does nothing that could cost an
// operator their way in. It restarts nothing, reboots nothing, and leaves the
// host keys alone.
func TestConvergeCloudInitDropIns_WritesMissingDropInsOnPVE(t *testing.T) {
	t.Parallel()

	mock := &dropInMockSSHClient{}
	require.NoError(t, dropInManager("pve", mock, false).convergeCloudInitDropIns(context.Background()))

	installs := installsOf(mock.commands)
	require.Len(t, installs, 2, "one install per missing drop-in: %v", mock.commands)

	for i, want := range []struct{ path, content string }{
		{keepHostKeysPath, guestboot.KeepHostKeysConfig},
		{trustInstanceCachePath, guestboot.TrustInstanceCacheConfig},
	} {
		cmd := installs[i]
		assert.Equal(t, want.content, dropInContent(t, cmd), "content of %s", want.path)
		assert.Contains(t, cmd, "sudo mv -f \"$tmp\" "+want.path)
	}

	for _, cmd := range mock.commands {
		for _, banned := range []string{"systemctl", "reboot", "shutdown", "ssh_host", "ssh-keygen", "/etc/ssh", "cloud-init clean"} {
			assert.NotContains(t, cmd, banned, "the converge step must not touch %s", banned)
		}
	}
}

// The install script must pass bash's own parser, or a quoting slip would
// only show up on a bastion.
func TestConvergeCloudInitDropIns_InstallScriptParses(t *testing.T) {
	t.Parallel()

	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}

	mock := &dropInMockSSHClient{}
	require.NoError(t, dropInManager("pve", mock, false).convergeCloudInitDropIns(context.Background()))

	for _, cmd := range installsOf(mock.commands) {
		require.True(t, strings.HasPrefix(cmd, "bash -c '") && strings.HasSuffix(cmd, "'"), cmd)
		script := strings.TrimSuffix(strings.TrimPrefix(cmd, "bash -c '"), "'")
		require.NotContains(t, script, "'", "the script must hold no single quote")

		out, err := exec.CommandContext(context.Background(), bash, "-n", "-c", script).CombinedOutput()
		require.NoError(t, err, string(out))
	}
}

// Init runs on every pass, so a bastion whose drop-ins already match is left
// exactly as it is.
func TestConvergeCloudInitDropIns_ChangesNothingWhenTheFilesMatch(t *testing.T) {
	t.Parallel()

	mock := &dropInMockSSHClient{checksums: map[string]string{
		keepHostKeysPath:       sha256Hex(guestboot.KeepHostKeysConfig),
		trustInstanceCachePath: sha256Hex(guestboot.TrustInstanceCacheConfig),
	}}
	require.NoError(t, dropInManager("pve", mock, false).convergeCloudInitDropIns(context.Background()))

	assert.Len(t, mock.commands, 2, "one checksum probe per drop-in")
	assert.Empty(t, installsOf(mock.commands), "nothing is written when both files match")
}

// A drop-in that differs from what ocfp ships is rewritten, and only that
// one.
func TestConvergeCloudInitDropIns_RewritesOnlyTheFileThatDiffers(t *testing.T) {
	t.Parallel()

	mock := &dropInMockSSHClient{checksums: map[string]string{
		keepHostKeysPath:       sha256Hex(guestboot.KeepHostKeysConfig),
		trustInstanceCachePath: sha256Hex("manual_cache_clean: false\n"),
	}}
	require.NoError(t, dropInManager("pve", mock, false).convergeCloudInitDropIns(context.Background()))

	installs := installsOf(mock.commands)
	require.Len(t, installs, 1)
	assert.Contains(t, installs[0], trustInstanceCachePath)
	assert.Equal(t, guestboot.TrustInstanceCacheConfig, dropInContent(t, installs[0]))
}

// AWS and GCP bastions are left alone. A new instance-id there is a new
// machine, so trusting the cache would be wrong, and their keep-host-keys
// drop-in comes from the user-data that built them.
func TestConvergeCloudInitDropIns_SkipsOtherProviders(t *testing.T) {
	t.Parallel()

	for _, provider := range []string{"aws", "gcp", ""} {
		mock := &dropInMockSSHClient{}
		require.NoError(t, dropInManager(provider, mock, false).convergeCloudInitDropIns(context.Background()))
		assert.Empty(t, mock.commands, "provider %q", provider)
	}
}

func TestConvergeCloudInitDropIns_DryRunTouchesNothing(t *testing.T) {
	t.Parallel()

	mock := &dropInMockSSHClient{}
	require.NoError(t, dropInManager("pve", mock, true).convergeCloudInitDropIns(context.Background()))
	assert.Empty(t, mock.commands)
}

func TestConvergeCloudInitDropIns_FailsNamingThePath(t *testing.T) {
	t.Parallel()

	mock := &dropInMockSSHClient{installErr: errors.New("exit status 1")}
	err := dropInManager("pve", mock, false).convergeCloudInitDropIns(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), keepHostKeysPath)
	assert.Contains(t, err.Error(), "sudo: a password is required")
}

// The step sits next to bastion_keys in every list that runs it, so a
// bastion-side init and a resumed parallel run converge the drop-ins too.
func TestPhaseLists_ConvergeCloudInitDropInsAfterBastionKeys(t *testing.T) {
	t.Parallel()

	m := newMinimalManager(newBaseConfig("bloc1", "pve"))

	for list, names := range map[string][]string{
		"parallel": parallelModePhaseNameOrder(m),
		"local":    localPhaseNameOrder(m),
	} {
		keys, dropIns := indexOf(names, "bastion_keys"), indexOf(names, "cloudinit_dropins")
		require.NotEqual(t, -1, keys, "%s list is missing bastion_keys", list)
		assert.Equal(t, keys+1, dropIns, "%s list must run cloudinit_dropins right after bastion_keys", list)
	}
}

// The drop-ins are a convenience, so a bastion whose sudo refuses them must
// still get through init. The converge error stays visible to its own tests,
// and the init-facing wrapper turns it into a warning.
func TestSyncCloudInitDropIns_FailureDoesNotAbortInit(t *testing.T) {
	t.Parallel()

	mock := &dropInMockSSHClient{installErr: errors.New("exit status 1")}
	require.NoError(t, dropInManager("pve", mock, false).syncCloudInitDropIns(context.Background()))
	assert.NotEmpty(t, installsOf(mock.commands), "the install should still have been attempted")
}

// dropInContent decodes the drop-in out of the install command.
func dropInContent(t *testing.T, cmd string) string {
	t.Helper()

	match := regexp.MustCompile(`echo ([A-Za-z0-9+/=]+) \| base64 -d`).FindStringSubmatch(cmd)
	require.Len(t, match, 2, "the install command carries the drop-in base64-encoded")

	text, err := base64.StdEncoding.DecodeString(match[1])
	require.NoError(t, err)

	return string(text)
}

// A file with the right bytes but the wrong mode or owner is not converged,
// because cloud-init may skip one it cannot read.
func TestConvergeCloudInitDropIns_RewritesWrongModeOrOwner(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"600 root:root", "644 ubuntu:ubuntu"} {
		mock := &dropInMockSSHClient{
			checksums: map[string]string{
				keepHostKeysPath:       sha256Hex(guestboot.KeepHostKeysConfig),
				trustInstanceCachePath: sha256Hex(guestboot.TrustInstanceCacheConfig),
			},
			modes: map[string]string{trustInstanceCachePath: mode},
		}
		require.NoError(t, dropInManager("pve", mock, false).convergeCloudInitDropIns(context.Background()))

		installs := installsOf(mock.commands)
		require.Len(t, installs, 1, "mode %q", mode)
		assert.Contains(t, installs[0], trustInstanceCachePath)
	}
}

// The install runs for real here, with sudo and chown stubbed, because a
// parse check cannot show that the destination is replaced whole or that a
// failure leaves nothing behind.
func TestDropInInstallScript_ReplacesAtomicallyAndCleansUp(t *testing.T) {
	t.Parallel()

	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}

	run := func(t *testing.T, chownFails bool) (dest string, err error) {
		t.Helper()

		dir := t.TempDir()
		stubs := t.TempDir()
		dest = filepath.Join(dir, "99-test.cfg")
		require.NoError(t, os.WriteFile(dest, []byte("old\n"), 0o600))

		chown := "#!/bin/sh\nexit 0\n"
		if chownFails {
			chown = "#!/bin/sh\nexit 1\n"
		}

		require.NoError(t, os.WriteFile(filepath.Join(stubs, "sudo"), []byte("#!/bin/sh\nexec \"$@\"\n"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(stubs, "chown"), []byte(chown), 0o755))

		script := renderDropInInstall(dest, "new\n")
		cmd := exec.CommandContext(context.Background(), bash, "-c", script)
		cmd.Env = append(os.Environ(), "PATH="+stubs+":"+os.Getenv("PATH"))
		_, err = cmd.CombinedOutput()

		entries, readErr := os.ReadDir(dir)
		require.NoError(t, readErr)
		require.Len(t, entries, 1, "only the destination may remain in %s", dir)

		return dest, err
	}

	dest, err := run(t, false)
	require.NoError(t, err)

	got, readErr := os.ReadFile(dest)
	require.NoError(t, readErr)
	assert.Equal(t, "new\n", string(got))

	info, statErr := os.Stat(dest)
	require.NoError(t, statErr)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm())

	dest, err = run(t, true)
	require.Error(t, err)

	got, readErr = os.ReadFile(dest)
	require.NoError(t, readErr)
	assert.Equal(t, "old\n", string(got), "a failed install leaves the previous file alone")
}
