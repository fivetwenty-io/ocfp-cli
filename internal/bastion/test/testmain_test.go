package test_test

import (
	"os"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/testenv"
)

// TestMain gives the tests a home of their own, so none of them can read
// or write the developer's real ocfp directories. The tests run in
// parallel, so the whole package shares one OCFP_HOME, set here.
func TestMain(m *testing.M) {
	os.Exit(testenv.Run(func() int {
		testenv.UseOcfpHome()

		return m.Run()
	}))
}
