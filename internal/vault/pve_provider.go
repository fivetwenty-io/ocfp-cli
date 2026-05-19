package vault

import (
	"fmt"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/providers"
	"go.uber.org/zap"
)

// PVEVaultProvider implements vault operations for Proxmox VE.
type PVEVaultProvider struct {
	*providers.BaseVaultProvider

	Safe        SafeInterface
	PathBuilder *PathBuilder
	logger      *zap.SugaredLogger

	// BlobstoreEndpoint is the S3-compatible endpoint URL supplied by the operator via
	// --blobstore-endpoint. When empty and BlobstoreMode is local/empty, ConfigureBlobstores
	// writes only the local-mode marker. When BlobstoreEndpoint is set, the endpoint plus
	// region/path_style flags are written to cf/blobstores/main, and credentials go to a
	// separate cf/blobstores/main/creds path.
	BlobstoreEndpoint string

	// BlobstoreMode is "local" (default) or "external". Determines whether bucket
	// creation runs and what gets written to vault.
	BlobstoreMode string

	// BlobstoreRegion is the S3 region (default "us-east-1" when empty).
	BlobstoreRegion string

	// BlobstoreAccessKey + BlobstoreSecretKey carry external-mode S3 credentials.
	// Written to a separate vault path so the config path stays secret-free.
	BlobstoreAccessKey string
	BlobstoreSecretKey string //nolint:gosec // field name is descriptive
}

// NewPVEVaultProvider creates a new Proxmox VE vault provider.
func NewPVEVaultProvider(cfg *config.Config, safe SafeInterface, blocName string) *PVEVaultProvider {
	return &PVEVaultProvider{
		BaseVaultProvider: providers.NewBaseVaultProvider(cfg, blocName),
		Safe:              safe,
		PathBuilder:       NewPathBuilder(cfg, blocName),
		logger:            logger.Get(),
	}
}

// Configure performs full vault configuration for Proxmox VE.
func (p *PVEVaultProvider) Configure(reporter providers.ProgressReporter) error {
	p.logger.Infow("Starting PVE vault configuration", "bloc", p.BlocName)

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

	p.logger.Infow("PVE vault configuration completed", "bloc", p.BlocName)

	return nil
}

