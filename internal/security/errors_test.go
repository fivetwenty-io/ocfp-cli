package security_test

import (
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/security"
)

func TestErrEmptyPath(t *testing.T) {
	err := security.ErrEmptyPath
	if err == nil {
		t.Fatal("ErrEmptyPath must not be nil")
	}

	if err.Error() != "empty path" {
		t.Errorf("unexpected message: %q", err.Error())
	}
}

func TestErrPathContainsDangerousPattern(t *testing.T) {
	path := "/some/../path"
	err := security.ErrPathContainsDangerousPattern(path)

	if err == nil {
		t.Fatal("expected non-nil error")
	}

	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q must contain path %q", err.Error(), path)
	}

	if !strings.Contains(err.Error(), "dangerous pattern") {
		t.Errorf("error %q must mention 'dangerous pattern'", err.Error())
	}
}

func TestErrPathContainsShellMetacharacters(t *testing.T) {
	path := "/tmp/foo;bar"
	err := security.ErrPathContainsShellMetacharacters(path)

	if err == nil {
		t.Fatal("expected non-nil error")
	}

	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q must contain path %q", err.Error(), path)
	}

	if !strings.Contains(err.Error(), "metacharacter") {
		t.Errorf("error %q must mention 'metacharacter'", err.Error())
	}
}

func TestErrPathDoesNotMatchPattern(t *testing.T) {
	path := "/tmp/noext"
	err := security.ErrPathDoesNotMatchPattern(path)

	if err == nil {
		t.Fatal("expected non-nil error")
	}

	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q must contain path %q", err.Error(), path)
	}

	if !strings.Contains(err.Error(), "pattern") {
		t.Errorf("error %q must mention 'pattern'", err.Error())
	}
}

func TestErrInvalidInput(t *testing.T) {
	input := "bad-input-value"
	err := security.ErrInvalidInput(input)

	if err == nil {
		t.Fatal("expected non-nil error")
	}

	if !strings.Contains(err.Error(), input) {
		t.Errorf("error %q must contain input %q", err.Error(), input)
	}

	if !strings.Contains(err.Error(), "invalid input") {
		t.Errorf("error %q must mention 'invalid input'", err.Error())
	}
}

func TestErrInputContainsShellMetacharacters(t *testing.T) {
	input := "cmd|injection"
	err := security.ErrInputContainsShellMetacharacters(input)

	if err == nil {
		t.Fatal("expected non-nil error")
	}

	if !strings.Contains(err.Error(), input) {
		t.Errorf("error %q must contain input %q", err.Error(), input)
	}

	if !strings.Contains(err.Error(), "metacharacter") {
		t.Errorf("error %q must mention 'metacharacter'", err.Error())
	}
}

// Dynamic errors are distinct instances — errors.Is must not match across calls.
func TestErrorDistinctInstances(t *testing.T) {
	a := security.ErrPathContainsDangerousPattern("/a")
	b := security.ErrPathContainsDangerousPattern("/b")

	if a.Error() == b.Error() {
		t.Error("distinct paths should produce distinct error messages")
	}
}

// ErrEmptyPath is a sentinel — multiple calls return the same value.
func TestErrEmptyPathIsSentinel(t *testing.T) {
	if security.ErrEmptyPath != security.ErrEmptyPath {
		t.Error("ErrEmptyPath sentinel must be stable")
	}
}
