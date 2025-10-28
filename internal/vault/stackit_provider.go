package vault

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	stackitcpi "github.com/ocfp/ocfp-cli-go/internal/cpi/stackit"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/providers"
	"github.com/ocfp/ocfp-cli-go/internal/state"
	"go.uber.org/zap"
)

const (
	// Network constants.
	NetworkPartsCount = 4
	NetworkPrefix     = 3
	LastOctet         = 254

	// IP allocation offsets (used by AWS provider).
	JumpboxOffset    = 5
	CFRouterOffset   = 10
	DiegoCellOffset  = 20
	DiegoCell1Offset = 21

	// Common ports.
	HTTPPort         = 80
	HTTPSPort        = 443
	SSHAltPort       = 2222
	HighPort         = 1024
	AlertmanagerPort = 9093
	GrafanaPort      = 3000

	// CIDR validation.
	CIDRPartsCount = 2
)

// StackitVaultProvider implements vault operations for STACKIT.
type StackitVaultProvider struct {
	*providers.BaseVaultProvider

	Safe        SafeInterface
	PathBuilder *PathBuilder
	logger      *zap.SugaredLogger
}

// NewStackitVaultProvider creates a new STACKIT vault provider.
func NewStackitVaultProvider(cfg *config.Config, safe SafeInterface, blocName string) *StackitVaultProvider {
	return &StackitVaultProvider{
		BaseVaultProvider: providers.NewBaseVaultProvider(cfg, blocName),
		Safe:              safe,
		PathBuilder:       NewPathBuilder(cfg, blocName),
		logger:            logger.Get(),
	}
}

// Configure performs full vault configuration for STACKIT.
func (s *StackitVaultProvider) Configure() error {
	s.logger.Infow("Starting STACKIT vault configuration", "bloc", s.BlocName)

	// Save OCFP configuration to vault first
	err := s.SaveConfigToVault()
	if err != nil {
		return fmt.Errorf("failed to save config to vault: %w", err)
	}

	// Configure both management and OCF environments
	for _, envType := range []string{"mgmt", "ocf"} {
		//nolint:noinlineerr // error is returned directly from configureEnvironment
		if err := s.configureEnvironment(envType); err != nil {
			return err
		}
	}

	// Configure shared components
	err = s.configureSharedComponents()
	if err != nil {
		return err
	}

	s.logger.Infow("STACKIT vault configuration completed", "bloc", s.BlocName)

	return nil
}

// ConfigureBlobstores configures blobstore settings.
func (s *StackitVaultProvider) ConfigureBlobstores(envPath, envType string) error {
	s.logger.Infow("Configuring blobstores", "env_type", envType)

	// STACKIT uses S3-compatible object storage
	// Configure blobstores for different systems
	systems := []string{"bosh"}
	if envType == OCFEnvType {
		systems = append(systems, "cf")
	}

	for _, system := range systems {
		systemBlobstores := s.getBlobstoresForSystem(system, envType)
		for blobstoreName, blobstoreConfig := range systemBlobstores {
			blobstorePath := s.PathBuilder.GetSystemBlobstorePath(envType, system, blobstoreName)

			err := s.Safe.SetMultiple(blobstorePath, blobstoreConfig)
			if err != nil {
				return fmt.Errorf("failed to set blobstore %s for %s: %w", blobstoreName, system, err)
			}
		}
	}

	return nil
}

// ConfigureCertificates configures TLS certificates.
func (s *StackitVaultProvider) ConfigureCertificates(envPath, envType string) error {
	s.logger.Info("Configuring certificates")

	// Certificate configuration for STACKIT
	// This is typically handled by Let's Encrypt or other certificate providers
	certsPath := s.PathBuilder.GetCertsPath()

	certConfig := map[string]interface{}{
		"provider": "letsencrypt",
		"region":   s.Config.Region,
		"domains":  s.Config.FQDNs,
	}

	err := s.Safe.SetMultiple(certsPath, certConfig)
	if err != nil {
		return fmt.Errorf("failed to set certificate configuration: %w", err)
	}

	return nil
}

// ConfigureDatabases configures database settings.
func (s *StackitVaultProvider) ConfigureDatabases(envPath, envType string) error {
	s.logger.Infow("Configuring databases", "env_type", envType)

	// STACKIT database configuration - simplified
	databases := s.getDatabasesForEnv(envType)

	for dbName, dbConfig := range databases {
		dbPath := s.PathBuilder.GetDatabasePath(envType, dbName)

		err := s.Safe.SetMultiple(dbPath, dbConfig)
		if err != nil {
			return fmt.Errorf("failed to set database %s: %w", dbName, err)
		}
	}

	return nil
}

// ConfigureFQDNs configures fully qualified domain names.
func (s *StackitVaultProvider) ConfigureFQDNs(envPath, envType string) error {
	s.logger.Infow("Configuring FQDNs", "env_type", envType)

	// Get FQDNs from configuration
	if s.Config.FQDNs == nil {
		s.logger.Info("No FQDNs configured")

		return nil
	}

	envFQDNs, exists := s.Config.FQDNs[envType]
	if !exists {
		s.logger.Infow("No FQDNs configured for environment", "env_type", envType)

		return nil
	}

	fqdnPath := s.PathBuilder.GetFQDNsPath(envType)

	data, ok := envFQDNs.(map[string]interface{})
	if !ok {
		return ErrInvalidFQDNsConfigType(envType, envFQDNs)
	}

	// Make a copy to avoid modifying the original config
	fqdns := make(map[string]interface{})
	for k, v := range data {
		fqdns[k] = v
	}

	// For mgmt environment, skip CF-related FQDNs
	if envType == MgmtEnvType {
		for system := range fqdns {
			if s.shouldSkipCFForEnvType(envType, system) {
				delete(fqdns, system)
				s.logger.Debugw("Skipped CF-related FQDN for mgmt environment", "system", system)
			}
		}
	}

	// For OCF environment, ensure shield FQDN exists (generate if missing)
	if envType == OCFEnvType {
		if _, exists := fqdns["shield"]; !exists {
			// Generate default shield FQDN
			// Try DomainName from config (Go equivalent of base_domain/system_domain)
			baseDomain := s.Config.DomainName
			if baseDomain == "" {
				baseDomain = "example.com"
			}
			fqdns["shield"] = fmt.Sprintf("shield.%s", baseDomain)
			s.logger.Infow("Added default shield FQDN for OCF environment", "fqdn", fqdns["shield"])
		}
	}

	// Only write to vault if we have FQDNs to write
	if len(fqdns) > 0 {
		err := s.Safe.SetMultiple(fqdnPath, fqdns)
		if err != nil {
			return fmt.Errorf("failed to set FQDNs: %w", err)
		}
	}

	return nil
}

// shouldSkipCFForEnvType determines if a CF-related system should be skipped for the given environment type.
// This matches the Perl implementation in OCFP::CPI::STACKIT::Vault::should_skip_cf_for_env_type (lines 29-50).
func (s *StackitVaultProvider) shouldSkipCFForEnvType(envType, system string) bool {
	// Only skip CF systems for mgmt environment
	if envType != MgmtEnvType {
		return false
	}

	// CF-specific systems that should be skipped in mgmt
	cfSystems := map[string]bool{
		"cf":               true,
		"cloud_controller": true,
		"api":              true,
		"uaa":              true,
		"diego":            true,
		"credhub":          true,
		"loggregator":      true,
		"router":           true,
		"doppler":          true,
		"log-api":          true,
		"syslog-scheduler": true,
	}

	// Direct match
	if cfSystems[system] {
		return true
	}

	// Pattern match: cf- prefix or -cf suffix (with - or _ separator)
	if strings.HasPrefix(system, "cf-") || strings.HasPrefix(system, "cf_") ||
		strings.HasSuffix(system, "-cf") || strings.HasSuffix(system, "_cf") {
		return true
	}

	return false
}

// ConfigureIAAS configures IaaS-specific settings.
func (s *StackitVaultProvider) ConfigureIAAS(envPath, envType string) error {
	s.logger.Infow("Configuring IaaS components", "env_type", envType)

	// Configure network
	err := s.configureNetwork(envType)
	if err != nil {
		return fmt.Errorf("failed to configure network: %w", err)
	}

	// Configure subnets
	err = s.configureSubnets(envType)
	if err != nil {
		return fmt.Errorf("failed to configure subnets: %w", err)
	}

	// Configure security groups
	err = s.configureSecurityGroups(envType)
	if err != nil {
		return fmt.Errorf("failed to configure security groups: %w", err)
	}

	// Configure region
	err = s.configureRegion(envType)
	if err != nil {
		return fmt.Errorf("failed to configure region: %w", err)
	}

	return nil
}

// ConfigureLoadBalancers configures load balancer settings.
func (s *StackitVaultProvider) ConfigureLoadBalancers(envPath, envType string) error {
	s.logger.Infow("Configuring load balancers", "env_type", envType)

	// Export service targets backed by reserved IPs (STACKIT parity) for both envs
	services := s.buildLBServiceTargetsFromState()
	if len(services) > 0 {
		for serviceName, cfg := range services {
			svcPath := s.PathBuilder.GetLoadBalancerPath(envType, serviceName)

			err := s.Safe.SetMultiple(svcPath, cfg)
			if err != nil {
				return fmt.Errorf("failed to set LB service %s: %w", serviceName, err)
			}
		}
	}

	return nil
}

