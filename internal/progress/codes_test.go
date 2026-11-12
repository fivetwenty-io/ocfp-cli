package progress_test

import (
	"errors"
	"fmt"
	"regexp"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
)

func TestRefusalCodesAreUpperSnake(t *testing.T) {
	t.Parallel()

	codes := map[string]progress.Code{
		"OWNER_RECORD_MISSING":      progress.CodeOwnerRecordMissing,
		"OWNER_MISMATCH":            progress.CodeOwnerMismatch,
		"REQUEST_UNSIGNED":          progress.CodeRequestUnsigned,
		"OWNER_HANDOVER":            progress.CodeOwnerHandover,
		"JOB_REQUEST_INVALID":       progress.CodeJobRequestInvalid,
		"VERB_NOT_ALLOWED":          progress.CodeVerbNotAllowed,
		"WIRE_CONTRACT_UNSUPPORTED": progress.CodeWireContractUnsupported,
		"OWNER_RECORD_EXISTS":       progress.CodeOwnerRecordExists,
		"JOB_ORPHANED":              progress.CodeJobOrphaned,
		"OWNER_RECORD_INVALID":      progress.CodeOwnerRecordInvalid,
		"ORG_BLOC_MISMATCH":         progress.CodeOrgBlocMismatch,
	}

	if len(codes) != 11 {
		t.Fatalf("have %d codes, want 11", len(codes))
	}

	shape := regexp.MustCompile(`^[A-Z][A-Z0-9_]+$`)

	for want, got := range codes {
		if string(got) != want {
			t.Errorf("code %q has value %q", want, got)
		}

		if !shape.MatchString(string(got)) {
			t.Errorf("code %q is not upper snake case", got)
		}
	}
}

func TestAsRefusalUnwraps(t *testing.T) {
	t.Parallel()

	ref := &progress.Refusal{Code: progress.CodeOwnerMismatch, Message: "wrong epoch"}
	err := fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", ref))

	got, ok := progress.AsRefusal(err)
	if !ok || got != ref {
		t.Fatalf("AsRefusal = %v, %v; want the wrapped refusal", got, ok)
	}

	if _, ok := progress.AsRefusal(errors.New("plain")); ok {
		t.Fatal("AsRefusal found a refusal in a plain error")
	}

	if _, ok := progress.AsRefusal(nil); ok {
		t.Fatal("AsRefusal found a refusal in nil")
	}

	if s := ref.Error(); s != "OWNER_MISMATCH: wrong epoch" {
		t.Fatalf("Error() = %q", s)
	}
}

func TestAsRefusalTypedNil(t *testing.T) {
	t.Parallel()

	var typedNil *progress.Refusal

	got, ok := progress.AsRefusal(fmt.Errorf("outer: %w", typedNil))
	if ok || got != nil {
		t.Fatalf("AsRefusal = %v, %v; want nil, false for a typed-nil refusal", got, ok)
	}

	ref := &progress.Refusal{Code: progress.CodeOwnerMismatch, Message: "wrong epoch"}

	got, ok = progress.AsRefusal(fmt.Errorf("outer: %w", ref))
	if !ok || got != ref {
		t.Fatalf("AsRefusal = %v, %v; want the wrapped refusal", got, ok)
	}
}
