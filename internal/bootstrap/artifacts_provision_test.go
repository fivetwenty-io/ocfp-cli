package bootstrap

import (
	"strings"
	"testing"
)

func testProvisionConn() artifactsProvisionConn {
	return artifactsProvisionConn{
		KeyPath:       "/home/op/.ocfp/ocfp-lab-wayne/ssh/id_ed25519",
		User:          "ubuntu",
		BastionHost:   "100.109.226.53",
		ArtifactsHost: "10.64.64.11",
	}
}

func TestBuildArtifactsProvisionSSHArgs_ProxyJumpThroughBastion(t *testing.T) {
	t.Parallel()

	args := buildArtifactsProvisionSSHArgs(testProvisionConn())
	joined := strings.Join(args, " ")

	// The bastion hop is an explicit ProxyCommand (not -o ProxyJump) so the
	// jump's inner ssh inherits the same relaxed host-key flags and a churned
	// bastion key can't trip strict checking. It must still route to the
	// bastion and netcat to the artifacts host (-W %h:%p).
	if !strings.Contains(joined, "ProxyCommand=ssh ") {
		t.Errorf("args must reach the bastion via an explicit ProxyCommand, got: %s", joined)
	}

	if !strings.Contains(joined, "-W %h:%p ubuntu@100.109.226.53") {
		t.Errorf("ProxyCommand must netcat through the bastion, got: %s", joined)
	}

	// The jump hop must carry the same host-key relaxation as the outer hop so
	// a rebuilt bastion's new host key never blocks provisioning.
	if !strings.Contains(joined, "ProxyCommand=ssh -i /home/op/.ocfp/ocfp-lab-wayne/ssh/id_ed25519 -o BatchMode=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null") {
		t.Errorf("ProxyCommand must re-pass relaxed host-key flags to the jump hop, got: %s", joined)
	}

	if !strings.Contains(joined, "ubuntu@10.64.64.11") {
		t.Errorf("args must target the artifacts host, got: %s", joined)
	}
}

func TestBuildArtifactsProvisionSSHArgs_UsesKeyAndBatchMode(t *testing.T) {
	t.Parallel()

	args := buildArtifactsProvisionSSHArgs(testProvisionConn())
	joined := strings.Join(args, " ")

	if !strings.Contains(joined, "-i /home/op/.ocfp/ocfp-lab-wayne/ssh/id_ed25519") {
		t.Errorf("args must pass the identity key, got: %s", joined)
	}

	// Non-interactive: must not hang waiting for a password or host-key prompt.
	if !strings.Contains(joined, "BatchMode=yes") {
		t.Errorf("args must set BatchMode=yes, got: %s", joined)
	}

	if !strings.Contains(joined, "StrictHostKeyChecking") {
		t.Errorf("args must set a StrictHostKeyChecking policy, got: %s", joined)
	}
}

func TestBuildArtifactsProvisionSSHArgs_RemoteCommandLast(t *testing.T) {
	t.Parallel()

	args := buildArtifactsProvisionSSHArgs(testProvisionConn())

	// The remote command must be the final argument so the script can be
	// piped to it over stdin (sudo bash -s).
	last := args[len(args)-1]
	if !strings.Contains(last, "sudo bash -s") {
		t.Errorf("final arg must be the remote command 'sudo bash -s', got: %q", last)
	}

	// The destination must come before the remote command.
	destIdx, cmdIdx := -1, len(args)-1
	for i, a := range args {
		if a == "ubuntu@10.64.64.11" {
			destIdx = i
		}
	}

	if destIdx == -1 || destIdx >= cmdIdx {
		t.Errorf("destination must precede the remote command; args: %v", args)
	}
}
