package vault

import (
	"fmt"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/providers"
	"go.uber.org/zap"
)

// ProxmoxVaultProvider implements vault operations for Proxmox.
type ProxmoxVaultProvider struct {
	*providers.BaseVaultProvider

	Safe        SafeInterface
	PathBuilder *PathBuilder
	logger      *zap.SugaredLogger
}

// NewProxmoxVaultProvider creates a new Proxmox vault provider.
func NewProxmoxVaultProvider(cfg *config.Config, safe SafeInterface, blocName string) *ProxmoxVaultProvider {
	return &ProxmoxVaultProvider{
		BaseVaultProvider: providers.NewBaseVaultProvider(cfg, blocName),
		Safe:              safe,
		PathBuilder:       NewPathBuilder(cfg, blocName),
		logger:            logger.Get(),
	}
}

// Configure performs full vault configuration for Proxmox.
func (p *ProxmoxVaultProvider) Configure(reporter providers.ProgressReporter) error {
	p.logger.Infow("Starting Proxmox vault configuration", "bloc", p.BlocName)

	// Track phase numbers across entire configuration (0-based for ReportPhaseStart)
	phaseIndex := 0

	// Total phases: 1 (config) + 7 (mgmt: networks, subnets, security-groups, blobstores, databases, load-balancers, fqdns)
	//               + 7 (ocf: same) + 2 (shared: certificates, public-ips) = 17
	totalPhases := 17

	// Save OCFP configuration to vault first (phase 1)
	err := p.SaveConfigToVault(reporter, phaseIndex, totalPhases)
	if err != nil {
		return fmt.Errorf("failed to save config to vault: %w", err)
	}

	phaseIndex++

	// Configure both management and OCF environments
	for _, envType := range []string{"mgmt", "ocf"} {
		err := p.configureEnvironment(envType, reporter, &phaseIndex, totalPhases)
		if err != nil {
			return err
		}
	}

	// Configure shared components
	err = p.configureSharedComponents(reporter, &phaseIndex, totalPhases)
	if err != nil {
		return err
	}

	// Report final summary
	if reporter != nil {
		reporter.ReportFinalSummary(true, 0, totalPhases, 0)
	}

	p.logger.Infow("Proxmox vault configuration completed", "bloc", p.BlocName)

	return nil
}

// SaveConfigToVault saves the OCFP configuration to vault.
func (p *ProxmoxVaultProvider) SaveConfigToVault(reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
	phaseName := PhaseConfig
	phaseStart := time.Now()

	if reporter != nil {
		reporter.ReportPhaseStart(phaseName, phaseNum, totalPhases)
	}

	p.logger.Info("Saving OCFP configuration to vault")

	configPath := p.PathBuilder.GetConfigPath()

	configData := map[string]interface{}{
		"provider":        "proxmox",
		"bloc":            p.BlocName,
		"host":            p.Config.APIEndpoint,
		"node":            p.Config.Region,
		"network_mode":    "bridge",
		"default_bridge":  "vmbr0",
		"default_storage": "local-lvm",
	}

	err := p.Safe.SetMultiple(configPath, configData)
	if err != nil {
		return fmt.Errorf("failed to save config to vault: %w", err)
	}

	if reporter != nil {
		reporter.ReportPhaseComplete(phaseName, time.Since(phaseStart))
	}

	return nil
}

// ConfigureNetworks configures network settings.
func (p *ProxmoxVaultProvider) ConfigureNetworks(_envPath, envType string, reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
	phaseName := "networks-" + envType
	phaseStart := time.Now()

	if reporter != nil {
		reporter.ReportPhaseStart(phaseName, phaseNum, totalPhases)
	}

	p.logger.Infow("Configuring networks", "env_type", envType)

	// Proxmox uses bridges instead of VPCs
	networkPath := p.PathBuilder.GetNetPath(envType)

	networkConfig := map[string]interface{}{
		"type":   "bridge",
		"bridge": "vmbr0",
		"cidr":   p.Config.VPCCIDRBlock,
	}

	if p.Config.Network.CIDR != "" {
		networkConfig["cidr"] = p.Config.Network.CIDR
	}

	err := p.Safe.SetMultiple(networkPath, networkConfig)
	if err != nil {
		return fmt.Errorf("failed to set network configuration: %w", err)
	}

	if reporter != nil {
		reporter.ReportPhaseComplete(phaseName, time.Since(phaseStart))
	}

	return nil
}

// ConfigureSubnets configures subnet settings (minimal for Proxmox).
func (p *ProxmoxVaultProvider) ConfigureSubnets(_envPath, envType string, reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
	phaseName := "subnets-" + envType
	phaseStart := time.Now()

	if reporter != nil {
		reporter.ReportPhaseStart(phaseName, phaseNum, totalPhases)
	}

	p.logger.Infow("Configuring subnets", "env_type", envType)

	// Proxmox bridges don't have native subnets - store CIDR metadata
	subnetPath := p.PathBuilder.GetSubnetsPath(envType)

	subnetConfig := map[string]interface{}{
		"note": "Proxmox bridges do not have native subnets",
		"cidr": p.Config.VPCCIDRBlock,
	}

	err := p.Safe.SetMultiple(subnetPath, subnetConfig)
	if err != nil {
		return fmt.Errorf("failed to set subnet configuration: %w", err)
	}

	if reporter != nil {
		reporter.ReportPhaseComplete(phaseName, time.Since(phaseStart))
	}

	return nil
}

