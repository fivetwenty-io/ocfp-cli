package security

import (
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	// Valid path characters: alphanumeric, dash, underscore, dot, slash.

	// Disallowed path patterns for security.
	dangerousPatterns = []*regexp.Regexp{ //nolint:gochecknoglobals // compiled regexps reused for efficiency
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
		return ErrEmptyPath
	}

	// Null bytes can truncate paths at the OS level and bypass suffix checks.
	if strings.ContainsRune(path, '\x00') {
		return ErrPathContainsNullByte(path)
	}

	// URL-encoded traversal: decode once and reject if the decoded form introduces
	// a ".." path component that was hidden by percent-encoding.
	if decoded, err := url.PathUnescape(path); err != nil {
		// Malformed percent-encoding — reject.
		return ErrPathContainsDangerousPattern(path)
	} else if decoded != path {
		// Encoding was present; check whether the decoded form traverses.
		for _, seg := range strings.Split(decoded, "/") {
			if seg == ".." {
				return ErrPathContainsDangerousPattern(path)
			}
		}
	}

	// Clean the path
	cleanPath := filepath.Clean(path)

	// Check for dangerous patterns
	for _, pattern := range dangerousPatterns {
		if pattern.MatchString(cleanPath) {
			return ErrPathContainsDangerousPattern(path)
		}
	}

	// Check for shell metacharacters
	if strings.ContainsAny(path, ";|&`$()") {
		return ErrPathContainsShellMetacharacters(path)
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
		return ErrPathDoesNotMatchPattern(path)
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
		return ErrInvalidInput(input)
	}

	if strings.Contains(input, ";") || strings.Contains(input, "|") || strings.Contains(input, "&") {
		return ErrInputContainsShellMetacharacters(input)
	}

	return nil
}
