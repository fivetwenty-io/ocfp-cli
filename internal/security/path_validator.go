package security

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	// Valid path characters: alphanumeric, dash, underscore, dot, slash.

	// Disallowed path patterns for security.
	dangerousPatterns = []*regexp.Regexp{
		regexp.MustCompile(`\.\.`),        // Directory traversal
		regexp.MustCompile(`^/proc`),      // Process filesystem
		regexp.MustCompile(`^/sys`),       // System filesystem
		regexp.MustCompile(`^/dev`),       // Device filesystem
		regexp.MustCompile(`/etc/passwd`), // System password file
		regexp.MustCompile(`/etc/shadow`), // System shadow file
		regexp.MustCompile(`\.ssh/id_`),   // SSH private keys (unless specifically allowed)
	}
)

// ValidatePath validates a file path for security.
func ValidatePath(path string) error {
	if path == "" {
		return errors.New("empty path")
	}

	// Clean the path
	cleanPath := filepath.Clean(path)

	// Check for dangerous patterns
	for _, pattern := range dangerousPatterns {
		if pattern.MatchString(cleanPath) {
			return fmt.Errorf("path contains dangerous pattern: %s", path)
		}
	}

	// Check for shell metacharacters
	if strings.ContainsAny(path, ";|&`$()") {
		return fmt.Errorf("path contains shell metacharacters: %s", path)
	}

	return nil
}

// ValidatePathWithPattern validates a path against a specific pattern.
func ValidatePathWithPattern(path string, pattern *regexp.Regexp) error {
	err := ValidatePath(path)
	if err != nil {
		return err
	}

	if pattern != nil && !pattern.MatchString(path) {
		return fmt.Errorf("path does not match required pattern: %s", path)
	}

	return nil
}

// ValidateConfigPath validates paths used for configuration files.
func ValidateConfigPath(path string) error {
	configPattern := regexp.MustCompile(`^[a-zA-Z0-9/._-]+\.(yml|yaml|json|toml)$`)

	return ValidatePathWithPattern(path, configPattern)
}

// ValidateSSHKeyPath validates SSH key file paths.
func ValidateSSHKeyPath(path string) error {
	keyPattern := regexp.MustCompile(`^[a-zA-Z0-9/._-]+$`)

	return ValidatePathWithPattern(path, keyPattern)
}

// ValidateInput validates input for command injection prevention.
func ValidateInput(input string, pattern *regexp.Regexp) error {
	if pattern != nil && !pattern.MatchString(input) {
		return fmt.Errorf("invalid input: %s", input)
	}

	if strings.Contains(input, ";") || strings.Contains(input, "|") || strings.Contains(input, "&") {
		return fmt.Errorf("input contains shell metacharacters: %s", input)
	}

	return nil
}
