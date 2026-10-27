package blocinv

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"
)

// maxSafeMessageRunes caps how much of an error message a user or a log sees.
const maxSafeMessageRunes = 200

// sourceQuotingError matches the errors goccy/go-yaml returns for a document it
// cannot parse. Their text quotes the offending lines of the file, so a secret
// on one of those lines would reach whoever reads the message.
type sourceQuotingError interface {
	error
	GetMessage() string
	FormatError(colored, inclSource bool) string
}

// quotedSourceLine matches a numbered source line in a parser's error text,
// such as "> 4 |     auth_token: [..." or "   3 | provider: pve".
var quotedSourceLine = regexp.MustCompile(`(?m)^\s*>?\s*\d+\s*\|`)

// SafeMessage describes err without ever repeating file content. A parse
// failure becomes a fixed phrase that names path when there is one. Any other
// error keeps only its first line, trimmed to a short length, so a parser's
// quoted source lines never get through. It returns "" for a nil error.
func SafeMessage(err error, path string) string {
	if err == nil {
		return ""
	}

	if isParseError(err) {
		if path == "" {
			return "file cannot be parsed (file contents are not shown)"
		}

		return path + " cannot be parsed (file contents are not shown)"
	}

	return firstLine(err.Error())
}

// Sanitize returns an error whose text is SafeMessage and whose chain still
// reaches err, so errors.Is and errors.As keep working for callers that branch
// on the cause. It returns nil for a nil error.
func Sanitize(err error, path string) error {
	if err == nil {
		return nil
	}

	return &safeError{msg: SafeMessage(err, path), err: err}
}

// safeError carries a sanitized message over the original error.
type safeError struct {
	msg string
	err error
}

func (e *safeError) Error() string { return e.msg }

func (e *safeError) Unwrap() error { return e.err }

// isParseError reports whether err, or anything it wraps, is a parser failure
// that may quote file content.
func isParseError(err error) bool {
	var (
		yamlErr    sourceQuotingError
		jsonSyntax *json.SyntaxError
		jsonType   *json.UnmarshalTypeError
	)

	if errors.As(err, &yamlErr) || errors.As(err, &jsonSyntax) || errors.As(err, &jsonType) {
		return true
	}

	// An error that was flattened to text loses its type, but its quoted
	// source lines still give it away.
	return strings.Contains(err.Error(), "\n") && quotedSourceLine.MatchString(err.Error())
}

// firstLine returns the first non-empty line of text, shortened to the cap.
func firstLine(text string) string {
	line := strings.TrimSpace(text)
	if idx := strings.IndexAny(line, "\r\n"); idx >= 0 {
		line = strings.TrimSpace(line[:idx])
	}

	if utf8.RuneCountInString(line) <= maxSafeMessageRunes {
		return line
	}

	runes := []rune(line)

	return string(runes[:maxSafeMessageRunes]) + "..."
}
