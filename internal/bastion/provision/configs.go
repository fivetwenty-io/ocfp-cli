package provision

import (
	"context"
	"fmt"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// ConfigFileManager handles configuration file generation
type ConfigFileManager struct {
	config   *config.Config
	provider string
	log      logger.Logger
}

// ConfigFile represents a configuration file to create
type ConfigFile struct {
	Name        string `yaml:"name"`
	Path        string `yaml:"path"`
	Content     string `yaml:"content"`
	Mode        uint32 `yaml:"mode"`
	CheckExists bool   `yaml:"check_exists"`
	PreCommand  string `yaml:"pre_command"`
	PostCommand string `yaml:"post_command"`
	Enabled     bool   `yaml:"enabled"`
}

// NewConfigFileManager creates a new configuration file manager
func NewConfigFileManager(provider string, cfg *config.Config) *ConfigFileManager {
	return &ConfigFileManager{
		config:   cfg,
		provider: provider,
		log:      logger.Get(),
	}
}

// GetConfigFiles returns the list of configuration files to create
func (cfm *ConfigFileManager) GetConfigFiles() []ConfigFile {
	return []ConfigFile{
		{
			Name:        "tmux",
			Path:        "${HOME}/.tmux.conf",
			CheckExists: true,
			Content:     cfm.generateTmuxConfig(),
			Mode:        0644,
			Enabled:     true,
		},
		{
			Name:        "replyrc",
			Path:        "${HOME}/.replyrc",
			CheckExists: true,
			Content:     cfm.generateReplyrcConfig(),
			Mode:        0644,
			Enabled:     true,
		},
		{
			Name:        "genesis",
			Path:        "${HOME}/.genesis/config",
			CheckExists: true,
			PreCommand:  "mkdir -p ${HOME}/.genesis/logs",
			Content:     cfm.generateGenesisConfig(),
			Mode:        0644,
			Enabled:     true,
		},
		{
			Name:        "gitconfig",
			Path:        "${HOME}/.gitconfig",
			CheckExists: true,
			Content:     cfm.generateGitConfig(),
			Mode:        0644,
			Enabled:     cfm.hasGitConfig(),
		},
		{
			Name:        "vimrc",
			Path:        "${HOME}/.vimrc",
			CheckExists: true,
			Content:     cfm.generateVimConfig(),
			Mode:        0644,
			Enabled:     true,
		},
	}
}

// GenerateConfigFileScript generates script for configuration file creation
func (cfm *ConfigFileManager) GenerateConfigFileScript(ctx context.Context) string {
	configFiles := cfm.GetConfigFiles()
	if len(configFiles) == 0 {
		return ""
	}

	var lines []string
	lines = append(lines, "# Configuration file creation")
	lines = append(lines, "")

	for _, file := range configFiles {
		if !file.Enabled {
			continue
		}

		lines = append(lines, fmt.Sprintf("# Create configuration file: %s", file.Name))

		// Run pre-command if specified
		if file.PreCommand != "" {
			lines = append(lines, fmt.Sprintf("# Pre-command for %s", file.Name))
			lines = append(lines, file.PreCommand)
			lines = append(lines, "")
		}

		// Expand path variables
		lines = append(lines, fmt.Sprintf("CONFIG_PATH='%s'", file.Path))
		lines = append(lines, "CONFIG_PATH=$(echo \"$CONFIG_PATH\" | envsubst)")
		lines = append(lines, "")

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

		// Run post-command if specified
		if file.PostCommand != "" {
			lines = append(lines, fmt.Sprintf("# Post-command for %s", file.Name))
			lines = append(lines, file.PostCommand)
		}

		lines = append(lines, "")
	}

	return strings.Join(lines, "\n")
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

func (cfm *ConfigFileManager) generateGenesisConfig() string {
	var config strings.Builder

	config.WriteString("# OCFP Genesis configuration\n")
	config.WriteString("---\n")
	config.WriteString("genesis:\n")
	config.WriteString("  author_name: OCFP\n")
	config.WriteString("  author_email: ops@example.com\n")

	if cfm.config.Bastion.Git.User.Name != "" {
		config.WriteString(fmt.Sprintf("  author_name: %s\n", cfm.config.Bastion.Git.User.Name))
	}

	if cfm.config.Bastion.Git.User.Email != "" {
		config.WriteString(fmt.Sprintf("  author_email: %s\n", cfm.config.Bastion.Git.User.Email))
	}

	config.WriteString("  env: ${OCFP_BLOC_NAME:-development}\n")
	config.WriteString("  vault:\n")
	config.WriteString("    url: https://127.0.0.1:8200\n")
	config.WriteString("    verify: false\n")
	config.WriteString("  bosh:\n")
	config.WriteString("    env: ${OCFP_BLOC_NAME:-bosh}\n")
	config.WriteString("  kit_repos:\n")
	config.WriteString("    - https://github.com/genesis-community\n")
	config.WriteString("  secrets:\n")
	config.WriteString("    base: secret/${OCFP_BLOC_NAME:-development}\n")

	return config.String()
}

func (cfm *ConfigFileManager) generateGitConfig() string {
	if !cfm.hasGitConfig() {
		return ""
	}

	var config strings.Builder
	config.WriteString("# OCFP Git configuration\n")
	config.WriteString("[user]\n")

	if cfm.config.Bastion.Git.User.Name != "" {
		config.WriteString(fmt.Sprintf("    name = %s\n", cfm.config.Bastion.Git.User.Name))
	}

	if cfm.config.Bastion.Git.User.Email != "" {
		config.WriteString(fmt.Sprintf("    email = %s\n", cfm.config.Bastion.Git.User.Email))
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
	return `" OCFP Vim configuration
set nocompatible
set number
set relativenumber
set tabstop=4
set shiftwidth=4
set expandtab
set autoindent
set smartindent
set hlsearch
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
set background=dark

" File type detection
filetype on
filetype plugin on
filetype indent on

" Show matching brackets
set showmatch

" Enable mouse support
set mouse=a

" Persistent undo
set undofile
set undodir=~/.vim/undodir

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
nnoremap <leader>to :tabonly<CR>

" Set leader key
let mapleader = ","`
}

// hasGitConfig checks if Git configuration is available
func (cfm *ConfigFileManager) hasGitConfig() bool {
	return cfm.config.Bastion.Git.User.Name != "" || cfm.config.Bastion.Git.User.Email != ""
}
