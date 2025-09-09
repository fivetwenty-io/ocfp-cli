package commands

import (
	"context"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/state"
	"github.com/spf13/viper"
)

// findBastionIP attempts to discover the bastion IP using multiple label strategies
// and a final name-based fallback for compatibility with Perl tooling.
func findBastionIP(ctx context.Context, provider cpi.Provider, blocName string) (string, error) {
	log := logger.WithOperation("findBastionIP")

	// Try local state cache first
	if ip, found := tryStateCache(blocName, log); found {
		return ip, nil
	}

	// Try label-based discovery strategies
	if ip, found := tryLabelBasedDiscovery(ctx, provider, blocName, log); found {
		return ip, nil
	}

	// Fall back to name-based discovery
	if ip, found := tryNameBasedDiscovery(ctx, provider, blocName, log); found {
		return ip, nil
	}

	return "", ErrNoBastionHostFound(blocName)
}

func tryStateCache(blocName string, log logger.Logger) (string, bool) {
	stateManager, err := state.NewManager("")
	if err != nil {
		return "", false
	}

	_, err = stateManager.Load(blocName)
	if err != nil {
		return "", false
	}

	v, err := stateManager.GetOutput("bastion_public_ip")
	if err != nil {
		return "", false
	}

	bastionIP, ok := v.(string)
	if !ok || bastionIP == "" {
		return "", false
	}

	logDiscoveryResult(log, "state-cache", "", "", bastionIP)

	return bastionIP, true
}

func tryLabelBasedDiscovery(ctx context.Context, provider cpi.Provider, blocName string, log logger.Logger) (string, bool) {
	labelKeys := []string{"component", "role", "job"}
	for _, key := range labelKeys {
		filters := map[string]string{
			"label.bloc":   blocName,
			"label." + key: RoleBastion,
		}

		instances, err := provider.Compute().ListInstances(ctx, filters)
		if err != nil || len(instances) == 0 {
			continue
		}

		// Check for direct public IP
		if ip := findDirectPublicIP(instances, key, log, blocName); ip != "" {
			return ip, true
		}

		// Check floating IPs
		if ip := findFloatingIPForInstances(ctx, provider, instances, key, log, blocName); ip != "" {
			return ip, true
		}
	}

	return "", false
}

func tryNameBasedDiscovery(ctx context.Context, provider cpi.Provider, blocName string, log logger.Logger) (string, bool) {
	instances, err := provider.Compute().ListInstances(ctx, map[string]string{
		"label.bloc": blocName,
	})
	if err != nil || len(instances) == 0 {
		return "", false
	}

	// Check for direct public IP on bastion-named instances
	for _, inst := range instances {
		if !isBastionInstance(inst.Name) {
			continue
		}

		if publicIP := firstNonEmpty(inst.FloatingIP, inst.PublicIP); publicIP != "" {
			logDiscoveryResult(log, "name", "", inst.Name, publicIP)
			cacheBastionIP(blocName, publicIP)

			return publicIP, true
		}
	}

	// Check floating IPs for bastion-named instances
	fips, err := provider.Network().ListFloatingIPs(ctx)
	if err != nil {
		return "", false
	}

	for _, inst := range instances {
		if !isBastionInstance(inst.Name) {
			continue
		}

		for _, fip := range fips {
			if fip.InstanceID == inst.ID && fip.Address != "" {
				logDiscoveryResult(log, "floating-ip-by-name", "", inst.Name, fip.Address)
				cacheBastionIP(blocName, fip.Address)

				return fip.Address, true
			}
		}
	}

	return "", false
}

func findDirectPublicIP(instances []*cpi.Instance, key string, log logger.Logger, blocName string) string {
	for _, inst := range instances {
		if publicIP := firstNonEmpty(inst.FloatingIP, inst.PublicIP); publicIP != "" {
			logDiscoveryResult(log, "label", key, inst.Name, publicIP)
			cacheBastionIP(blocName, publicIP)

			return publicIP
		}
	}

	return ""
}

func findFloatingIPForInstances(ctx context.Context, provider cpi.Provider, instances []*cpi.Instance, key string, log logger.Logger, blocName string) string {
	fips, err := provider.Network().ListFloatingIPs(ctx)
	if err != nil {
		return ""
	}

	for _, inst := range instances {
		for _, fip := range fips {
			if fip.InstanceID == inst.ID && fip.Address != "" {
				logDiscoveryResult(log, "floating-ip-by-label", key, inst.Name, fip.Address)
				cacheBastionIP(blocName, fip.Address)

				return fip.Address
			}
		}
	}

	return ""
}

func isBastionInstance(name string) bool {
	lowerName := strings.ToLower(name)

	return strings.HasSuffix(lowerName, "-"+RoleBastion) || strings.Contains(lowerName, RoleBastion)
}

func logDiscoveryResult(log logger.Logger, strategy, key, name, ipAddress string) {
	if viper.GetBool("debug_lookup") {
		logDebugDiscovery(log, strategy, key, name, ipAddress)
	} else {
		logNormalDiscovery(log, strategy, key, name, ipAddress)
	}
}

func logDebugDiscovery(log logger.Logger, strategy, key, name, ipAddress string) {
	if key != "" {
		log.Infof("Bastion lookup matched: strategy=%s key=%s name=%s ip=%s", strategy, key, name, ipAddress)
	} else {
		log.Infof("Bastion lookup matched: strategy=%s name=%s ip=%s", strategy, name, ipAddress)
	}
}

func logNormalDiscovery(log logger.Logger, strategy, key, name, ipAddress string) {
	if key != "" {
		log.Debugf("Found bastion by %s %s: %s (%s)", strategy, key, name, ipAddress)
	} else {
		log.Debugf("Found bastion by %s: %s (%s)", strategy, name, ipAddress)
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}

	return ""
}

// cacheBastionIP saves the discovered bastion IP in the local state store (best-effort).
func cacheBastionIP(blocName, bastionIP string) {
	if bastionIP == "" {
		return
	}

	stateManager, err := state.NewManager("")
	if err != nil {
		return
	}

	_, err = stateManager.Load(blocName)
	if err != nil {
		return
	}

	err = stateManager.SetOutput("bastion_public_ip", bastionIP)
	if err != nil {
		return
	}

	_ = stateManager.Save()
}
