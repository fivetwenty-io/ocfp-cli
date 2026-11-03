package guestboot

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/cpi"
)

const (
	ingressOnlyExec      = "ExecStart=/usr/local/sbin/ocfp-tailscale-watchdog --ingress-only"
	ingressExecCondition = "ExecCondition=/bin/grep -q -- --ingress-only /usr/local/sbin/ocfp-tailscale-watchdog"
)

// The nft table lives only in the kernel, so a cold start leaves a bastion with
// no ingress until the watchdog timer first fires. The ingress unit rebuilds it
// early in every boot.
func TestIngressService_RunsTheWatchdogInIngressOnlyMode(t *testing.T) {
	t.Parallel()

	for _, want := range []string{
		"Type=oneshot",
		ingressOnlyExec,
		"After=ocfp-dataset.service ocfp-firstboot.service",
		ingressExecCondition,
		"WantedBy=multi-user.target",
	} {
		if !strings.Contains(IngressService, want) {
			t.Errorf("IngressService is missing %q:\n%s", want, IngressService)
		}
	}
}

// Loading the table needs no network, and the rule matches tailscale0 by name,
// so waiting for the network only delays the unit.
func TestIngressService_DoesNotWaitForTheNetwork(t *testing.T) {
	t.Parallel()

	if strings.Contains(IngressService, "network-online") {
		t.Errorf("IngressService must not order on network-online.target:\n%s", IngressService)
	}
}

// Every writer of the table takes the same lock, and the check happens inside
// it, so concurrent runs cannot append duplicate rules.
func TestIngressWriters_SerializeOnTheSharedLock(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct{ script, first string }{
		"watchdog":        {WatchdogScript, "nft list table ip ocfp_ingress"},
		"cloud watchdog":  {cloudWatchdog(), "nft list table ip ocfp_ingress"},
		"firstboot":       {FirstbootScript, "nft delete table ip ocfp_ingress"},
		"cloud firstboot": {cloudFirstboot(), "nft delete table ip ocfp_ingress"},
	} {
		lock := strings.Index(tc.script, "flock")
		first := strings.Index(tc.script, tc.first)

		if lock < 0 || first < 0 || lock > first {
			t.Errorf("%s: flock (%d) must come before %q (%d)", name, lock, tc.first, first)
		}

		if !strings.Contains(tc.script, IngressLockPath) {
			t.Errorf("%s: does not use the shared lock file %s", name, IngressLockPath)
		}
	}
}

// A unit that reports success with no table hides the failure, so a missing or
// broken jq must fail the ingress-only run.
func TestWatchdogScript_IngressOnlyFailsWhenJQCannotRun(t *testing.T) {
	t.Parallel()

	for name, script := range map[string]string{"pve": WatchdogScript, "cloud": cloudWatchdog()} {
		run := runIngressOnlyWith(t, script, map[string]string{"/var/lib/ocfp/firstboot-done": "no-firstboot-marker"},
			map[string]string{"jq": "#!/bin/bash\nexit 127\n"})

		if run.err == nil {
			t.Errorf("%s: --ingress-only must exit non-zero when jq cannot run\n%s", name, run.out)
		}

		if run.table != "" {
			t.Errorf("%s: no table should be built:\n%s", name, run.table)
		}

		if !strings.Contains(run.calls, "logger") {
			t.Errorf("%s: the failure must be logged\ncalls:\n%s", name, run.calls)
		}
	}
}

// The unit has to carry no nft logic of its own, or it could drift from the
// table the watchdog keeps.
func TestIngressService_CarriesNoIngressLogicOfItsOwn(t *testing.T) {
	t.Parallel()

	for _, bad := range []string{"nft", "ocfp_ingress", "dnat", "tailscale0"} {
		if strings.Contains(IngressService, bad) {
			t.Errorf("IngressService must reuse the watchdog's ingress block, found %q", bad)
		}
	}
}

// There is one ingress block, in the watchdog, and the ingress-only exit comes
// right after it and before any tailscale logic.
func TestWatchdogScript_IngressOnlyExitsAfterIngressBlockBeforeTailscale(t *testing.T) {
	t.Parallel()

	if n := strings.Count(WatchdogScript, IngressNftTable); n != 1 {
		t.Fatalf("WatchdogScript embeds the shared nft table %d times, want 1", n)
	}

	block := strings.Index(WatchdogScript, IngressNftTable)
	exit := strings.Index(WatchdogScript, "\"--ingress-only\" ]]; then\n  exit 0")
	status := strings.Index(WatchdogScript, "tailscale status")

	if exit < 0 {
		t.Fatal("WatchdogScript has no --ingress-only exit")
	}

	if exit < block {
		t.Error("--ingress-only exits before the ingress block runs")
	}

	if status < 0 || exit > status {
		t.Error("--ingress-only must exit before the first tailscale call")
	}

	for _, line := range strings.Split(WatchdogScript[:exit], "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			continue
		}

		for _, tool := range []string{"tailscale ", "systemctl ", "cloudflared "} {
			if strings.Contains(line, tool) {
				t.Errorf("%q runs before the --ingress-only exit: %s", tool, line)
			}
		}
	}
}

