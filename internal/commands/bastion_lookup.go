package commands

import (
	"context"
	"fmt"
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

	// 0) Try local state cache first
	if stateManager, err := state.NewManager(""); err == nil {
		if _, lerr := stateManager.Load(blocName); lerr == nil {
			if v, gerr := stateManager.GetOutput("bastion_public_ip"); gerr == nil {
				if bastionIP, ok := v.(string); ok && bastionIP != "" {
					if viper.GetBool("debug_lookup") {
						log.Infof("Bastion lookup matched: strategy=state-cache ip=%s", bastionIP)
					} else {
						log.Debugf("Using bastion IP from state cache: %s", bastionIP)
					}

					return bastionIP, nil
				}
			}
		}
	}

	// Try common label keys used across implementations
	labelKeys := []string{"component", "role", "job"}
	for _, key := range labelKeys {
		filters := map[string]string{
			"label.bloc": blocName,
		}
		filters["label."+key] = "bastion"

		instances, err := provider.Compute().ListInstances(ctx, filters)
		if err != nil {
			return "", fmt.Errorf("failed to list instances: %w", err)
		}

		if len(instances) == 0 {
			continue
		}

		// Prefer an instance that already exposes a floating/public IP
		for _, inst := range instances {
			publicIP := firstNonEmpty(inst.FloatingIP, inst.PublicIP)
			if publicIP != "" {
				if viper.GetBool("debug_lookup") {
					log.Infof("Bastion lookup matched: strategy=label key=%s name=%s ip=%s", key, inst.Name, publicIP)
				} else {
					log.Debugf("Found bastion by label %s: %s (%s)", key, inst.Name, publicIP)
				}

				cacheBastionIP(blocName, publicIP)

				return publicIP, nil
			}
		}

		// Fallback: fetch any associated floating IPs and return the first match
		if len(instances) > 0 {
			fips, err := provider.Network().ListFloatingIPs(ctx)
			if err == nil {
				for _, inst := range instances {
					for _, fip := range fips {
						if fip.InstanceID == inst.ID && fip.Address != "" {
							if viper.GetBool("debug_lookup") {
								log.Infof("Bastion lookup matched: strategy=floating-ip-by-label key=%s name=%s ip=%s", key, inst.Name, fip.Address)
							} else {
								log.Debugf("Found bastion floating IP by label %s: %s (%s)", key, inst.Name, fip.Address)
							}

							cacheBastionIP(blocName, fip.Address)

							return fip.Address, nil
						}
					}
				}
			}
		}
	}

	// Name-based fallback: list by bloc only, then match name containing "-bastion"
	instances, err := provider.Compute().ListInstances(ctx, map[string]string{
		"label.bloc": blocName,
	})
	if err != nil {
		return "", fmt.Errorf("failed to list instances: %w", err)
	}

	if len(instances) == 0 {
		return "", fmt.Errorf("no instances found for bloc %s", blocName)
	}

	for _, inst := range instances {
		name := strings.ToLower(inst.Name)
		if strings.HasSuffix(name, "-bastion") || strings.Contains(name, "bastion") {
			publicIP := firstNonEmpty(inst.FloatingIP, inst.PublicIP)
			if publicIP != "" {
				if viper.GetBool("debug_lookup") {
					log.Infof("Bastion lookup matched: strategy=name name=%s ip=%s", inst.Name, publicIP)
				} else {
					log.Debugf("Found bastion by name: %s (%s)", inst.Name, publicIP)
				}

				cacheBastionIP(blocName, publicIP)

				return publicIP, nil
			}
		}
	}

	// Final attempt: correlate any instance with name like bastion to floating IP resources
	fips, err := provider.Network().ListFloatingIPs(ctx)
	if err == nil {
		for _, inst := range instances {
			name := strings.ToLower(inst.Name)
			if !strings.HasSuffix(name, "-bastion") && !strings.Contains(name, "bastion") {
				continue
			}

			for _, fip := range fips {
				if fip.InstanceID == inst.ID && fip.Address != "" {
					if viper.GetBool("debug_lookup") {
						log.Infof("Bastion lookup matched: strategy=floating-ip-by-name name=%s ip=%s", inst.Name, fip.Address)
					} else {
						log.Debugf("Found bastion floating IP by name: %s (%s)", inst.Name, fip.Address)
					}

					cacheBastionIP(blocName, fip.Address)

					return fip.Address, nil
				}
			}
		}
	}

	return "", fmt.Errorf("no bastion host found for bloc %s", blocName)
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

	if _, err := stateManager.Load(blocName); err != nil {
		return
	}

	if err := stateManager.SetOutput("bastion_public_ip", bastionIP); err != nil {
		return
	}

	_ = stateManager.Save()
}
