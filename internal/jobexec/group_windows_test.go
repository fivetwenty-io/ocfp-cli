//go:build windows

package jobexec

import "testing"

// Terminating the verb's Job Object ends the verb and every process it
// started.
func TestJobObjectKillsTheTree(t *testing.T) {
	t.Parallel()

	killsTheTree(t)
}
