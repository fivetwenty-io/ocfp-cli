package commands

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
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

	cmd := &cobra.Command{
		Use:   "ops",
		Short: "Manage ops-https load balancer",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			log := logger.Get()

			configFile := viper.GetString("config")
			blocName := viper.GetString("bloc")
			if name == "" {
				name = blocName + "-ops-https"
			}

			cfg, err := config.LoadWithParams(configFile, blocName)
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			provider, err := cpi.GetProvider(cfg.Provider)
			if err != nil {
				return fmt.Errorf("get provider: %w", err)
			}
			if err := provider.Initialize(ctx, cfg); err != nil {
				return fmt.Errorf("init provider: %w", err)
			}
			defer func() { _ = provider.Cleanup(ctx) }()

			lbMgr := provider.LoadBalancer()
			if lbMgr == nil {
				return errors.New("provider lacks load balancer manager")
			}
			netMgr := provider.Network()
			if netMgr == nil {
				return errors.New("provider lacks network manager")
			}

			if dryRun {
				t := &ui.Table{Title: "DRY RUN — Ops LB Plan"}
				t.Sections = append(t.Sections, ui.Section{Title: "Load Balancer", Headers: []string{"NAME", "TYPE", "PROTOCOL", "PORT"}, Rows: [][]string{{name, "external", protocol, strconv.Itoa(port)}}})
				desired := map[string]bool{}
				if spec, ok := cfg.LBs[name]; ok && len(spec.Targets) > 0 {
					for _, tgt := range spec.Targets {
						ip := tgt
						if isToken(tgt) {
							if r, err := resolveTargetIP(blocName, tgt); err == nil {
								ip = r
							}
						}
						desired[ip] = true
					}
				} else {
					backends := []struct{ key, index string }{{"vault_ip", "0"}, {"prometheus_ip", "0"}, {"shield_ip", "0"}}
					if includeDoomsday {
						backends = append(backends, struct{ key, index string }{"doomsday_ip", "1"})
					}
					for _, b := range backends {
						if ip, err := resolveReservedIP(blocName, fmt.Sprintf("reserved:%s:%s", b.key, b.index)); err == nil && ip != "" {
							desired[ip] = true
						}
					}
				}
				existing := map[string]bool{}
				if lb, err := netMgr.GetLoadBalancer(ctx, name); err == nil && lb != nil {
					if pools, err := netMgr.GetBackendPools(ctx, lb.ID); err == nil && len(pools) > 0 {
						rows := [][]string{}
						for _, m := range pools[0].Members {
							existing[m.IPAddress] = true
							rows = append(rows, []string{m.IPAddress, strconv.Itoa(m.Port)})
						}
						t.Sections = append(t.Sections, ui.Section{Title: "Current Members", Headers: []string{"IP", "PORT"}, Rows: rows})
					}
				}
				addRows := [][]string{}
				for ip := range desired {
					if !existing[ip] {
						addRows = append(addRows, []string{ip, strconv.Itoa(port)})
					}
				}
				if len(addRows) > 0 {
					t.Sections = append(t.Sections, ui.Section{Title: "Add", Headers: []string{"IP", "PORT"}, Rows: addRows})
				}
				if removeUnused {
					remRows := [][]string{}
					for ip := range existing {
						if !desired[ip] {
							remRows = append(remRows, []string{ip})
						}
					}
					if len(remRows) > 0 {
						t.Sections = append(t.Sections, ui.Section{Title: "Remove", Headers: []string{"IP"}, Rows: remRows})
					}
				}
				if output == "" {
					output = "table"
				}

				return ui.Render(t, strings.ToLower(output))
			}

			// Ensure LB
			loadBalancer, err := ensureLoadBalancerByName(ctx, lbMgr, name, "external", protocol, port, 0)
			if err != nil {
				return err
			}
			log.Info("Ensured ops LB", "id", loadBalancer.ID, "name", loadBalancer.Name, "port", loadBalancer.Port)

			// Add standard backends using reserved IPs
			backends := []struct {
				key   string
				index string
			}{
				{"vault_ip", "0"},
				{"prometheus_ip", "0"},
				{"shield_ip", "0"},
			}
			if includeDoomsday {
				backends = append(backends, struct{ key, index string }{"doomsday_ip", "1"})
			}

			// Default behavior: if lbs config present for this name, sync from config
			if spec, ok := cfg.LBs[name]; ok && len(spec.Targets) > 0 {
				if spec.Protocol == "" {
					spec.Protocol = protocol
				}
				if spec.Port == 0 {
					spec.Port = port
				}
				err := reconcileLBFromSpec(ctx, lbMgr, netMgr, loadBalancer, blocName, spec, removeUnused)
				if err != nil {
					return err
				}
			} else {
				// Fallback: derive from reserved IPs as before
				desired := map[string]bool{}
				for _, b := range backends {
					backendIP, err := resolveReservedIP(blocName, fmt.Sprintf("reserved:%s:%s", b.key, b.index))
					if err != nil {
						log.Warn("reserved not found", "key", b.key, "index", b.index, "err", err)

						continue
					}
					desired[backendIP] = true
				}
				existing := map[string]bool{}
				if pools, err := netMgr.GetBackendPools(ctx, loadBalancer.ID); err == nil {
					for _, p := range pools {
						for _, m := range p.Members {
							existing[m.IPAddress] = true
						}
					}
				}
				for memberIP := range desired {
					if existing[memberIP] {
						log.Info("Backend exists", "ip", memberIP)

						continue
					}
					member := &cpi.BackendMember{IPAddress: memberIP, Port: port, TargetPort: 0, Weight: 1}
					err := netMgr.AddBackendMember(ctx, loadBalancer.ID, member)
					if err != nil {
						log.Warn("failed adding backend", "ip", memberIP, "err", err)
					} else {
						log.Info("Added backend", "ip", memberIP)
					}
				}
				if removeUnused {
					if pools, err := netMgr.GetBackendPools(ctx, loadBalancer.ID); err == nil {
						for _, p := range pools {
							for _, m := range p.Members {
								if !desired[m.IPAddress] {
									err := netMgr.RemoveBackendMember(ctx, loadBalancer.ID, m.IPAddress)
									if err != nil {
										log.Warn("failed remove backend", "ip", m.IPAddress, "err", err)
									} else {
										log.Info("Removed backend", "ip", m.IPAddress)
									}
								}
							}
						}
					}
				}
			}

			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "override load balancer name (default <bloc>-ops-https)")
	cmd.Flags().IntVar(&port, "port", 443, "ops HTTPS port")
	cmd.Flags().StringVar(&protocol, "protocol", "https", "protocol (https|http|tcp)")
	cmd.Flags().BoolVar(&includeDoomsday, "with-doomsday", false, "also add doomsday backend from ocfp-1")
	cmd.Flags().BoolVar(&removeUnused, "remove-unused", false, "remove backends not in the reserved set")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview changes without making them")
	cmd.Flags().StringVar(&output, "output", "table", "output format: table|json|yaml (dry-run)")

	return cmd
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

	cmd := &cobra.Command{
		Use:   "routers",
		Short: "Manage CF routers load balancers",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			log := logger.Get()

			configFile := viper.GetString("config")
			blocName := viper.GetString("bloc")
			if namePrefix == "" {
				namePrefix = blocName + "-router"
			}

			cfg, err := config.LoadWithParams(configFile, blocName)
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			provider, err := cpi.GetProvider(cfg.Provider)
			if err != nil {
				return fmt.Errorf("get provider: %w", err)
			}
			if err := provider.Initialize(ctx, cfg); err != nil {
				return fmt.Errorf("init provider: %w", err)
			}
			defer func() { _ = provider.Cleanup(ctx) }()

			lbMgr := provider.LoadBalancer()
			if lbMgr == nil {
				return errors.New("provider lacks load balancer manager")
			}
			netMgr := provider.Network()
			if netMgr == nil {
				return errors.New("provider lacks network manager")
			}

			if dryRun {
				t := &ui.Table{Title: "DRY RUN — Routers LB Plan"}
				addPlan := func(lbName, proto string, port int, job string) {
					t.Sections = append(t.Sections, ui.Section{Title: lbName, Headers: []string{"NAME", "TYPE", "PROTOCOL", "PORT"}, Rows: [][]string{{lbName, "external", proto, strconv.Itoa(port)}}})
					desired := map[string]bool{}
					if spec, ok := cfg.LBs[lbName]; ok && len(spec.Targets) > 0 {
						for _, tgt := range spec.Targets {
							ip := tgt
							if isToken(tgt) {
								if r, err := resolveTargetIP(blocName, tgt); err == nil {
									ip = r
								}
							}
							desired[ip] = true
						}
					} else {
						for _, ip := range getStatePublicIPsByJob(blocName, job) {
							desired[ip] = true
						}
					}
					existing := map[string]bool{}
					if lb, err := netMgr.GetLoadBalancer(ctx, lbName); err == nil && lb != nil {
						if pools, err := netMgr.GetBackendPools(ctx, lb.ID); err == nil && len(pools) > 0 {
							rows := [][]string{}
							for _, m := range pools[0].Members {
								existing[m.IPAddress] = true
								rows = append(rows, []string{m.IPAddress, strconv.Itoa(m.Port)})
							}
							t.Sections = append(t.Sections, ui.Section{Title: fmt.Sprintf("Current Members (%s)", lbName), Headers: []string{"IP", "PORT"}, Rows: rows})
						}
					}
					addRows := [][]string{}
					for ip := range desired {
						if !existing[ip] {
							addRows = append(addRows, []string{ip, strconv.Itoa(port)})
						}
					}
					if len(addRows) > 0 {
						t.Sections = append(t.Sections, ui.Section{Title: fmt.Sprintf("Add (%s)", lbName), Headers: []string{"IP", "PORT"}, Rows: addRows})
					}
					if removeUnused {
						remRows := [][]string{}
						for ip := range existing {
							if !desired[ip] {
								remRows = append(remRows, []string{ip})
							}
						}
						if len(remRows) > 0 {
							t.Sections = append(t.Sections, ui.Section{Title: fmt.Sprintf("Remove (%s)", lbName), Headers: []string{"IP"}, Rows: remRows})
						}
					}
				}
				if createHTTP {
					addPlan(namePrefix+"-80", "http", httpPort, "router")
				}
				if createHTTPS {
					addPlan(namePrefix+"-443", "https", httpsPort, "router")
				}
				if output == "" {
					output = "table"
				}

				return ui.Render(t, strings.ToLower(output))
			}

			if createHTTP {
				name := namePrefix + "-80"
				loadBalancer, err := ensureLoadBalancerByName(ctx, lbMgr, name, "external", "http", httpPort, 0)
				if err != nil {
					return err
				}
				if spec, ok := cfg.LBs[name]; ok && len(spec.Targets) > 0 {
					if spec.Protocol == "" {
						spec.Protocol = "http"
					}
					if spec.Port == 0 {
						spec.Port = httpPort
					}
					err := reconcileLBFromSpec(ctx, lbMgr, netMgr, loadBalancer, blocName, spec, removeUnused)
					if err != nil {
						return err
					}
				} else {
					// Fallback: use public IPs with job=router
					desired := getStatePublicIPsByJob(blocName, "router")
					err := reconcileLBIPs(ctx, lbMgr, netMgr, loadBalancer, desired, httpPort, removeUnused)
					if err != nil {
						return err
					}
				}
				log.Info("Ensured router HTTP LB", "name", name)
			}
			if createHTTPS {
				name := namePrefix + "-443"
				loadBalancer, err := ensureLoadBalancerByName(ctx, lbMgr, name, "external", "https", httpsPort, 0)
				if err != nil {
					return err
				}
				if spec, ok := cfg.LBs[name]; ok && len(spec.Targets) > 0 {
					if spec.Protocol == "" {
						spec.Protocol = "https"
					}
					if spec.Port == 0 {
						spec.Port = httpsPort
					}
					err := reconcileLBFromSpec(ctx, lbMgr, netMgr, loadBalancer, blocName, spec, removeUnused)
					if err != nil {
						return err
					}
				} else {
					desired := getStatePublicIPsByJob(blocName, "router")
					err := reconcileLBIPs(ctx, lbMgr, netMgr, loadBalancer, desired, httpsPort, removeUnused)
					if err != nil {
						return err
					}
				}
				log.Info("Ensured router HTTPS LB", "name", name)
			}

			return nil
		},
	}
	cmd.Flags().StringVar(&namePrefix, "name-prefix", "", "LB name prefix (default <bloc>-router)")
	cmd.Flags().IntVar(&httpPort, "http-port", 80, "HTTP port")
	cmd.Flags().IntVar(&httpsPort, "https-port", 443, "HTTPS port")
	cmd.Flags().BoolVar(&createHTTP, "http", true, "ensure HTTP router LB")
	cmd.Flags().BoolVar(&createHTTPS, "https", true, "ensure HTTPS router LB")
	cmd.Flags().BoolVar(&removeUnused, "remove-unused", false, "remove backends not listed in lbs config")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview changes without making them")
	cmd.Flags().StringVar(&output, "output", "table", "output format: table|json|yaml (dry-run)")

	return cmd
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

	cmd := &cobra.Command{
		Use:   "tcp-routers",
		Short: "Manage CF TCP routers load balancer",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			log := logger.Get()
			configFile := viper.GetString("config")
			blocName := viper.GetString("bloc")
			if name == "" {
				name = blocName + "-tcp-router"
			}

			cfg, err := config.LoadWithParams(configFile, blocName)
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			provider, err := cpi.GetProvider(cfg.Provider)
			if err != nil {
				return fmt.Errorf("get provider: %w", err)
			}
			if err := provider.Initialize(ctx, cfg); err != nil {
				return fmt.Errorf("init provider: %w", err)
			}
			defer func() { _ = provider.Cleanup(ctx) }()

			lbMgr := provider.LoadBalancer()
			if lbMgr == nil {
				return errors.New("provider lacks load balancer manager")
			}
			netMgr := provider.Network()
			if netMgr == nil {
				return errors.New("provider lacks network manager")
			}
			if dryRun {
				t := &ui.Table{Title: "DRY RUN — TCP Routers LB Plan"}
				t.Sections = append(t.Sections, ui.Section{Title: "Load Balancer", Headers: []string{"NAME", "TYPE", "PROTOCOL", "PORT"}, Rows: [][]string{{name, "external", "tcp", strconv.Itoa(port)}}})
				desired := map[string]bool{}
				if spec, ok := cfg.LBs[name]; ok && len(spec.Targets) > 0 {
					for _, tgt := range spec.Targets {
						ip := tgt
						if isToken(tgt) {
							if r, err := resolveTargetIP(blocName, tgt); err == nil {
								ip = r
							}
						}
						desired[ip] = true
					}
				} else {
					for _, ip := range getStatePublicIPsByJob(blocName, "tcp-router") {
						desired[ip] = true
					}
				}
				existing := map[string]bool{}
				if lb, err := netMgr.GetLoadBalancer(ctx, name); err == nil && lb != nil {
					if pools, err := netMgr.GetBackendPools(ctx, lb.ID); err == nil && len(pools) > 0 {
						rows := [][]string{}
						for _, m := range pools[0].Members {
							existing[m.IPAddress] = true
							rows = append(rows, []string{m.IPAddress, strconv.Itoa(m.Port)})
						}
						t.Sections = append(t.Sections, ui.Section{Title: "Current Members", Headers: []string{"IP", "PORT"}, Rows: rows})
					}
				}
				addRows := [][]string{}
				for ip := range desired {
					if !existing[ip] {
						addRows = append(addRows, []string{ip, strconv.Itoa(port)})
					}
				}
				if len(addRows) > 0 {
					t.Sections = append(t.Sections, ui.Section{Title: "Add", Headers: []string{"IP", "PORT"}, Rows: addRows})
				}
				if removeUnused {
					remRows := [][]string{}
					for ip := range existing {
						if !desired[ip] {
							remRows = append(remRows, []string{ip})
						}
					}
					if len(remRows) > 0 {
						t.Sections = append(t.Sections, ui.Section{Title: "Remove", Headers: []string{"IP"}, Rows: remRows})
					}
				}
				if output == "" {
					output = "table"
				}

				return ui.Render(t, strings.ToLower(output))
			}
			loadBalancer, err := ensureLoadBalancerByName(ctx, lbMgr, name, "external", "tcp", port, 0)
			if err != nil {
				return err
			}
			if spec, ok := cfg.LBs[name]; ok && len(spec.Targets) > 0 {
				if spec.Protocol == "" {
					spec.Protocol = "tcp"
				}
				if spec.Port == 0 {
					spec.Port = port
				}
				err := reconcileLBFromSpec(ctx, lbMgr, netMgr, loadBalancer, blocName, spec, removeUnused)
				if err != nil {
					return err
				}
			} else {
				desired := getStatePublicIPsByJob(blocName, "tcp-router")
				err := reconcileLBIPs(ctx, lbMgr, netMgr, loadBalancer, desired, port, removeUnused)
				if err != nil {
					return err
				}
			}
			log.Info("Ensured TCP router LB", "name", name, "port", port)

			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "LB name (default <bloc>-tcp-router)")
	cmd.Flags().IntVar(&port, "port", 1024, "TCP router port (placeholder)")
	cmd.Flags().BoolVar(&removeUnused, "remove-unused", false, "remove backends not listed in lbs config")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview changes without making them")
	cmd.Flags().StringVar(&output, "output", "table", "output format: table|json|yaml (dry-run)")

	return cmd
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

	cmd := &cobra.Command{
		Use:   "cf-ssh",
		Short: "Manage CF SSH load balancer",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			log := logger.Get()
			configFile := viper.GetString("config")
			blocName := viper.GetString("bloc")
			if name == "" {
				name = blocName + "-cf-ssh"
			}

			cfg, err := config.LoadWithParams(configFile, blocName)
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			provider, err := cpi.GetProvider(cfg.Provider)
			if err != nil {
				return fmt.Errorf("get provider: %w", err)
			}
			if err := provider.Initialize(ctx, cfg); err != nil {
				return fmt.Errorf("init provider: %w", err)
			}
			defer func() { _ = provider.Cleanup(ctx) }()

			lbMgr := provider.LoadBalancer()
			if lbMgr == nil {
				return errors.New("provider lacks load balancer manager")
			}
			netMgr := provider.Network()
			if netMgr == nil {
				return errors.New("provider lacks network manager")
			}
			if dryRun {
				t := &ui.Table{Title: "DRY RUN — CF-SSH LB Plan"}
				t.Sections = append(t.Sections, ui.Section{Title: "Load Balancer", Headers: []string{"NAME", "TYPE", "PROTOCOL", "PORT"}, Rows: [][]string{{name, "external", "tcp", strconv.Itoa(port)}}})
				desired := map[string]bool{}
				if spec, ok := cfg.LBs[name]; ok && len(spec.Targets) > 0 {
					for _, tgt := range spec.Targets {
						ip := tgt
						if isToken(tgt) {
							if r, err := resolveTargetIP(blocName, tgt); err == nil {
								ip = r
							}
						}
						desired[ip] = true
					}
				} else {
					for _, ip := range getStatePublicIPsByJob(blocName, "cf-ssh") {
						desired[ip] = true
					}
				}
				existing := map[string]bool{}
				if lb, err := netMgr.GetLoadBalancer(ctx, name); err == nil && lb != nil {
					if pools, err := netMgr.GetBackendPools(ctx, lb.ID); err == nil && len(pools) > 0 {
						rows := [][]string{}
						for _, m := range pools[0].Members {
							existing[m.IPAddress] = true
							rows = append(rows, []string{m.IPAddress, strconv.Itoa(m.Port)})
						}
						t.Sections = append(t.Sections, ui.Section{Title: "Current Members", Headers: []string{"IP", "PORT"}, Rows: rows})
					}
				}
				addRows := [][]string{}
				for ip := range desired {
					if !existing[ip] {
						addRows = append(addRows, []string{ip, strconv.Itoa(port)})
					}
				}
				if len(addRows) > 0 {
					t.Sections = append(t.Sections, ui.Section{Title: "Add", Headers: []string{"IP", "PORT"}, Rows: addRows})
				}
				if removeUnused {
					remRows := [][]string{}
					for ip := range existing {
						if !desired[ip] {
							remRows = append(remRows, []string{ip})
						}
					}
					if len(remRows) > 0 {
						t.Sections = append(t.Sections, ui.Section{Title: "Remove", Headers: []string{"IP"}, Rows: remRows})
					}
				}
				if output == "" {
					output = "table"
				}

				return ui.Render(t, strings.ToLower(output))
			}
			loadBalancer, err := ensureLoadBalancerByName(ctx, lbMgr, name, "external", "tcp", port, 0)
			if err != nil {
				return err
			}
			if spec, ok := cfg.LBs[name]; ok && len(spec.Targets) > 0 {
				if spec.Protocol == "" {
					spec.Protocol = "tcp"
				}
				if spec.Port == 0 {
					spec.Port = port
				}
				err := reconcileLBFromSpec(ctx, lbMgr, netMgr, loadBalancer, blocName, spec, removeUnused)
				if err != nil {
					return err
				}
			} else {
				desired := getStatePublicIPsByJob(blocName, "cf-ssh")
				err := reconcileLBIPs(ctx, lbMgr, netMgr, loadBalancer, desired, port, removeUnused)
				if err != nil {
					return err
				}
			}
			log.Info("Ensured CF-SSH LB", "name", name, "port", port)

			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "LB name (default <bloc>-cf-ssh)")
	cmd.Flags().IntVar(&port, "port", 2222, "CF SSH port")
	cmd.Flags().BoolVar(&removeUnused, "remove-unused", false, "remove backends not listed in lbs config")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview changes without making them")
	cmd.Flags().StringVar(&output, "output", "table", "output format: table|json|yaml (dry-run)")

	return cmd
}

