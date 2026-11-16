//go:build darwin

package jobexec

import (
	"errors"
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

// canWaitWithoutReaping reports whether waitWithoutReaping works here.
const canWaitWithoutReaping = true

// darwinZombie is SZOMB, the state of a process that has exited and has not
// been reaped.
const darwinZombie = 5

// exitRecheck is how often waitWithoutReaping looks at the child's state
// again while it waits for the kernel's exit event.
const exitRecheck = time.Second

// waitWithoutReaping waits for the child pid to exit and leaves it a zombie,
// so that its pid, and the group id it leads, stay its own until the reap.
// Darwin has no waitid, so it waits on a kqueue for the process's exit. A
// process that has already exited refuses the registration with ESRCH, and
// pid is this process's unreaped child, so ESRCH means it has exited.
func waitWithoutReaping(pid int) error {
	kq, err := unix.Kqueue()
	if err != nil {
		return fmt.Errorf("wait for process %d: kqueue: %w", pid, err)
	}

	defer func() { _ = unix.Close(kq) }()

	var change unix.Kevent_t

	unix.SetKevent(&change, pid, unix.EVFILT_PROC, unix.EV_ADD|unix.EV_ONESHOT)
	change.Fflags = unix.NOTE_EXIT

	for {
		_, err = unix.Kevent(kq, []unix.Kevent_t{change}, nil, nil)
		if !errors.Is(err, unix.EINTR) {
			break
		}
	}

	switch {
	case errors.Is(err, unix.ESRCH):
		return nil
	case err != nil:
		return fmt.Errorf("wait for process %d: watch its exit: %w", pid, err)
	}

	events := make([]unix.Kevent_t, 1)
	timeout := unix.NsecToTimespec(exitRecheck.Nanoseconds())

	for {
		n, err := unix.Kevent(kq, nil, events, &timeout)

		switch {
		case errors.Is(err, unix.EINTR):
			continue
		case err != nil:
			return fmt.Errorf("wait for process %d: %w", pid, err)
		case n > 0 && events[0].Fflags&unix.NOTE_EXIT != 0:
			return nil
		}

		exited, err := isZombie(pid)
		if err != nil {
			return err
		}

		if exited {
			return nil
		}
	}
}

// isZombie reports whether pid has exited and not been reaped.
func isZombie(pid int) (bool, error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return false, fmt.Errorf("wait for process %d: read its state: %w", pid, err)
	}

	return kp.Proc.P_stat == darwinZombie, nil
}
