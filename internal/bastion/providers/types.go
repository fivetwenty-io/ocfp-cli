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
// SSHOptions disables strict host-key checking by default because bastion IPs
// are dynamic and the operator-managed bastion is implicitly trusted. Callers
// that pin known hosts should override SSHOptions.
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
//
// GetConnectionDetails carries a context so provider implementations can
// propagate cancellation and deadlines to underlying cloud SDK calls (e.g.,
// AWS EC2 DescribeInstances when discovering bastion IPs).
type BastionInitializer interface {
	Validate() error
	PrepareEnvironment() map[string]string
	GetConnectionDetails(ctx context.Context) (*ConnectionDetails, error)
}
