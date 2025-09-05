package provision

import (
	"context"
	"fmt"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// DirectoryManager handles OCFP directory structure and symlink management.
type DirectoryManager struct {
	config   *config.Config
	provider string
	log      logger.Logger
}

// NewDirectoryManager creates a new directory manager.
func NewDirectoryManager(provider string, cfg *config.Config) *DirectoryManager {
	return &DirectoryManager{
		config:   cfg,
		provider: provider,
		log:      logger.Get(),
	}
}

// GetOCFPDirectories returns OCFP-specific directories to create.
func (dm *DirectoryManager) GetOCFPDirectories() []DirectoryConfig {
	ocfpPath := "${HOME}/ocfp"

	return []DirectoryConfig{
		{Path: ocfpPath, Mode: 0755},
		{Path: ocfpPath + "/cli", Mode: 0755},
		{Path: ocfpPath + "/deployments", Mode: 0755},
		{Path: ocfpPath + "/releases", Mode: 0755},
		{Path: ocfpPath + "/artifacts", Mode: 0755},
		{Path: ocfpPath + "/kits", Mode: 0755},
		{Path: "${HOME}/.ocfp", Mode: 0755},
		{Path: "${HOME}/.ocfp/config", Mode: 0755},
		{Path: "${HOME}/.ocfp/logs", Mode: 0755},
		{Path: "${HOME}/.ocfp/logs/provision", Mode: 0755},
		{Path: "${HOME}/bin", Mode: 0755},
		{Path: "${HOME}/.ssh", Mode: 0700},
		{Path: "${HOME}/.genesis", Mode: 0755},
		{Path: "${HOME}/.genesis/logs", Mode: 0755},
		{Path: "${HOME}/.config", Mode: 0755},
		{Path: "${HOME}/.config/nvim", Mode: 0755},
	}
}

// GetOCFPSymlinks returns OCFP symlinks to create.
func (dm *DirectoryManager) GetOCFPSymlinks() map[string]string {
	return map[string]string{
		"${HOME}/ops":         "${HOME}/ocfp",
		"${HOME}/deployments": "${HOME}/ocfp/deployments",
	}
}

// GenerateOCFPDirectoryScript generates script for OCFP directory setup.
func (dm *DirectoryManager) GenerateOCFPDirectoryScript(ctx context.Context) string {
	var lines []string

	lines = append(lines, "# OCFP directory structure setup")
	lines = append(lines, "")

	// Create directories
	directories := dm.GetOCFPDirectories()

	lines = append(lines, "# Create OCFP directories")

	for _, dir := range directories {
		lines = append(lines, fmt.Sprintf("DIR_PATH='%s'", dir.Path))
		lines = append(lines, "DIR_PATH=$(echo \"$DIR_PATH\" | envsubst)")
		lines = append(lines, "if [ ! -d \"$DIR_PATH\" ]; then")
		lines = append(lines, "    log_info \"Creating directory: $DIR_PATH\"")

		if dir.Mode != 0 {
			lines = append(lines, fmt.Sprintf("    mkdir -p \"$DIR_PATH\" && chmod %o \"$DIR_PATH\"", dir.Mode))
		} else {
			lines = append(lines, "    mkdir -p \"$DIR_PATH\"")
		}

		lines = append(lines, "    if [ $? -eq 0 ]; then")
		lines = append(lines, "        log_success \"Directory created: $DIR_PATH\"")
		lines = append(lines, "    else")
		lines = append(lines, "        log_error \"Failed to create directory: $DIR_PATH\"")
		lines = append(lines, "    fi")
		lines = append(lines, "else")
		lines = append(lines, "    log_info \"Directory already exists: $DIR_PATH\"")
		lines = append(lines, "fi")
		lines = append(lines, "")
	}

	// Fix permissions on kit directories (common issue)
	lines = append(lines, "# Fix permissions on kit directories")
	lines = append(lines, "if [ -d \"$HOME/ocfp/kits\" ]; then")
	lines = append(lines, "    log_info 'Checking for directories with wrong permissions in ~/ocfp/kits'")
	lines = append(lines, "    find \"$HOME/ocfp/kits\" -type d -perm 000 -exec chmod 755 {} \\; 2>/dev/null || true")
	lines = append(lines, "    log_success 'Kit directory permissions fixed'")
	lines = append(lines, "fi")
	lines = append(lines, "")

	// Set ownership of OCFP directories
	lines = append(lines, "# Set ownership of OCFP directories")
	lines = append(lines, "log_info 'Setting ownership of ~/ocfp to $USER:$USER'")
	lines = append(lines, "chown -R \"$USER:$USER\" \"$HOME/ocfp\" 2>/dev/null || true")
	lines = append(lines, "")

	// Create symlinks
	symlinks := dm.GetOCFPSymlinks()

	lines = append(lines, "# Create OCFP symlinks")

	for linkPath, targetPath := range symlinks {
		expandedLink := dm.expandVariables(linkPath)
		expandedTarget := dm.expandVariables(targetPath)

		lines = append(lines, fmt.Sprintf("LINK_PATH='%s'", expandedLink))
		lines = append(lines, fmt.Sprintf("TARGET_PATH='%s'", expandedTarget))
		lines = append(lines, "LINK_PATH=$(echo \"$LINK_PATH\" | envsubst)")
		lines = append(lines, "TARGET_PATH=$(echo \"$TARGET_PATH\" | envsubst)")
		lines = append(lines, "")
		lines = append(lines, "if [ ! -e \"$LINK_PATH\" ]; then")
		lines = append(lines, "    log_info \"Creating symlink: $LINK_PATH -> $TARGET_PATH\"")
		lines = append(lines, "    ln -sf \"$TARGET_PATH\" \"$LINK_PATH\"")
		lines = append(lines, "    if [ $? -eq 0 ]; then")
		lines = append(lines, "        log_success \"Symlink created: $LINK_PATH\"")
		lines = append(lines, "    else")
		lines = append(lines, "        log_error \"Failed to create symlink: $LINK_PATH\"")
		lines = append(lines, "    fi")
		lines = append(lines, "else")
		lines = append(lines, "    log_info \"Symlink already exists: $LINK_PATH\"")
		lines = append(lines, "    if [ -L \"$LINK_PATH\" ]; then")
		lines = append(lines, "        ACTUAL_TARGET=$(readlink \"$LINK_PATH\")")
		lines = append(lines, "        log_info \"  Points to: $ACTUAL_TARGET\"")
		lines = append(lines, "    fi")
		lines = append(lines, "fi")
		lines = append(lines, "")
	}

	// Log final directory structure status
	lines = append(lines, "# Log final OCFP directory structure")
	lines = append(lines, "log_info 'Final OCFP directory structure:'")

	checkDirs := []string{
		"${HOME}/ocfp",
		"${HOME}/ocfp/deployments",
		"${HOME}/ocfp/releases",
		"${HOME}/ocfp/artifacts",
		"${HOME}/ocfp/cli",
		"${HOME}/bin",
	}

	for _, dir := range checkDirs {
		expandedDir := dm.expandVariables(dir)
		lines = append(lines, fmt.Sprintf("if [ -d '%s' ]; then", expandedDir))
		lines = append(lines, fmt.Sprintf("    log_info '  ✓ %s'", expandedDir))
		lines = append(lines, "else")
		lines = append(lines, fmt.Sprintf("    log_warning '  ✗ %s (missing)'", expandedDir))
		lines = append(lines, "fi")
	}

	lines = append(lines, "")

	return strings.Join(lines, "\n")
}

// GenerateOCFPCLISetupScript generates script for OCFP CLI setup.
func (dm *DirectoryManager) GenerateOCFPCLISetupScript(ctx context.Context) string {
	var lines []string

	lines = append(lines, "# OCFP CLI setup")
	lines = append(lines, "")

	// Check multiple possible locations for the ocfp binary
	lines = append(lines, "# Check for OCFP binary locations")
	lines = append(lines, "OCFP_LOCATIONS=(")
	lines = append(lines, `    "${HOME}/ocfp/ocfp-cli/bin/ocfp"`)
	lines = append(lines, `    "${HOME}/ocfp/cli/bin/ocfp"`)
	lines = append(lines, `    "${HOME}/ocfp/cli/ocfp"`)
	lines = append(lines, `    "/usr/local/bin/ocfp"`)
	lines = append(lines, ")")
	lines = append(lines, "")

	lines = append(lines, "OCFP_BIN=\"\"")
	lines = append(lines, "for location in \"${OCFP_LOCATIONS[@]}\"; do")
	lines = append(lines, "    if [ -f \"$location\" ]; then")
	lines = append(lines, "        OCFP_BIN=\"$location\"")
	lines = append(lines, "        log_info \"Found ocfp binary at: $location\"")
	lines = append(lines, "        break")
	lines = append(lines, "    fi")
	lines = append(lines, "done")
	lines = append(lines, "")

	// Create symlink if binary found
	lines = append(lines, "if [ -n \"$OCFP_BIN\" ]; then")
	lines = append(lines, "    OCFP_LINK=\"$HOME/bin/ocfp\"")
	lines = append(lines, "    if [ ! -e \"$OCFP_LINK\" ]; then")
	lines = append(lines, "        log_info \"Creating ocfp symlink: $OCFP_LINK -> $OCFP_BIN\"")
	lines = append(lines, "        ln -sf \"$OCFP_BIN\" \"$OCFP_LINK\"")
	lines = append(lines, "        log_success \"OCFP symlink created\"")
	lines = append(lines, "    else")
	lines = append(lines, "        log_info \"OCFP symlink already exists\"")
	lines = append(lines, "    fi")
	lines = append(lines, "else")
	lines = append(lines, "    log_info 'OCFP binary not found yet, will be available after OCFP CLI is copied'")
	lines = append(lines, "fi")
	lines = append(lines, "")

	return strings.Join(lines, "\n")
}

// expandVariables expands environment variables in strings.
func (dm *DirectoryManager) expandVariables(text string) string {
	text = strings.ReplaceAll(text, "${HOME}", "$HOME")
	text = strings.ReplaceAll(text, "${USER}", "$USER")
	text = strings.ReplaceAll(text, "${OCFP_BLOC_NAME}", "$OCFP_BLOC_NAME")

	return text
}
