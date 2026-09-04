package bastion

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/bastion/ssh"
	"github.com/ocfp/ocfp-cli-go/internal/config"
)

// ocfpCLIMockSSHClient records executed commands and transferred files so the
// phase test can tell a release download apart from a local upload.
// binaryPresent answers the phase's "is an ocfp already installed" probe.
type ocfpCLIMockSSHClient struct {
	commands      []string
	transfers     []string
	binaryPresent bool
}

func (c *ocfpCLIMockSSHClient) Connect(_ context.Context) error { return nil }

func (c *ocfpCLIMockSSHClient) TransferFile(_ context.Context, local, _ string, _ ssh.TransferOptions) error {
	c.transfers = append(c.transfers, local)

	return nil
}

func (c *ocfpCLIMockSSHClient) ExecuteCommand(_ context.Context, cmd string) (*ssh.CommandResult, error) {
	c.commands = append(c.commands, cmd)

	if strings.HasPrefix(cmd, "test -x ") && c.binaryPresent {
		return &ssh.CommandResult{ExitCode: 0, Stdout: "present\n"}, nil
	}

	return &ssh.CommandResult{ExitCode: 0}, nil
}

func (c *ocfpCLIMockSSHClient) CreateTunnel(_ context.Context, _, _ int) error { return nil }

func (c *ocfpCLIMockSSHClient) Close() error { return nil }

func newOCFPCLIManager(cfg *config.Config, mock *ocfpCLIMockSSHClient) *Manager {
	m := newMinimalManager(cfg)
	m.sshClient = mock
	m.options = &ProvisioningOptions{}

	return m
}

func TestSetupOCFPCLI_ReleaseModeRunsDownloadScript(t *testing.T) {
	t.Setenv("OCFP_CLI_SOURCE", "")
	t.Setenv("OCFP_BINARY_PATH", "")
	t.Setenv("OCFP_CLI_VERSION", "0.1.0")

	mock := &ocfpCLIMockSSHClient{}
	m := newOCFPCLIManager(newBaseConfig("bloc1", "aws"), mock)

	if err := m.setupOCFPCLI(context.Background()); err != nil {
		t.Fatalf("setupOCFPCLI: %v", err)
	}

	if len(mock.transfers) != 0 {
		t.Errorf("release mode must not scp anything, transferred %v", mock.transfers)
	}

	joined := strings.Join(mock.commands, "\n")
	if !strings.Contains(joined, "github.com/fivetwenty-io/ocfp-cli/releases/download/") {
		t.Errorf("release mode should run the release download script, commands were:\n%s", joined)
	}

	if !strings.Contains(joined, "0.1.0") {
		t.Errorf("release script should carry the pinned version 0.1.0, commands were:\n%s", joined)
	}

	if !strings.Contains(joined, "completion bash") {
		t.Errorf("a successful install should be followed by the completions script, commands were:\n%s", joined)
	}
}

// TestSetupOCFPCLI_DefaultVersionFromDevBuildIsLatest runs the real default
// path: tests are not stamped, so version.Version is "dev" and the bastion
// must ask GitHub for the latest release rather than fail or fetch v-dev.
func TestSetupOCFPCLI_DefaultVersionFromDevBuildIsLatest(t *testing.T) {
	t.Setenv("OCFP_CLI_SOURCE", "")
	t.Setenv("OCFP_BINARY_PATH", "")
	t.Setenv("OCFP_CLI_VERSION", "")

	mock := &ocfpCLIMockSSHClient{}
	m := newOCFPCLIManager(newBaseConfig("bloc1", "aws"), mock)

	if err := m.setupOCFPCLI(context.Background()); err != nil {
		t.Fatalf("setupOCFPCLI: %v", err)
	}

	joined := strings.Join(mock.commands, "\n")
	if !strings.Contains(joined, "releases/latest") {
		t.Errorf("a dev operator build should resolve the latest release, commands were:\n%s", joined)
	}

	if strings.Contains(joined, "download/vdev/") {
		t.Errorf("the literal dev version must never reach the download URL, commands were:\n%s", joined)
	}
}

func TestSetupOCFPCLI_StaleBinaryPathStillInstallsRelease(t *testing.T) {
	t.Setenv("OCFP_CLI_SOURCE", "")
	t.Setenv("OCFP_BINARY_PATH", filepath.Join(t.TempDir(), "gone"))
	t.Setenv("OCFP_CLI_VERSION", "0.1.0")

	mock := &ocfpCLIMockSSHClient{}
	m := newOCFPCLIManager(newBaseConfig("bloc1", "aws"), mock)

	if err := m.setupOCFPCLI(context.Background()); err != nil {
		t.Fatalf("setupOCFPCLI: %v", err)
	}

	if !strings.Contains(strings.Join(mock.commands, "\n"), "releases/download") {
		t.Error("a stale OCFP_BINARY_PATH must not silently switch to the local source")
	}
}

