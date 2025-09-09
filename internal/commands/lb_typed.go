package commands

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/state"
	"github.com/ocfp/ocfp-cli-go/internal/ui"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const (
	// Load balancer port constants for typed commands.
	OpsHTTPSPort      = 443
	RouterHTTPPort    = 80
	RouterHTTPSPort   = 443
	TCPRouterPort     = 1024
	CFSSHPort         = 2222
	DefaultWeight     = 1
	DefaultMemberPort = 0
)

// setupProviderAndManagers loads config, initializes provider, and returns managers + cleanup.
//
//nolint:ireturn
func setupProviderAndManagers(ctx context.Context, configFile, blocName string) (*config.Config, cpi.LoadBalancerManager, cpi.NetworkManager, func(), error) {
	cfg, err := config.LoadWithParams(configFile, blocName)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("load config: %w", err)
	}

	provider, err := cpi.GetProvider(cfg.Provider)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("get provider: %w", err)
	}

	err = provider.Initialize(ctx, cfg)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("init provider: %w", err)
	}

	cleanup := func() { _ = provider.Cleanup(ctx) }

	lbMgr := provider.LoadBalancer()
	if lbMgr == nil {
		cleanup()

		return nil, nil, nil, nil, ErrProviderLacksLoadBalancerManager
	}

	netMgr := provider.Network()
	if netMgr == nil {
		cleanup()

		return nil, nil, nil, nil, ErrProviderLacksNetworkManager
	}

	return cfg, lbMgr, netMgr, cleanup, nil
}

// opsLBConfig holds configuration for ops load balancer management.
type opsLBConfig struct {
	name            string
	port            int
	protocol        string
	includeDoomsday bool
	removeUnused    bool
	dryRun          bool
	output          string
	blocName        string
	cfg             *config.Config
}

// getOpsLBDefaultBackends returns the default backend configuration for ops LB.
func getOpsLBDefaultBackends(includeDoomsday bool) []struct{ key, index string } {
	backends := []struct{ key, index string }{
		{"vault_ip", "0"},
		{"prometheus_ip", "0"},
		{"shield_ip", "0"},
	}
	if includeDoomsday {
		backends = append(backends, struct{ key, index string }{"doomsday_ip", "1"})
	}

	return backends
}

// renderOpsLBDryRunPlan generates and renders the dry-run plan for ops load balancer.
func renderOpsLBDryRunPlan(ctx context.Context, config *opsLBConfig, netMgr cpi.NetworkManager) error {
	opsTable := createOpsLBTable(config)
	desired := buildOpsLBDesiredIPs(config)

	existing, err := getExistingLBMembers(ctx, config, netMgr, opsTable)
	if err != nil {
		return err
	}

	addPlannedLBChanges(config, opsTable, desired, existing)

	return renderOpsLBTable(config, opsTable)
}

func createOpsLBTable(config *opsLBConfig) *ui.Table {
	opsTable := &ui.Table{
		Title:    "DRY RUN — Ops LB Plan",
		Summary:  "",
		Sections: nil,
	}
	opsTable.Sections = append(opsTable.Sections, ui.Section{
		Title:   "Load Balancer",
		Headers: []string{"NAME", "TYPE", "PROTOCOL", "PORT"},
		Rows:    [][]string{{config.name, "external", config.protocol, strconv.Itoa(config.port)}},
	})

	return opsTable
}

func getExistingLBMembers(ctx context.Context, config *opsLBConfig, netMgr cpi.NetworkManager, opsTable *ui.Table) (map[string]bool, error) {
	existing := map[string]bool{}

	loadBalancer, err := netMgr.GetLoadBalancer(ctx, config.name)
	if err != nil {
		return existing, fmt.Errorf("failed to get load balancer %s: %w", config.name, err)
	}

	if loadBalancer == nil {
		return existing, nil
	}

	pools, err := netMgr.GetBackendPools(ctx, loadBalancer.ID)
	if err != nil {
		return existing, fmt.Errorf("failed to get backend pools for load balancer %s: %w", loadBalancer.ID, err)
	}

	if len(pools) == 0 {
		return existing, nil
	}

	rows := make([][]string, 0, len(pools[0].Members))
	for _, m := range pools[0].Members {
		existing[m.IPAddress] = true
		rows = append(rows, []string{m.IPAddress, strconv.Itoa(m.Port)})
	}

	opsTable.Sections = append(opsTable.Sections, ui.Section{
		Title:   "Current Members",
		Headers: []string{"IP", "PORT"},
		Rows:    rows,
	})

	return existing, nil
}

func addPlannedLBChanges(config *opsLBConfig, opsTable *ui.Table, desired map[string]bool, existing map[string]bool) {
	addAddSection(config, opsTable, desired, existing)
	addRemoveSection(config, opsTable, desired, existing)
}

func addAddSection(config *opsLBConfig, opsTable *ui.Table, desired map[string]bool, existing map[string]bool) {
	addRows := make([][]string, 0, len(desired))
	for ip := range desired {
		if !existing[ip] {
			addRows = append(addRows, []string{ip, strconv.Itoa(config.port)})
		}
	}

	if len(addRows) > 0 {
		opsTable.Sections = append(opsTable.Sections, ui.Section{
			Title:   "Add",
			Headers: []string{"IP", "PORT"},
			Rows:    addRows,
		})
	}
}

func addRemoveSection(config *opsLBConfig, opsTable *ui.Table, desired map[string]bool, existing map[string]bool) {
	if !config.removeUnused {
		return
	}

	remRows := make([][]string, 0, len(existing))
	for ip := range existing {
		if !desired[ip] {
			remRows = append(remRows, []string{ip})
		}
	}

	if len(remRows) > 0 {
		opsTable.Sections = append(opsTable.Sections, ui.Section{
			Title:   "Remove",
			Headers: []string{"IP"},
			Rows:    remRows,
		})
	}
}

