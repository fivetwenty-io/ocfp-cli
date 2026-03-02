package commands

import (
	"fmt"

	"github.com/ocfp/ocfp-cli-go/internal/state"
	"github.com/spf13/cobra"
)

// Output format constants.
const (
	OutputFormatTable = "table"
	OutputFormatJSON  = "json"
	OutputFormatYAML  = "yaml"
)

// Resource display flag names.
const (
	FlagAll            = "all"
	FlagServers        = "servers"
	FlagVolumes        = "volumes"
	FlagBuckets        = "buckets"
	FlagLoadBalancers  = "load-balancers"
	FlagPublicIPs      = "public-ips"
	FlagKeys           = "keys"
	FlagNetworks       = "networks"
	FlagSubnets        = "subnets"
	FlagSecurityGroups = "security-groups"
	FlagRouters        = "routers"
	FlagSnapshots      = "snapshots"
)

// ResourceDisplayFlags holds the state of resource display flags.
type ResourceDisplayFlags struct {
	All            bool
	Servers        bool
	Volumes        bool
	Buckets        bool
	LoadBalancers  bool
	PublicIPs      bool
	Keys           bool
	Networks       bool
	Subnets        bool
	SecurityGroups bool
	Routers        bool
	Snapshots      bool
}

// GetResourceTypesForFlag returns the resource types associated with a display flag.
func GetResourceTypesForFlag(flag string) []string {
	mapping := map[string][]string{
		FlagServers:        {state.ResourceTypeInstance},
		FlagVolumes:        {state.ResourceTypeVolume},
		FlagBuckets:        {state.ResourceTypeBucket},
		FlagLoadBalancers:  {state.ResourceTypeLoadBalancer},
		FlagPublicIPs:      {state.ResourceTypePublicIP, state.ResourceTypeFloatingIP},
		FlagKeys:           {state.ResourceTypeKeyPair},
		FlagNetworks:       {state.ResourceTypeNetwork},
		FlagSubnets:        {state.ResourceTypeSubnet},
		FlagSecurityGroups: {state.ResourceTypeSecurityGroup},
		FlagRouters:        {state.ResourceTypeRouter},
		FlagSnapshots:      {state.ResourceTypeSnapshot},
	}

	return mapping[flag]
}

// GetFlagForResourceType returns the display flag associated with a resource type.
func GetFlagForResourceType(resourceType string) string {
	mapping := map[string]string{
		state.ResourceTypeInstance:      FlagServers,
		state.ResourceTypeVolume:        FlagVolumes,
		state.ResourceTypeBucket:        FlagBuckets,
		state.ResourceTypeLoadBalancer:  FlagLoadBalancers,
		state.ResourceTypePublicIP:      FlagPublicIPs,
		state.ResourceTypeFloatingIP:    FlagPublicIPs,
		state.ResourceTypeKeyPair:       FlagKeys,
		state.ResourceTypeNetwork:       FlagNetworks,
		state.ResourceTypeSubnet:        FlagSubnets,
		state.ResourceTypeSecurityGroup: FlagSecurityGroups,
		state.ResourceTypeRouter:        FlagRouters,
		state.ResourceTypeSnapshot:      FlagSnapshots,
	}

	return mapping[resourceType]
}

// AddResourceDisplayFlags adds resource display flags to a command.
func AddResourceDisplayFlags(cmd *cobra.Command, flags *ResourceDisplayFlags) {
	cmd.Flags().BoolVar(&flags.All, FlagAll, false, "show all resources (default)")
	cmd.Flags().BoolVar(&flags.Servers, FlagServers, false, "show compute instances")
	cmd.Flags().BoolVar(&flags.Servers, "instances", false, "alias for --servers")
	cmd.Flags().BoolVar(&flags.Volumes, FlagVolumes, false, "show block volumes")
	cmd.Flags().BoolVar(&flags.Buckets, FlagBuckets, false, "show object storage buckets")
	cmd.Flags().BoolVar(&flags.LoadBalancers, FlagLoadBalancers, false, "show load balancers")
	cmd.Flags().BoolVar(&flags.LoadBalancers, "lbs", false, "alias for --load-balancers")
	cmd.Flags().BoolVar(&flags.PublicIPs, FlagPublicIPs, false, "show public IP addresses")
	cmd.Flags().BoolVar(&flags.Keys, FlagKeys, false, "show SSH key pairs")
	cmd.Flags().BoolVar(&flags.Keys, "key-pairs", false, "alias for --keys")
	cmd.Flags().BoolVar(&flags.Networks, FlagNetworks, false, "show networks/VPCs")
	cmd.Flags().BoolVar(&flags.Networks, "nets", false, "alias for --networks")
	cmd.Flags().BoolVar(&flags.Subnets, FlagSubnets, false, "show subnets")
	cmd.Flags().BoolVar(&flags.SecurityGroups, FlagSecurityGroups, false, "show security groups")
	cmd.Flags().BoolVar(&flags.SecurityGroups, "sgs", false, "alias for --security-groups")
	cmd.Flags().BoolVar(&flags.Routers, FlagRouters, false, "show routers")
	cmd.Flags().BoolVar(&flags.Snapshots, FlagSnapshots, false, "show volume snapshots")
}

// AnyFlagSet returns true if any resource-specific flag is set (excluding --all).
func (f *ResourceDisplayFlags) AnyFlagSet() bool {
	return f.Servers || f.Volumes || f.Buckets || f.LoadBalancers ||
		f.PublicIPs || f.Keys || f.Networks || f.Subnets ||
		f.SecurityGroups || f.Routers || f.Snapshots
}

// GetEnabledFlags returns a map of flag names to their enabled state.
func (f *ResourceDisplayFlags) GetEnabledFlags() map[string]bool {
	return map[string]bool{
		FlagServers:        f.Servers,
		FlagVolumes:        f.Volumes,
		FlagBuckets:        f.Buckets,
		FlagLoadBalancers:  f.LoadBalancers,
		FlagPublicIPs:      f.PublicIPs,
		FlagKeys:           f.Keys,
		FlagNetworks:       f.Networks,
		FlagSubnets:        f.Subnets,
		FlagSecurityGroups: f.SecurityGroups,
		FlagRouters:        f.Routers,
		FlagSnapshots:      f.Snapshots,
	}
}

// NormalizeFlags applies default logic: if no specific flags are set, enable all.
func (f *ResourceDisplayFlags) NormalizeFlags() {
	if !f.AnyFlagSet() && !f.All {
		f.All = true
	}
}

// ValidateOutputFormat validates the output format string.
func ValidateOutputFormat(format string) error {
	validFormats := map[string]bool{
		OutputFormatTable: true,
		OutputFormatJSON:  true,
		OutputFormatYAML:  true,
	}

	if !validFormats[format] {
		return fmt.Errorf("%w %q: must be table, json, or yaml", ErrInvalidOutputFormat, format)
	}

	return nil
}
