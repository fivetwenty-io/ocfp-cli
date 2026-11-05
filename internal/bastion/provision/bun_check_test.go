package provision

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/config"
)

// runInstallCheck evaluates a tool's generated install check the way the
// tools script does, under bash with set -euo pipefail, with HOME set to home
// and a PATH that holds only the system directories.
func runInstallCheck(t *testing.T, condition, home string) bool {
	t.Helper()

	script := "set -euo pipefail\nif " + condition + "; then echo installed; else echo missing; fi\n"
	cmd := exec.Command(bashShell(t), "-c", script) // #nosec G204 -- test fixture
	cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin"}

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("check failed to run: %v\n%s", err, out)
	}

	return strings.TrimSpace(string(out)) == "installed"
}

func TestAdvancedTools_BunCheckFindsHomeInstall(t *testing.T) {
	t.Parallel()

	atm := NewAdvancedToolManager("aws", &config.Config{})

	var bun *AdvancedBinaryTool

	for _, tool := range atm.GetAdvancedBinaryTools() {
		if tool.Name == "bun" {
			bun = &tool

			break
		}
	}

	if bun == nil {
		t.Fatal("bun is not among the advanced tools")
	}

	condition := atm.getInstallCheckCondition(*bun)

	if runInstallCheck(t, condition, t.TempDir()) {
		t.Fatalf("check passes with no bun installed: %s", condition)
	}

	home := t.TempDir()
	binDir := filepath.Join(home, ".bun", "bin")

	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}

	writeFakeExecutableFile(t, filepath.Join(binDir, "bun"), "#!/bin/sh\necho 1.3.0\n")

	if !runInstallCheck(t, condition, home) {
		t.Fatalf("check misses bun installed at ~/.bun/bin/bun: %s", condition)
	}
}