// ConfigurePublicIPs configures public IP addresses by querying the STACKIT API.
// This matches the Perl implementation in OCFP::CPI::STACKIT::Vault::configure_public_ips (lines 499-533).
func (s *StackitVaultProvider) ConfigurePublicIPs() error {
	s.logger.Info("Configuring public IPs for bloc", "bloc", s.BlocName)

	// Get STACKIT CPI client
	client, err := s.getStackitClient()
	if err != nil {
		s.logger.Warnw("Failed to get STACKIT client, skipping public IPs configuration", "error", err)
		return nil
	}

	// Fetch all public IPs from STACKIT API
	allIPs, err := s.fetchAllPublicIPs(client)
	if err != nil {
		s.logger.Warnw("Failed to fetch public IPs from API, skipping", "error", err)
		return nil
	}

	if len(allIPs) == 0 {
		s.logger.Info("No public IPs found in STACKIT API")
		return nil
	}

	s.logger.Infow("Found public IPs from API", "total_count", len(allIPs))

	// Filter IPs by bloc and managed-by label
	blocIPs := s.filterBlocIPs(allIPs)
	if len(blocIPs) == 0 {
		s.logger.Infow("No public IPs found for bloc", "bloc", s.BlocName)
		return nil
	}

	s.logger.Infow("Filtered public IPs for bloc", "bloc", s.BlocName, "count", len(blocIPs))

	// Group IPs by job type
	ipsByJob := s.groupIPsByJob(blocIPs)

	// Prepare vault data for mgmt and ocf environments
	mgmtVaultData, ocfVaultData := s.preparePublicIPVaultData(ipsByJob)

	// Store IPs in vault
	err = s.storePublicIPsInVault(mgmtVaultData, ocfVaultData)
	if err != nil {
		return err
	}

	// Display summary
	s.displayPublicIPSummary(mgmtVaultData, ocfVaultData)

	return nil
}

// getStackitClient retrieves or creates a STACKIT CPI client.
func (s *StackitVaultProvider) getStackitClient() (cpi.NetworkManager, error) {
	// Create STACKIT config from OCFP config
	stackitConfig := &stackitcpi.Config{
		ProjectID:           s.Config.ProjectID,
		OrgID:               s.Config.OrgID,
		AuthToken:           s.Config.AuthToken,
		ServiceAccountToken: s.Config.ServiceAccountToken,
		ServiceAccountJSON:  s.Config.ServiceAccountJSON,
		Region:              s.Config.Region,
		BaseURL:             s.Config.APIEndpoint,
	}

	// Create STACKIT client
	client, err := stackitcpi.NewClient(stackitConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create STACKIT client: %w", err)
	}

	// Initialize client
	ctx := context.Background()
	err = client.Initialize(ctx, stackitConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize STACKIT client: %w", err)
	}

	return client.Network(), nil
}

// fetchAllPublicIPs queries the STACKIT API for all public IPs in the project.
// This matches the Perl implementation in _fetch_all_public_ips (lines 535-554).
func (s *StackitVaultProvider) fetchAllPublicIPs(networkManager cpi.NetworkManager) ([]*cpi.PublicIP, error) {
	ctx := context.Background()

	// Call ListPublicIPs to get all IPs
	publicIPs, err := networkManager.ListPublicIPs(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list public IPs: %w", err)
	}

	return publicIPs, nil
}

// filterBlocIPs filters public IPs by bloc name and managed-by=ocfp label.
// This matches the Perl implementation in _filter_bloc_ips (lines 556-573).
func (s *StackitVaultProvider) filterBlocIPs(allIPs []*cpi.PublicIP) []*cpi.PublicIP {
	blocIPs := make([]*cpi.PublicIP, 0)

	for _, ip := range allIPs {
		if ip.Labels == nil {
			continue
		}

		// Skip if not managed by OCFP
		managedBy, hasManagedBy := ip.Labels["managed-by"]
		if !hasManagedBy || managedBy != "ocfp" {
			continue
		}

		// Skip if belongs to different bloc
		bloc, hasBloc := ip.Labels["bloc"]
		if !hasBloc || bloc != s.BlocName {
			continue
		}

		blocIPs = append(blocIPs, ip)
	}

	return blocIPs
}

// groupIPsByJob groups public IPs by job type from labels.
// This matches the Perl implementation in _group_ips_by_job (lines 575-595).
func (s *StackitVaultProvider) groupIPsByJob(blocIPs []*cpi.PublicIP) map[string][]*cpi.PublicIP {
	ipsByJob := make(map[string][]*cpi.PublicIP)

	for _, ip := range blocIPs {
		// Get job from labels or use "unknown"
		job := "unknown"
		if ip.Labels != nil {
			if jobLabel, exists := ip.Labels["job"]; exists {
				job = jobLabel
			}
		}

		// Use Job field if available (preferred)
		if ip.Job != "" {
			job = ip.Job
		}

		ipsByJob[job] = append(ipsByJob[job], ip)
	}

	return ipsByJob
}

// jobTypeMapping defines the mapping of job types to vault key prefixes and environments.
// This matches the Perl implementation %JOB_TYPE_MAP (lines 629-637).
var jobTypeMapping = map[string]struct {
	prefix      string
	environment string
}{
	"bastion":    {prefix: "bastion_", environment: "mgmt"},
	"ops":        {prefix: "ops_", environment: "mgmt"},
	"router":     {prefix: "cf_router_", environment: "ocf"},
	"tcp-router": {prefix: "cf_tcp_router_", environment: "ocf"},
	"jumpbox":    {prefix: "jumpbox_", environment: "mgmt"},
}

// preparePublicIPVaultData prepares vault data for mgmt and ocf environments.
// This matches the Perl implementation in _prepare_vault_data (lines 597-626).
func (s *StackitVaultProvider) preparePublicIPVaultData(ipsByJob map[string][]*cpi.PublicIP) (map[string]interface{}, map[string]interface{}) {
	mgmtVaultData := make(map[string]interface{})
	ocfVaultData := make(map[string]interface{})

	// Process each job type
	for job, ips := range ipsByJob {
		// Sort by index (if available)
		sortedIPs := s.sortIPsByIndex(ips)

		// Determine vault key and environment for each IP
		for _, ip := range sortedIPs {
			key, environment := s.determineVaultKeyAndEnvironment(job, ip.Index)

			// Store in appropriate environment
			if environment == "mgmt" {
				mgmtVaultData[key] = ip.IPAddress
				s.logger.Infow("Prepared mgmt IP", "key", key, "ip", ip.IPAddress)
			} else {
				ocfVaultData[key] = ip.IPAddress
				s.logger.Infow("Prepared ocf IP", "key", key, "ip", ip.IPAddress)
			}
		}
	}

	return mgmtVaultData, ocfVaultData
}

// sortIPsByIndex sorts IPs by their index field.
func (s *StackitVaultProvider) sortIPsByIndex(ips []*cpi.PublicIP) []*cpi.PublicIP {
	// Create a copy to avoid modifying original
	sorted := make([]*cpi.PublicIP, len(ips))
	copy(sorted, ips)

	// Simple bubble sort by index (small arrays, simplicity over efficiency)
	for i := 0; i < len(sorted)-1; i++ {
		for j := 0; j < len(sorted)-i-1; j++ {
			// Compare indices (treat empty as "unknown")
			idx1 := sorted[j].Index
			idx2 := sorted[j+1].Index

			if idx1 == "" {
				idx1 = "unknown"
			}

			if idx2 == "" {
				idx2 = "unknown"
			}

			if idx1 > idx2 {
				sorted[j], sorted[j+1] = sorted[j+1], sorted[j]
			}
		}
	}

	return sorted
}

// determineVaultKeyAndEnvironment determines the vault key and environment for a job and index.
// This matches the Perl implementation in _determine_vault_key_and_env (lines 639-659).
func (s *StackitVaultProvider) determineVaultKeyAndEnvironment(job, index string) (string, string) {
	// Use index or "unknown" if not set
	if index == "" {
		index = "unknown"
	}

	// Check if job type is in the mapping
	if mapping, exists := jobTypeMapping[job]; exists {
		key := mapping.prefix + index
		environment := mapping.environment
		return key, environment
	}

	// Unknown job type - determine environment based on prefix
	s.logger.Warnw("Unknown job type for public IP", "job", job)

	key := fmt.Sprintf("%s_%s", job, index)

	// If it starts with cf_, put it in ocf, otherwise mgmt
	environment := "mgmt"
	if strings.HasPrefix(job, "cf_") || strings.HasPrefix(job, "cf-") {
		environment = "ocf"
	}

	return key, environment
}

// storePublicIPsInVault stores public IPs in vault at appropriate paths.
// This matches the Perl implementation in _store_ips_in_vault (lines 661-687).
func (s *StackitVaultProvider) storePublicIPsInVault(mgmtVaultData, ocfVaultData map[string]interface{}) error {
	// Store management IPs in vault
	if len(mgmtVaultData) > 0 {
		mgmtPath := fmt.Sprintf("secret/config/%s/mgmt/public-ips", s.BlocName)
		s.logger.Infow("Storing management public IPs in vault",
			"count", len(mgmtVaultData),
			"path", mgmtPath)

		err := s.Safe.SetMultiple(mgmtPath, mgmtVaultData)
		if err != nil {
			return fmt.Errorf("failed to store management public IPs: %w", err)
		}

		s.logger.Info("Successfully stored management public IPs in vault")
	}

	// Store OCF IPs in vault
	if len(ocfVaultData) > 0 {
		ocfPath := fmt.Sprintf("secret/config/%s/ocf/public-ips", s.BlocName)
		s.logger.Infow("Storing OCF public IPs in vault",
			"count", len(ocfVaultData),
			"path", ocfPath)

		err := s.Safe.SetMultiple(ocfPath, ocfVaultData)
		if err != nil {
			return fmt.Errorf("failed to store OCF public IPs: %w", err)
		}

		s.logger.Info("Successfully stored OCF public IPs in vault")
	}

	return nil
}

// displayPublicIPSummary displays a summary of configured public IPs.
// This matches the Perl implementation in _display_ip_summary (lines 689-703).
func (s *StackitVaultProvider) displayPublicIPSummary(mgmtVaultData, ocfVaultData map[string]interface{}) {
	if len(mgmtVaultData) == 0 && len(ocfVaultData) == 0 {
		s.logger.Warnw("No public IPs configured for bloc", "bloc", s.BlocName)
		return
	}

	s.logger.Info("Public IPs summary:")

	// Count by job type
	bastionCount := s.countKeysWithPrefix(mgmtVaultData, "bastion_")
	opsCount := s.countKeysWithPrefix(mgmtVaultData, "ops_")
	jumpboxCount := s.countKeysWithPrefix(mgmtVaultData, "jumpbox_")
	routerCount := s.countKeysWithPrefix(ocfVaultData, "cf_router_")
	tcpRouterCount := s.countKeysWithPrefix(ocfVaultData, "cf_tcp_router_")

	s.logger.Infow("  bastion IPs", "count", bastionCount)
	s.logger.Infow("  ops IPs", "count", opsCount)
	s.logger.Infow("  jumpbox IPs", "count", jumpboxCount)
	s.logger.Infow("  cf_router IPs", "count", routerCount)
	s.logger.Infow("  cf_tcp_router IPs", "count", tcpRouterCount)
}

