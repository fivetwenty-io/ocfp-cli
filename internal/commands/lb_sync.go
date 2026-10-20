package commands

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/ui"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// syncLBConfig holds configuration for load balancer synchronization.
type syncLBConfig struct {
	name         string
	removeUnused bool
	dryRun       bool
	output       string
	blocName     string
	cfg          *config.Config
	spec         config.LBService
}

// renderSyncLBDryRunPlan generates and renders the dry-run plan for LB sync.
func renderSyncLBDryRunPlan(ctx context.Context, config *syncLBConfig, netMgr cpi.NetworkManager) error {
	syncTable := &ui.Table{
		Title:    "DRY RUN — LB Sync Plan",
		Summary:  "",
		Sections: nil,
	}

	addLoadBalancerSection(syncTable, config)

	desired := resolveDesiredIPs(config)
	existing := getCurrentMembers(ctx, netMgr, config, syncTable)

	addMemberChangeSections(syncTable, config, desired, existing)

	output := getOutputFormat(config.output)

	err := ui.Render(syncTable, strings.ToLower(output))
	if err != nil {
		return fmt.Errorf("failed to render sync table: %w", err)
	}

	return nil
}

// addLoadBalancerSection adds the load balancer configuration section.
func addLoadBalancerSection(syncTable *ui.Table, config *syncLBConfig) {
	syncTable.Sections = append(syncTable.Sections, ui.Section{
		Title:   "Load Balancer",
		Headers: []string{"NAME", "TYPE", "PROTOCOL", "PORT"},
		Rows:    [][]string{{config.name, "external", config.spec.Protocol, strconv.Itoa(config.spec.Port)}},
	})
}

// resolveDesiredIPs resolves the desired IP addresses from targets.
func resolveDesiredIPs(config *syncLBConfig) map[string]bool {
	desired := map[string]bool{}

	for _, tgt := range config.spec.Targets {
		targetIP := resolveTargetIP(tgt, config.blocName)
		if isValidIP(targetIP) {
			desired[targetIP] = true
		}
	}

	return desired
}

// resolveTargetIP resolves a target to an IP address.
func resolveTargetIP(target, blocName string) string {
	if isToken(target) {
		resolvedIP, err := ResolveTargetIP(blocName, target)
		if err == nil {
			return resolvedIP
		}
	}

	return target
}

// isValidIP checks if a string is a valid IP address.
func isValidIP(ip string) bool {
	return net.ParseIP(ip) != nil
}

// getCurrentMembers gets current backend members and adds them to the table.
func getCurrentMembers(ctx context.Context, netMgr cpi.NetworkManager, config *syncLBConfig, syncTable *ui.Table) map[string]bool {
	existing := map[string]bool{}

	lb, err := netMgr.GetLoadBalancer(ctx, config.name)
	if err != nil || lb == nil {
		return existing
	}

	pools, err := netMgr.GetBackendPools(ctx, lb.ID)
	if err != nil || len(pools) == 0 {
		return existing
	}

	rows := buildCurrentMembersRows(pools[0].Members, existing)
	if len(rows) > 0 {
		syncTable.Sections = append(syncTable.Sections, ui.Section{
			Title:   "Current Members",
			Headers: []string{"IP", "PORT"},
			Rows:    rows,
		})
	}

	return existing
}

// buildCurrentMembersRows builds rows for current members and populates existing map.
func buildCurrentMembersRows(members []*cpi.BackendMember, existing map[string]bool) [][]string {
	rows := make([][]string, 0, len(members))

	for _, member := range members {
		existing[member.IPAddress] = true
		rows = append(rows, []string{member.IPAddress, strconv.Itoa(member.Port)})
	}

	return rows
}

// addMemberChangeSections adds sections for member additions and removals.
func addMemberChangeSections(syncTable *ui.Table, config *syncLBConfig, desired, existing map[string]bool) {
	addMembersToAddSection(syncTable, config, desired, existing)
	addMembersToRemoveSection(syncTable, config, desired, existing)
}

// addMembersToAddSection adds section for members to be added.
func addMembersToAddSection(syncTable *ui.Table, config *syncLBConfig, desired, existing map[string]bool) {
	addRows := buildAddRows(desired, existing, config.spec.Port)
	if len(addRows) > 0 {
		syncTable.Sections = append(syncTable.Sections, ui.Section{
			Title:   "Add",
			Headers: []string{"IP", "PORT"},
			Rows:    addRows,
		})
	}
}

// buildAddRows builds rows for members to be added.
func buildAddRows(desired, existing map[string]bool, port int) [][]string {
	addRows := make([][]string, 0, len(desired))

	for ip := range desired {
		if !existing[ip] {
			addRows = append(addRows, []string{ip, strconv.Itoa(port)})
		}
	}

	return addRows
}

