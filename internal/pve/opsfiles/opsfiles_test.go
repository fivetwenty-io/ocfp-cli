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
		if perm := info.Mode().Perm(); perm != 0644 {
			t.Errorf("%s: file mode %04o, want 0644", tc.file, perm)
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

// TestPVEGuestAgentTemplate_IncludesBothStemcells — runtime-config must target
// both ubuntu-jammy and ubuntu-noble stemcell families.
func TestPVEGuestAgentTemplate_IncludesBothStemcells(t *testing.T) {
	t.Parallel()

	for _, os := range []string{"ubuntu-jammy", "ubuntu-noble"} {
		if !strings.Contains(opsfiles.PVEGuestAgentRuntimeConfig, os) {
			t.Errorf("PVEGuestAgentRuntimeConfig: missing stemcell %q in include block", os)
		}
	}
}

// TestWriteRuntimeConfigToDir_CreatesFile — T04: WriteRuntimeConfigToDir writes
// pve-guest-agent.yml to the target dir with correct content and mode 0644.
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
	if perm := info.Mode().Perm(); perm != 0644 {
		t.Errorf("file mode %04o, want 0644", perm)
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
