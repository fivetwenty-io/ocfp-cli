package aws

// main_test.go — package-wide test setup. Shortens the compute polling seams
// before any test runs so waiter happy paths finish in milliseconds instead
// of waiting out production tick intervals. Set once here (not per-test)
// because the tests run with t.Parallel and per-test mutation would race.

import (
	"os"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	instancePollInterval = 5 * time.Millisecond
	rebootTransitionDelay = time.Millisecond

	os.Exit(m.Run())
}
