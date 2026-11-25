package enroll

import (
	"testing"
	"time"
)

func TestBackoffDoublesToItsCap(t *testing.T) {
	t.Parallel()

	for name, c := range map[string]struct {
		b    *Backoff
		want []time.Duration
	}{
		"pending": {PendingBackoff(), []time.Duration{10 * time.Second, 20 * time.Second, 40 * time.Second, time.Minute, time.Minute}},
		"fetch": {FetchBackoff(), []time.Duration{
			5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second, 80 * time.Second,
			160 * time.Second, 5 * time.Minute, 5 * time.Minute,
		}},
		"rejected": {RejectedBackoff(), []time.Duration{10 * time.Minute, 10 * time.Minute}},
	} {
		// With no jitter drawn, each wait is the ceiling itself.
		c.b.Rand = func() float64 { return 0 }

		for i, want := range c.want {
			if got := c.b.Next(); got != want {
				t.Errorf("%s: wait %d = %s, want %s", name, i, got, want)
			}
		}

		c.b.Reset()

		if got := c.b.Next(); got != c.want[0] {
			t.Errorf("%s: after Reset = %s", name, got)
		}
	}

	if PendingMax >= 2*time.Minute {
		t.Error("the pending cap reaches the pending entry's life")
	}
}

func TestBackoffJitterStaysInItsBand(t *testing.T) {
	t.Parallel()

	b := PendingBackoff()
	b.Rand = func() float64 { return 0.999999 }

	for range 10 {
		d := b.Next()
		if d > PendingMax || d < time.Duration(float64(PendingBase)*(1-Jitter)) {
			t.Errorf("a wait of %s is outside its band", d)
		}
	}

	if got := b.Next(); got <= time.Duration(float64(PendingMax)*(1-Jitter)) {
		t.Errorf("the jitter took %s below its band", got)
	}

	// A broken source draws nothing rather than a wait past the cap.
	b.Rand = func() float64 { return -3 }
	if got := b.Next(); got != PendingMax {
		t.Errorf("a broken source gave %s", got)
	}

	// The process's own source stays in the band, many times over.
	f := FetchBackoff()
	for range 1000 {
		if d := f.Next(); d > FetchMax || d <= 0 {
			t.Fatalf("a drawn wait of %s", d)
		}
	}

	// A zero backoff still waits and never overflows.
	var z Backoff
	for range 100 {
		if d := z.Next(); d <= 0 || d > time.Second {
			t.Fatalf("a zero backoff waited %s", d)
		}
	}

	huge := &Backoff{Base: 1 << 40, Max: 1<<63 - 1}
	for range 100 {
		if d := huge.Next(); d <= 0 {
			t.Fatalf("a large cap overflowed to %s", d)
		}
	}
}
