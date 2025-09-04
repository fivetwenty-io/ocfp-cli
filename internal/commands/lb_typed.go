package commands

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/state"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// LBs: ops
func newLBOpsCmd() *cobra.Command {
	var (
		name            string
		port            int
		protocol        string
		includeDoomsday bool
		removeUnused    bool
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
				name = fmt.Sprintf("%s-ops-https", blocName)
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
				return fmt.Errorf("provider lacks load balancer manager")
			}
			netMgr := provider.Network()
			if netMgr == nil {
				return fmt.Errorf("provider lacks network manager")
			}

			// Ensure LB
			lb, err := ensureLoadBalancerByName(ctx, lbMgr, name, "external", protocol, port, 0)
			if err != nil {
				return err
			}
			log.Info("Ensured ops LB", "id", lb.ID, "name", lb.Name, "port", lb.Port)

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
				if err := reconcileLBFromSpec(ctx, lbMgr, netMgr, lb, blocName, spec, removeUnused); err != nil {
					return err
				}
			} else {
				// Fallback: derive from reserved IPs as before
				desired := map[string]bool{}
				for _, b := range backends {
					ip, err := resolveReservedIP(blocName, fmt.Sprintf("reserved:%s:%s", b.key, b.index))
					if err != nil {
						log.Warn("reserved not found", "key", b.key, "index", b.index, "err", err)
						continue
					}
					desired[ip] = true
				}
				existing := map[string]bool{}
				if pools, err := netMgr.GetBackendPools(ctx, lb.ID); err == nil {
					for _, p := range pools {
						for _, m := range p.Members {
							existing[m.IPAddress] = true
						}
					}
				}
				for ip := range desired {
					if existing[ip] {
						log.Info("Backend exists", "ip", ip)
						continue
					}
					member := &cpi.BackendMember{IPAddress: ip, Port: port, TargetPort: 0, Weight: 1}
					if err := netMgr.AddBackendMember(ctx, lb.ID, member); err != nil {
						log.Warn("failed adding backend", "ip", ip, "err", err)
					} else {
						log.Info("Added backend", "ip", ip)
					}
				}
				if removeUnused {
					if pools, err := netMgr.GetBackendPools(ctx, lb.ID); err == nil {
						for _, p := range pools {
							for _, m := range p.Members {
								if !desired[m.IPAddress] {
									if err := netMgr.RemoveBackendMember(ctx, lb.ID, m.IPAddress); err != nil {
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
	return cmd
}

// LBs: routers (HTTP/HTTPS front-doors)
func newLBRoutersCmd() *cobra.Command {
	var (
		namePrefix   string
		httpPort     int
		httpsPort    int
		createHTTP   bool
		createHTTPS  bool
		removeUnused bool
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
				namePrefix = fmt.Sprintf("%s-router", blocName)
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
				return fmt.Errorf("provider lacks load balancer manager")
			}
			netMgr := provider.Network()
			if netMgr == nil {
				return fmt.Errorf("provider lacks network manager")
			}

			if createHTTP {
				name := fmt.Sprintf("%s-80", namePrefix)
				lb, err := ensureLoadBalancerByName(ctx, lbMgr, name, "external", "http", httpPort, 0)
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
					if err := reconcileLBFromSpec(ctx, lbMgr, netMgr, lb, blocName, spec, removeUnused); err != nil {
						return err
					}
				} else {
					// Fallback: use public IPs with job=router
					desired := getStatePublicIPsByJob(blocName, "router")
					if err := reconcileLBIPs(ctx, lbMgr, netMgr, lb, desired, httpPort, removeUnused); err != nil {
						return err
					}
				}
				log.Info("Ensured router HTTP LB", "name", name)
			}
			if createHTTPS {
				name := fmt.Sprintf("%s-443", namePrefix)
				lb, err := ensureLoadBalancerByName(ctx, lbMgr, name, "external", "https", httpsPort, 0)
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
					if err := reconcileLBFromSpec(ctx, lbMgr, netMgr, lb, blocName, spec, removeUnused); err != nil {
						return err
					}
				} else {
					desired := getStatePublicIPsByJob(blocName, "router")
					if err := reconcileLBIPs(ctx, lbMgr, netMgr, lb, desired, httpsPort, removeUnused); err != nil {
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
	return cmd
}

// LBs: tcp-routers
func newLBTCPRoutersCmd() *cobra.Command {
	var (
		name         string
		port         int
		removeUnused bool
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
				name = fmt.Sprintf("%s-tcp-router", blocName)
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
				return fmt.Errorf("provider lacks load balancer manager")
			}
			netMgr := provider.Network()
			if netMgr == nil {
				return fmt.Errorf("provider lacks network manager")
			}
			lb, err := ensureLoadBalancerByName(ctx, lbMgr, name, "external", "tcp", port, 0)
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
				if err := reconcileLBFromSpec(ctx, lbMgr, netMgr, lb, blocName, spec, removeUnused); err != nil {
					return err
				}
			} else {
				desired := getStatePublicIPsByJob(blocName, "tcp-router")
				if err := reconcileLBIPs(ctx, lbMgr, netMgr, lb, desired, port, removeUnused); err != nil {
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
	return cmd
}

// LBs: cf-ssh
func newLBCFSSHCmd() *cobra.Command {
	var (
		name         string
		port         int
		removeUnused bool
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
				name = fmt.Sprintf("%s-cf-ssh", blocName)
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
				return fmt.Errorf("provider lacks load balancer manager")
			}
			netMgr := provider.Network()
			if netMgr == nil {
				return fmt.Errorf("provider lacks network manager")
			}
			lb, err := ensureLoadBalancerByName(ctx, lbMgr, name, "external", "tcp", port, 0)
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
				if err := reconcileLBFromSpec(ctx, lbMgr, netMgr, lb, blocName, spec, removeUnused); err != nil {
					return err
				}
			} else {
				desired := getStatePublicIPsByJob(blocName, "cf-ssh")
				if err := reconcileLBIPs(ctx, lbMgr, netMgr, lb, desired, port, removeUnused); err != nil {
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
	return cmd
}

// ensureLoadBalancerByName tries to find LB by name; creates it if not found.
func ensureLoadBalancerByName(ctx context.Context, net cpi.LoadBalancerManager, name, lbType, protocol string, port, targetPort int) (*cpi.LoadBalancer, error) {
	// Try get by name, else list and match
	if lb, err := net.GetLoadBalancer(ctx, name); err == nil && lb != nil && lb.ID != "" {
		return lb, nil
	}
	lbs, err := net.ListLoadBalancers(ctx, nil)
	if err == nil {
		for _, lb := range lbs {
			if lb.Name == name {
				return lb, nil
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

// reconcileLBFromSpec ensures LB pool members match the given spec (adds missing, optionally removes unused)
func reconcileLBFromSpec(ctx context.Context, lbMgr cpi.LoadBalancerManager, netMgr cpi.NetworkManager, lb *cpi.LoadBalancer, blocName string, spec config.LBService, removeUnused bool) error {
	log := logger.Get()
	desired := map[string]bool{}
	for _, t := range spec.Targets {
		ip := t
		if len(t) >= 9 && t[:9] == "reserved:" {
			r, err := resolveReservedIP(blocName, t)
			if err != nil {
				log.Warn("reserved unresolved", "token", t, "err", err)
				continue
			}
			ip = r
		}
		desired[ip] = true
	}
	existing := map[string]bool{}
	if pools, err := netMgr.GetBackendPools(ctx, lb.ID); err == nil {
		for _, p := range pools {
			for _, m := range p.Members {
				existing[m.IPAddress] = true
			}
		}
	}
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
}

// reconcileLBIPs reconciles LB members to the provided list of IPs
func reconcileLBIPs(ctx context.Context, lbMgr cpi.LoadBalancerManager, netMgr cpi.NetworkManager, lb *cpi.LoadBalancer, ips []string, port int, removeUnused bool) error {
	log := logger.Get()
	desired := map[string]bool{}
	for _, ip := range ips {
		desired[ip] = true
	}
	existing := map[string]bool{}
	if pools, err := netMgr.GetBackendPools(ctx, lb.ID); err == nil {
		for _, p := range pools {
			for _, m := range p.Members {
				existing[m.IPAddress] = true
			}
		}
	}
	for ip := range desired {
		if existing[ip] {
			continue
		}
		member := &cpi.BackendMember{IPAddress: ip, Port: port, TargetPort: 0, Weight: 1}
		if err := netMgr.AddBackendMember(ctx, lb.ID, member); err != nil {
			log.Warn("failed add backend", "ip", ip, "err", err)
		} else {
			log.Info("added backend", "ip", ip)
		}
	}
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
}

// getStatePublicIPsByJob returns a list of public IP addresses from state where job label matches
func getStatePublicIPsByJob(blocName, job string) []string {
	sm, err := state.NewManager(filepath.Join(""))
	if err != nil {
		return nil
	}
	if _, err := sm.Load(blocName); err != nil {
		return nil
	}
	res, err := sm.ListResources("public_ip")
	if err != nil {
		return nil
	}
	out := []string{}
	for _, r := range res {
		// Prefer labels in Tags, fallback to Properties.job
		if r.Tags != nil && r.Tags["job"] == job {
			if addr, ok := r.Properties["address"].(string); ok && addr != "" {
				out = append(out, addr)
			}
			continue
		}
		if j, ok := r.Properties["job"].(string); ok && j == job {
			if addr, ok := r.Properties["address"].(string); ok && addr != "" {
				out = append(out, addr)
			}
		}
	}
	return out
}
