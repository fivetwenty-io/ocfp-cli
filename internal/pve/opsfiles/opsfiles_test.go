package opsfiles_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/pve/opsfiles"
)

// T01 TestEmbed_NatsTuning_NotEmpty — embedded content non-empty and contains
// the key tuning values confirmed in R3-01.
func TestEmbed_NatsTuning_NotEmpty(t *testing.T) {
	t.Parallel()

	if opsfiles.NatsTuning == "" {
		t.Fatal("NatsTuning: embedded content is empty")
	}

	for _, want := range []string{"ping_interval", "30s"} {
		if !strings.Contains(opsfiles.NatsTuning, want) {
			t.Errorf("NatsTuning: expected to contain %q", want)
		}
	}
}

// TestNatsTuning_PingInterval30s — alias for T01c per task-list naming.
func TestNatsTuning_PingInterval30s(t *testing.T) {
	t.Parallel()

	if !strings.Contains(opsfiles.NatsTuning, "ping_interval") {
		t.Error("NatsTuning: missing ping_interval key")
	}
	if !strings.Contains(opsfiles.NatsTuning, "30s") {
		t.Error("NatsTuning: missing 30s value for ping_interval")
	}
	if !strings.Contains(opsfiles.NatsTuning, "poll_user_sync") {
		t.Error("NatsTuning: missing poll_user_sync key")
	}
}

// TestNatsTuning_PingMaxOutstandingThree — director NATS grace must be widened
// from the default (2) to 3, taking the total ping window from ~90s to ~120s.
// HM agent_timeout=180s still tears down truly dead agents, so the wider
// window does not mask real failures.
func TestNatsTuning_PingMaxOutstandingThree(t *testing.T) {
	t.Parallel()

	// Match the actual replace op so we cannot pass on an unrelated literal `3`
	// elsewhere in the file (e.g. a doc example).
	wantBlock := "path: /instance_groups/name=bosh/properties/nats/ping_max_outstanding?\n  value: 3"
	if !strings.Contains(opsfiles.NatsTuning, wantBlock) {
		t.Errorf("NatsTuning: missing ping_max_outstanding=3 replace op; expected substring:\n%s", wantBlock)
	}

	// Guard against regressions back to the upstream default.
	staleBlock := "path: /instance_groups/name=bosh/properties/nats/ping_max_outstanding?\n  value: 2"
	if strings.Contains(opsfiles.NatsTuning, staleBlock) {
		t.Error("NatsTuning: ping_max_outstanding still set to 2; upstream bumped to 3 (commit 5d41a74)")
	}
}

// TestNatsTuning_DocsMention120sGraceAndHMTimeout — the rationale block must
// name the resulting ~120s grace window and call out HM agent_timeout=180s so
// future readers know why the wider NATS window is safe.
func TestNatsTuning_DocsMention120sGraceAndHMTimeout(t *testing.T) {
	t.Parallel()

	for _, want := range []string{"~120 s", "agent_timeout=180s"} {
		if !strings.Contains(opsfiles.NatsTuning, want) {
			t.Errorf("NatsTuning: doc block missing rationale marker %q", want)
		}
	}
}

// T02 TestEmbed_HMTuning_NotEmpty — embedded content non-empty and contains
// the key tuning values confirmed in R3-01.
func TestEmbed_HMTuning_NotEmpty(t *testing.T) {
	t.Parallel()

	if opsfiles.HMTuning == "" {
		t.Fatal("HMTuning: embedded content is empty")
	}

	for _, want := range []string{"resurrector_enabled", "false"} {
		if !strings.Contains(opsfiles.HMTuning, want) {
			t.Errorf("HMTuning: expected to contain %q", want)
		}
	}
}

