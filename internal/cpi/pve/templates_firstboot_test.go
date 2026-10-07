package pve

import (
	"os"
	"os/exec"
	"path/filepath"
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

func TestWatchdogScript_IngressReinstallBeforeHealthChecks(t *testing.T) {
	t.Parallel()

	ingress := strings.Index(watchdogScript, "ocfp_ingress")
	health := strings.Index(watchdogScript, ".Self.Online")

	if ingress == -1 {
		t.Fatal("watchdogScript must reinstall the ocfp_ingress nft table (lost on reboot)")
	}

	// After a clean reboot tailscale comes back healthy on its own, and the
	// healthy-path exit-0s in the connectivity check skip everything after
	// them — the ingress reinstall must run before that check or a rebooted
	// bastion never gets its DNAT table back.
	if health != -1 && ingress > health {
		t.Error("ocfp_ingress reinstall must come BEFORE the Self.Online health check and its early exits")
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
		"fib daddr type local",
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
		"fib daddr type local",
	} {
		if !strings.Contains(watchdogScript, want) {
			t.Errorf("watchdogScript missing %q", want)
		}
	}
}

// Fake executables for the watchdog ingress harness. Each records its calls
// to ${FAKE_LOG}; nft keeps the installed table text in ${FAKE_NFT_STATE} so
// the script sees what a previous install left behind.
const (
	fakeDmidecode = `#!/bin/bash
case "$2" in
  system-family) echo ocfp-bastion ;;
  system-serial-number) echo tskey-test ;;
  system-sku-number) echo '{"ingress":{"origin_ip":"10.0.0.9","ports":[80,443]}}' ;;
esac
`
	fakeNft = `#!/bin/bash
echo "nft $*" >> "${FAKE_LOG}"
case "$1 $2" in
  "list table")
    [ -f "${FAKE_NFT_STATE}" ] || exit 1
    cat "${FAKE_NFT_STATE}"
    ;;
  "delete table")
    rm -f "${FAKE_NFT_STATE}"
    ;;
  "-f -")
    cat > "${FAKE_NFT_STATE}"
    ;;
esac
`
	fakeLogger = `#!/bin/bash
echo "logger $*" >> "${FAKE_LOG}"
`

	// watchdogConnectivityMarker is where the ingress block ends and the
	// tailscale health checks begin. The harness stops there so it needs no
	// tailscale, systemctl, or tailnet peers.
	watchdogConnectivityMarker = "status=$(tailscale status --json"
)

// runWatchdogIngress runs the ingress block of watchdogScript under /bin/bash
// (3.2 on macOS) with fake dmidecode, nft, and logger on PATH. existing is
// the table text nft already holds, or empty for no table. It returns the
// recorded call log and the table text left behind.
func runWatchdogIngress(t *testing.T, existing string) (calls, table string) {
	t.Helper()

	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not installed")
	}

	head, _, ok := strings.Cut(watchdogScript, watchdogConnectivityMarker)
	if !ok {
		t.Fatalf("watchdogScript has no %q marker", watchdogConnectivityMarker)
	}

	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}

	for name, body := range map[string]string{"dmidecode": fakeDmidecode, "nft": fakeNft, "logger": fakeLogger} {
		writeFakeExecutableFile(t, filepath.Join(bin, name), body)
	}

	script := filepath.Join(dir, "watchdog.sh")
	if err := os.WriteFile(script, []byte(head), 0o600); err != nil {
		t.Fatal(err)
	}

	state := filepath.Join(dir, "nft-state")
	if existing != "" {
		if err := os.WriteFile(state, []byte(existing), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	logFile := filepath.Join(dir, "calls.log")
	cmd := exec.Command("/bin/bash", script)
	cmd.Env = append(os.Environ(),
		"PATH="+bin+":"+os.Getenv("PATH"),
		"FAKE_LOG="+logFile,
		"FAKE_NFT_STATE="+state,
	)

	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("watchdog ingress block failed: %v\n%s", err, out)
	}

	logBytes, _ := os.ReadFile(logFile)
	stateBytes, _ := os.ReadFile(state)

	return string(logBytes), string(stateBytes)
}

const (
	oldUnscopedTable = `table ip ocfp_ingress {
  chain prerouting {
    type nat hook prerouting priority dstnat; policy accept;
    iifname "tailscale0" tcp dport { 80, 443 } dnat to 10.0.0.9
  }
}
`
	scopedTable = `table ip ocfp_ingress {
  chain prerouting {
    type nat hook prerouting priority dstnat; policy accept;
    iifname "tailscale0" fib daddr type local tcp dport { 80, 443 } dnat to 10.0.0.9
  }
}
`
	scopedRule = `iifname "tailscale0" fib daddr type local tcp dport { 80, 443 } dnat to 10.0.0.9`
)

func TestWatchdogIngress_InstallsScopedTableWhenMissing(t *testing.T) {
	t.Parallel()

	calls, table := runWatchdogIngress(t, "")

	if !strings.Contains(table, scopedRule) {
		t.Errorf("installed table lacks scoped rule %q:\n%s", scopedRule, table)
	}

	if !strings.Contains(table, "masquerade") {
		t.Errorf("installed table lost the masquerade rule:\n%s", table)
	}

	if !strings.Contains(calls, "logger -t ocfp-tailscale-watchdog") {
		t.Errorf("missing table install was not logged:\n%s", calls)
	}
}

func TestWatchdogIngress_ReplacesUnscopedTable(t *testing.T) {
	t.Parallel()

	calls, table := runWatchdogIngress(t, oldUnscopedTable)

	if !strings.Contains(calls, "nft delete table ip ocfp_ingress") {
		t.Errorf("unscoped table was not deleted:\n%s", calls)
	}

	if !strings.Contains(table, scopedRule) {
		t.Errorf("replacement table lacks scoped rule %q:\n%s", scopedRule, table)
	}

	if !strings.Contains(calls, "logger -t ocfp-tailscale-watchdog") {
		t.Errorf("replacement was not logged:\n%s", calls)
	}
}

func TestWatchdogIngress_LeavesScopedTableAlone(t *testing.T) {
	t.Parallel()

	calls, table := runWatchdogIngress(t, scopedTable)

	if table != scopedTable {
		t.Errorf("scoped table was modified:\n%s", table)
	}

	for _, bad := range []string{"nft delete", "nft -f", "logger"} {
		if strings.Contains(calls, bad) {
			t.Errorf("scoped table should be left alone, saw %q:\n%s", bad, calls)
		}
	}
}
