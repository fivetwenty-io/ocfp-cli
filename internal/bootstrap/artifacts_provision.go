package bootstrap

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/fivetwenty-io/ocfp-cli/internal/artifacts/provision"
	"github.com/fivetwenty-io/ocfp-cli/internal/bastion/ssh"
	"github.com/fivetwenty-io/ocfp-cli/internal/config"
	"github.com/fivetwenty-io/ocfp-cli/internal/logger"
)

const (
	// artifactsSSHReadyTimeout bounds how long we wait for the artifacts VM's
	// sshd to accept connections (through the bastion) after boot. The artifacts
	// VM clones the *unseeded* generic template, so its first boot runs a full
	// cloud-init (datasource, growpart, package config) before sshd answers —
	// noticeably slower than a pre-seeded image, and reached through a bastion
	// that may itself have just booted. 4m was too short and left a
	// half-provisioned VM; 10m gives the cold first boot real headroom.
	artifactsSSHReadyTimeout = 10 * time.Minute

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
	KeyPath       string // local private key path (config.OcfpSSHKeyDir(bloc)/id_ed25519)
	User          string // login user for both bastion and artifacts (e.g. ubuntu)
	BastionHost   string // bastion address reachable from the operator (tailscale IP)
	ArtifactsHost string // artifacts SDN IP, reachable from the bastion
}

// artifactsSSHArgs builds the ssh argument vector to run remoteCmd on the
// artifacts VM by hopping through the bastion. The artifacts VM is recreated
// on each bootstrap and its host key churns, so the final hop to it keeps the
// relaxed host-key options of every host behind the bastion.
//
// The bastion hop is expressed as an explicit ProxyCommand rather than
// `-o ProxyJump=…` because options given with -o apply only to the
// destination: the inner ssh ProxyJump spawns takes its host-key checking
// from the user's ssh config instead. The ProxyCommand states the bastion's
// checking itself, so the bastion's key is always checked against the real
// known_hosts however the user's ssh config is set up.
func artifactsSSHArgs(c artifactsProvisionConn, remoteCmd string) ([]string, error) {
	proxyCommand, err := BastionProxyCommand(c.KeyPath, c.User, c.BastionHost, true)
	if err != nil {
		return nil, err
	}

	args := []string{"-i", c.KeyPath, "-o", "BatchMode=yes"}
	args = append(args, ssh.InternalHostKeyOptions()...)

	return append(args,
		"-o", "IdentitiesOnly=yes",
		"-o", "ConnectTimeout="+artifactsSSHConnectTimeout,
		"-o", "ProxyCommand="+proxyCommand,
		c.User+"@"+c.ArtifactsHost,
		remoteCmd,
	), nil
}

// BastionProxyCommand returns the ssh ProxyCommand that reaches a host behind
// the bloc bastion. It relays through the bastion with -W and gives the jump
// hop the bastion's own host-key options, accept-new against the real
// known_hosts, which `-o ProxyJump=…` would leave to the user's ssh config
// (see artifactsSSHArgs). A bastion seen for the first time is recorded, and
// one whose key has changed is refused. The jump hop authenticates with the
// same key and login user as the final hop, and keyPath, user, and
// bastionHost are interpolated into a string ssh runs through a shell, so
// callers must pass validated values. The inner ssh reads the user's ssh
// config as usual, so settings such as a port for the bastion still apply.
//
// An empty keyPath leaves authentication to the operator's own ssh setup.
// The jump hop then drops -i and IdentitiesOnly, so the agent, the default
// keys, and the ssh config apply, while the host-key options stay exactly as
// they are with a key. It keeps BatchMode=yes only when batchMode is set,
// which callers do for any run that is not interactive, so a passphrase or
// password prompt can reach a person but never stalls a script. With a key
// the jump hop is always in batch mode and batchMode is ignored.
//
// It returns an error only when the bastion's host key options cannot be
// built (see ssh.BastionHostKeyOptions).
func BastionProxyCommand(keyPath, user, bastionHost string, batchMode bool) (string, error) {
	hostKeyOptions, err := ssh.BastionHostKeyOptions()
	if err != nil {
		return "", err
	}

	// The outer ssh expands %-tokens in a ProxyCommand before the shell sees
	// it, and the inner ssh expands them again in the known_hosts path. The
	// options already double each literal % for the inner ssh, and doubling
	// again here leaves that for the inner ssh once the outer is done.
	hostKey := strings.ReplaceAll(ssh.ShellJoin(hostKeyOptions), "%", "%%")

	if keyPath == "" {
		batch := ""
		if batchMode {
			batch = "-o BatchMode=yes "
		}

		return fmt.Sprintf(
			"ssh %s%s -o LogLevel=ERROR -o ConnectTimeout=%s -W %%h:%%p %s@%s",
			batch, hostKey, artifactsSSHConnectTimeout, user, bastionHost,
		), nil
	}

	return fmt.Sprintf(
		"ssh -i %s -o BatchMode=yes %s -o IdentitiesOnly=yes -o LogLevel=ERROR -o ConnectTimeout=%s -W %%h:%%p %s@%s",
		keyPath, hostKey, artifactsSSHConnectTimeout, user, bastionHost,
	), nil
}

