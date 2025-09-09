package provision

import (
	"context"
	"fmt"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// CFPluginManager handles CloudFoundry plugin installations.
type CFPluginManager struct {
	config   *config.Config
	provider string
	log      logger.Logger
}

const (
	// cfInstallForceFlag appends force flag to cf install commands.
	cfInstallForceFlag = " -f"
)

// CFPlugin represents a CloudFoundry plugin configuration.
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

// NewCFPluginManager creates a new CF plugin manager.
func NewCFPluginManager(provider string, cfg *config.Config) *CFPluginManager {
	return &CFPluginManager{
		config:   cfg,
		provider: provider,
		log:      logger.Get(),
	}
}

// GetCFPlugins returns the list of CF plugins to install.
func (cfm *CFPluginManager) GetCFPlugins() []CFPlugin {
	plugins := cfm.getDefaultPlugins()

	if cfm.config != nil {
		cfm.applyEnableDisableOverrides(plugins)
		cfm.applyPluginOverrides(plugins)
	}

	return plugins
}

// GenerateCFPluginInstallScript generates script for CF plugin installation.
func (cfm *CFPluginManager) GenerateCFPluginInstallScript(ctx context.Context) string {
	plugins := cfm.GetCFPlugins()
	if len(plugins) == 0 {
		return ""
	}

	lines := cfm.initScriptLines(plugins)
	cfm.addScriptHeader(lines)
	cfm.addCLICheck(lines)

	for _, plugin := range plugins {
		if plugin.Enabled {
			cfm.addPluginInstallSection(lines, plugin)
		}
	}

	cfm.addVerificationSection(lines)

	return strings.Join(*lines, "\n")
}

// getDefaultPlugins returns the default list of CF plugins.
func (cfm *CFPluginManager) getDefaultPlugins() []CFPlugin {
	return []CFPlugin{
		{
			Name:              "targets",
			Enabled:           true,
			Repo:              "",
			RepoURL:           "",
			GitHubRepo:        "cloudfoundry-community/cf-targets-plugin",
			InstallFromGitHub: true,
			Version:           "",
			Force:             false,
		},
		{
			Name:              "top",
			Enabled:           true,
			Repo:              "",
			RepoURL:           "",
			GitHubRepo:        "cloudfoundry-community/cf-top-plugin",
			InstallFromGitHub: true,
			Version:           "",
			Force:             false,
		},
		{
			Name:              "logs",
			Enabled:           true,
			Repo:              "",
			RepoURL:           "",
			GitHubRepo:        "cloudfoundry-incubator/cf-logs-plugin",
			InstallFromGitHub: true,
			Version:           "",
			Force:             false,
		},
	}
}

// applyEnableDisableOverrides applies enable/disable configuration overrides to plugins.
func (cfm *CFPluginManager) applyEnableDisableOverrides(plugins []CFPlugin) {
	enable := cfm.buildNameSet(cfm.config.Bastion.CFPlugins.Enable)
	disable := cfm.buildNameSet(cfm.config.Bastion.CFPlugins.Disable)

	if len(enable) == 0 && len(disable) == 0 {
		return
	}

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

// buildNameSet creates a set of lowercased names.
func (cfm *CFPluginManager) buildNameSet(names []string) map[string]struct{} {
	set := make(map[string]struct{})
	for _, n := range names {
		set[strings.ToLower(n)] = struct{}{}
	}

	return set
}

// applyPluginOverrides applies per-plugin configuration overrides.
func (cfm *CFPluginManager) applyPluginOverrides(plugins []CFPlugin) {
	if cfm.config.Bastion.CFPluginOverrides == nil {
		return
	}

	for i := range plugins {
		name := strings.ToLower(plugins[i].Name)
		for key, override := range cfm.config.Bastion.CFPluginOverrides {
			if strings.ToLower(key) == name {
				cfm.applyOverrideToPlugin(&plugins[i], override)
			}
		}
	}
}

// applyOverrideToPlugin applies a single override to a plugin.
func (cfm *CFPluginManager) applyOverrideToPlugin(plugin *CFPlugin, override config.CFPluginOverride) {
	if override.GitHubRepo != "" {
		plugin.GitHubRepo = override.GitHubRepo
		plugin.InstallFromGitHub = true
	}

	if override.Version != "" {
		plugin.Version = override.Version
	}

	if override.Repo != "" {
		plugin.Repo = override.Repo
	}

	if override.RepoURL != "" {
		plugin.RepoURL = override.RepoURL
	}

	if override.Force != nil {
		plugin.Force = *override.Force
	}
}

// initScriptLines creates the lines slice with appropriate capacity.
func (cfm *CFPluginManager) initScriptLines(plugins []CFPlugin) *[]string {
	base := 8 // header + CF CLI checks
	per := 25 // worst-case lines per enabled plugin
	tail := 6 // verification section
	lines := make([]string, 0, base+per*len(plugins)+tail)

	return &lines
}

// addScriptHeader adds the script header to the lines.
func (cfm *CFPluginManager) addScriptHeader(lines *[]string) {
	*lines = append(*lines, "# CloudFoundry plugin installation", "")
}

// addCLICheck adds CF CLI availability check to the lines.
func (cfm *CFPluginManager) addCLICheck(lines *[]string) {
	*lines = append(*lines,
		"# Ensure CF CLI is available",
		"if ! command -v cf >/dev/null 2>&1; then",
		"    log_error 'CF CLI not found, skipping plugin installation'",
		"    return 0",
		"fi",
		"")
}

// addPluginInstallSection adds installation commands for a single plugin.
func (cfm *CFPluginManager) addPluginInstallSection(lines *[]string, plugin CFPlugin) {
	*lines = append(*lines, "# Install CF plugin: "+plugin.Name)

	cfm.addPluginExistenceCheck(lines, plugin)

	if plugin.InstallFromGitHub && plugin.GitHubRepo != "" {
		cfm.addGitHubInstallCommands(lines, plugin)
	} else if plugin.Repo != "" {
		cfm.addRepoInstallCommands(lines, plugin)
	}

	*lines = append(*lines, "fi", "")
}

// addPluginExistenceCheck adds commands to check if plugin is already installed.
func (cfm *CFPluginManager) addPluginExistenceCheck(lines *[]string, plugin CFPlugin) {
	*lines = append(*lines,
		fmt.Sprintf("if cf plugins | grep -q '^%s '; then", plugin.Name),
		fmt.Sprintf("    log_info 'CF plugin %s already installed'", plugin.Name),
		"else",
		fmt.Sprintf("    log_info 'Installing CF plugin: %s'", plugin.Name))
}

// addGitHubInstallCommands adds commands for installing from GitHub.
func (cfm *CFPluginManager) addGitHubInstallCommands(lines *[]string, plugin CFPlugin) {
	*lines = append(*lines, "    # Install from GitHub releases")

	cfm.addReleaseVersionCommands(lines, plugin)

	*lines = append(*lines,
		fmt.Sprintf("    PLUGIN_URL=\"https://github.com/%s/releases/download/${LATEST_RELEASE}/%s-linux-amd64\"", plugin.GitHubRepo, plugin.Name),
		"    ",
		"    # Download and install plugin",
		fmt.Sprintf("    curl -fsSL \"$PLUGIN_URL\" -o \"/tmp/%s-plugin\"", plugin.Name),
		"    if [ $? -eq 0 ]; then",
		fmt.Sprintf("        chmod +x \"/tmp/%s-plugin\"", plugin.Name))

	cfm.addInstallCommand(lines, plugin, fmt.Sprintf("/tmp/%s-plugin", plugin.Name))

	*lines = append(*lines,
		"        if [ $? -eq 0 ]; then",
		fmt.Sprintf("            log_success 'CF plugin %s installed successfully'", plugin.Name),
		"        else",
		fmt.Sprintf("            log_error 'Failed to install CF plugin %s'", plugin.Name),
		"        fi",
		fmt.Sprintf("        rm -f \"/tmp/%s-plugin\"", plugin.Name),
		"    else",
		fmt.Sprintf("        log_error 'Failed to download CF plugin %s'", plugin.Name),
		"    fi")
}

// addReleaseVersionCommands adds commands to determine release version.
func (cfm *CFPluginManager) addReleaseVersionCommands(lines *[]string, plugin CFPlugin) {
	if plugin.Version != "" {
		*lines = append(*lines, fmt.Sprintf("    LATEST_RELEASE='%s'", plugin.Version))
	} else {
		*lines = append(*lines, fmt.Sprintf("    LATEST_RELEASE=$(curl -s https://api.github.com/repos/%s/releases/latest | grep 'tag_name' | cut -d'\"' -f4)", plugin.GitHubRepo))
	}
}

// addInstallCommand adds the cf install-plugin command.
func (cfm *CFPluginManager) addInstallCommand(lines *[]string, plugin CFPlugin, path string) {
	installCmd := fmt.Sprintf("cf install-plugin \"%s\"", path)
	if plugin.Force {
		installCmd += cfInstallForceFlag
	}

	*lines = append(*lines, "        "+installCmd)
}

// addRepoInstallCommands adds commands for installing from CF repository.
func (cfm *CFPluginManager) addRepoInstallCommands(lines *[]string, plugin CFPlugin) {
	addRepoCmd := fmt.Sprintf("cf add-plugin-repo %s %s", plugin.Name, plugin.RepoURL)
	if plugin.Force {
		addRepoCmd += cfInstallForceFlag
	}

	*lines = append(*lines, "    "+addRepoCmd)

	installCmd := "cf install-plugin " + plugin.Name
	if plugin.Force {
		installCmd += cfInstallForceFlag
	}

	*lines = append(*lines, "    "+installCmd)
}

// addVerificationSection adds the plugin verification commands.
func (cfm *CFPluginManager) addVerificationSection(lines *[]string) {
	*lines = append(*lines,
		"# Verify installed plugins",
		"log_info 'Installed CF plugins:'",
		"cf plugins | grep '^[a-zA-Z]' | while read plugin_line; do",
		"    log_info \"  $plugin_line\"",
		"done",
		"")
}
