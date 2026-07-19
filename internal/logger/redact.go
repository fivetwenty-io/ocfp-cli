package logger

import (
	"regexp"
	"strings"
)

// Regexes compiled once at package scope so RedactSecrets stays cheap even
// when called on every Debug-level log entry.
var (
	// exportSecretSingleQuoteRe matches `export NAME='value'` shell lines where
	// NAME looks like it holds a secret (token, password, credential, etc.).
	exportSecretSingleQuoteRe = regexp.MustCompile(
		`(?i)(export\s+[A-Za-z_][A-Za-z0-9_]*(?:TOKEN|SECRET|PASSWORD|PASSWD|CREDENTIAL|APIKEY|API_KEY|PRIVATE_KEY)[A-Za-z0-9_]*=)'[^']*'`,
	)

	// exportSecretDoubleQuoteRe is the double-quoted counterpart of exportSecretSingleQuoteRe.
	exportSecretDoubleQuoteRe = regexp.MustCompile(
		`(?i)(export\s+[A-Za-z_][A-Za-z0-9_]*(?:TOKEN|SECRET|PASSWORD|PASSWD|CREDENTIAL|APIKEY|API_KEY|PRIVATE_KEY)[A-Za-z0-9_]*=)"[^"]*"`,
	)

	// githubTokenLiteralRe matches raw GitHub token literals regardless of the
	// surrounding context (export lines, URLs, Authorization headers, etc.).
	githubTokenLiteralRe = regexp.MustCompile(
		`\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{16,}\b|\bgithub_pat_[A-Za-z0-9_]{16,}\b`,
	)

	// authHeaderRe matches `Authorization: <scheme> <credential>` and redacts only
	// the credential, keeping the scheme visible for debugging.
	authHeaderRe = regexp.MustCompile(
		`(?i)(Authorization:\s*(?:token|Bearer|Basic))\s+\S+`,
	)
)

// redactedPlaceholder is substituted for the redacted portion of a match.
const redactedPlaceholder = "[REDACTED]"

// RedactSecrets scrubs known secret-bearing patterns out of a string before it
// is written to a log entry. It is intentionally applied only to a small
// allow-list of log field values (command/script/stdout/stderr — see call
// sites in internal/bastion) that are known to carry rendered shell text,
// not to every log field, to keep the cost bounded to the places that can
// plausibly leak a secret.
//
// It does NOT mutate the string used to actually execute a command; callers
// must pass a copy intended only for logging.
//
// Before applying any pattern, it first undoes shell single-quote folding.
// The command string actually reaching the log call sites in
// internal/bastion is not the raw rendered script - it is that script
// wrapped for `bash -c` by escapeShellString (internal/bastion/phases.go),
// which replaces every literal single quote in the script (including the
// ones bounding an `export NAME=value` assignment) with one of these
// quote-folding escape sequences:
//
//	close quote, double-quoted literal quote, reopen quote:  '"'"'
//	close quote, backslash-escaped literal quote, reopen quote:  '\''
//
// That folding sits directly between the `=` and the secret value, so
// without undoing it first, the export-secret patterns below would match a
// bogus few-byte span inside the escape sequence itself and let the real
// value pass through unredacted. Collapsing either form back to a plain
// quote here only changes the copy of the string that ends up in a log
// entry - it is never used to build or execute a command - so a cosmetic
// de-escape of logged shell text is an acceptable trade for closing that
// gap.
func RedactSecrets(s string) string {
	if s == "" {
		return s
	}

	s = strings.ReplaceAll(s, `'"'"'`, "'")
	s = strings.ReplaceAll(s, `'\''`, "'")

	s = exportSecretSingleQuoteRe.ReplaceAllString(s, "${1}'"+redactedPlaceholder+"'")
	s = exportSecretDoubleQuoteRe.ReplaceAllString(s, `${1}"`+redactedPlaceholder+`"`)
	s = githubTokenLiteralRe.ReplaceAllString(s, redactedPlaceholder)
	s = authHeaderRe.ReplaceAllString(s, "${1} "+redactedPlaceholder)

	return s
}
