package probes_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/pve/probes"
)

// mockRunBosh returns a RunBosh func that emits output and optionally an error.
func mockRunBosh(output string, err error) func(ctx context.Context, args ...string) ([]byte, error) {
	return func(_ context.Context, args ...string) ([]byte, error) {
		return []byte(output), err
	}
}

func newProbe(runBosh func(context.Context, ...string) ([]byte, error)) *probes.UAAFlywayProbe {
	return &probes.UAAFlywayProbe{
		RunBosh:    runBosh,
		Deployment: "cf-lab",
		Env:        "lab",
		Instance:   "database/0",
	}
}

// T18 — FAILED_ROWS=2 → FAIL with DROP DATABASE remediation.
func TestUAAFlywayProbe_FailedRows_ReturnsRemediation(t *testing.T) {
	p := newProbe(mockRunBosh("FAILED_ROWS=2", nil))
	r := p.Run(context.Background())

	if r.OK {
		t.Fatal("expected OK=false for FAILED_ROWS=2")
	}
	if !strings.Contains(r.Remediation, "DROP DATABASE uaa") {
		t.Errorf("Remediation missing DROP DATABASE uaa:\n%s", r.Remediation)
	}
	if !strings.Contains(r.Remediation, "CREATE DATABASE uaa") {
		t.Errorf("Remediation missing CREATE DATABASE uaa:\n%s", r.Remediation)
	}
	if !strings.Contains(r.Remediation, "GRANT ALL PRIVILEGES") {
		t.Errorf("Remediation missing GRANT ALL PRIVILEGES:\n%s", r.Remediation)
	}
	if !strings.Contains(r.Remediation, "recreate uaa") {
		t.Errorf("Remediation missing 'recreate uaa':\n%s", r.Remediation)
	}
}

func TestUAAFlywayProbe_FailedRows_IncludesCount(t *testing.T) {
	p := newProbe(mockRunBosh("FAILED_ROWS=5", nil))
	r := p.Run(context.Background())
	if r.OK {
		t.Fatal("expected OK=false for FAILED_ROWS=5")
	}
	if !strings.Contains(r.Remediation, "5 failed migration row") {
		t.Errorf("Remediation should mention row count 5:\n%s", r.Remediation)
	}
}

func TestUAAFlywayProbe_FailedRows_Zero_IsOK(t *testing.T) {
	p := newProbe(mockRunBosh("FAILED_ROWS=0", nil))
	r := p.Run(context.Background())
	if !r.OK {
		t.Fatalf("expected OK=true for FAILED_ROWS=0, got detail=%q", r.Detail)
	}
}

// T19 — OK_DB_MISSING → PASS.
func TestUAAFlywayProbe_OKDBMissing_PASS(t *testing.T) {
	p := newProbe(mockRunBosh("OK_DB_MISSING", nil))
	r := p.Run(context.Background())
	if !r.OK {
		t.Fatalf("expected OK=true for OK_DB_MISSING, got detail=%q", r.Detail)
	}
	if r.Detail != probes.SentinelOKDBMissing {
		t.Errorf("Detail=%q want %q", r.Detail, probes.SentinelOKDBMissing)
	}
}

func TestUAAFlywayProbe_OKFresh_PASS(t *testing.T) {
	p := newProbe(mockRunBosh("OK_FRESH", nil))
	r := p.Run(context.Background())
	if !r.OK {
		t.Fatalf("expected OK=true for OK_FRESH, got detail=%q", r.Detail)
	}
}

func TestUAAFlywayProbe_SkipNoPXC_PASS(t *testing.T) {
	p := newProbe(mockRunBosh("SKIP_NO_PXC", nil))
	r := p.Run(context.Background())
	if !r.OK {
		t.Fatalf("expected OK=true for SKIP_NO_PXC, got detail=%q", r.Detail)
	}
}

func TestUAAFlywayProbe_SkipNoMySQLBin_PASS(t *testing.T) {
	p := newProbe(mockRunBosh("SKIP_NO_MYSQL_BIN", nil))
	r := p.Run(context.Background())
	if !r.OK {
		t.Fatalf("expected OK=true for SKIP_NO_MYSQL_BIN, got detail=%q", r.Detail)
	}
}

// T20 — PROBE_ERROR → OK (non-fatal, detail recorded).
func TestUAAFlywayProbe_PROBEERROR_PASS_WithDetail(t *testing.T) {
	p := newProbe(mockRunBosh("PROBE_ERROR: some mysql error here", nil))
	r := p.Run(context.Background())
	if !r.OK {
		t.Fatalf("expected OK=true for PROBE_ERROR (non-fatal), got detail=%q", r.Detail)
	}
	if !strings.Contains(r.Detail, "PROBE_ERROR") {
		t.Errorf("expected Detail to contain PROBE_ERROR, got %q", r.Detail)
	}
}