// TestHMTuning_ResurrectorDisabled — alias for T01d per task-list naming.
func TestHMTuning_ResurrectorDisabled(t *testing.T) {
	t.Parallel()

	if !strings.Contains(opsfiles.HMTuning, "resurrector_enabled") {
		t.Error("HMTuning: missing resurrector_enabled key")
	}
	if !strings.Contains(opsfiles.HMTuning, "false") {
		t.Error("HMTuning: missing false value for resurrector_enabled")
	}
	if !strings.Contains(opsfiles.HMTuning, "agent_timeout") {
		t.Error("HMTuning: missing agent_timeout key")
	}
	if !strings.Contains(opsfiles.HMTuning, "analyze_agents") {
		t.Error("HMTuning: missing analyze_agents key")
	}
}

// T03 TestEmbed_OSConf_NotEmpty — embedded content non-empty and references
// the qemu-guest-agent package confirmed in R3-01.
func TestEmbed_OSConf_NotEmpty(t *testing.T) {
	t.Parallel()

	if opsfiles.OSConf == "" {
		t.Fatal("OSConf: embedded content is empty")
	}

	if !strings.Contains(opsfiles.OSConf, "qemu-guest-agent") {
		t.Error("OSConf: expected to contain \"qemu-guest-agent\"")
	}
}

// TestOSConfTemplate_ContainsExactSHA1 — alias for T01b per task-list naming.
func TestOSConfTemplate_ContainsExactSHA1(t *testing.T) {
	t.Parallel()

	const wantSHA1 = "d20772d8ce6e781ceb13cac7df5950bfa4330ba1"
	if !strings.Contains(opsfiles.OSConf, wantSHA1) {
		t.Errorf("OSConf: expected exact sha1 %q not found", wantSHA1)
	}
}

// TestOSConf_DetachedInstall — director-side QGA install must use the same
// setsid-detached, bounded-timeout pattern as the runtime-config addon so a
// slow apt mirror cannot stall `bosh create-env` pre-start.
func TestOSConf_DetachedInstall(t *testing.T) {
	t.Parallel()

	for _, want := range []string{
		"setsid",
		"timeout 120",
		"timeout 180",
		"command -v qemu-ga",
	} {
		if !strings.Contains(opsfiles.OSConf, want) {
			t.Errorf("OSConf: missing detached-install marker %q", want)
		}
	}
}

// TestOSConf_NoEnableStaticUnit — `systemctl enable` on Noble fails because
// qemu-guest-agent.service is a STATIC unit. The director-side install must
// use `start` only.
func TestOSConf_NoEnableStaticUnit(t *testing.T) {
	t.Parallel()

	if strings.Contains(opsfiles.OSConf, "enable --now qemu-guest-agent") {
		t.Error("OSConf: `enable --now qemu-guest-agent` fails on Noble (STATIC unit); use start only")
	}
	if strings.Contains(opsfiles.OSConf, "systemctl enable qemu-guest-agent") {
		t.Error("OSConf: `systemctl enable qemu-guest-agent` fails on Noble (STATIC unit); use start only")
	}
}

// T04 TestWriteToDeploymentsDir_CreatesFiles_WithCorrectContent — writes all
// three files to a temp dir and verifies presence and content for each.
func TestWriteToDeploymentsDir_CreatesFiles_WithCorrectContent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	if err := opsfiles.WriteToDeploymentsDir(dir); err != nil {
		t.Fatalf("WriteToDeploymentsDir(%q): unexpected error: %v", dir, err)
	}

	cases := []struct {
		file    string
		content string
		probe   string // substring that must appear
	}{
		{"nats-tuning.yml", opsfiles.NatsTuning, "ping_interval"},
		{"hm-tuning.yml", opsfiles.HMTuning, "resurrector_enabled"},
		{"os-conf.yml", opsfiles.OSConf, "qemu-guest-agent"},
	}

	for _, tc := range cases {
		path := filepath.Join(dir, tc.file)

		data, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("ReadFile(%q): %v", path, err)
			continue
		}

		got := string(data)
		if got != tc.content {
			t.Errorf("%s: written content does not match embedded constant (len got=%d want=%d)",
				tc.file, len(got), len(tc.content))
		}

		if !strings.Contains(got, tc.probe) {
			t.Errorf("%s: missing expected substring %q", tc.file, tc.probe)
		}

		info, err := os.Stat(path)
		if err != nil {
			t.Errorf("Stat(%q): %v", path, err)
			continue
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("%s: file mode %04o, want 0600", tc.file, perm)
		}
	}
}