// ensureLoadBalancerByName tries to find LB by name; creates it if not found.
func ensureLoadBalancerByName(ctx context.Context, net cpi.LoadBalancerManager, name, lbType, protocol string, port, targetPort int) (*cpi.LoadBalancer, error) {
	// Try get by name, else list and match
	if loadBalancer, err := net.GetLoadBalancer(ctx, name); err == nil && loadBalancer != nil && loadBalancer.ID != "" {
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
		Name: name,
		Type: lbType,
	}

	return net.CreateLoadBalancer(ctx, req)
}

// reconcileLBFromSpec ensures LB pool members match the given spec (adds missing, optionally removes unused).
func reconcileLBFromSpec(ctx context.Context, lbMgr cpi.LoadBalancerManager, netMgr cpi.NetworkManager, loadBalancer *cpi.LoadBalancer, blocName string, spec config.LBService, removeUnused bool) error {
	log := logger.Get()
	desired := map[string]bool{}

	for _, t := range spec.Targets {
		targetIP := t
		if len(t) >= 9 && t[:9] == "reserved:" {
			resolvedIP, err := resolveReservedIP(blocName, t)
			if err != nil {
				log.Warn("reserved unresolved", "token", t, "err", err)

				continue
			}

			targetIP = resolvedIP
		}

		desired[targetIP] = true
	}

	existing := map[string]bool{}

	if pools, err := netMgr.GetBackendPools(ctx, loadBalancer.ID); err == nil {
		for _, p := range pools {
			for _, m := range p.Members {
				existing[m.IPAddress] = true
			}
		}
	}

	for ipAddress := range desired {
		if existing[ipAddress] {
			continue
		}

		member := &cpi.BackendMember{IPAddress: ipAddress, Port: spec.Port, TargetPort: 0, Weight: 1}
		err := netMgr.AddBackendMember(ctx, loadBalancer.ID, member)
		if err != nil {
			log.Warn("failed add backend", "ip", ipAddress, "err", err)
		} else {
			log.Info("added backend", "ip", ipAddress)
		}
	}

	if removeUnused {
		if pools, err := netMgr.GetBackendPools(ctx, loadBalancer.ID); err == nil {
			for _, p := range pools {
				for _, m := range p.Members {
					if !desired[m.IPAddress] {
						err := netMgr.RemoveBackendMember(ctx, loadBalancer.ID, m.IPAddress)
						if err != nil {
							log.Warn("failed remove backend", "ip", m.IPAddress, "err", err)
						} else {
							log.Info("removed backend", "ip", m.IPAddress)
						}
					}
				}
			}
		}
	}

	return nil
}

