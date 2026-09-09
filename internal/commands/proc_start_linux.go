//go:build linux

package commands

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// procUserHZ is the clock-tick rate the /proc filesystem reports process
// times in. The kernel fixes it at 100 for userspace regardless of the
// scheduler's own HZ, which is why /proc/<pid>/stat needs no sysconf call.
const procUserHZ = 100

// procStatStartTimeField is the 1-based index of starttime in
// /proc/<pid>/stat as documented in proc(5).
const procStatStartTimeField = 22

// processStartTime returns the wall-clock time at which the process with
// the given PID started, computed from its /proc/<pid>/stat start tick
// and the system boot time in /proc/stat.
func processStartTime(pid int) (time.Time, error) {
	bootTime, err := linuxBootTime()
	if err != nil {
		return time.Time{}, err
	}

	startTicks, err := linuxProcessStartTicks(pid)
	if err != nil {
		return time.Time{}, err
	}

	sinceBoot := time.Duration(startTicks) * time.Second / procUserHZ

	return bootTime.Add(sinceBoot), nil
}

// linuxProcessStartTicks reads the starttime field of /proc/<pid>/stat.
// The comm field (2) may contain spaces or parentheses, so the fields are
// counted from the last closing parenthesis rather than split naively.
func linuxProcessStartTicks(pid int) (int64, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, fmt.Errorf("%w: pid %d: %w", ErrProcessStartTimeUnavailable, pid, err)
	}

	stat := string(data)

	commEnd := strings.LastIndexByte(stat, ')')
	if commEnd < 0 {
		return 0, fmt.Errorf("%w: pid %d: malformed stat line", ErrProcessStartTimeUnavailable, pid)
	}

	// Fields after comm start at field 3 (state).
	rest := strings.Fields(stat[commEnd+1:])

	idx := procStatStartTimeField - 3
	if idx >= len(rest) {
		return 0, fmt.Errorf("%w: pid %d: stat line has %d fields", ErrProcessStartTimeUnavailable, pid, len(rest))
	}

	ticks, err := strconv.ParseInt(rest[idx], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: pid %d: parsing starttime: %w", ErrProcessStartTimeUnavailable, pid, err)
	}

	return ticks, nil
}

// linuxBootTime reads the btime line of /proc/stat.
func linuxBootTime() (time.Time, error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: reading /proc/stat: %w", ErrProcessStartTimeUnavailable, err)
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 || fields[0] != "btime" {
			continue
		}

		secs, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return time.Time{}, fmt.Errorf("%w: parsing btime: %w", ErrProcessStartTimeUnavailable, err)
		}

		return time.Unix(secs, 0), nil
	}

	if err := scanner.Err(); err != nil {
		return time.Time{}, fmt.Errorf("%w: reading /proc/stat: %w", ErrProcessStartTimeUnavailable, err)
	}

	return time.Time{}, fmt.Errorf("%w: /proc/stat has no btime line", ErrProcessStartTimeUnavailable)
}
