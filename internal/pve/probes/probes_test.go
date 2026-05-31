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

// calledProbe records whether Run was invoked, then delegates to stubProbe.
type calledProbe struct {
	stub   stubProbe
	called bool
}

func (c *calledProbe) Name() string { return c.stub.name }
func (c *calledProbe) Run(ctx context.Context) probes.Result {
	c.called = true
	return c.stub.Run(ctx)
}

func TestRunAll_AllPass_ReturnsOK(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	p1 := &stubProbe{name: "p1", result: probes.Result{OK: true, Detail: "fine"}}
	p2 := &stubProbe{name: "p2", result: probes.Result{OK: true, Detail: "also fine"}}

	got := probes.RunAll(ctx, p1, p2)
	if !got.OK {
		t.Fatalf("RunAll: expected OK=true, got OK=false detail=%q", got.Detail)
	}
}

func TestRunAll_FirstFail_StopsAndReturnsFail(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	failResult := probes.Result{OK: false, Detail: "boom", Remediation: "fix it"}
	p1 := &stubProbe{name: "p1", result: failResult}
	p2 := &calledProbe{stub: stubProbe{name: "p2", result: probes.Result{OK: true}}}

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
	if p2.called {
		t.Error("RunAll: second probe must not be called after first probe fails")
	}
}

func TestRunAll_SecondFail_ReturnsSecondResult(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

	ctx := context.Background()
	got := probes.RunAll(ctx)
	if !got.OK {
		t.Fatal("RunAll with no probes: expected OK=true")
	}
}
