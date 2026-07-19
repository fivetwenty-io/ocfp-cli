package pve

import (
	"strings"
	"testing"
)

// These tests pin the high-level invariants of the firstboot + watchdog
// scripts. They intentionally do NOT diff the full script content — that
// would couple the test suite to whitespace edits. They DO catch accidental
// changes to the SMBIOS-reading discipline, the role discriminator gate, and
// the systemd unit ordering, all of which are correctness-critical.

func TestFirstbootScript_ReadsSMBIOSFields(t *testing.T) {
	t.Parallel()

	for _, want := range []string{
		"dmidecode -s system-family",
		"dmidecode -s system-serial-number",
		"dmidecode -s system-sku-number",
	} {
		if !strings.Contains(firstbootScript, want) {
			t.Errorf("firstbootScript missing %q — must read all 3 SMBIOS fields", want)
		}
	}
}

func TestFirstbootScript_GatesOnFamily(t *testing.T) {
	t.Parallel()

	if !strings.Contains(firstbootScript, smbiosFamilyBastion) {
		t.Errorf("firstbootScript must check for family=%q before acting", smbiosFamilyBastion)
	}

	if !strings.Contains(firstbootScript, "exit 0") {
		t.Errorf("firstbootScript must exit 0 (no-op) when family doesn't match — other clones of the template would otherwise fail")
	}
}

func TestFirstbootScript_InstallsTailscaleIdempotently(t *testing.T) {
	t.Parallel()

	if !strings.Contains(firstbootScript, "command -v tailscale") {
		t.Errorf("firstbootScript must check command -v tailscale before installing — re-run safety")
	}

	if !strings.Contains(firstbootScript, "tailscale.com/install.sh") {
		t.Errorf("firstbootScript must use official tailscale install script")
	}

	if !strings.Contains(firstbootScript, "tailscale up") {
		t.Errorf("firstbootScript must run tailscale up")
	}
}

func TestFirstbootScript_HardeningFlagsPresent(t *testing.T) {
	t.Parallel()

	// These flags are the lessons learned from commit 3a2efab.
	for _, flag := range []string{"--accept-dns", "--accept-routes"} {
		if !strings.Contains(firstbootScript, flag) {
			t.Errorf("firstbootScript missing %q hardening flag", flag)
		}
	}
}

func TestWatchdogScript_GatedOnSelfOnline(t *testing.T) {
	t.Parallel()

	if !strings.Contains(watchdogScript, ".Self.Online") {
		t.Errorf("watchdogScript must check Self.Online from tailscale status JSON — that's the whole point")
	}

	if !strings.Contains(watchdogScript, "tailscale status --json") {
		t.Errorf("watchdogScript must call tailscale status --json")
	}
}

func TestSystemdUnits_OrderingCorrect(t *testing.T) {
	t.Parallel()

	if !strings.Contains(firstbootService, "After=cloud-init.service network-online.target") {
		t.Errorf("firstboot.service must wait for cloud-init AND network online — premature run breaks tailscale install")
	}

	if !strings.Contains(firstbootService, "ConditionPathExists=!") {
		t.Errorf("firstboot.service needs a sentinel ConditionPathExists so it doesn't re-run after success")
	}

	if !strings.Contains(watchdogTimer, "OnBootSec=") {
		t.Errorf("watchdog.timer missing OnBootSec — first check should happen shortly after boot")
	}

	if !strings.Contains(watchdogTimer, "OnUnitActiveSec=") {
		t.Errorf("watchdog.timer missing OnUnitActiveSec — recurring cadence required")
	}
}

func TestFirstbootScript_ShebangAndFailFast(t *testing.T) {
	t.Parallel()

	for name, script := range map[string]string{
		"firstboot": firstbootScript,
		"watchdog":  watchdogScript,
	} {
		if !strings.HasPrefix(script, "#!/bin/bash") {
			t.Errorf("%s script missing #!/bin/bash shebang", name)
		}

		if !strings.Contains(script, "set -euo pipefail") {
			t.Errorf("%s script must `set -euo pipefail` for fail-fast", name)
		}
	}
}

func TestFirstbootScript_InstallsCloudflared(t *testing.T) {
	if !strings.Contains(firstbootScript, "cloudflare.token") {
		t.Error("firstbootScript must read .cloudflare.token from sku JSON")
	}
	if !strings.Contains(firstbootScript, "cloudflared service install") {
		t.Error("firstbootScript must install the cloudflared connector service")
	}
	if !strings.Contains(firstbootScript, "cloudflared-linux-amd64.deb") {
		t.Error("firstbootScript must fetch the official cloudflared package")
	}
	if !strings.Contains(firstbootScript, `cfd_ver="2026.5.2"`) {
		t.Error("firstbootScript must pin a specific cloudflared version, not 'latest'")
	}
	if !strings.Contains(firstbootScript, "sha256sum -c") {
		t.Error("firstbootScript must verify the cloudflared package checksum before install")
	}
}

func TestWatchdogScript_RestartsCloudflared(t *testing.T) {
	if !strings.Contains(watchdogScript, "cloudflared") {
		t.Error("watchdogScript should keep cloudflared running")
	}
}

func TestFirstbootScript_PersistsIPForwarding(t *testing.T) {
	t.Parallel()

	// The bastion is a subnet router (--advertise-routes); the base cloud
	// template ships IP forwarding OFF and tailscale only warns. Persist it so
	// routed SDN traffic works and survives reboots.
	for _, want := range []string{
		"net.ipv4.ip_forward = 1",
		"/etc/sysctl.d/", // persisted drop-in, not merely a runtime sysctl -w
		"sysctl",
	} {
		if !strings.Contains(firstbootScript, want) {
			t.Errorf("firstbootScript must persist IP forwarding for subnet routing; missing %q", want)
		}
	}
}

func TestWatchdogScript_ProbesDatapathNotJustOnline(t *testing.T) {
	t.Parallel()

	// Self.Online reflects only the control-plane/disco view: it can be true
	// while tailscaled's tun datapath is wedged (disco answers, but inbound
	// packets never reach the guest kernel — sshd/ICMP dead). The watchdog must
	// verify the actual datapath with a kernel-level ICMP, not trust Online.
	if !strings.Contains(watchdogScript, "tailscale ping --icmp") {
		t.Errorf("watchdogScript must probe the datapath with a kernel-level ICMP (tailscale ping --icmp), not just Self.Online")
	}
}

func TestWatchdogScript_RestartsDaemonOnWedge(t *testing.T) {
	t.Parallel()

	// Re-running `tailscale up` does NOT rebuild a wedged tun device; only a
	// daemon restart recovers the datapath. The watchdog must escalate to a
	// restart when the datapath probe fails.
	if !strings.Contains(watchdogScript, "systemctl restart tailscaled") {
		t.Errorf("watchdogScript must restart tailscaled when the datapath is wedged (tailscale up cannot fix a wedged tun device)")
	}
}

func TestFirstbootScript_InstallsIngressDNAT(t *testing.T) {
	for _, want := range []string{
		`.ingress.origin_ip // ""`,
		"table ip ocfp_ingress",
		"dnat to $ing_origin",
		"masquerade",
	} {
		if !strings.Contains(firstbootScript, want) {
			t.Errorf("firstbootScript missing %q", want)
		}
	}
}

func TestWatchdogScript_ReassertsIngressDNAT(t *testing.T) {
	for _, want := range []string{
		`.ingress.origin_ip // ""`,
		"nft list table ip ocfp_ingress",
	} {
		if !strings.Contains(watchdogScript, want) {
			t.Errorf("watchdogScript missing %q", want)
		}
	}
}
