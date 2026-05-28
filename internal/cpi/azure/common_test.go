package azure

import (
	"sync"
	"testing"
	"time"
)

// fakeClock is a controllable clock for tests.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(t time.Time) *fakeClock { return &fakeClock{now: t} }

func (fc *fakeClock) Now() time.Time {
	fc.mu.Lock()
	defer fc.mu.Unlock()

	return fc.now
}

// Advance moves the fake clock forward by d.
func (fc *fakeClock) Advance(d time.Duration) {
	fc.mu.Lock()
	defer fc.mu.Unlock()

	fc.now = fc.now.Add(d)
}

func TestStripLabelPrefix(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"plain key unchanged", "bloc", "bloc"},
		{"label. prefix stripped", "label.bloc", "bloc"},
		{"label: prefix stripped", "label:bloc", "bloc"},
		{"tag: prefix unchanged", "tag:bloc", "tag:bloc"},
		{"nested label. key", "label.managed-by", "managed-by"},
		{"empty string unchanged", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripLabelPrefix(tt.input)
			if got != tt.expected {
				t.Errorf("stripLabelPrefix(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestMatchesInstanceFilters_LabelPrefix(t *testing.T) {
	tags := map[string]string{
		"bloc":       "520-aws-wayne",
		"managed-by": "ocfp",
		"role":       "bastion",
	}

	tests := []struct {
		name     string
		filters  map[string]string
		expected bool
	}{
		{
			"empty filters match everything",
			map[string]string{},
			true,
		},
		{
			"plain key matches",
			map[string]string{"bloc": "520-aws-wayne"},
			true,
		},
		{
			"label. prefix matches after stripping",
			map[string]string{"label.bloc": "520-aws-wayne", "label.role": "bastion"},
			true,
		},
		{
			"label: prefix matches after stripping",
			map[string]string{"label:bloc": "520-aws-wayne"},
			true,
		},
		{
			"wrong value does not match",
			map[string]string{"label.bloc": "wrong-bloc"},
			false,
		},
		{
			"missing tag does not match",
			map[string]string{"label.nonexistent": "value"},
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchesInstanceFilters(tags, tt.filters)
			if got != tt.expected {
				t.Errorf("matchesInstanceFilters() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestCircuitBreaker_Closed(t *testing.T) {
	cb := NewCircuitBreaker(3, 60*time.Second)

	if !cb.Allow() {
		t.Error("Expected Allow() true in closed state")
	}

	if cb.State() != CircuitClosed {
		t.Errorf("Expected CircuitClosed, got %v", cb.State())
	}
}

func TestCircuitBreaker_OpenAfterFailures(t *testing.T) {
	cb := NewCircuitBreaker(3, 60*time.Second)

	for i := 0; i < 3; i++ {
		cb.RecordFailure()
	}

	if cb.State() != CircuitOpen {
		t.Errorf("Expected CircuitOpen after 3 failures, got %v", cb.State())
	}

	if cb.Allow() {
		t.Error("Expected Allow() false in open state")
	}
}

func TestCircuitBreaker_HalfOpen(t *testing.T) {
	cb := NewCircuitBreaker(2, 100*time.Millisecond)

	fc := newFakeClock(time.Now())
	cb.setClock(fc)

	// Open the circuit.
	cb.RecordFailure()
	cb.RecordFailure()

	if cb.State() != CircuitOpen {
		t.Fatalf("Expected CircuitOpen, got %v", cb.State())
	}

	// Advance past the reset timeout.
	fc.Advance(150 * time.Millisecond)

	if !cb.Allow() {
		t.Error("Expected Allow() true after timeout (half-open transition)")
	}

	if cb.State() != CircuitHalfOpen {
		t.Errorf("Expected CircuitHalfOpen, got %v", cb.State())
	}
}

func TestCircuitBreaker_RecoveryToClosedState(t *testing.T) {
	cb := NewCircuitBreaker(2, 50*time.Millisecond)

	fc := newFakeClock(time.Now())
	cb.setClock(fc)

	// Open the circuit.
	cb.RecordFailure()
	cb.RecordFailure()

	// Advance past the reset timeout.
	fc.Advance(100 * time.Millisecond)

	// Transition to half-open.
	if !cb.Allow() {
		t.Fatal("Expected Allow() true after timeout")
	}

	// Record success to close the circuit.
	cb.RecordSuccess()

	if cb.State() != CircuitClosed {
		t.Errorf("Expected CircuitClosed after RecordSuccess, got %v", cb.State())
	}

	if !cb.Allow() {
		t.Error("Expected Allow() true in closed state")
	}
}

func TestCircuitBreaker_Reset(t *testing.T) {
	cb := NewCircuitBreaker(2, 60*time.Second)

	cb.RecordFailure()
	cb.RecordFailure()

	if cb.State() != CircuitOpen {
		t.Fatalf("Expected CircuitOpen, got %v", cb.State())
	}

	cb.Reset()

	if cb.State() != CircuitClosed {
		t.Errorf("Expected CircuitClosed after Reset, got %v", cb.State())
	}

	if !cb.Allow() {
		t.Error("Expected Allow() true after Reset")
	}
}
