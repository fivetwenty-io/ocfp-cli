package enroll

import (
	"math/rand/v2"
	"time"
)

// The waits of the enrollment service.
const (
	// PendingBase and PendingMax bound the wait between asks while a
	// request is pending. The cap stays below the pending entry's
	// two-minute life, so a live bastion's entry never lapses.
	PendingBase = 10 * time.Second
	PendingMax  = time.Minute
	// FetchBase and FetchMax bound the wait between attempts to fetch the
	// worker binary.
	FetchBase = 5 * time.Second
	FetchMax  = 5 * time.Minute
	// RejectedPoll is the wait between asks once a person has rejected
	// the request, so a reopened request is seen without anyone touching
	// the bastion.
	RejectedPoll = 10 * time.Minute
	// Jitter is the share of each wait that is drawn at random. A wait
	// falls in (1-Jitter)·d to d, so jitter never takes it past its cap.
	Jitter = 0.2
)

// Backoff doubles a wait from Base up to Max, and draws each wait at
// random from its top fifth, so bastions that failed together don't ask
// again together. It never gives up; the caller decides when to stop.
// The zero Rand uses the process's random source. A Backoff is not safe
// for concurrent use.
type Backoff struct {
	Base time.Duration
	Max  time.Duration
	// Rand returns a number in [0, 1). Tests set it.
	Rand func() float64

	attempt uint
}

// PendingBackoff returns the backoff for asking while a request is
// pending.
func PendingBackoff() *Backoff { return &Backoff{Base: PendingBase, Max: PendingMax} }

// FetchBackoff returns the backoff for fetching the worker binary.
func FetchBackoff() *Backoff { return &Backoff{Base: FetchBase, Max: FetchMax} }

// RejectedBackoff returns the backoff for polling a rejected request,
// which waits RejectedPoll each time, with the same jitter.
func RejectedBackoff() *Backoff { return &Backoff{Base: RejectedPoll, Max: RejectedPoll} }

// Next returns the next wait and doubles the one after it, up to Max.
func (b *Backoff) Next() time.Duration {
	d := b.ceiling()
	if b.attempt < maxDoublings {
		b.attempt++
	}

	r := rand.Float64 //nolint:gosec // jitter spreads retries; it guards nothing
	if b.Rand != nil {
		r = b.Rand
	}

	f := r()
	if f < 0 || f >= 1 {
		f = 0
	}

	return d - time.Duration(f*Jitter*float64(d))
}

// Reset starts the doubling over, after a step that succeeded.
func (b *Backoff) Reset() { b.attempt = 0 }

// maxDoublings stops the count well before a shift could overflow.
const maxDoublings = 32

func (b *Backoff) ceiling() time.Duration {
	base, maxWait := b.Base, b.Max
	if base <= 0 {
		base = time.Second
	}

	if maxWait < base {
		maxWait = base
	}

	d := base
	for i := uint(0); i < b.attempt && d < maxWait; i++ {
		if d > maxWait/2 {
			return maxWait
		}

		d *= 2
	}

	return min(d, maxWait)
}
