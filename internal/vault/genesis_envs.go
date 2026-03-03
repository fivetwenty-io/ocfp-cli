package vault

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	commandExecutor   = defaultCommandExecutor //nolint:gochecknoglobals // test override
	absolutePathRegex = regexp.MustCompile(`(/[^ \t\n\r"',:]+)`)
	tildePathRegex    = regexp.MustCompile(`(~[^ \t\n\r"',:]+)`)
)

func defaultCommandExecutor(name string, args ...string) (string, error) {
	//nolint:noctx // Legacy exec without context, refactor separately
	cmd := exec.Command(name, args...) // #nosec G204 - arguments originate from controlled fallbacks
	output, err := cmd.CombinedOutput()

	return string(output), err
}

// discoverGenesisEnvironmentCandidates attempts to locate Genesis environment directories by invoking
// `genesis envs` using several fallbacks that mirror the legacy Perl implementation.
func (gi *GenesisIntegration) discoverGenesisEnvironmentCandidates(_ context.Context) []string {
	attempts := [][]string{{"genesis", "envs", "--json"}, {"genesis", "envs"}}

	for _, attempt := range attempts {
		if paths := gi.tryGenesisCommand(attempt...); len(paths) > 0 {
			return paths
		}
	}

	whichOutput, err := commandExecutor("which", "genesis")
	if err != nil {
		gi.logger.Debugw("which genesis failed", "error", err)
	} else {
		bin := strings.TrimSpace(whichOutput)
		if bin != "" {
			bin = strings.Split(bin, "\n")[0]
			commands := [][]string{{bin, "envs", "--json"}, {bin, "envs"}}

			for _, attempt := range commands {
				if paths := gi.tryGenesisCommand(attempt...); len(paths) > 0 {
					return paths
				}
			}
		}
	}

	shellAttempts := []string{"genesis envs --json 2>&1", "genesis envs 2>&1"}
	for _, command := range shellAttempts {
		if paths := gi.tryGenesisCommand("sh", "-c", command); len(paths) > 0 {
			return paths
		}
	}

	return nil
}

func (gi *GenesisIntegration) tryGenesisCommand(command ...string) []string {
	if len(command) == 0 {
		return nil
	}

	output, err := commandExecutor(command[0], command[1:]...)
	trimmedOutput := strings.TrimSpace(output)

	if err != nil {
		gi.logger.Debugw("genesis command produced error", "command", strings.Join(command, " "), "error", err, "output", trimmedOutput)
	} else {
		gi.logger.Debugw("genesis command executed", "command", strings.Join(command, " "))
	}

	paths := gi.extractGenesisPaths(trimmedOutput)
	if len(paths) > 0 {
		gi.logger.Debugw("discovered genesis environment paths", "paths", paths, "command", strings.Join(command, " "))
	}

	return paths
}

func (gi *GenesisIntegration) extractGenesisPaths(output string) []string {
	if output == "" {
		return nil
	}

	// Try structured JSON extraction first
	if paths := gi.extractFromJSON(output); len(paths) > 0 {
		return uniquePaths(paths)
	}

	// Fall back to regex-based path extraction
	return uniquePaths(gi.extractFromRegex(output))
}

// envEntry represents a single environment entry from genesis JSON output.
type envEntry struct {
	Path      string `json:"path"`
	Directory string `json:"directory"`
	EnvPath   string `json:"environment_path"`
	EnvDir    string `json:"environment_dir"`
	File      string `json:"file"`
	EnvFile   string `json:"env_file"`
	Workspace string `json:"workspace"`
}

// extractFromJSON attempts to parse the output as JSON and extract genesis paths.
// Returns nil if the output is not valid JSON or contains no environment entries.
func (gi *GenesisIntegration) extractFromJSON(output string) []string {
	var jsonPayload struct {
		Environments []envEntry `json:"environments"`
	}

	err := json.Unmarshal([]byte(output), &jsonPayload)
	if err != nil {
		return nil
	}

	var paths []string

	for _, entry := range jsonPayload.Environments {
		paths = append(paths, gi.collectCandidatePaths(entry.Path, entry.Directory, entry.EnvPath, entry.EnvDir, entry.Workspace)...)
		paths = append(paths, gi.normalizedFileDir(entry.File)...)
		paths = append(paths, gi.normalizedFileDir(entry.EnvFile)...)
	}

	return paths
}

