package commands

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProcessStartTime_SelfIsPlausible asserts the platform lookup returns
// a start time for this very process that is neither in the future nor
// absurdly old, which is all a black-box test can pin down.
func TestProcessStartTime_SelfIsPlausible(t *testing.T) {
	started, err := processStartTime(os.Getpid())
	if err != nil {
		t.Skipf("start time unavailable on this platform: %v", err)
	}

	now := time.Now()
	assert.False(t, started.After(now.Add(lockStartSlack)), "start %v is after now %v", started, now)
	assert.True(t, started.After(now.Add(-24*time.Hour)), "start %v is implausibly old", started)
}

// TestProcessStartTime_ChildIsAfterLaunchTime asserts the start time of a
// child launched just now lands after the moment the test recorded before
// launching it, within the clock-resolution slack.
func TestProcessStartTime_ChildIsAfterLaunchTime(t *testing.T) {
	before := time.Now()
	pid := liveChildPID(t)

	started, err := processStartTime(pid)
	if err != nil {
		t.Skipf("start time unavailable on this platform: %v", err)
	}

	assert.False(t, started.Before(before.Add(-lockStartSlack)), "child start %v precedes launch %v", started, before)
	assert.False(t, started.After(time.Now().Add(lockStartSlack)), "child start %v is in the future", started)
}

func TestStartedAfterLock(t *testing.T) {
	pid := liveChildPID(t)

	if _, err := processStartTime(pid); err != nil {
		t.Skipf("start time unavailable on this platform: %v", err)
	}

	assert.True(t, startedAfterLock(pid, time.Now().Add(-time.Hour)),
		"a lock an hour older than the process means the PID was recycled")
	assert.False(t, startedAfterLock(pid, time.Now()),
		"a lock stamped after the process started belongs to it")
	assert.False(t, startedAfterLock(pid, time.Time{}),
		"a lock with no timestamp falls back to plain liveness")

	require.False(t, startedAfterLock(deadPID(t), time.Now().Add(-time.Hour)),
		"an unreadable start time (dead PID) must not claim recycling")
}
