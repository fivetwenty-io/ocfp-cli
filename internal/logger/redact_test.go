package logger

import (
	"strings"
	"testing"
)

func TestRedactSecrets_ExportSingleQuoted(t *testing.T) {
	t.Parallel()

	in := `export GITHUB_TOKEN='gho_FAKEFAKEFAKEFAKEFAKE1234'`

	got := RedactSecrets(in)

	if strings.Contains(got, "FAKEFAKEFAKEFAKEFAKE1234") {
		t.Fatalf("expected token value to be redacted, got: %q", got)
	}

	want := `export GITHUB_TOKEN='[REDACTED]'`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRedactSecrets_ExportDoubleQuoted(t *testing.T) {
	t.Parallel()

	in := `export VAULT_TOKEN="s.abcdef0123456789"`

	got := RedactSecrets(in)

	if strings.Contains(got, "abcdef0123456789") {
		t.Fatalf("expected token value to be redacted, got: %q", got)
	}

	want := `export VAULT_TOKEN="[REDACTED]"`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRedactSecrets_ExportVariantSecretNames(t *testing.T) {
	t.Parallel()

	cases := []string{
		`export DB_PASSWORD='hunter2'`,
		`export DB_PASSWD='hunter2'`,
		`export SOME_CREDENTIAL='hunter2'`,
		`export MY_APIKEY='hunter2'`,
		`export MY_API_KEY='hunter2'`,
		`export TLS_PRIVATE_KEY='hunter2'`,
		`export SOME_SECRET='hunter2'`,
	}

	for _, in := range cases {
		got := RedactSecrets(in)
		if strings.Contains(got, "hunter2") {
			t.Errorf("expected value redacted for input %q, got %q", in, got)
		}

		if !strings.Contains(got, "[REDACTED]") {
			t.Errorf("expected [REDACTED] marker in output for input %q, got %q", in, got)
		}
	}
}

func TestRedactSecrets_LeavesNonSecretExportsIntact(t *testing.T) {
	t.Parallel()

	in := "export OCFP_BLOC='pve-cpi'\nexport OCFP_PROVIDER='pve'\n"

	got := RedactSecrets(in)

	if got != in {
		t.Fatalf("expected non-secret export lines untouched, got %q", got)
	}
}

func TestRedactSecrets_GitHubTokenLiteralOutsideExport(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"ghp token in url": "https://x-access-token:ghp_FAKEFAKEFAKEFAKEFAKE1234@github.com/org/repo.git",
		"gho token bare":   "token=gho_FAKEFAKEFAKEFAKEFAKE1234",
		"ghu token bare":   "ghu_FAKEFAKEFAKEFAKEFAKE1234",
		"ghs token bare":   "ghs_FAKEFAKEFAKEFAKEFAKE1234",
		"ghr token bare":   "ghr_FAKEFAKEFAKEFAKEFAKE1234",
		"github_pat token": "github_pat_FAKEFAKEFAKEFAKEFAKE1234_moretext",
	}

	for name, in := range cases {
		got := RedactSecrets(in)
		if strings.Contains(got, "FAKEFAKEFAKEFAKEFAKE1234") {
			t.Errorf("%s: expected token literal redacted, got %q", name, got)
		}

		if !strings.Contains(got, "[REDACTED]") {
			t.Errorf("%s: expected [REDACTED] marker, got %q", name, got)
		}
	}
}

func TestRedactSecrets_AuthorizationHeader(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"token scheme":  "Authorization: token ghp_FAKEFAKEFAKEFAKEFAKE1234",
		"Bearer scheme": "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.fake.signature",
		"Basic scheme":  "Authorization: Basic dXNlcjpwYXNz",
	}

	for name, in := range cases {
		got := RedactSecrets(in)

		if strings.Contains(got, "ghp_FAKEFAKEFAKEFAKEFAKE1234") ||
			strings.Contains(got, "eyJhbGciOiJIUzI1NiJ9.fake.signature") ||
			strings.Contains(got, "dXNlcjpwYXNz") {
			t.Errorf("%s: expected credential redacted, got %q", name, got)
		}

		if !strings.Contains(got, "[REDACTED]") {
			t.Errorf("%s: expected [REDACTED] marker, got %q", name, got)
		}
	}
}

