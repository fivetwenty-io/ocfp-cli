package cfgloader_test

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/tests/integration/cfgloader"
)

// testdataPath returns the absolute path of a file under testdata/ adjacent to
// this test file. Uses runtime.Caller so tests work regardless of invocation cwd.
func testdataPath(name string) string {
	_, thisFile, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(thisFile), "testdata", name)
}

// noopBoshInt is a BoshInt stub that always succeeds with an empty string.
// Used for Load tests where boshInt is not invoked.
var noopBoshInt cfgloader.BoshInt = func(_, _ string) (string, error) {
	return "", nil
}

// T60: missing top-level key returns error mentioning the missing key.
func TestLoad_MissingTopKey_ReturnsError(t *testing.T) {
	t.Parallel()
	path := testdataPath("missing_top_key.yml")
	_, err := cfgloader.Load(path, noopBoshInt)
	if err == nil {
		t.Fatal("expected error for missing top key, got nil")
	}
	if !strings.Contains(err.Error(), "tiers") {
		t.Errorf("error does not mention missing key %q: %s", "tiers", err.Error())
	}
}

// T61: missing tier1 key returns error mentioning the tier1-prefixed key.
func TestLoad_MissingTier1Key_ReturnsError(t *testing.T) {
	t.Parallel()
	path := testdataPath("missing_tier1_key.yml")
	_, err := cfgloader.Load(path, noopBoshInt)
	if err == nil {
		t.Fatal("expected error for missing tier1 key, got nil")
	}
	if !strings.Contains(err.Error(), "tier1.vmid_range_start") {
		t.Errorf("error does not mention %q: %s", "tier1.vmid_range_start", err.Error())
	}
}

// T62: Required*Keys functions return stable, non-empty slices in correct order.
func TestRequiredKeys_StableOrder(t *testing.T) {
	t.Parallel()

	topKeys := cfgloader.RequiredTopKeys()
	if len(topKeys) == 0 {
		t.Fatal("RequiredTopKeys returned empty slice")
	}

	// Stable across two calls.
	topKeys2 := cfgloader.RequiredTopKeys()
	if len(topKeys) != len(topKeys2) {
		t.Fatalf("RequiredTopKeys returned different lengths on successive calls: %d vs %d",
			len(topKeys), len(topKeys2))
	}
	for i := range topKeys {
		if topKeys[i] != topKeys2[i] {
			t.Errorf("RequiredTopKeys[%d] changed between calls: %q vs %q",
				i, topKeys[i], topKeys2[i])
		}
	}

	// "tiers" must be first, "tier3" must be last per Python _REQUIRED_TOP.
	if topKeys[0] != "tiers" {
		t.Errorf("RequiredTopKeys[0] = %q, want %q", topKeys[0], "tiers")
	}
	if topKeys[len(topKeys)-1] != "tier3" {
		t.Errorf("RequiredTopKeys[last] = %q, want %q", topKeys[len(topKeys)-1], "tier3")
	}

	tier1Keys := cfgloader.RequiredTier1Keys()
	if len(tier1Keys) == 0 {
		t.Fatal("RequiredTier1Keys returned empty slice")
	}
	// First key must be vmid_range_start per Python _REQUIRED_TIER1.
	if tier1Keys[0] != "vmid_range_start" {
		t.Errorf("RequiredTier1Keys[0] = %q, want %q", tier1Keys[0], "vmid_range_start")
	}
	// Last key must be disk_size_mib.
	if tier1Keys[len(tier1Keys)-1] != "disk_size_mib" {
		t.Errorf("RequiredTier1Keys[last] = %q, want %q", tier1Keys[len(tier1Keys)-1], "disk_size_mib")
	}

	tier2Keys := cfgloader.RequiredTier2Keys()
	if len(tier2Keys) == 0 {
		t.Fatal("RequiredTier2Keys returned empty slice")
	}
	if tier2Keys[0] != "bosh_env_alias" {
		t.Errorf("RequiredTier2Keys[0] = %q, want %q", tier2Keys[0], "bosh_env_alias")
	}

	// tier3 has no required keys — slice may be empty; verify it doesn't panic.
	_ = cfgloader.RequiredTier3Keys()
}