func renderOpsLBTable(config *opsLBConfig, opsTable *ui.Table) error {
	output := config.output
	if output == "" {
		output = OutputTable
	}

	err := ui.Render(opsTable, strings.ToLower(output))
	if err != nil {
		return fmt.Errorf("failed to render ops table: %w", err)
	}

	return nil
}

// executeOpsLBSync performs the actual load balancer synchronization.
// syncOpsLBFromConfig syncs ops LB using config spec if available.
func syncOpsLBFromConfig(ctx context.Context, config *opsLBConfig, netMgr cpi.NetworkManager, loadBalancer *cpi.LoadBalancer) error {
	spec, ok := config.cfg.LBs[config.name]
	if !ok || len(spec.Targets) == 0 {
		return nil // No config spec available, use fallback
	}

	if spec.Protocol == "" {
		spec.Protocol = config.protocol
	}

	if spec.Port == 0 {
		spec.Port = config.port
	}

	return reconcileLBFromSpec(ctx, netMgr, loadBalancer, config.blocName, spec, config.removeUnused)
}

// buildOpsLBDesiredIPs builds the desired IPs map from reserved IPs.
func buildOpsLBDesiredIPs(config *opsLBConfig) map[string]bool {
	log := logger.Get()
	backends := getOpsLBDefaultBackends(config.includeDoomsday)
	desired := map[string]bool{}

	for _, b := range backends {
		backendIP, err := ResolveReservedIP(config.blocName, fmt.Sprintf("reserved:%s:%s", b.key, b.index))
		if err != nil {
			log.Warn("reserved not found", "key", b.key, "index", b.index, "err", err)

			continue
		}

		desired[backendIP] = true
	}

	return desired
}

// addOpsBackendMembers adds missing backend members for ops LB.
func addOpsBackendMembers(ctx context.Context, netMgr cpi.NetworkManager, lbID string, desired, existing map[string]bool, port int) {
	log := logger.Get()

	for memberIP := range desired {
		if existing[memberIP] {
			log.Info("Backend exists", "ip", memberIP)

			continue
		}

		member := &cpi.BackendMember{
			ID:         "",
			IPAddress:  memberIP,
			Port:       port,
			TargetPort: DefaultMemberPort,
			Weight:     DefaultWeight,
			Status:     "",
		}

		err := netMgr.AddBackendMember(ctx, lbID, member)
		if err != nil {
			log.Warn("failed adding backend", "ip", memberIP, "err", err)
		} else {
			log.Info("Added backend", "ip", memberIP)
		}
	}
}

// removeOpsUnusedMembers removes unused backend members for ops LB.
func removeOpsUnusedMembers(ctx context.Context, netMgr cpi.NetworkManager, lbID string, desired map[string]bool) {
	log := logger.Get()

	pools, err := netMgr.GetBackendPools(ctx, lbID)
	if err == nil {
		for _, p := range pools {
			for _, member := range p.Members {
				if !desired[member.IPAddress] {
					err := netMgr.RemoveBackendMember(ctx, lbID, member.IPAddress)
					if err != nil {
						log.Warn("failed remove backend", "ip", member.IPAddress, "err", err)
					} else {
						log.Info("Removed backend", "ip", member.IPAddress)
					}
				}
			}
		}
	}
}

func executeOpsLBSync(ctx context.Context, config *opsLBConfig, lbMgr cpi.LoadBalancerManager, netMgr cpi.NetworkManager) error {
	log := logger.Get()

	// Ensure LB
	loadBalancer, err := ensureLoadBalancerByName(ctx, lbMgr, config.name)
	if err != nil {
		return err
	}

	log.Info("Ensured ops LB", "id", loadBalancer.ID, "name", loadBalancer.Name, "port", loadBalancer.Port)

	// Try to sync from config first
	err = syncOpsLBFromConfig(ctx, config, netMgr, loadBalancer)
	if err != nil {
		return err
	}

	// Check if we used config spec (if so, we're done)
	if spec, ok := config.cfg.LBs[config.name]; ok && len(spec.Targets) > 0 {
		return nil
	}

	// Fallback: derive from reserved IPs
	desired := buildOpsLBDesiredIPs(config)
	existing := getExistingBackendMembers(ctx, netMgr, loadBalancer.ID)

	// Add missing members
	addOpsBackendMembers(ctx, netMgr, loadBalancer.ID, desired, existing, config.port)

	// Remove unused members
	if config.removeUnused {
		removeOpsUnusedMembers(ctx, netMgr, loadBalancer.ID, desired)
	}

	return nil
}

// LBs: ops.
func newLBOpsCmd() *cobra.Command {
	var (
		name            string
		port            int
		protocol        string
		includeDoomsday bool
		removeUnused    bool
		dryRun          bool
		output          string
	)

	//nolint:exhaustruct // Using zero values for optional fields
	cmd := &cobra.Command{
		Use:   "ops",
		Short: "Manage ops-https load balancer",
		RunE:  makeLBOpsRunFunc(&name, &port, &protocol, &includeDoomsday, &removeUnused, &dryRun, &output),
	}

	addLBOpsFlags(cmd, &name, &port, &protocol, &includeDoomsday, &removeUnused, &dryRun, &output)

	return cmd
}

func makeLBOpsRunFunc(name *string, port *int, protocol *string, includeDoomsday, removeUnused, dryRun *bool, output *string) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		config, err := prepareLBOpsConfig(*name, *port, *protocol, *includeDoomsday, *removeUnused, *dryRun, *output)
		if err != nil {
			return err
		}

		ctx := context.Background()

		return executeLBOpsCommand(ctx, config)
	}
}

