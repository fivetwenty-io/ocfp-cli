package blocinv

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"regexp"
	"strconv"
	"strings"
)

// Kind is a fixed description of one class of failure. Its text is written
// here, never taken from an error, so it is always safe to show.
type Kind string

// The classes of failure a sanitized message can report.
const (
	KindParse            Kind = "cannot be parsed (file contents are not shown)"
	KindFileMissing      Kind = "file does not exist"
	KindFileRead         Kind = "file cannot be read"
	KindConfigInvalid    Kind = "config is invalid"
	KindBlocNotInConfig  Kind = "bloc not found in config"
	KindProviderSetup    Kind = "provider setup failed"
	KindProviderConnect  Kind = "provider connection failed"
	KindProviderAuth     Kind = "provider authentication failed"
	KindProviderRequest  Kind = "provider request failed"
	KindPermissionDenied Kind = "permission denied"
	KindVMNotFound       Kind = "VM not found"
	KindTimeout          Kind = "timed out"
	KindCanceled         Kind = "canceled"
	KindUnexpected       Kind = "unexpected error"
)

// ClassifiedError is a failure reduced to a fixed class and a few fields that
// are safe to show. Its text is built from those alone, so nothing the cause
// says, such as a quoted config value or a provider's response body, can
// reach a user or a log through it. The cause stays reachable through Unwrap.
type ClassifiedError struct {
	Kind Kind
	// Path is a file path the failure concerns.
	Path string
	// VMID is the provider's id of the VM the failure concerns.
	VMID string
	// HTTPStatus is the provider's HTTP status code, or 0 when there is none.
	HTTPStatus int
	// Err is the cause. Its text is never shown.
	Err error
}

// Error renders the class and the safe fields. A parse failure keeps the
// phrasing "<path> cannot be parsed", which reads better than a class name.
func (e *ClassifiedError) Error() string {
	if e.Kind == KindParse {
		subject := "file"
		if e.Path != "" {
			subject = e.Path
		}

		return subject + " " + string(KindParse)
	}

	details := make([]string, 0, 3) //nolint:mnd // the three safe fields

	if e.Path != "" {
		details = append(details, e.Path)
	}

	if e.VMID != "" {
		details = append(details, "VM "+e.VMID)
	}

	if e.HTTPStatus != 0 {
		details = append(details, "HTTP "+strconv.Itoa(e.HTTPStatus))
	}

	if len(details) == 0 {
		return string(e.Kind)
	}

	return string(e.Kind) + " (" + strings.Join(details, ", ") + ")"
}

func (e *ClassifiedError) Unwrap() error { return e.Err }

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

// Classify reduces err to a ClassifiedError. When err already carries one, the
// outermost is returned as it is, since its creator knew more about the
// failure. Otherwise the class comes from what the error is, a parse failure,
// a timeout, or a file error, and path is the file it concerns; anything
// unrecognised is unexpected, and its text is dropped. It returns nil for a
// nil error.
func Classify(err error, path string) *ClassifiedError {
	if err == nil {
		return nil
	}

	var classified *ClassifiedError
	if errors.As(err, &classified) {
		return classified
	}

	result := &ClassifiedError{Kind: genericKind(err), Path: path, Err: err}

	var pathErr *fs.PathError
	if result.Path == "" && errors.As(err, &pathErr) {
		result.Path = pathErr.Path
	}

	return result
}

// genericKind classifies an error by the standard error types it wraps.
func genericKind(err error) Kind {
	var pathErr *fs.PathError

	switch {
	case isParseError(err):
		return KindParse
	case errors.Is(err, context.DeadlineExceeded):
		return KindTimeout
	case errors.Is(err, context.Canceled):
		return KindCanceled
	case errors.Is(err, fs.ErrPermission):
		return KindPermissionDenied
	case errors.Is(err, fs.ErrNotExist):
		return KindFileMissing
	case errors.As(err, &pathErr):
		return KindFileRead
	default:
		return KindUnexpected
	}
}

// SafeMessage describes err with a fixed class and only the fields that are
// safe to show, never with the error's own text, so neither file content nor
// a provider's response can get through. It returns "" for a nil error.
func SafeMessage(err error, path string) string {
	if err == nil {
		return ""
	}

	return Classify(err, path).Error()
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
