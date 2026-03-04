package providers

import (
	"context"
	"time"
)

// defaultSSHUser is the default SSH user for cloud VM images.
const defaultSSHUser = "ubuntu"

// ConnectionDetails holds SSH connection information.
type ConnectionDetails struct {
	Host           string
	Port           int
	User           string
	PrivateKeyPath string
	Password       string //nolint:gosec // field name is descriptive, not a hardcoded secret
	SSHOptions     []string
	UseSSHPass     bool
}

// BastionInitializer defines the interface for bastion initialization.
type BastionInitializer interface {
	Validate() error
	PrepareEnvironment() map[string]string
	GetConnectionDetails() (*ConnectionDetails, error)
	Initialize(ctx context.Context) error
}

// CommandResult holds the result of a remote command execution.
type CommandResult struct {
	Command  string
	ExitCode int
	Stdout   string
	Stderr   string
	Duration time.Duration
}
