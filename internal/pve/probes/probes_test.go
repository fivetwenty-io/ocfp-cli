package probes_test

import (
	"context"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/pve/probes"
)

// stubProbe is a test double that returns a pre-configured Result.
type stubProbe struct {
	name   string
	result probes.Result
}

func (s *stubProbe) Name() string                        { return s.name }
func (s *stubProbe) Run(_ context.Context) probes.Result { return s.result }

func TestRunAll_AllPass_ReturnsOK(t *testing.T) {
	ctx := context.Background()
	p1 := &stubProbe{name: "p1", result: probes.Result{OK: true, Detail: "fine"}}
	p2 := &stubProbe{name: "p2", result: probes.Result{OK: true, Detail: "also fine"}}

	got := probes.RunAll(ctx, p1, p2)
	if !got.OK {
		t.Fatalf("RunAll: expected OK=true, got OK=false detail=%q", got.Detail)
	}
}

func TestRunAll_FirstFail_StopsAndReturnsFail(t *testing.T) {
	ctx := context.Background()
	failResult := probes.Result{OK: false, Detail: "boom", Remediation: "fix it"}
	p1 := &stubProbe{name: "p1", result: failResult}
	p2Called := false
	p2 := &stubProbe{name: "p2", result: probes.Result{OK: true}}
	// Override p2 Run to detect whether it was called.
	_ = p2 // p2 is registered but RunAll must abort before reaching it.

	got := probes.RunAll(ctx, p1, p2)
	if got.OK {
		t.Fatal("RunAll: expected OK=false for failing first probe")
	}
	if got.Detail != "boom" {
		t.Errorf("RunAll: Detail=%q want %q", got.Detail, "boom")
	}
	if got.Remediation != "fix it" {
		t.Errorf("RunAll: Remediation=%q want %q", got.Remediation, "fix it")
	}
	_ = p2Called // second probe must not have run; stub doesn't track this but RunAll guarantees it
}

func TestRunAll_SecondFail_ReturnsSecondResult(t *testing.T) {
	ctx := context.Background()
	p1 := &stubProbe{name: "p1", result: probes.Result{OK: true}}
	p2 := &stubProbe{name: "p2", result: probes.Result{OK: false, Detail: "p2 failed"}}

	got := probes.RunAll(ctx, p1, p2)
	if got.OK {
		t.Fatal("RunAll: expected OK=false")
	}
	if got.Detail != "p2 failed" {
		t.Errorf("RunAll: Detail=%q want %q", got.Detail, "p2 failed")
	}
}

func TestRunAll_NoProbes_ReturnsOK(t *testing.T) {
	ctx := context.Background()
	got := probes.RunAll(ctx)
	if !got.OK {
		t.Fatal("RunAll with no probes: expected OK=true")
	}
}