// countKeysWithPrefix counts keys in a map that start with a given prefix.
func (s *StackitVaultProvider) countKeysWithPrefix(data map[string]interface{}, prefix string) int {
	count := 0
	for key := range data {
		if strings.HasPrefix(key, prefix) {
			count++
		}
	}
	return count
}

// GetProviderName returns the provider name.
func (s *StackitVaultProvider) GetProviderName() string {
	return "stackit"
}

// SaveConfigToVault saves the OCFP configuration to vault.
// Format: Base64(gzip(JSON)) - matches Perl implementation for compatibility.
func (s *StackitVaultProvider) SaveConfigToVault() error {
	s.logger.Info("Saving OCFP configuration to vault")

	// Convert config to JSON
	jsonConfig, err := json.Marshal(s.Config) //nolint:musttag // Config struct has json tags
	if err != nil {
		return fmt.Errorf("failed to marshal config to JSON: %w", err)
	}

	// Compress with gzip (level 9 - maximum compression) to match Perl implementation
	var compressedBuf bytes.Buffer
	gzipWriter, err := gzip.NewWriterLevel(&compressedBuf, gzip.BestCompression)
	if err != nil {
		return fmt.Errorf("failed to create gzip writer: %w", err)
	}

	_, err = gzipWriter.Write(jsonConfig)
	if err != nil {
		gzipWriter.Close()
		return fmt.Errorf("failed to write to gzip: %w", err)
	}

	err = gzipWriter.Close()
	if err != nil {
		return fmt.Errorf("failed to close gzip writer: %w", err)
	}

	// Base64 encode the compressed data
	encoded := base64.StdEncoding.EncodeToString(compressedBuf.Bytes())

	// Save to vault at secret/config/{bloc}/ocfp:config
	configPath := s.PathBuilder.GetOCFPConfigPath()

	err = s.Safe.Set(configPath, "config", encoded)
	if err != nil {
		return fmt.Errorf("failed to save config to vault: %w", err)
	}

	s.logger.Infow("OCFP configuration saved to vault", "path", configPath)

	return nil
}

// configureEnvironment configures a single environment (mgmt or ocf).
func (s *StackitVaultProvider) configureEnvironment(envType string) error {
	s.logger.Infow("Configuring environment", "env_type", envType)

	envPath := s.PathBuilder.GetEnvironmentPath(envType)

	// Configure IaaS components
	err := s.ConfigureIAAS(envPath, envType)
	if err != nil {
		return fmt.Errorf("failed to configure IaaS for %s: %w", envType, err)
	}

	// Configure services
	err = s.configureServices(envPath, envType)
	if err != nil {
		return err
	}

	// Configure environment-specific components
	err = s.configureEnvironmentComponents(envType)
	if err != nil {
		return err
	}

	return nil
}

// configureServices configures all service components for an environment.
func (s *StackitVaultProvider) configureServices(envPath, envType string) error {
	err := s.ConfigureBlobstores(envPath, envType)
	if err != nil {
		return fmt.Errorf("failed to configure blobstores for %s: %w", envType, err)
	}

	err = s.ConfigureDatabases(envPath, envType)
	if err != nil {
		return fmt.Errorf("failed to configure databases for %s: %w", envType, err)
	}

	err = s.ConfigureLoadBalancers(envPath, envType)
	if err != nil {
		return fmt.Errorf("failed to configure load balancers for %s: %w", envType, err)
	}

	err = s.ConfigureFQDNs(envPath, envType)
	if err != nil {
		return fmt.Errorf("failed to configure FQDNs for %s: %w", envType, err)
	}

	return nil
}

// configureEnvironmentComponents configures environment-specific components.
func (s *StackitVaultProvider) configureEnvironmentComponents(envType string) error {
	// Configure Shield admin credentials
	err := s.configureShield(envType)
	if err != nil {
		return fmt.Errorf("failed to configure Shield for %s: %w", envType, err)
	}

	// Configure CPI settings
	err = s.configureCPI(envType)
	if err != nil {
		return fmt.Errorf("failed to configure CPI for %s: %w", envType, err)
	}

	// Configure policies
	err = s.configurePolicies(envType)
	if err != nil {
		return fmt.Errorf("failed to configure policies for %s: %w", envType, err)
	}

	// Configure users (mgmt only)
	err = s.configureUsers(envType)
	if err != nil {
		return fmt.Errorf("failed to configure users for %s: %w", envType, err)
	}

	// Configure BOSH-specific components
	err = s.configureBOSH(envType)
	if err != nil {
		return fmt.Errorf("failed to configure BOSH for %s: %w", envType, err)
	}

	// Configure BOSH metadata
	err = s.configureBOSHMeta(envType)
	if err != nil {
		return fmt.Errorf("failed to configure BOSH meta for %s: %w", envType, err)
	}

	return nil
}

// configureSharedComponents configures components shared between environments.
func (s *StackitVaultProvider) configureSharedComponents() error {
	// Configure certificates (shared between environments)
	err := s.ConfigureCertificates("", "")
	if err != nil {
		return fmt.Errorf("failed to configure certificates: %w", err)
	}

	// Configure public IPs (OCF environment only)
	err = s.ConfigurePublicIPs()
	if err != nil {
		return fmt.Errorf("failed to configure public IPs: %w", err)
	}

	return nil
}

// getDatabasesForEnv returns database configuration for an environment.
func (s *StackitVaultProvider) getDatabasesForEnv(envType string) map[string]map[string]interface{} {
	// StackIT-specific hostname formatter
	hostnameFormatter := func(env string) string {
		switch env {
		case MgmtEnvType:
			return fmt.Sprintf("postgres-%s-mgmt.%s", s.BlocName, s.Config.Region)
		case OCFEnvType:
			return fmt.Sprintf("postgres-%s-cf.%s", s.BlocName, s.Config.Region)
		default:
			return ""
		}
	}

	return BuildDatabasesForEnv(envType, hostnameFormatter)
}

// buildTargetsFromIPs builds targets from a list of IPs.
func (b *lbServiceBuilder) buildTargetsFromIPs(ips []string, prefix string) []map[string]interface{} {
	targets := make([]map[string]interface{}, 0, len(ips))
	for i, ip := range ips {
		targets = append(targets, map[string]interface{}{
			"ip":   ip,
			"name": fmt.Sprintf("%s-%d", prefix, i),
		})
	}

	return targets
}

// configureBOSH configures BOSH-specific components.
func (s *StackitVaultProvider) configureBOSH(envType string) error {
	s.logger.Infow("Configuring BOSH components", "env_type", envType)

	// Configure IAM
	err := s.configureIAM(envType)
	if err != nil {
		return fmt.Errorf("failed to configure IAM: %w", err)
	}

	// Configure keys
	err = s.configureKeys(envType)
	if err != nil {
		return fmt.Errorf("failed to configure keys: %w", err)
	}

	// Configure KMS (no-op for STACKIT)
	s.configureKMS()

	return nil
}

// configureIAM configures IAM credentials.
func (s *StackitVaultProvider) configureIAM(envType string) error {
	// For STACKIT, we use service account credentials for S3-compatible storage
	s3Path := s.PathBuilder.GetS3Path(envType)

	// Use service account token or key from config
	accessKey := s.Config.ServiceAccountToken
	secretKey := s.Config.ServiceAccountToken // Simplified - would be different in real implementation

	if accessKey == "" {
		s.logger.Warn("No service account credentials found for S3 access")

		return nil
	}

	s3Data := map[string]interface{}{
		"access_key": accessKey,
		"secret_key": secretKey,
	}

	err := s.Safe.SetMultiple(s3Path, s3Data)
	if err != nil {
		return fmt.Errorf("failed to set S3 IAM credentials: %w", err)
	}

	return nil
}

// configureKMS configures KMS settings (no-op for STACKIT).
func (s *StackitVaultProvider) configureKMS() {
	// STACKIT doesn't have a native KMS service like AWS
	// This is a no-op but we'll log it
	s.logger.Info("KMS configuration not applicable for STACKIT")
}

// configureKeys configures SSH keys.
func (s *StackitVaultProvider) configureKeys(envType string) error {
	// For STACKIT, we configure the bastion keypair for BOSH
	boshKeyPath := s.PathBuilder.GetBOSHKeyPath(envType)

	keypairName := s.BlocName + "-bastion"

	// Initialize key data with required fields
	keyData := map[string]interface{}{
		"keypair_name": keypairName,
	}

	// Try to get keypair information from state
	stateManager := s.loadStateManager()
	if stateManager != nil {
		// Look for keypair resource in state
		keypairs, err := stateManager.ListResources("keypair")
		if err == nil && len(keypairs) > 0 {
			// Use the first keypair (should be the bastion keypair)
			keypair := keypairs[0]

			// Set ID from state
			if keypair.ID != "" {
				keyData["id"] = keypair.ID
			}

			// Set fingerprint from keypair properties
			if fingerprint, ok := keypair.Properties["fingerprint"].(string); ok && fingerprint != "" {
				keyData["fingerprint"] = fingerprint
			}

			// Set type from keypair properties or default to ssh-rsa
			if keyType, ok := keypair.Properties["type"].(string); ok && keyType != "" {
				keyData["type"] = keyType
			} else {
				keyData["type"] = "ssh-rsa"
			}
		}
	}

	// Set defaults if not found in state
	if _, exists := keyData["id"]; !exists {
		keyData["id"] = s.BlocName + "-bosh-key"
	}

	if _, exists := keyData["fingerprint"]; !exists {
		keyData["fingerprint"] = ""
	}

	if _, exists := keyData["type"]; !exists {
		keyData["type"] = "ssh-rsa"
	}

	// Determine private key path
	// Try standard locations
	privateKeyPath := s.getPrivateKeyPath(keypairName)
	if privateKeyPath != "" {
		keyData["private_key_path"] = privateKeyPath

		// Read private key content
		privateKeyContent, err := s.readPrivateKeyContent(privateKeyPath)
		if err == nil && privateKeyContent != "" {
			keyData["private"] = privateKeyContent
		} else {
			// If we can't read it, store error indicator
			keyData["private"] = "PRIVATE_KEY_READ_ERROR"
			s.logger.Warnw("Failed to read private key content", "path", privateKeyPath, "error", err)
		}
	} else {
		// No private key path found - store reference to key name
		keyData["private"] = keypairName
	}

	err := s.Safe.SetMultiple(boshKeyPath, keyData)
	if err != nil {
		return fmt.Errorf("failed to set BOSH key data: %w", err)
	}

	return nil
}