func prepareLBOpsConfig(name string, port int, protocol string, includeDoomsday, removeUnused, dryRun bool, output string) (*opsLBConfig, error) {
	configFile := viper.GetString("config")

	blocName := viper.GetString("bloc")
	if name == "" {
		name = blocName + "-ops-https"
	}

	cfg, err := config.LoadWithParams(configFile, blocName)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	return &opsLBConfig{
		name:            name,
		port:            port,
		protocol:        protocol,
		includeDoomsday: includeDoomsday,
		removeUnused:    removeUnused,
		dryRun:          dryRun,
		output:          output,
		blocName:        blocName,
		cfg:             cfg,
	}, nil
}

func executeLBOpsCommand(ctx context.Context, config *opsLBConfig) error {
	provider, err := cpi.GetProvider(config.cfg.Provider)
	if err != nil {
		return fmt.Errorf("get provider: %w", err)
	}

	err = provider.Initialize(ctx, config.cfg)
	if err != nil {
		return fmt.Errorf("init provider: %w", err)
	}

	defer func() { _ = provider.Cleanup(ctx) }()

	lbMgr := provider.LoadBalancer()
	if lbMgr == nil {
		return ErrProviderLacksLoadBalancerManager
	}

	netMgr := provider.Network()
	if netMgr == nil {
		return ErrProviderLacksNetworkManager
	}

	if config.dryRun {
		return renderOpsLBDryRunPlan(ctx, config, netMgr)
	}

	return executeOpsLBSync(ctx, config, lbMgr, netMgr)
}

func addLBOpsFlags(cmd *cobra.Command, name *string, port *int, protocol *string, includeDoomsday, removeUnused, dryRun *bool, output *string) {
	cmd.Flags().StringVar(name, "name", "", "override load balancer name (default <bloc>-ops-https)")
	cmd.Flags().IntVar(port, "port", OpsHTTPSPort, "ops HTTPS port")
	cmd.Flags().StringVar(protocol, "protocol", "https", "protocol (https|http|tcp)")
	cmd.Flags().BoolVar(includeDoomsday, "with-doomsday", false, "also add doomsday backend from ocfp-1")
	cmd.Flags().BoolVar(removeUnused, "remove-unused", false, "remove backends not in the reserved set")
	cmd.Flags().BoolVar(dryRun, "dry-run", false, "preview changes without making them")
	cmd.Flags().StringVar(output, "output", "table", "output format: table|json|yaml (dry-run)")
}

// routersLBConfig holds configuration for router load balancer management.
type routersLBConfig struct {
	namePrefix   string
	httpPort     int
	httpsPort    int
	createHTTP   bool
	createHTTPS  bool
	removeUnused bool
	dryRun       bool
	output       string
	blocName     string
	cfg          *config.Config
}

// addRouterLBPlan adds a dry-run plan section for a specific router load balancer.
// buildRouterPlanDesiredIPs builds the desired IPs for a router LB plan.
func buildRouterPlanDesiredIPs(config *routersLBConfig, lbName, job string) map[string]bool {
	desired := map[string]bool{}

	if spec, ok := config.cfg.LBs[lbName]; ok && len(spec.Targets) > 0 {
		for _, tgt := range spec.Targets {
			ipAddress := tgt
			if isToken(tgt) {
				r, err := ResolveTargetIP(config.blocName, tgt)
				if err == nil {
					ipAddress = r
				}
			}

			desired[ipAddress] = true
		}
	} else {
		for _, ip := range getStatePublicIPsByJob(config.blocName, job) {
			desired[ip] = true
		}
	}

	return desired
}

// addRouterLBCurrentMembersSection adds current members section to table.
func addRouterLBCurrentMembersSection(ctx context.Context, table *ui.Table, netMgr cpi.NetworkManager, lbName string) map[string]bool {
	existing := map[string]bool{}

	lb, err := netMgr.GetLoadBalancer(ctx, lbName)
	if err == nil && lb != nil {
		pools, err := netMgr.GetBackendPools(ctx, lb.ID)
		if err == nil && len(pools) > 0 {
			rows := make([][]string, 0, len(pools[0].Members))
			for _, m := range pools[0].Members {
				existing[m.IPAddress] = true
				rows = append(rows, []string{m.IPAddress, strconv.Itoa(m.Port)})
			}

			table.Sections = append(table.Sections, ui.Section{
				Title:   fmt.Sprintf("Current Members (%s)", lbName),
				Headers: []string{"IP", "PORT"},
				Rows:    rows,
			})
		}
	}

	return existing
}

// addRouterLBChangesSections adds add/remove sections to table.
func addRouterLBChangesSections(table *ui.Table, config *routersLBConfig, lbName string, desired, existing map[string]bool, port int) {
	// Add section
	addRows := make([][]string, 0, len(desired))
	for ip := range desired {
		if !existing[ip] {
			addRows = append(addRows, []string{ip, strconv.Itoa(port)})
		}
	}

	if len(addRows) > 0 {
		table.Sections = append(table.Sections, ui.Section{
			Title:   fmt.Sprintf("Add (%s)", lbName),
			Headers: []string{"IP", "PORT"},
			Rows:    addRows,
		})
	}

	// Remove section
	if config.removeUnused {
		remRows := make([][]string, 0, len(existing))
		for ip := range existing {
			if !desired[ip] {
				remRows = append(remRows, []string{ip})
			}
		}

		if len(remRows) > 0 {
			table.Sections = append(table.Sections, ui.Section{
				Title:   fmt.Sprintf("Remove (%s)", lbName),
				Headers: []string{"IP"},
				Rows:    remRows,
			})
		}
	}
}