// TestWriteToDeploymentsDir_CreatesDir — verifies MkdirAll runs so callers
// need not pre-create the target directory.
func TestWriteToDeploymentsDir_CreatesDir(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	dir := filepath.Join(base, "deep", "nested", "ops")

	if err := opsfiles.WriteToDeploymentsDir(dir); err != nil {
		t.Fatalf("WriteToDeploymentsDir(%q): %v", dir, err)
	}

	if _, err := os.Stat(dir); err != nil {
		t.Errorf("target dir %q was not created: %v", dir, err)
	}
}

// TestWriteToDeploymentsDir_EmptyDir_ReturnsError — empty string dir must
// produce a descriptive error before any OS call.
func TestWriteToDeploymentsDir_EmptyDir_ReturnsError(t *testing.T) {
	t.Parallel()

	err := opsfiles.WriteToDeploymentsDir("")
	if err == nil {
		t.Fatal("WriteToDeploymentsDir(\"\"): expected error, got nil")
	}
	if !strings.Contains(err.Error(), "must not be empty") {
		t.Errorf("error %q does not mention 'must not be empty'", err.Error())
	}
}

// TestAll_ReturnsThreeEntries — All() must always return exactly three entries
// with the expected keys.
func TestAll_ReturnsThreeEntries(t *testing.T) {
	t.Parallel()

	all := opsfiles.All()
	wantKeys := []string{"nats-tuning.yml", "hm-tuning.yml", "os-conf.yml"}

	if len(all) != len(wantKeys) {
		t.Fatalf("All(): len=%d, want %d", len(all), len(wantKeys))
	}

	for _, k := range wantKeys {
		v, ok := all[k]
		if !ok {
			t.Errorf("All(): missing key %q", k)
			continue
		}
		if v == "" {
			t.Errorf("All()[%q]: empty value", k)
		}
	}
}

// TestEmbed_PVEGuestAgent_NotEmpty — T04b/T04c: PVEGuestAgentRuntimeConfig must
// be non-empty and contain the detached-install markers confirmed in R3-01.
func TestEmbed_PVEGuestAgent_NotEmpty(t *testing.T) {
	t.Parallel()

	if opsfiles.PVEGuestAgentRuntimeConfig == "" {
		t.Fatal("PVEGuestAgentRuntimeConfig: embedded content is empty")
	}

	for _, want := range []string{"qemu-guest-agent", "setsid"} {
		if !strings.Contains(opsfiles.PVEGuestAgentRuntimeConfig, want) {
			t.Errorf("PVEGuestAgentRuntimeConfig: expected to contain %q", want)
		}
	}
}

// TestPVEGuestAgentTemplate_ContainsSetsid — alias for T04b per task-list naming.
func TestPVEGuestAgentTemplate_ContainsSetsid(t *testing.T) {
	t.Parallel()

	if !strings.Contains(opsfiles.PVEGuestAgentRuntimeConfig, "setsid") {
		t.Error("PVEGuestAgentRuntimeConfig: missing setsid detached-install pattern")
	}
}

// TestPVEGuestAgentTemplate_AptTimeout120s — alias for T04c per task-list naming.
func TestPVEGuestAgentTemplate_AptTimeout120s(t *testing.T) {
	t.Parallel()

	if !strings.Contains(opsfiles.PVEGuestAgentRuntimeConfig, "timeout 120") {
		t.Error("PVEGuestAgentRuntimeConfig: missing 'timeout 120' for apt-get update")
	}

	if !strings.Contains(opsfiles.PVEGuestAgentRuntimeConfig, "timeout 180") {
		t.Error("PVEGuestAgentRuntimeConfig: missing 'timeout 180' for apt-get install")
	}
}