func TestRedactSecrets_EmptyString(t *testing.T) {
	t.Parallel()

	if got := RedactSecrets(""); got != "" {
		t.Fatalf("expected empty string to pass through unchanged, got %q", got)
	}
}

// TestRedactSecrets_FullRenderedScript exercises RedactSecrets against a
// realistic rendering of wrapScriptWithFunctions' env-export block, as
// produced for an actual bastion provisioning command, and asserts that the
// token value is gone while the surrounding script text (preamble, other env
// vars, and the trailing script body) is left intact.
func TestRedactSecrets_FullRenderedScript(t *testing.T) {
	t.Parallel()

	const fakeToken = "gho_FAKEFAKEFAKEFAKEFAKE1234" //nolint:gosec // test fixture, not a real credential

	script := `#!/bin/bash
set -euo pipefail

# Export OCFP environment variables
export OCFP_BLOC='pve-cpi'
export OCFP_PROVIDER='pve'
export GITHUB_TOKEN='` + fakeToken + `'
# Linuxbrew on PATH (phases run non-login)
if [ -x /home/linuxbrew/.linuxbrew/bin/brew ]; then eval "$(/home/linuxbrew/.linuxbrew/bin/brew shellenv)"; fi

echo "provisioning bastion"
`

	got := RedactSecrets(script)

	if strings.Contains(got, fakeToken) {
		t.Fatalf("expected token value to be redacted from rendered script, got:\n%s", got)
	}

	for _, want := range []string{
		"#!/bin/bash",
		"set -euo pipefail",
		"# Export OCFP environment variables",
		"export OCFP_BLOC='pve-cpi'",
		"export OCFP_PROVIDER='pve'",
		"export GITHUB_TOKEN='[REDACTED]'",
		"# Linuxbrew on PATH (phases run non-login)",
		`if [ -x /home/linuxbrew/.linuxbrew/bin/brew ]; then eval "$(/home/linuxbrew/.linuxbrew/bin/brew shellenv)"; fi`,
		`echo "provisioning bastion"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("expected redacted script to still contain %q, got:\n%s", want, got)
		}
	}
}

// escapeShellStringLikePhasesGo reproduces, byte-for-byte, the transform
// applied by (*Manager).escapeShellString in internal/bastion/phases.go:
//
//	escaped := strings.ReplaceAll(script, "'", "'\"'\"'")
//	return fmt.Sprintf("'%s'", escaped)
//
// It is duplicated here rather than imported because internal/bastion
// imports internal/logger (for RedactSecrets itself), so importing
// internal/bastion from this test would create an import cycle. If
// escapeShellString in internal/bastion/phases.go ever changes, this copy
// must be updated to match, or this regression test stops testing the real
// production leak path it exists to cover.
func escapeShellStringLikePhasesGo(script string) string {
	escaped := strings.ReplaceAll(script, "'", `'"'"'`)

	return "'" + escaped + "'"
}

// TestRedactSecrets_EscapedCommand_QuoteFoldingVariant reproduces the actual
// leak the adversarial review found: the string reaching the log call sites
// in internal/bastion is not the raw rendered script but that script run
// through escapeShellString and prefixed with "bash -c ", exactly as
// (*Manager).executeScript does before calling sshClient.ExecuteCommand.
// Every `'` in the script - including the ones bounding
// `export NAME='value'` - becomes the shell quote-folding sequence
// `'"'"'`, which sits directly between `=` and the secret value. Before the
// fix, exportSecretSingleQuoteRe matched a bogus 3-byte span inside that
// escape sequence and let the real secret value through untouched. This
// test asserts both a GITHUB_TOKEN and an AWS_SECRET_ACCESS_KEY (the kind of
// secret the validator confirmed leaks via addAWSEnvVars-populated env
// vars) are fully redacted once escaped and wrapped exactly as production
// does.
func TestRedactSecrets_EscapedCommand_QuoteFoldingVariant(t *testing.T) {
	t.Parallel()

	const fakeGithubToken = "gho_FAKEFAKEFAKEFAKEFAKE1234"    //nolint:gosec // test fixture, not a real credential
	const fakeAWSSecret = "FAKEAWSSECRETfakeAWSsecret1234567" //nolint:gosec // test fixture, not a real credential

	script := "#!/bin/bash\n" +
		"set -euo pipefail\n" +
		"# Export OCFP environment variables\n" +
		"export OCFP_BLOC='pve-cpi'\n" +
		"export GITHUB_TOKEN='" + fakeGithubToken + "'\n" +
		"export AWS_SECRET_ACCESS_KEY='" + fakeAWSSecret + "'\n" +
		"echo \"provisioning bastion\"\n"

	// Reproduce the real production transform: escapeShellString(script)
	// wrapped as "bash -c <escaped>", matching executeScript in
	// internal/bastion/phases.go exactly.
	cmd := "bash -c " + escapeShellStringLikePhasesGo(script)

	// Sanity check the fixture actually reproduces the bug's precondition:
	// the quote-folding sequence must appear directly after each secret's
	// `=`, not a bare `'`.
	if !strings.Contains(cmd, `GITHUB_TOKEN='"'"'`+fakeGithubToken) {
		t.Fatalf("test fixture did not reproduce the quote-folding escape before the token; cmd: %q", cmd)
	}

	got := RedactSecrets(cmd)

	if strings.Contains(got, fakeGithubToken) {
		t.Fatalf("expected GITHUB_TOKEN value to be redacted from escaped command, got:\n%s", got)
	}

	if strings.Contains(got, fakeAWSSecret) {
		t.Fatalf("expected AWS_SECRET_ACCESS_KEY value to be redacted from escaped command, got:\n%s", got)
	}

	if strings.Count(got, "[REDACTED]") != 2 { //nolint:mnd
		t.Fatalf("expected exactly 2 [REDACTED] markers (one per secret), got %d in:\n%s", strings.Count(got, "[REDACTED]"), got)
	}

	for _, want := range []string{
		"#!/bin/bash",
		"set -euo pipefail",
		"export OCFP_BLOC='pve-cpi'",
		`echo "provisioning bastion"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("expected redacted escaped command to still contain %q, got:\n%s", want, got)
		}
	}
}

// TestRedactSecrets_EscapedCommand_BackslashQuoteVariant covers the rarer
// quote-folding form some shell-quoting implementations use instead of the
// double-quote form:
//
//	close quote, backslash-escaped literal quote, reopen quote:  '\''
//
// This makes sure RedactSecrets normalizes both forms.
func TestRedactSecrets_EscapedCommand_BackslashQuoteVariant(t *testing.T) {
	t.Parallel()

	const fakeToken = "gho_FAKEFAKEFAKEFAKEFAKE9999" //nolint:gosec // test fixture, not a real credential

	// Build the same shape escapeShellString produces, but using the `'\''`
	// folding variant instead of `'"'"'`.
	script := "export GITHUB_TOKEN='" + fakeToken + "'\n"
	escaped := strings.ReplaceAll(script, "'", `'\''`)
	cmd := "bash -c '" + escaped + "'"

	got := RedactSecrets(cmd)

	if strings.Contains(got, fakeToken) {
		t.Fatalf("expected token value to be redacted from backslash-quote-folded command, got:\n%s", got)
	}

	if !strings.Contains(got, "export GITHUB_TOKEN='[REDACTED]'") {
		t.Fatalf("expected redacted export line, got:\n%s", got)
	}
}