func addRouterLBPlan(ctx context.Context, table *ui.Table, config *routersLBConfig, netMgr cpi.NetworkManager, lbName, proto string, port int, job string) {
	table.Sections = append(table.Sections, ui.Section{
		Title:   lbName,
		Headers: []string{"NAME", "TYPE", "PROTOCOL", "PORT"},
		Rows:    [][]string{{lbName, "external", proto, strconv.Itoa(port)}},
	})

	desired := buildRouterPlanDesiredIPs(config, lbName, job)
	existing := addRouterLBCurrentMembersSection(ctx, table, netMgr, lbName)
	addRouterLBChangesSections(table, config, lbName, desired, existing, port)
}

// renderRoutersLBDryRunPlan generates and renders the dry-run plan for router load balancers.
func renderRoutersLBDryRunPlan(ctx context.Context, config *routersLBConfig, netMgr cpi.NetworkManager) error {
	table := &ui.Table{
		Title:    "DRY RUN — Routers LB Plan",
		Summary:  "",
		Sections: nil,
	}

	if config.createHTTP {
		addRouterLBPlan(ctx, table, config, netMgr, config.namePrefix+"-80", "http", config.httpPort, "router")
	}

	if config.createHTTPS {
		addRouterLBPlan(ctx, table, config, netMgr, config.namePrefix+"-443", "https", config.httpsPort, "router")
	}

	output := config.output
	if output == "" {
		output = OutputTable
	}

	err := ui.Render(table, strings.ToLower(output))
	if err != nil {
		return fmt.Errorf("failed to render table: %w", err)
	}

	return nil
}

// ensureRouterLB ensures a router load balancer is created and synced.
func ensureRouterLB(ctx context.Context, config *routersLBConfig, lbMgr cpi.LoadBalancerManager, netMgr cpi.NetworkManager, name, protocol string, port int) error {
	log := logger.Get()

	loadBalancer, err := ensureLoadBalancerByName(ctx, lbMgr, name)
	if err != nil {
		return err
	}

	err = configureRouterLB(ctx, config, netMgr, loadBalancer, name, protocol, port)
	if err != nil {
		return err
	}

	log.Info("Ensured router LB", "name", name, "protocol", protocol)

	return nil
}

// configureRouterLB configures a router load balancer with the appropriate targets.
func configureRouterLB(ctx context.Context, config *routersLBConfig, netMgr cpi.NetworkManager, loadBalancer *cpi.LoadBalancer, name, protocol string, port int) error {
	if spec, ok := config.cfg.LBs[name]; ok && len(spec.Targets) > 0 {
		return configureFromSpec(ctx, config, netMgr, loadBalancer, spec, protocol, port)
	}

	return configureFromPublicIPs(ctx, config, netMgr, loadBalancer, port)
}

// configureFromSpec configures load balancer from specification.
func configureFromSpec(ctx context.Context, config *routersLBConfig, netMgr cpi.NetworkManager, loadBalancer *cpi.LoadBalancer, spec config.LBService, protocol string, port int) error {
	if spec.Protocol == "" {
		spec.Protocol = protocol
	}

	if spec.Port == 0 {
		spec.Port = port
	}

	return reconcileLBFromSpec(ctx, netMgr, loadBalancer, config.blocName, spec, config.removeUnused)
}

// configureFromPublicIPs configures load balancer using public IPs with job=router.
func configureFromPublicIPs(ctx context.Context, config *routersLBConfig, netMgr cpi.NetworkManager, loadBalancer *cpi.LoadBalancer, port int) error {
	desired := getStatePublicIPsByJob(config.blocName, "router")

	return reconcileLBIPs(ctx, netMgr, loadBalancer, desired, port, config.removeUnused)
}

// executeRoutersLBSync performs the actual router load balancer synchronization.
func executeRoutersLBSync(ctx context.Context, config *routersLBConfig, lbMgr cpi.LoadBalancerManager, netMgr cpi.NetworkManager) error {
	if config.createHTTP {
		name := config.namePrefix + "-80"

		err := ensureRouterLB(ctx, config, lbMgr, netMgr, name, "http", config.httpPort)
		if err != nil {
			return err
		}
	}

	if config.createHTTPS {
		name := config.namePrefix + "-443"

		err := ensureRouterLB(ctx, config, lbMgr, netMgr, name, "https", config.httpsPort)
		if err != nil {
			return err
		}
	}

	return nil
}

// LBs: routers (HTTP/HTTPS front-doors).
func newLBRoutersCmd() *cobra.Command {
	var (
		namePrefix   string
		httpPort     int
		httpsPort    int
		createHTTP   bool
		createHTTPS  bool
		removeUnused bool
		dryRun       bool
		output       string
	)

	//nolint:exhaustruct // Using zero values for optional fields
	cmd := &cobra.Command{
		Use:   "routers",
		Short: "Manage CF routers load balancers",
		RunE:  makeLBRoutersRunFunc(&namePrefix, &httpPort, &httpsPort, &createHTTP, &createHTTPS, &removeUnused, &dryRun, &output),
	}

	addLBRoutersFlags(cmd, &namePrefix, &httpPort, &httpsPort, &createHTTP, &createHTTPS, &removeUnused, &dryRun, &output)

	return cmd
}

func makeLBRoutersRunFunc(namePrefix *string, httpPort, httpsPort *int, createHTTP, createHTTPS, removeUnused, dryRun *bool, output *string) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		config, err := prepareLBRoutersConfig(*namePrefix, *httpPort, *httpsPort, *createHTTP, *createHTTPS, *removeUnused, *dryRun, *output)
		if err != nil {
			return err
		}

		ctx := context.Background()

		return executeLBRoutersCommand(ctx, config)
	}
}