// The rule matches the interface by name, so nft loads it before tailscale0
// exists. Nothing in the table or the ingress block waits on the interface.
func TestIngressNftTable_MatchesInterfaceByName(t *testing.T) {
	t.Parallel()

	if !strings.Contains(IngressNftTable, `iifname "tailscale0"`) {
		t.Error("the table must match the interface with iifname, which is a string match")
	}

	if strings.Contains(IngressNftTable, `iif "tailscale0"`) || strings.Contains(IngressNftTable, "iif tailscale0") {
		t.Error("iif resolves the interface index at load time and fails before tailscale0 exists")
	}

	head, _, _ := strings.Cut(WatchdogScript, "\"--ingress-only\" ]]; then\n  exit 0")
	if strings.Contains(head, "ip link") || strings.Contains(head, "wait-online") {
		t.Error("the ingress block must not wait for the tailscale0 interface")
	}
}

type ingressRun struct {
	err   error
	out   string
	calls string
	table string
}

// runIngressOnly runs script with --ingress-only under fake dmidecode, nft,
// logger, systemctl, and tailscale. The tailscale fakes record the call and
// fail, so any reach into the health logic shows in the log.
func runIngressOnly(t *testing.T, script string, mark map[string]string) ingressRun {
	t.Helper()

	return runIngressOnlyWith(t, script, mark, nil)
}

// runIngressOnlyWith is runIngressOnly with extra or replacement fake commands.
func runIngressOnlyWith(t *testing.T, script string, mark, extra map[string]string) ingressRun {
	t.Helper()

	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not installed")
	}

	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")

	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}

	record := "#!/bin/bash\necho \"$(basename \"$0\") $*\" >> \"${FAKE_LOG}\"\n"
	fakes := map[string]string{
		"dmidecode":   "#!/bin/bash\ncase \"$2\" in\n  system-family) echo ocfp-bastion ;;\n  system-sku-number) echo '{\"ingress\":{\"origin_ip\":\"10.0.0.9\",\"ports\":[80,443]}}' ;;\nesac\n",
		"nft":         record + "[ \"$1 $2\" = \"-f -\" ] && cat > \"${FAKE_NFT_STATE}\"\n[ \"$1 $2\" = \"list table\" ] && { [ -f \"${FAKE_NFT_STATE}\" ] && cat \"${FAKE_NFT_STATE}\"; exit; }\nexit 0\n",
		"logger":      record,
		"systemctl":   record + "exit 1\n",
		"tailscale":   record + "exit 1\n",
		"cloudflared": record + "exit 1\n",
		"flock":       record,
	}

	for name, body := range extra {
		fakes[name] = body
	}

	for name, body := range fakes {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	mark = mapsMerge(mark, map[string]string{IngressLockPath: "ingress.lock"})

	for from, to := range mark {
		script = strings.ReplaceAll(script, from, filepath.Join(dir, to))
	}

	path := filepath.Join(dir, "watchdog.sh")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	logFile := filepath.Join(dir, "calls.log")
	state := filepath.Join(dir, "nft-state")
	cmd := exec.Command("/bin/bash", path, "--ingress-only")
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "FAKE_LOG="+logFile, "FAKE_NFT_STATE="+state)
	out, err := cmd.CombinedOutput()
	calls, _ := os.ReadFile(logFile)
	table, _ := os.ReadFile(state)

	return ingressRun{err: err, out: string(out), calls: string(calls), table: string(table)}
}

func mapsMerge(a, b map[string]string) map[string]string {
	out := map[string]string{}

	for k, v := range a {
		out[k] = v
	}

	for k, v := range b {
		out[k] = v
	}

	return out
}