// ConfigureSecurityGroups configures security group settings.
func (p *ProxmoxVaultProvider) ConfigureSecurityGroups(_envPath, envType string, reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
	phaseName := "security-groups-" + envType
	phaseStart := time.Now()

	if reporter != nil {
		reporter.ReportPhaseStart(phaseName, phaseNum, totalPhases)
	}

	p.logger.Infow("Configuring security groups", "env_type", envType)

	// Proxmox uses firewall groups at cluster level
	sgPath := p.PathBuilder.GetSecurityGroupsPath(envType)

	sgConfig := map[string]interface{}{
		"type":        "firewall_group",
		"description": fmt.Sprintf("Security group for %s environment", envType),
	}

	err := p.Safe.SetMultiple(sgPath, sgConfig)
	if err != nil {
		return fmt.Errorf("failed to set security group configuration: %w", err)
	}

	if reporter != nil {
		reporter.ReportPhaseComplete(phaseName, time.Since(phaseStart))
	}

	return nil
}

// ConfigureBlobstores configures blobstore settings.
func (p *ProxmoxVaultProvider) ConfigureBlobstores(_envPath, envType string, reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
	phaseName := "blobstores-" + envType
	phaseStart := time.Now()

	if reporter != nil {
		reporter.ReportPhaseStart(phaseName, phaseNum, totalPhases)
	}

	p.logger.Infow("Configuring blobstores", "env_type", envType)

	// Proxmox doesn't have native object storage - document external requirement
	blobstorePath := p.PathBuilder.GetBlobstoresPath(envType)

	blobstoreConfig := map[string]interface{}{
		"note":     "Proxmox requires external S3-compatible storage (MinIO, Ceph, etc.)",
		"provider": "external",
	}

	err := p.Safe.SetMultiple(blobstorePath, blobstoreConfig)
	if err != nil {
		return fmt.Errorf("failed to set blobstore configuration: %w", err)
	}

	if reporter != nil {
		reporter.ReportPhaseComplete(phaseName, time.Since(phaseStart))
	}

	return nil
}

// ConfigureDatabases configures database settings.
func (p *ProxmoxVaultProvider) ConfigureDatabases(_envPath, envType string, reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
	phaseName := "databases-" + envType
	phaseStart := time.Now()

	if reporter != nil {
		reporter.ReportPhaseStart(phaseName, phaseNum, totalPhases)
	}

	p.logger.Infow("Configuring databases", "env_type", envType)

	// Database configuration (typically VM-based for Proxmox)
	dbPath := p.PathBuilder.GetDatabasesPath(envType)

	dbConfig := map[string]interface{}{
		"type": "vm-based",
		"note": "Databases deployed as VMs on Proxmox",
	}

	err := p.Safe.SetMultiple(dbPath, dbConfig)
	if err != nil {
		return fmt.Errorf("failed to set database configuration: %w", err)
	}

	if reporter != nil {
		reporter.ReportPhaseComplete(phaseName, time.Since(phaseStart))
	}

	return nil
}

// ConfigureLoadBalancers configures load balancer settings.
func (p *ProxmoxVaultProvider) ConfigureLoadBalancers(_envPath, envType string, reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
	phaseName := "load-balancers-" + envType
	phaseStart := time.Now()

	if reporter != nil {
		reporter.ReportPhaseStart(phaseName, phaseNum, totalPhases)
	}

	p.logger.Infow("Configuring load balancers", "env_type", envType)

	// Proxmox doesn't have native load balancers - document external requirement
	lbPath := p.PathBuilder.GetLoadBalancersPath(envType)

	lbConfig := map[string]interface{}{
		"note":     "Proxmox requires external load balancer (HAProxy VM, MetalLB, etc.)",
		"provider": "external",
	}

	err := p.Safe.SetMultiple(lbPath, lbConfig)
	if err != nil {
		return fmt.Errorf("failed to set load balancer configuration: %w", err)
	}

	if reporter != nil {
		reporter.ReportPhaseComplete(phaseName, time.Since(phaseStart))
	}

	return nil
}

// ConfigureFQDNs configures FQDN settings.
func (p *ProxmoxVaultProvider) ConfigureFQDNs(_envPath, envType string, reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
	phaseName := "fqdns-" + envType
	phaseStart := time.Now()

	if reporter != nil {
		reporter.ReportPhaseStart(phaseName, phaseNum, totalPhases)
	}

	p.logger.Infow("Configuring FQDNs", "env_type", envType)

	fqdnPath := p.PathBuilder.GetFQDNsPath(envType)

	fqdnConfig := map[string]interface{}{
		"env_type": envType,
	}

	// Add configured FQDNs if available
	if p.Config.FQDNs != nil {
		if p.Config.FQDNs.Base != "" {
			fqdnConfig["base"] = p.Config.FQDNs.Base
		}
	}

	err := p.Safe.SetMultiple(fqdnPath, fqdnConfig)
	if err != nil {
		return fmt.Errorf("failed to set FQDN configuration: %w", err)
	}

	if reporter != nil {
		reporter.ReportPhaseComplete(phaseName, time.Since(phaseStart))
	}

	return nil
}

