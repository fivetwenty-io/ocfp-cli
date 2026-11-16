package jobexec

import (
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// defaultClientBuffer bounds how much of the stream can wait for a client
// that is behind. It holds two records of the largest size.
const defaultClientBuffer = 2 * maxRecordLine

// defaultClientGrace is how long a run that has finished its job waits for a
// client that takes nothing more of the stream before it gives up on it.
const defaultClientGrace = 30 * time.Second

// clientChunk is the most that one write to the client carries, so that a
// slow client is seen to take the stream a piece at a time.
const clientChunk = 32 << 10

// clientWriter writes the stream to the run's client on a goroutine of its
// own, so that a client that is slow, or that has stopped reading, never
// holds up the job, the watch for its cancel, or its spool. What the run
// writes waits in a buffer of at most limit bytes. A client that falls
// further behind than that, or whose write fails, gets nothing more. The
// spool holds the whole stream all the same, so the client can attach again
// for the rest.
type clientWriter struct {
	w     io.Writer
	limit int

	// wake tells the writer goroutine that there is more to do.
	wake chan struct{}

	// done is closed when the writer goroutine ends, and dropped when the
	// client is dropped.
	done    chan struct{}
	dropped chan struct{}

	// taken counts the bytes the client has taken.
	taken atomic.Int64

	mu      sync.Mutex
	buf     []byte
	err     error // why the client gets nothing more
	closing bool

	finishOnce sync.Once
	finishErr  error
}

// newClientWriter starts the goroutine that writes to w, with at most limit
// bytes waiting for it.
func newClientWriter(w io.Writer, limit int) *clientWriter {
	c := &clientWriter{w: w, limit: limit, wake: make(chan struct{}, 1), done: make(chan struct{}), dropped: make(chan struct{})}

	go c.run()

	return c
}

// queue hands p to the client. It never waits for the client. A client that
// already has limit bytes waiting is dropped instead.
func (c *clientWriter) queue(p []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.err != nil || c.closing {
		return
	}

	if len(c.buf)+len(p) > c.limit {
		c.dropLocked(fmt.Errorf("the client fell more than %d bytes behind the stream", c.limit))

		return
	}

	c.buf = append(c.buf, p...)
	c.signal()
}

// dropLocked gives up on the client for err. It must be called with mu held.
func (c *clientWriter) dropLocked(err error) {
	if c.err == nil {
		c.err = err
		close(c.dropped)
	}

	c.buf = nil
	c.signal()
}

func (c *clientWriter) signal() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// run writes what waits in the buffer to the client, a chunk at a time,
// until the client is dropped or the run finishes and nothing is left.
func (c *clientWriter) run() {
	defer close(c.done)

	for {
		c.mu.Lock()

		for len(c.buf) == 0 && c.err == nil && !c.closing {
			c.mu.Unlock()
			<-c.wake
			c.mu.Lock()
		}

		if c.err != nil || len(c.buf) == 0 {
			c.mu.Unlock()

			return
		}

		n := min(len(c.buf), clientChunk)
		chunk := append([]byte(nil), c.buf[:n]...)

		c.buf = c.buf[n:]
		if len(c.buf) == 0 {
			c.buf = nil
		}

		c.mu.Unlock()

		_, err := c.w.Write(chunk)
		if err != nil {
			c.mu.Lock()
			c.dropLocked(err)
			c.mu.Unlock()

			return
		}

		c.taken.Add(int64(n))
	}
}

// finish waits for the client to take what is left of the stream, for as
// long as it goes on taking some within each grace, and returns why the
// client did not get the whole stream, or nil when it did. A client that
// takes nothing for a whole grace is dropped. A second call returns what the
// first did.
func (c *clientWriter) finish(grace time.Duration) error {
	c.finishOnce.Do(func() {
		c.mu.Lock()
		c.closing = true
		c.signal()
		c.mu.Unlock()

		taken := c.taken.Load()

		for !c.waitDone(grace) {
			now := c.taken.Load()
			if now == taken {
				c.mu.Lock()
				c.dropLocked(fmt.Errorf("the client took none of the stream for %s", grace))
				c.mu.Unlock()

				break
			}

			taken = now
		}

		c.mu.Lock()
		c.finishErr = c.err
		c.mu.Unlock()
	})

	return c.finishErr
}

// waitDone reports whether the writer goroutine ended, or the client was
// dropped, within d. A dropped client's goroutine is not waited for, since a
// write that never returns would keep it from ending.
func (c *clientWriter) waitDone(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-c.done:
		return true
	case <-c.dropped:
		return true
	case <-t.C:
		return false
	}
}