// buildArtifactsProvisionSSHArgs returns the ssh args that run the provision
// script piped over stdin (`sudo bash -s`).
func buildArtifactsProvisionSSHArgs(c artifactsProvisionConn) ([]string, error) {
	return artifactsSSHArgs(c, "sudo bash -s")
}

// provisionArtifactsViaSSH renders the RustFS provisioning script and runs it
// on the artifacts VM over SSH, hopping through the bastion. It resolves the
// connection from the bloc config: the bastion's operator-reachable address
// (bastion_ip) and the bootstrap keypair, with the artifacts VM addressed by
// its SDN IP (reachable from the bastion).
func (m *Manager) provisionArtifactsViaSSH(ctx context.Context, in provision.ArtifactsCloudInitInputs, artifactsIP string) error {
	bastionHost := strings.TrimSpace(m.config.BastionIP)
	if bastionHost == "" {
		return fmt.Errorf("bastion_ip not set in config; cannot reach artifacts %s over SSH", artifactsIP) //nolint:err113 // descriptive, not caller-testable
	}

	script, err := provision.RenderArtifactsProvisionScript(in)
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
	err := m.waitArtifactsSSHReady(ctx, conn)
	if err != nil {
		return fmt.Errorf("artifacts ssh not reachable: %w", err)
	}

	args, err := buildArtifactsProvisionSSHArgs(conn)
	if err != nil {
		return fmt.Errorf("provision script did not run: %w", err)
	}

	runCtx, cancel := context.WithTimeout(ctx, artifactsProvisionRunTimeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "ssh", args...) // #nosec G204 -- args are CLI/config-constructed, not user-tainted
	cmd.Stdin = strings.NewReader(script)

	var stdout, stderr bytes.Buffer

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	logger.Infof("Provisioning artifacts RustFS over SSH via bastion %s -> %s", conn.BastionHost, conn.ArtifactsHost)

	err = cmd.Run()
	if err != nil {
		hostKeyErr := ssh.SystemSSHHostKeyFailure(stderr.String(), conn.BastionHost, ssh.DefaultSSHPort, err)
		if hostKeyErr != nil {
			return fmt.Errorf("provision script did not run: %w", hostKeyErr)
		}

		return fmt.Errorf("provision script failed: %w (stderr tail: %s)", err, tailString(stderr.String(), 800))
	}

	return nil
}

// waitArtifactsSSHReady polls a trivial remote command until the artifacts VM
// accepts SSH through the bastion or the deadline elapses.
func (m *Manager) waitArtifactsSSHReady(ctx context.Context, conn artifactsProvisionConn) error {
	args, err := artifactsSSHArgs(conn, "true")
	if err != nil {
		return err
	}

	deadline := time.Now().Add(artifactsSSHReadyTimeout)

	for {
		probeCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		probe := exec.CommandContext(probeCtx, "ssh", args...) // #nosec G204 -- args are CLI/config-constructed, not user-tainted

		var stderr bytes.Buffer

		probe.Stderr = &stderr
		err := probe.Run()

		cancel()

		if err == nil {
			return nil
		}

		// A refused bastion key does not change by waiting, so stop now.
		hostKeyErr := ssh.SystemSSHHostKeyFailure(stderr.String(), conn.BastionHost, ssh.DefaultSSHPort, err)
		if hostKeyErr != nil {
			return hostKeyErr
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
