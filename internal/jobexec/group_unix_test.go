//go:build unix

package jobexec

import "testing"

// A group kill reaches the verb's own children, which share its group.
func TestKillGroupKillsGrandchildren(t *testing.T) {
	t.Parallel()

	killsTheTree(t)
}

// A group kill reaches no process outside the verb's own group.
func TestKillGroupSparesAnotherGroup(t *testing.T) {
	t.Parallel()

	killed := startGroupChild(t, "tree")
	spared := startGroupChild(t, "echo")

	killed.echo(t, "ping")
	spared.echo(t, "ping")

	must(t, killed.g.kill())

	killed.wantEOF(t)
	spared.echo(t, "still here")

	if killed.g.cmd.Process.Pid == spared.g.cmd.Process.Pid {
		t.Fatal("the two helpers share a pid")
	}
}
