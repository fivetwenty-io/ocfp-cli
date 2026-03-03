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

	// Delegate to instance-level discovery and extract the IP
	inst, err := findBastionInstance(ctx, provider, blocName)
	if err != nil {
		return "", err
	}

	// Check for a direct public/floating IP on the instance
	if publicIP := firstNonEmpty(inst.FloatingIP, inst.PublicIP); publicIP != "" {
		log.Debugf("Found bastion IP from instance %s: %s", inst.Name, publicIP)
		cacheBastionIP(blocName, publicIP)

		return publicIP, nil
	}

	// Check floating IPs associated by instance ID
	fips, err := provider.Network().ListFloatingIPs(ctx, nil)
	if err == nil {
		for _, fip := range fips {
			if fip.InstanceID == inst.ID && fip.Address != "" {
				log.Debugf("Found bastion floating IP for instance %s: %s", inst.Name, fip.Address)
				cacheBastionIP(blocName, fip.Address)

				return fip.Address, nil
			}
		}
	}

	return "", ErrNoBastionHostFound(blocName)
}

// findBastionInstance discovers the bastion instance using label-based strategies
// followed by name-based pattern matching. It returns the *cpi.Instance directly,
// which is useful when callers need the full instance (e.g., configure commands).
func findBastionInstance(ctx context.Context, provider cpi.Provider, blocName string) (*cpi.Instance, error) {
	log := logger.WithOperation("findBastionInstance")

	// Try label-based discovery strategies
	if inst := tryLabelBasedInstanceDiscovery(ctx, provider, blocName, log); inst != nil {
		return inst, nil
	}

	// Fall back to name-based discovery
	if inst := tryNameBasedInstanceDiscovery(ctx, provider, blocName, log); inst != nil {
		return inst, nil
	}

	return nil, ErrNoBastionHostFound(blocName)
}

func tryLabelBasedInstanceDiscovery(ctx context.Context, provider cpi.Provider, blocName string, log logger.Logger) *cpi.Instance {
	labelKeys := []string{"component", "role", "job"}
	for _, key := range labelKeys {
		filters := map[string]string{
			"label.bloc":   blocName,
			"label." + key: RoleBastion,
		}

		log.Debugf("Label instance discovery: trying key=%s filters=%v", key, filters)

		instances, err := provider.Compute().ListInstances(ctx, filters)
		if err != nil {
			log.Debugf("Label instance discovery: key=%s failed: %v", key, err)

			continue
		}

		if len(instances) == 0 {
			log.Debugf("Label instance discovery: key=%s returned 0 instances", key)

			continue
		}

		log.Debugf("Label instance discovery: key=%s found %d instances", key, len(instances))

		return instances[0]
	}

	return nil
}

func tryNameBasedInstanceDiscovery(ctx context.Context, provider cpi.Provider, blocName string, log logger.Logger) *cpi.Instance {
	log.Debugf("Name instance discovery: listing instances with label.bloc=%s", blocName)

	instances, err := provider.Compute().ListInstances(ctx, map[string]string{
		"label.bloc": blocName,
	})
	if err != nil {
		log.Debugf("Name instance discovery: failed to list instances: %v", err)

		return nil
	}

	if len(instances) == 0 {
		log.Debugf("Name instance discovery: no instances found for bloc=%s", blocName)

		return nil
	}

	log.Debugf("Name instance discovery: checking %d instances for bastion name pattern", len(instances))

	for _, inst := range instances {
		if isBastionInstance(inst.Name) {
			log.Debugf("Name instance discovery: matched instance %s by name pattern", inst.Name)

			return inst
		}
	}

	return nil
}

func tryStateCache(blocName string, log logger.Logger) (string, bool) {
	// Get standard state directory for this bloc
	stateDir, err := state.GetStateDir(blocName)
	if err != nil {
		log.Debugf("State cache: failed to get state dir for %s: %v", blocName, err)

		return "", false
	}

	stateManager, err := state.NewManager(stateDir)
	if err != nil {
		log.Debugf("State cache: failed to create state manager for %s: %v", stateDir, err)

		return "", false
	}

	_, err = stateManager.Load(blocName)
	if err != nil {
		log.Debugf("State cache: failed to load state for %s: %v", blocName, err)

		return "", false
	}

	outputVal, err := stateManager.GetOutput("bastion_public_ip")
	if err != nil {
		log.Debugf("State cache: bastion_public_ip not found in state: %v", err)

		return "", false
	}

	bastionIP, ok := outputVal.(string)
	if !ok || bastionIP == "" {
		log.Debugf("State cache: bastion_public_ip is empty or not a string")

		return "", false
	}

	logDiscoveryResult(log, "state-cache", "", "", bastionIP)

	return bastionIP, true
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

	// Get standard state directory for this bloc
	stateDir, err := state.GetStateDir(blocName)
	if err != nil {
		return
	}

	stateManager, err := state.NewManager(stateDir)
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