func prepareLBRoutersConfig(namePrefix string, httpPort, httpsPort int, createHTTP, createHTTPS, removeUnused, dryRun bool, output string) (*routersLBConfig, error) {
	configFile := viper.GetString("config")

	blocName := viper.GetString("bloc")
	if namePrefix == "" {
		namePrefix = blocName + "-router"
	}

	cfg, err := config.LoadWithParams(configFile, blocName)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	return &routersLBConfig{
		namePrefix:   namePrefix,
		httpPort:     httpPort,
		httpsPort:    httpsPort,
		createHTTP:   createHTTP,
		createHTTPS:  createHTTPS,
		removeUnused: removeUnused,
		dryRun:       dryRun,
		output:       output,
		blocName:     blocName,
		cfg:          cfg,
	}, nil
}

func executeLBRoutersCommand(ctx context.Context, config *routersLBConfig) error {
	provider, err := cpi.GetProvider(config.cfg.Provider)
	if err != nil {
		return fmt.Errorf("get provider: %w", err)
	}

	err = provider.Initialize(ctx, config.cfg)
	if err != nil {
		return fmt.Errorf("init provider: %w", err)
	}

	defer func() { _ = provider.Cleanup(ctx) }()

	lbMgr := provider.LoadBalancer()
	if lbMgr == nil {
		return ErrProviderLacksLoadBalancerManager
	}

	netMgr := provider.Network()
	if netMgr == nil {
		return ErrProviderLacksNetworkManager
	}

	if config.dryRun {
		return renderRoutersLBDryRunPlan(ctx, config, netMgr)
	}

	return executeRoutersLBSync(ctx, config, lbMgr, netMgr)
}

func addLBRoutersFlags(cmd *cobra.Command, namePrefix *string, httpPort, httpsPort *int, createHTTP, createHTTPS, removeUnused, dryRun *bool, output *string) {
	cmd.Flags().StringVar(namePrefix, "name-prefix", "", "LB name prefix (default <bloc>-router)")
	cmd.Flags().IntVar(httpPort, "http-port", RouterHTTPPort, "HTTP port")
	cmd.Flags().IntVar(httpsPort, "https-port", RouterHTTPSPort, "HTTPS port")
	cmd.Flags().BoolVar(createHTTP, "http", true, "ensure HTTP router LB")
	cmd.Flags().BoolVar(createHTTPS, "https", true, "ensure HTTPS router LB")
	cmd.Flags().BoolVar(removeUnused, "remove-unused", false, "remove backends not listed in lbs config")
	cmd.Flags().BoolVar(dryRun, "dry-run", false, "preview changes without making them")
	cmd.Flags().StringVar(output, "output", OutputTable, "output format: table|json|yaml (dry-run)")
}

// tcpRoutersLBConfig holds configuration for TCP router load balancer management.
type tcpRoutersLBConfig struct {
	name         string
	port         int
	removeUnused bool
	dryRun       bool
	output       string
	blocName     string
	cfg          *config.Config
}

// renderTCPRoutersLBDryRunPlan generates and renders the dry-run plan for TCP router load balancer.
// buildTCPRouterPlanDesiredIPs builds desired IPs for TCP router LB.
func buildTCPRouterPlanDesiredIPs(config *tcpRoutersLBConfig) map[string]bool {
	desired := map[string]bool{}

	if spec, ok := config.cfg.LBs[config.name]; ok && len(spec.Targets) > 0 {
		for _, tgt := range spec.Targets {
			ipAddress := tgt
			if isToken(tgt) {
				r, err := ResolveTargetIP(config.blocName, tgt)
				if err == nil {
					ipAddress = r
				}
			}

			desired[ipAddress] = true
		}
	} else {
		for _, ip := range getStatePublicIPsByJob(config.blocName, "tcp-router") {
			desired[ip] = true
		}
	}

	return desired
}

// addTCPRouterCurrentMembersSection adds current members section to table.
func addTCPRouterCurrentMembersSection(ctx context.Context, table *ui.Table, netMgr cpi.NetworkManager, config *tcpRoutersLBConfig) map[string]bool {
	existing := map[string]bool{}

	lb, err := netMgr.GetLoadBalancer(ctx, config.name)
	if err == nil && lb != nil {
		pools, err := netMgr.GetBackendPools(ctx, lb.ID)
		if err == nil && len(pools) > 0 {
			rows := make([][]string, 0, len(pools[0].Members))
			for _, m := range pools[0].Members {
				existing[m.IPAddress] = true
				rows = append(rows, []string{m.IPAddress, strconv.Itoa(m.Port)})
			}

			table.Sections = append(table.Sections, ui.Section{
				Title:   "Current Members",
				Headers: []string{"IP", "PORT"},
				Rows:    rows,
			})
		}
	}

	return existing
}

// addTCPRouterChangesSections adds add/remove sections to table.
func addTCPRouterChangesSections(table *ui.Table, config *tcpRoutersLBConfig, desired, existing map[string]bool) {
	// Add section
	addRows := [][]string{}

	for ip := range desired {
		if !existing[ip] {
			addRows = append(addRows, []string{ip, strconv.Itoa(config.port)})
		}
	}

	if len(addRows) > 0 {
		table.Sections = append(table.Sections, ui.Section{
			Title:   "Add",
			Headers: []string{"IP", "PORT"},
			Rows:    addRows,
		})
	}

	// Remove section
	if config.removeUnused {
		remRows := [][]string{}

		for ip := range existing {
			if !desired[ip] {
				remRows = append(remRows, []string{ip})
			}
		}

		if len(remRows) > 0 {
			table.Sections = append(table.Sections, ui.Section{
				Title:   "Remove",
				Headers: []string{"IP"},
				Rows:    remRows,
			})
		}
	}
}

