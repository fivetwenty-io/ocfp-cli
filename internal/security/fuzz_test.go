package security_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/security"
)

// FuzzPathValidation verifies the invariant:
//
//	ValidatePath returns nil error AND the cleaned result stays within
//	the safe character set, OR it returns a non-nil error.
//
// A finding means ValidatePath returned nil for a path whose cleaned form
// contains ".." (escape possible).
func FuzzPathValidation(f *testing.F) {
	// Seed corpus — known traversal patterns and benign paths.
	seeds := []string{
		"../etc/passwd",
		"../../etc/shadow",
		"/proc/self/mem",
		"/sys/kernel/debug",
		"/dev/null",
		"/etc/passwd",
		"/etc/shadow",
		"/root/.ssh/id_rsa",
		"/home/user/.ssh/id_ed25519",
		"configs/env.yml",
		"genesis.yml",
		"",
		".",
		"..",
		"./ok",
		"%2e%2e/escape",
		"..%2f..%2fetc%2fpasswd",
		"/tmp/foo;bar",
		"/tmp/foo|evil",
		"cmd&inject",
		"back`tick`",
		"$(whoami)",
		"/normal/path/file.txt",
		"\x00null",
		`..\..\windows\system32`,
		"a/b/c/d/e/f",
	}

	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, path string) {
		err := security.ValidatePath(path)

		if err != nil {
			// Error returned — nothing to check further; validator correctly rejected.
			return
		}

		// Validator returned nil. Confirm the cleaned path does NOT contain "..".
		// If it does, the validator allowed a traversal-capable path — property violation.
		cleaned := filepath.Clean(path)
		if strings.Contains(cleaned, "..") {
			t.Errorf(
				"ValidatePath(%q) = nil but filepath.Clean(%q) = %q contains '..'; traversal escape possible",
				path, path, cleaned,
			)
		}
	})
}
