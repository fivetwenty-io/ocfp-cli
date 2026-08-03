package provision

import (
	"context"
	"strings"
	"testing"
)

// script renders the CF plugin install script for the default plugin set.
func cfPluginScript(t *testing.T) string {
	t.Helper()

	mgr := NewCFPluginManager("pve", nil)

	return mgr.GenerateCFPluginInstallScript(context.Background())
}

// The `cf plugins` table lists a plugin under its own advertised name, which
// is not the key we track it by: the `targets` plugin reports itself as
// `cf-targets`. Anchoring the existence check on the bare key means the check
// never matches an installed plugin, so every run reinstalls and any manual
// remediation is unreachable.
func TestExistenceCheckMatchesAdvertisedPluginName(t *testing.T) {
	script := cfPluginScript(t)

	if strings.Contains(script, "grep -q '^targets '") {
		t.Errorf("existence check anchors on the bare plugin key; `cf plugins` lists it as cf-targets so this never matches\nscript:\n%s", script)
	}

	if !strings.Contains(script, "cf-targets") {
		t.Errorf("existence check should tolerate the cf- prefixed advertised name\nscript:\n%s", script)
	}
}

// Upstream release assets embed the version and use a `+` separator, e.g.
// cf-targets-plugin-2.3.0+linux.amd64. Constructing an unversioned name
// (cf-targets-plugin-linux.amd64) 404s, which fails the whole phase.
func TestGitHubAssetURLIsNotHardcodedUnversioned(t *testing.T) {
	script := cfPluginScript(t)

	if strings.Contains(script, "cf-targets-plugin-linux.amd64") {
		t.Errorf("script builds an unversioned asset name that 404s against current releases\nscript:\n%s", script)
	}
}

// Rather than guessing the asset filename, the script should resolve the
// download URL from the release metadata so it survives upstream renames.
func TestGitHubAssetURLResolvedFromReleaseMetadata(t *testing.T) {
	script := cfPluginScript(t)

	if !strings.Contains(script, "browser_download_url") {
		t.Errorf("script should select the linux.amd64 asset from release metadata\nscript:\n%s", script)
	}

	if !strings.Contains(script, "linux.amd64") {
		t.Errorf("script should filter release assets to linux.amd64\nscript:\n%s", script)
	}
}

// A plugin that cannot be resolved or downloaded must not abort the phase:
// CF plugins are auxiliary tooling, and the bastion init phases that follow
// (binary_tools, vault_populate, ...) are unrelated to them.
func TestPluginDownloadFailureDoesNotAbortPhase(t *testing.T) {
	script := cfPluginScript(t)

	if !strings.Contains(script, "curl -fsSL \"$PLUGIN_URL\" -o \"/tmp/targets-plugin\" || true") {
		t.Errorf("plugin download should tolerate failure so the phase continues\nscript:\n%s", script)
	}
}
