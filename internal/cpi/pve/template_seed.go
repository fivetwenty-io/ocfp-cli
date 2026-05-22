package pve

import (
	"context"
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// Template seeding installs the firstboot + watchdog units into a template VM
// before it is converted to a PVE template. We drive the VM via termproxy
// because PVE 9.x's snippets-upload API is blocked, the upstream Ubuntu Noble
// cloud image ships without qemu-guest-agent, and SSH to the PVE host is
// out of scope. See plans/pve-snippet-delivery-and-tailscale-config.md.

const (
	// templateSeedCIUser is the cloud-init user the seed step authenticates
	// as. Cloud-init creates it on first boot of the template VM. After
	// `cloud-init clean --logs` and `qm template`, cloned VMs re-run
	// cloud-init and get whatever ciuser the bloc config asks for.
	templateSeedCIUser = "ubuntu"

	// templateSeedCIPassword is the ephemeral password set on the
	// template-build VM solely to allow termproxy login during seed.
	// Long enough to defeat trivial guessing; the VM is destroyed (or
	// converted to a template, which wipes cloud-init credentials on
	// next clone) immediately after seed.
	templateSeedCIPassword = "OcfpSeed.4f9e2a7b3c8d1d6e" //nolint:gosec // ephemeral, single-use

	// timeouts for the seed phases. Real-world numbers from lab runs:
	// cloud-init done in ~90s, apt install in ~45s, write_files in <5s,
	// daemon-reload + enable + shutdown in <5s.
	templateSeedBootTimeout   = 240 * time.Second
	templateSeedLoginTimeout  = 60 * time.Second
	templateSeedAptTimeout    = 180 * time.Second
	templateSeedShellTimeout  = 30 * time.Second
)

// shellPromptRe matches a typical interactive prompt at end-of-buffer. We
// look for "$ " or "# " preceded by something so a single "$" in command
// output doesn't trigger early. The xterm bracketed-paste sequence (\x1b[?2004h)
// often precedes the prompt; ignore it.
var (
	loginPromptRe = regexp.MustCompile(`login:\s*$`)
	pwPromptRe    = regexp.MustCompile(`Password:\s*$`)
	shellPromptRe = regexp.MustCompile(`[\$#]\s*$`)
	cmdMarkerRe   = regexp.MustCompile(`OCFP_CMD_OK_\d+`)
)

// seedTemplateVM connects to the template-build VM via termproxy and writes
// the firstboot + watchdog units, enables systemd, runs cloud-init clean,
// and shuts the VM down so it can be converted to a template.
//
// The VM must already be set up with:
//   - ciuser = templateSeedCIUser
//   - cipassword = templateSeedCIPassword
//   - serial0 = socket
//   - ipconfig0 = ip=dhcp (or whatever lets apt reach the internet)
//   - net0 = a bridge with internet egress
//   - agent = enabled=1 (so QEMU exposes the agent device, even though
//     the guest doesn't yet have the package installed)
//
// On return, the VM is in `stopped` state and ready for `qm template`.
func (m *ComputeManager) seedTemplateVM(ctx context.Context, node string, vmid int) error {
	log := logger.WithOperation("seedTemplateVM")

	tokenHeader := buildPVEAPITokenHeader(m.client.config)
	if tokenHeader == "" {
		return fmt.Errorf("template seed requires API token auth (TokenID + TokenSecret)")
	}

	log.Infof("opening termproxy to vmid %d", vmid)

	sess, err := OpenTermproxy(ctx, m.client.config.Host, tokenHeader, node, vmid, m.client.config.VerifySSL)
	if err != nil {
		return fmt.Errorf("open termproxy: %w", err)
	}

	defer sess.Close()

	if err := seedLogin(sess); err != nil {
		return fmt.Errorf("seed login: %w", err)
	}

	log.Infof("seeding firstboot + watchdog units")

	if err := seedWriteUnits(sess); err != nil {
		return fmt.Errorf("seed write units: %w", err)
	}

	if err := seedFinalize(sess); err != nil {
		return fmt.Errorf("seed finalize: %w", err)
	}

	log.Infof("seed complete; VM shutdown initiated")

	return nil
}

// seedLogin walks the login prompt → password → shell prompt sequence,
// sending wake-up CRs first because the VM may still be mid-boot when we
// connect.
func seedLogin(sess *TermproxySession) error {
	// Wake the serial; cloud-init may be writing boot messages.
	for i := 0; i < 3; i++ {
		_ = sess.SendLine("")
		time.Sleep(500 * time.Millisecond)
	}

	if _, err := sess.ExpectRegex(loginPromptRe, templateSeedBootTimeout); err != nil {
		return fmt.Errorf("wait login prompt: %w", err)
	}

	if err := sess.SendLine(templateSeedCIUser); err != nil {
		return err
	}

	if _, err := sess.ExpectRegex(pwPromptRe, templateSeedLoginTimeout); err != nil {
		return fmt.Errorf("wait password prompt: %w", err)
	}

	if err := sess.SendLine(templateSeedCIPassword); err != nil {
		return err
	}

	if _, err := sess.ExpectRegex(shellPromptRe, templateSeedLoginTimeout); err != nil {
		return fmt.Errorf("wait shell prompt: %w", err)
	}

	return nil
}

// seedWriteUnits installs jq + qemu-guest-agent, writes the script + unit
// files, and enables the services.
func seedWriteUnits(sess *TermproxySession) error {
	// apt non-interactive; jq required by firstboot/watchdog scripts; agent
	// useful for future operator introspection (qm guest exec works after
	// this is installed).
	aptCmd := "sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -q jq qemu-guest-agent"
	if err := runShell(sess, aptCmd, templateSeedAptTimeout); err != nil {
		return fmt.Errorf("apt install: %w", err)
	}

	files := []struct {
		path, content string
		mode          string
	}{
		{"/usr/local/sbin/ocfp-firstboot", firstbootScript, "0755"},
		{"/usr/local/sbin/ocfp-tailscale-watchdog", watchdogScript, "0755"},
		{"/etc/systemd/system/ocfp-firstboot.service", firstbootService, "0644"},
		{"/etc/systemd/system/ocfp-tailscale-watchdog.service", watchdogService, "0644"},
		{"/etc/systemd/system/ocfp-tailscale-watchdog.timer", watchdogTimer, "0644"},
	}

	for _, f := range files {
		if err := writeRemoteFile(sess, f.path, f.content, f.mode); err != nil {
			return fmt.Errorf("write %s: %w", f.path, err)
		}
	}

	for _, c := range []string{
		"sudo systemctl daemon-reload",
		"sudo systemctl enable ocfp-firstboot.service",
		"sudo systemctl enable ocfp-tailscale-watchdog.timer",
	} {
		if err := runShell(sess, c, templateSeedShellTimeout); err != nil {
			return fmt.Errorf("%s: %w", c, err)
		}
	}

	return nil
}

// seedFinalize runs cloud-init clean (so cloned VMs re-run cloud-init with
// their own per-VM config) and shuts the VM down. We do NOT wait for the
// shell prompt after shutdown because the session dies as soon as systemd
// stops the getty.
func seedFinalize(sess *TermproxySession) error {
	if err := runShell(sess, "sudo cloud-init clean --logs", templateSeedShellTimeout); err != nil {
		return fmt.Errorf("cloud-init clean: %w", err)
	}

	// Fire-and-forget shutdown. ExpectRegex will see connection close and
	// return; ignoring its result is fine.
	_ = sess.SendLine("sudo shutdown -h now")
	time.Sleep(2 * time.Second)

	return nil
}

// runShell sends a command, appends a sentinel echo so we can match the
// prompt regardless of $PS1 customizations, and waits for the sentinel.
func runShell(sess *TermproxySession, cmd string, timeout time.Duration) error {
	marker := fmt.Sprintf("OCFP_CMD_OK_%d", time.Now().UnixNano())
	wrapped := cmd + " && echo " + marker

	if err := sess.SendLine(wrapped); err != nil {
		return err
	}

	markerSpecific := regexp.MustCompile(regexp.QuoteMeta(marker))

	out, err := sess.ExpectRegex(markerSpecific, timeout)
	if err != nil {
		return fmt.Errorf("%w (output tail: %q)", err, tail(out, 400))
	}

	return nil
}

// writeRemoteFile base64-encodes the content locally and writes it on the
// remote via `tee` + `chmod`. base64 avoids shell-quoting headaches for
// arbitrary script content.
func writeRemoteFile(sess *TermproxySession, path, content, mode string) error {
	b64 := base64.StdEncoding.EncodeToString([]byte(content))
	// Single-quote the base64 — alphabet excludes single quote so it's safe.
	cmd := fmt.Sprintf("echo '%s' | base64 -d | sudo tee %s >/dev/null && sudo chmod %s %s",
		b64, path, mode, path)

	return runShell(sess, cmd, templateSeedShellTimeout)
}

// buildPVEAPITokenHeader assembles the "PVEAPIToken=<id>=<secret>" header
// value termproxy needs. Returns "" if either field is unset.
func buildPVEAPITokenHeader(cfg *Config) string {
	if cfg.TokenID == "" || cfg.TokenSecret == "" {
		return ""
	}

	return "PVEAPIToken=" + strings.TrimSpace(cfg.TokenID) + "=" + strings.TrimSpace(cfg.TokenSecret)
}