// normalizedFileDir normalizes a file path and returns its directory as a single-element slice,
// or nil if the path is empty or cannot be normalized.
func (gi *GenesisIntegration) normalizedFileDir(filePath string) []string {
	if filePath == "" {
		return nil
	}

	if normalized := gi.normalizeGenesisPath(filePath); normalized != "" {
		return []string{filepath.Dir(normalized)}
	}

	return nil
}

// extractFromRegex uses regex patterns to find absolute and tilde paths in the output,
// then validates and normalizes them.
func (gi *GenesisIntegration) extractFromRegex(output string) []string {
	matches := absolutePathRegex.FindAllString(output, -1)
	matches = append(matches, tildePathRegex.FindAllString(output, -1)...)

	var paths []string

	for _, match := range matches {
		normalized := gi.normalizeGenesisPath(match)
		if normalized == "" {
			continue
		}

		info, err := os.Stat(normalized)
		if err != nil {
			continue
		}

		if info.IsDir() {
			paths = append(paths, normalized)

			continue
		}

		paths = append(paths, gi.yamlFileDir(normalized)...)
	}

	return paths
}

// yamlFileDir returns the parent directory of a YAML file path as a single-element slice.
// Returns nil if the file is not a YAML file or its directory does not exist.
func (gi *GenesisIntegration) yamlFileDir(normalized string) []string {
	switch strings.ToLower(filepath.Ext(normalized)) {
	case ".yml", ".yaml":
		dir := filepath.Dir(normalized)

		info, statErr := os.Stat(dir)
		if statErr == nil && info.IsDir() {
			return []string{dir}
		}
	}

	return nil
}

func (gi *GenesisIntegration) collectCandidatePaths(values ...string) []string {
	var paths []string

	for _, value := range values {
		normalized := gi.normalizeGenesisPath(value)
		if normalized == "" {
			continue
		}

		info, err := os.Stat(normalized)
		if err != nil {
			continue
		}

		if info.IsDir() {
			paths = append(paths, normalized)

			continue
		}

		switch strings.ToLower(filepath.Ext(normalized)) {
		case ".yml", ".yaml":
			paths = append(paths, filepath.Dir(normalized))
		}
	}

	return paths
}

func (gi *GenesisIntegration) normalizeGenesisPath(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}

	value = strings.Trim(value, "\"'()[]{}")
	value = strings.TrimSuffix(value, ",")
	value = strings.TrimSuffix(value, ";")
	value = strings.TrimSuffix(value, ":")

	if strings.Contains(value, "://") && !strings.HasPrefix(value, "file://") {
		return ""
	}

	if strings.Contains(value, "=") {
		const maxParts = 2

		parts := strings.SplitN(value, "=", maxParts)
		value = strings.TrimSpace(parts[len(parts)-1])
	}

	if strings.Contains(value, "::") {
		parts := strings.Split(value, "::")
		value = strings.TrimSpace(parts[len(parts)-1])
	}

	value = strings.ReplaceAll(value, "\\", "/")

	switch {
	case strings.HasPrefix(value, "~/"):
		home, homeErr := os.UserHomeDir()
		if homeErr == nil {
			value = filepath.Join(home, value[2:])
		}
	case value == "~":
		home2, homeErr2 := os.UserHomeDir()
		if homeErr2 == nil {
			value = home2
		}
	case strings.HasPrefix(value, "./") || strings.HasPrefix(value, "../"):
		abs, absErr := filepath.Abs(value)
		if absErr == nil {
			value = abs
		}
	}

	if value == "" {
		return ""
	}

	return filepath.Clean(value)
}

func uniquePaths(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(paths))

	ordered := make([]string, 0, len(paths))
	for _, path := range paths {
		if path == "" {
			continue
		}

		if _, found := seen[path]; found {
			continue
		}

		seen[path] = struct{}{}
		ordered = append(ordered, path)
	}

	return ordered
}
