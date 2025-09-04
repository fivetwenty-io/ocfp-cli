package commands

import (
	"context"
	"fmt"
	"net"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// newLBSyncCmd syncs an LB's backend pool from bloc config under lbs:<name>
func newLBSyncCmd() *cobra.Command {
	var (
		name         string
		removeUnused bool
	)
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Sync a load balancer from bloc config (lbs:)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return fmt.Errorf("--name is required")
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
				return fmt.Errorf("provider lacks load balancer manager")
			}
			netMgr := provider.Network()
			if netMgr == nil {
				return fmt.Errorf("provider lacks network manager")
			}

			if spec.Port <= 0 {
				return fmt.Errorf("lbs.%s.port must be > 0", name)
			}
			if spec.Protocol == "" {
				spec.Protocol = "tcp"
			}

			lb, err := ensureLoadBalancerByName(ctx, lbMgr, name, "external", spec.Protocol, spec.Port, 0)
			if err != nil {
				return err
			}

			// Desired IPs
			desired := map[string]bool{}
			for _, t := range spec.Targets {
				ip := t
				if isToken(t) {
					r, err := resolveTargetIP(blocName, t)
					if err != nil {
						log.Warn("token unresolved", "token", t, "err", err)
						continue
					}
					ip = r
				}
				if net.ParseIP(ip) == nil {
					log.Warn("invalid target ip", "value", ip)
					continue
				}
				desired[ip] = true
			}

			// Existing
			existing := map[string]bool{}
			if pools, err := netMgr.GetBackendPools(ctx, lb.ID); err == nil {
				for _, p := range pools {
					for _, m := range p.Members {
						existing[m.IPAddress] = true
					}
				}
			}

			// Add missing
			for ip := range desired {
				if existing[ip] {
					continue
				}
				member := &cpi.BackendMember{IPAddress: ip, Port: spec.Port, TargetPort: 0, Weight: 1}
				if err := netMgr.AddBackendMember(ctx, lb.ID, member); err != nil {
					log.Warn("failed add backend", "ip", ip, "err", err)
				} else {
					log.Info("added backend", "ip", ip)
				}
			}

			// Remove unused
			if removeUnused {
				if pools, err := netMgr.GetBackendPools(ctx, lb.ID); err == nil {
					for _, p := range pools {
						for _, m := range p.Members {
							if !desired[m.IPAddress] {
								if err := netMgr.RemoveBackendMember(ctx, lb.ID, m.IPAddress); err != nil {
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
	return cmd
}
