package commands

import (
	"bytes"
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"testing"
	"time"
)

// lockedBuffer is a terminal that a copy may still write to after the run
// returns.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

// slowTerminal stalls on its first write, like a paused pager.
type slowTerminal struct {
	lockedBuffer

	stall time.Duration
	once  sync.Once
}

func (s *slowTerminal) Write(p []byte) (int, error) {
	s.once.Do(func() { time.Sleep(s.stall) })

	return s.lockedBuffer.Write(p)
}

// A background process can keep stderr open after the command exits, as
// "ssh -f" and a ProxyCommand do. The run returns promptly, and what the
// background process writes later still reaches the terminal.
func TestRunShowingStderrDoesNotWaitForABackgroundHolder(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	var terminal lockedBuffer

	cmd := exec.Command("/bin/sh", "-c", "echo early >&2; (sleep 2; echo late >&2) & exit 0")

	start := time.Now()

	err := runShowingStderr(cmd, &terminal, "100.109.226.53")
	if err != nil {
		t.Fatalf("got %v, want success", err)
	}

	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("the run took %s to return, waiting on the background process", elapsed)
	}

	if !strings.Contains(terminal.String(), "early") {
		t.Errorf("terminal = %q, want the command's own output", terminal.String())
	}

	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(terminal.String(), "late") && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}

	if !strings.Contains(terminal.String(), "late") {
		t.Errorf("terminal = %q, want the background process's later output too", terminal.String())
	}
}

// A terminal that is slow to take output, such as a paused pager, still
// gets all of it, however long it stalls after the command exits.
func TestRunShowingStderrDeliversEverythingToASlowTerminal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	terminal := &slowTerminal{stall: 3 * time.Second} //nolint:exhaustruct // zero values are ready to use

	const size = 40000

	cmd := exec.Command("/bin/sh", "-c", "head -c 40000 /dev/zero | tr '\\0' x >&2; exit 0")

	err := runShowingStderr(cmd, terminal, "100.109.226.53")
	if err != nil {
		t.Fatalf("got %v, want success", err)
	}

	if got := len(terminal.String()); got != size {
		t.Errorf("the terminal got %d of %d bytes", got, size)
	}
}

// The command can exit while the copy is busy writing to a slow terminal
// rather than waiting to read. The grace still applies to the copy's next
// read, so a background process holding the pipe does not hold the run.
func TestRunShowingStderrGraceStartsAfterASlowWrite(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	terminal := &slowTerminal{stall: time.Second} //nolint:exhaustruct // zero values are ready to use

	cmd := exec.Command("/bin/sh", "-c", "echo early >&2; (sleep 5; echo late >&2) & exit 0")

	start := time.Now()

	err := runShowingStderr(cmd, terminal, "100.109.226.53")
	if err != nil {
		t.Fatalf("got %v, want success", err)
	}

	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("the run took %s to return, waiting on the background process", elapsed)
	}

	if !strings.Contains(terminal.String(), "early") {
		t.Errorf("terminal = %q, want the command's own output", terminal.String())
	}
}

// openFiles counts this process's open file descriptors.
func openFiles(t *testing.T) int {
	t.Helper()

	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Skipf("cannot list open files: %v", err)
	}

	return len(entries)
}

// Each run closes both ends of its stderr pipe once the command is done
// with them, so runs do not leak file descriptors. The garbage collector
// would close a dropped file too, so it is held off while the runs are
// counted.
func TestRunShowingStderrClosesItsPipe(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	const runs = 20

	runtime.GC()
	gcPercent := debug.SetGCPercent(-1)
	t.Cleanup(func() { debug.SetGCPercent(gcPercent) })

	before := openFiles(t)

	for range runs {
		err := runShowingStderr(exec.Command("/bin/sh", "-c", "echo x >&2"), &lockedBuffer{}, "100.109.226.53") //nolint:exhaustruct // zero values are ready to use
		if err != nil {
			t.Fatalf("got %v, want success", err)
		}
	}

	// The copy closes the read end just after the run returns.
	after := openFiles(t)
	for deadline := time.Now().Add(2 * time.Second); after > before && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)

		after = openFiles(t)
	}

	if after-before >= runs/2 {
		t.Errorf("%d runs left %d more file descriptors open", runs, after-before)
	}
}
