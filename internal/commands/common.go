package commands

import (
	"context"
	"fmt"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

// initializeProvider initializes the cloud provider.
//
//nolint:ireturn // Returns interface by design for provider abstraction
func initializeProvider(ctx context.Context, cfg *config.Config) (cpi.Provider, error) {
	provider, err := cpi.GetProvider(cfg.Provider)
	if err != nil {
		return nil, fmt.Errorf("failed to get provider: %w", err)
	}

	err = provider.Initialize(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize provider: %w", err)
	}

	return provider, nil
}

// Shared string constants to avoid repeated literals across commands.
const (
	OutputTable = "table"
	OutputJSON  = "json"

	ProtocolTCP = "tcp"

	KeywordAll = "all"

	RoleBastion = "bastion"

	ResourceRouter        = "router"
	ResourceRouters       = "routers"
	ResourceInstance      = "instance"
	ResourceInstances     = "instances"
	ResourceFloatingIP    = "floating_ip"
	ResourcePublicIP      = "public_ip"
	ResourceBucket        = "bucket"
	ResourceSnapshot      = "snapshot"
	ResourceVolume        = "volume"
	ResourceSubnet        = "subnet"
	ResourceLoadBalancer  = "loadbalancer"
	ResourceSecurityGroup    = "security_group"
	ResourceNetworkInterface = "network_interface"

	CategoryNetwork = "network"

	ProviderStackit = "stackit"
)
