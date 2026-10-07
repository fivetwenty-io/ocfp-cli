package bastion

import (
	"context"
	"encoding/base64"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ocfp/ocfp-cli-go/internal/bastion/ssh"
	"github.com/ocfp/ocfp-cli-go/internal/vaultboot"
)

// bootUnitMockSSHClient answers the support check and the operator probe,
// and records every command, so the tests can read back the unit the phase
// installed.
type bootUnitMockSSHClient struct {
	commands   []string
	whoami     string
	supportErr error
	probeErr   error
	installErr error
}

func (c *bootUnitMockSSHClient) Connect(_ context.Context) error { return nil }

func (c *bootUnitMockSSHClient) TransferFile(_ context.Context, _, _ string, _ ssh.TransferOptions) error {
	return nil
}

func (c *bootUnitMockSSHClient) ExecuteCommand(_ context.Context, cmd string) (*ssh.CommandResult, error) {
	c.commands = append(c.commands, cmd)

	if strings.Contains(cmd, "vault start --check-support") {
		if c.supportErr != nil {
			return &ssh.CommandResult{ExitCode: 1, Stderr: `Error: unknown flag: --check-support`}, c.supportErr
		}

		return &ssh.CommandResult{}, nil
	}

	if strings.Contains(cmd, "id -un") {
		if c.probeErr != nil {
			return &ssh.CommandResult{ExitCode: 1}, c.probeErr
		}

		return &ssh.CommandResult{Stdout: c.whoami}, nil
	}

	if c.installErr != nil {
		return &ssh.CommandResult{ExitCode: 1, Stderr: "sudo: a password is required"}, c.installErr
	}

	return &ssh.CommandResult{}, nil
}

func (c *bootUnitMockSSHClient) CreateTunnel(_ context.Context, _, _ int) error { return nil }

func (c *bootUnitMockSSHClient) Close() error { return nil }

// installedFile decodes, out of the install command, the file the script
// writes to the temporary path that ends with suffix.
func installedFile(t *testing.T, cmd, suffix string) string {
	t.Helper()

	pattern := regexp.MustCompile(`echo ([A-Za-z0-9+/=]+) \| base64 -d > "\$tmp/` + regexp.QuoteMeta(suffix) + `"`)
	match := pattern.FindStringSubmatch(cmd)
	require.Len(t, match, 2, "the install command carries %s base64-encoded", suffix)

	text, err := base64.StdEncoding.DecodeString(match[1])
	require.NoError(t, err)

	return string(text)
}

// installedDropIn decodes the operator drop-in out of the install command.
func installedDropIn(t *testing.T, cmd string) string {
	t.Helper()

	return installedFile(t, cmd, "ocfp-vault@ocfp-lab-drgao.service.d/operator.conf")
}

func bootUnitManager(mock *bootUnitMockSSHClient, dryRun bool) *Manager {
	m := newMinimalManager(newBaseConfig("ocfp-lab-drgao", "pve"))
	m.sshClient = mock
	m.options = &ProvisioningOptions{DryRun: dryRun}

	return m
}

func TestInstallVaultBootUnit_InstallsAndEnablesTheBlocInstance(t *testing.T) {
	t.Parallel()

	mock := &bootUnitMockSSHClient{whoami: "ubuntu\n/home/ubuntu\n"}
	m := bootUnitManager(mock, false)

	require.NoError(t, m.installVaultBootUnit(context.Background()))
	require.Len(t, mock.commands, 3, "one support check, one probe for the operator, one install")
	assert.Equal(t, "/usr/local/bin/ocfp vault start --check-support", mock.commands[0])

	want, err := vaultboot.Render(vaultboot.UnitParams{
		User: "ubuntu", Home: "/home/ubuntu", OCFPPath: "/usr/local/bin/ocfp",
	})
	require.NoError(t, err)

	wantCmd, err := vaultboot.InstallCommand("ocfp-lab-drgao", want)
	require.NoError(t, err)

	install := mock.commands[2]
	assert.Equal(t, wantCmd, install)
	assert.Equal(t, want.Template, installedFile(t, install, "ocfp-vault@ocfp-lab-drgao.service"))
	assert.Equal(t, want.DropIn, installedDropIn(t, install))
	assert.NotContains(t, install, "--now", "init must not start a second vault through the unit")
}

