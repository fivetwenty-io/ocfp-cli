package providers

import (
	"context"
)

// defaultSSHUser is the default SSH user for cloud VM images.
const defaultSSHUser = "ubuntu"

// defaultAWSSSHUser is the default SSH user for AWS EC2 Amazon Linux images.
const defaultAWSSSHUser = "ec2-user"

// ConnectionDetails holds SSH connection information.
//
// SSHOptions holds extra ssh -o options. Host-key checking is not among
// them: the SSH client verifies the bastion against the user's known_hosts.
type ConnectionDetails struct {
	Host           string
	Port           int
	User           string
	PrivateKeyPath string
	Password       string // #nosec -- field name is descriptive, not a hardcoded secret
	SSHOptions     []string
	UseSSHPass     bool
}

// BastionInitializer defines the interface for bastion initialization.
//
// GetConnectionDetails carries a context so provider implementations can
// propagate cancellation and deadlines to underlying cloud SDK calls (e.g.,
// AWS EC2 DescribeInstances when discovering bastion IPs).
//
// PreviewConnectionDetails is the form of GetConnectionDetails that a dry
// run uses. It makes the same lookups and fails where they fail, but it
// writes no file. It doesn't create the bloc's state directory, and it
// doesn't restore the bloc's SSH key from the config. It returns the path
// that the restored key would have.
type BastionInitializer interface {
	Validate() error
	PrepareEnvironment() map[string]string
	GetConnectionDetails(ctx context.Context) (*ConnectionDetails, error)
	PreviewConnectionDetails(ctx context.Context) (*ConnectionDetails, error)
}