func renderTCPRoutersLBDryRunPlan(ctx context.Context, config *tcpRoutersLBConfig, netMgr cpi.NetworkManager) error {
	table := &ui.Table{
		Title:    "DRY RUN — TCP Routers LB Plan",
		Summary:  "",
		Sections: nil,
	}
	table.Sections = append(table.Sections, ui.Section{
		Title:   "Load Balancer",
		Headers: []string{"NAME", "TYPE", "PROTOCOL", "PORT"},
		Rows:    [][]string{{config.name, "external", "tcp", strconv.Itoa(config.port)}},
	})

	desired := buildTCPRouterPlanDesiredIPs(config)
	existing := addTCPRouterCurrentMembersSection(ctx, table, netMgr, config)
	addTCPRouterChangesSections(table, config, desired, existing)

	output := config.output
	if output == "" {
		output = OutputTable
	}

	err := ui.Render(table, strings.ToLower(output))
	if err != nil {
		return fmt.Errorf("failed to render TCP routers output: %w", err)
	}

	return nil
}

// executeTCPRoutersLBSync performs the actual TCP router load balancer synchronization.
func executeTCPRoutersLBSync(ctx context.Context, config *tcpRoutersLBConfig, lbMgr cpi.LoadBalancerManager, netMgr cpi.NetworkManager) error {
	return ensureAndSyncTCPLB(ctx, lbMgr, netMgr, config.name, config.port, config.blocName, config.cfg, config.removeUnused, "tcp-router", "Ensured TCP router LB")
}

// LBs: tcp-routers.
func newLBTCPRoutersCmd() *cobra.Command {
	var (
		name         string
		port         int
		removeUnused bool
		dryRun       bool
		output       string
	)

	//nolint:exhaustruct // Using zero values for optional fields
	cmd := &cobra.Command{
		Use:   "tcp-routers",
		Short: "Manage CF TCP routers load balancer",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			configFile := viper.GetString("config")
			blocName := viper.GetString("bloc")
			if name == "" {
				name = blocName + "-tcp-router"
			}

			cfg, lbMgr, netMgr, cleanup, err := setupProviderAndManagers(ctx, configFile, blocName)
			if err != nil {
				return err
			}
			defer cleanup()

			config := &tcpRoutersLBConfig{
				name:         name,
				port:         port,
				removeUnused: removeUnused,
				dryRun:       dryRun,
				output:       output,
				blocName:     blocName,
				cfg:          cfg,
			}

			if dryRun {
				return renderTCPRoutersLBDryRunPlan(ctx, config, netMgr)
			}

			return executeTCPRoutersLBSync(ctx, config, lbMgr, netMgr)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "LB name (default <bloc>-tcp-router)")
	cmd.Flags().IntVar(&port, "port", TCPRouterPort, "TCP router port (placeholder)")
	cmd.Flags().BoolVar(&removeUnused, "remove-unused", false, "remove backends not listed in lbs config")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview changes without making them")
	cmd.Flags().StringVar(&output, "output", OutputTable, "output format: table|json|yaml (dry-run)")

	return cmd
}

// cfSSHLBConfig holds configuration for CF SSH load balancer management.
type cfSSHLBConfig struct {
	name         string
	port         int
	removeUnused bool
	dryRun       bool
	output       string
	blocName     string
	cfg          *config.Config
}

// renderCFSSHLBDryRunPlan generates and renders the dry-run plan for CF SSH load balancer.
// buildCFSSHPlanDesiredIPs builds desired IPs for CF-SSH LB.
func buildCFSSHPlanDesiredIPs(config *cfSSHLBConfig) map[string]bool {
	desired := map[string]bool{}

	if spec, ok := config.cfg.LBs[config.name]; ok && len(spec.Targets) > 0 {
		for _, tgt := range spec.Targets {
			ipAddress := tgt
			if isToken(tgt) {
				r, err := ResolveTargetIP(config.blocName, tgt)
				if err == nil {
					ipAddress = r
				}
			}

			desired[ipAddress] = true
		}
	} else {
		for _, ip := range getStatePublicIPsByJob(config.blocName, "cf-ssh") {
			desired[ip] = true
		}
	}

	return desired
}

// addCFSSHCurrentMembersSection adds current members section to table.
func addCFSSHCurrentMembersSection(ctx context.Context, table *ui.Table, netMgr cpi.NetworkManager, config *cfSSHLBConfig) map[string]bool {
	existing := map[string]bool{}

	lb, err := netMgr.GetLoadBalancer(ctx, config.name)
	if err == nil && lb != nil {
		pools, err := netMgr.GetBackendPools(ctx, lb.ID)
		if err == nil && len(pools) > 0 {
			rows := [][]string{}

			for _, m := range pools[0].Members {
				existing[m.IPAddress] = true
				rows = append(rows, []string{m.IPAddress, strconv.Itoa(m.Port)})
			}

			table.Sections = append(table.Sections, ui.Section{
				Title:   "Current Members",
				Headers: []string{"IP", "PORT"},
				Rows:    rows,
			})
		}
	}

	return existing
}

// addCFSSHChangesSections adds add/remove sections to table.
func addCFSSHChangesSections(table *ui.Table, config *cfSSHLBConfig, desired, existing map[string]bool) {
	// Add section
	addRows := [][]string{}

	for ip := range desired {
		if !existing[ip] {
			addRows = append(addRows, []string{ip, strconv.Itoa(config.port)})
		}
	}

	if len(addRows) > 0 {
		table.Sections = append(table.Sections, ui.Section{
			Title:   "Add",
			Headers: []string{"IP", "PORT"},
			Rows:    addRows,
		})
	}

	// Remove section
	if config.removeUnused {
		remRows := [][]string{}

		for ip := range existing {
			if !desired[ip] {
				remRows = append(remRows, []string{ip})
			}
		}

		if len(remRows) > 0 {
			table.Sections = append(table.Sections, ui.Section{
				Title:   "Remove",
				Headers: []string{"IP"},
				Rows:    remRows,
			})
		}
	}
}

