package provision

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// ErrVersionNotFoundWithPattern returns an error when a version cannot be found using the given regex pattern.
func ErrVersionNotFoundWithPattern(pattern string) error {
	return fmt.Errorf("version not found with pattern: %s", pattern) //nolint:err113 // dynamic error with context
}

// AdvancedToolManager handles advanced binary tool installations with version detection.
type AdvancedToolManager struct {
	config   *config.Config
	provider string
	log      logger.Logger
}

// AdvancedBinaryTool represents an advanced binary tool configuration.
type AdvancedBinaryTool struct {
	Name            string            `yaml:"name"`
	Enabled         bool              `yaml:"enabled"`
	Condition       string            `yaml:"condition"`
	CheckCommand    string            `yaml:"checkCommand"`
	CheckFile       string            `yaml:"checkFile"`
	URL             string            `yaml:"url"`
	VersionURL      string            `yaml:"versionUrl"`
	VersionPattern  string            `yaml:"versionPattern"`
	URLTemplate     string            `yaml:"urlTemplate"`
	FixedVersion    string            `yaml:"version"`
	ArchMap         map[string]string `yaml:"archMap"`
	Dest            string            `yaml:"dest"`
	Mode            uint32            `yaml:"mode"`
	Sudo            bool              `yaml:"sudo"`
	Extract         bool              `yaml:"extract"`
	InstallCommand  string            `yaml:"installCommand"`
	InstallScript   string            `yaml:"installScript"`
	PathAddition    string            `yaml:"pathAddition"`
	Cleanup         string            `yaml:"cleanup"`
	PostInstall     string            `yaml:"postInstall"`
	VerifyCommand   string            `yaml:"verifyCommand"`
	BuildFromSource bool              `yaml:"buildFromSource"`
}

// NewAdvancedToolManager creates a new advanced tool manager.
func NewAdvancedToolManager(provider string, cfg *config.Config) *AdvancedToolManager {
	return &AdvancedToolManager{
		config:   cfg,
		provider: provider,
		log:      logger.Get(),
	}
}

// GetAdvancedBinaryTools returns advanced binary tools configuration.
func (atm *AdvancedToolManager) GetAdvancedBinaryTools() []AdvancedBinaryTool {
	tools := atm.getBaseTool()
	tools = append(tools, atm.getCFEcosystemTools()...)
	tools = append(tools, atm.getBuildTools()...)
	tools = append(tools, atm.getEditorTools()...)

	if atm.config != nil {
		tools = atm.applyConfigOverrides(tools)
	}

	return tools
}

// GenerateAdvancedToolScript generates script for advanced tool installation.
func (atm *AdvancedToolManager) GenerateAdvancedToolScript(_ctx context.Context) string {
	tools := atm.GetAdvancedBinaryTools()
	if len(tools) == 0 {
		return ""
	}

	lines := make([]string, 0, scriptBufferToolsBase+scriptBufferToolsPerItem*len(tools))

	atm.addScriptHeader(&lines)
	atm.addArchitectureDetection(&lines)
	atm.addToolInstallations(&lines, tools)

	return strings.Join(lines, "\n")
}

