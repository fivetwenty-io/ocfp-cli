package provision

import (
	"context"
	"fmt"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// CFPluginManager handles CloudFoundry plugin installations
type CFPluginManager struct {
	config   *config.Config
	provider string
	log      logger.Logger
}

// CFPlugin represents a CloudFoundry plugin configuration
type CFPlugin struct {
	Name              string `yaml:"name"`
	Enabled           bool   `yaml:"enabled"`
	Repo              string `yaml:"repo"`
	RepoURL           string `yaml:"repoUrl"`
	GitHubRepo        string `yaml:"githubRepo"`
	InstallFromGitHub bool   `yaml:"installFromGithub"`
	Version           string `yaml:"version"`
	Force             bool   `yaml:"force"`
}

// NewCFPluginManager creates a new CF plugin manager
func NewCFPluginManager(provider string, cfg *config.Config) *CFPluginManager {
	return &CFPluginManager{
		config:   cfg,
		provider: provider,
		log:      logger.Get(),
	}
}

// GetCFPlugins returns the list of CF plugins to install
func (cfm *CFPluginManager) GetCFPlugins() []CFPlugin {
	plugins := []CFPlugin{
		{
			Name:              "targets",
			Enabled:           true,
			GitHubRepo:        "cloudfoundry-community/cf-targets-plugin",
			InstallFromGitHub: true,
		},
		{
			Name:              "top",
			Enabled:           true,
			GitHubRepo:        "cloudfoundry-community/cf-top-plugin",
			InstallFromGitHub: true,
		},
		{
			Name:              "logs",
			Enabled:           true,
			GitHubRepo:        "cloudfoundry-incubator/cf-logs-plugin",
			InstallFromGitHub: true,
		},
	}
	// Apply config overrides
	if cfm.config != nil {
		enable := make(map[string]struct{})
		for _, n := range cfm.config.Bastion.CFPlugins.Enable {
			enable[strings.ToLower(n)] = struct{}{}
		}
		disable := make(map[string]struct{})
		for _, n := range cfm.config.Bastion.CFPlugins.Disable {
			disable[strings.ToLower(n)] = struct{}{}
		}
		if len(enable) > 0 || len(disable) > 0 {
			for index := range plugins {
				name := strings.ToLower(plugins[index].Name)
				if _, ok := enable[name]; ok {
					plugins[index].Enabled = true
				}
				if _, ok := disable[name]; ok {
					plugins[index].Enabled = false
				}
			}
		}
		// Per-plugin overrides by name
		if cfm.config.Bastion.CFPluginOverrides != nil {
			for index := range plugins {
				name := strings.ToLower(plugins[index].Name)
				for key, override := range cfm.config.Bastion.CFPluginOverrides {
					if strings.ToLower(key) != name {
						continue
					}
					if override.GitHubRepo != "" {
						plugins[index].GitHubRepo = override.GitHubRepo
						plugins[index].InstallFromGitHub = true
					}
					if override.Version != "" {
						plugins[index].Version = override.Version
					}
					if override.Repo != "" {
						plugins[index].Repo = override.Repo
					}
					if override.RepoURL != "" {
						plugins[index].RepoURL = override.RepoURL
					}
					if override.Force != nil {
						plugins[index].Force = *override.Force
					}
				}
			}
		}
	}
	return plugins
}

// GenerateCFPluginInstallScript generates script for CF plugin installation
func (cfm *CFPluginManager) GenerateCFPluginInstallScript(ctx context.Context) string {
	plugins := cfm.GetCFPlugins()
	if len(plugins) == 0 {
		return ""
	}

	var lines []string
	lines = append(lines, "# CloudFoundry plugin installation")
	lines = append(lines, "")

	// Ensure CF CLI is available
	lines = append(lines, "# Ensure CF CLI is available")
	lines = append(lines, "if ! command -v cf >/dev/null 2>&1; then")
	lines = append(lines, "    log_error 'CF CLI not found, skipping plugin installation'")
	lines = append(lines, "    return 0")
	lines = append(lines, "fi")
	lines = append(lines, "")

	for _, plugin := range plugins {
		if !plugin.Enabled {
			continue
		}

		lines = append(lines, fmt.Sprintf("# Install CF plugin: %s", plugin.Name))

		// Check if plugin is already installed
		lines = append(lines, fmt.Sprintf("if cf plugins | grep -q '^%s '; then", plugin.Name))
		lines = append(lines, fmt.Sprintf("    log_info 'CF plugin %s already installed'", plugin.Name))
		lines = append(lines, "else")
		lines = append(lines, fmt.Sprintf("    log_info 'Installing CF plugin: %s'", plugin.Name))

		if plugin.InstallFromGitHub && plugin.GitHubRepo != "" {
			// Install from GitHub releases
			lines = append(lines, "    # Install from GitHub releases")
			if plugin.Version != "" {
				lines = append(lines, fmt.Sprintf("    LATEST_RELEASE='%s'", plugin.Version))
			} else {
				lines = append(lines, fmt.Sprintf("    LATEST_RELEASE=$(curl -s https://api.github.com/repos/%s/releases/latest | grep 'tag_name' | cut -d'\"' -f4)", plugin.GitHubRepo))
			}
			lines = append(lines, fmt.Sprintf("    PLUGIN_URL=\"https://github.com/%s/releases/download/${LATEST_RELEASE}/%s-linux-amd64\"", plugin.GitHubRepo, plugin.Name))
			lines = append(lines, "    ")
			lines = append(lines, "    # Download and install plugin")
			lines = append(lines, fmt.Sprintf("    curl -fsSL \"$PLUGIN_URL\" -o \"/tmp/%s-plugin\"", plugin.Name))
			lines = append(lines, "    if [ $? -eq 0 ]; then")
			lines = append(lines, fmt.Sprintf("        chmod +x \"/tmp/%s-plugin\"", plugin.Name))

			// Build CF install command
			installCmd := fmt.Sprintf("cf install-plugin \"/tmp/%s-plugin\"", plugin.Name)
			if plugin.Force {
				installCmd += " -f"
			}

			lines = append(lines, fmt.Sprintf("        %s", installCmd))
			lines = append(lines, "        if [ $? -eq 0 ]; then")
			lines = append(lines, fmt.Sprintf("            log_success 'CF plugin %s installed successfully'", plugin.Name))
			lines = append(lines, "        else")
			lines = append(lines, fmt.Sprintf("            log_error 'Failed to install CF plugin %s'", plugin.Name))
			lines = append(lines, "        fi")
			lines = append(lines, fmt.Sprintf("        rm -f \"/tmp/%s-plugin\"", plugin.Name))
			lines = append(lines, "    else")
			lines = append(lines, fmt.Sprintf("        log_error 'Failed to download CF plugin %s'", plugin.Name))
			lines = append(lines, "    fi")
		} else if plugin.Repo != "" {
			// Install from CF Community repository
			installCmd := fmt.Sprintf("cf add-plugin-repo %s %s", plugin.Name, plugin.RepoURL)
			if plugin.Force {
				installCmd += " -f"
			}

			lines = append(lines, fmt.Sprintf("    %s", installCmd))
			lines = append(lines, fmt.Sprintf("    cf install-plugin %s", plugin.Name))

			if plugin.Force {
				lines[len(lines)-1] += " -f"
			}
		}

		lines = append(lines, "fi")
		lines = append(lines, "")
	}

	// Verify installed plugins
	lines = append(lines, "# Verify installed plugins")
	lines = append(lines, "log_info 'Installed CF plugins:'")
	lines = append(lines, "cf plugins | grep '^[a-zA-Z]' | while read plugin_line; do")
	lines = append(lines, "    log_info \"  $plugin_line\"")
	lines = append(lines, "done")
	lines = append(lines, "")

	return strings.Join(lines, "\n")
}