// TestPVEGuestAgentTemplate_IncludesNobleStemcell — runtime-config must target
// the ubuntu-noble stemcell family.
func TestPVEGuestAgentTemplate_IncludesNobleStemcell(t *testing.T) {
	t.Parallel()

	if !strings.Contains(opsfiles.PVEGuestAgentRuntimeConfig, "ubuntu-noble") {
		t.Error("PVEGuestAgentRuntimeConfig: missing stemcell \"ubuntu-noble\" in include block")
	}
}

// TestPVEGuestAgentTemplate_IncludesJammyStemcell — addon must also target the
// ubuntu-jammy family so labs can roll between Jammy and Noble without losing
// the QGA install path.
func TestPVEGuestAgentTemplate_IncludesJammyStemcell(t *testing.T) {
	t.Parallel()

	if !strings.Contains(opsfiles.PVEGuestAgentRuntimeConfig, "ubuntu-jammy") {
		t.Error("PVEGuestAgentRuntimeConfig: missing stemcell \"ubuntu-jammy\" in include block")
	}
}

// TestPVEGuestAgentTemplate_InstalledFlagSentinel — the detached install must
// drop /var/vcap/sys/log/pve-guest-agent/installed.flag on success so the
// unstick-agent path can distinguish "install never ran" from "install done,
// agent wedged".
func TestPVEGuestAgentTemplate_InstalledFlagSentinel(t *testing.T) {
	t.Parallel()

	if !strings.Contains(opsfiles.PVEGuestAgentRuntimeConfig, "installed.flag") {
		t.Error("PVEGuestAgentRuntimeConfig: missing installed.flag sentinel drop")
	}
}

// TestPVEGuestAgentTemplate_ActiveWaitLoop — the detached install must briefly
// poll `systemctl is-active --quiet qemu-guest-agent.service` so the log
// records a concrete success state before the background block exits.
func TestPVEGuestAgentTemplate_ActiveWaitLoop(t *testing.T) {
	t.Parallel()

	if !strings.Contains(opsfiles.PVEGuestAgentRuntimeConfig, "is-active --quiet qemu-guest-agent.service") {
		t.Error("PVEGuestAgentRuntimeConfig: missing systemctl is-active --quiet wait loop")
	}
}

// TestWriteRuntimeConfigToDir_CreatesFile — T04: WriteRuntimeConfigToDir writes
// pve-guest-agent.yml to the target dir with correct content and mode 0600.
func TestWriteRuntimeConfigToDir_CreatesFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	if err := opsfiles.WriteRuntimeConfigToDir(dir); err != nil {
		t.Fatalf("WriteRuntimeConfigToDir(%q): unexpected error: %v", dir, err)
	}

	path := filepath.Join(dir, "pve-guest-agent.yml")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}

	got := string(data)
	if got != opsfiles.PVEGuestAgentRuntimeConfig {
		t.Errorf("written content does not match PVEGuestAgentRuntimeConfig (len got=%d want=%d)",
			len(got), len(opsfiles.PVEGuestAgentRuntimeConfig))
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%q): %v", path, err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file mode %04o, want 0600", perm)
	}
}

// TestWriteRuntimeConfigToDir_CreatesDir — verifies MkdirAll so callers need
// not pre-create the target directory.
func TestWriteRuntimeConfigToDir_CreatesDir(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	dir := filepath.Join(base, "deep", "nested", "runtime-configs")

	if err := opsfiles.WriteRuntimeConfigToDir(dir); err != nil {
		t.Fatalf("WriteRuntimeConfigToDir(%q): %v", dir, err)
	}

	if _, err := os.Stat(dir); err != nil {
		t.Errorf("target dir %q was not created: %v", dir, err)
	}
}

// TestWriteRuntimeConfigToDir_EmptyDir_ReturnsError — empty string dir must
// produce a descriptive error before any OS call.
func TestWriteRuntimeConfigToDir_EmptyDir_ReturnsError(t *testing.T) {
	t.Parallel()

	err := opsfiles.WriteRuntimeConfigToDir("")
	if err == nil {
		t.Fatal("WriteRuntimeConfigToDir(\"\"): expected error, got nil")
	}
	if !strings.Contains(err.Error(), "must not be empty") {
		t.Errorf("error %q does not mention 'must not be empty'", err.Error())
	}
}
