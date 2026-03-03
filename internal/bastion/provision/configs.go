package provision

import (
	"context"
	"fmt"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// ConfigFileManager handles configuration file generation.
type ConfigFileManager struct {
	config   *config.Config
	provider string
	log      logger.Logger
}

// ConfigFile represents a configuration file to create.
type ConfigFile struct {
	Name        string `yaml:"name"`
	Path        string `yaml:"path"`
	Content     string `yaml:"content"`
	Mode        uint32 `yaml:"mode"`
	CheckExists bool   `yaml:"checkExists"`
	PreCommand  string `yaml:"preCommand"`
	PostCommand string `yaml:"postCommand"`
	Enabled     bool   `yaml:"enabled"`
}

// NewConfigFileManager creates a new configuration file manager.
func NewConfigFileManager(provider string, cfg *config.Config) *ConfigFileManager {
	return &ConfigFileManager{
		config:   cfg,
		provider: provider,
		log:      logger.Get(),
	}
}

// GetConfigFiles returns the list of configuration files to create.
func (cfm *ConfigFileManager) GetConfigFiles() []ConfigFile {
	return []ConfigFile{
		{
			Name:        "tmux",
			Path:        "${HOME}/.tmux.conf",
			CheckExists: true,
			Content:     cfm.generateTmuxConfig(),
			Mode:        fileModeStandard,
			Enabled:     true,
			PreCommand:  "",
			PostCommand: "",
		},
		{
			Name:        "replyrc",
			Path:        "${HOME}/.replyrc",
			CheckExists: true,
			Content:     cfm.generateReplyrcConfig(),
			Mode:        fileModeStandard,
			Enabled:     true,
			PreCommand:  "",
			PostCommand: "",
		},
		{
			Name:        "genesis",
			Path:        "${HOME}/.genesis/config",
			CheckExists: true,
			PreCommand:  "mkdir -p ${HOME}/.genesis/logs",
			Content:     cfm.GenerateGenesisConfig(),
			Mode:        fileModeStandard,
			Enabled:     true,
			PostCommand: "",
		},
		{
			Name:        "gitconfig",
			Path:        "${HOME}/.gitconfig",
			CheckExists: true,
			Content:     cfm.generateGitConfig(),
			Mode:        fileModeStandard,
			Enabled:     cfm.hasGitConfig(),
			PreCommand:  "",
			PostCommand: "",
		},
		{
			Name:        "vimrc",
			Path:        "${HOME}/.vimrc",
			CheckExists: true,
			Content:     cfm.generateVimConfig(),
			Mode:        fileModeStandard,
			Enabled:     true,
			PreCommand:  "",
			PostCommand: "",
		},
	}
}

// GenerateConfigFileScript generates script for configuration file creation.
func (cfm *ConfigFileManager) GenerateConfigFileScript(ctx context.Context) string {
	configFiles := cfm.GetConfigFiles()
	if len(configFiles) == 0 {
		return ""
	}

	lines := cfm.generateConfigFileHeader()

	for _, file := range configFiles {
		if file.Enabled {
			lines = append(lines, cfm.generateConfigFileSection(file)...)
		}
	}

	return strings.Join(lines, "\n")
}

// GenerateGenesisConfig generates the Genesis configuration file content.
func (cfm *ConfigFileManager) GenerateGenesisConfig() string {
	var config strings.Builder

	config.WriteString("# ~/.genesis/config\n\n")

	cfm.writeGenesisBasicSettings(&config)
	cfm.writeGenesisLoggingConfig(&config)

	return config.String()
}

func (cfm *ConfigFileManager) generateConfigFileHeader() []string {
	return []string{
		"# Configuration file creation",
		"",
	}
}

func (cfm *ConfigFileManager) generateConfigFileSection(file ConfigFile) []string {
	const perFile = 32

	lines := make([]string, 0, perFile)

	lines = append(lines, "# Create configuration file: "+file.Name)
	lines = append(lines, cfm.generatePreCommand(file)...)
	lines = append(lines, cfm.generatePathSetup(file)...)
	lines = append(lines, cfm.generateFileCreation(file)...)
	lines = append(lines, cfm.generatePostCommand(file)...)
	lines = append(lines, "")

	return lines
}

func (cfm *ConfigFileManager) generatePreCommand(file ConfigFile) []string {
	if file.PreCommand == "" {
		return nil
	}

	return []string{
		"# Pre-command for " + file.Name,
		file.PreCommand,
		"",
	}
}

func (cfm *ConfigFileManager) generatePathSetup(file ConfigFile) []string {
	return []string{
		fmt.Sprintf("CONFIG_PATH=\"%s\"", file.Path),
		"",
	}
}

func (cfm *ConfigFileManager) generateFileCreation(file ConfigFile) []string {
	lines := []string{}

	if file.CheckExists {
		lines = append(lines, "if [ -f \"$CONFIG_PATH\" ]; then")
		lines = append(lines, fmt.Sprintf("    log_info '%s configuration already exists at $CONFIG_PATH'", file.Name))
		lines = append(lines, "else")
	}

	lines = append(lines, fmt.Sprintf("    log_info 'Creating %s configuration at $CONFIG_PATH'", file.Name))
	lines = append(lines, "    mkdir -p \"$(dirname \"$CONFIG_PATH\")\"")
	lines = append(lines, "    cat > \"$CONFIG_PATH\" << 'CONFIG_EOF'")
	lines = append(lines, file.Content)
	lines = append(lines, "CONFIG_EOF")

	if file.Mode != 0 {
		lines = append(lines, fmt.Sprintf("    chmod %o \"$CONFIG_PATH\"", file.Mode))
	}

	lines = append(lines, fmt.Sprintf("    log_success '%s configuration created'", file.Name))

	if file.CheckExists {
		lines = append(lines, "fi")
	}

	return lines
}

func (cfm *ConfigFileManager) generatePostCommand(file ConfigFile) []string {
	if file.PostCommand == "" {
		return nil
	}

	return []string{
		"# Post-command for " + file.Name,
		file.PostCommand,
	}
}

// Configuration file content generators

func (cfm *ConfigFileManager) generateTmuxConfig() string {
	return `# OCFP tmux configuration
# Enable mouse support
set -g mouse on

# Set default terminal
set -g default-terminal "screen-256color"

# Enable activity alerts
setw -g monitor-activity on
set -g visual-activity on

# Set window and pane index to start at 1
set -g base-index 1
setw -g pane-base-index 1

# Enable vi mode
setw -g mode-keys vi

# Key bindings
bind r source-file ~/.tmux.conf \; display-message "Config reloaded!"
bind | split-window -h
bind - split-window -v

# Pane navigation
bind h select-pane -L
bind j select-pane -D
bind k select-pane -U
bind l select-pane -R

# Window navigation
bind -n M-Left previous-window
bind -n M-Right next-window

# Status bar configuration
set -g status-bg black
set -g status-fg white
set -g status-left '#[fg=green]#S '
set -g status-right '#[fg=yellow]#(uptime | cut -d"," -f 1) #[fg=cyan]%Y-%m-%d %H:%M'
set -g status-right-length 50
set -g status-left-length 20

# Pane borders
set -g pane-border-style fg=magenta
set -g pane-active-border-style fg=cyan

# History limit
set -g history-limit 10000

# Enable clipboard
set -g set-clipboard on`
}

func (cfm *ConfigFileManager) generateReplyrcConfig() string {
	return `# OCFP Reply (Perl REPL) configuration
script_line1 = use strict;
script_line2 = use warnings;
script_line3 = use v5.20;
script_line4 = use Data::Printer;

plugins = Interrupt
plugins = FancyPrompt
plugins = DataDumper
plugins = Colors
plugins = ReadLine
plugins = Hints
plugins = Packages
plugins = LexicalPersistence
plugins = ResultCache
plugins = Autocomplete::Packages
plugins = Autocomplete::Lexicals
plugins = Autocomplete::Functions
plugins = Autocomplete::Globals
plugins = Autocomplete::Methods
plugins = Autocomplete::Commands

[FancyPrompt]
format = \001\e[1;32m\002%s\001\e[0m\002:%03d%s> 
line_format = \001\e[1;31m\002%5s\001\e[0m\002 | %s

[DataDumper]
filter = Data::Printer

[Packages]
packages = YAML::XS
packages = JSON::PP  
packages = Service::Vault
packages = Try::Tiny
packages = OCFP
packages = OCFP::Config`
}

// writeGenesisBasicSettings writes basic Genesis configuration settings.
func (cfm *ConfigFileManager) writeGenesisBasicSettings(config *strings.Builder) {
	// BOSH targeting
	config.WriteString("# BOSH targeting\n")
	config.WriteString("default_bosh_target: ask\n\n")

	// Repository management
	config.WriteString("# Repository management\n")
	config.WriteString("legacy_repo_suffix: false\n")
	config.WriteString("deployment_roots:\n")
	config.WriteString("  - /home/ubuntu/ocfp/deployments\n\n")

	// Display preferences
	config.WriteString("# Display preferences\n")
	config.WriteString("output_style: fun\n")
	config.WriteString("show_duration: true\n\n")

	// Deployment behavior
	config.WriteString("# Deployment behavior\n")
	config.WriteString("fix_on_deploy: ask\n")
	config.WriteString("confirm_release_overrides: outdated\n\n")

	// Cache and storage
	config.WriteString("# Cache and storage\n")
	config.WriteString("spec_cache_dir: \"/tmp/genesis-cache\"\n")
	config.WriteString("bosh_logs_path: \"/home/ubuntu/ocfp/logs\"\n\n")

	// Warning suppression
	config.WriteString("# Warning suppression\n")
	config.WriteString("suppress_warnings:\n")
	config.WriteString("  oversized_secrets: false\n")
	config.WriteString("  bosh_target: true\n\n")

	// Genesis behavior
	config.WriteString("# Genesis behavior\n")
	config.WriteString("embedded_genesis: warn\n")
	config.WriteString("automatic_config_upgrade: \"yes\"\n\n")
}

// writeGenesisLoggingConfig writes Genesis logging configuration.
func (cfm *ConfigFileManager) writeGenesisLoggingConfig(config *strings.Builder) {
	config.WriteString("# Comprehensive logging setup\n")
	config.WriteString("logs:\n")

	cfm.writeMainApplicationLog(config)
	cfm.writeDebugLog(config)
	cfm.writeErrorLog(config)
}

// writeMainApplicationLog writes the main application log configuration.
func (cfm *ConfigFileManager) writeMainApplicationLog(config *strings.Builder) {
	config.WriteString("  # Main application log\n")
	config.WriteString("  - file: \"/home/ubuntu/.genesis/logs/genesis.log\"\n")
	config.WriteString("    level: INFO\n")
	config.WriteString("    timestamp: true\n")
	config.WriteString("    style: plain\n")
	config.WriteString("    lifespan: forever\n")
	config.WriteString("    show_stack: default\n")
	config.WriteString("    truncate: false\n")
	config.WriteString("    \n")
}

// writeDebugLog writes the debug log configuration.
func (cfm *ConfigFileManager) writeDebugLog(config *strings.Builder) {
	config.WriteString("  # Debug log for troubleshooting\n")
	config.WriteString("  - file: \"/home/ubuntu/.genesis/logs/debug.log\"\n")
	config.WriteString("    level: DEBUG\n")
	config.WriteString("    style: rfc-5424\n")
	config.WriteString("    lifespan: current\n")
	config.WriteString("    truncate: true\n")
	config.WriteString("    timestamp: true\n")
	config.WriteString("    show_stack: full\n")
	config.WriteString("    \n")
}

// writeErrorLog writes the error log configuration.
func (cfm *ConfigFileManager) writeErrorLog(config *strings.Builder) {
	config.WriteString("  # Error-only log\n")
	config.WriteString("  - file: \"/home/ubuntu/.genesis/logs/errors.log\"\n")
	config.WriteString("    level: ERROR\n")
	config.WriteString("    style: plain\n")
	config.WriteString("    lifespan: forever\n")
	config.WriteString("    timestamp: true\n")
	config.WriteString("    show_stack: fatal\n")
}

func (cfm *ConfigFileManager) generateGitConfig() string {
	if !cfm.hasGitConfig() {
		return ""
	}

	var config strings.Builder
	config.WriteString("# OCFP Git configuration\n")
	config.WriteString("[user]\n")

	if cfm.config.Bastion.Git.User.Name != "" {
		fmt.Fprintf(&config, "    name = %s\n", cfm.config.Bastion.Git.User.Name)
	}

	if cfm.config.Bastion.Git.User.Email != "" {
		fmt.Fprintf(&config, "    email = %s\n", cfm.config.Bastion.Git.User.Email)
	}

	config.WriteString("\n[core]\n")
	config.WriteString("    editor = nvim\n")
	config.WriteString("    autocrlf = input\n")
	config.WriteString("\n[push]\n")
	config.WriteString("    default = simple\n")
	config.WriteString("\n[pull]\n")
	config.WriteString("    rebase = false\n")
	config.WriteString("\n[init]\n")
	config.WriteString("    defaultBranch = main\n")
	config.WriteString("\n[alias]\n")
	config.WriteString("    st = status\n")
	config.WriteString("    co = checkout\n")
	config.WriteString("    br = branch\n")
	config.WriteString("    ci = commit\n")
	config.WriteString("    unstage = reset HEAD --\n")
	config.WriteString("    last = log -1 HEAD\n")
	config.WriteString("    visual = !gitk\n")

	return config.String()
}

func (cfm *ConfigFileManager) generateVimConfig() string {
	sections := []string{
		cfm.getVimHeader(),
		cfm.getVimBasicSettings(),
		cfm.getVimDisplaySettings(),
		cfm.getVimFileTypeSettings(),
		cfm.getVimMiscSettings(),
		cfm.getVimKeyMappings(),
	}

	return strings.Join(sections, "\n\n")
}

func (cfm *ConfigFileManager) getVimHeader() string {
	return `" OCFP Vim configuration
set nocompatible`
}

func (cfm *ConfigFileManager) getVimBasicSettings() string {
	return `set number
set relativenumber
set tabstop=4
set shiftwidth=4
set expandtab
set autoindent
set smartindent`
}

func (cfm *ConfigFileManager) getVimDisplaySettings() string {
	return `set hlsearch
set incsearch
set ignorecase
set smartcase
set ruler
set showcmd
set wildmenu
set wildmode=longest:full,full
set backspace=indent,eol,start

" Enable syntax highlighting
syntax enable

" Color scheme
set background=dark`
}

func (cfm *ConfigFileManager) getVimFileTypeSettings() string {
	return `" File type detection
filetype on
filetype plugin on
filetype indent on`
}

func (cfm *ConfigFileManager) getVimMiscSettings() string {
	return `" Show matching brackets
set showmatch

" Enable mouse support
set mouse=a

" Persistent undo
set undofile
set undodir=~/.vim/undodir`
}

func (cfm *ConfigFileManager) getVimKeyMappings() string {
	return `" Set leader key
let mapleader = ","

" Better search highlighting
nnoremap <leader>h :nohlsearch<CR>

" Quick save
nnoremap <leader>w :w<CR>

" Quick quit
nnoremap <leader>q :q<CR>

" Split navigation
nnoremap <C-h> <C-w>h
nnoremap <C-j> <C-w>j
nnoremap <C-k> <C-w>k
nnoremap <C-l> <C-w>l

" Tab navigation
nnoremap <leader>tn :tabnew<CR>
nnoremap <leader>tc :tabclose<CR>
nnoremap <leader>to :tabonly<CR>`
}

// hasGitConfig checks if Git configuration is available.
func (cfm *ConfigFileManager) hasGitConfig() bool {
	return cfm.config.Bastion.Git.User.Name != "" || cfm.config.Bastion.Git.User.Email != ""
}
