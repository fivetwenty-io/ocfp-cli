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
		{Path: ocfpPath, Mode: directoryModeStandard, Owner: "", Group: "", Condition: ""},
		{Path: ocfpPath + "/cli", Mode: directoryModeStandard, Owner: "", Group: "", Condition: ""},
		{Path: ocfpPath + "/deployments", Mode: directoryModeStandard, Owner: "", Group: "", Condition: ""},
		{Path: ocfpPath + "/releases", Mode: directoryModeStandard, Owner: "", Group: "", Condition: ""},
		{Path: ocfpPath + "/artifacts", Mode: directoryModeStandard, Owner: "", Group: "", Condition: ""},
		{Path: ocfpPath + "/kits", Mode: directoryModeStandard, Owner: "", Group: "", Condition: ""},
		{Path: "${HOME}/.ocfp", Mode: directoryModeStandard, Owner: "", Group: "", Condition: ""},
		{Path: "${HOME}/.ocfp/logs", Mode: directoryModeStandard, Owner: "", Group: "", Condition: ""},
		{Path: "${HOME}/.ocfp/logs/provision", Mode: directoryModeStandard, Owner: "", Group: "", Condition: ""},
		{Path: "${HOME}/bin", Mode: directoryModeStandard, Owner: "", Group: "", Condition: ""},
		{Path: "${HOME}/.ssh", Mode: directoryModeSSH, Owner: "", Group: "", Condition: ""},
		{Path: "${HOME}/.genesis", Mode: directoryModeStandard, Owner: "", Group: "", Condition: ""},
		{Path: "${HOME}/.genesis/logs", Mode: directoryModeStandard, Owner: "", Group: "", Condition: ""},
		{Path: "${HOME}/.config", Mode: directoryModeStandard, Owner: "", Group: "", Condition: ""},
		{Path: "${HOME}/.config/nvim", Mode: directoryModeStandard, Owner: "", Group: "", Condition: ""},
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
	directories := dm.GetOCFPDirectories()
	lines := make([]string, 0, scriptBufferDirectoriesBase+scriptBufferDirectoriesPerItem*len(directories))

	lines = append(lines, "# OCFP directory structure setup")
	lines = append(lines, "")

	lines = append(lines, dm.generateDirectoryCreationScript(directories)...)
	lines = append(lines, dm.generatePermissionFixScript()...)
	lines = append(lines, dm.generateOwnershipScript()...)
	lines = append(lines, dm.generateSymlinkCreationScript()...)
	lines = append(lines, dm.generateDirectoryStatusScript()...)

	return strings.Join(lines, "\n")
}

// GenerateOCFPCLISetupScript generates script for OCFP CLI setup.
func (dm *DirectoryManager) GenerateOCFPCLISetupScript(ctx context.Context) string {
	// Fixed-size-ish script; preallocate a sensible capacity
	lines := make([]string, 0, scriptBufferBase)

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

func (dm *DirectoryManager) generateDirectoryCreationScript(directories []DirectoryConfig) []string {
	lines := []string{"# Create OCFP directories"}

	for _, dir := range directories {
		lines = append(lines, fmt.Sprintf("DIR_PATH=\"%s\"", dir.Path))
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

	return lines
}

func (dm *DirectoryManager) generatePermissionFixScript() []string {
	return []string{
		"# Fix permissions on kit directories",
		"if [ -d \"$HOME/ocfp/kits\" ]; then",
		"    log_info 'Checking for directories with wrong permissions in ~/ocfp/kits'",
		"    find \"$HOME/ocfp/kits\" -type d -perm 000 -exec chmod 755 {} \\; 2>/dev/null || true",
		"    log_success 'Kit directory permissions fixed'",
		"fi",
		"",
	}
}

func (dm *DirectoryManager) generateOwnershipScript() []string {
	return []string{
		"# Set ownership of OCFP directories",
		"log_info 'Setting ownership of ~/ocfp to $USER:$USER'",
		"chown -R \"$USER:$USER\" \"$HOME/ocfp\" 2>/dev/null || true",
		"",
	}
}

func (dm *DirectoryManager) generateSymlinkCreationScript() []string {
	symlinks := dm.GetOCFPSymlinks()
	lines := []string{"# Create OCFP symlinks"}

	for linkPath, targetPath := range symlinks {
		expandedLink := dm.expandVariables(linkPath)
		expandedTarget := dm.expandVariables(targetPath)

		lines = append(lines, fmt.Sprintf("LINK_PATH=\"%s\"", expandedLink))
		lines = append(lines, fmt.Sprintf("TARGET_PATH=\"%s\"", expandedTarget))
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

	return lines
}

func (dm *DirectoryManager) generateDirectoryStatusScript() []string {
	lines := []string{
		"# Log final OCFP directory structure",
		"log_info 'Final OCFP directory structure:'"}

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
		lines = append(lines, fmt.Sprintf("if [ -d \"%s\" ]; then", expandedDir))
		lines = append(lines, fmt.Sprintf("    log_info '  ✓ %s'", expandedDir))
		lines = append(lines, "else")
		lines = append(lines, fmt.Sprintf("    log_warning '  ✗ %s (missing)'", expandedDir))
		lines = append(lines, "fi")
	}

	lines = append(lines, "")

	return lines
}

// expandVariables expands environment variables in strings.
func (dm *DirectoryManager) expandVariables(text string) string {
	text = strings.ReplaceAll(text, "${HOME}", "$HOME")
	text = strings.ReplaceAll(text, "${USER}", "$USER")
	text = strings.ReplaceAll(text, "${OCFP_BLOC}", "$OCFP_BLOC")

	return text
}
