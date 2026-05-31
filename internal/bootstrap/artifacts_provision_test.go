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

	if !strings.Contains(joined, "ProxyJump=ubuntu@100.109.226.53") {
		t.Errorf("args must ProxyJump through the bastion, got: %s", joined)
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