// getPrivateKeyPath attempts to find the private key path for the given keypair name.
func (s *StackitVaultProvider) getPrivateKeyPath(keypairName string) string {
	// Try standard OCFP location first
	homeDir := os.Getenv("HOME")
	if homeDir == "" {
		return ""
	}

	// Try Ed25519 key (preferred)
	ed25519Path := filepath.Join(homeDir, ".ocfp", s.BlocName, "ssh", "id_ed25519")
	if _, err := os.Stat(ed25519Path); err == nil {
		return ed25519Path
	}

	// Try RSA key
	rsaPath := filepath.Join(homeDir, ".ocfp", s.BlocName, "ssh", "id_rsa")
	if _, err := os.Stat(rsaPath); err == nil {
		return rsaPath
	}

	// Try standard .ssh directory with bloc name
	sshPath := filepath.Join(homeDir, ".ssh", keypairName)
	if _, err := os.Stat(sshPath); err == nil {
		return sshPath
	}

	return ""
}

// readPrivateKeyContent reads the private key content from the given path.
func (s *StackitVaultProvider) readPrivateKeyContent(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("failed to read private key: %w", err)
	}

	return string(content), nil
}

// configureRegion configures region settings.
func (s *StackitVaultProvider) configureRegion(envType string) error {
	netPath := s.PathBuilder.GetNetPath(envType)
	regionData := map[string]interface{}{
		"region": s.Config.Region,
	}

	err := s.Safe.SetMultiple(netPath, regionData)
	if err != nil {
		return fmt.Errorf("failed to set region data: %w", err)
	}

	return nil
}

// configureSecurityGroups configures security group settings.
// This matches the Perl implementation in OCFP::CPI::STACKIT::Vault::configure_sgs (lines 1543-1697).
func (s *StackitVaultProvider) configureSecurityGroups(envType string) error {
	s.logger.Infow("Configuring security groups", "env_type", envType)

	// Build security group mapping (Perl: _build_sg_mapping, lines 1582-1612)
	sgMapping := s.buildSecurityGroupMapping()

	// Get network path for vault storage
	netPath := s.PathBuilder.GetNetPath(envType)

	// Load state manager to check for security group data
	stateManager := s.loadStateManager()

	// Process each security group type
	for sgType, sgFullName := range sgMapping {
		// Try to find the security group from state or other sources
		sg := s.findSecurityGroup(stateManager, sgType, sgFullName)

		// Store in vault if found
		if sg != nil {
			err := s.storeSecurityGroupToVault(sg, sgType, sgFullName, netPath)
			if err != nil {
				// Log error but continue with other security groups
				s.logger.Warnw("Failed to store security group",
					"type", sgType,
					"name", sgFullName,
					"error", err)
				continue
			}
			s.logger.Infow("Stored security group",
				"type", sgType,
				"name", sgFullName,
				"id", sg["id"])
		} else {
			// Security group not found - skip with debug message
			s.logger.Debugw("Security group not found - skipping",
				"type", sgType,
				"name", sgFullName)
		}
	}

	return nil
}

// buildSecurityGroupMapping builds the mapping of security group types to full names.
// This matches the Perl implementation in _build_sg_mapping (lines 1582-1612).
func (s *StackitVaultProvider) buildSecurityGroupMapping() map[string]string {
	// Define standard security group types (Perl: lines 1586-1594)
	sgTypes := []string{
		"bastion",
		"infra",
		"ocfp",
		"lb-ext",
		"ocf-cf-router-ingress",
		"ocf-cf-tcp-router-ingress",
		"ocf-cf-ssh-proxy-ingress",
	}

	// Create mapping from type to full name
	sgMapping := make(map[string]string)
	for _, sgType := range sgTypes {
		// Map type to full prefixed name: {bloc}-{type}
		fullName := fmt.Sprintf("%s-%s", s.BlocName, sgType)
		sgMapping[sgType] = fullName
	}

	// Add default security group (Perl: lines 1608-1609)
	sgMapping["default"] = "default"

	return sgMapping
}

// findSecurityGroup attempts to find a security group from state manager or provider data.
// This matches the Perl implementation in _find_security_group (lines 1614-1645).
func (s *StackitVaultProvider) findSecurityGroup(stateManager *state.Manager, sgType, sgFullName string) map[string]interface{} {
	// First try to get from state manager
	if stateManager != nil {
		// Look for security group resources in state
		resources, err := stateManager.ListResources("security_group")
		if err == nil && len(resources) > 0 {
			// Find matching security group by name
			for _, resource := range resources {
				// Check if name matches
				if name, ok := resource.Properties["name"].(string); ok && name == sgFullName {
					// Found matching security group
					sg := make(map[string]interface{})
					sg["id"] = resource.ID
					sg["name"] = name

					// Add description if available
					if desc, ok := resource.Properties["description"].(string); ok {
						sg["description"] = desc
					} else {
						sg["description"] = fmt.Sprintf("Security group for %s", sgType)
					}

					s.logger.Debugw("Found security group in state",
						"type", sgType,
						"name", sgFullName,
						"id", resource.ID)

					return sg
				}
			}
		}

		// Also try to get from outputs (for provider data compatibility)
		sgKey := fmt.Sprintf("security_group_%s_id", sgType)
		if sgID, err := stateManager.GetOutput(sgKey); err == nil {
			if id, ok := sgID.(string); ok && id != "" {
				sg := make(map[string]interface{})
				sg["id"] = id
				sg["name"] = sgFullName
				sg["description"] = fmt.Sprintf("Security group for %s", sgType)

				s.logger.Debugw("Found security group in state outputs",
					"type", sgType,
					"name", sgFullName,
					"id", id)

				return sg
			}
		}
	}

	// Security group not found
	return nil
}

// storeSecurityGroupToVault stores a security group to vault with proper path handling.
// This matches the Perl implementation in _store_sg_to_vault (lines 1667-1697).
//
// CRITICAL: CF-specific security groups are stored directly under net/ path,
// NOT under net/sgs/, for deployment compatibility (Perl: lines 1676-1685).
func (s *StackitVaultProvider) storeSecurityGroupToVault(sg map[string]interface{}, sgType, sgFullName, netPath string) error {
	// Get security group ID
	sgID, ok := sg["id"].(string)
	if !ok || sgID == "" {
		return fmt.Errorf("security group missing ID")
	}

	// Build security group data
	sgData := map[string]interface{}{
		"id":   sgID,
		"name": sgFullName,
	}

	// Add description if available
	if desc, ok := sg["description"].(string); ok && desc != "" {
		sgData["description"] = desc
	} else {
		sgData["description"] = fmt.Sprintf("Security group for %s", sgType)
	}

	// Determine vault path based on security group type
	// CRITICAL PATH LOGIC (Perl: lines 1676-1696):
	// - CF-specific groups (starting with "cf-" or "ocf-cf-"): store at {net_path}/{sg_type}
	// - Standard groups: store at {net_path}/sgs/{sg_type}
	var vaultPath string
	if strings.HasPrefix(sgType, "cf-") || strings.HasPrefix(sgType, "ocf-cf-") {
		// CF-specific: Store directly under net/ path (NOT in sgs/)
		vaultPath = fmt.Sprintf("%s/%s", netPath, sgType)
		s.logger.Debugw("Using CF-specific path (directly under net/)",
			"type", sgType,
			"path", vaultPath)
	} else {
		// Standard: Store under net/sgs/
		vaultPath = fmt.Sprintf("%s/sgs/%s", netPath, sgType)
		s.logger.Debugw("Using standard path (under net/sgs/)",
			"type", sgType,
			"path", vaultPath)
	}

	// Store in vault
	err := s.Safe.SetMultiple(vaultPath, sgData)
	if err != nil {
		return fmt.Errorf("failed to set security group data: %w", err)
	}

	s.logger.Infow("Saved security group to vault",
		"name", sgFullName,
		"id", sgID,
		"path", vaultPath)

	return nil
}

// configureSubnetReservedIPs configures reserved IP addresses for a subnet.
// This matches the Perl implementation in OCFP::CPI::STACKIT::Vault::configure_subnet_reserved_ips (lines 1197-1279).
func (s *StackitVaultProvider) configureSubnetReservedIPs(cidr, subnetType string, subnetNum int, envType string) error {
	// Skip non-ocfp subnets (only process ocfp subnets)
	if subnetType != "ocfp" {
		return nil
	}

	subnetName := fmt.Sprintf("%s-%d", subnetType, subnetNum)
	s.logger.Infow("Configuring reserved IPs", "subnet", subnetName)

	// Get default IP assignments for this environment
	assignments := getDefaultReservedIPAssignments()

	// Calculate IPs based on CIDR and assignments
	vaultIPs, err := s.calculateReservedIPs(cidr, assignments, envType, subnetNum)
	if err != nil {
		return fmt.Errorf("failed to calculate reserved IPs: %w", err)
	}

	// Write to vault
	reservedPath := s.PathBuilder.GetReservedIPsPath(envType, subnetType, subnetNum)
	s.logger.Debugw("Writing reserved IPs to vault",
		"path", reservedPath,
		"count", len(vaultIPs))

	err = s.Safe.SetMultiple(reservedPath, vaultIPs)
	if err != nil {
		return fmt.Errorf("failed to set reserved IPs: %w", err)
	}

	s.logger.Infow("Successfully configured reserved IPs",
		"subnet", subnetName,
		"count", len(vaultIPs))

	return nil
}