// ConfigureCertificates configures TLS certificates.
func (p *ProxmoxVaultProvider) ConfigureCertificates(_envPath, _envType string, reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
	phaseName := PhaseCertificates
	phaseStart := time.Now()

	if reporter != nil {
		reporter.ReportPhaseStart(phaseName, phaseNum, totalPhases)
	}

	p.logger.Info("Configuring certificates")

	certsPath := p.PathBuilder.GetCertsPath()

	certConfig := map[string]interface{}{
		"provider": "letsencrypt",
		"note":     "Certificates managed via Let's Encrypt or manual import",
	}

	err := p.Safe.SetMultiple(certsPath, certConfig)
	if err != nil {
		return fmt.Errorf("failed to set certificate configuration: %w", err)
	}

	if reporter != nil {
		reporter.ReportPhaseComplete(phaseName, time.Since(phaseStart))
	}

	return nil
}

// ConfigurePublicIPs configures public IP settings.
func (p *ProxmoxVaultProvider) ConfigurePublicIPs(reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
	phaseName := PhasePublicIPs
	phaseStart := time.Now()

	if reporter != nil {
		reporter.ReportPhaseStart(phaseName, phaseNum, totalPhases)
	}

	p.logger.Info("Configuring public IPs")

	publicIPPath := p.PathBuilder.GetPublicIPsPath()

	publicIPConfig := map[string]interface{}{
		"note":     "Proxmox requires external IP management",
		"provider": "external",
	}

	err := p.Safe.SetMultiple(publicIPPath, publicIPConfig)
	if err != nil {
		return fmt.Errorf("failed to set public IP configuration: %w", err)
	}

	if reporter != nil {
		reporter.ReportPhaseComplete(phaseName, time.Since(phaseStart))
	}

	return nil
}

// ConfigureIAAS configures IAAS settings (implements VaultProvider interface).
func (p *ProxmoxVaultProvider) ConfigureIAAS(envPath, envType string, reporter providers.ProgressReporter, phaseNum *int, totalPhases int) error {
	// IAAS configuration is handled by ConfigureNetworks for Proxmox
	return p.ConfigureNetworks(envPath, envType, reporter, *phaseNum, totalPhases)
}

// GetProviderName returns the provider name.
func (p *ProxmoxVaultProvider) GetProviderName() string {
	return "proxmox"
}

// configureEnvironment configures vault paths for a specific environment type.
func (p *ProxmoxVaultProvider) configureEnvironment(envType string, reporter providers.ProgressReporter, phaseIndex *int, totalPhases int) error {
	envPath := p.PathBuilder.GetEnvironmentPath(envType)

	// Configure networks
	err := p.ConfigureNetworks(envPath, envType, reporter, *phaseIndex, totalPhases)
	if err != nil {
		return err
	}

	*phaseIndex++

	// Configure subnets (minimal for Proxmox - bridges don't have native subnets)
	err = p.ConfigureSubnets(envPath, envType, reporter, *phaseIndex, totalPhases)
	if err != nil {
		return err
	}

	*phaseIndex++

	// Configure security groups
	err = p.ConfigureSecurityGroups(envPath, envType, reporter, *phaseIndex, totalPhases)
	if err != nil {
		return err
	}

	*phaseIndex++

	// Configure blobstores (external for Proxmox)
	err = p.ConfigureBlobstores(envPath, envType, reporter, *phaseIndex, totalPhases)
	if err != nil {
		return err
	}

	*phaseIndex++

	// Configure databases
	err = p.ConfigureDatabases(envPath, envType, reporter, *phaseIndex, totalPhases)
	if err != nil {
		return err
	}

	*phaseIndex++

	// Configure load balancers (external for Proxmox)
	err = p.ConfigureLoadBalancers(envPath, envType, reporter, *phaseIndex, totalPhases)
	if err != nil {
		return err
	}

	*phaseIndex++

	// Configure FQDNs
	err = p.ConfigureFQDNs(envPath, envType, reporter, *phaseIndex, totalPhases)
	if err != nil {
		return err
	}

	*phaseIndex++

	return nil
}

// configureSharedComponents configures shared vault paths.
func (p *ProxmoxVaultProvider) configureSharedComponents(reporter providers.ProgressReporter, phaseIndex *int, totalPhases int) error {
	// Configure certificates
	err := p.ConfigureCertificates("", "", reporter, *phaseIndex, totalPhases)
	if err != nil {
		return err
	}

	*phaseIndex++

	// Configure public IPs
	err = p.ConfigurePublicIPs(reporter, *phaseIndex, totalPhases)
	if err != nil {
		return err
	}

	*phaseIndex++

	return nil
}