func requireIngressOnlyBehavior(t *testing.T, run ingressRun) {
	t.Helper()

	if run.err != nil {
		t.Fatalf("--ingress-only must exit 0: %v\n%s", run.err, run.out)
	}

	if !strings.Contains(run.table, `iifname "tailscale0" fib daddr type local tcp dport { 80, 443 } dnat to 10.0.0.9`) {
		t.Errorf("the ingress table was not installed:\n%s\ncalls:\n%s", run.table, run.calls)
	}

	if lock, nft := strings.Index(run.calls, "flock"), strings.Index(run.calls, "nft "); lock < 0 || nft < lock {
		t.Errorf("the table must be built under the lock, flock at %d, nft at %d:\n%s", lock, nft, run.calls)
	}

	for _, bad := range []string{"tailscale ", "systemctl ", "cloudflared "} {
		if strings.Contains(run.calls, bad) {
			t.Errorf("--ingress-only reached the health logic, saw %q:\n%s", bad, run.calls)
		}
	}
}

func TestWatchdogScript_IngressOnlyBuildsTableAndSkipsTailscale(t *testing.T) {
	t.Parallel()

	requireIngressOnlyBehavior(t, runIngressOnly(t, WatchdogScript, nil))
}

// The cloud watchdog adds a firstboot retry ahead of the ingress block. An
// ingress-only run at boot must not poke firstboot or bootstrap, or touch the
// connector.
func TestCloudWatchdog_IngressOnlyBuildsTableAndSkipsRetriesAndTailscale(t *testing.T) {
	t.Parallel()

	script := cloudWatchdog()
	script = strings.ReplaceAll(script, "cat /etc/ocfp/guest.json", `echo '{"ingress":{"origin_ip":"10.0.0.9","ports":[80,443]}}'`)

	run := runIngressOnly(t, script, map[string]string{"/var/lib/ocfp/firstboot-done": "no-firstboot-marker"})
	requireIngressOnlyBehavior(t, run)
}

func renderedUserData(t *testing.T, req *cpi.InstanceRequest) string {
	t.Helper()

	data, err := Render("aws", req)
	if err != nil {
		t.Fatal(err)
	}

	raw, err := base64.StdEncoding.DecodeString(strings.Split(data, "'")[1])
	if err != nil {
		t.Fatal(err)
	}

	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}

	body, err := io.ReadAll(gz)
	if err != nil {
		t.Fatal(err)
	}

	return string(body)
}

func TestCloudUserData_InstallsAndEnablesIngressUnit(t *testing.T) {
	t.Parallel()

	for name, req := range map[string]*cpi.InstanceRequest{
		"without data disk": {},
		"with data disk":    {DataDisk: &cpi.DataDiskSpec{Serial: "ocfpdata", Filesystem: "ext4", Mountpoint: "/data", HomeDir: "/home/ubuntu", User: "ubuntu"}},
	} {
		script := renderedUserData(t, req)

		if !strings.Contains(script, "cat > /etc/systemd/system/ocfp-ingress.service") {
			t.Errorf("%s: user-data does not install ocfp-ingress.service", name)
		}

		if !strings.Contains(script, ingressOnlyExec) {
			t.Errorf("%s: installed unit does not run the watchdog in ingress-only mode", name)
		}

		enable := script[strings.Index(script, "systemctl enable ocfp-"):]
		enable, _, _ = strings.Cut(enable, "\n")

		if !strings.Contains(enable, "ocfp-ingress.service") {
			t.Errorf("%s: ocfp-ingress.service is not enabled: %s", name, enable)
		}

		if !strings.Contains(enable, "ocfp-tailscale-watchdog.timer") {
			t.Errorf("%s: the watchdog timer must stay enabled: %s", name, enable)
		}
	}
}

// A data disk reaches the ingress unit through the same drop-in the watchdog
// gets, so the scripts see the mounted home and the same jq.
func TestCloudUserData_IngressUnitWaitsForDataset(t *testing.T) {
	t.Parallel()

	script := renderedUserData(t, &cpi.InstanceRequest{DataDisk: &cpi.DataDiskSpec{Serial: "ocfpdata", Filesystem: "ext4", Mountpoint: "/data", HomeDir: "/home/ubuntu", User: "ubuntu"}})

	if !strings.Contains(script, "/etc/systemd/system/ocfp-ingress.service.d/ocfp-dataset.conf") {
		t.Error("the ingress unit gets no dataset drop-in on a bastion with a data disk")
	}
}

func TestWatchdogTimer_IsUnchanged(t *testing.T) {
	t.Parallel()

	for _, want := range []string{"OnBootSec=2min", "OnUnitActiveSec=5min", "Persistent=true"} {
		if !strings.Contains(WatchdogTimer, want) {
			t.Errorf("the watchdog timer lost %q", want)
		}
	}
}
