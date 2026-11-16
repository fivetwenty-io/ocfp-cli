package jobexec

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"
)

// A client that stops reading is dropped once more than the buffer waits for
// it, and queueing never waits on it.
func TestAClientThatFallsTooFarBehindIsDropped(t *testing.T) {
	t.Parallel()

	client := newStalledClient(t)
	c := newClientWriter(client, 64)

	start := time.Now()

	for range 100 {
		c.queue(bytes.Repeat([]byte("x"), 16))
	}

	if took := time.Since(start); took > time.Second {
		t.Fatalf("queueing took %s for a client that had stopped reading", took)
	}

	err := c.finish(time.Hour)
	if err == nil || !strings.Contains(err.Error(), "behind") {
		t.Fatalf("finish = %v, want the client dropped for falling behind", err)
	}

	if again := c.finish(time.Hour); again == nil || again.Error() != err.Error() {
		t.Fatalf("a second finish = %v, want %v", again, err)
	}
}

// slowClient takes each write after a pause.
type slowClient struct {
	pause time.Duration

	mu  sync.Mutex
	got bytes.Buffer
}

func (c *slowClient) Write(p []byte) (int, error) {
	time.Sleep(c.pause)

	c.mu.Lock()
	defer c.mu.Unlock()

	return c.got.Write(p)
}

// A client that goes on taking the stream gets all of it once the run has
// finished, even when that takes longer than one grace.
func TestASlowClientGetsTheWholeStream(t *testing.T) {
	t.Parallel()

	client := &slowClient{pause: 50 * time.Millisecond}
	c := newClientWriter(client, defaultClientBuffer)

	// Each write takes a twentieth of the grace, which leaves room for a
	// loaded host, and the whole stream takes longer than one grace.
	want := bytes.Repeat([]byte("0123456789abcdef"), 24*clientChunk/16)
	c.queue(want)

	grace := time.Second
	start := time.Now()

	err := c.finish(grace)
	if err != nil {
		t.Fatalf("finish = %v", err)
	}

	if took := time.Since(start); took < grace {
		t.Logf("the client took the stream in %s, within one grace, so the test shows less than it could", took)
	}

	client.mu.Lock()
	defer client.mu.Unlock()

	if !bytes.Equal(client.got.Bytes(), want) {
		t.Fatalf("the client got %d bytes, want %d", client.got.Len(), len(want))
	}
}

// A client whose write fails is dropped, and finish reports why.
func TestAClientWhoseWriteFailsIsDropped(t *testing.T) {
	t.Parallel()

	c := newClientWriter(failingWriter{}, defaultClientBuffer)
	c.queue([]byte("line\n"))

	err := c.finish(time.Hour)
	if err == nil || !strings.Contains(err.Error(), "broken pipe") {
		t.Fatalf("finish = %v, want the write's error", err)
	}
}
