package commands

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/state"
	"github.com/spf13/cobra"
)

func TestResourceDisplayFlags_AnyFlagSet(t *testing.T) {
	tests := []struct {
		name     string
		flags    ResourceDisplayFlags
		expected bool
	}{
		{
			name:     "no flags set",
			flags:    ResourceDisplayFlags{},
			expected: false,
		},
		{
			name: "servers flag set",
			flags: ResourceDisplayFlags{
				Servers: true,
			},
			expected: true,
		},
		{
			name: "multiple flags set",
			flags: ResourceDisplayFlags{
				Servers: true,
				Volumes: true,
			},
			expected: true,
		},
		{
			name: "all flag set (should return false as it's not a specific flag)",
			flags: ResourceDisplayFlags{
				All: true,
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.flags.AnyFlagSet()
			if got != tt.expected {
				t.Errorf("AnyFlagSet() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestResourceDisplayFlags_NormalizeFlags(t *testing.T) {
	tests := []struct {
		name     string
		flags    ResourceDisplayFlags
		expected ResourceDisplayFlags
	}{
		{
			name:  "no flags set should enable all",
			flags: ResourceDisplayFlags{},
			expected: ResourceDisplayFlags{
				All: true,
			},
		},
		{
			name: "specific flag set should not change",
			flags: ResourceDisplayFlags{
				Servers: true,
			},
			expected: ResourceDisplayFlags{
				Servers: true,
				All:     false,
			},
		},
		{
			name: "all flag already set should not change",
			flags: ResourceDisplayFlags{
				All: true,
			},
			expected: ResourceDisplayFlags{
				All: true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.flags.NormalizeFlags()
			if tt.flags.All != tt.expected.All {
				t.Errorf("NormalizeFlags() All = %v, want %v", tt.flags.All, tt.expected.All)
			}
		})
	}
}

func TestGetResourceTypesForFlag(t *testing.T) {
	tests := []struct {
		name     string
		flag     string
		expected []string
	}{
		{
			name:     "servers flag",
			flag:     FlagServers,
			expected: []string{state.ResourceTypeInstance},
		},
		{
			name:     "volumes flag",
			flag:     FlagVolumes,
			expected: []string{state.ResourceTypeVolume},
		},
		{
			name:     "public-ips flag (multiple types)",
			flag:     FlagPublicIPs,
			expected: []string{state.ResourceTypePublicIP, state.ResourceTypeFloatingIP},
		},
		{
			name:     "unknown flag",
			flag:     "unknown",
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetResourceTypesForFlag(tt.flag)
			if len(got) != len(tt.expected) {
				t.Errorf("GetResourceTypesForFlag() returned %d types, want %d", len(got), len(tt.expected))
				return
			}

			for i, expected := range tt.expected {
				if got[i] != expected {
					t.Errorf("GetResourceTypesForFlag()[%d] = %v, want %v", i, got[i], expected)
				}
			}
		})
	}
}

func TestGetFlagForResourceType(t *testing.T) {
	tests := []struct {
		name         string
		resourceType string
		expected     string
	}{
		{
			name:         "compute instance",
			resourceType: state.ResourceTypeInstance,
			expected:     FlagServers,
		},
		{
			name:         "block volume",
			resourceType: state.ResourceTypeVolume,
			expected:     FlagVolumes,
		},
		{
			name:         "public IP",
			resourceType: state.ResourceTypePublicIP,
			expected:     FlagPublicIPs,
		},
		{
			name:         "floating IP",
			resourceType: state.ResourceTypeFloatingIP,
			expected:     FlagPublicIPs,
		},
		{
			name:         "unknown type",
			resourceType: "unknown_type",
			expected:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetFlagForResourceType(tt.resourceType)
			if got != tt.expected {
				t.Errorf("GetFlagForResourceType() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestAddResourceDisplayFlags(t *testing.T) {
	cmd := &cobra.Command{
		Use: "test",
	}

	flags := &ResourceDisplayFlags{}
	AddResourceDisplayFlags(cmd, flags)

	// Test that all flags were added
	requiredFlags := []string{
		FlagAll,
		FlagServers,
		"instances",
		FlagVolumes,
		FlagBuckets,
		FlagLoadBalancers,
		"lbs",
		FlagPublicIPs,
		FlagKeys,
		"key-pairs",
		FlagNetworks,
		"nets",
		FlagSubnets,
		FlagSecurityGroups,
		"sgs",
		FlagRouters,
		FlagSnapshots,
	}

	for _, flagName := range requiredFlags {
		flag := cmd.Flags().Lookup(flagName)
		if flag == nil {
			t.Errorf("Flag %q was not added to command", flagName)
		}
	}
}

func TestParseDisplayFlagsFromCmd(t *testing.T) {
	tests := []struct {
		name     string
		flagsSet map[string]bool
		expected ResourceDisplayFlags
	}{
		{
			name:     "no flags set",
			flagsSet: map[string]bool{},
			expected: ResourceDisplayFlags{},
		},
		{
			name: "servers flag set",
			flagsSet: map[string]bool{
				FlagServers: true,
			},
			expected: ResourceDisplayFlags{
				Servers: true,
			},
		},
		{
			name: "instances alias set",
			flagsSet: map[string]bool{
				"instances": true,
			},
			expected: ResourceDisplayFlags{
				Servers: true,
			},
		},
		{
			name: "multiple flags set",
			flagsSet: map[string]bool{
				FlagServers: true,
				FlagVolumes: true,
				FlagBuckets: true,
			},
			expected: ResourceDisplayFlags{
				Servers: true,
				Volumes: true,
				Buckets: true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := &cobra.Command{
				Use: "test",
			}

			flags := &ResourceDisplayFlags{}
			AddResourceDisplayFlags(cmd, flags)

			// Set the flags
			for flagName, value := range tt.flagsSet {
				_ = cmd.Flags().Set(flagName, "true")
				_ = value // Unused
			}

			got := ParseDisplayFlagsFromCmd(cmd)

			if got.Servers != tt.expected.Servers {
				t.Errorf("ParseDisplayFlagsFromCmd() Servers = %v, want %v", got.Servers, tt.expected.Servers)
			}
			if got.Volumes != tt.expected.Volumes {
				t.Errorf("ParseDisplayFlagsFromCmd() Volumes = %v, want %v", got.Volumes, tt.expected.Volumes)
			}
			if got.Buckets != tt.expected.Buckets {
				t.Errorf("ParseDisplayFlagsFromCmd() Buckets = %v, want %v", got.Buckets, tt.expected.Buckets)
			}
		})
	}
}