func TestUAAFlywayProbe_BoshSSHError_DeploymentAbsent_PASS(t *testing.T) {
	// bosh ssh fails and stderr contains "doesn't exist" → CF not yet deployed → OK.
	p := newProbe(mockRunBosh("doesn't exist", fmt.Errorf("bosh ssh exit 1")))
	r := p.Run(context.Background())
	if !r.OK {
		t.Fatalf("expected OK=true when deployment doesn't exist, got detail=%q", r.Detail)
	}
}

func TestUAAFlywayProbe_BoshSSHError_Generic_PASS(t *testing.T) {
	// Generic bosh ssh failure → non-fatal (log + continue).
	p := newProbe(mockRunBosh("connection refused", fmt.Errorf("bosh ssh exit 255")))
	r := p.Run(context.Background())
	if !r.OK {
		t.Fatalf("expected OK=true for generic bosh ssh failure (non-fatal), got detail=%q", r.Detail)
	}
	if !strings.Contains(r.Detail, "non-fatal") {
		t.Errorf("Detail should mention non-fatal: %q", r.Detail)
	}
}

func TestUAAFlywayProbe_UnparseableOutput_FAIL(t *testing.T) {
	p := newProbe(mockRunBosh("some unexpected output\nno sentinel here", nil))
	r := p.Run(context.Background())
	if r.OK {
		t.Fatal("expected OK=false for unparseable probe output")
	}
	if !strings.Contains(r.Detail, "unparseable") {
		t.Errorf("Detail should mention unparseable: %q", r.Detail)
	}
}

func TestUAAFlywayProbe_NilRunBosh_FAIL(t *testing.T) {
	p := &probes.UAAFlywayProbe{
		RunBosh:    nil,
		Deployment: "cf-lab",
		Env:        "lab",
	}
	r := p.Run(context.Background())
	if r.OK {
		t.Fatal("expected OK=false when RunBosh is nil")
	}
}

func TestUAAFlywayProbe_EmptyDeployment_FAIL(t *testing.T) {
	p := &probes.UAAFlywayProbe{
		RunBosh:    mockRunBosh("OK_FRESH", nil),
		Deployment: "",
		Env:        "lab",
	}
	r := p.Run(context.Background())
	if r.OK {
		t.Fatal("expected OK=false when Deployment is empty")
	}
}

func TestUAAFlywayProbe_EmptyEnv_FAIL(t *testing.T) {
	p := &probes.UAAFlywayProbe{
		RunBosh:    mockRunBosh("OK_FRESH", nil),
		Deployment: "cf-lab",
		Env:        "",
	}
	r := p.Run(context.Background())
	if r.OK {
		t.Fatal("expected OK=false when Env is empty")
	}
}

func TestUAAFlywayProbe_DefaultInstance_DatabaseZero(t *testing.T) {
	var capturedArgs []string
	p := &probes.UAAFlywayProbe{
		RunBosh: func(_ context.Context, args ...string) ([]byte, error) {
			capturedArgs = args
			return []byte("OK_FRESH"), nil
		},
		Deployment: "cf-lab",
		Env:        "lab",
		Instance:   "", // empty → default "database/0"
	}
	_ = p.Run(context.Background())
	found := false
	for _, a := range capturedArgs {
		if a == "database/0" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected bosh args to contain database/0, got %v", capturedArgs)
	}
}

func TestUAAFlywayProbe_Name(t *testing.T) {
	p := newProbe(nil)
	if p.Name() != "uaa-flyway" {
		t.Errorf("Name()=%q want %q", p.Name(), "uaa-flyway")
	}
}

func TestUAAFlywayProbe_RemediationIncludesEnvAndDeployment(t *testing.T) {
	p := &probes.UAAFlywayProbe{
		RunBosh:    mockRunBosh("FAILED_ROWS=1", nil),
		Deployment: "cf-production",
		Env:        "prod",
	}
	r := p.Run(context.Background())
	if r.OK {
		t.Fatal("expected OK=false")
	}
	if !strings.Contains(r.Remediation, "cf-production") {
		t.Errorf("Remediation missing deployment name: %s", r.Remediation)
	}
	if !strings.Contains(r.Remediation, "prod") {
		t.Errorf("Remediation missing env name: %s", r.Remediation)
	}
}