// TestSetupOCFPCLI_DirectoryBinaryPathStillInstallsRelease covers a path that
// exists but is not a regular file, which used to select the local source and
// then fail on the checksum instead of falling through to the release.
func TestSetupOCFPCLI_DirectoryBinaryPathStillInstallsRelease(t *testing.T) {
	t.Setenv("OCFP_CLI_SOURCE", "")
	t.Setenv("OCFP_BINARY_PATH", t.TempDir())
	t.Setenv("OCFP_CLI_VERSION", "0.1.0")

	mock := &ocfpCLIMockSSHClient{}
	m := newOCFPCLIManager(newBaseConfig("bloc1", "aws"), mock)

	if err := m.setupOCFPCLI(context.Background()); err != nil {
		t.Fatalf("setupOCFPCLI: %v", err)
	}

	if !strings.Contains(strings.Join(mock.commands, "\n"), "releases/download") {
		t.Error("a directory OCFP_BINARY_PATH must not select the local source")
	}
}

func TestSetupOCFPCLI_ForceReachesTheScript(t *testing.T) {
	t.Setenv("OCFP_CLI_SOURCE", "")
	t.Setenv("OCFP_BINARY_PATH", "")
	t.Setenv("OCFP_CLI_VERSION", "0.1.0")

	mock := &ocfpCLIMockSSHClient{}
	m := newOCFPCLIManager(newBaseConfig("bloc1", "aws"), mock)
	m.options.Force = true

	if err := m.setupOCFPCLI(context.Background()); err != nil {
		t.Fatalf("setupOCFPCLI: %v", err)
	}

	if !strings.Contains(strings.Join(mock.commands, "\n"), "Forcing reinstall") {
		t.Error("--force must reach the generated release script")
	}
}

// TestSetupOCFPCLI_FullInitFailsWhenNothingIsInstalled pins that a failed
// install is only tolerated when the bastion already has an ocfp. Otherwise
// later phases would silently skip vault populate and configure.
func TestSetupOCFPCLI_FullInitFailsWhenNothingIsInstalled(t *testing.T) {
	t.Setenv("OCFP_CLI_SOURCE", "local")
	t.Setenv("OCFP_BINARY_PATH", "")
	t.Chdir(t.TempDir())
	t.Setenv("HOME", t.TempDir())

	mock := &ocfpCLIMockSSHClient{binaryPresent: false}
	m := newOCFPCLIManager(newBaseConfig("bloc1", "aws"), mock)

	if err := m.setupOCFPCLI(context.Background()); err == nil {
		t.Fatal("full init must fail when the install fails and no ocfp exists on the bastion")
	}
}

func TestSetupOCFPCLI_LocalModeWithoutBinaryWarnsInFullInit(t *testing.T) {
	t.Setenv("OCFP_CLI_SOURCE", "local")
	t.Setenv("OCFP_BINARY_PATH", "")
	t.Chdir(t.TempDir())
	t.Setenv("HOME", t.TempDir())

	mock := &ocfpCLIMockSSHClient{binaryPresent: true}
	m := newOCFPCLIManager(newBaseConfig("bloc1", "aws"), mock)

	if err := m.setupOCFPCLI(context.Background()); err != nil {
		t.Fatalf("full init should warn and continue when the local binary is missing but the bastion has one, got %v", err)
	}

	joined := strings.Join(mock.commands, "\n")
	if strings.Contains(joined, "releases/download") {
		t.Errorf("local mode must not download a release, commands were:\n%s", joined)
	}

	if strings.Contains(joined, "completion bash") {
		t.Errorf("completions must not run when no binary was installed, commands were:\n%s", joined)
	}
}

func TestSetupOCFPCLI_LocalModeWithoutBinaryFailsInOCFPOnly(t *testing.T) {
	t.Setenv("OCFP_CLI_SOURCE", "local")
	t.Setenv("OCFP_BINARY_PATH", "")
	t.Chdir(t.TempDir())
	t.Setenv("HOME", t.TempDir())

	mock := &ocfpCLIMockSSHClient{}
	m := newOCFPCLIManager(newBaseConfig("bloc1", "aws"), mock)
	m.options.OCFPOnly = true

	if err := m.setupOCFPCLI(context.Background()); err == nil {
		t.Fatal("--ocfp mode must fail when the local binary is missing")
	}
}

func TestSetupOCFPCLI_InvalidSourceIsAlwaysFatal(t *testing.T) {
	t.Setenv("OCFP_CLI_SOURCE", "carrier-pigeon")

	mock := &ocfpCLIMockSSHClient{}
	m := newOCFPCLIManager(newBaseConfig("bloc1", "aws"), mock)

	if err := m.setupOCFPCLI(context.Background()); err == nil {
		t.Fatal("an unknown OCFP_CLI_SOURCE must fail rather than silently pick a mode")
	}

	if len(mock.commands) != 0 {
		t.Errorf("nothing should run on the bastion after a config error, ran %v", mock.commands)
	}
}