func renderCFSSHLBDryRunPlan(ctx context.Context, config *cfSSHLBConfig, netMgr cpi.NetworkManager) error {
	table := &ui.Table{
		Title:    "DRY RUN — CF-SSH LB Plan",
		Summary:  "",
		Sections: nil,
	}
	table.Sections = append(table.Sections, ui.Section{
		Title:   "Load Balancer",
		Headers: []string{"NAME", "TYPE", "PROTOCOL", "PORT"},
		Rows:    [][]string{{config.name, "external", "tcp", strconv.Itoa(config.port)}},
	})

	desired := buildCFSSHPlanDesiredIPs(config)
	existing := addCFSSHCurrentMembersSection(ctx, table, netMgr, config)
	addCFSSHChangesSections(table, config, desired, existing)

	output := config.output
	if output == "" {
		output = "table"
	}

	err := ui.Render(table, strings.ToLower(output))
	if err != nil {
		return fmt.Errorf("failed to render CF SSH output: %w", err)
	}

	return nil
}

// executeCFSSHLBSync performs the actual CF SSH load balancer synchronization.
func executeCFSSHLBSync(ctx context.Context, config *cfSSHLBConfig, lbMgr cpi.LoadBalancerManager, netMgr cpi.NetworkManager) error {
	return ensureAndSyncTCPLB(ctx, lbMgr, netMgr, config.name, config.port, config.blocName, config.cfg, config.removeUnused, "cf-ssh", "Ensured CF-SSH LB")
}

// LBs: cf-ssh.
func newLBCFSSHCmd() *cobra.Command {
	var (
		name         string
		port         int
		removeUnused bool
		dryRun       bool
		output       string
	)

	//nolint:exhaustruct // Using zero values for optional fields
	cmd := &cobra.Command{
		Use:   "cf-ssh",
		Short: "Manage CF SSH load balancer",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			configFile := viper.GetString("config")
			blocName := viper.GetString("bloc")
			if name == "" {
				name = blocName + "-cf-ssh"
			}

			cfg, lbMgr, netMgr, cleanup, err := setupProviderAndManagers(ctx, configFile, blocName)
			if err != nil {
				return err
			}
			defer cleanup()

			config := &cfSSHLBConfig{
				name:         name,
				port:         port,
				removeUnused: removeUnused,
				dryRun:       dryRun,
				output:       output,
				blocName:     blocName,
				cfg:          cfg,
			}

			if dryRun {
				return renderCFSSHLBDryRunPlan(ctx, config, netMgr)
			}

			return executeCFSSHLBSync(ctx, config, lbMgr, netMgr)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "LB name (default <bloc>-cf-ssh)")
	cmd.Flags().IntVar(&port, "port", CFSSHPort, "CF SSH port")
	cmd.Flags().BoolVar(&removeUnused, "remove-unused", false, "remove backends not listed in lbs config")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview changes without making them")
	cmd.Flags().StringVar(&output, "output", "table", "output format: table|json|yaml (dry-run)")

	return cmd
}

// ensureLoadBalancerByName tries to find LB by name; creates it if not found.
func ensureLoadBalancerByName(ctx context.Context, net cpi.LoadBalancerManager, name string) (*cpi.LoadBalancer, error) {
	// Try get by name, else list and match
	loadBalancer, err := net.GetLoadBalancer(ctx, name)
	if err == nil && loadBalancer != nil && loadBalancer.ID != "" {
		return loadBalancer, nil
	}

	lbs, err := net.ListLoadBalancers(ctx, nil)
	if err == nil {
		for _, existingLB := range lbs {
			if existingLB.Name == name {
				return existingLB, nil
			}
		}
	}
	// Create
	req := &cpi.CreateLoadBalancerRequest{
		Name:           name,
		Type:           "application",
		Scheme:         "internet-facing",
		NetworkID:      "",
		SubnetIDs:      nil,
		SecurityGroups: nil,
		Tags:           nil,
	}

	lb, err := net.CreateLoadBalancer(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to create load balancer: %w", err)
	}

	return lb, nil
}

// reconcileLBFromSpec ensures LB pool members match the given spec (adds missing, optionally removes unused).
func reconcileLBFromSpec(ctx context.Context, netMgr cpi.NetworkManager, loadBalancer *cpi.LoadBalancer, blocName string, spec config.LBService, removeUnused bool) error {
	log := logger.Get()

	desired := buildDesiredTargets(blocName, spec.Targets, log)
	existing := getExistingBackends(ctx, netMgr, loadBalancer.ID)

	addMissingBackends(ctx, netMgr, loadBalancer.ID, desired, existing, spec.Port, log)

	if removeUnused {
		removeUnusedBackends(ctx, netMgr, loadBalancer.ID, desired, log)
	}

	return nil
}

func buildDesiredTargets(blocName string, targets []string, log logger.Logger) map[string]bool {
	desired := make(map[string]bool)

	for _, t := range targets {
		targetIP := t
		if len(t) >= 9 && t[:9] == "reserved:" {
			resolvedIP, err := ResolveReservedIP(blocName, t)
			if err != nil {
				log.Warn("reserved unresolved", "token", t, "err", err)

				continue
			}

			targetIP = resolvedIP
		}

		desired[targetIP] = true
	}

	return desired
}

func getExistingBackends(ctx context.Context, netMgr cpi.NetworkManager, lbID string) map[string]bool {
	existing := make(map[string]bool)

	pools, err := netMgr.GetBackendPools(ctx, lbID)
	if err != nil {
		return existing
	}

	for _, p := range pools {
		for _, m := range p.Members {
			existing[m.IPAddress] = true
		}
	}

	return existing
}

