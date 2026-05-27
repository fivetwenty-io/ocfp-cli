package security_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/security"
)

// --- ValidatePath -----------------------------------------------------------

func TestValidatePath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		path    string
		wantErr bool
		errFrag string // substring expected in error message when wantErr=true
	}{
		// Empty path
		{name: "empty string", path: "", wantErr: true, errFrag: "empty path"},

		// Allowed paths — clean, no special chars
		{name: "simple relative", path: "configs/my-env.yml", wantErr: false},
		{name: "simple absolute", path: "/home/user/deployments/cf.yml", wantErr: false},
		{name: "single filename", path: "genesis.yml", wantErr: false},
		{name: "path with dot prefix", path: "/home/user/.genesis/config", wantErr: false},
		// NOTE: .genesis/config is allowed because ".." pattern does not appear
		// (it is a single dot, not double).

		// Directory traversal — literal raw path
		// ValidatePath runs filepath.Clean first, THEN checks the cleaned result.
		// Paths where Clean resolves ".." to a safe component therefore PASS the
		// dangerous-pattern check even though the raw string contained "..".
		// The remaining tests confirm the actual boundary.
		{name: "literal ../", path: "../escape", wantErr: true, errFrag: "dangerous"},
		// "../../etc/passwd" cleans to "../../etc/passwd" (relative, still has ..) — caught
		{name: "literal ../../etc/passwd", path: "../../etc/passwd", wantErr: true, errFrag: "dangerous"},
		// "/var/log/../etc/passwd" cleans to "/etc/passwd" — caught by /etc/passwd pattern
		{name: "absolute with .. resolves to /etc/passwd", path: "/var/log/../etc/passwd", wantErr: true, errFrag: "dangerous"},
		// "/a/b/../c" cleans to "/a/c" — no dangerous pattern remains; validator allows it
		{name: "double dot middle resolves safe", path: "/a/b/../c", wantErr: false},
		// "/a/b/.." cleans to "/a" — no dangerous pattern remains; validator allows it
		{name: "trailing .. resolves safe", path: "/a/b/..", wantErr: false},

		// Directory traversal — Windows-style (backslash not in POSIX path but worth confirming)
		// On darwin/linux filepath.Clean normalises \\; the regex catches the remaining ".."
		{name: "backslash traversal", path: `..\..\etc\passwd`, wantErr: true, errFrag: "dangerous"},

		// URL-encoded traversal — validator does NOT URL-decode inputs.
		// "%2e%2e/etc/passwd" contains "/etc/passwd" literally, so it IS caught
		// by the /etc/passwd pattern. A variant that does NOT contain a blocked
		// suffix (e.g., "%2e%2e/escape") passes — callers must URL-decode first.
		{name: "percent-encoded with /etc/passwd suffix", path: "%2e%2e/etc/passwd", wantErr: true, errFrag: "dangerous"},
		// This variant has no blocked suffix and no literal ".."; it passes validation.
		{name: "percent-encoded traversal no blocked suffix passes", path: "%2e%2e/escape", wantErr: false},

		// Dangerous absolute prefixes
		{name: "proc filesystem", path: "/proc/self/mem", wantErr: true, errFrag: "dangerous"},
		{name: "sys filesystem", path: "/sys/kernel/debug", wantErr: true, errFrag: "dangerous"},
		{name: "dev filesystem", path: "/dev/null", wantErr: true, errFrag: "dangerous"},
		{name: "etc/passwd exact", path: "/etc/passwd", wantErr: true, errFrag: "dangerous"},
		{name: "etc/shadow exact", path: "/etc/shadow", wantErr: true, errFrag: "dangerous"},
		{name: "ssh id_rsa", path: "/home/user/.ssh/id_rsa", wantErr: true, errFrag: "dangerous"},
		{name: "ssh id_ed25519", path: "/root/.ssh/id_ed25519", wantErr: true, errFrag: "dangerous"},

		// Shell metacharacters in path
		{name: "semicolon", path: "/tmp/foo;bar", wantErr: true, errFrag: "metacharacter"},
		{name: "pipe", path: "/tmp/foo|bar", wantErr: true, errFrag: "metacharacter"},
		{name: "ampersand", path: "/tmp/foo&bar", wantErr: true, errFrag: "metacharacter"},
		{name: "backtick", path: "/tmp/foo`date`", wantErr: true, errFrag: "metacharacter"},
		{name: "dollar", path: "/tmp/$HOME/file", wantErr: true, errFrag: "metacharacter"},
		{name: "paren open", path: "/tmp/foo(bar", wantErr: true, errFrag: "metacharacter"},
		{name: "paren close", path: "/tmp/foo)bar", wantErr: true, errFrag: "metacharacter"},

		// Null byte — filepath.Clean treats it as part of the string; the path
		// passes unless another rule catches it. Document the current behavior.
		// This is NOT expected to return an error under the current implementation.
		{name: "null byte in path", path: "/tmp/foo\x00bar", wantErr: false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := security.ValidatePath(tc.path)
			if tc.wantErr {
				if err == nil {
					t.Errorf("ValidatePath(%q) = nil, want error containing %q", tc.path, tc.errFrag)
					return
				}
				if tc.errFrag != "" && !strings.Contains(err.Error(), tc.errFrag) {
					t.Errorf("ValidatePath(%q) error = %q, want it to contain %q", tc.path, err.Error(), tc.errFrag)
				}
			} else {
				if err != nil {
					t.Errorf("ValidatePath(%q) = %v, want nil", tc.path, err)
				}
			}
		})
	}
}