// SaveConfigToVault saves the OCFP configuration to vault.
func (p *PVEVaultProvider) SaveConfigToVault(reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
	phaseName := PhaseConfig
	phaseStart := time.Now()

	if reporter != nil {
		reporter.ReportPhaseStart(phaseName, phaseNum, totalPhases)
	}

	p.logger.Info("Saving OCFP configuration to vault")

	configPath := p.PathBuilder.GetConfigPath()

	configData := map[string]interface{}{
		"provider":        "pve",
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
func (p *PVEVaultProvider) ConfigureNetworks(_envPath, envType string, reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
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
func (p *PVEVaultProvider) ConfigureSubnets(_envPath, envType string, reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
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
func (p *PVEVaultProvider) ConfigureSecurityGroups(_envPath, envType string, reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
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

// ConfigureBlobstores writes blobstore configuration to vault.
//
// Two modes are honoured:
//
//   - local (default): writes only `mode: local` and `status: configured` to
//     cf/blobstores/main. No endpoint, region, or credentials are written.
//
//   - external: writes mode, endpoint, region, and path_style to
//     cf/blobstores/main. Credentials go to cf/blobstores/main/creds so the
//     config path stays free of secrets. Empty endpoint in external mode
//     surfaces an error rather than writing a half-configured entry.
//
// Path mirrors AWS cf/blobstores/main naming for kit compatibility.
func (p *PVEVaultProvider) ConfigureBlobstores(_envPath, envType string, reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
	phaseName := "blobstores-" + envType
	phaseStart := time.Now()

	if reporter != nil {
		reporter.ReportPhaseStart(phaseName, phaseNum, totalPhases)
	}

	mode := p.resolveBlobstoreMode()
	blobstorePath := p.PathBuilder.GetSystemBlobstorePath(envType, "cf", "main")

	switch mode {
	case "external":
		err := p.configureExternalBlobstore(envType, blobstorePath)
		if err != nil {
			return err
		}
	default:
		p.logger.Infow("Configuring local-mode blobstore (no external endpoint)", "env_type", envType)

		err := p.Safe.SetMultiple(blobstorePath, map[string]interface{}{
			"mode":   "local",
			"status": "configured",
		})
		if err != nil {
			return fmt.Errorf("failed to set blobstore (local mode) configuration: %w", err)
		}
	}

	if reporter != nil {
		reporter.ReportPhaseComplete(phaseName, time.Since(phaseStart))
	}

	return nil
}

// resolveBlobstoreMode picks the active mode, preferring an explicit operator
// choice and falling back to `external` whenever an endpoint was supplied
// (backwards compatible with the old --blobstore-endpoint-only contract).
func (p *PVEVaultProvider) resolveBlobstoreMode() string {
	if p.BlobstoreMode != "" {
		return p.BlobstoreMode
	}

	if p.BlobstoreEndpoint != "" {
		return "external"
	}

	return "local"
}

// configureExternalBlobstore writes the external-mode config + credentials.
func (p *PVEVaultProvider) configureExternalBlobstore(envType, blobstorePath string) error {
	if p.BlobstoreEndpoint == "" {
		return fmt.Errorf("pve blobstore external mode requires --blobstore-endpoint")
	}

	region := p.BlobstoreRegion
	if region == "" {
		region = "us-east-1"
	}

	p.logger.Infow("Configuring external blobstore", "env_type", envType, "endpoint", p.BlobstoreEndpoint, "region", region)

	blobstoreConfig := map[string]interface{}{
		"mode":       "external",
		"endpoint":   p.BlobstoreEndpoint,
		"region":     region,
		"path_style": true,
		"status":     "configured",
	}

	err := p.Safe.SetMultiple(blobstorePath, blobstoreConfig)
	if err != nil {
		return fmt.Errorf("failed to set blobstore configuration: %w", err)
	}

	if p.BlobstoreAccessKey != "" || p.BlobstoreSecretKey != "" {
		credsPath := blobstorePath + "/creds"

		credsConfig := map[string]interface{}{
			"access_key": p.BlobstoreAccessKey,
			"secret_key": p.BlobstoreSecretKey,
		}

		err = p.Safe.SetMultiple(credsPath, credsConfig)
		if err != nil {
			return fmt.Errorf("failed to set blobstore credentials: %w", err)
		}
	}

	return nil
}

// ConfigureDatabases configures database settings.
func (p *PVEVaultProvider) ConfigureDatabases(_envPath, envType string, reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
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
func (p *PVEVaultProvider) ConfigureLoadBalancers(_envPath, envType string, reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
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
func (p *PVEVaultProvider) ConfigureFQDNs(_envPath, envType string, reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
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
func (p *PVEVaultProvider) ConfigureCertificates(_envPath, _envType string, reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
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
func (p *PVEVaultProvider) ConfigurePublicIPs(reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
	phaseName := PhasePublicIPs
	phaseStart := time.Now()

	if reporter != nil {
		reporter.ReportPhaseStart(phaseName, phaseNum, totalPhases)
	}

	p.logger.Info("Configuring public IPs")

	publicIPPath := p.PathBuilder.GetPublicIPsPath()

	// Write a pending marker so downstream consumers can detect unconfigured state
	// with a single key check ("status" == "pending"). PVE has no IaaS-managed
	// floating IPs; the operator must allocate them externally. The "provider"
	// key records that fact without conflicting with the AWS status semantics.
	publicIPConfig := map[string]interface{}{
		"status":   PublicIPStatusPending,
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
func (p *PVEVaultProvider) ConfigureIAAS(envPath, envType string, reporter providers.ProgressReporter, phaseNum *int, totalPhases int) error {
	// IAAS configuration is handled by ConfigureNetworks for Proxmox VE
	return p.ConfigureNetworks(envPath, envType, reporter, *phaseNum, totalPhases)
}

// GetProviderName returns the provider name.
func (p *PVEVaultProvider) GetProviderName() string {
	return "pve"
}

// configureEnvironment configures vault paths for a specific environment type.
func (p *PVEVaultProvider) configureEnvironment(envType string, reporter providers.ProgressReporter, phaseIndex *int, totalPhases int) error {
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

	// Configure blobstores (external for Proxmox; skipped when BlobstoreEndpoint is empty)
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

	// Configure CPI credentials for this environment
	err = p.configureCPI(envType)
	if err != nil {
		return fmt.Errorf("failed to configure CPI for %s: %w", envType, err)
	}

	// Configure availability zones (Proxmox nodes as AZ entries)
	err = p.ConfigureAZs(envType)
	if err != nil {
		return fmt.Errorf("failed to configure AZs for %s: %w", envType, err)
	}

	return nil
}

// configureCPI writes Proxmox VE CPI credentials to vault.
//
// Path: secret/config/{bloc}/{envType}/cpi/pve
//
// Auth mode selection (mutually exclusive):
//   - API token auth: Config.AuthToken (token_id) + Config.TokenSecret (token_secret) both set.
//     Writes token_id and token_secret. Does NOT write username or password.
//   - User/password auth: Config.Username + Config.Password both set, AuthToken empty.
//     Writes username and password. Does NOT write token_id or token_secret.
//
// Common fields written in both modes:
//   - host   — PVE API endpoint (Config.APIEndpoint)
//   - node   — Primary Proxmox node name (Config.Region)
//   - status — literal "configured"
//
// Returns an error when host is empty, or when neither auth mode is fully configured.
// Config.Password is never aliased as token_secret; use Config.TokenSecret explicitly.
func (p *PVEVaultProvider) configureCPI(envType string) error {
	p.logger.Infow("Configuring PVE CPI credentials", "env_type", envType)

	host := p.Config.APIEndpoint
	if host == "" {
		return fmt.Errorf("pve configureCPI: api_endpoint (host) is required but not set in config")
	}

	node := p.Config.Region
	cpiPath := p.PathBuilder.GetEnvironmentPath(envType) + "/cpi/pve"

	apiTokenMode := p.Config.AuthToken != "" && p.Config.TokenSecret != ""
	userPassMode := p.Config.Username != "" && p.Config.Password != "" && p.Config.AuthToken == ""

	switch {
	case apiTokenMode:
		// API token auth: write token_id + token_secret only.
		cpiConfig := map[string]interface{}{
			"host":         host,
			"node":         node,
			"token_id":     p.Config.AuthToken,
			"token_secret": p.Config.TokenSecret,
			"status":       "configured",
		}

		if err := p.Safe.SetMultiple(cpiPath, cpiConfig); err != nil {
			return fmt.Errorf("failed to set PVE CPI configuration: %w", err)
		}

	case userPassMode:
		// Username/password auth: write username + password only.
		cpiConfig := map[string]interface{}{
			"host":     host,
			"node":     node,
			"username": p.Config.Username,
			"password": p.Config.Password,
			"status":   "configured",
		}

		if err := p.Safe.SetMultiple(cpiPath, cpiConfig); err != nil {
			return fmt.Errorf("failed to set PVE CPI configuration: %w", err)
		}

	default:
		return fmt.Errorf("pve configureCPI: no complete auth configuration found; set (auth_token + token_secret) for API token auth or (username + password) for user/password auth")
	}

	p.logger.Infow("PVE CPI credentials configured", "env_type", envType, "path", cpiPath)

	return nil
}

// ConfigureAZs writes Proxmox node names as availability zone entries.
//
// Path pattern: secret/config/{bloc}/{envType}/net/azs/{node}
//
// Proxmox does not have availability zones in the cloud-provider sense. Each
// Proxmox node in the cluster acts as an independent failure domain. This method
// writes one vault entry per node so that BOSH directors can reference them as
// AZ cloud properties.
//
// Node list source (in priority order):
//  1. Config.Nodes — iterated when len > 0; one vault write per node.
//  2. Config.Region — fallback single-node when Nodes is empty.
//  3. Both empty — logs a warning and returns nil (no error).
func (p *PVEVaultProvider) ConfigureAZs(envType string) error {
	p.logger.Infow("Configuring PVE AZs (nodes as AZ entries)", "env_type", envType)

	// Multi-node: iterate Config.Nodes when set; single-node: fall back to Config.Region.
	switch {
	case len(p.Config.Nodes) > 0:
		for _, node := range p.Config.Nodes {
			azPath := p.PathBuilder.GetAZPath(envType, node)

			azData := map[string]interface{}{
				"node_name": node,
				"status":    "configured",
			}

			if err := p.Safe.SetMultiple(azPath, azData); err != nil {
				return fmt.Errorf("failed to set AZ entry for node %s: %w", node, err)
			}

			p.logger.Infow("PVE AZ entry configured", "env_type", envType, "node", node, "path", azPath)
		}

	case p.Config.Region != "":
		node := p.Config.Region
		azPath := p.PathBuilder.GetAZPath(envType, node)

		azData := map[string]interface{}{
			"node_name": node,
			"status":    "configured",
		}

		if err := p.Safe.SetMultiple(azPath, azData); err != nil {
			return fmt.Errorf("failed to set AZ entry for node %s: %w", node, err)
		}

		p.logger.Infow("PVE AZ entry configured", "env_type", envType, "node", node, "path", azPath)

	default:
		p.logger.Warnw("No nodes configured (Nodes slice and Region are both empty), skipping AZ configuration", "env_type", envType)
	}

	return nil
}

// configureSharedComponents configures shared vault paths.
func (p *PVEVaultProvider) configureSharedComponents(reporter providers.ProgressReporter, phaseIndex *int, totalPhases int) error {
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