// addMembersToRemoveSection adds section for members to be removed.
func addMembersToRemoveSection(syncTable *ui.Table, config *syncLBConfig, desired, existing map[string]bool) {
	if !config.removeUnused {
		return
	}

	remRows := buildRemoveRows(desired, existing)
	if len(remRows) > 0 {
		syncTable.Sections = append(syncTable.Sections, ui.Section{
			Title:   "Remove",
			Headers: []string{"IP"},
			Rows:    remRows,
		})
	}
}

// buildRemoveRows builds rows for members to be removed.
func buildRemoveRows(desired, existing map[string]bool) [][]string {
	remRows := make([][]string, 0, len(existing))

	for ip := range existing {
		if !desired[ip] {
			remRows = append(remRows, []string{ip})
		}
	}

	return remRows
}

// getOutputFormat returns the output format, defaulting to table if empty.
func getOutputFormat(output string) string {
	if output == "" {
		return OutputTable
	}

	return output
}

func executeSyncLBSync(ctx context.Context, cfg *syncLBConfig, lbMgr cpi.LoadBalancerManager, netMgr cpi.NetworkManager) error {
	desired, err := resolveLBTargets(cfg.blocName, cfg.spec.Targets)
	if err != nil {
		return err
	}

	lb, err := ensureLoadBalancerByName(ctx, lbMgr, cfg.name, cfg.spec.Protocol, cfg.spec.Port)
	if err != nil {
		return err
	}

	return reconcileNativeLBMembers(ctx, netMgr, lb.ID, desired, cfg.spec.Port, cfg.removeUnused)
}

// newLBSyncCmd syncs an LB's backend pool from bloc config under lbs:<name>.
func newLBSyncCmd() *cobra.Command {
	var (
		name         string
		removeUnused bool
		dryRun       bool
		output       string
	)

	//nolint:exhaustruct // Using zero values for optional fields
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Sync a load balancer from bloc config (lbs:)",
		RunE:  makeLBSyncRunFunc(&name, &removeUnused, &dryRun, &output),
	}

	addLBSyncFlags(cmd, &name, &removeUnused, &dryRun, &output)

	return cmd
}

func makeLBSyncRunFunc(name *string, removeUnused, dryRun *bool, output *string) func(*cobra.Command, []string) error {
	return func(_cmd *cobra.Command, _args []string) error {
		if *name == "" {
			return ErrNameIsRequired
		}

		config, err := prepareLBSyncConfig(*name, *removeUnused, *dryRun, *output)
		if err != nil {
			return err
		}

		ctx := context.Background()

		return executeLBSyncCommand(ctx, config)
	}
}

func prepareLBSyncConfig(name string, removeUnused, dryRun bool, output string) (*syncLBConfig, error) {
	configFile := viper.GetString("config")
	blocName := viper.GetString("bloc")

	cfg, err := config.LoadWithParams(configFile, blocName)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	spec, ok := cfg.LBs[name]
	if !ok {
		return nil, ErrLBEntryNotFoundInConfig(name)
	}

	if spec.Port <= 0 {
		return nil, ErrLBPortMustBeGreaterThanZero(name)
	}

	if spec.Protocol == "" {
		spec.Protocol = ProtocolTCP
	}

	return &syncLBConfig{
		name:         name,
		removeUnused: removeUnused,
		dryRun:       dryRun,
		output:       output,
		blocName:     blocName,
		cfg:          cfg,
		spec:         spec,
	}, nil
}

func executeLBSyncCommand(ctx context.Context, config *syncLBConfig) error {
	provider, err := cpi.GetProvider(config.cfg.Provider)
	if err != nil {
		return fmt.Errorf("get provider: %w", err)
	}

	err = provider.Initialize(ctx, config.cfg)
	if err != nil {
		return fmt.Errorf("init provider: %w", err)
	}

	defer func() { _ = provider.Cleanup(ctx) }()

	lbMgr := provider.LoadBalancerManager()
	if lbMgr == nil {
		return ErrProviderLacksLoadBalancerManager
	}

	netMgr := provider.NetworkManager()
	if netMgr == nil {
		return ErrProviderLacksNetworkManager
	}

	if config.dryRun {
		return renderSyncLBDryRunPlan(ctx, config, netMgr)
	}

	return executeSyncLBSync(ctx, config, lbMgr, netMgr)
}

func addLBSyncFlags(cmd *cobra.Command, name *string, removeUnused, dryRun *bool, output *string) {
	cmd.Flags().StringVar(name, "name", "", "Name of the LB (must exist under lbs: in config)")
	cmd.Flags().BoolVar(removeUnused, "remove-unused", false, "Remove backends not present in config")
	cmd.Flags().BoolVar(dryRun, "dry-run", false, "preview changes without making them")
	cmd.Flags().StringVar(output, "output", OutputTable, "output format: table|json|yaml (dry-run)")
}
