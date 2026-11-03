package guestboot

import (
	"strings"
	"testing"
)

// The firstboot and watchdog scripts read the SKU with jq under systemd's
// minimal PATH, so they need the same extension the dataset script has. Without
// it a bastion whose only jq is under linuxbrew gets its disk prepared and then
// fails before it can bring tailscale up.
func TestFirstbootAndWatchdogExtendPathBeforeFirstJQ(t *testing.T) {
	for name, script := range map[string]string{
		"firstboot": FirstbootScript,
		"watchdog":  WatchdogScript,
	} {
		line := strings.Index(script, "\n"+datasetPathLine+"\n")
		if line < 0 {
			t.Errorf("%s script is missing the PATH line %q", name, datasetPathLine)

			continue
		}

		if jq := strings.Index(script, "jq "); line > jq {
			t.Errorf("%s script sets PATH after it first runs jq", name)
		}

		if dmi := strings.Index(script, "dmidecode"); line > dmi {
			t.Errorf("%s script sets PATH after it first runs a tool", name)
		}
	}
}