// T63: SynthesizeCPIConfig sets host and port correctly from boshInt mock.
func TestSynthesizeCPIConfig_HostAndPort(t *testing.T) {
	t.Parallel()

	path := testdataPath("valid.yml")
	cfg, err := cfgloader.Load(path, noopBoshInt)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// Returns "10.0.0.1" for /pve_host, "8006" for /pve_port, "" for all others.
	mockBoshInt := cfgloader.BoshInt(func(file, jsonPath string) (string, error) {
		switch jsonPath {
		case "/pve_host":
			return "10.0.0.1", nil
		case "/pve_port":
			return "8006", nil
		default:
			return "", nil
		}
	})

	cpi, err := cfgloader.SynthesizeCPIConfig(cfg, mockBoshInt)
	if err != nil {
		t.Fatalf("SynthesizeCPIConfig failed: %v", err)
	}

	host, ok := cpi["host"]
	if !ok {
		t.Fatal("cpi map missing key \"host\"")
	}
	if host != "10.0.0.1" {
		t.Errorf("cpi[\"host\"] = %q, want %q", host, "10.0.0.1")
	}

	port, ok := cpi["port"]
	if !ok {
		t.Fatal("cpi map missing key \"port\"")
	}
	if port != 8006 {
		t.Errorf("cpi[\"port\"] = %v (%T), want int 8006", port, port)
	}

	// verify_ssl must always be false.
	if cpi["verify_ssl"] != false {
		t.Errorf("cpi[\"verify_ssl\"] = %v, want false", cpi["verify_ssl"])
	}

	// vmid_range_start must come from cfg.Tier1, not boshInt.
	if cpi["vmid_range_start"] != 900 {
		t.Errorf("cpi[\"vmid_range_start\"] = %v, want 900", cpi["vmid_range_start"])
	}
}

// T64: BoshInt injection — verify the injected function receives the correct
// file and path arguments. Pure injection test; no subprocess involved.
func TestBoshInt_Subprocess_CallsBoshCorrectly(t *testing.T) {
	t.Parallel()

	type call struct {
		file string
		path string
	}
	var calls []call

	fakeBoshInt := cfgloader.BoshInt(func(file, jsonPath string) (string, error) {
		calls = append(calls, call{file: file, path: jsonPath})
		// Return dry-run placeholder so auth falls into placeholder branch.
		return "<dry-run:REDACTED>", nil
	})

	path := testdataPath("valid.yml")
	cfg, err := cfgloader.Load(path, noopBoshInt)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if _, err := cfgloader.SynthesizeCPIConfig(cfg, fakeBoshInt); err != nil {
		t.Fatalf("SynthesizeCPIConfig failed: %v", err)
	}

	// Every call must use cfg.BoshVars as the file argument.
	for _, c := range calls {
		if c.file != cfg.BoshVars {
			t.Errorf("boshInt called with file %q, want cfg.BoshVars %q", c.file, cfg.BoshVars)
		}
	}

	// All required paths must have been called.
	requiredPaths := []string{
		"/pve_host", "/pve_port", "/pve_user", "/pve_node",
		"/pve_vm_storage", "/pve_disk_storage", "/pve_stemcell_storage", "/pve_iso_storage",
		"/pve_network_bridge", "/pve_verify_ssl", "/pve_api_token", "/pve_password",
	}
	called := make(map[string]bool, len(calls))
	for _, c := range calls {
		called[c.path] = true
	}
	var missing []string
	for _, rp := range requiredPaths {
		if !called[rp] {
			missing = append(missing, rp)
		}
	}
	if len(missing) > 0 {
		t.Errorf("boshInt not called for required paths: %s", strings.Join(missing, ", "))
	}
}

// TestLoad_ValidFile_ReturnsConfig exercises the happy-path end to end.
func TestLoad_ValidFile_ReturnsConfig(t *testing.T) {
	t.Parallel()

	path := testdataPath("valid.yml")
	cfg, err := cfgloader.Load(path, noopBoshInt)
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}

	if !cfg.Tiers.Lifecycle {
		t.Error("cfg.Tiers.Lifecycle should be true")
	}
	if cfg.BoshVars != "manifests/bosh/vars.yml" {
		t.Errorf("cfg.BoshVars = %q, want %q", cfg.BoshVars, "manifests/bosh/vars.yml")
	}
	if cfg.Tier1.VMIDRangeStart != 900 {
		t.Errorf("cfg.Tier1.VMIDRangeStart = %d, want 900", cfg.Tier1.VMIDRangeStart)
	}
	if cfg.Tier1.VMIDRangeEnd != 999 {
		t.Errorf("cfg.Tier1.VMIDRangeEnd = %d, want 999", cfg.Tier1.VMIDRangeEnd)
	}
	if cfg.Tier2.BoshEnvAlias != "pve" {
		t.Errorf("cfg.Tier2.BoshEnvAlias = %q, want %q", cfg.Tier2.BoshEnvAlias, "pve")
	}
	if cfg.Tier1.NetworkTest.SDN.VNet != "itvnet" {
		t.Errorf("cfg.Tier1.NetworkTest.SDN.VNet = %q, want %q", cfg.Tier1.NetworkTest.SDN.VNet, "itvnet")
	}
	if cfg.Tier1.NetworkTest.Bridge.Iface != "vmbr9" {
		t.Errorf("cfg.Tier1.NetworkTest.Bridge.Iface = %q, want %q", cfg.Tier1.NetworkTest.Bridge.Iface, "vmbr9")
	}
}

