package commands

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/ui"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// newLBSyncCmd syncs an LB's backend pool from bloc config under lbs:<name>.
func newLBSyncCmd() *cobra.Command {
	var (
		name         string
		removeUnused bool
		dryRun       bool
		output       string
	)

	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Sync a load balancer from bloc config (lbs:)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return errors.New("--name is required")
			}
			ctx := context.Background()
			log := logger.Get()

			configFile := viper.GetString("config")
			blocName := viper.GetString("bloc")
			cfg, err := config.LoadWithParams(configFile, blocName)
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			spec, ok := cfg.LBs[name]
			if !ok {
				return fmt.Errorf("lbs entry '%s' not found in config", name)
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

			if spec.Port <= 0 {
				return fmt.Errorf("lbs.%s.port must be > 0", name)
			}
            if spec.Protocol == "" {
                spec.Protocol = ProtocolTCP
            }

			if dryRun {
				t := &ui.Table{Title: "DRY RUN — LB Sync Plan"}
				// LB config
				t.Sections = append(t.Sections, ui.Section{Title: "Load Balancer", Headers: []string{"NAME", "TYPE", "PROTOCOL", "PORT"}, Rows: [][]string{{name, "external", spec.Protocol, strconv.Itoa(spec.Port)}}})
				// Desired
				desired := map[string]bool{}
				for _, tgt := range spec.Targets {
					ip := tgt
					if isToken(tgt) {
						if r, err := resolveTargetIP(blocName, tgt); err == nil {
							ip = r
						}
					}
					if net.ParseIP(ip) == nil {
						continue
					}
					desired[ip] = true
				}
				// Existing
				existing := map[string]bool{}
				if lb, err := netMgr.GetLoadBalancer(ctx, name); err == nil && lb != nil {
					if pools, err := netMgr.GetBackendPools(ctx, lb.ID); err == nil && len(pools) > 0 {
						rows := make([][]string, 0, len(pools[0].Members))
						for _, m := range pools[0].Members {
							existing[m.IPAddress] = true
							rows = append(rows, []string{m.IPAddress, strconv.Itoa(m.Port)})
						}
						t.Sections = append(t.Sections, ui.Section{Title: "Current Members", Headers: []string{"IP", "PORT"}, Rows: rows})
					}
				}
				addRows := make([][]string, 0, len(desired))
				for ip := range desired {
					if !existing[ip] {
						addRows = append(addRows, []string{ip, strconv.Itoa(spec.Port)})
					}
				}
				if len(addRows) > 0 {
					t.Sections = append(t.Sections, ui.Section{Title: "Add", Headers: []string{"IP", "PORT"}, Rows: addRows})
				}
				if removeUnused {
					remRows := make([][]string, 0, len(existing))
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
                    output = OutputTable
                }

				return ui.Render(t, strings.ToLower(output))
			}

			loadBalancer, err := ensureLoadBalancerByName(ctx, lbMgr, name, "external", spec.Protocol, spec.Port, 0)
			if err != nil {
				return err
			}

			// Desired IPs
			desired := map[string]bool{}
			for _, t := range spec.Targets {
				targetIP := t
				if isToken(t) {
					r, err := resolveTargetIP(blocName, t)
					if err != nil {
						log.Warn("token unresolved", "token", t, "err", err)

						continue
					}
					targetIP = r
				}
				if net.ParseIP(targetIP) == nil {
					log.Warn("invalid target ip", "value", targetIP)

					continue
				}
				desired[targetIP] = true
			}

			// Existing
			existing := map[string]bool{}
			if pools, err := netMgr.GetBackendPools(ctx, loadBalancer.ID); err == nil {
				for _, p := range pools {
					for _, m := range p.Members {
						existing[m.IPAddress] = true
					}
				}
			}

			// Add missing
			for memberIP := range desired {
				if existing[memberIP] {
					continue
				}
				member := &cpi.BackendMember{IPAddress: memberIP, Port: spec.Port, TargetPort: 0, Weight: 1}
				err := netMgr.AddBackendMember(ctx, loadBalancer.ID, member)
				if err != nil {
					log.Warn("failed add backend", "ip", memberIP, "err", err)
				} else {
					log.Info("added backend", "ip", memberIP)
				}
			}

			// Remove unused
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
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Name of the LB (must exist under lbs: in config)")
	cmd.Flags().BoolVar(&removeUnused, "remove-unused", false, "Remove backends not present in config")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview changes without making them")
    cmd.Flags().StringVar(&output, "output", OutputTable, "output format: table|json|yaml (dry-run)")

	return cmd
}