// GetVersionFromAPI fetches the latest version from a GitHub API URL.
func (atm *AdvancedToolManager) GetVersionFromAPI(ctx context.Context, versionURL, pattern string) (string, error) {
	client := &http.Client{
		Timeout:       httpClientTimeout,
		Transport:     nil,
		CheckRedirect: nil,
		Jar:           nil,
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, versionURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to build request: %w", err)
	}

	resp, err := client.Do(req) //nolint:gosec // URL is from trusted internal config
	if err != nil {
		return "", fmt.Errorf("failed to fetch version info: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	// Try JSON parsing first
	var release struct {
		TagName string `json:"tagName"`
	}

	err = json.Unmarshal(body, &release)
	if err == nil && release.TagName != "" {
		// Clean up version string
		version := strings.TrimPrefix(release.TagName, "v")

		return version, nil
	}

	// Fall back to regex pattern matching
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("invalid version pattern: %w", err)
	}

	matches := re.FindStringSubmatch(string(body))
	if len(matches) < minRegexMatches {
		return "", ErrVersionNotFoundWithPattern(pattern)
	}

	version := strings.TrimPrefix(matches[1], "v")

	return version, nil
}

// getBaseTool returns core utility tools.
func (atm *AdvancedToolManager) getBaseTool() []AdvancedBinaryTool {
	return []AdvancedBinaryTool{
		{
			Name:           "vault",
			Enabled:        false, // installed via brew (hashicorp/tap)
			CheckCommand:   "vault",
			VersionURL:     "https://api.github.com/repos/hashicorp/vault/releases/latest",
			VersionPattern: `"tag_name":\s*"v?([^"]+)"`,
			URLTemplate:    "https://releases.hashicorp.com/vault/${VERSION}/vault_${VERSION}_linux_amd64.zip",
			Extract:        true,
			InstallCommand: "sudo install /tmp/vault /usr/local/bin/vault",
			Cleanup:        "/tmp/vault*",
			VerifyCommand:  "vault version",
		},
		{
			Name:           "safe",
			Enabled:        false, // installed via base binary tools
			CheckCommand:   "safe",
			VersionURL:     "https://api.github.com/repos/cloudfoundry-community/safe/releases/latest",
			VersionPattern: `"tag_name":\s*"v?([^"]+)"`,
			URLTemplate:    "https://github.com/cloudfoundry-community/safe/releases/download/v${VERSION}/safe-${VERSION}-linux-amd64",
			Dest:           "/usr/local/bin/safe",
			Mode:           fileModeExecutable,
			Sudo:           true,
			VerifyCommand:  "safe --version",
		},
		{
			Name:           "spruce",
			Enabled:        true,
			CheckCommand:   "spruce",
			VersionURL:     "https://api.github.com/repos/geofffranks/spruce/releases/latest",
			VersionPattern: `"tag_name":\s*"v?([^"]+)"`,
			URLTemplate:    "https://github.com/geofffranks/spruce/releases/download/v${VERSION}/spruce-linux-amd64",
			Dest:           "/usr/local/bin/spruce",
			Mode:           fileModeExecutable,
			Sudo:           true,
			VerifyCommand:  "spruce --version",
		},
		{
			Name:           "yq",
			Enabled:        false, // installed via brew
			CheckCommand:   "yq",
			VersionURL:     "https://api.github.com/repos/mikefarah/yq/releases/latest",
			VersionPattern: `"tag_name":\s*"v?([^"]+)"`,
			URLTemplate:    "https://github.com/mikefarah/yq/releases/download/v${VERSION}/yq_linux_amd64",
			Dest:           "/usr/local/bin/yq",
			Mode:           fileModeExecutable,
			Sudo:           true,
			VerifyCommand:  "yq --version",
		},
		{
			Name:           "ripgrep",
			Enabled:        false, // installed via brew
			CheckCommand:   "rg",
			VersionURL:     "https://api.github.com/repos/BurntSushi/ripgrep/releases/latest",
			VersionPattern: `"tag_name":\s*"([^"]+)"`,
			URLTemplate:    "https://github.com/BurntSushi/ripgrep/releases/download/${VERSION}/ripgrep-${VERSION}-x86_64-unknown-linux-musl.tar.gz",
			Extract:        true,
			InstallCommand: "sudo install /tmp/ripgrep-*/rg /usr/local/bin/rg",
			Cleanup:        "/tmp/ripgrep*",
			VerifyCommand:  "rg --version",
		},
	}
}

// getCFEcosystemTools returns CloudFoundry ecosystem tools.
// These are installed via GitHub binary downloads because the cloudfoundry brew tap
// ships macOS-only (Mach-O) binaries that do not work on Linux.
func (atm *AdvancedToolManager) getCFEcosystemTools() []AdvancedBinaryTool {
	return []AdvancedBinaryTool{
		{
			Name:           "bosh",
			Enabled:        true,
			CheckCommand:   "bosh",
			VersionURL:     "https://api.github.com/repos/cloudfoundry/bosh-cli/releases/latest",
			VersionPattern: `"tag_name":\s*"v?([^"]+)"`,
			URLTemplate:    "https://github.com/cloudfoundry/bosh-cli/releases/download/v${VERSION}/bosh-cli-${VERSION}-linux-amd64",
			Dest:           "/usr/local/bin/bosh",
			Mode:           fileModeExecutable,
			Sudo:           true,
			VerifyCommand:  "bosh --version",
		},
		{
			Name:           "cf",
			Enabled:        true,
			CheckCommand:   "cf",
			VersionURL:     "https://api.github.com/repos/cloudfoundry/cli/releases/latest",
			VersionPattern: `"tag_name":\s*"v?([^"]+)"`,
			URLTemplate:    "https://github.com/cloudfoundry/cli/releases/download/v${VERSION}/cf8-cli_${VERSION}_linux_x86-64.tgz",
			Extract:        true,
			InstallCommand: "sudo install /tmp/cf8 /usr/local/bin/cf",
			Cleanup:        "/tmp/cf8*",
			VerifyCommand:  "cf version",
		},
		{
			Name:           "credhub",
			Enabled:        true,
			CheckCommand:   "credhub",
			VersionURL:     "https://api.github.com/repos/cloudfoundry/credhub-cli/releases/latest",
			VersionPattern: `"tag_name":\s*"([^"]+)"`,
			URLTemplate:    "https://github.com/cloudfoundry/credhub-cli/releases/download/${VERSION}/credhub-linux-amd64-${VERSION}.tgz",
			Extract:        true,
			InstallCommand: "sudo install /tmp/credhub /usr/local/bin/credhub",
			Cleanup:        "/tmp/credhub*",
			VerifyCommand:  "credhub --version",
		},
		{
			Name:           "uaa",
			Enabled:        true,
			CheckCommand:   "uaa",
			VersionURL:     "https://api.github.com/repos/cloudfoundry/uaa-cli/releases/latest",
			VersionPattern: `"tag_name":\s*"v?([^"]+)"`,
			// Release tags carry a leading "v" (v0.20.0) but the asset filename
			// drops it (uaa-linux-amd64-0.20.0). VERSION is v-stripped, so the
			// path segment needs a literal "v" prefix while the filename does not.
			URLTemplate:   "https://github.com/cloudfoundry/uaa-cli/releases/download/v${VERSION}/uaa-linux-amd64-${VERSION}",
			Dest:          "/usr/local/bin/uaa",
			Mode:          fileModeExecutable,
			Sudo:          true,
			VerifyCommand: "uaa version",
		},
	}
}

// getBuildTools returns development and build tools.
func (atm *AdvancedToolManager) getBuildTools() []AdvancedBinaryTool {
	return []AdvancedBinaryTool{
		{
			Name:           "fly",
			Enabled:        true,
			CheckCommand:   "fly",
			VersionURL:     "https://api.github.com/repos/concourse/concourse/releases/latest",
			VersionPattern: `"tag_name":\s*"v([^"]+)"`,
			URLTemplate:    "https://github.com/concourse/concourse/releases/download/v${VERSION}/fly-${VERSION}-linux-amd64.tgz",
			Extract:        true,
			InstallCommand: "sudo install /tmp/fly /usr/local/bin/fly",
			Cleanup:        "/tmp/fly*",
			VerifyCommand:  "fly --version",
		},
		{
			Name:          "bun",
			Enabled:       true,
			CheckCommand:  "bun",
			InstallScript: "curl -fsSL https://bun.sh/install | bash",
			PathAddition:  "${HOME}/.bun/bin",
			VerifyCommand: "bun --version",
		},
		{
			Name:           "graft",
			Enabled:        false, // Disabled: architecture mapping issue
			CheckCommand:   "graft",
			VersionURL:     "https://api.github.com/repos/wayneeseguin/graft/releases/latest",
			VersionPattern: `"tag_name":\s*"([^"]+)"`,
			URLTemplate:    "https://github.com/wayneeseguin/graft/releases/download/${VERSION}/graft-${ARCH}",
			ArchMap: map[string]string{
				"x86_64":  "linux-amd64",
				"aarch64": "linux-arm64",
				"arm64":   "linux-arm64",
			},
			InstallCommand: "sudo install /tmp/graft /usr/local/bin/graft",
			Cleanup:        "/tmp/graft",
			VerifyCommand:  "graft --version",
		},
		{
			Name:          "nvm",
			Enabled:       true,
			CheckFile:     "${HOME}/.nvm/nvm.sh",
			InstallScript: "curl -o- https://raw.githubusercontent.com/nvm-sh/nvm/v0.39.0/install.sh | bash",
			PathAddition:  "${NVM_DIR:-${HOME}/.nvm}",
			PostInstall:   "install_nodejs_latest",
		},
	}
}

// getEditorTools returns text editors and development environments.
func (atm *AdvancedToolManager) getEditorTools() []AdvancedBinaryTool {
	return []AdvancedBinaryTool{
		{
			Name:           "nvim",
			Enabled:        false, // Disabled: installed via APT package instead
			CheckCommand:   "nvim",
			VersionURL:     "https://api.github.com/repos/neovim/neovim/releases/latest",
			VersionPattern: `"tag_name":\s*"v?([^"]+)"`,
			URLTemplate:    "https://github.com/neovim/neovim/releases/download/v${VERSION}/nvim-linux-${ARCH}.appimage",
			ArchMap: map[string]string{
				"x86_64":  "x86_64",
				"aarch64": "arm64",
				"arm64":   "arm64",
			},
			Dest:          "/usr/local/bin/nvim",
			Mode:          fileModeExecutable,
			Sudo:          true,
			Extract:       false,
			VerifyCommand: "nvim --version",
		},
	}
}

// applyConfigOverrides applies configuration overrides to tools.
func (atm *AdvancedToolManager) applyConfigOverrides(tools []AdvancedBinaryTool) []AdvancedBinaryTool {
	enable := make(map[string]struct{})
	for _, n := range atm.config.Bastion.Tools.Enable {
		enable[strings.ToLower(n)] = struct{}{}
	}

	disable := make(map[string]struct{})
	for _, n := range atm.config.Bastion.Tools.Disable {
		disable[strings.ToLower(n)] = struct{}{}
	}

	if len(enable) > 0 || len(disable) > 0 {
		atm.applyEnableDisableRules(tools, enable, disable)
	}

	return atm.applyToolOverrides(tools)
}

// applyEnableDisableRules applies enable/disable rules to tools.
func (atm *AdvancedToolManager) applyEnableDisableRules(tools []AdvancedBinaryTool, enable, disable map[string]struct{}) {
	for index := range tools {
		name := strings.ToLower(tools[index].Name)
		if _, ok := enable[name]; ok {
			tools[index].Enabled = true
		}

		if _, ok := disable[name]; ok {
			tools[index].Enabled = false
		}
	}
}

// applyToolOverrides applies per-tool overrides from config by name.
func (atm *AdvancedToolManager) applyToolOverrides(tools []AdvancedBinaryTool) []AdvancedBinaryTool {
	if !atm.hasToolOverrides() {
		return tools
	}

	for toolIndex := range tools {
		override := atm.findToolOverride(tools[toolIndex].Name)
		if override != nil {
			atm.applyOverrideToTool(&tools[toolIndex], override)
		}
	}

	return tools
}

// addScriptHeader adds the script header.
func (atm *AdvancedToolManager) addScriptHeader(lines *[]string) {
	*lines = append(*lines, "# Advanced binary tool installation")
	*lines = append(*lines, "")
	// Set up GitHub auth header to avoid API rate limiting (60 req/hr unauthenticated)
	*lines = append(*lines, "# Use GitHub token if available to avoid rate limiting")
	*lines = append(*lines, `GITHUB_AUTH_HEADER=""`)
	*lines = append(*lines, `if [ -n "${GITHUB_TOKEN:-}" ]; then`)
	*lines = append(*lines, `    GITHUB_AUTH_HEADER="Authorization: token ${GITHUB_TOKEN}"`)
	*lines = append(*lines, "fi")
	*lines = append(*lines, "")
}

// addArchitectureDetection adds architecture detection helper.
func (atm *AdvancedToolManager) addArchitectureDetection(lines *[]string) {
	*lines = append(*lines, "# Architecture detection")
	*lines = append(*lines, "ARCH=$(uname -m)")
	*lines = append(*lines, "case $ARCH in")
	*lines = append(*lines, "    x86_64) ARCH_NORMALIZED=\"x86_64\" ;;")
	*lines = append(*lines, "    aarch64) ARCH_NORMALIZED=\"aarch64\" ;;")
	*lines = append(*lines, "    arm64) ARCH_NORMALIZED=\"arm64\" ;;")
	*lines = append(*lines, "    *) ARCH_NORMALIZED=\"$ARCH\" ;;")
	*lines = append(*lines, "esac")
	*lines = append(*lines, "")
}

// addToolInstallations adds all tool installations.
func (atm *AdvancedToolManager) addToolInstallations(lines *[]string, tools []AdvancedBinaryTool) {
	for _, tool := range tools {
		if atm.shouldSkipTool(tool) {
			continue
		}

		atm.addSingleToolInstallation(lines, tool)
	}
}

// shouldSkipTool checks if a tool should be skipped.
func (atm *AdvancedToolManager) shouldSkipTool(tool AdvancedBinaryTool) bool {
	return !tool.Enabled || atm.shouldSkipCondition(tool.Condition)
}

// addSingleToolInstallation adds installation script for a single tool.
func (atm *AdvancedToolManager) addSingleToolInstallation(lines *[]string, tool AdvancedBinaryTool) {
	*lines = append(*lines, "# Install advanced tool: "+tool.Name)

	checkCondition := atm.getInstallCheckCondition(tool)
	atm.addInstallCheckWrapper(lines, checkCondition, tool)
}

// getInstallCheckCondition builds the condition to check if tool is installed.
// For tools with a VerifyCommand, we check that the tool actually runs successfully
// (not just that it exists on PATH). This catches cases like a macOS binary installed
// via brew on Linux — it will be on PATH but fail to execute.
func (atm *AdvancedToolManager) getInstallCheckCondition(tool AdvancedBinaryTool) string {
	if tool.VerifyCommand != "" {
		return tool.VerifyCommand + " >/dev/null 2>&1"
	}

	if tool.CheckCommand != "" {
		return fmt.Sprintf("command -v %s >/dev/null 2>&1", tool.CheckCommand)
	}

	if tool.CheckFile != "" {
		expandedPath := atm.expandVariables(tool.CheckFile)

		return fmt.Sprintf("[ -f \"%s\" ]", expandedPath)
	}

	return ""
}

// addInstallCheckWrapper adds the conditional wrapper for tool installation.
func (atm *AdvancedToolManager) addInstallCheckWrapper(lines *[]string, checkCondition string, tool AdvancedBinaryTool) {
	if checkCondition != "" {
		*lines = append(*lines, fmt.Sprintf("if %s; then", checkCondition))
		*lines = append(*lines, fmt.Sprintf("    log_info '%s already installed'", tool.Name))
		*lines = append(*lines, "else")
	}

	atm.addToolInstallationSteps(lines, tool)

	if checkCondition != "" {
		*lines = append(*lines, "fi")
	}

	*lines = append(*lines, "")
}

// addToolInstallationSteps adds the actual installation steps for a tool.
func (atm *AdvancedToolManager) addToolInstallationSteps(lines *[]string, tool AdvancedBinaryTool) {
	*lines = append(*lines, fmt.Sprintf("    log_info 'Installing %s'", tool.Name))

	atm.addBrewUnlink(lines, tool)
	atm.addInstallationMethod(lines, tool)
	atm.addPostInstallSteps(lines, tool)
	atm.addVerificationStep(lines, tool)
	atm.addCleanupStep(lines, tool)
}

// addInstallationMethod adds the appropriate installation method.
func (atm *AdvancedToolManager) addInstallationMethod(lines *[]string, tool AdvancedBinaryTool) {
	switch {
	case tool.InstallScript != "":
		atm.addScriptInstallation(lines, tool)
	case tool.URLTemplate != "" && (tool.VersionURL != "" || tool.FixedVersion != ""):
		*lines = append(*lines, atm.generateVersionBasedInstall(tool)...)
	case tool.URL != "":
		*lines = append(*lines, atm.generateDirectInstall(tool)...)
	}
}

// addScriptInstallation adds script-based installation.
func (atm *AdvancedToolManager) addScriptInstallation(lines *[]string, tool AdvancedBinaryTool) {
	*lines = append(*lines, "    "+tool.InstallScript)

	if tool.PathAddition != "" {
		expandedPath := atm.expandVariables(tool.PathAddition)
		*lines = append(*lines, fmt.Sprintf("    export PATH=\"%s:$PATH\"", expandedPath))
	}
}

// addPostInstallSteps adds post-installation steps.
func (atm *AdvancedToolManager) addPostInstallSteps(lines *[]string, tool AdvancedBinaryTool) {
	if tool.PostInstall != "" {
		*lines = append(*lines, "    # Post-install for "+tool.Name)
		*lines = append(*lines, "    "+atm.getPostInstallScript(tool.PostInstall))
	}
}

// addVerificationStep adds verification step.
func (atm *AdvancedToolManager) addVerificationStep(lines *[]string, tool AdvancedBinaryTool) {
	if tool.VerifyCommand != "" {
		*lines = append(*lines, fmt.Sprintf("    if %s >/dev/null 2>&1; then", tool.VerifyCommand))
		*lines = append(*lines, fmt.Sprintf("        log_success '%s installed and verified'", tool.Name))
		*lines = append(*lines, "    else")
		*lines = append(*lines, fmt.Sprintf("        log_warning '%s installation may have failed verification'", tool.Name))
		*lines = append(*lines, "    fi")
	}
}

// addCleanupStep adds cleanup step.
func (atm *AdvancedToolManager) addCleanupStep(lines *[]string, tool AdvancedBinaryTool) {
	if tool.Cleanup != "" {
		*lines = append(*lines, fmt.Sprintf("    rm -rf %s 2>/dev/null || true", tool.Cleanup))
	}
}

// addBrewUnlink removes a conflicting brew-managed symlink for tools we install
// via binary download. This prevents a broken brew binary (e.g., macOS Mach-O on
// Linux) from shadowing the correctly-installed binary in /usr/local/bin.
func (atm *AdvancedToolManager) addBrewUnlink(lines *[]string, tool AdvancedBinaryTool) {
	if tool.CheckCommand == "" {
		return
	}

	*lines = append(*lines, fmt.Sprintf("    brew unlink %s 2>/dev/null || true", tool.CheckCommand))
}

// generateVersionBasedInstall generates installation commands for version-based tools.
func (atm *AdvancedToolManager) generateVersionBasedInstall(tool AdvancedBinaryTool) []string {
	lines := make([]string, 0, 32) //nolint:mnd // rough capacity for version-based install script

	lines = append(lines, atm.generateVersionDetermination(tool)...)
	lines = append(lines, "")
	lines = append(lines, atm.generateArchitectureMapping(tool)...)
	lines = append(lines, atm.generateDownloadCommands(tool)...)
	lines = append(lines, atm.generateExtractionCommands(tool)...)
	lines = append(lines, atm.generateInstallCommands(tool)...)

	return lines
}

func (atm *AdvancedToolManager) generateVersionDetermination(tool AdvancedBinaryTool) []string {
	var lines []string

	lines = append(lines, "    # Determine version for "+tool.Name)

	if tool.FixedVersion != "" {
		lines = append(lines, fmt.Sprintf("    LATEST_VERSION='%s'", tool.FixedVersion))
		lines = append(lines, "    log_info \"Using configured version: $LATEST_VERSION\"")
	} else {
		// Use jq to parse JSON and extract tag_name, then remove leading 'v'
		// Guard against jq returning "null" (API rate limit or invalid response)
		lines = append(lines, fmt.Sprintf("    LATEST_VERSION=$(curl -sL -H \"${GITHUB_AUTH_HEADER}\" '%s' | jq -r '.tag_name // empty' | sed 's/^v//')", tool.VersionURL))
		lines = append(lines, "    if [ -n \"$LATEST_VERSION\" ] && [ \"$LATEST_VERSION\" != \"null\" ]; then")
		lines = append(lines, "        log_info \"Latest version: $LATEST_VERSION\"")
		lines = append(lines, "    else")
		lines = append(lines, "        LATEST_VERSION=''")
		lines = append(lines, fmt.Sprintf("        log_error 'Failed to get latest version for %s (API may be rate-limited)'", tool.Name))
		lines = append(lines, fmt.Sprintf("        log_warning 'Skipping %s installation'", tool.Name))
		lines = append(lines, "    fi")
	}

	return lines
}

func (atm *AdvancedToolManager) generateArchitectureMapping(tool AdvancedBinaryTool) []string {
	var lines []string

	// Only proceed if version was determined successfully
	lines = append(lines, "    if [ -n \"$LATEST_VERSION\" ]; then")

	if len(tool.ArchMap) > 0 {
		lines = append(lines, "        # Map architecture")
		lines = append(lines, "        MAPPED_ARCH=\"$ARCH_NORMALIZED\"")

		lines = append(lines, "        case $ARCH_NORMALIZED in")
		for arch, mapped := range tool.ArchMap {
			lines = append(lines, fmt.Sprintf("            %s) MAPPED_ARCH=\"%s\" ;;", arch, mapped))
		}

		lines = append(lines, "        esac")
		urlTemplate := strings.ReplaceAll(tool.URLTemplate, "${ARCH}", "${MAPPED_ARCH}")
		lines = append(lines, fmt.Sprintf("        DOWNLOAD_URL='%s'", urlTemplate))
	} else {
		lines = append(lines, fmt.Sprintf("        DOWNLOAD_URL='%s'", tool.URLTemplate))
	}

	lines = append(lines, "        DOWNLOAD_URL=$(echo \"$DOWNLOAD_URL\" | sed \"s/\\${VERSION}/${LATEST_VERSION}/g\")")
	lines = append(lines, "        log_info \"Download URL: $DOWNLOAD_URL\"")
	lines = append(lines, "")

	return lines
}

func (atm *AdvancedToolManager) generateDownloadCommands(tool AdvancedBinaryTool) []string {
	lines := make([]string, 0, 2) //nolint:mnd // download command + success message
	lines = append(lines, fmt.Sprintf("        if curl -fsSL \"$DOWNLOAD_URL\" -o '/tmp/%s-download'; then", tool.Name))
	lines = append(lines, fmt.Sprintf("            log_success '%s downloaded successfully'", tool.Name))

	return lines
}

func (atm *AdvancedToolManager) generateExtractionCommands(tool AdvancedBinaryTool) []string {
	var lines []string

	if tool.Extract {
		// Extract tarballs into an owned scratch dir, then copy the contents
		// into /tmp. Extracting directly with `-C /tmp` fails when an archive
		// carries a `./` root entry: tar tries to chmod /tmp itself (sticky,
		// root-owned) and exits non-zero with "Cannot change mode ...:
		// Operation not permitted". `cp` into /tmp never touches /tmp's mode,
		// so downstream `install /tmp/<name>` paths keep working. unzip already
		// targets a subdir-safe path, so it is left as-is.
		dl := fmt.Sprintf("/tmp/%s-download", tool.Name)
		lines = append(lines, "            # Extract "+tool.Name)
		lines = append(lines, fmt.Sprintf("            if file '%s' | grep -q 'Zip archive'; then", dl))
		lines = append(lines, fmt.Sprintf("                unzip -o -q '%s' -d /tmp/", dl))
		lines = append(lines, fmt.Sprintf("            elif file '%s' | grep -q 'gzip'; then", dl))
		lines = append(lines, extractTarIntoTmp(dl, "-xzf")...)
		lines = append(lines, fmt.Sprintf("            elif file '%s' | grep -q 'bzip2'; then", dl))
		lines = append(lines, extractTarIntoTmp(dl, "-xjf")...)
		lines = append(lines, "            else")
		lines = append(lines, extractTarIntoTmp(dl, "-xf")...)
		lines = append(lines, "            fi")
		lines = append(lines, "")
	}

	return lines
}

// extractTarIntoTmp returns shell lines that extract a tar archive into an
// owned scratch directory and then copy its contents into /tmp. Extracting
// straight into /tmp fails when an archive carries a `./` root entry, because
// tar then tries to chmod /tmp (sticky, root-owned) and exits non-zero. Copying
// into /tmp never alters /tmp's mode, and the /tmp/<file> layout that downstream
// install steps expect is preserved. tarFlag is the tar mode flag (-xzf/-xjf/-xf).
func extractTarIntoTmp(downloadPath, tarFlag string) []string {
	// cp -R (not -a/-p): a plain recursive copy that does not try to preserve
	// timestamps/ownership on the destination, which would fail on /tmp ("cp:
	// preserving times for '/tmp/.': Operation not permitted"). Regular-file
	// mode bits (the binaries' +x) are carried over; install/chmod steps follow.
	return []string{
		"                _ocfp_xd=\"$(mktemp -d)\"",
		fmt.Sprintf("                tar --no-same-owner --no-same-permissions -C \"$_ocfp_xd\" %s '%s'", tarFlag, downloadPath),
		"                cp -R \"$_ocfp_xd\"/. /tmp/",
		"                rm -rf \"$_ocfp_xd\"",
	}
}

func (atm *AdvancedToolManager) generateInstallCommands(tool AdvancedBinaryTool) []string {
	var lines []string

	if tool.InstallCommand != "" {
		lines = append(lines, "            # Install "+tool.Name)
		installCmd := strings.ReplaceAll(tool.InstallCommand, "${VERSION}", "$LATEST_VERSION")
		lines = append(lines, "            "+installCmd)
	} else if !tool.Extract {
		lines = append(lines, atm.generateVersionBasedDirectInstallCommands(tool)...)
	}

	// Close download success block, add failure handling
	lines = append(lines, "        else")
	lines = append(lines, fmt.Sprintf("            log_error 'Failed to download %s from ${DOWNLOAD_URL}'", tool.Name))
	lines = append(lines, "        fi")

	// Close version check block
	lines = append(lines, "    fi")

	return lines
}

// generateVersionBasedDirectInstallCommands generates direct install commands for version-based tools.
func (atm *AdvancedToolManager) generateVersionBasedDirectInstallCommands(tool AdvancedBinaryTool) []string {
	var lines []string

	// Direct binary installation - needs 12 spaces for inside download success block
	installCmd := fmt.Sprintf("mv '/tmp/%s-download' '%s'", tool.Name, tool.Dest)
	if tool.Sudo {
		installCmd = "sudo " + installCmd
	}

	lines = append(lines, "            "+installCmd)

	if tool.Mode != 0 {
		chmodCmd := fmt.Sprintf("chmod %o '%s'", tool.Mode, tool.Dest)
		if tool.Sudo {
			chmodCmd = "sudo " + chmodCmd
		}

		lines = append(lines, "            "+chmodCmd)
	}

	return lines
}

// generateDirectInstall generates installation commands for direct URL tools.
func (atm *AdvancedToolManager) generateDirectInstall(tool AdvancedBinaryTool) []string {
	var lines []string

	lines = append(lines, "    # Direct install "+tool.Name)
	lines = append(lines, fmt.Sprintf("    if curl -fsSL '%s' -o '/tmp/%s'; then", tool.URL, tool.Name))

	// Install
	installCmd := fmt.Sprintf("mv '/tmp/%s' '%s'", tool.Name, tool.Dest)
	if tool.Sudo {
		installCmd = "sudo " + installCmd
	}

	lines = append(lines, "        "+installCmd)

	// Set permissions
	if tool.Mode != 0 {
		chmodCmd := fmt.Sprintf("chmod %o '%s'", tool.Mode, tool.Dest)
		if tool.Sudo {
			chmodCmd = "sudo " + chmodCmd
		}

		lines = append(lines, "        "+chmodCmd)
	}

	lines = append(lines, fmt.Sprintf("        log_success '%s installed successfully'", tool.Name))
	lines = append(lines, "    else")
	lines = append(lines, fmt.Sprintf("        log_error 'Failed to download %s'", tool.Name))
	lines = append(lines, "    fi")

	return lines
}

// getPostInstallScript returns post-install script content.
func (atm *AdvancedToolManager) getPostInstallScript(postInstall string) string {
	switch postInstall {
	case "install_nodejs_latest":
		return `# Install latest Node.js via NVM
if [ -f "$HOME/.nvm/nvm.sh" ]; then
    source "$HOME/.nvm/nvm.sh"
    nvm install node
    nvm use node
    log_success "Latest Node.js installed via NVM"
else
    log_warning "NVM not found, skipping Node.js installation"
fi`
	case "configure_lazyvim":
		return `# Configure LazyVim
if [ -d "$HOME/.config/nvim" ]; then
    log_info "LazyVim configuration already exists"
else
    log_info "Setting up LazyVim configuration"
    git clone git@github.com:LazyVim/starter "$HOME/.config/nvim"
    rm -rf "$HOME/.config/nvim/.git"
    log_success "LazyVim configured"
fi`
	default:
		return "# Post-install: " + postInstall
	}
}

// expandVariables expands environment variables in strings.
func (atm *AdvancedToolManager) expandVariables(text string) string {
	// Simple variable expansion
	text = strings.ReplaceAll(text, "${HOME}", "$HOME")
	text = strings.ReplaceAll(text, "${USER}", "$USER")
	text = strings.ReplaceAll(text, "${NVM_DIR}", "$HOME/.nvm")

	return text
}

// shouldSkipCondition evaluates whether a condition should be skipped.
func (atm *AdvancedToolManager) shouldSkipCondition(condition string) bool {
	if condition == "" {
		return false
	}

	switch condition {
	case condProviderIsStackit:
		return atm.provider != providerStackit
	case condProviderIsAWS:
		return atm.provider != providerAWS
	case condProviderIsAzure:
		return atm.provider != providerAzure
	case condProviderIsGCP:
		return atm.provider != providerGCP
	case condProviderIsOpenstack:
		return atm.provider != providerOpenStack
	case condProviderIsVMware:
		return atm.provider != providerVMware && atm.provider != providerVsphere
	default:
		return false
	}
}

// hasToolOverrides checks if tool overrides are configured.
func (atm *AdvancedToolManager) hasToolOverrides() bool {
	return atm.config != nil && atm.config.Bastion.ToolOverrides != nil
}

// findToolOverride finds the override configuration for a tool name.
func (atm *AdvancedToolManager) findToolOverride(toolName string) *config.ToolOverride {
	name := strings.ToLower(toolName)
	for k, v := range atm.config.Bastion.ToolOverrides {
		if strings.ToLower(k) == name {
			temp := v

			return &temp
		}
	}

	return nil
}

// applyOverrideToTool applies a specific override to a tool.
func (atm *AdvancedToolManager) applyOverrideToTool(tool *AdvancedBinaryTool, override *config.ToolOverride) {
	atm.applyURLOverrides(tool, override)
	atm.applyInstallationOverrides(tool, override)
	atm.applyBehaviorOverrides(tool, override)
}

// applyURLOverrides applies URL-related overrides to a tool.
func (atm *AdvancedToolManager) applyURLOverrides(tool *AdvancedBinaryTool, override *config.ToolOverride) {
	if override.URL != "" {
		tool.URL = override.URL
	}

	if override.VersionURL != "" {
		tool.VersionURL = override.VersionURL
	}

	if override.VersionPattern != "" {
		tool.VersionPattern = override.VersionPattern
	}

	if override.URLTemplate != "" {
		tool.URLTemplate = override.URLTemplate
	}

	if override.Version != "" {
		tool.FixedVersion = override.Version
	}
}

// applyInstallationOverrides applies installation-related overrides to a tool.
func (atm *AdvancedToolManager) applyInstallationOverrides(tool *AdvancedBinaryTool, override *config.ToolOverride) {
	if override.Dest != "" {
		tool.Dest = override.Dest
	}

	if override.Mode != 0 {
		tool.Mode = override.Mode
	}

	if override.Sudo != nil {
		tool.Sudo = *override.Sudo
	}

	if override.Extract != nil {
		tool.Extract = *override.Extract
	}

	if override.InstallCommand != "" {
		tool.InstallCommand = override.InstallCommand
	}

	if override.InstallScript != "" {
		tool.InstallScript = override.InstallScript
	}
}

// applyBehaviorOverrides applies behavior-related overrides to a tool.
func (atm *AdvancedToolManager) applyBehaviorOverrides(tool *AdvancedBinaryTool, override *config.ToolOverride) {
	if override.VerifyCommand != "" {
		tool.VerifyCommand = override.VerifyCommand
	}

	if override.PathAddition != "" {
		tool.PathAddition = override.PathAddition
	}

	if override.Cleanup != "" {
		tool.Cleanup = override.Cleanup
	}
}