// TestLoad_FileNotFound returns an error for a nonexistent path.
func TestLoad_FileNotFound(t *testing.T) {
	t.Parallel()
	_, err := cfgloader.Load("/nonexistent/path/config.yml", noopBoshInt)
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

// TestLoad_NonYAMLFile returns a parse error for non-YAML content.
func TestLoad_NonYAMLFile(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "bad.yml")
	if err := os.WriteFile(p, []byte("{{{{invalid: yaml: [unclosed"), 0o644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}
	_, err := cfgloader.Load(p, noopBoshInt)
	if err == nil {
		t.Fatal("expected error for non-YAML file, got nil")
	}
}

// TestSynthesizeCPIConfig_BoshIntError propagates boshInt errors to caller.
func TestSynthesizeCPIConfig_BoshIntError(t *testing.T) {
	t.Parallel()

	path := testdataPath("valid.yml")
	cfg, err := cfgloader.Load(path, noopBoshInt)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	failBoshInt := cfgloader.BoshInt(func(file, jsonPath string) (string, error) {
		return "", fmt.Errorf("bosh not found")
	})

	_, err = cfgloader.SynthesizeCPIConfig(cfg, failBoshInt)
	if err == nil {
		t.Fatal("expected error when boshInt fails, got nil")
	}
	if !strings.Contains(err.Error(), "bosh int failed") {
		t.Errorf("error missing expected prefix: %s", err.Error())
	}
}

// TestSynthesizeCPIConfig_InvalidPort returns an error when pve_port is non-integer.
func TestSynthesizeCPIConfig_InvalidPort(t *testing.T) {
	t.Parallel()

	path := testdataPath("valid.yml")
	cfg, err := cfgloader.Load(path, noopBoshInt)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	badPortBoshInt := cfgloader.BoshInt(func(file, jsonPath string) (string, error) {
		if jsonPath == "/pve_port" {
			return "not-a-number", nil
		}
		return "", nil
	})

	_, err = cfgloader.SynthesizeCPIConfig(cfg, badPortBoshInt)
	if err == nil {
		t.Fatal("expected error for non-integer pve_port, got nil")
	}
	if !strings.Contains(err.Error(), "not-a-number") {
		t.Errorf("error does not contain bad port value: %s", err.Error())
	}
}

// TestSynthesizeCPIConfig_Tier1BridgeOverride verifies tier1.network_bridge
// takes precedence over pve_network_bridge from vars.
func TestSynthesizeCPIConfig_Tier1BridgeOverride(t *testing.T) {
	t.Parallel()

	path := testdataPath("valid.yml")
	cfg, err := cfgloader.Load(path, noopBoshInt)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	// cfg.Tier1.NetworkBridge == "vmbr0" from valid.yml.

	mockBoshInt := cfgloader.BoshInt(func(file, jsonPath string) (string, error) {
		if jsonPath == "/pve_network_bridge" {
			return "vmbr-from-vars", nil
		}
		return "", nil
	})

	cpi, err := cfgloader.SynthesizeCPIConfig(cfg, mockBoshInt)
	if err != nil {
		t.Fatalf("SynthesizeCPIConfig failed: %v", err)
	}
	if cpi["network_bridge"] != "vmbr0" {
		t.Errorf("cpi[\"network_bridge\"] = %q, want %q (tier1 should override vars)",
			cpi["network_bridge"], "vmbr0")
	}
}

// TestSynthesizeCPIConfig_DefaultPort verifies absent pve_port defaults to 8006.
func TestSynthesizeCPIConfig_DefaultPort(t *testing.T) {
	t.Parallel()

	path := testdataPath("valid.yml")
	cfg, err := cfgloader.Load(path, noopBoshInt)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	emptyPortBoshInt := cfgloader.BoshInt(func(file, jsonPath string) (string, error) {
		return "", nil
	})

	cpi, err := cfgloader.SynthesizeCPIConfig(cfg, emptyPortBoshInt)
	if err != nil {
		t.Fatalf("SynthesizeCPIConfig failed: %v", err)
	}
	if cpi["port"] != 8006 {
		t.Errorf("cpi[\"port\"] = %v, want 8006 (default when pve_port empty)", cpi["port"])
	}
}
