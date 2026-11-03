package bastion

import (
	"context"
	"errors"
	"strings"

	"github.com/fivetwenty-io/ocfp-cli/internal/bastion/ssh"
)

// failOnJQSSHClient fails only the commands that touch the jq link.
type failOnJQSSHClient struct{ mockSSHClient }

func (c *failOnJQSSHClient) ExecuteCommand(ctx context.Context, cmd string) (*ssh.CommandResult, error) {
	if strings.Contains(cmd, "/usr/local/bin/jq") {
		return &ssh.CommandResult{ExitCode: 1, Stderr: "sudo: a password is required"}, errors.New("exit status 1")
	}

	return c.mockSSHClient.ExecuteCommand(ctx, cmd)
}
