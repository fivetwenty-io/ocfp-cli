//go:build unix && !linux && !darwin

package jobexec

import "errors"

// canWaitWithoutReaping reports whether waitWithoutReaping works here. A
// release is built only for Linux, Darwin, and Windows, and no other Unix
// has a wait that leaves the child unreaped here, so no verb starts.
const canWaitWithoutReaping = false

func waitWithoutReaping(int) error {
	return errors.New("this system cannot wait for a process without reaping it")
}