// --- ValidatePathWithPattern ------------------------------------------------

func TestValidatePathWithPattern(t *testing.T) {
	t.Parallel()

	alphaPattern := regexp.MustCompile(`^[a-z/]+$`)

	tests := []struct {
		name    string
		path    string
		pattern *regexp.Regexp
		wantErr bool
		errFrag string
	}{
		// nil pattern falls through to ValidatePath only
		{name: "nil pattern valid path", path: "genesis.yml", pattern: nil, wantErr: false},
		{name: "nil pattern empty path", path: "", pattern: nil, wantErr: true, errFrag: "empty path"},

		// Pattern match — passes
		{name: "matches pattern", path: "/cfg/env", pattern: alphaPattern, wantErr: false},

		// Pattern no-match — rejected
		{name: "does not match pattern", path: "/cfg/Env", pattern: alphaPattern, wantErr: true, errFrag: "pattern"},
		{name: "pattern mismatch with digits", path: "/cfg/env1", pattern: alphaPattern, wantErr: true, errFrag: "pattern"},

		// ValidatePath rejection takes precedence over pattern check
		{name: "dangerous path with matching pattern", path: "/proc/self/mem", pattern: nil, wantErr: true, errFrag: "dangerous"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := security.ValidatePathWithPattern(tc.path, tc.pattern)
			if tc.wantErr {
				if err == nil {
					t.Errorf("ValidatePathWithPattern(%q, pattern) = nil, want error containing %q", tc.path, tc.errFrag)
					return
				}
				if tc.errFrag != "" && !strings.Contains(err.Error(), tc.errFrag) {
					t.Errorf("error = %q, want %q", err.Error(), tc.errFrag)
				}
			} else {
				if err != nil {
					t.Errorf("ValidatePathWithPattern(%q, pattern) = %v, want nil", tc.path, err)
				}
			}
		})
	}
}

// --- ValidateConfigPath -----------------------------------------------------

func TestValidateConfigPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{name: "valid yaml", path: "config/env.yml", wantErr: false},
		{name: "valid yaml uppercase ext reject", path: "config/env.YML", wantErr: true},
		{name: "valid yaml abs", path: "/etc/myapp/config.yaml", wantErr: false},
		{name: "valid json", path: "data/settings.json", wantErr: false},
		{name: "valid toml", path: "app/config.toml", wantErr: false},
		{name: "no extension", path: "config/env", wantErr: true},
		{name: "wrong extension", path: "config/env.conf", wantErr: true},
		{name: "traversal in config path", path: "../escape.yml", wantErr: true},
		{name: "shell meta in config path", path: "config/env;evil.yml", wantErr: true},
		{name: "empty path", path: "", wantErr: true},
		// Space not allowed by the pattern
		{name: "space in path", path: "config/my file.yml", wantErr: true},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := security.ValidateConfigPath(tc.path)
			if tc.wantErr && err == nil {
				t.Errorf("ValidateConfigPath(%q) = nil, want error", tc.path)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("ValidateConfigPath(%q) = %v, want nil", tc.path, err)
			}
		})
	}
}

// --- ValidateSSHKeyPath -----------------------------------------------------

func TestValidateSSHKeyPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{name: "alphanumeric path", path: "/home/user/mykey", wantErr: false},
		{name: "path with dots and dashes", path: "/home/user/.keys/deploy-key", wantErr: false},
		// ssh id_ triggers the dangerous-pattern rule in ValidatePath
		{name: "id_rsa path rejected by dangerous", path: "/root/.ssh/id_rsa", wantErr: true},
		{name: "traversal rejected", path: "../keys/id_rsa", wantErr: true},
		{name: "shell meta rejected", path: "/tmp/key;evil", wantErr: true},
		{name: "empty path", path: "", wantErr: true},
		// Space not allowed by the pattern
		{name: "space in path", path: "/home/user/my key", wantErr: true},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := security.ValidateSSHKeyPath(tc.path)
			if tc.wantErr && err == nil {
				t.Errorf("ValidateSSHKeyPath(%q) = nil, want error", tc.path)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("ValidateSSHKeyPath(%q) = %v, want nil", tc.path, err)
			}
		})
	}
}

// --- ValidateInput ----------------------------------------------------------

func TestValidateInput(t *testing.T) {
	t.Parallel()

	alphaNum := regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

	tests := []struct {
		name    string
		input   string
		pattern *regexp.Regexp
		wantErr bool
		errFrag string
	}{
		// nil pattern, clean input — only metachar check applies
		{name: "nil pattern clean", input: "hello-world", pattern: nil, wantErr: false},
		{name: "nil pattern semicolon", input: "hello;world", pattern: nil, wantErr: true, errFrag: "metacharacter"},
		{name: "nil pattern pipe", input: "hello|world", pattern: nil, wantErr: true, errFrag: "metacharacter"},
		{name: "nil pattern ampersand", input: "hello&world", pattern: nil, wantErr: true, errFrag: "metacharacter"},

		// with pattern
		{name: "matches pattern", input: "hello_world", pattern: alphaNum, wantErr: false},
		{name: "fails pattern", input: "hello world", pattern: alphaNum, wantErr: true, errFrag: "invalid input"},
		{name: "empty fails pattern", input: "", pattern: alphaNum, wantErr: true, errFrag: "invalid input"},

		// semicolon checked even with nil pattern (both metachar branches)
		{name: "semicolon-only char", input: ";", pattern: nil, wantErr: true, errFrag: "metacharacter"},
		{name: "pipe-only char", input: "|", pattern: nil, wantErr: true, errFrag: "metacharacter"},
		{name: "ampersand-only char", input: "&", pattern: nil, wantErr: true, errFrag: "metacharacter"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := security.ValidateInput(tc.input, tc.pattern)
			if tc.wantErr {
				if err == nil {
					t.Errorf("ValidateInput(%q, pattern) = nil, want error containing %q", tc.input, tc.errFrag)
					return
				}
				if tc.errFrag != "" && !strings.Contains(err.Error(), tc.errFrag) {
					t.Errorf("error = %q, want %q", err.Error(), tc.errFrag)
				}
			} else {
				if err != nil {
					t.Errorf("ValidateInput(%q, pattern) = %v, want nil", tc.input, err)
				}
			}
		})
	}
}
