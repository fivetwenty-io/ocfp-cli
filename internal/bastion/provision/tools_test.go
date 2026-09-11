package provision

import (
	"context"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
)

func TestAdvancedTools_SafeDisabledByDefault(t *testing.T) {
	cfg := &config.Config{
		Bastion: config.Bastion{
			Tools: config.OverrideSets{},
		},
	}

	atm := NewAdvancedToolManager("aws", cfg)
	tools := atm.GetAdvancedBinaryTools()

	for _, tool := range tools {
		if tool.Name == "safe" && tool.Enabled {
			t.Error("Expected 'safe' to be disabled in advanced tools (installed via base binary tools)")
		}
	}
}

func TestAdvancedTools_SpruceOrigEnabledByDefault(t *testing.T) {
	cfg := &config.Config{
		Bastion: config.Bastion{
			Tools: config.OverrideSets{},
		},
	}

	atm := NewAdvancedToolManager("aws", cfg)
	tools := atm.GetAdvancedBinaryTools()

	found := false

	for _, tool := range tools {
		if tool.Name == "spruce-orig" {
			found = true

			if !tool.Enabled {
				t.Error("Expected 'spruce-orig' to be enabled in advanced tools")
			}
		}
	}

	if !found {
		t.Error("Expected 'spruce-orig' in advanced binary tools list")
	}
}

func TestAdvancedTools_NoDuplicatesWithBaseTools(t *testing.T) {
	cfg := &config.Config{
		Bastion: config.Bastion{
			Tools: config.OverrideSets{},
		},
	}

	atm := NewAdvancedToolManager("aws", cfg)
	advTools := atm.GetAdvancedBinaryTools()

	provCfg := NewConfig("aws", cfg, nil)
	baseTools := provCfg.GetBinaryTools()

	// Collect enabled tool names from both lists
	enabledBase := make(map[string]bool)
	for _, t := range baseTools {
		if t.Enabled {
			enabledBase[t.Name] = true
		}
	}

	for _, tool := range advTools {
		if tool.Enabled && enabledBase[tool.Name] {
			t.Errorf("Tool '%s' is enabled in both base and advanced tools — will be installed twice", tool.Name)
		}
	}
}

func TestAdvancedTools_VersionScript_RejectsNullVersion(t *testing.T) {
	cfg := &config.Config{
		Bastion: config.Bastion{
			Tools: config.OverrideSets{},
		},
	}

	atm := NewAdvancedToolManager("aws", cfg)
	script := atm.GenerateAdvancedToolScript(context.Background())

	// Verify the script uses jq's // empty to prevent "null" output
	if !strings.Contains(script, "// empty") {
		t.Error("Expected version detection script to use jq's '// empty' to guard against null values")
	}

	// Verify the script checks for "null" string as a fallback
	if !strings.Contains(script, `!= "null"`) {
		t.Error("Expected version detection script to reject 'null' string version")
	}

	// Verify -n (preferred) instead of ! -z
	if strings.Contains(script, `! -z "$LATEST_VERSION"`) {
		t.Error("Expected version check to use '[ -n' instead of '[ ! -z'")
	}
}

func TestAdvancedTools_VersionScript_ClearsVersionOnFailure(t *testing.T) {
	cfg := &config.Config{
		Bastion: config.Bastion{
			Tools: config.OverrideSets{},
		},
	}

	atm := NewAdvancedToolManager("aws", cfg)
	script := atm.GenerateAdvancedToolScript(context.Background())

	// When version fetch fails, LATEST_VERSION should be cleared
	// so the download block is skipped
	if !strings.Contains(script, "LATEST_VERSION=''") {
		t.Error("Expected script to clear LATEST_VERSION on version fetch failure")
	}
}

// findAdvancedTool returns the named tool from the advanced list, failing the
// test when it is absent.
func findAdvancedTool(t *testing.T, tools []AdvancedBinaryTool, name string) AdvancedBinaryTool {
	t.Helper()

	for _, tool := range tools {
		if tool.Name == name {
			return tool
		}
	}

	t.Fatalf("expected %q in the advanced binary tool list", name)

	return AdvancedBinaryTool{}
}

func TestAdvancedTools_ShieldEnabledAndPinned(t *testing.T) {
	cfg := &config.Config{
		Bastion: config.Bastion{
			Tools: config.OverrideSets{},
		},
	}

	atm := NewAdvancedToolManager("aws", cfg)
	shield := findAdvancedTool(t, atm.GetAdvancedBinaryTools(), "shield")

	if !shield.Enabled {
		t.Error("expected shield to be enabled: the Homebrew formula is macOS-only, so the bastion needs the release binary")
	}

	if shield.FixedVersion != "9.0.2" {
		t.Errorf("expected shield pinned to 9.0.2, got %q", shield.FixedVersion)
	}

	if shield.VersionURL != "" {
		t.Errorf("expected no version lookup for a pinned tool, got %q", shield.VersionURL)
	}

	wantURL := "https://github.com/shieldproject/shield/releases/download/v${VERSION}/shield-linux-amd64"
	if shield.URLTemplate != wantURL {
		t.Errorf("expected url template %q, got %q", wantURL, shield.URLTemplate)
	}

	if shield.Dest != "/usr/local/bin/shield" {
		t.Errorf("expected shield installed at /usr/local/bin/shield, got %q", shield.Dest)
	}

	if shield.Extract {
		t.Error("expected shield-linux-amd64 to be treated as a bare binary, not an archive")
	}

	if !shield.Sudo {
		t.Error("expected shield install to use sudo: /usr/local/bin is root-owned")
	}

	if shield.Mode != fileModeExecutable {
		t.Errorf("expected mode %o, got %o", fileModeExecutable, shield.Mode)
	}

	if shield.VerifyCommand != "shield --version" {
		t.Errorf("expected verification via 'shield --version', got %q", shield.VerifyCommand)
	}
}

func TestAdvancedTools_ShieldScriptUsesPinnedRelease(t *testing.T) {
	cfg := &config.Config{
		Bastion: config.Bastion{
			Tools: config.OverrideSets{},
		},
	}

	atm := NewAdvancedToolManager("aws", cfg)
	script := atm.GenerateAdvancedToolScript(context.Background())

	if !strings.Contains(script, "LATEST_VERSION='9.0.2'") {
		t.Error("expected the script to set the pinned shield version directly")
	}

	if !strings.Contains(script, "https://github.com/shieldproject/shield/releases/download/v${VERSION}/shield-linux-amd64") {
		t.Error("expected the script to carry the shield release URL template")
	}

	if !strings.Contains(script, "sudo mv '/tmp/shield-download' '/usr/local/bin/shield'") {
		t.Error("expected the script to install the downloaded shield binary into /usr/local/bin")
	}

	if strings.Contains(script, "api.github.com/repos/shieldproject/shield") {
		t.Error("expected a pinned tool to skip the GitHub releases API entirely")
	}
}

func TestAdvancedTools_ShieldVersionOverrideApplies(t *testing.T) {
	cfg := &config.Config{
		Bastion: config.Bastion{
			Tools: config.OverrideSets{},
			ToolOverrides: map[string]config.ToolOverride{
				"shield": {Version: "9.1.0"},
			},
		},
	}

	atm := NewAdvancedToolManager("aws", cfg)
	shield := findAdvancedTool(t, atm.GetAdvancedBinaryTools(), "shield")

	if shield.FixedVersion != "9.1.0" {
		t.Errorf("expected the config override to repin shield to 9.1.0, got %q", shield.FixedVersion)
	}
}