// reservedIPAssignment represents an IP allocation configuration.
type reservedIPAssignment struct {
	// Simple offset for single IP
	Offset int

	// Environment-specific subnet assignments: offset -> [subnet_nums]
	SubnetMapping map[int][]int

	// Range specification (e.g., "11-29" or "0-10,30->")
	RangeSpec string

	// Environment-specific range specs
	SubnetRanges map[string][]int // "range-spec" -> [subnet_nums]
}

// getDefaultReservedIPAssignments returns the default IP assignment map.
// This matches the Perl implementation in default_reserved_ip_assignments (lines 2339-2394).
func getDefaultReservedIPAssignments() map[string]map[string]*reservedIPAssignment {
	return map[string]map[string]*reservedIPAssignment{
		"bosh": {
			"mgmt": {
				SubnetMapping: map[int][]int{4: {0}},
			},
			"ocf": {
				SubnetMapping: map[int][]int{31: {0}},
			},
			"other": {
				Offset: 10,
			},
		},
		"vault": {
			"mgmt": {Offset: 5},
			"ocf":  {Offset: 32},
		},
		"jumpbox": {
			"mgmt": {Offset: 6},
			"ocf":  {Offset: 33},
		},
		"concourse": {
			"mgmt": {Offset: 7},
			"ocf":  {Offset: 34},
		},
		"prometheus": {
			"mgmt": {Offset: 8},
			"ocf":  {Offset: 35},
		},
		"shield": {
			"mgmt": {
				SubnetMapping: map[int][]int{9: {0}},
			},
			"ocf": {
				SubnetMapping: map[int][]int{36: {0}},
			},
		},
		"doomsday": {
			"mgmt": {
				SubnetMapping: map[int][]int{9: {1}},
			},
		},
		"ocfp_ui": {
			"mgmt": {
				SubnetMapping: map[int][]int{9: {2}},
			},
			"ocf": {
				SubnetMapping: map[int][]int{36: {2}},
			},
		},
		"bastion": {
			"mgmt": {
				SubnetMapping: map[int][]int{3: {0}},
			},
			"ocf": {
				SubnetMapping: map[int][]int{37: {0}},
			},
			"other": {
				Offset: 3,
			},
		},
		"blacksmith": {
			"mgmt": {
				SubnetMapping: map[int][]int{10: {0}},
			},
			"ocf": {
				SubnetMapping: map[int][]int{36: {1}},
			},
			"other": {
				Offset: 10,
			},
		},
		"available": {
			"mgmt": {
				RangeSpec: "11-29",
			},
			"ocf": {
				SubnetRanges: map[string][]int{
					"38->": {0},
					"37->": {1, 2},
				},
			},
		},
		"reserved": {
			"mgmt": {
				RangeSpec: "0-10,30->",
			},
			"ocf": {
				SubnetRanges: map[string][]int{
					"0-36": {1, 2},
					"0-37": {0},
				},
			},
			"other": {
				RangeSpec: "0-15",
			},
		},
	}
}

// calculateReservedIPs calculates all reserved IPs for a subnet based on assignments.
func (s *StackitVaultProvider) calculateReservedIPs(
	cidr string,
	assignments map[string]map[string]*reservedIPAssignment,
	envType string,
	subnetNum int,
) (map[string]interface{}, error) {
	// Parse CIDR to get base IP
	baseIP, networkBits, err := parseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("invalid CIDR %s: %w", cidr, err)
	}

	vaultIPs := make(map[string]interface{})
	usedIPs := make(map[string]bool)

	// Process each assignment type in sorted order for consistency
	assignmentTypes := make([]string, 0, len(assignments))
	for assignmentType := range assignments {
		assignmentTypes = append(assignmentTypes, assignmentType)
	}

	// Sort for deterministic output
	sortAssignmentTypes(assignmentTypes)

	for _, assignmentType := range assignmentTypes {
		envMap := assignments[assignmentType]

		// Get assignment for this environment (or fallback to "other")
		assignment := envMap[envType]
		if assignment == nil {
			assignment = envMap["other"]
		}

		if assignment == nil {
			continue
		}

		// Process simple offset (single IP)
		if assignment.Offset > 0 {
			ip := addOffsetToIP(baseIP, assignment.Offset)
			if !usedIPs[ip] {
				vaultIPs[assignmentType+"_ip"] = ip
				usedIPs[ip] = true
				s.logger.Debugw("Reserved IP",
					"type", assignmentType,
					"ip", ip,
					"offset", assignment.Offset)
			}
			continue
		}

		// Process subnet mapping (offset -> [subnet_nums])
		if len(assignment.SubnetMapping) > 0 {
			for offset, subnets := range assignment.SubnetMapping {
				// Check if this subnet number is in the list
				if containsInt(subnets, subnetNum) {
					ip := addOffsetToIP(baseIP, offset)
					if !usedIPs[ip] {
						vaultIPs[assignmentType+"_ip"] = ip
						usedIPs[ip] = true
						s.logger.Debugw("Reserved IP from subnet mapping",
							"type", assignmentType,
							"ip", ip,
							"offset", offset,
							"subnet_num", subnetNum)
					}
					break
				}
			}
			continue
		}

		// Process range specification
		if assignment.RangeSpec != "" {
			ranges, err := parseIPRangeSpec(assignment.RangeSpec, baseIP, networkBits)
			if err != nil {
				s.logger.Warnw("Failed to parse range spec",
					"type", assignmentType,
					"spec", assignment.RangeSpec,
					"error", err)
				continue
			}

			// Store range boundaries
			idx := 0
			for _, ipRange := range ranges {
				vaultIPs[fmt.Sprintf("%s_%d", assignmentType, idx)] = ipRange.Start
				idx++
				vaultIPs[fmt.Sprintf("%s_%d", assignmentType, idx)] = ipRange.End
				idx++

				s.logger.Debugw("Reserved IP range",
					"type", assignmentType,
					"start", ipRange.Start,
					"end", ipRange.End)
			}
			continue
		}

		// Process subnet-specific ranges
		if len(assignment.SubnetRanges) > 0 {
			for rangeSpec, subnets := range assignment.SubnetRanges {
				// Check if this subnet number is in the list
				if containsInt(subnets, subnetNum) {
					ranges, err := parseIPRangeSpec(rangeSpec, baseIP, networkBits)
					if err != nil {
						s.logger.Warnw("Failed to parse subnet range spec",
							"type", assignmentType,
							"spec", rangeSpec,
							"error", err)
						continue
					}

					// Store range boundaries
					idx := 0
					for _, ipRange := range ranges {
						vaultIPs[fmt.Sprintf("%s_%d", assignmentType, idx)] = ipRange.Start
						idx++
						vaultIPs[fmt.Sprintf("%s_%d", assignmentType, idx)] = ipRange.End
						idx++

						s.logger.Debugw("Reserved IP range from subnet mapping",
							"type", assignmentType,
							"start", ipRange.Start,
							"end", ipRange.End,
							"subnet_num", subnetNum)
					}
					break
				}
			}
		}
	}

	return vaultIPs, nil
}

// ipRange represents an IP address range.
type ipRange struct {
	Start string
	End   string
}

// parseCIDR parses a CIDR notation and returns base IP and network bits.
func parseCIDR(cidr string) (string, int, error) {
	parts := strings.Split(cidr, "/")
	if len(parts) != 2 {
		return "", 0, fmt.Errorf("invalid CIDR format")
	}

	baseIP := parts[0]
	networkBits := 0

	_, err := fmt.Sscanf(parts[1], "%d", &networkBits)
	if err != nil {
		return "", 0, fmt.Errorf("invalid network bits: %w", err)
	}

	// Validate IP address format
	octets := strings.Split(baseIP, ".")
	if len(octets) != 4 {
		return "", 0, fmt.Errorf("invalid IP address format")
	}

	return baseIP, networkBits, nil
}

// addOffsetToIP adds an offset to an IP address.
func addOffsetToIP(baseIP string, offset int) string {
	octets := strings.Split(baseIP, ".")
	if len(octets) != 4 {
		return baseIP
	}

	// Convert last octet to int
	lastOctet, err := strconv.Atoi(octets[3])
	if err != nil {
		return baseIP
	}

	// Add offset
	newOctet := lastOctet + offset

	// Handle overflow into third octet if needed
	if newOctet > 255 {
		thirdOctet, err := strconv.Atoi(octets[2])
		if err != nil {
			return baseIP
		}
		thirdOctet += newOctet / 256
		newOctet = newOctet % 256
		octets[2] = strconv.Itoa(thirdOctet)
	}

	octets[3] = strconv.Itoa(newOctet)

	return strings.Join(octets, ".")
}

// parseIPRangeSpec parses a range specification like "11-29" or "0-10,30->".
// This matches the Perl implementation in _parse_ip_range_string (lines 1476-1497).
func parseIPRangeSpec(rangeSpec string, baseIP string, networkBits int) ([]ipRange, error) {
	var ranges []ipRange

	// Split by comma for multiple ranges
	subranges := strings.Split(rangeSpec, ",")

	for _, subrange := range subranges {
		subrange = strings.TrimSpace(subrange)

		// Parse range like "11-29" or "30->" or "10"
		var lower, upper int

		if strings.Contains(subrange, "->") {
			// Open-ended range like "30->"
			parts := strings.Split(subrange, "->")
			lower, _ = strconv.Atoi(strings.TrimSpace(parts[0]))
			upper = calculateLastHostOffset(networkBits)
		} else if strings.Contains(subrange, "-") {
			// Closed range like "11-29"
			parts := strings.Split(subrange, "-")
			lower, _ = strconv.Atoi(strings.TrimSpace(parts[0]))
			upper, _ = strconv.Atoi(strings.TrimSpace(parts[1]))
		} else {
			// Single value like "10"
			lower, _ = strconv.Atoi(strings.TrimSpace(subrange))
			upper = lower
		}

		startIP := addOffsetToIP(baseIP, lower)
		endIP := addOffsetToIP(baseIP, upper)

		ranges = append(ranges, ipRange{
			Start: startIP,
			End:   endIP,
		})
	}

	return ranges, nil
}

