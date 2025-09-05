package ssh

import (
	"io"
	"time"
)

// ConnectionDetails holds SSH connection information.
type ConnectionDetails struct {
	Host           string
	Port           int
	User           string
	PrivateKeyPath string
	Password       string
	SSHOptions     []string
	UseSSHPass     bool
}

// CommandResult holds the result of a remote command execution.
type CommandResult struct {
	Command  string
	ExitCode int
	Stdout   string
	Stderr   string
	Duration time.Duration
}

// TransferOptions configures file transfer behavior.
type TransferOptions struct {
	Recursive    bool
	Preserve     bool
	Compress     bool
	Progress     io.Writer
	MaxRetries   int
	ChunkSize    int64
	Verify       bool
	BackupRemote bool
}

// ProvisioningOptions configures bastion provisioning behavior.
type ProvisioningOptions struct {
	DryRun      bool
	Force       bool
	Parallel    bool
	Resume      bool
	Verbose     bool
	MaxWorkers  int
	ProgressOut io.Writer
	LogFile     string
}
