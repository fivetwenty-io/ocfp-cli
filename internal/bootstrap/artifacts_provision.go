package bootstrap

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	pveclient "github.com/ocfp/ocfp-cli-go/internal/cpi/pve"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

const (
	// artifactsSSHReadyTimeout bounds how long we wait for the artifacts VM's
	// sshd to accept connections (through the bastion) after boot.
	artifactsSSHReadyTimeout = 4 * time.Minute

	// artifactsSSHReadyPoll is the reachability poll interval.
	artifactsSSHReadyPoll = 6 * time.Second

	// artifactsProvisionRunTimeout bounds the provisioning script run itself
	// (apt install + rustfs download dominate).
	artifactsProvisionRunTimeout = 8 * time.Minute

	// artifactsSSHConnectTimeout is the per-attempt TCP connect timeout passed
	// to ssh; short so the readiness loop polls promptly while booting.
	artifactsSSHConnectTimeout = "15"
)

// artifactsProvisionConn describes how to reach the artifacts VM over SSH,
// hopping through the bastion. The same identity key authenticates both hops
// (the bootstrap keypair's public key is installed on the bastion and the
// artifacts VM), so a single -i covers the ProxyJump and the target.
type artifactsProvisionConn struct {
	KeyPath       string // local private key path (~/.ocfp/<bloc>/ssh/id_ed25519)
	User          string // login user for both bastion and artifacts (e.g. ubuntu)
	BastionHost   string // bastion address reachable from the operator (tailscale IP)
	ArtifactsHost string // artifacts SDN IP, reachable from the bastion
}

// artifactsSSHArgs builds the ssh argument vector to run remoteCmd on the
// artifacts VM via ProxyJump through the bastion. Host-key checking is disabled
// against an ephemeral known-hosts file because bastions are recreated on each
// bootstrap (their host keys churn), which would otherwise trip accept-new.
func artifactsSSHArgs(c artifactsProvisionConn, remoteCmd string) []string {
	return []string{
		"-i", c.KeyPath,
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=" + os.DevNull,
		"-o", "ConnectTimeout=" + artifactsSSHConnectTimeout,
		"-o", "ProxyJump=" + c.User + "@" + c.BastionHost,
		c.User + "@" + c.ArtifactsHost,
		remoteCmd,
	}
}

// buildArtifactsProvisionSSHArgs returns the ssh args that run the provision
// script piped over stdin (`sudo bash -s`).
func buildArtifactsProvisionSSHArgs(c artifactsProvisionConn) []string {
	return artifactsSSHArgs(c, "sudo bash -s")
}

// provisionArtifactsViaSSH renders the RustFS provisioning script and runs it
// on the artifacts VM over SSH, hopping through the bastion. It resolves the
// connection from the bloc config: the bastion's operator-reachable address
// (bastion_ip) and the bootstrap keypair, with the artifacts VM addressed by
// its SDN IP (reachable from the bastion).
func (m *Manager) provisionArtifactsViaSSH(ctx context.Context, in pveclient.ArtifactsCloudInitInputs, artifactsIP string) error {
	bastionHost := strings.TrimSpace(m.config.BastionIP)
	if bastionHost == "" {
		return fmt.Errorf("bastion_ip not set in config; cannot reach artifacts %s over SSH", artifactsIP) //nolint:err113 // descriptive, not caller-testable
	}

	script, err := pveclient.RenderArtifactsProvisionScript(in)
	if err != nil {
		return fmt.Errorf("render provision script: %w", err)
	}

	conn := artifactsProvisionConn{
		KeyPath:       filepath.Join(config.OcfpSSHKeyDir(m.options.BlocName), "id_ed25519"),
		User:          m.bastionDefaultUsername(),
		BastionHost:   bastionHost,
		ArtifactsHost: artifactsIP,
	}

	return m.provisionArtifactsOverSSH(ctx, conn, script)
}

// provisionArtifactsOverSSH waits for the artifacts VM's sshd (reached through
// the bastion) and then runs the provisioning script on it via stdin. This is
// the PVE-9.x-compatible replacement for cloud-init user-data delivery, which
// the snippet-upload block makes impossible.
func (m *Manager) provisionArtifactsOverSSH(ctx context.Context, conn artifactsProvisionConn, script string) error {
	if err := m.waitArtifactsSSHReady(ctx, conn); err != nil {
		return fmt.Errorf("artifacts ssh not reachable: %w", err)
	}

	runCtx, cancel := context.WithTimeout(ctx, artifactsProvisionRunTimeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "ssh", buildArtifactsProvisionSSHArgs(conn)...)
	cmd.Stdin = strings.NewReader(script)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	logger.Infof("Provisioning artifacts RustFS over SSH via bastion %s -> %s", conn.BastionHost, conn.ArtifactsHost)

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("provision script failed: %w (stderr tail: %s)", err, tailString(stderr.String(), 800))
	}

	return nil
}

// waitArtifactsSSHReady polls a trivial remote command until the artifacts VM
// accepts SSH through the bastion or the deadline elapses.
func (m *Manager) waitArtifactsSSHReady(ctx context.Context, conn artifactsProvisionConn) error {
	deadline := time.Now().Add(artifactsSSHReadyTimeout)

	for {
		probeCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		err := exec.CommandContext(probeCtx, "ssh", artifactsSSHArgs(conn, "true")...).Run()

		cancel()

		if err == nil {
			return nil
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for sshd: %w", artifactsSSHReadyTimeout, err)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(artifactsSSHReadyPoll):
		}
	}
}

// tailString returns the last n characters of s, prefixed with an ellipsis when
// truncated. Used to keep error messages bounded.
func tailString(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}

	return "..." + s[len(s)-n:]
}
