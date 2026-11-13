package aws

// main_test.go — package-wide test setup. Shortens the compute polling seams
// before any test runs so waiter happy paths finish in milliseconds instead
// of waiting out production tick intervals, and gives the tests a home of
// their own. Set once here (not per-test)
// because the tests run with t.Parallel and per-test mutation would race.

import (
	"testing"
	"time"

	"github.com/fivetwenty-io/ocfp-cli/internal/testenv"
)

func TestMain(m *testing.M) {
	instancePollInterval = 5 * time.Millisecond
	rebootTransitionDelay = time.Millisecond

	testenv.Main(m)
}