// calculateLastHostOffset calculates the last usable host offset for a network.
func calculateLastHostOffset(networkBits int) int {
	// Calculate number of hosts: 2^(32-networkBits) - 1
	hostBits := 32 - networkBits
	if hostBits <= 0 {
		return 0
	}

	// For typical /24 network: 2^8 - 1 = 255
	// But we want last usable host (254)
	maxHosts := (1 << uint(hostBits)) - 2

	return maxHosts
}

// containsInt checks if a slice contains an integer.
func containsInt(slice []int, value int) bool {
	for _, v := range slice {
		if v == value {
			return true
		}
	}
	return false
}

// sortAssignmentTypes sorts assignment types for deterministic output.
// Order matches the Perl implementation's processing order.
func sortAssignmentTypes(types []string) {
	// Define priority order matching Perl implementation
	priority := map[string]int{
		"bosh":       1,
		"vault":      2,
		"jumpbox":    3,
		"concourse":  4,
		"prometheus": 5,
		"shield":     6,
		"doomsday":   7,
		"ocfp_ui":    8,
		"bastion":    9,
		"blacksmith": 10,
		"available":  11,
		"reserved":   12,
	}

	// Sort by priority
	for i := 0; i < len(types)-1; i++ {
		for j := i + 1; j < len(types); j++ {
			pri1 := priority[types[i]]
			pri2 := priority[types[j]]

			// If not in priority map, sort alphabetically
			if pri1 == 0 {
				pri1 = 1000
			}
			if pri2 == 0 {
				pri2 = 1000
			}

			if pri1 > pri2 {
				types[i], types[j] = types[j], types[i]
			} else if pri1 == pri2 && types[i] > types[j] {
				// Alphabetical for same priority
				types[i], types[j] = types[j], types[i]
			}
		}
	}
}

// configureSubnets configures subnet settings in vault.
func (s *StackitVaultProvider) configureSubnets(envType string) error {
	s.logger.Infow("Configuring subnets", "env_type", envType)

	subnetsPath := s.PathBuilder.GetSubnetsPath(envType)

	for i, subnet := range s.Config.Subnets {
		err := s.configureSubnet(envType, i, subnet)
		if err != nil {
			return err
		}
	}

	s.logger.Infow("Subnets configuration completed", "path", subnetsPath)

	return nil
}

func (s *StackitVaultProvider) configureSubnet(envType string, subnetNum int, subnet config.Subnet) error {
	subnetType := subnet.Type
	if subnetType == "" {
		subnetType = "ocfp" // Default subnet type
	}

	subnetPath := s.PathBuilder.GetSubnetPath(envType, subnetType, subnetNum)

	networkInfo, err := s.parseSubnetCIDR(subnet.CIDR)
	if err != nil {
		return err
	}

	availabilityZone := s.getAvailabilityZone(subnetNum)

	subnetData := s.buildSubnetData(subnetType, subnetNum, subnet.CIDR, networkInfo, availabilityZone)

	err = s.Safe.SetMultiple(subnetPath, subnetData)
	if err != nil {
		return fmt.Errorf("failed to set subnet data for %s-%d: %w", subnetType, subnetNum, err)
	}

	if subnetType == "ocfp" {
		err := s.configureSubnetReservedIPs(subnet.CIDR, subnetType, subnetNum, envType)
		if err != nil {
			return fmt.Errorf("failed to configure reserved IPs: %w", err)
		}
	}

	return nil
}

type subnetNetworkInfo struct {
	network    string
	cidrPrefix string
	gateway    string
	lastHost   string
}

func (s *StackitVaultProvider) parseSubnetCIDR(cidr string) (*subnetNetworkInfo, error) {
	cidrParts := strings.Split(cidr, "/")
	if len(cidrParts) != CIDRPartsCount {
		return nil, ErrInvalidCIDRFormat(cidr)
	}

	network := cidrParts[0]

	networkParts := strings.Split(network, ".")
	if len(networkParts) != NetworkPartsCount {
		return nil, ErrInvalidNetworkAddress(network)
	}

	cidrPrefix := strings.Join(networkParts[:NetworkPrefix], ".")
	lastOctet, _ := strconv.Atoi(networkParts[3])
	gateway := fmt.Sprintf("%s.%d", cidrPrefix, lastOctet+1)
	lastHost := cidrPrefix + "." + strconv.Itoa(LastOctet)

	return &subnetNetworkInfo{
		network:    network,
		cidrPrefix: cidrPrefix,
		gateway:    gateway,
		lastHost:   lastHost,
	}, nil
}

func (s *StackitVaultProvider) getAvailabilityZone(subnetNum int) string {
	if len(s.Config.AZs) <= subnetNum {
		return ""
	}

	azNames := make([]string, 0, len(s.Config.AZs))
	for name := range s.Config.AZs {
		azNames = append(azNames, name)
	}

	if subnetNum < len(azNames) {
		return azNames[subnetNum]
	}

	return ""
}

func (s *StackitVaultProvider) buildSubnetData(subnetType string, subnetNum int, cidr string, networkInfo *subnetNetworkInfo, availabilityZone string) map[string]interface{} {
	// Get master network CIDR
	netCIDR := s.Config.Network.CIDR

	// Calculate net_prefix (first 3 octets of master network)
	netPrefix := s.calculateNetworkPrefix(netCIDR)

	// Get network_id from state if available
	networkID := s.getNetworkIDFromState()

	// Build base subnet data
	subnetData := map[string]interface{}{
		"id":          fmt.Sprintf("%s-%s-%d", s.BlocName, subnetType, subnetNum),
		"cidr_block":  cidr,
		"cidr_prefix": networkInfo.cidrPrefix,
		"ip_0":        networkInfo.network,
		"ip_n":        networkInfo.lastHost,
		"gateway":     networkInfo.gateway,
		"dns":         s.Config.DNS,
		"az":          availabilityZone,
		"type":        subnetType,

		// New fields for Perl parity
		"subnet_cidr":   cidr,                                        // Virtual subnet's specific CIDR
		"subnet_prefix": networkInfo.cidrPrefix,                      // First 3 octets of subnet CIDR
		"net_cidr":      netCIDR,                                     // Master network CIDR
		"net_prefix":    netPrefix,                                   // First 3 octets of master network
		"name":          fmt.Sprintf("%s-%d", subnetType, subnetNum), // Subnet name: {type}-{num}
		"subnet_num":    subnetNum,                                   // Subnet number within type
		"provider":      "stackit",                                   // Provider name
		"provider_type": "virtual_subnet",                            // Provider type
		"parent_cidr":   cidr,                                        // Same as subnet_cidr
		"environment":   s.BlocName,                                  // Bloc name
		"region":        s.Config.Region,                             // STACKIT region
	}

	// Add network_id if available
	if networkID != "" {
		subnetData["network_id"] = networkID
	}

	// Add 'virtual' flag only for non-reserved subnets
	if subnetType != "reserved" {
		subnetData["virtual"] = "true"
	}

	return subnetData
}

// calculateNetworkPrefix extracts the first 3 octets from a CIDR.
func (s *StackitVaultProvider) calculateNetworkPrefix(cidr string) string {
	// Split CIDR to get network address
	parts := strings.Split(cidr, "/")
	if len(parts) != CIDRPartsCount {
		return ""
	}

	networkAddr := parts[0]
	octets := strings.Split(networkAddr, ".")

	if len(octets) < NetworkPrefix {
		return ""
	}

	// Join first 3 octets
	return strings.Join(octets[:NetworkPrefix], ".")
}

// getNetworkIDFromState retrieves the network_id from state manager.
func (s *StackitVaultProvider) getNetworkIDFromState() string {
	stateManager := s.loadStateManager()
	if stateManager == nil {
		return ""
	}

	networkID, err := stateManager.GetOutput("network_id")
	if err != nil {
		return ""
	}

	if id, ok := networkID.(string); ok {
		return id
	}

	return ""
}

// configureNetwork configures network settings in vault.
func (s *StackitVaultProvider) configureNetwork(envType string) error {
	netPath := s.PathBuilder.GetNetPath(envType)

	// STACKIT network configuration with all required fields matching Perl implementation
	networkData := map[string]interface{}{
		"id":          s.Config.ProjectID, // Use project ID as network identifier
		"cidr_block":  s.Config.Network.CIDR,
		"dns":         s.Config.DNS,
		"region":      s.Config.Region,
		"provider":    "stackit",
		"name":        fmt.Sprintf("%s-net", s.BlocName),
		"ipv4_cidr":   s.Config.Network.CIDR, // IPv4 CIDR (same as cidr_block for STACKIT)
		"project_id":  s.Config.ProjectID,
		"description": fmt.Sprintf("Primary STACKIT network for environment %s", s.BlocName),
	}

	// Try to fetch additional fields from STACKIT API if network ID is available
	networkID := s.getNetworkIDFromState()
	if networkID != "" {
		s.logger.Debugw("Fetching network details from API", "network_id", networkID)

		networkManager, err := s.getStackitClient()
		if err != nil {
			// Log warning but don't fail - gracefully degrade to basic fields
			s.logger.Warnw("Failed to create STACKIT client, using basic network fields only", "error", err)
		} else {
			ctx := context.Background()
			network, err := networkManager.GetNetwork(ctx, networkID)
			if err != nil {
				// Log warning but don't fail - gracefully degrade to basic fields
				s.logger.Warnw("Failed to fetch network details from API, using basic fields only",
					"network_id", networkID, "error", err)
			} else if network != nil {
				// Add status field if available from API
				if network.State != "" {
					networkData["status"] = network.State
					s.logger.Debugw("Added network status from API", "status", network.State)
				}

				// Add created_at field if available from API
				if !network.CreatedAt.IsZero() {
					networkData["created_at"] = network.CreatedAt.Format(time.RFC3339)
					s.logger.Debugw("Added network created_at from API", "created_at", network.CreatedAt)
				}
			}
		}
	}

	err := s.Safe.SetMultiple(netPath, networkData)
	if err != nil {
		return fmt.Errorf("failed to set network data: %w", err)
	}

	// Configure availability zones
	for azName, azData := range s.Config.AZs {
		azPath := s.PathBuilder.GetAZPath(envType, azName)

		// Format cloud_properties as JSON string matching Perl format:
		// { cloud_properties => sprintf( '{"availability_zone": "%s"}', $az_name ) }
		azInfo := map[string]interface{}{
			"cloud_properties": fmt.Sprintf(`{"availability_zone": "%s"}`, azData.Zone),
		}

		err := s.Safe.SetMultiple(azPath, azInfo)
		if err != nil {
			return fmt.Errorf("failed to set AZ data for %s: %w", azName, err)
		}
	}

	s.logger.Infow("Network configuration completed", "path", netPath)

	return nil
}