// An ocfp binary without 'vault start' would fail the unit at every boot,
// so the phase fails before it installs anything.
func TestInstallVaultBootUnit_FailsWhenTheBinaryHasNoVaultStart(t *testing.T) {
	t.Parallel()

	mock := &bootUnitMockSSHClient{whoami: "ubuntu\n/home/ubuntu\n", supportErr: errors.New("exit 1")}
	m := bootUnitManager(mock, false)

	err := m.installVaultBootUnit(context.Background())
	require.ErrorIs(t, err, errOCFPLacksVaultStart)
	assert.Contains(t, err.Error(), "/usr/local/bin/ocfp")
	assert.Contains(t, err.Error(), "unknown flag", "the remote output explains the failure")
	assert.Len(t, mock.commands, 1, "nothing is probed or installed")
}

func TestInstallVaultBootUnit_DryRunTouchesNothing(t *testing.T) {
	t.Parallel()

	mock := &bootUnitMockSSHClient{whoami: "ubuntu\n/home/ubuntu\n"}
	m := bootUnitManager(mock, true)

	require.NoError(t, m.installVaultBootUnit(context.Background()))
	assert.Empty(t, mock.commands)
}

func TestInstallVaultBootUnit_FailsWhenTheInstallFails(t *testing.T) {
	t.Parallel()

	mock := &bootUnitMockSSHClient{whoami: "ubuntu\n/home/ubuntu\n", installErr: errors.New("exit 1")}
	m := bootUnitManager(mock, false)

	err := m.installVaultBootUnit(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), vaultboot.UnitPath)
	assert.Contains(t, err.Error(), "a password is required", "the remote output explains the failure")
}

func TestInstallVaultBootUnit_FailsWhenTheOperatorCannotBeRead(t *testing.T) {
	t.Parallel()

	for name, mock := range map[string]*bootUnitMockSSHClient{
		"probe fails":  {probeErr: errors.New("exit 255")},
		"no home":      {whoami: "ubuntu\n"},
		"root":         {whoami: "root\n/root\n"},
		"odd home":     {whoami: "ubuntu\n/home/my user\n"},
		"empty output": {whoami: ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			m := bootUnitManager(mock, false)

			require.Error(t, m.installVaultBootUnit(context.Background()))
			assert.Len(t, mock.commands, 2, "nothing is installed without a valid operator")
		})
	}
}

func TestInstallVaultBootUnit_FailsForABlocTheUnitCannotName(t *testing.T) {
	t.Parallel()

	mock := &bootUnitMockSSHClient{whoami: "ubuntu\n/home/ubuntu\n"}
	m := bootUnitManager(mock, false)
	m.config.Name = "bad/bloc"

	err := m.installVaultBootUnit(context.Background())
	require.ErrorIs(t, err, vaultboot.ErrInvalidBloc)
	assert.Empty(t, mock.commands)
}

// The unit has to be in place before vault_inception runs, so that a reboot
// at any later point in init brings the vault back.
func TestPhaseLists_InstallTheBootUnitBeforeVaultInception(t *testing.T) {
	t.Parallel()

	m := newMinimalManager(newBaseConfig("bloc1", "aws"))

	for list, names := range map[string][]string{
		"sequential": sequentialPhaseNameOrder(m),
		"parallel":   parallelModePhaseNameOrder(m),
		"local":      localPhaseNameOrder(m),
	} {
		unit, inception := indexOf(names, "vault_boot_unit"), indexOf(names, "vault_inception")
		require.NotEqual(t, -1, unit, "%s list is missing vault_boot_unit", list)
		require.NotEqual(t, -1, inception, list)
		assert.Less(t, unit, inception, "%s list must install the unit before vault_inception", list)
	}
}
