package ssh

import (
	"io"
	"time"
)

const (
	// SSH connection defaults.
	DefaultSSHPort = 22

	// File permissions.
	sshDirectoryMode = 0700
	privateKeyMode   = 0600
	localDirMode     = 0750

	// Timeouts.
	defaultCommandTimeout = 30 * time.Second
	shortTimeout          = 10 * time.Second
	longTimeout           = 45 * time.Second
	mediumTimeout         = 15 * time.Second

	// Transfer configuration.
	transferBufferSize = 32 * 1024 // 32KB
	percentageBase     = 100

	// SSH key configuration.
	minKeySize = 2048

	// Channel buffer sizes.
	defaultChannelBuffer = 2
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
	OCFPOnly    bool
	ConfigOnly  bool
}