// getBlobstoresForSystem returns blobstore configuration for a system.
func (s *StackitVaultProvider) getBlobstoresForSystem(system, envType string) map[string]map[string]interface{} {
	blobstores := make(map[string]map[string]interface{})

	switch system {
	case "bosh":
		blobstores["artifacts"] = map[string]interface{}{
			"name":   fmt.Sprintf("%s-%s-bosh-artifacts", s.BlocName, envType),
			"region": s.Config.Region,
			"type":   "s3",
		}
	case "cf":
		cfBlobstores := []string{"buildpacks", "droplets", "packages", "resources"}
		for _, name := range cfBlobstores {
			blobstores[name] = map[string]interface{}{
				"name":   fmt.Sprintf("%s-%s-cf-%s", s.BlocName, envType, name),
				"region": s.Config.Region,
				"type":   "s3",
			}
		}
	}

	return blobstores
}

// (Removed unused getLoadBalancersForEnv helper)

// buildLBServiceTargetsFromState assembles a small set of LB service definitions
// using reserved IPs persisted by bootstrap for STACKIT. Mirrors Perl service mapping.
func (s *StackitVaultProvider) buildLBServiceTargetsFromState() map[string]map[string]interface{} {
	stateManager := s.loadStateManager()
	if stateManager == nil {
		return map[string]map[string]interface{}{}
	}

	builder := &lbServiceBuilder{
		stateManager: stateManager,
		blocName:     s.BlocName,
		services:     make(map[string]map[string]interface{}),
	}

	builder.addOpsHTTPSService()
	builder.addManagementServices()
	builder.addPrometheusSharedServices()
	builder.addRouterServices()
	builder.addTCPRouterService()
	builder.addCFSSHService()

	return builder.services
}

func (s *StackitVaultProvider) loadStateManager() *state.Manager {
	// Get standard state directory for this bloc
	stateDir, err := state.GetStateDir(s.BlocName)
	if err != nil {
		return nil
	}

	stateManager, err := state.NewManager(stateDir)
	if err != nil {
		return nil
	}

	_, err = stateManager.Load(s.BlocName)
	if err != nil {
		return nil
	}

	return stateManager
}

// configureShield configures Shield admin credentials for an environment.
// This matches the Perl implementation in OCFP::CPI::STACKIT::Vault::configure_shield.
func (s *StackitVaultProvider) configureShield(envType string) error {
	s.logger.Infow("Configuring Shield admin credentials", "env_type", envType)

	shieldAdminPath := s.PathBuilder.GetEnvironmentPath(envType) + "/shield/admin"

	// Set default Shield admin credentials
	// In production, these would be generated/retrieved from a secure source
	shieldAdminCreds := map[string]interface{}{
		"username": "shieldadmin",
		"password": fmt.Sprintf("shield-password-%s-%s", envType, s.BlocName),
	}

	err := s.Safe.SetMultiple(shieldAdminPath, shieldAdminCreds)
	if err != nil {
		return fmt.Errorf("failed to set Shield admin credentials: %w", err)
	}

	s.logger.Infow("Successfully configured Shield admin credentials", "path", shieldAdminPath)

	return nil
}

// configureCPI configures STACKIT CPI configuration for an environment.
// This matches the Perl implementation in OCFP::CPI::STACKIT::Vault::configure_cpi.
func (s *StackitVaultProvider) configureCPI(envType string) error {
	s.logger.Infow("Configuring STACKIT CPI credentials", "env_type", envType)

	cpiPath := s.PathBuilder.GetEnvironmentPath(envType) + "/cpi/stackit"

	// Build CPI configuration
	cpiConfig := map[string]interface{}{
		"project_id":              s.Config.ProjectID,
		"org_id":                  s.Config.OrgID,
		"region":                  s.Config.Region,
		"default_region":          s.Config.Region,
		"default_key_name":        s.BlocName + "-bastion",
		"default_security_groups": fmt.Sprintf(`["default","%s-ocfp"]`, s.BlocName),
		"keypair_name":            s.BlocName,
	}

	// Add authentication method - prefer service_account_json, then service_account_token
	// This matches the Perl implementation priority logic (Vault.pm:2245-2257)
	authMethodConfigured := false
	if s.Config.ServiceAccountJSON != "" {
		cpiConfig["service_account_json"] = s.Config.ServiceAccountJSON
		s.logger.Debugw("Using service_account_json for authentication (preferred)")
		authMethodConfigured = true
	} else if s.Config.ServiceAccountToken != "" {
		cpiConfig["service_account_token"] = s.Config.ServiceAccountToken
		s.logger.Debugw("Using service_account_token for authentication")
		authMethodConfigured = true
	}

	// Check for missing required fields
	missingFields := []string{}

	if s.Config.ProjectID == "" {
		missingFields = append(missingFields, "project_id")
	}

	if s.Config.OrgID == "" {
		missingFields = append(missingFields, "org_id")
	}

	if s.Config.Region == "" {
		missingFields = append(missingFields, "region")
	}

	if !authMethodConfigured {
		missingFields = append(missingFields, "service_account_json or service_account_token")
	}

	if len(missingFields) > 0 {
		s.logger.Warnw("Missing required CPI configuration fields", "env_type", envType, "missing", strings.Join(missingFields, ", "))
		s.logger.Infow("CPI configuration may be incomplete", "env_type", envType)
	}

	err := s.Safe.SetMultiple(cpiPath, cpiConfig)
	if err != nil {
		return fmt.Errorf("failed to set CPI configuration: %w", err)
	}

	s.logger.Infow("Successfully configured STACKIT CPI credentials", "env_type", envType)

	return nil
}

// configurePolicies configures deployment policies for an environment.
// This matches the Perl implementation in OCFP::CPI::STACKIT::Vault::configure_policies.
func (s *StackitVaultProvider) configurePolicies(envType string) error {
	s.logger.Infow("Configuring policies", "env_type", envType)

	policiesPath := s.PathBuilder.GetEnvironmentPath(envType) + "/policies"

	// Set default policy values
	userProvidedBoshCreds := "allow" // ignore, allow, require
	deploymentChangeReasonSize := 0

	policies := map[string]interface{}{
		"user_provided_bosh_creds":               userProvidedBoshCreds,
		"deployment_change_reason_required_size": deploymentChangeReasonSize,
	}

	err := s.Safe.SetMultiple(policiesPath, policies)
	if err != nil {
		return fmt.Errorf("failed to set policies: %w", err)
	}

	s.logger.Infow("Configured policies", "env_type", envType,
		"user_provided_bosh_creds", userProvidedBoshCreds,
		"deployment_change_reason_required_size", deploymentChangeReasonSize)

	return nil
}

// configureUsers configures jumpbox users for the management environment.
// This matches the Perl implementation in OCFP::CPI::STACKIT::Vault::configure_users.
//
//nolint:unparam // error return for future implementation; maintains interface consistency
func (s *StackitVaultProvider) configureUsers(envType string) error {
	// Users configuration is only relevant for mgmt environment (jumpbox)
	if envType != "mgmt" {
		return nil
	}

	// Check if users are defined in the configuration
	if s.Config.Users == nil || len(s.Config.Users) == 0 {
		s.logger.Infow("No users configured, skipping jumpbox user configuration")
		return nil
	}

	usersPath := s.PathBuilder.GetJumpboxUsersPath()
	s.logger.Infow("Configuring jumpbox users", "path", usersPath, "user_count", len(s.Config.Users))

	// HTTP client for fetching keys from GitHub/GitLab
	httpClient := &http.Client{
		Timeout: 30 * time.Second,
	}

	// Valid SSH key prefixes for validation
	validSSHKeyPrefixes := []string{
		"ssh-rsa",
		"ssh-ed25519",
		"ecdsa-sha2-nistp256",
		"ecdsa-sha2-nistp384",
		"ecdsa-sha2-nistp521",
	}

	// Process each user
	userCount := 0
	for username, keySpec := range s.Config.Users {
		if keySpec == "" {
			s.logger.Warnw("No SSH key provided for user, skipping", "username", username)
			continue
		}

		var sshKeys string
		var err error

		// Check if this is a GitHub/GitLab lookup
		if strings.HasPrefix(keySpec, "github/") {
			remoteUser := strings.TrimPrefix(keySpec, "github/")
			sshKeys, err = s.fetchSSHKeysFromProvider(httpClient, "github", remoteUser, username, validSSHKeyPrefixes)
			if err != nil {
				s.logger.Warnw("Failed to fetch SSH keys from GitHub, skipping user",
					"username", username,
					"remote_user", remoteUser,
					"error", err)
				continue
			}
		} else if strings.HasPrefix(keySpec, "gitlab/") {
			remoteUser := strings.TrimPrefix(keySpec, "gitlab/")
			sshKeys, err = s.fetchSSHKeysFromProvider(httpClient, "gitlab", remoteUser, username, validSSHKeyPrefixes)
			if err != nil {
				s.logger.Warnw("Failed to fetch SSH keys from GitLab, skipping user",
					"username", username,
					"remote_user", remoteUser,
					"error", err)
				continue
			}
		} else {
			// Direct SSH key - validate format
			if !s.isValidSSHKey(keySpec, validSSHKeyPrefixes) {
				s.logger.Warnw("Invalid SSH key format for user, skipping",
					"username", username)
				continue
			}
			sshKeys = keySpec
		}

		// Store user SSH key(s) in vault
		if sshKeys != "" {
			userData := map[string]interface{}{
				username: sshKeys,
			}
			if err := s.Safe.SetMultiple(usersPath, userData); err != nil {
				s.logger.Errorw("Failed to write user SSH keys to vault",
					"username", username,
					"path", usersPath,
					"error", err)
				return fmt.Errorf("failed to write user %s to vault: %w", username, err)
			}
			s.logger.Infow("Stored SSH key(s) for user", "username", username)
			userCount++
		}
	}

	if userCount > 0 {
		s.logger.Infow("Successfully configured jumpbox users", "count", userCount)
	} else {
		s.logger.Infow("No valid users configured for jumpbox")
	}

	return nil
}

