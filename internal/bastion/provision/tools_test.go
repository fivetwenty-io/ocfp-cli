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
