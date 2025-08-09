package commands

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// NewTeardownCmd creates the teardown command
func NewTeardownCmd() *cobra.Command {
	var (
		force      bool
		dryRun     bool
		skip       []string
		publicIPs  bool
		all        bool
		nuke       bool
		servers    bool
		volumes    bool
		snapshots  bool
		buckets    bool
		secGroups  bool
		network    bool
	)

	cmd := &cobra.Command{
		Use:   "teardown",
		Short: "Remove all resources created by bootstrap",
		Long: `Teardown removes resources created by OCFP bootstrap and BOSH deployments.

The command supports different modes:
- Default: Delete only bootstrap-created resources
- All: Delete all OCFP/BOSH-managed resources (--all)
- Nuke: Delete ALL resources in the project (--nuke, requires --force)

Resources are deleted in dependency order:
1. Servers (to free volumes and network interfaces)
2. Load balancers
3. Snapshots (to allow volume deletion)
4. Volumes
5. Buckets (after emptying if needed)
6. Security groups
7. Networks
8. Public IPs (only if --public-ips flag is used)`,
		Example: `  # Interactive teardown (with confirmation)
  ocfp teardown --bloc-name production

  # Force teardown (no confirmation)
  ocfp teardown --bloc-name production --force

  # Dry run to preview deletions
  ocfp teardown --bloc-name production --dry-run

  # Delete specific resource types
  ocfp teardown --bloc-name production --servers --volumes

  # Skip specific resource types
  ocfp teardown --bloc-name production --skip network --skip storage

  # Delete including public IPs (use with caution!)
  ocfp teardown --bloc-name production --public-ips --force

  # DANGER: Delete ALL resources in project
  ocfp teardown --bloc-name production --nuke --force`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTeardown(cmd, args)
		},
	}

	// Command-specific flags
	cmd.Flags().BoolVar(&force, "force", false, "skip confirmation prompts")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview what would be deleted without actually deleting")
	cmd.Flags().StringSliceVar(&skip, "skip", []string{}, "skip deletion of specific resource types")
	cmd.Flags().BoolVar(&publicIPs, "public-ips", false, "include public IPs in deletion")
	cmd.Flags().BoolVar(&all, "all", false, "delete all OCFP/BOSH-managed resources")
	cmd.Flags().BoolVar(&nuke, "nuke", false, "DANGER: delete ALL resources in project")
	
	// Resource type flags
	cmd.Flags().BoolVar(&servers, "servers", false, "delete servers")
	cmd.Flags().BoolVar(&volumes, "volumes", false, "delete volumes")
	cmd.Flags().BoolVar(&snapshots, "snapshots", false, "delete snapshots")
	cmd.Flags().BoolVar(&buckets, "buckets", false, "delete buckets")
	cmd.Flags().BoolVar(&secGroups, "security-groups", false, "delete security groups")
	cmd.Flags().BoolVar(&network, "network", false, "delete networks")

	// Bind flags to viper
	viper.BindPFlag("teardown.force", cmd.Flags().Lookup("force"))
	viper.BindPFlag("teardown.dry_run", cmd.Flags().Lookup("dry-run"))
	viper.BindPFlag("teardown.skip", cmd.Flags().Lookup("skip"))
	viper.BindPFlag("teardown.public_ips", cmd.Flags().Lookup("public-ips"))
	viper.BindPFlag("teardown.all", cmd.Flags().Lookup("all"))
	viper.BindPFlag("teardown.nuke", cmd.Flags().Lookup("nuke"))

	return cmd
}

func runTeardown(cmd *cobra.Command, args []string) error {
	// Get configuration values
	blocName := viper.GetString("bloc_name")
	iaas := viper.GetString("iaas")
	force := viper.GetBool("teardown.force")
	dryRun := viper.GetBool("teardown.dry_run")
	nuke := viper.GetBool("teardown.nuke")

	// Validate required configuration
	if blocName == "" {
		return fmt.Errorf("bloc-name is required")
	}
	if iaas == "" {
		return fmt.Errorf("iaas provider is required")
	}

	// Validate nuke mode
	if nuke && !force {
		return fmt.Errorf("--nuke requires --force for safety")
	}

	// TODO: Load configuration
	// TODO: Initialize provider
	// TODO: Discover resources
	// TODO: Build dependency graph
	// TODO: Execute teardown workflow

	// Placeholder output
	fmt.Printf("Teardown configuration:\n")
	fmt.Printf("  Bloc: %s\n", blocName)
	fmt.Printf("  Provider: %s\n", iaas)
	fmt.Printf("  Mode: %s\n", getTeardownMode(viper.GetBool("teardown.all"), nuke))
	fmt.Printf("  Dry Run: %v\n", dryRun)
	fmt.Printf("  Force: %v\n", force)
	
	if skip := viper.GetStringSlice("teardown.skip"); len(skip) > 0 {
		fmt.Printf("  Skip: %s\n", strings.Join(skip, ", "))
	}

	fmt.Println("\n[This is a placeholder - teardown implementation pending]")

	return nil
}

func getTeardownMode(all, nuke bool) string {
	if nuke {
		return "NUKE (delete ALL resources)"
	}
	if all {
		return "ALL (delete all OCFP/BOSH resources)"
	}
	return "BOOTSTRAP (delete bootstrap-created resources)"
}