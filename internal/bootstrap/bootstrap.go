package bootstrap

import (
	"context"
	"fmt"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

// Options represents bootstrap options
type Options struct {
	BlocName string
	Provider string
	Region   string
	Force    bool
	DryRun   bool
	Timeout  time.Duration
}

// Manager handles the bootstrap process
type Manager struct {
	config       *config.Config
	provider     cpi.Provider
	stateManager *state.Manager
	options      *Options
}

// NewManager creates a new bootstrap manager
func NewManager(cfg *config.Config, provider cpi.Provider, stateManager *state.Manager, opts *Options) *Manager {
	return &Manager{
		config:       cfg,
		provider:     provider,
		stateManager: stateManager,
		options:      opts,
	}
}

// Execute runs the bootstrap process
func (m *Manager) Execute(ctx context.Context) error {
	logger.Infof("Starting bootstrap for bloc: %s", m.options.BlocName)

	// Load or create state
	if _, err := m.stateManager.Load(m.options.BlocName); err != nil {
		return fmt.Errorf("failed to load state: %w", err)
	}

	// Acquire state lock
	if err := m.stateManager.Lock(m.options.BlocName); err != nil {
		return fmt.Errorf("failed to acquire state lock: %w", err)
	}
	defer m.stateManager.Unlock(m.options.BlocName)

	// Execute bootstrap phases
	phases := []struct {
		name string
		fn   func(context.Context) error
	}{
		{"network", m.createNetwork},
		{"subnets", m.createSubnets},
		{"security_groups", m.createSecurityGroups},
		{"keypair", m.createKeyPair},
		{"volumes", m.createVolumes},
		{"bastion", m.createBastion},
	}

	for _, phase := range phases {
		logger.Infof("Executing phase: %s", phase.name)

		if m.options.DryRun {
			logger.Infof("[DRY RUN] Would execute: %s", phase.name)
			continue
		}

		if err := phase.fn(ctx); err != nil {
			return fmt.Errorf("phase %s failed: %w", phase.name, err)
		}

		// Save state after each phase
		if err := m.stateManager.Save(); err != nil {
			logger.Warnf("Failed to save state after phase %s: %v", phase.name, err)
		}
	}

	logger.Infof("Bootstrap completed successfully for bloc: %s", m.options.BlocName)
	return nil
}

// createNetwork creates the VPC/network
func (m *Manager) createNetwork(ctx context.Context) error {
	logger.Info("Creating network...")

	// Check if network already exists in state
	existingNetwork, _ := m.stateManager.GetResource("network", m.config.Name)
	if existingNetwork != nil && existingNetwork.State == string(cpi.ResourceStateActive) {
		logger.Info("Network already exists, skipping creation")
		return nil
	}

	// Create network
	network, err := m.provider.Network().CreateNetwork(ctx, &cpi.CreateNetworkRequest{
		Name:       fmt.Sprintf("%s-net", m.config.Name),
		CIDR:       m.config.Network.NetworkCIDR,
		DNSServers: m.config.DNS,
		Tags: map[string]string{
			"managed-by": "ocfp",
			"bloc-name":  m.options.BlocName,
			"created-at": time.Now().Format(time.RFC3339),
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create network: %w", err)
	}

	// Save to state
	if err := m.stateManager.AddResource(&state.Resource{
		ID:       network.ID,
		Type:     "network",
		Name:     m.config.Name,
		Provider: m.options.Provider,
		State:    string(network.State),
		Properties: map[string]interface{}{
			"cidr":        network.CIDR,
			"dns_servers": network.DNSServers,
		},
		Tags: network.Tags,
	}); err != nil {
		return fmt.Errorf("failed to save network to state: %w", err)
	}

	// Save network ID as output
	m.stateManager.SetOutput("network_id", network.ID)

	logger.Infof("Network created: %s (%s)", network.Name, network.ID)
	return nil
}

// createSubnets creates the subnets
func (m *Manager) createSubnets(ctx context.Context) error {
	logger.Info("Creating subnets...")

	// Get network ID from state
	networkID, err := m.stateManager.GetOutput("network_id")
	if err != nil {
		return fmt.Errorf("network ID not found in state: %w", err)
	}

	// Create subnets for each bloc
	for _, bloc := range m.config.Blocs {
		for _, subnet := range bloc.Subnets {
			subnetName := fmt.Sprintf("%s-%s", bloc.Name, subnet.Name)

			// Check if subnet already exists
			existingSubnet, _ := m.stateManager.GetResource("subnet", subnetName)
			if existingSubnet != nil && existingSubnet.State == string(cpi.ResourceStateActive) {
				logger.Infof("Subnet %s already exists, skipping", subnetName)
				continue
			}

			// Create subnet
			createdSubnet, err := m.provider.Network().CreateSubnet(ctx, &cpi.CreateSubnetRequest{
				Name:             subnetName,
				NetworkID:        networkID.(string),
				CIDR:             subnet.CIDR,
				AvailabilityZone: subnet.AvailabilityZone,
				Type:             subnet.Type,
				Tags: map[string]string{
					"managed-by":  "ocfp",
					"bloc-name":   m.options.BlocName,
					"environment": bloc.Environment,
				},
			})
			if err != nil {
				return fmt.Errorf("failed to create subnet %s: %w", subnetName, err)
			}

			// Save to state
			if err := m.stateManager.AddResource(&state.Resource{
				ID:       createdSubnet.ID,
				Type:     "subnet",
				Name:     subnetName,
				Provider: m.options.Provider,
				State:    string(createdSubnet.State),
				Properties: map[string]interface{}{
					"cidr":              createdSubnet.CIDR,
					"availability_zone": createdSubnet.AvailabilityZone,
					"network_id":        networkID,
				},
				Tags: createdSubnet.Tags,
			}); err != nil {
				return fmt.Errorf("failed to save subnet to state: %w", err)
			}

			// Add dependency
			m.stateManager.AddDependency(fmt.Sprintf("subnet.%s", subnetName), "network."+m.config.Name)

			// Save subnet ID as output
			m.stateManager.SetOutput(fmt.Sprintf("subnet_%s_id", subnetName), createdSubnet.ID)

			logger.Infof("Subnet created: %s (%s)", subnetName, createdSubnet.ID)
		}
	}

	return nil
}

// createSecurityGroups creates security groups with default rules
func (m *Manager) createSecurityGroups(ctx context.Context) error {
	logger.Info("Creating security groups...")

	// Get network ID
	networkID, err := m.stateManager.GetOutput("network_id")
	if err != nil {
		return fmt.Errorf("network ID not found in state: %w", err)
	}

	// Default security groups to create
	securityGroups := []struct {
		name        string
		description string
		rules       []*cpi.SecurityRule
	}{
		{
			name:        "default",
			description: "Default security group",
			rules: []*cpi.SecurityRule{
				{
					Direction:    "ingress",
					Protocol:     "tcp",
					PortRangeMin: 22,
					PortRangeMax: 22,
					RemoteIPCIDR: "0.0.0.0/0",
					Description:  "SSH access",
				},
				{
					Direction:    "egress",
					Protocol:     "all",
					RemoteIPCIDR: "0.0.0.0/0",
					Description:  "Allow all outbound",
				},
			},
		},
		{
			name:        "bastion",
			description: "Bastion security group",
			rules: []*cpi.SecurityRule{
				{
					Direction:    "ingress",
					Protocol:     "tcp",
					PortRangeMin: 22,
					PortRangeMax: 22,
					RemoteIPCIDR: "0.0.0.0/0",
					Description:  "SSH from anywhere",
				},
				{
					Direction:    "egress",
					Protocol:     "all",
					RemoteIPCIDR: "0.0.0.0/0",
					Description:  "Allow all outbound",
				},
			},
		},
	}

	for _, sg := range securityGroups {
		sgName := fmt.Sprintf("%s-%s", m.config.Name, sg.name)

		// Check if already exists
		existingSG, _ := m.stateManager.GetResource("security_group", sgName)
		if existingSG != nil {
			logger.Infof("Security group %s already exists, skipping", sgName)
			continue
		}

		// Create security group
		createdSG, err := m.provider.Security().CreateSecurityGroup(ctx, &cpi.CreateSecurityGroupRequest{
			Name:        sgName,
			Description: sg.description,
			NetworkID:   networkID.(string),
			Rules:       sg.rules,
			Tags: map[string]string{
				"managed-by": "ocfp",
				"bloc-name":  m.options.BlocName,
			},
		})
		if err != nil {
			return fmt.Errorf("failed to create security group %s: %w", sgName, err)
		}

		// Save to state
		if err := m.stateManager.AddResource(&state.Resource{
			ID:       createdSG.ID,
			Type:     "security_group",
			Name:     sgName,
			Provider: m.options.Provider,
			State:    "active",
			Properties: map[string]interface{}{
				"network_id": networkID,
				"rules":      len(createdSG.Rules),
			},
			Tags: createdSG.Tags,
		}); err != nil {
			return fmt.Errorf("failed to save security group to state: %w", err)
		}

		m.stateManager.SetOutput(fmt.Sprintf("sg_%s_id", sg.name), createdSG.ID)
		logger.Infof("Security group created: %s", sgName)
	}

	return nil
}

// createKeyPair creates or imports SSH key pair
func (m *Manager) createKeyPair(ctx context.Context) error {
	logger.Info("Managing SSH key pair...")

	keypairName := fmt.Sprintf("%s-bastion", m.config.Name)

	// Check if keypair already exists
	existingKP, _ := m.stateManager.GetResource("keypair", keypairName)
	if existingKP != nil {
		logger.Info("Key pair already exists, skipping")
		return nil
	}

	// Try to get existing key pair first
	existingKeyPair, err := m.provider.Compute().GetKeyPair(ctx, keypairName)
	if err != nil && !cpi.IsNotFound(err) {
		return fmt.Errorf("failed to check existing key pair: %w", err)
	}

	if existingKeyPair != nil {
		logger.Infof("Using existing key pair: %s", keypairName)
	} else {
		// Create new key pair
		keyPair, err := m.provider.Compute().CreateKeyPair(ctx, keypairName)
		if err != nil {
			return fmt.Errorf("failed to create key pair: %w", err)
		}

		// Save private key to local file
		if keyPair.PrivateKey != "" {
			keyPath := config.GetSSHKeyPath(m.config.Name, "bastion")
			// TODO: Save private key to file
			logger.Infof("Private key would be saved to: %s", keyPath)
		}

		logger.Infof("Key pair created: %s", keypairName)
	}

	// Save to state
	if err := m.stateManager.AddResource(&state.Resource{
		ID:       keypairName,
		Type:     "keypair",
		Name:     keypairName,
		Provider: m.options.Provider,
		State:    "active",
		Tags: map[string]string{
			"managed-by": "ocfp",
			"bloc-name":  m.options.BlocName,
		},
	}); err != nil {
		return fmt.Errorf("failed to save key pair to state: %w", err)
	}

	return nil
}

// createVolumes creates persistent volumes
func (m *Manager) createVolumes(ctx context.Context) error {
	logger.Info("Creating volumes...")

	// Default volumes for bastion
	volumes := []struct {
		name    string
		size    int
		volType string
	}{
		{"bastion-root", 50, "standard"},
		{"bastion-data", 100, "standard"},
	}

	for _, vol := range volumes {
		volName := fmt.Sprintf("%s-%s", m.config.Name, vol.name)

		// Check if already exists
		existingVol, _ := m.stateManager.GetResource("volume", volName)
		if existingVol != nil {
			logger.Infof("Volume %s already exists, skipping", volName)
			continue
		}

		// Create volume
		volume, err := m.provider.Storage().CreateVolume(ctx, &cpi.CreateVolumeRequest{
			Name: volName,
			Size: vol.size,
			Type: vol.volType,
			Tags: map[string]string{
				"managed-by": "ocfp",
				"bloc-name":  m.options.BlocName,
			},
		})
		if err != nil {
			return fmt.Errorf("failed to create volume %s: %w", volName, err)
		}

		// Save to state
		if err := m.stateManager.AddResource(&state.Resource{
			ID:       volume.ID,
			Type:     "volume",
			Name:     volName,
			Provider: m.options.Provider,
			State:    string(volume.State),
			Properties: map[string]interface{}{
				"size": volume.Size,
				"type": volume.Type,
			},
			Tags: volume.Tags,
		}); err != nil {
			return fmt.Errorf("failed to save volume to state: %w", err)
		}

		m.stateManager.SetOutput(fmt.Sprintf("volume_%s_id", vol.name), volume.ID)
		logger.Infof("Volume created: %s (%dGB)", volName, vol.size)
	}

	return nil
}

// createBastion creates the bastion host
func (m *Manager) createBastion(ctx context.Context) error {
	logger.Info("Creating bastion host...")

	bastionName := fmt.Sprintf("%s-bastion", m.config.Name)

	// Check if already exists
	existingBastion, _ := m.stateManager.GetResource("instance", bastionName)
	if existingBastion != nil && existingBastion.State == string(cpi.ResourceStateActive) {
		logger.Info("Bastion already exists, skipping")
		return nil
	}

	// Get required IDs from state
	networkID, _ := m.stateManager.GetOutput("network_id")
	subnetID, _ := m.stateManager.GetOutput("subnet_mgmt-default_id")
	sgID, _ := m.stateManager.GetOutput("sg_bastion_id")

	// Create bastion instance
	instance, err := m.provider.Compute().CreateInstance(ctx, &cpi.CreateInstanceRequest{
		Name:           bastionName,
		Flavor:         m.config.Bastion.Flavor,
		Image:          m.config.Bastion.Image,
		NetworkID:      networkID.(string),
		SubnetID:       subnetID.(string),
		SecurityGroups: []string{sgID.(string)},
		KeyPair:        fmt.Sprintf("%s-bastion", m.config.Name),
		UserData:       generateBastionUserData(m.config),
		Tags: map[string]string{
			"managed-by": "ocfp",
			"bloc-name":  m.options.BlocName,
			"role":       "bastion",
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create bastion: %w", err)
	}

	// Save to state
	if err := m.stateManager.AddResource(&state.Resource{
		ID:       instance.ID,
		Type:     "instance",
		Name:     bastionName,
		Provider: m.options.Provider,
		State:    string(instance.State),
		Properties: map[string]interface{}{
			"flavor":     instance.Flavor,
			"image":      instance.Image,
			"private_ip": instance.PrivateIP,
			"public_ip":  instance.PublicIP,
		},
		Tags: instance.Tags,
	}); err != nil {
		return fmt.Errorf("failed to save bastion to state: %w", err)
	}

	// Add dependencies
	m.stateManager.AddDependency(fmt.Sprintf("instance.%s", bastionName), fmt.Sprintf("subnet.mgmt-default"))
	m.stateManager.AddDependency(fmt.Sprintf("instance.%s", bastionName), fmt.Sprintf("security_group.%s-bastion", m.config.Name))

	// Save outputs
	m.stateManager.SetOutput("bastion_id", instance.ID)
	m.stateManager.SetOutput("bastion_private_ip", instance.PrivateIP)
	if instance.PublicIP != "" {
		m.stateManager.SetOutput("bastion_public_ip", instance.PublicIP)
	}

	logger.Infof("Bastion created: %s (IP: %s)", bastionName, instance.PrivateIP)
	return nil
}

// generateBastionUserData generates cloud-init user data for bastion
func generateBastionUserData(cfg *config.Config) string {
	// TODO: Generate proper cloud-init script
	return fmt.Sprintf(`#!/bin/bash
set -e

# Set hostname
hostnamectl set-hostname %s-bastion

# Update system
apt-get update
apt-get upgrade -y

# Install required packages
apt-get install -y git tmux vim curl wget jq

# Set environment variables
echo "export OCFP_BLOC_NAME=%s" >> /etc/environment

# Create ocfp user
useradd -m -s /bin/bash ocfp
usermod -aG sudo ocfp

# Setup directories
mkdir -p /home/ocfp/.ocfp
chown -R ocfp:ocfp /home/ocfp/.ocfp

echo "Bastion initialization complete"
`, cfg.Name, cfg.Name)
}
