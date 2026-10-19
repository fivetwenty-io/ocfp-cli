package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"strings"
)

// importPrivateTopology verifies every external resource before recording any,
// and never creates a subnet or changes the operator's routing configuration.
func (m *Manager) importPrivateTopology(ctx context.Context) error {
	netMgr := m.provider.NetworkManager()

	network, err := netMgr.GetNetwork(ctx, m.config.Network.ID)
	if err != nil {
		return fmt.Errorf("read imported network: %w", err)
	}

	if network == nil || !sameCloudNetwork(network.ID, m.config.Network.ID) {
		return errors.New("configured imported network was not returned")
	}

	if err := m.checkImportedIdentity("network", m.resolveNetworkName(), network.ID); err != nil {
		return err
	}

	var (
		subs  []*cpi.Subnet
		cidrs []string
	)

	for _, cfg := range m.config.Network.Subnets {
		sub, err := netMgr.GetSubnet(ctx, cfg.ID)
		if err != nil {
			return fmt.Errorf("read imported subnet %s: %w", cfg.ID, err)
		}

		imported, err := m.validateImportedSubnet(sub, network.ID, cfg)
		if err != nil {
			return err
		}

		subs = append(subs, imported)
		cidrs = append(cidrs, sub.CIDR)
	}

	if err := m.validateWorkloadSubnetCount("existing-private", cidrs); err != nil {
		return err
	}

	if err := m.importExistingNetwork(m.resolveNetworkName(), network); err != nil {
		return err
	}

	if err := m.markImported("network", m.resolveNetworkName()); err != nil {
		return err
	}

	for i, sub := range subs {
		name := m.config.Network.Subnets[i].Name
		if err := m.saveSubnetToState(sub, name, network.ID, i); err != nil {
			return err
		}

		if err := m.markImported("subnet", name); err != nil {
			return err
		}
	}

	return m.stateManager.Save()
}
func (m *Manager) markImported(kind, name string) error {
	resource, err := m.stateManager.GetResource(kind, name)
	if err != nil {
		return err
	}

	if resource.Properties == nil {
		resource.Properties = map[string]any{}
	}

	resource.Properties["imported"] = true
	resource.Properties["preserve"] = true

	return m.stateManager.UpdateResource(resource)
}
func sameCloudNetwork(a, b string) bool {
	a = strings.TrimPrefix(a, "https://www.googleapis.com/compute/v1/")
	b = strings.TrimPrefix(b, "https://www.googleapis.com/compute/v1/")

	return a == b || (!strings.Contains(a, "/") && strings.HasSuffix(b, "/"+a)) || (!strings.Contains(b, "/") && strings.HasSuffix(a, "/"+b))
}

func (m *Manager) checkImportedIdentity(kind, name, id string) error {
	resources, err := m.stateManager.GetResourcesByType(kind)
	if err != nil {
		return err
	}

	for _, resource := range resources {
		if resource.Name == name && !sameCloudNetwork(resource.ID, id) {
			return fmt.Errorf("imported %s %s conflicts with tracked resource %s", kind, name, resource.ID)
		}
	}

	return nil
}

func (m *Manager) validateImportedSubnet(sub *cpi.Subnet, networkID string, cfg config.Subnet) (*cpi.Subnet, error) {
	if sub == nil || !sameCloudNetwork(sub.ID, cfg.ID) || !sameCloudNetwork(sub.NetworkID, networkID) {
		return nil, fmt.Errorf("imported subnet %s is missing or belongs to another network", cfg.ID)
	}

	if sub.CIDR != cfg.CIDR {
		return nil, fmt.Errorf("imported subnet %s CIDR differs from configured %s", cfg.ID, cfg.CIDR)
	}

	if err := m.checkImportedIdentity("subnet", cfg.Name, sub.ID); err != nil {
		return nil, err
	}

	if m.config.Provider == "aws" && cfg.AvailabilityZone != "" && cfg.AvailabilityZone != sub.AvailabilityZone {
		return nil, fmt.Errorf("imported subnet %s availability zone differs from configured %s", cfg.ID, cfg.AvailabilityZone)
	}

	if m.config.Provider == "gcp" && strings.TrimSuffix(cfg.AvailabilityZone, cfg.AvailabilityZone[len(cfg.AvailabilityZone)-2:]) != sub.AvailabilityZone {
		return nil, fmt.Errorf("imported subnet %s region differs from configured zone %s", cfg.ID, cfg.AvailabilityZone)
	}

	imported := *sub
	if cfg.AvailabilityZone != "" {
		imported.AvailabilityZone = cfg.AvailabilityZone
	}

	imported.Type = cfg.Type
	if imported.Type == "" {
		imported.Type = "ocfp"
	}

	return &imported, nil
}
