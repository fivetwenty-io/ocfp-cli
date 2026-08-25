package commands

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
)

// planText renders a cleanup plan as one line per command for assertions.
func planText(cmds []cleanupCommand) string {
	var sb strings.Builder

	for _, c := range cmds {
		sb.WriteString(c.name + " " + strings.Join(c.args, " ") + "\n")
	}

	return sb.String()
}

// TestVaultCleanupCommands_TouchOnlyOwnBloc guards the eviction the operators
// watched happen: one bloc's cleanup killed a sibling's vault 24 seconds after
// it came up, leaving the survivor's data dir half-initialized ("Vault is
// already initialized").
func TestVaultCleanupCommands_TouchOnlyOwnBloc(t *testing.T) {
	t.Parallel()

	paths := getVaultInceptionPaths("ocfp-lab-drgao", false)
	text := planText(vaultCleanupCommands(paths))

	ownPort := strconv.Itoa(config.InceptionVaultPort("ocfp-lab-drgao"))
	foreignPort := strconv.Itoa(config.InceptionVaultPort("ocfp-lab-drhu"))

	if !strings.Contains(text, ownPort) {
		t.Errorf("cleanup plan does not mention its own port %s:\n%s", ownPort, text)
	}

	if strings.Contains(text, foreignPort) {
		t.Errorf("cleanup plan targets a sibling bloc's port %s:\n%s", foreignPort, text)
	}

	if strings.Contains(text, "8234") {
		t.Errorf("cleanup plan targets the shared legacy port:\n%s", text)
	}
}

// The bare "inception-vault" session and "inception" safe target are shared by
// every bloc on the workstation, so a bloc-scoped cleanup must not delete them.
func TestVaultCleanupCommands_LeavesSharedBareNamesAlone(t *testing.T) {
	t.Parallel()

	paths := getVaultInceptionPaths("ocfp-lab-drgao", false)

	for _, cmd := range vaultCleanupCommands(paths) {
		for _, arg := range cmd.args {
			if arg == "inception-vault" || arg == "inception" {
				t.Errorf("cleanup plan touches shared bare name %q: %v %v", arg, cmd.name, cmd.args)
			}
		}
	}
}

func TestVaultCleanupCommands_TargetsOwnSessionAndTarget(t *testing.T) {
	t.Parallel()

	paths := getVaultInceptionPaths("ocfp-lab-drgao", false)
	text := planText(vaultCleanupCommands(paths))

	if !strings.Contains(text, "ocfp-lab-drgao-inception-vault") {
		t.Errorf("cleanup plan does not kill its own tmux session:\n%s", text)
	}

	if !strings.Contains(text, "ocfp-lab-drgao-inception") {
		t.Errorf("cleanup plan does not delete its own safe target:\n%s", text)
	}
}

func TestVaultCleanupCommands_PortKillSparesThisProcessAndClients(t *testing.T) {
	t.Parallel()

	paths := getVaultInceptionPaths("ocfp-lab-drgao", false)
	text := planText(vaultCleanupCommands(paths))

	if !strings.Contains(text, "-sTCP:LISTEN") {
		t.Errorf("port kill is not restricted to listeners, so it also kills clients of the port:\n%s", text)
	}

	if !strings.Contains(text, "grep -vx "+strconv.Itoa(os.Getpid())) {
		t.Errorf("port kill does not exclude this process, so the CLI can SIGKILL itself:\n%s", text)
	}
}
