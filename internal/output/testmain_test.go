package output

import (
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/testenv"
)

// TestMain gives the tests a home of their own, so none of them can read
// or write the developer's real ocfp directories.
func TestMain(m *testing.M) {
	testenv.Main(m)
}
