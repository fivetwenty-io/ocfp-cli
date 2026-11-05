//go:build unix

package provision

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// holdRcLock takes the per-user rc lock under home the way another init
// would and releases it when the test ends. It skips the test when flock(1)
// is not installed, since the phase then runs without the lock.
func holdRcLock(t *testing.T, home string) {
	t.Helper()

	if _, err := exec.LookPath("flock"); err != nil {
		t.Skip("flock not available")
	}

	dir := filepath.Join(home, ".ocfp")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	lock, err := os.OpenFile(filepath.Join(dir, "rc-path.lock"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}

	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = lock.Close() })
}

func TestShellEnvironment_LockTimeoutSkipsRcFiles(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	bashrc := filepath.Join(home, ".bashrc")
	profile := filepath.Join(home, ".profile")
	writePathFile(t, bashrc, skelBashrc, 0o644)
	writePathFile(t, profile, skelProfile, 0o644)

	holdRcLock(t, home)

	tmp := t.TempDir()

	out, err := shellEnvironmentCommand(t, home, tmp, 1).CombinedOutput()
	if err != nil {
		t.Fatalf("a held lock must not fail the phase: %v\n%s", err, out)
	}

	if len(warningsAbout(string(out), "rc-path.lock")) == 0 {
		t.Fatalf("expected a warning naming the lock file, got:\n%s", out)
	}

	if got := readFile(t, bashrc); got != skelBashrc {
		t.Fatalf(".bashrc changed while the lock was held:\n%s", got)
	}

	if got := readFile(t, profile); got != skelProfile {
		t.Fatalf(".profile changed while the lock was held:\n%s", got)
	}

	assertNoTempFiles(t, tmp, home)
}

func TestShellEnvironment_SignalWhileWaitingCleansUp(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	bashrc := filepath.Join(home, ".bashrc")
	writePathFile(t, bashrc, skelBashrc, 0o644)

	holdRcLock(t, home)

	tmp := t.TempDir()
	cmd := shellEnvironmentCommand(t, home, tmp, rcLockWaitSeconds)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out

	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	// The phase stages its block files before it waits for the lock, so
	// once both hold content it is waiting in flock.
	deadline := time.Now().Add(10 * time.Second)

	for {
		if staged := nonEmptyFiles(t, tmp); staged >= 2 {
			break
		}

		if time.Now().After(deadline) {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Wait()
			t.Fatalf("phase never staged its temporary files:\n%s", out.String())
		}

		time.Sleep(20 * time.Millisecond)
	}

	time.Sleep(200 * time.Millisecond)

	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}

	if err := cmd.Wait(); err == nil {
		t.Fatalf("phase exited zero after SIGTERM:\n%s", out.String())
	}

	if got := readFile(t, bashrc); got != skelBashrc {
		t.Fatalf(".bashrc changed:\n%s", got)
	}

	assertNoTempFiles(t, tmp, home)
}

func nonEmptyFiles(t *testing.T, dir string) int {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	count := 0

	for _, entry := range entries {
		if info, err := entry.Info(); err == nil && info.Size() > 0 {
			count++
		}
	}

	return count
}
