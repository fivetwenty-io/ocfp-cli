package state

import "time"

// SetNowFn replaces the package clock and returns a restore function.
// Call t.Cleanup(restore) immediately after calling SetNowFn.
func SetNowFn(fn func() time.Time) (restore func()) {
	prev := nowFn
	nowFn = fn
	return func() { nowFn = prev }
}
