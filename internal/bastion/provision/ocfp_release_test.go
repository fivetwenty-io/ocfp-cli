package provision

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateOCFPReleaseInstallScript_PinnedVersion(t *testing.T) {
	t.Parallel()

	script := GenerateOCFPReleaseInstallScript("0.1.0", false)

	for _, want := range []string{
		`OCFP_VERSION='0.1.0'`,
		"https://github.com/fivetwenty-io/ocfp-cli/releases/download/v${OCFP_VERSION}/",
		"ocfp_${OCFP_VERSION}_linux_${OCFP_ARCH}.tar.gz",
		"ocfp_${OCFP_VERSION}_SHA256SUMS",
		"sha256sum -c",
		"/usr/local/bin/ocfp",
		"x86_64) OCFP_ARCH=\"amd64\"",
		"aarch64) OCFP_ARCH=\"arm64\"",
		"arm64) OCFP_ARCH=\"arm64\"",
		`trap 'rm -rf "${OCFP_TMP}"' EXIT`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script is missing %q\n%s", want, script)
		}
	}

	if strings.Contains(script, "api.github.com") {
		t.Error("a pinned version must not consult the GitHub API")
	}
}

func TestGenerateOCFPReleaseInstallScript_LatestUsesAPIWithoutJQ(t *testing.T) {
	t.Parallel()

	script := GenerateOCFPReleaseInstallScript("", false)

	for _, want := range []string{
		"https://api.github.com/repos/fivetwenty-io/ocfp-cli/releases/latest",
		`GITHUB_AUTH_HEADER="Authorization: token ${GITHUB_TOKEN}"`,
		`"tag_name"`,
		"retrying anonymously",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script is missing %q\n%s", want, script)
		}
	}

	if strings.Contains(script, "jq ") {
		t.Error("the latest lookup must not depend on jq, which a fresh bastion does not have yet")
	}
}

func TestGenerateOCFPReleaseInstallScript_SkipsWhenInstalledVersionMatches(t *testing.T) {
	t.Parallel()

	script := GenerateOCFPReleaseInstallScript("0.1.0", false)

	if !strings.Contains(script, `"OCFP CLI v${OCFP_VERSION} "`) {
		t.Errorf("script should compare the installed `ocfp version` banner before downloading\n%s", script)
	}
}

func TestGenerateOCFPReleaseInstallScript_ForceSkipsTheVersionGuard(t *testing.T) {
	t.Parallel()

	script := GenerateOCFPReleaseInstallScript("0.1.0", true)

	if strings.Contains(script, `"OCFP CLI v${OCFP_VERSION} "`) {
		t.Errorf("force must reinstall even when the banner matches\n%s", script)
	}

	if !strings.Contains(script, "Forcing reinstall") {
		t.Errorf("force should say so in the log\n%s", script)
	}
}

func TestGenerateOCFPReleaseInstallScript_RejectsUnknownArch(t *testing.T) {
	t.Parallel()

	script := GenerateOCFPReleaseInstallScript("0.1.0", false)

	if !strings.Contains(script, "unsupported architecture") {
		t.Errorf("script should fail loudly on an architecture without a release asset\n%s", script)
	}
}

func TestGenerateOCFPCompletionsScript_InstallsAllThreeShells(t *testing.T) {
	t.Parallel()

	script := GenerateOCFPCompletionsScript()

	for _, want := range []string{
		"completion bash | sudo tee /usr/share/bash-completion/completions/ocfp",
		"completion zsh | sudo tee /usr/share/zsh/site-functions/_ocfp",
		"completion fish | sudo tee /usr/share/fish/vendor_completions.d/ocfp.fish",
		"sudo mkdir -p",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script is missing %q\n%s", want, script)
		}
	}
}

// TestOCFPReleaseScripts_ParseAsBash catches quoting mistakes the substring
// tests cannot. It wraps each fragment in the same strict-mode preamble the
// phase uses and asks bash to parse it.
func TestOCFPReleaseScripts_ParseAsBash(t *testing.T) {
	t.Parallel()

	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}

	scripts := map[string]string{
		"pinned":      GenerateOCFPReleaseInstallScript("0.1.0", false),
		"latest":      GenerateOCFPReleaseInstallScript("", false),
		"forced":      GenerateOCFPReleaseInstallScript("0.1.0", true),
		"completions": GenerateOCFPCompletionsScript(),
	}

	for name, body := range scripts {
		path := filepath.Join(t.TempDir(), name+".sh")

		content := "#!/bin/bash\nset -euo pipefail\nlog_info() { :; }; log_success() { :; }; log_warning() { :; }; log_error() { :; }\n" + body + "\n"
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("%s: write: %v", name, err)
		}

		out, err := exec.Command(bash, "-n", path).CombinedOutput() // #nosec G204 -- bash from LookPath, path from t.TempDir
		if err != nil {
			t.Errorf("%s: bash -n failed: %v\n%s\n%s", name, err, out, content)
		}
	}
}

// TestOCFPReleaseScript_MatchesGoreleaserNaming couples the asset names the
// bastion downloads to the templates goreleaser publishes, so a rename in
// .goreleaser.yaml fails here instead of on every bastion.
func TestOCFPReleaseScript_MatchesGoreleaserNaming(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "..", "..", ".goreleaser.yaml"))
	if err != nil {
		t.Skipf("goreleaser config not available: %v", err)
	}

	cfg := string(raw)

	for _, want := range []string{
		"{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}",
		"{{ .ProjectName }}_{{ .Version }}_SHA256SUMS",
		"owner: fivetwenty-io",
		"name: ocfp-cli",
		"project_name: ocfp",
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf(".goreleaser.yaml no longer contains %q; update ocfp_release.go to match the new naming", want)
		}
	}
}