func addMissingBackends(ctx context.Context, netMgr cpi.NetworkManager, lbID string, desired, existing map[string]bool, port int, log logger.Logger) {
	for ipAddress := range desired {
		if existing[ipAddress] {
			continue
		}

		member := &cpi.BackendMember{
			ID:         "",
			IPAddress:  ipAddress,
			Port:       port,
			TargetPort: DefaultMemberPort,
			Weight:     DefaultWeight,
			Status:     "",
		}

		err := netMgr.AddBackendMember(ctx, lbID, member)
		if err != nil {
			log.Warn("failed add backend", "ip", ipAddress, "err", err)
		} else {
			log.Info("added backend", "ip", ipAddress)
		}
	}
}

func removeUnusedBackends(ctx context.Context, netMgr cpi.NetworkManager, lbID string, desired map[string]bool, log logger.Logger) {
	pools, err := netMgr.GetBackendPools(ctx, lbID)
	if err != nil {
		return
	}

	for _, p := range pools {
		for _, member := range p.Members {
			if !desired[member.IPAddress] {
				err := netMgr.RemoveBackendMember(ctx, lbID, member.IPAddress)
				if err != nil {
					log.Warn("failed remove backend", "ip", member.IPAddress, "err", err)
				} else {
					log.Info("removed backend", "ip", member.IPAddress)
				}
			}
		}
	}
}

// reconcileLBIPs reconciles LB members to the provided list of IPs.
func reconcileLBIPs(ctx context.Context, netMgr cpi.NetworkManager, loadBalancer *cpi.LoadBalancer, ips []string, port int, removeUnused bool) error {
	log := logger.Get()

	desired := buildDesiredIPSet(ips)
	existing := getExistingBackends(ctx, netMgr, loadBalancer.ID)

	addMissingBackends(ctx, netMgr, loadBalancer.ID, desired, existing, port, log)

	if removeUnused {
		removeUnusedBackends(ctx, netMgr, loadBalancer.ID, desired, log)
	}

	return nil
}

func buildDesiredIPSet(ips []string) map[string]bool {
	desired := make(map[string]bool)
	for _, ip := range ips {
		desired[ip] = true
	}

	return desired
}

// ensureAndSyncTCPLB ensures a TCP LB exists and syncs members from config spec or fallback job.
func ensureAndSyncTCPLB(
	ctx context.Context,
	lbMgr cpi.LoadBalancerManager,
	netMgr cpi.NetworkManager,
	name string,
	port int,
	blocName string,
	cfg *config.Config,
	removeUnused bool,
	fallbackJob string,
	logMsg string,
) error {
	log := logger.Get()

	loadBalancer, err := ensureLoadBalancerByName(ctx, lbMgr, name)
	if err != nil {
		return err
	}

	err = configureTCPLB(ctx, netMgr, loadBalancer, name, port, blocName, cfg, removeUnused, fallbackJob)
	if err != nil {
		return err
	}

	log.Info(logMsg, "name", name, "port", port)

	return nil
}

// configureTCPLB configures a TCP load balancer with the appropriate targets.
func configureTCPLB(ctx context.Context, netMgr cpi.NetworkManager, loadBalancer *cpi.LoadBalancer, name string, port int, blocName string, cfg *config.Config, removeUnused bool, fallbackJob string) error {
	if spec, ok := cfg.LBs[name]; ok && len(spec.Targets) > 0 {
		return configureTCPFromSpec(ctx, netMgr, loadBalancer, blocName, spec, port, removeUnused)
	}

	return configureTCPFromPublicIPs(ctx, netMgr, loadBalancer, blocName, fallbackJob, port, removeUnused)
}

// configureTCPFromSpec configures TCP load balancer from specification.
func configureTCPFromSpec(ctx context.Context, netMgr cpi.NetworkManager, loadBalancer *cpi.LoadBalancer, blocName string, spec config.LBService, port int, removeUnused bool) error {
	if spec.Protocol == "" {
		spec.Protocol = ProtocolTCP
	}

	if spec.Port == 0 {
		spec.Port = port
	}

	return reconcileLBFromSpec(ctx, netMgr, loadBalancer, blocName, spec, removeUnused)
}

// configureTCPFromPublicIPs configures TCP load balancer using public IPs with the fallback job.
func configureTCPFromPublicIPs(ctx context.Context, netMgr cpi.NetworkManager, loadBalancer *cpi.LoadBalancer, blocName, fallbackJob string, port int, removeUnused bool) error {
	desired := getStatePublicIPsByJob(blocName, fallbackJob)

	return reconcileLBIPs(ctx, netMgr, loadBalancer, desired, port, removeUnused)
}

// getStatePublicIPsByJob returns a list of public IP addresses from state where job label matches.
func getStatePublicIPsByJob(blocName, job string) []string {
	stateManager, err := state.NewManager("")
	if err != nil {
		return nil
	}

	_, err = stateManager.Load(blocName)
	if err != nil {
		return nil
	}

	res, err := stateManager.ListResources("public_ip")
	if err != nil {
		return nil
	}

	out := []string{}

	for _, resource := range res {
		// Prefer labels in Tags, fallback to Properties.job
		if resource.Tags != nil && resource.Tags["job"] == job {
			if addr, ok := resource.Properties["address"].(string); ok && addr != "" {
				out = append(out, addr)
			}

			continue
		}

		if j, ok := resource.Properties["job"].(string); ok && j == job {
			if addr, ok := resource.Properties["address"].(string); ok && addr != "" {
				out = append(out, addr)
			}
		}
	}

	return out
}
