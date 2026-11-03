package bastion

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ocfp/ocfp-cli-go/internal/bastion/ssh"
	"github.com/ocfp/ocfp-cli-go/internal/guestboot"
)

// dropInMockSSHClient answers each checksum probe with the checksum held for
// that path, and records every command.
type dropInMockSSHClient struct {
	commands   []string
	checksums  map[string]string
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
				return &ssh.CommandResult{Stdout: sum + "\n"}, nil
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
		assert.Equal(t, want.content, installedFile(t, cmd, "dropin"), "content of %s", want.path)
		assert.Contains(t, cmd, "sudo install -D -m 0644 -o root -g root \"$tmp/dropin\" "+want.path)
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
	assert.Equal(t, guestboot.TrustInstanceCacheConfig, installedFile(t, installs[0], "dropin"))
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
