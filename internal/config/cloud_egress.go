package config

import (
	"errors"
	"fmt"
	"strings"
)

// ValidateCloudEgress requires explicit infrastructure identity for private cloud guests.
func (cfg *Config) ValidateCloudEgress() error {
	if err := cfg.validateSharedVPC(); err != nil {
		return err
	}

	mode := cfg.Network.CloudEgress
	if mode == "" {
		return nil
	}

	if !strings.EqualFold(cfg.Provider, "aws") && !strings.EqualFold(cfg.Provider, "gcp") {
		return errors.New("network.cloudEgress applies only to AWS/GCP")
	}

	if mode == "public" {
		return nil
	}

	if mode != "existing-private" {
		return errors.New("network.cloudEgress must be public or existing-private")
	}

	if cfg.Network.ID == "" || len(cfg.Network.Subnets) == 0 {
		return errors.New("existing-private requires network.id and explicit network.subnets with IDs")
	}

	return cfg.validatePrivateSubnets()
}

func (cfg *Config) validatePrivateSubnets() error {
	seen := map[string]bool{}
	names := map[string]bool{}

	for _, sub := range cfg.Network.Subnets {
		if sub.ID == "" || sub.Name == "" || sub.CIDR == "" {
			return errors.New("existing-private requires each subnet id, name and cidr")
		}

		if sub.Type != "" && sub.Type != "ocfp" {
			return errors.New("existing-private subnets must have type ocfp")
		}

		if strings.EqualFold(cfg.Provider, "gcp") && (len(sub.AvailabilityZone) < 3 || sub.AvailabilityZone[len(sub.AvailabilityZone)-2] != '-') {
			return errors.New("existing-private GCP subnet requires a physical availabilityZone")
		}

		if seen[sub.ID] || names[sub.Name] {
			return fmt.Errorf("duplicate imported subnet ID %s", sub.ID)
		}

		seen[sub.ID] = true
		names[sub.Name] = true
	}

	return nil
}

// CloudPublicIP is false only when the operator supplies private egress routing.
func (cfg *Config) CloudPublicIP() bool { return cfg.Network.CloudEgress != "existing-private" }

func (cfg *Config) validateSharedVPC() error {
	if !strings.EqualFold(cfg.Provider, "gcp") {
		return nil
	}

	if cfg.EnableSharedVPC && cfg.HostProjectID == "" {
		return errors.New("enable_shared_vpc requires host_project_id")
	}

	if !cfg.EnableSharedVPC && cfg.HostProjectID != "" {
		return errors.New("host_project_id requires enable_shared_vpc")
	}

	expected := cfg.ProjectID
	if cfg.EnableSharedVPC {
		expected = cfg.HostProjectID
	}

	ids := []string{cfg.Network.ID}
	for _, sub := range cfg.Network.Subnets {
		ids = append(ids, sub.ID)
	}

	for _, id := range ids {
		parts := strings.Split(strings.TrimPrefix(id, "https://www.googleapis.com/compute/v1/"), "/")
		if len(parts) >= 4 && parts[0] == "projects" && parts[1] != expected {
			return fmt.Errorf("network resource project %s differs from configured network project %s; configure enable_shared_vpc and host_project_id for Shared VPC", parts[1], expected)
		}
	}

	return nil
}