// fetchSSHKeysFromProvider fetches SSH keys from GitHub or GitLab.
func (s *StackitVaultProvider) fetchSSHKeysFromProvider(
	client *http.Client,
	provider string,
	remoteUser string,
	username string,
	validPrefixes []string,
) (string, error) {
	// Construct URL based on provider
	var url string
	switch provider {
	case "github":
		url = fmt.Sprintf("https://github.com/%s.keys", remoteUser)
	case "gitlab":
		url = fmt.Sprintf("https://gitlab.com/%s.keys", remoteUser)
	default:
		return "", fmt.Errorf("unsupported provider: %s", provider)
	}

	s.logger.Infow("Fetching SSH keys from provider",
		"username", username,
		"provider", provider,
		"remote_user", remoteUser)

	// Fetch keys
	resp, err := client.Get(url)
	if err != nil {
		return "", fmt.Errorf("failed to fetch keys from %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP error fetching keys: status %d", resp.StatusCode)
	}

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}

	fetchedKeys := strings.TrimSpace(string(body))
	if fetchedKeys == "" {
		return "", fmt.Errorf("no keys returned from %s", provider)
	}

	// Validate and filter SSH keys
	var validKeys []string
	for _, line := range strings.Split(fetchedKeys, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if s.isValidSSHKey(line, validPrefixes) {
			validKeys = append(validKeys, line)
		}
	}

	if len(validKeys) == 0 {
		return "", fmt.Errorf("no valid SSH keys found for %s user %s", provider, remoteUser)
	}

	s.logger.Infow("Fetched SSH keys from provider",
		"username", username,
		"provider", provider,
		"key_count", len(validKeys))

	return strings.Join(validKeys, "\n"), nil
}

// isValidSSHKey validates that a string is a properly formatted SSH public key.
func (s *StackitVaultProvider) isValidSSHKey(key string, validPrefixes []string) bool {
	key = strings.TrimSpace(key)
	if key == "" {
		return false
	}

	for _, prefix := range validPrefixes {
		if strings.HasPrefix(key, prefix+" ") {
			// Basic validation: must have at least the key type and key data
			parts := strings.Fields(key)
			if len(parts) >= 2 {
				return true
			}
		}
	}
	return false
}

// configureBOSHMeta configures BOSH metadata information.
// This matches the Perl implementation in OCFP::CPI::STACKIT::Vault::configure_bosh_meta.
func (s *StackitVaultProvider) configureBOSHMeta(envType string) error {
	s.logger.Infow("Configuring BOSH meta information", "env_type", envType)

	boshPath := s.PathBuilder.GetBOSHPath(envType)

	// Parse DNS servers from config (defaults to 1.1.1.1)
	dnsNS := "1.1.1.1"
	dnsServers := strings.Split(dnsNS, ",")

	boshMeta := make(map[string]interface{})

	// Store DNS servers as dns.0, dns.1, etc.
	for i, dns := range dnsServers {
		boshMeta[fmt.Sprintf("dns.%d", i)] = strings.TrimSpace(dns)
	}

	// Store key name
	keyName := s.BlocName + "-bastion"
	boshMeta["key_name"] = keyName

	// Store region and availability zone
	boshMeta["region"] = s.Config.Region
	boshMeta["az"] = s.Config.Region + "-1" // Default to first AZ for STACKIT

	// Try to get the private key from the keys/bosh path if it exists
	boshKeysPath := s.PathBuilder.GetBOSHKeyPath(envType)

	boshKeysData, err := s.Safe.GetAll(boshKeysPath)
	if err == nil && boshKeysData != nil {
		if privateKey, ok := boshKeysData["private"]; ok {
			boshMeta["private_key"] = privateKey

			s.logger.Debug("Found and included BOSH private key in meta information")
		}
	}

	// Write to vault at the bosh path
	if len(boshMeta) > 0 {
		err := s.Safe.SetMultiple(boshPath, boshMeta)
		if err != nil {
			return fmt.Errorf("failed to set BOSH meta information: %w", err)
		}

		s.logger.Infow("Successfully configured BOSH meta information", "path", boshPath)
	}

	return nil
}

type lbServiceBuilder struct {
	stateManager *state.Manager
	blocName     string
	services     map[string]map[string]interface{}
}

func (b *lbServiceBuilder) getReservedIP(idx int, key string) string {
	k := fmt.Sprintf("reserved_%s-ocfp-%d_%s", b.blocName, idx, key)

	v, err := b.stateManager.GetOutput(k)
	if err != nil {
		return ""
	}

	if sv, ok := v.(string); ok {
		return sv
	}

	return ""
}

func (b *lbServiceBuilder) addOpsHTTPSService() {
	opsTargets := []map[string]interface{}{}

	for _, key := range []string{"vault_ip", "prometheus_ip", "shield_ip"} {
		if ip := b.getReservedIP(0, key); ip != "" {
			opsTargets = append(opsTargets, map[string]interface{}{"ip": ip, "name": key})
		}
	}

	if ip := b.getReservedIP(1, "doomsday_ip"); ip != "" {
		opsTargets = append(opsTargets, map[string]interface{}{"ip": ip, "name": "doomsday_ip"})
	}

	if len(opsTargets) > 0 {
		b.services["ops-https"] = map[string]interface{}{
			"name":     b.blocName + "-ops-https",
			"protocol": "tcp",
			"port":     HTTPSPort,
			"targets":  opsTargets,
		}
	}
}

func (b *lbServiceBuilder) addManagementServices() {
	services := []struct {
		name string
		idx  int
		key  string
		port int
	}{
		{"doomsday-mgmt", 1, "doomsday_ip", 8000},
		{"concourse-mgmt", 0, "concourse_ip", 8080},
		{"prometheus-mgmt", 0, "prometheus_ip", 9090},
		{"shield-mgmt", 0, "shield_ip", HTTPSPort},
	}

	for _, svc := range services {
		if ip := b.getReservedIP(svc.idx, svc.key); ip != "" {
			b.services[svc.name] = map[string]interface{}{
				"name":     fmt.Sprintf("%s-%s", b.blocName, svc.name),
				"protocol": "tcp",
				"port":     svc.port,
				"targets":  []map[string]interface{}{{"ip": ip, "name": svc.key}},
			}
		}
	}
}

func (b *lbServiceBuilder) addPrometheusSharedServices() {
	prometheusIP := b.getReservedIP(0, "prometheus_ip")
	if prometheusIP == "" {
		return
	}

	sharedServices := []struct {
		name string
		port int
	}{
		{"alertmanager-mgmt", AlertmanagerPort},
		{"grafana-mgmt", GrafanaPort},
	}

	for _, svc := range sharedServices {
		b.services[svc.name] = map[string]interface{}{
			"name":     fmt.Sprintf("%s-%s", b.blocName, svc.name),
			"protocol": "tcp",
			"port":     svc.port,
			"targets":  []map[string]interface{}{{"ip": prometheusIP, "name": "prometheus_ip"}},
		}
	}
}

func (b *lbServiceBuilder) getPublicIPsByJob(job string) []string {
	res, _ := b.stateManager.ListResources("public_ip")
	ips := []string{}

	for _, resource := range res {
		if b.resourceMatchesJob(*resource, job) {
			if addr, ok := resource.Properties["address"].(string); ok && addr != "" {
				ips = append(ips, addr)
			}
		}
	}

	return ips
}

func (b *lbServiceBuilder) resourceMatchesJob(resource state.Resource, job string) bool {
	if resource.Tags != nil && resource.Tags["job"] == job {
		return true
	}

	if j, ok := resource.Properties["job"].(string); ok && j == job {
		return true
	}

	return false
}

func (b *lbServiceBuilder) addRouterServices() {
	ips := b.getPublicIPsByJob("router")
	if len(ips) == 0 {
		return
	}

	targets := b.buildTargetsFromIPs(ips, "router")

	b.services["router-80"] = map[string]interface{}{
		"name":     b.blocName + "-router-80",
		"protocol": "http",
		"port":     HTTPPort,
		"targets":  targets,
	}
	b.services["router-443"] = map[string]interface{}{
		"name":     b.blocName + "-router-443",
		"protocol": "https",
		"port":     HTTPSPort,
		"targets":  targets,
	}
}

func (b *lbServiceBuilder) addTCPRouterService() {
	ips := b.getPublicIPsByJob("tcp-router")
	if len(ips) == 0 {
		return
	}

	targets := b.buildTargetsFromIPs(ips, "tcp-router")

	b.services["tcp-router"] = map[string]interface{}{
		"name":     b.blocName + "-tcp-router",
		"protocol": "tcp",
		"port":     HighPort,
		"targets":  targets,
	}
}

func (b *lbServiceBuilder) addCFSSHService() {
	ips := b.getPublicIPsByJob("cf-ssh")
	if len(ips) == 0 {
		return
	}

	targets := b.buildTargetsFromIPs(ips, "cf-ssh")

	b.services["cf-ssh"] = map[string]interface{}{
		"name":     b.blocName + "-cf-ssh",
		"protocol": "tcp",
		"port":     SSHAltPort,
		"targets":  targets,
	}
}
