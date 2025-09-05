package provision

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// AdvancedToolManager handles advanced binary tool installations with version detection
type AdvancedToolManager struct {
	config   *config.Config
	provider string
	log      logger.Logger
}

// AdvancedBinaryTool represents an advanced binary tool configuration
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

// NewAdvancedToolManager creates a new advanced tool manager
func NewAdvancedToolManager(provider string, cfg *config.Config) *AdvancedToolManager {
	return &AdvancedToolManager{
		config:   cfg,
		provider: provider,
		log:      logger.Get(),
	}
}

// GetAdvancedBinaryTools returns advanced binary tools configuration
func (atm *AdvancedToolManager) GetAdvancedBinaryTools() []AdvancedBinaryTool {
	tools := []AdvancedBinaryTool{
		{
			Name:          "yq",
			Enabled:       true,
			CheckCommand:  "yq",
			URL:           "https://github.com/mikefarah/yq/releases/latest/download/yq_linux_amd64",
			Dest:          "/usr/local/bin/yq",
			Mode:          0755,
			Sudo:          true,
			VerifyCommand: "yq --version",
		},
		{
			Name:           "ripgrep",
			Enabled:        true,
			CheckCommand:   "rg",
			VersionURL:     "https://api.github.com/repos/BurntSushi/ripgrep/releases/latest",
			VersionPattern: `"tag_name":\s*"([^"]+)"`,
			URLTemplate:    "https://github.com/BurntSushi/ripgrep/releases/download/${VERSION}/ripgrep-${VERSION}-x86_64-unknown-linux-musl.tar.gz",
			Extract:        true,
			InstallCommand: "sudo install /tmp/ripgrep-*/rg /usr/local/bin/rg",
			Cleanup:        "/tmp/ripgrep*",
			VerifyCommand:  "rg --version",
		},
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
			Enabled:        true,
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
		{
			Name:          "nvim",
			Enabled:       true,
			CheckCommand:  "nvim",
			URL:           "https://github.com/neovim/neovim/releases/latest/download/nvim-linux-x86_64.appimage",
			Dest:          "/usr/local/bin/nvim",
			Mode:          0755,
			Sudo:          true,
			VerifyCommand: "nvim --version",
		},
	}
	// Apply config overrides (enable/disable by name)
	if atm.config != nil {
		enable := make(map[string]struct{})
		for _, n := range atm.config.Bastion.Tools.Enable {
			enable[strings.ToLower(n)] = struct{}{}
		}
		disable := make(map[string]struct{})
		for _, n := range atm.config.Bastion.Tools.Disable {
			disable[strings.ToLower(n)] = struct{}{}
		}
		if len(enable) > 0 || len(disable) > 0 {
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
		// Apply per-tool field overrides
		tools = atm.applyToolOverrides(tools)
	}
	return tools
}

// GenerateAdvancedToolScript generates script for advanced tool installation
func (atm *AdvancedToolManager) GenerateAdvancedToolScript(ctx context.Context) string {
	tools := atm.GetAdvancedBinaryTools()
	if len(tools) == 0 {
		return ""
	}

	var lines []string
	lines = append(lines, "# Advanced binary tool installation")
	lines = append(lines, "")

	// Helper function for architecture detection
	lines = append(lines, "# Architecture detection")
	lines = append(lines, "ARCH=$(uname -m)")
	lines = append(lines, "case $ARCH in")
	lines = append(lines, "    x86_64) ARCH_NORMALIZED=\"x86_64\" ;;")
	lines = append(lines, "    aarch64) ARCH_NORMALIZED=\"aarch64\" ;;")
	lines = append(lines, "    arm64) ARCH_NORMALIZED=\"arm64\" ;;")
	lines = append(lines, "    *) ARCH_NORMALIZED=\"$ARCH\" ;;")
	lines = append(lines, "esac")
	lines = append(lines, "")

	for _, tool := range tools {
		if !tool.Enabled || atm.shouldSkipCondition(tool.Condition) {
			continue
		}

		lines = append(lines, fmt.Sprintf("# Install advanced tool: %s", tool.Name))

		// Check if tool is already installed
		checkCondition := ""
		if tool.CheckCommand != "" {
			checkCondition = fmt.Sprintf("command -v %s >/dev/null 2>&1", tool.CheckCommand)
		} else if tool.CheckFile != "" {
			expandedPath := atm.expandVariables(tool.CheckFile)
			checkCondition = fmt.Sprintf("[ -f \"%s\" ]", expandedPath)
		}

		if checkCondition != "" {
			lines = append(lines, fmt.Sprintf("if %s; then", checkCondition))
			lines = append(lines, fmt.Sprintf("    log_info '%s already installed'", tool.Name))
			lines = append(lines, "else")
		}

		lines = append(lines, fmt.Sprintf("    log_info 'Installing %s'", tool.Name))

		if tool.InstallScript != "" {
			// Script-based installation
			lines = append(lines, fmt.Sprintf("    %s", tool.InstallScript))

			if tool.PathAddition != "" {
				expandedPath := atm.expandVariables(tool.PathAddition)
				lines = append(lines, fmt.Sprintf("    export PATH=\"%s:$PATH\"", expandedPath))
			}
		} else if tool.URLTemplate != "" && (tool.VersionURL != "" || tool.FixedVersion != "") {
			// Version-based installation (supports fixed version override)
			lines = append(lines, atm.generateVersionBasedInstall(tool)...)
		} else if tool.URL != "" {
			// Direct URL installation
			lines = append(lines, atm.generateDirectInstall(tool)...)
		}

		// Run post-install if specified
		if tool.PostInstall != "" {
			lines = append(lines, fmt.Sprintf("    # Post-install for %s", tool.Name))
			lines = append(lines, fmt.Sprintf("    %s", atm.getPostInstallScript(tool.PostInstall)))
		}

		// Verify installation
		if tool.VerifyCommand != "" {
			lines = append(lines, fmt.Sprintf("    if %s >/dev/null 2>&1; then", tool.VerifyCommand))
			lines = append(lines, fmt.Sprintf("        log_success '%s installed and verified'", tool.Name))
			lines = append(lines, "    else")
			lines = append(lines, fmt.Sprintf("        log_warning '%s installation may have failed verification'", tool.Name))
			lines = append(lines, "    fi")
		}

		// Cleanup if specified
		if tool.Cleanup != "" {
			lines = append(lines, fmt.Sprintf("    rm -rf %s 2>/dev/null || true", tool.Cleanup))
		}

		if checkCondition != "" {
			lines = append(lines, "fi")
		}
		lines = append(lines, "")
	}

	return strings.Join(lines, "\n")
}

// generateVersionBasedInstall generates installation commands for version-based tools
func (atm *AdvancedToolManager) generateVersionBasedInstall(tool AdvancedBinaryTool) []string {
	var lines []string

	// Determine version
	lines = append(lines, fmt.Sprintf("    # Determine version for %s", tool.Name))
	if tool.FixedVersion != "" {
		lines = append(lines, fmt.Sprintf("    LATEST_VERSION='%s'", tool.FixedVersion))
		lines = append(lines, "    log_info \"Using configured version: $LATEST_VERSION\"")
	} else {
		lines = append(lines, fmt.Sprintf("    LATEST_VERSION=$(curl -s '%s' | grep -oP '%s' | head -1)", tool.VersionURL, tool.VersionPattern))
		lines = append(lines, "    if [ -z \"$LATEST_VERSION\" ]; then")
		lines = append(lines, fmt.Sprintf("        log_error 'Failed to get latest version for %s'", tool.Name))
		lines = append(lines, "        return 1")
		lines = append(lines, "    fi")
		lines = append(lines, "    log_info \"Latest version: $LATEST_VERSION\"")
	}
	lines = append(lines, "")

	// Handle architecture mapping
	if len(tool.ArchMap) > 0 {
		lines = append(lines, "    # Map architecture")
		lines = append(lines, "    MAPPED_ARCH=\"$ARCH_NORMALIZED\"")
		lines = append(lines, "    case $ARCH_NORMALIZED in")
		for arch, mapped := range tool.ArchMap {
			lines = append(lines, fmt.Sprintf("        %s) MAPPED_ARCH=\"%s\" ;;", arch, mapped))
		}
		lines = append(lines, "    esac")
		urlTemplate := strings.ReplaceAll(tool.URLTemplate, "${ARCH}", "${MAPPED_ARCH}")
		lines = append(lines, fmt.Sprintf("    DOWNLOAD_URL=\"%s\"", urlTemplate))
	} else {
		lines = append(lines, fmt.Sprintf("    DOWNLOAD_URL=\"%s\"", tool.URLTemplate))
	}

	lines = append(lines, "    DOWNLOAD_URL=$(echo \"$DOWNLOAD_URL\" | sed \"s/\\${VERSION}/$LATEST_VERSION/g\")")
	lines = append(lines, "    log_info \"Download URL: $DOWNLOAD_URL\"")
	lines = append(lines, "")

	// Download
	lines = append(lines, fmt.Sprintf("    curl -fsSL \"$DOWNLOAD_URL\" -o '/tmp/%s-download'", tool.Name))
	lines = append(lines, "    if [ $? -ne 0 ]; then")
	lines = append(lines, fmt.Sprintf("        log_error 'Failed to download %s'", tool.Name))
	lines = append(lines, "        return 1")
	lines = append(lines, "    fi")
	lines = append(lines, "")

	// Extract if needed
	if tool.Extract {
		lines = append(lines, fmt.Sprintf("    # Extract %s", tool.Name))
		lines = append(lines, "    cd /tmp")
		lines = append(lines, fmt.Sprintf("    if file '%s-download' | grep -q 'gzip'; then", tool.Name))
		lines = append(lines, fmt.Sprintf("        tar -xzf '%s-download'", tool.Name))
		lines = append(lines, fmt.Sprintf("    elif file '%s-download' | grep -q 'bzip2'; then", tool.Name))
		lines = append(lines, fmt.Sprintf("        tar -xjf '%s-download'", tool.Name))
		lines = append(lines, "    else")
		lines = append(lines, fmt.Sprintf("        tar -xf '%s-download'", tool.Name))
		lines = append(lines, "    fi")
		lines = append(lines, "")
	}

	// Install
	if tool.InstallCommand != "" {
		lines = append(lines, fmt.Sprintf("    # Install %s", tool.Name))
		installCmd := strings.ReplaceAll(tool.InstallCommand, "${VERSION}", "$LATEST_VERSION")
		lines = append(lines, fmt.Sprintf("    %s", installCmd))
	} else if !tool.Extract {
		// Direct binary installation
		installCmd := fmt.Sprintf("mv '/tmp/%s-download' '%s'", tool.Name, tool.Dest)
		if tool.Sudo {
			installCmd = "sudo " + installCmd
		}
		lines = append(lines, fmt.Sprintf("    %s", installCmd))

		if tool.Mode != 0 {
			chmodCmd := fmt.Sprintf("chmod %o '%s'", tool.Mode, tool.Dest)
			if tool.Sudo {
				chmodCmd = "sudo " + chmodCmd
			}
			lines = append(lines, fmt.Sprintf("    %s", chmodCmd))
		}
	}

	return lines
}

// generateDirectInstall generates installation commands for direct URL tools
func (atm *AdvancedToolManager) generateDirectInstall(tool AdvancedBinaryTool) []string {
	var lines []string

	lines = append(lines, fmt.Sprintf("    # Direct install %s", tool.Name))
	lines = append(lines, fmt.Sprintf("    curl -fsSL '%s' -o '/tmp/%s'", tool.URL, tool.Name))
	lines = append(lines, "    if [ $? -ne 0 ]; then")
	lines = append(lines, fmt.Sprintf("        log_error 'Failed to download %s'", tool.Name))
	lines = append(lines, "        return 1")
	lines = append(lines, "    fi")

	// Install
	installCmd := fmt.Sprintf("mv '/tmp/%s' '%s'", tool.Name, tool.Dest)
	if tool.Sudo {
		installCmd = "sudo " + installCmd
	}
	lines = append(lines, fmt.Sprintf("    %s", installCmd))

	// Set permissions
	if tool.Mode != 0 {
		chmodCmd := fmt.Sprintf("chmod %o '%s'", tool.Mode, tool.Dest)
		if tool.Sudo {
			chmodCmd = "sudo " + chmodCmd
		}
		lines = append(lines, fmt.Sprintf("    %s", chmodCmd))
	}

	return lines
}

// getPostInstallScript returns post-install script content
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
    git clone https://github.com/LazyVim/starter "$HOME/.config/nvim"
    rm -rf "$HOME/.config/nvim/.git"
    log_success "LazyVim configured"
fi`
	default:
		return fmt.Sprintf("# Post-install: %s", postInstall)
	}
}

// GetVersionFromAPI fetches the latest version from a GitHub API URL
func (atm *AdvancedToolManager) GetVersionFromAPI(versionURL, pattern string) (string, error) {
	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	resp, err := client.Get(versionURL)
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

	if err := json.Unmarshal(body, &release); err == nil && release.TagName != "" {
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
	if len(matches) < 2 {
		return "", fmt.Errorf("version not found with pattern: %s", pattern)
	}

	version := strings.TrimPrefix(matches[1], "v")
	return version, nil
}

// expandVariables expands environment variables in strings
func (atm *AdvancedToolManager) expandVariables(text string) string {
	// Simple variable expansion
	text = strings.ReplaceAll(text, "${HOME}", "$HOME")
	text = strings.ReplaceAll(text, "${USER}", "$USER")
	text = strings.ReplaceAll(text, "${NVM_DIR}", "$HOME/.nvm")
	return text
}

// shouldSkipCondition evaluates whether a condition should be skipped
func (atm *AdvancedToolManager) shouldSkipCondition(condition string) bool {
	if condition == "" {
		return false
	}

	switch condition {
	case "provider_is_stackit":
		return atm.provider != "stackit"
	case "provider_is_aws":
		return atm.provider != "aws"
	case "provider_is_azure":
		return atm.provider != "azure"
	case "provider_is_gcp":
		return atm.provider != "gcp"
	case "provider_is_openstack":
		return atm.provider != "openstack"
	case "provider_is_vmware":
		return atm.provider != "vmware" && atm.provider != "vsphere"
	default:
		return false
	}
}

// apply per-tool overrides from config by name
func (atm *AdvancedToolManager) applyToolOverrides(tools []AdvancedBinaryTool) []AdvancedBinaryTool {
	if atm.config == nil || atm.config.Bastion.ToolOverrides == nil {
		return tools
	}
	for toolIndex := range tools {
		name := strings.ToLower(tools[toolIndex].Name)
		// Find override key in case-insensitive manner
		var override *config.ToolOverride
		for k, v := range atm.config.Bastion.ToolOverrides {
			if strings.ToLower(k) == name {
				temp := v
				override = &temp
				break
			}
		}
		if override == nil {
			continue
		}
		if override.URL != "" {
			tools[toolIndex].URL = override.URL
		}
		if override.VersionURL != "" {
			tools[toolIndex].VersionURL = override.VersionURL
		}
		if override.VersionPattern != "" {
			tools[toolIndex].VersionPattern = override.VersionPattern
		}
		if override.URLTemplate != "" {
			tools[toolIndex].URLTemplate = override.URLTemplate
		}
		if override.Version != "" {
			tools[toolIndex].FixedVersion = override.Version
		}
		if override.Dest != "" {
			tools[toolIndex].Dest = override.Dest
		}
		if override.Mode != 0 {
			tools[toolIndex].Mode = override.Mode
		}
		if override.Sudo != nil {
			tools[toolIndex].Sudo = *override.Sudo
		}
		if override.Extract != nil {
			tools[toolIndex].Extract = *override.Extract
		}
		if override.InstallCommand != "" {
			tools[toolIndex].InstallCommand = override.InstallCommand
		}
		if override.InstallScript != "" {
			tools[toolIndex].InstallScript = override.InstallScript
		}
		if override.VerifyCommand != "" {
			tools[toolIndex].VerifyCommand = override.VerifyCommand
		}
		if override.PathAddition != "" {
			tools[toolIndex].PathAddition = override.PathAddition
		}
		if override.Cleanup != "" {
			tools[toolIndex].Cleanup = override.Cleanup
		}
	}
	return tools
}
