package version_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/testenv"
	"github.com/fivetwenty-io/ocfp-cli/internal/version"
	"github.com/fivetwenty-io/ocfp-cli/internal/writeguard"
	"github.com/stretchr/testify/assert"
)

// TestMain gives the tests a home of their own. The stamped binary one of
// them runs inherits it.
func TestMain(m *testing.M) {
	testenv.Main(m)
}

// TestGetReportsLinkerValues is the fast twin of
// TestStampedBuildReportsVersion. It sets the variable the linker would set
// and checks that Get reports it.
func TestGetReportsLinkerValues(t *testing.T) {
	restore := version.Version
	version.Version = "9.9.9-test"
	defer func() { version.Version = restore }()

	if got := version.Get().Version; got != "9.9.9-test" {
		t.Errorf("Get().Version = %q, want %q", got, "9.9.9-test")
	}
}

// TestStampedBuildReportsVersion builds the real binary with the linker flag
// that the Makefile and the release configuration use, so a wrong module path
// in those flags shows up as a binary that reports the default version.
func TestStampedBuildReportsVersion(t *testing.T) {
	if testing.Short() {
		t.Skip("covered by TestGetReportsLinkerValues in the watch tier")
	}

	bin := filepath.Join(t.TempDir(), "ocfp")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}

	ldflags := "-X github.com/fivetwenty-io/ocfp-cli/internal/version.Version=9.9.9-test"

	build := exec.Command("go", "build", "-ldflags", ldflags, "-o", bin, "./cmd/ocfp")
	build.Dir = filepath.Join("..", "..")

	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build failed: %v\n%s", err, out)
	}

	out, err := exec.Command(bin, "version").CombinedOutput()
	if err != nil {
		t.Fatalf("running version failed: %v\n%s", err, out)
	}

	if !strings.Contains(string(out), "9.9.9-test") {
		t.Errorf("version output %q does not contain the stamped version", out)
	}
}

// The stamped binary inherits this process's environment. If version ever
// reads config or writes a log, it must find the test home, not the
// developer's own directories.
func TestStampedBinaryInheritsTheTestHome(t *testing.T) {
	assert.Equal(t, "1", os.Getenv(writeguard.EnvVar), "the process must be marked as a test process")
	assert.True(t, strings.HasPrefix(filepath.Base(filepath.Dir(os.Getenv("HOME"))), "ocfp-test-home-"),
		"HOME is %q, which is not in a testenv tree", os.Getenv("HOME"))
}