// reconcileLBIPs reconciles LB members to the provided list of IPs.
func reconcileLBIPs(ctx context.Context, lbMgr cpi.LoadBalancerManager, netMgr cpi.NetworkManager, loadBalancer *cpi.LoadBalancer, ips []string, port int, removeUnused bool) error {
	log := logger.Get()

	desired := map[string]bool{}
	for _, ipAddress := range ips {
		desired[ipAddress] = true
	}

	existing := map[string]bool{}

	if pools, err := netMgr.GetBackendPools(ctx, loadBalancer.ID); err == nil {
		for _, p := range pools {
			for _, m := range p.Members {
				existing[m.IPAddress] = true
			}
		}
	}

	for ipAddress := range desired {
		if existing[ipAddress] {
			continue
		}

		member := &cpi.BackendMember{IPAddress: ipAddress, Port: port, TargetPort: 0, Weight: 1}
		err := netMgr.AddBackendMember(ctx, loadBalancer.ID, member)
		if err != nil {
			log.Warn("failed add backend", "ip", ipAddress, "err", err)
		} else {
			log.Info("added backend", "ip", ipAddress)
		}
	}

	if removeUnused {
		if pools, err := netMgr.GetBackendPools(ctx, loadBalancer.ID); err == nil {
			for _, p := range pools {
				for _, m := range p.Members {
					if !desired[m.IPAddress] {
						err := netMgr.RemoveBackendMember(ctx, loadBalancer.ID, m.IPAddress)
						if err != nil {
							log.Warn("failed remove backend", "ip", m.IPAddress, "err", err)
						} else {
							log.Info("removed backend", "ip", m.IPAddress)
						}
					}
				}
			}
		}
	}

	return nil
}

// getStatePublicIPsByJob returns a list of public IP addresses from state where job label matches.
func getStatePublicIPsByJob(blocName, job string) []string {
	stateManager, err := state.NewManager(filepath.Join(""))
	if err != nil {
		return nil
	}

	if _, err := stateManager.Load(blocName); err != nil {
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
