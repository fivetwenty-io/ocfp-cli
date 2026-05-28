package test_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/bastion"
)

// Static errors for error classification unit tests.
var (
	errConnectionRefused       = errors.New("connection refused")
	errPermissionDenied        = errors.New("permission denied")
	errConfigFileNotFound      = errors.New("config file not found")
	errCommandNotFoundBosh     = errors.New("command not found: bosh")
	errContextDeadlineExceeded = errors.New("context deadline exceeded")
)

// TestErrorClassification tests error classification and retry logic.
func TestErrorClassification(t *testing.T) {
	t.Parallel()

	errorHandler := bastion.NewErrorHandler()
	testCases := getErrorClassificationTestCases()

	for _, testCase := range testCases {
		tc := testCase
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runErrorClassificationTest(t, errorHandler, tc)
		})
	}
}

type errorTestCase struct {
	name         string
	err          error
	expectedType bastion.ErrorType
	retryable    bool
}

func getErrorClassificationTestCases() []errorTestCase {
	return []errorTestCase{
		{
			name:         "network error",
			err:          errConnectionRefused,
			expectedType: bastion.ErrorTypeNetwork,
			retryable:    true,
		},
		{
			name:         "permission error",
			err:          errPermissionDenied,
			expectedType: bastion.ErrorTypePermission,
			retryable:    false,
		},
		{
			name:         "configuration error",
			err:          errConfigFileNotFound,
			expectedType: bastion.ErrorTypeConfiguration,
			retryable:    false,
		},
		{
			name:         "dependency error",
			err:          errCommandNotFoundBosh,
			expectedType: bastion.ErrorTypeDependency,
			retryable:    true,
		},
		{
			name:         "timeout error",
			err:          errContextDeadlineExceeded,
			expectedType: bastion.ErrorTypeTimeout,
			retryable:    true,
		},
	}
}

func runErrorClassificationTest(t *testing.T, errorHandler *bastion.ErrorHandler, tc errorTestCase) {
	t.Helper()

	bastionErr := errorHandler.ClassifyError(tc.err, "test_phase", "test_command")

	if bastionErr.Type != tc.expectedType {
		t.Errorf("expected error type %s, got %s", tc.expectedType, bastionErr.Type)
	}

	if bastionErr.Retryable != tc.retryable {
		t.Errorf("expected retryable %t, got %t", tc.retryable, bastionErr.Retryable)
	}

	if len(bastionErr.Suggestions) == 0 {
		t.Error("expected at least one error suggestion")
	}

	// Phase and command context must be preserved in the classified error.
	if bastionErr.Phase != "test_phase" {
		t.Errorf("expected phase %q preserved in error, got %q", "test_phase", bastionErr.Phase)
	}

	// Original error message must be traceable through the classified error.
	if !strings.Contains(bastionErr.Error(), tc.err.Error()) {
		t.Errorf("expected classified error to contain original message %q, got %q",
			tc.err.Error(), bastionErr.Error())
	}
}

// TestProgressReporting tests progress reporting output formatting.
func TestProgressReporting(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer

	progress := &bastion.ProvisioningProgress{
		TotalSteps:     10,
		CompletedSteps: 3,
		CurrentStep:    "test_phase",
		StartTime:      time.Now().Add(-2 * time.Minute),
		Errors:         []error{},
		Checkpoints:    make(map[string]bool),
	}

	mode := bastion.SelectOutputMode(&output)
	reporter := bastion.NewProgressReporter(&output, mode, progress)

	// Phase start must produce output.
	reporter.ReportPhaseStart("test_phase", 3, 10)

	if output.Len() == 0 {
		t.Error("expected output from phase start report")
	}

	startOutput := output.String()

	// Phase name must appear in start output.
	if !strings.Contains(startOutput, "test_phase") {
		t.Errorf("expected phase name in start output, got: %s", startOutput)
	}

	// Total step count must appear. The step index may be displayed 1-based
	// (e.g., "[04/10]") so only assert the total "10" is present.
	if !strings.Contains(startOutput, "10") {
		t.Errorf("expected total step count in start output, got: %s", startOutput)
	}

	output.Reset()

	// Phase completion must produce output containing phase name.
	reporter.ReportPhaseComplete("test_phase", 5*time.Second)

	if output.Len() == 0 {
		t.Error("expected output from phase completion report")
	}

	completeOutput := output.String()

	if !strings.Contains(completeOutput, "test_phase") {
		t.Errorf("expected phase name in completion output, got: %s", completeOutput)
	}
}
