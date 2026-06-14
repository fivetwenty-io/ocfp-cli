package vault

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
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
	// NetworkPartsCount is the number of octets in an IPv4 address.
	NetworkPartsCount = 4
	// NetworkPrefix is the number of octets forming the network prefix.
	NetworkPrefix = 3
	// LastOctet is the maximum usable last octet value for IP address generation.
	LastOctet = 254

	// BroadcastAndNetworkAddrs is the count of non-host addresses in a subnet
	// (network address + broadcast address), used to compute the last usable host.
	BroadcastAndNetworkAddrs = 2

	// JumpboxOffset is the IP offset for jumpbox allocation within a subnet.
	JumpboxOffset = 5
	// BoshIPOffset is the IP offset for BOSH director allocation within a subnet.
	BoshIPOffset = 6
	// CFRouterOffset is the IP offset for Cloud Foundry router allocation.
	CFRouterOffset = 10
	// CFRouter1Offset is the IP offset for the second Cloud Foundry router.
	CFRouter1Offset = 11
	// DiegoCellOffset is the IP offset for the first Diego cell.
	DiegoCellOffset = 20
	// DiegoCell1Offset is the IP offset for the second Diego cell.
	DiegoCell1Offset = 21

	// HTTPPort is the standard HTTP port number.
	HTTPPort = 80
	// HTTPSPort is the standard HTTPS port number.
	HTTPSPort = 443
	// SSHAltPort is the alternate SSH port used by Cloud Foundry.
	SSHAltPort = 2222
	// HighPort is the lower bound for ephemeral port ranges.
	HighPort = 1024
	// AlertmanagerPort is the default Prometheus Alertmanager port.
	AlertmanagerPort = 9093
	// GrafanaPort is the default Grafana dashboard port.
	GrafanaPort = 3000

	// CIDRPartsCount is the expected number of parts when splitting a CIDR notation string.
	CIDRPartsCount = 2

	// unknownValue is the placeholder for unknown job or index values.
	unknownValue = "unknown"
)

// STACKIT vault provider errors.
var (
	ErrSecurityGroupMissingID = errors.New("security group missing ID")
	ErrInvalidIPAddressFormat = errors.New("invalid IP address format")
	ErrNetworkNotFound        = errors.New("network not found in STACKIT project")
	ErrHTTPErrorFetchingKeys  = errors.New("HTTP error fetching keys")
	ErrNoKeysReturned         = errors.New("no keys returned from provider")
	ErrNoValidSSHKeys         = errors.New("no valid SSH keys found")
)

// NetworkManagerFactory is a function that creates a NetworkManager.
// It can be overridden in tests to inject mock implementations.
type NetworkManagerFactory func(cfg *config.Config) (cpi.NetworkManager, error)

// StackitVaultProvider implements vault operations for STACKIT.
type StackitVaultProvider struct {
	*providers.BaseVaultProvider

	Safe                  SafeInterface
	PathBuilder           *PathBuilder
	logger                *zap.SugaredLogger
	networkManagerFactory NetworkManagerFactory
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
func (s *StackitVaultProvider) Configure(reporter providers.ProgressReporter) error {
	s.logger.Infow("Starting STACKIT vault configuration", "bloc", s.BlocName)

	// Track phase numbers across entire configuration (0-based for ReportPhaseStart)
	phaseIndex := 0

	// Total phases: 1 (config) + 7 (mgmt: networks, subnets, security-groups, blobstores, databases, load-balancers, fqdns)
	//               + 7 (ocf: same) + 2 (shared: certificates, public-ips) = 17
	totalPhases := 17

	// Save OCFP configuration to vault first (phase 1)
	err := s.SaveConfigToVault(reporter, phaseIndex, totalPhases)
	if err != nil {
		return fmt.Errorf("failed to save config to vault: %w", err)
	}

	phaseIndex++

	// Configure both management and OCF environments
	for _, envType := range []string{MgmtEnvType, OCFEnvType} {
		//nolint:noinlineerr // error is returned directly from configureEnvironment
		if err := s.configureEnvironment(envType, reporter, &phaseIndex, totalPhases); err != nil {
			return err
		}
	}

	// Configure shared components
	err = s.configureSharedComponents(reporter, &phaseIndex, totalPhases)
	if err != nil {
		return err
	}

	// Report final summary
	if reporter != nil {
		reporter.ReportFinalSummary(true, 0, totalPhases, 0)
	}

	s.logger.Infow("STACKIT vault configuration completed", "bloc", s.BlocName)

	return nil
}

// ConfigureBlobstores configures blobstore settings.
func (s *StackitVaultProvider) ConfigureBlobstores(_envPath, envType string, reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
	phaseName := "blobstores-" + envType
	phaseStart := time.Now()

	if reporter != nil {
		reporter.ReportPhaseStart(phaseName, phaseNum, totalPhases)
	}

	s.logger.Infow("Configuring blobstores", "env_type", envType)

	// STACKIT uses S3-compatible object storage
	// Configure blobstores for different systems
	systems := []string{boshSystem}
	if envType == OCFEnvType {
		systems = append(systems, "cf")
	}

	totalBlobstores := 0

	for _, system := range systems {
		systemBlobstores := s.getBlobstoresForSystem(system, envType)
		totalBlobstores += len(systemBlobstores)
	}

	currentBlobstore := 0

	for _, system := range systems {
		systemBlobstores := s.getBlobstoresForSystem(system, envType)
		for blobstoreName, blobstoreConfig := range systemBlobstores {
			currentBlobstore++

			if reporter != nil {
				label := fmt.Sprintf("Writing %s/%s", system, blobstoreName)
				reporter.ReportSubtaskProgress(phaseName, currentBlobstore, totalBlobstores, label)
			}

			blobstorePath := s.PathBuilder.GetSystemBlobstorePath(envType, system, blobstoreName)

			err := s.Safe.SetMultiple(blobstorePath, blobstoreConfig)
			if err != nil {
				return fmt.Errorf("failed to set blobstore %s for %s: %w", blobstoreName, system, err)
			}
		}
	}

	if reporter != nil {
		reporter.ReportPhaseComplete(phaseName, time.Since(phaseStart))
	}

	return nil
}

// ConfigureCertificates configures TLS certificates.
func (s *StackitVaultProvider) ConfigureCertificates(_envPath, _envType string, reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
	phaseName := PhaseCertificates
	phaseStart := time.Now()

	if reporter != nil {
		reporter.ReportPhaseStart(phaseName, phaseNum, totalPhases)
	}

	s.logger.Info("Configuring certificates")

	if reporter != nil {
		reporter.ReportSubtaskProgress(phaseName, 1, 1, "Writing certificate configuration")
	}

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

	if reporter != nil {
		reporter.ReportPhaseComplete(phaseName, time.Since(phaseStart))
	}

	return nil
}

// ConfigureDatabases configures database settings.
func (s *StackitVaultProvider) ConfigureDatabases(_envPath, envType string, reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
	phaseName := "databases-" + envType
	phaseStart := time.Now()

	if reporter != nil {
		reporter.ReportPhaseStart(phaseName, phaseNum, totalPhases)
	}

	s.logger.Infow("Configuring databases", "env_type", envType)

	// STACKIT database configuration - simplified
	databases := s.getDatabasesForEnv(envType)

	totalDatabases := len(databases)
	currentDatabase := 0

	for dbName, dbConfig := range databases {
		currentDatabase++

		if reporter != nil {
			label := "Writing " + dbName
			reporter.ReportSubtaskProgress(phaseName, currentDatabase, totalDatabases, label)
		}

		dbPath := s.PathBuilder.GetDatabasePath(envType, dbName)

		err := s.Safe.SetMultiple(dbPath, dbConfig)
		if err != nil {
			return fmt.Errorf("failed to set database %s: %w", dbName, err)
		}
	}

	if reporter != nil {
		reporter.ReportPhaseComplete(phaseName, time.Since(phaseStart))
	}

	return nil
}

// ConfigureFQDNs configures fully qualified domain names.
// It supports a base FQDN that is used to derive service FQDNs when not explicitly set.
// The base FQDN is stored at a shared path, while environment-specific FQDNs are stored
// under their respective environment paths.
func (s *StackitVaultProvider) ConfigureFQDNs(_envPath, envType string, reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
	phaseName := "fqdns-" + envType
	phaseStart := time.Now()

	reportPhaseStart(reporter, phaseName, phaseNum, totalPhases)

	s.logger.Infow("Configuring FQDNs", "env_type", envType)

	fqdnConfig := s.Config.FQDNs
	if fqdnConfig == nil {
		s.logger.Info("No FQDNs configured")
		reportPhaseComplete(reporter, phaseName, phaseStart)

		return nil
	}

	// Store base FQDN at shared path (only for mgmt, the first env type processed)
	err := s.storeBaseFQDN(fqdnConfig, envType)
	if err != nil {
		return err
	}

	explicit := s.getExplicitFQDNsForEnv(fqdnConfig, envType)
	base := s.resolveEffectiveBase(fqdnConfig)

	if base == "" && len(explicit) == 0 {
		s.logger.Infow("No base FQDN or explicit FQDNs configured for environment", "env_type", envType)
		reportPhaseComplete(reporter, phaseName, phaseStart)

		return nil
	}

	fqdns := PopulateFQDNsForEnv(envType, explicit, base, config.CloudflareEnabled(s.Config.Cloudflare))
	s.filterCFFQDNsForMgmt(envType, fqdns)

	if len(fqdns) > 0 {
		reportSubtask(reporter, phaseName, 1, 1, "Writing FQDNs configuration")

		setErr := s.Safe.SetMultiple(s.PathBuilder.GetFQDNsPath(envType), fqdns)
		if setErr != nil {
			return fmt.Errorf("failed to set FQDNs: %w", setErr)
		}

		s.logger.Infow("Stored FQDNs for environment", "env_type", envType, "count", len(fqdns))
	}

	reportPhaseComplete(reporter, phaseName, phaseStart)

	return nil
}

// reportPhaseStart reports phase start if the reporter is non-nil.
func reportPhaseStart(reporter providers.ProgressReporter, phaseName string, phaseNum, totalPhases int) {
	if reporter != nil {
		reporter.ReportPhaseStart(phaseName, phaseNum, totalPhases)
	}
}

// reportPhaseComplete reports phase completion if the reporter is non-nil.
func reportPhaseComplete(reporter providers.ProgressReporter, phaseName string, phaseStart time.Time) {
	if reporter != nil {
		reporter.ReportPhaseComplete(phaseName, time.Since(phaseStart))
	}
}

// reportSubtask reports subtask progress if the reporter is non-nil.
func reportSubtask(reporter providers.ProgressReporter, phaseName string, current, total int, label string) {
	if reporter != nil {
		reporter.ReportSubtaskProgress(phaseName, current, total, label)
	}
}

// ConfigureIAAS configures IaaS-specific settings.
func (s *StackitVaultProvider) ConfigureIAAS(_envPath, envType string, reporter providers.ProgressReporter, phaseNum *int, totalPhases int) error {
	s.logger.Infow("Configuring IaaS components", "env_type", envType)

	// Configure network (phase)
	err := s.configureNetwork(envType, reporter, *phaseNum, totalPhases)
	if err != nil {
		return fmt.Errorf("failed to configure network: %w", err)
	}

	*phaseNum++

	// Configure subnets (phase)
	err = s.configureSubnets(envType, reporter, *phaseNum, totalPhases)
	if err != nil {
		return fmt.Errorf("failed to configure subnets: %w", err)
	}

	*phaseNum++

	// Configure security groups (phase)
	err = s.configureSecurityGroups(envType, reporter, *phaseNum, totalPhases)
	if err != nil {
		return fmt.Errorf("failed to configure security groups: %w", err)
	}

	*phaseNum++

	// Configure region (not a separate phase, internal operation)
	err = s.configureRegion(envType)
	if err != nil {
		return fmt.Errorf("failed to configure region: %w", err)
	}

	return nil
}

// ConfigureLoadBalancers configures load balancer settings.
func (s *StackitVaultProvider) ConfigureLoadBalancers(_envPath, envType string, reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
	phaseName := "load-balancers-" + envType
	phaseStart := time.Now()

	if reporter != nil {
		reporter.ReportPhaseStart(phaseName, phaseNum, totalPhases)
	}

	s.logger.Infow("Configuring load balancers", "env_type", envType)

	// Export service targets backed by reserved IPs (STACKIT parity) for both envs
	services := s.buildLBServiceTargetsFromState()
	if len(services) > 0 {
		totalServices := len(services)
		currentService := 0

		for serviceName, cfg := range services {
			currentService++

			if reporter != nil {
				label := "Writing " + serviceName
				reporter.ReportSubtaskProgress(phaseName, currentService, totalServices, label)
			}

			svcPath := s.PathBuilder.GetLoadBalancerPath(envType, serviceName)

			err := s.Safe.SetMultiple(svcPath, cfg)
			if err != nil {
				return fmt.Errorf("failed to set LB service %s: %w", serviceName, err)
			}
		}
	}

	if reporter != nil {
		reporter.ReportPhaseComplete(phaseName, time.Since(phaseStart))
	}

	return nil
}

// ConfigurePublicIPs configures public IP addresses by querying the STACKIT API.
// This matches the Perl implementation in OCFP::CPI::STACKIT::Vault::configure_public_ips (lines 499-533).
func (s *StackitVaultProvider) ConfigurePublicIPs(reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
	phaseName := PhasePublicIPs
	phaseStart := time.Now()

	reportPhaseStart(reporter, phaseName, phaseNum, totalPhases)

	s.logger.Info("Configuring public IPs for bloc", "bloc", s.BlocName)

	reportSubtask(reporter, phaseName, 1, 5, "Initializing STACKIT client") //nolint:mnd

	client, err := s.getStackitClient()
	if err != nil {
		s.logger.Warnw("Failed to get STACKIT client, skipping public IPs configuration", "error", err)
		reportPhaseComplete(reporter, phaseName, phaseStart)

		return nil
	}

	reportSubtask(reporter, phaseName, 2, 5, "Fetching public IPs from API") //nolint:mnd

	blocIPs, done := s.fetchAndFilterBlocIPs(client, reporter, phaseName, phaseStart)
	if done {
		return nil
	}

	reportSubtask(reporter, phaseName, 4, 5, "Grouping IPs by job type") //nolint:mnd

	ipsByJob := s.groupIPsByJob(blocIPs)
	mgmtVaultData, ocfVaultData := s.preparePublicIPVaultData(ipsByJob)

	reportSubtask(reporter, phaseName, 5, 5, "Writing IPs to vault") //nolint:mnd

	err = s.storePublicIPsInVault(mgmtVaultData, ocfVaultData)
	if err != nil {
		return err
	}

	s.displayPublicIPSummary(mgmtVaultData, ocfVaultData)
	reportPhaseComplete(reporter, phaseName, phaseStart)

	return nil
}

// jobTypeMapping defines the mapping of job types to vault key prefixes and environments.
// This matches the Perl implementation %JOB_TYPE_MAP (lines 629-637).
//
//nolint:gochecknoglobals // package-level lookup table for job type mapping
var jobTypeMapping = map[string]struct {
	prefix      string
	environment string
}{
	"bastion":    {prefix: "bastion_", environment: MgmtEnvType},
	"ops":        {prefix: "ops_", environment: MgmtEnvType},
	"router":     {prefix: "router_", environment: OCFEnvType},
	"tcp-router": {prefix: "tcp-router_", environment: OCFEnvType},
	"jumpbox":    {prefix: "jumpbox_", environment: MgmtEnvType},
}

// GetProviderName returns the provider name.
func (s *StackitVaultProvider) GetProviderName() string {
	return "stackit"
}

// SaveConfigToVault saves the OCFP configuration to vault.
// Format: Base64(gzip(JSON)) - matches Perl implementation for compatibility.
func (s *StackitVaultProvider) SaveConfigToVault(reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
	phaseName := PhaseConfig
	phaseStart := time.Now()

	if reporter != nil {
		reporter.ReportPhaseStart(phaseName, phaseNum, totalPhases)
	}

	s.logger.Info("Saving OCFP configuration to vault")

	// Convert config to JSON
	if reporter != nil {
		reporter.ReportSubtaskProgress(phaseName, 1, 1, "Writing OCFP configuration")
	}

	jsonConfig, err := json.Marshal(s.Config) //nolint:musttag,gosec // Config has json tags; G117: intentional secret serialization to vault
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
		_ = gzipWriter.Close()

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

	if reporter != nil {
		reporter.ReportPhaseComplete(phaseName, time.Since(phaseStart))
	}

	return nil
}

// storeBaseFQDN stores the base FQDN at the shared path if configured and this is the mgmt env.
func (s *StackitVaultProvider) storeBaseFQDN(fqdnConfig *config.FQDNConfig, envType string) error {
	if fqdnConfig.Base == "" || envType != MgmtEnvType {
		return nil
	}

	basePath := s.PathBuilder.GetBaseFQDNPath()

	err := s.Safe.Set(basePath, "value", fqdnConfig.Base)
	if err != nil {
		return fmt.Errorf("failed to set base FQDN: %w", err)
	}

	s.logger.Infow("Stored base FQDN", "path", basePath, "base", fqdnConfig.Base)

	return nil
}

// getExplicitFQDNsForEnv returns the explicitly configured FQDNs for the given environment type.
func (s *StackitVaultProvider) getExplicitFQDNsForEnv(fqdnConfig *config.FQDNConfig, envType string) map[string]string {
	switch envType {
	case MgmtEnvType:
		return fqdnConfig.Mgmt
	case OCFEnvType:
		return fqdnConfig.OCF
	default:
		return nil
	}
}

// resolveEffectiveBase returns the effective base FQDN, falling back to DomainName.
func (s *StackitVaultProvider) resolveEffectiveBase(fqdnConfig *config.FQDNConfig) string {
	if fqdnConfig.Base != "" {
		return fqdnConfig.Base
	}

	return s.Config.DomainName
}

// filterCFFQDNsForMgmt removes CF-related FQDNs from the map for the mgmt environment.
func (s *StackitVaultProvider) filterCFFQDNsForMgmt(envType string, fqdns map[string]interface{}) {
	if envType != MgmtEnvType {
		return
	}

	for system := range fqdns {
		if s.shouldSkipCFForEnvType(envType, system) {
			delete(fqdns, system)
			s.logger.Debugw("Skipped CF-related FQDN for mgmt environment", "system", system)
		}
	}
}

// fetchAndFilterBlocIPs fetches all public IPs from the API and filters them to this bloc.
// Returns the filtered IPs and a boolean indicating whether the caller should return early
// (true means no IPs were found or an error occurred, and the phase has been completed).
func (s *StackitVaultProvider) fetchAndFilterBlocIPs(
	client cpi.NetworkManager,
	reporter providers.ProgressReporter,
	phaseName string,
	phaseStart time.Time,
) ([]*cpi.PublicIP, bool) {
	allIPs, err := s.fetchAllPublicIPs(client)
	if err != nil {
		s.logger.Warnw("Failed to fetch public IPs from API, skipping", "error", err)
		reportPhaseComplete(reporter, phaseName, phaseStart)

		return nil, true
	}

	if len(allIPs) == 0 {
		s.logger.Info("No public IPs found in STACKIT API")
		reportPhaseComplete(reporter, phaseName, phaseStart)

		return nil, true
	}

	s.logger.Infow("Found public IPs from API", "total_count", len(allIPs))

	reportSubtask(reporter, phaseName, 3, 5, "Filtering IPs by bloc") //nolint:mnd

	blocIPs := s.filterBlocIPs(allIPs)
	if len(blocIPs) == 0 {
		s.logger.Infow("No public IPs found for bloc", "bloc", s.BlocName)
		reportPhaseComplete(reporter, phaseName, phaseStart)

		return nil, true
	}

	s.logger.Infow("Filtered public IPs for bloc", "bloc", s.BlocName, "count", len(blocIPs))

	return blocIPs, false
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

// getStackitClient retrieves or creates a STACKIT CPI client.
// If a networkManagerFactory is set (e.g., in tests), it will be used instead
// of creating a real STACKIT client.
//
//nolint:ireturn // returns interface by design
func (s *StackitVaultProvider) getStackitClient() (cpi.NetworkManager, error) {
	// Use factory if provided (allows test injection)
	if s.networkManagerFactory != nil {
		return s.networkManagerFactory(s.Config)
	}

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

	for _, ip := range allIPs { //nolint:varnamelen // ip is clear in context
		if ip.Labels == nil {
			continue
		}

		// Skip if not managed by OCFP
		managedBy, hasManagedBy := ip.Labels["managed-by"]
		if !hasManagedBy || managedBy != DefaultSubnetType {
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

	for _, ip := range blocIPs { //nolint:varnamelen // ip is clear in context
		// Get job from labels or use unknownValue
		job := unknownValue

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
		for _, ip := range sortedIPs { //nolint:varnamelen // ip is clear in context
			key, environment := s.determineVaultKeyAndEnvironment(job, ip.Index)

			// Store in appropriate environment
			if environment == MgmtEnvType {
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
	for i := range len(sorted) - 1 {
		for j := range len(sorted) - i - 1 { //nolint:varnamelen // j is clear in context
			// Compare indices (treat empty as unknownValue)
			idx1 := sorted[j].Index
			idx2 := sorted[j+1].Index

			if idx1 == "" {
				idx1 = unknownValue
			}

			if idx2 == "" {
				idx2 = unknownValue
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
	// Use index or unknownValue if not set
	if index == "" {
		index = unknownValue
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
	environment := MgmtEnvType
	if strings.HasPrefix(job, "cf_") || strings.HasPrefix(job, "cf-") {
		environment = OCFEnvType
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
	routerCount := s.countKeysWithPrefix(ocfVaultData, "router_")
	tcpRouterCount := s.countKeysWithPrefix(ocfVaultData, "tcp-router_")

	s.logger.Infow("  bastion IPs", "count", bastionCount)
	s.logger.Infow("  ops IPs", "count", opsCount)
	s.logger.Infow("  jumpbox IPs", "count", jumpboxCount)
	s.logger.Infow("  router IPs", "count", routerCount)
	s.logger.Infow("  tcp-router IPs", "count", tcpRouterCount)
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

// configureEnvironment configures a single environment (mgmt or ocf).
func (s *StackitVaultProvider) configureEnvironment(envType string, reporter providers.ProgressReporter, phaseNum *int, totalPhases int) error {
	s.logger.Infow("Configuring environment", "env_type", envType)

	envPath := s.PathBuilder.GetEnvironmentPath(envType)

	// Configure IaaS components (4 phases: networks, subnets, security-groups, region)
	err := s.ConfigureIAAS(envPath, envType, reporter, phaseNum, totalPhases)
	if err != nil {
		return fmt.Errorf("failed to configure IaaS for %s: %w", envType, err)
	}

	// Configure services (4 phases: blobstores, databases, load-balancers, fqdns)
	err = s.configureServices(envPath, envType, reporter, phaseNum, totalPhases)
	if err != nil {
		return err
	}

	// Configure environment-specific components (not phase-tracked yet, internal operations)
	err = s.configureEnvironmentComponents(envType)
	if err != nil {
		return err
	}

	return nil
}

// configureServices configures all service components for an environment.
func (s *StackitVaultProvider) configureServices(envPath, envType string, reporter providers.ProgressReporter, phaseNum *int, totalPhases int) error {
	err := s.ConfigureBlobstores(envPath, envType, reporter, *phaseNum, totalPhases)
	if err != nil {
		return fmt.Errorf("failed to configure blobstores for %s: %w", envType, err)
	}

	*phaseNum++

	err = s.ConfigureDatabases(envPath, envType, reporter, *phaseNum, totalPhases)
	if err != nil {
		return fmt.Errorf("failed to configure databases for %s: %w", envType, err)
	}

	*phaseNum++

	err = s.ConfigureLoadBalancers(envPath, envType, reporter, *phaseNum, totalPhases)
	if err != nil {
		return fmt.Errorf("failed to configure load balancers for %s: %w", envType, err)
	}

	*phaseNum++

	err = s.ConfigureFQDNs(envPath, envType, reporter, *phaseNum, totalPhases)
	if err != nil {
		return fmt.Errorf("failed to configure FQDNs for %s: %w", envType, err)
	}

	*phaseNum++

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

	// Configure blobstores for BOSH (and CF if ocf env)
	err = s.configureBlobstores(envType)
	if err != nil {
		return fmt.Errorf("failed to configure blobstores for %s: %w", envType, err)
	}

	return nil
}

// configureSharedComponents configures components shared between environments.
func (s *StackitVaultProvider) configureSharedComponents(reporter providers.ProgressReporter, phaseNum *int, totalPhases int) error {
	// Configure certificates (shared between environments)
	err := s.ConfigureCertificates("", "", reporter, *phaseNum, totalPhases)
	if err != nil {
		return fmt.Errorf("failed to configure certificates: %w", err)
	}

	*phaseNum++

	// Configure public IPs (OCF environment only)
	err = s.ConfigurePublicIPs(reporter, *phaseNum, totalPhases)
	if err != nil {
		return fmt.Errorf("failed to configure public IPs: %w", err)
	}

	*phaseNum++

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
	// For STACKIT, we use S3-compatible storage credentials
	s3Path := s.PathBuilder.GetS3Path(envType)

	// Get S3 credentials from config s3 map
	var accessKeyID, secretAccessKey string
	if s.Config.S3 != nil {
		accessKeyID = s.Config.S3["access_key_id"]
		secretAccessKey = s.Config.S3["secret_access_key"]
	}

	if accessKeyID == "" || secretAccessKey == "" {
		s.logger.Warn("No S3 credentials found (access_key_id or secret_access_key missing in s3 config)")

		return nil
	} // Write S3 credentials with all required fields to match Perl output

	s3Data := map[string]interface{}{
		"access_key_id":     accessKeyID,
		"secret_access_key": secretAccessKey,
		"region":            s.Config.Region,
	}

	err := s.Safe.SetMultiple(s3Path, s3Data)
	if err != nil {
		return fmt.Errorf("failed to set S3 IAM credentials: %w", err)
	}

	s.logger.Infow("Configured S3 IAM credentials", "env_type", envType, "region", s.Config.Region)

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
	boshKeyPath := s.PathBuilder.GetBOSHKeyPath(envType)
	keypairName := s.BlocName + "-bastion"

	keyData := map[string]interface{}{
		"keypair_name": keypairName,
	}

	s.loadKeyDataFromState(keyData)
	s.applyKeyDefaults(keyData)
	s.resolvePrivateKey(keyData, keypairName)

	err := s.Safe.SetMultiple(boshKeyPath, keyData)
	if err != nil {
		return fmt.Errorf("failed to set BOSH key data: %w", err)
	}

	return nil
}

// loadKeyDataFromState populates key data fields from the state manager if available.
func (s *StackitVaultProvider) loadKeyDataFromState(keyData map[string]interface{}) {
	stateManager := s.loadStateManager()
	if stateManager == nil {
		return
	}

	keypairs, err := stateManager.ListResources("keypair")
	if err != nil || len(keypairs) == 0 {
		return
	}

	keypair := keypairs[0]

	if keypair.ID != "" {
		keyData["id"] = keypair.ID
	}

	if fingerprint, ok := keypair.Properties["fingerprint"].(string); ok && fingerprint != "" {
		keyData["fingerprint"] = fingerprint
	}

	if keyType, ok := keypair.Properties["type"].(string); ok && keyType != "" {
		keyData["type"] = keyType
	} else {
		keyData["type"] = "ssh-rsa"
	}
}

// applyKeyDefaults sets default values for key data fields not already populated from state.
func (s *StackitVaultProvider) applyKeyDefaults(keyData map[string]interface{}) {
	if _, exists := keyData["id"]; !exists {
		keyData["id"] = s.BlocName + "-bosh-key"
	}

	if _, exists := keyData["fingerprint"]; !exists {
		keyData["fingerprint"] = ""
	}

	if _, exists := keyData["type"]; !exists {
		keyData["type"] = "ssh-rsa"
	}
}

// resolvePrivateKey finds the private key file and stores its path and content in key data.
func (s *StackitVaultProvider) resolvePrivateKey(keyData map[string]interface{}, keypairName string) {
	privateKeyPath := s.getPrivateKeyPath(keypairName)
	if privateKeyPath == "" {
		keyData["private"] = keypairName

		return
	}

	keyData["private_key_path"] = privateKeyPath

	privateKeyContent, err := s.readPrivateKeyContent(privateKeyPath)
	if err == nil && privateKeyContent != "" {
		keyData["private"] = privateKeyContent
	} else {
		keyData["private"] = "PRIVATE_KEY_READ_ERROR"

		s.logger.Warnw("Failed to read private key content", "path", privateKeyPath, "error", err)
	}
}

// getPrivateKeyPath attempts to find the private key path for the given keypair name.
func (s *StackitVaultProvider) getPrivateKeyPath(keypairName string) string {
	// Try standard OCFP location first
	sshKeyDir := config.OcfpSSHKeyDir(s.BlocName)
	if sshKeyDir == "" {
		return ""
	}

	// Try Ed25519 key (preferred)
	ed25519Path := filepath.Join(sshKeyDir, "id_ed25519")

	_, statErr := os.Stat(ed25519Path) //nolint:gosec // path from trusted config
	if statErr == nil {
		return ed25519Path
	}

	// Try RSA key
	rsaPath := filepath.Join(sshKeyDir, "id_rsa")

	_, statErr = os.Stat(rsaPath) //nolint:gosec // path from trusted config
	if statErr == nil {
		return rsaPath
	}

	// Try standard .ssh directory with bloc name
	homeDir, homeErr := os.UserHomeDir()
	if homeErr != nil {
		return ""
	}

	sshPath := filepath.Join(homeDir, ".ssh", keypairName)

	_, statErr = os.Stat(sshPath) //nolint:gosec // path from trusted config
	if statErr == nil {
		return sshPath
	}

	return ""
}

// readPrivateKeyContent reads the private key content from the given path.
func (s *StackitVaultProvider) readPrivateKeyContent(path string) (string, error) {
	content, err := os.ReadFile(path) //nolint:gosec // path is from trusted internal resolution
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
//
//nolint:unparam // error return kept for consistent phase function signatures
func (s *StackitVaultProvider) configureSecurityGroups(envType string, reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
	phaseName := "security-groups-" + envType
	phaseStart := time.Now()

	if reporter != nil {
		reporter.ReportPhaseStart(phaseName, phaseNum, totalPhases)
	}

	s.logger.Infow("Configuring security groups", "env_type", envType)

	// Build security group mapping (Perl: _build_sg_mapping, lines 1582-1612)
	sgMapping := s.buildSecurityGroupMapping()

	// Get network path for vault storage
	netPath := s.PathBuilder.GetNetPath(envType)

	// Load state manager to check for security group data
	stateManager := s.loadStateManager()

	totalGroups := len(sgMapping)
	currentGroup := 0

	// Process each security group type
	for sgType, sgFullName := range sgMapping {
		currentGroup++

		if reporter != nil {
			label := "Writing " + sgFullName
			reporter.ReportSubtaskProgress(phaseName, currentGroup, totalGroups, label)
		}

		// Try to find the security group from state or other sources
		sg := s.findSecurityGroup(stateManager, sgType, sgFullName) //nolint:varnamelen // sg is clear in context

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

	if reporter != nil {
		reporter.ReportPhaseComplete(phaseName, time.Since(phaseStart))
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
		DefaultSubnetType,
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
// Falls back to API query if not found in state (matching Perl's robustness).
func (s *StackitVaultProvider) findSecurityGroup(stateManager *state.Manager, sgType, sgFullName string) map[string]interface{} {
	if stateManager != nil {
		if sg := s.findSGInStateResources(stateManager, sgType, sgFullName); sg != nil {
			return sg
		}

		if sg := s.findSGInStateOutputs(stateManager, sgType, sgFullName); sg != nil {
			return sg
		}
	}

	if sg := s.findSGViaAPI(sgType, sgFullName); sg != nil {
		return sg
	}

	s.logger.Debugw("Security group not found in state or API",
		"type", sgType, "name", sgFullName)

	return nil
}

// findSGInStateResources looks up a security group by name in state resource records.
func (s *StackitVaultProvider) findSGInStateResources(stateManager *state.Manager, sgType, sgFullName string) map[string]interface{} {
	resources, err := stateManager.ListResources("security_group")
	if err != nil || len(resources) == 0 {
		return nil
	}

	for _, resource := range resources {
		name, ok := resource.Properties["name"].(string)
		if !ok || name != sgFullName {
			continue
		}

		sg := map[string]interface{}{ //nolint:varnamelen // sg is clear in context
			"id":   resource.ID,
			"name": name,
		}

		if desc, ok := resource.Properties["description"].(string); ok {
			sg["description"] = desc
		} else {
			sg["description"] = "Security group for " + sgType
		}

		s.logger.Debugw("Found security group in state",
			"type", sgType, "name", sgFullName, "id", resource.ID)

		return sg
	}

	return nil
}

// findSGInStateOutputs looks up a security group ID in state outputs.
func (s *StackitVaultProvider) findSGInStateOutputs(stateManager *state.Manager, sgType, sgFullName string) map[string]interface{} {
	sgKey := fmt.Sprintf("security_group_%s_id", sgType)

	sgID, err := stateManager.GetOutput(sgKey)
	if err != nil {
		return nil
	}

	id, ok := sgID.(string) //nolint:varnamelen // id is clear in context
	if !ok || id == "" {
		return nil
	}

	s.logger.Debugw("Found security group in state outputs",
		"type", sgType, "name", sgFullName, "id", id)

	return map[string]interface{}{
		"id":          id,
		"name":        sgFullName,
		"description": "Security group for " + sgType,
	}
}

// findSGViaAPI queries the STACKIT API to find a security group by name.
func (s *StackitVaultProvider) findSGViaAPI(sgType, sgFullName string) map[string]interface{} {
	s.logger.Debugw("Security group not in state, querying API",
		"type", sgType, "name", sgFullName)

	client, err := s.getStackitClient()
	if err != nil {
		s.logger.Warnw("Failed to create STACKIT client for SG lookup", "error", err)

		return nil
	}

	ctx := context.Background()

	sgs, err := client.ListSecurityGroups(ctx, nil)
	if err != nil {
		s.logger.Warnw("Failed to list security groups from API", "error", err)

		return nil
	}

	for _, apiSg := range sgs {
		if apiSg.Name != sgFullName {
			continue
		}

		desc := apiSg.Description
		if desc == "" {
			desc = "Security group for " + sgType
		}

		s.logger.Infow("Found security group via API fallback",
			"type", sgType, "name", sgFullName, "id", apiSg.ID)

		return map[string]interface{}{
			"id":          apiSg.ID,
			"name":        apiSg.Name,
			"description": desc,
		}
	}

	return nil
}

// storeSecurityGroupToVault stores a security group to vault with proper path handling.
// This matches the Perl implementation in _store_sg_to_vault (lines 1667-1697).
//
// CRITICAL: CF-specific security groups are stored directly under net/ path,
// NOT under net/sgs/, for deployment compatibility (Perl: lines 1676-1685).
func (s *StackitVaultProvider) storeSecurityGroupToVault(sg map[string]interface{}, sgType, sgFullName, netPath string) error { //nolint:varnamelen // sg is clear in context
	// Get security group ID
	sgID, ok := sg["id"].(string)
	if !ok || sgID == "" {
		return ErrSecurityGroupMissingID
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
		sgData["description"] = "Security group for " + sgType
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
	if subnetType != DefaultSubnetType {
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
//
//nolint:funlen // reserved IP assignment map is inherently large
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
				Offset: 10, //nolint:mnd
			},
		},
		"vault": {
			"mgmt": {Offset: 5},  //nolint:mnd
			"ocf":  {Offset: 32}, //nolint:mnd
		},
		"jumpbox": {
			"mgmt": {Offset: 6},  //nolint:mnd
			"ocf":  {Offset: 33}, //nolint:mnd
		},
		"concourse": {
			"mgmt": {Offset: 7},  //nolint:mnd
			"ocf":  {Offset: 34}, //nolint:mnd
		},
		"prometheus": {
			"mgmt": {Offset: 8},  //nolint:mnd
			"ocf":  {Offset: 35}, //nolint:mnd
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
				Offset: 3, //nolint:mnd
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
				Offset: 10, //nolint:mnd
			},
		},
		"shout": {
			"mgmt": {
				SubnetMapping: map[int][]int{10: {1}},
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
	baseIP, networkBits, err := parseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("invalid CIDR %s: %w", cidr, err)
	}

	vaultIPs := make(map[string]interface{})
	usedIPs := make(map[string]bool)

	assignmentTypes := make([]string, 0, len(assignments))
	for assignmentType := range assignments {
		assignmentTypes = append(assignmentTypes, assignmentType)
	}

	sortAssignmentTypes(assignmentTypes)

	for _, assignmentType := range assignmentTypes {
		envMap := assignments[assignmentType]

		assignment := envMap[envType]
		if assignment == nil {
			assignment = envMap["other"]
		}

		if assignment == nil {
			continue
		}

		s.processAssignment(assignmentType, assignment, baseIP, networkBits, subnetNum, vaultIPs, usedIPs)
	}

	return vaultIPs, nil
}

// processAssignment dispatches a single reserved IP assignment to the appropriate processor.
func (s *StackitVaultProvider) processAssignment(
	assignmentType string,
	assignment *reservedIPAssignment,
	baseIP string,
	networkBits, subnetNum int,
	vaultIPs map[string]interface{},
	usedIPs map[string]bool,
) {
	switch {
	case assignment.Offset > 0:
		s.processOffsetAssignment(assignmentType, assignment.Offset, baseIP, vaultIPs, usedIPs)
	case len(assignment.SubnetMapping) > 0:
		s.processSubnetMappingAssignment(assignmentType, assignment.SubnetMapping, baseIP, subnetNum, vaultIPs, usedIPs)
	case assignment.RangeSpec != "":
		s.processRangeSpecAssignment(assignmentType, assignment.RangeSpec, baseIP, networkBits, vaultIPs)
	case len(assignment.SubnetRanges) > 0:
		s.processSubnetRangesAssignment(assignmentType, assignment.SubnetRanges, baseIP, networkBits, subnetNum, vaultIPs)
	}
}

// processOffsetAssignment handles a simple offset-based single IP reservation.
func (s *StackitVaultProvider) processOffsetAssignment(
	assignmentType string, offset int, baseIP string,
	vaultIPs map[string]interface{}, usedIPs map[string]bool,
) {
	ip := addOffsetToIP(baseIP, offset) //nolint:varnamelen // ip is clear in context
	if usedIPs[ip] {
		return
	}

	vaultIPs[assignmentType+"_ip"] = ip
	usedIPs[ip] = true

	// Add IP bounds (_a and _b) for Genesis compatibility (Perl: lines 1343-1346)
	vaultIPs[assignmentType+"_a"] = addOffsetToIP(baseIP, offset-1)
	vaultIPs[assignmentType+"_b"] = addOffsetToIP(baseIP, offset+1)

	s.logger.Debugw("Reserved IP", "type", assignmentType, "ip", ip, "offset", offset)
}

// processSubnetMappingAssignment handles offset-to-subnet-number mapping reservations.
func (s *StackitVaultProvider) processSubnetMappingAssignment(
	assignmentType string, subnetMapping map[int][]int, baseIP string,
	subnetNum int, vaultIPs map[string]interface{}, usedIPs map[string]bool,
) {
	for offset, subnets := range subnetMapping {
		if !containsInt(subnets, subnetNum) {
			continue
		}

		ip := addOffsetToIP(baseIP, offset) //nolint:varnamelen // ip is clear in context
		if usedIPs[ip] {
			break
		}

		vaultIPs[assignmentType+"_ip"] = ip
		usedIPs[ip] = true
		vaultIPs[assignmentType+"_a"] = addOffsetToIP(baseIP, offset-1)
		vaultIPs[assignmentType+"_b"] = addOffsetToIP(baseIP, offset+1)

		s.logger.Debugw("Reserved IP from subnet mapping",
			"type", assignmentType, "ip", ip, "offset", offset, "subnet_num", subnetNum)

		break
	}
}

// processRangeSpecAssignment handles range-specification-based IP reservations.
func (s *StackitVaultProvider) processRangeSpecAssignment(
	assignmentType, rangeSpec, baseIP string, networkBits int,
	vaultIPs map[string]interface{},
) {
	ranges, err := parseIPRangeSpec(rangeSpec, baseIP, networkBits)
	if err != nil {
		s.logger.Warnw("Failed to parse range spec",
			"type", assignmentType, "spec", rangeSpec, "error", err)

		return
	}

	s.storeIPRanges(assignmentType, ranges, vaultIPs, "Reserved IP range", 0)
}

// processSubnetRangesAssignment handles subnet-specific range-based IP reservations.
func (s *StackitVaultProvider) processSubnetRangesAssignment(
	assignmentType string, subnetRanges map[string][]int, baseIP string,
	networkBits, subnetNum int, vaultIPs map[string]interface{},
) {
	for rangeSpec, subnets := range subnetRanges {
		if !containsInt(subnets, subnetNum) {
			continue
		}

		ranges, err := parseIPRangeSpec(rangeSpec, baseIP, networkBits)
		if err != nil {
			s.logger.Warnw("Failed to parse subnet range spec",
				"type", assignmentType, "spec", rangeSpec, "error", err)

			continue
		}

		s.storeIPRanges(assignmentType, ranges, vaultIPs, "Reserved IP range from subnet mapping", subnetNum)

		break
	}
}

// storeIPRanges writes IP range boundaries into the vault data map and logs them.
func (s *StackitVaultProvider) storeIPRanges(
	assignmentType string, ranges []ipRange,
	vaultIPs map[string]interface{}, logMessage string, subnetNum int,
) {
	idx := 0
	for _, rng := range ranges {
		vaultIPs[fmt.Sprintf("%s_%d", assignmentType, idx)] = rng.Start
		idx++
		vaultIPs[fmt.Sprintf("%s_%d", assignmentType, idx)] = rng.End
		idx++

		logFields := []interface{}{"type", assignmentType, "start", rng.Start, "end", rng.End}
		if subnetNum > 0 {
			logFields = append(logFields, "subnet_num", subnetNum)
		}

		s.logger.Debugw(logMessage, logFields...)
	}
}

// ipRange represents an IP address range.
type ipRange struct {
	Start string
	End   string
}

// parseCIDR parses a CIDR notation and returns base IP and network bits.
func parseCIDR(cidr string) (string, int, error) {
	parts := strings.Split(cidr, "/")
	if len(parts) != 2 { //nolint:mnd
		return "", 0, ErrInvalidCIDRFormat(cidr)
	}

	baseIP := parts[0]
	networkBits := 0

	_, err := fmt.Sscanf(parts[1], "%d", &networkBits)
	if err != nil {
		return "", 0, fmt.Errorf("invalid network bits: %w", err)
	}

	// Validate IP address format
	octets := strings.Split(baseIP, ".")
	if len(octets) != 4 { //nolint:mnd
		return "", 0, ErrInvalidIPAddressFormat
	}

	return baseIP, networkBits, nil
}

// addOffsetToIP adds an offset to an IP address.
func addOffsetToIP(baseIP string, offset int) string {
	octets := strings.Split(baseIP, ".")
	if len(octets) != 4 { //nolint:mnd
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
	if newOctet > 255 { //nolint:mnd
		thirdOctet, err := strconv.Atoi(octets[2])
		if err != nil {
			return baseIP
		}

		thirdOctet += newOctet / 256 //nolint:mnd
		newOctet %= 256
		octets[2] = strconv.Itoa(thirdOctet)
	}

	octets[3] = strconv.Itoa(newOctet)

	return strings.Join(octets, ".")
}

// parseIPRangeSpec parses a range specification like "11-29" or "0-10,30->".
// This matches the Perl implementation in _parse_ip_range_string (lines 1476-1497).
//
//nolint:unparam // error return kept for future validation of malformed range specs
func parseIPRangeSpec(rangeSpec string, baseIP string, networkBits int) ([]ipRange, error) {
	// Split by comma for multiple ranges
	subranges := strings.Split(rangeSpec, ",")
	ranges := make([]ipRange, 0, len(subranges))

	for _, subrange := range subranges {
		subrange = strings.TrimSpace(subrange)

		// Parse range like "11-29" or "30->" or "10"
		var lower, upper int

		switch {
		case strings.Contains(subrange, "->"):
			// Open-ended range like "30->"
			parts := strings.Split(subrange, "->")
			lower, _ = strconv.Atoi(strings.TrimSpace(parts[0]))
			upper = calculateLastHostOffset(networkBits)
		case strings.Contains(subrange, "-"):
			// Closed range like "11-29"
			parts := strings.Split(subrange, "-")
			lower, _ = strconv.Atoi(strings.TrimSpace(parts[0]))
			upper, _ = strconv.Atoi(strings.TrimSpace(parts[1]))
		default:
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
	hostBits := 32 - networkBits //nolint:mnd
	if hostBits <= 0 {
		return 0
	}

	// For typical /24 network: 2^8 - 1 = 255
	// But we want last usable host (254)
	maxHosts := (1 << uint(hostBits)) - 2 //nolint:mnd

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
		"vault":      2,  //nolint:mnd
		"jumpbox":    3,  //nolint:mnd
		"concourse":  4,  //nolint:mnd
		"prometheus": 5,  //nolint:mnd
		"shield":     6,  //nolint:mnd
		"doomsday":   7,  //nolint:mnd
		"ocfp_ui":    8,  //nolint:mnd
		"bastion":    9,  //nolint:mnd
		"blacksmith": 10, //nolint:mnd
		"shout":      11, //nolint:mnd
		"available":  12, //nolint:mnd
		"reserved":   13, //nolint:mnd
	}

	// Sort by priority
	for i := range len(types) - 1 { //nolint:varnamelen // i is clear in context
		for j := i + 1; j < len(types); j++ { //nolint:varnamelen // j is clear in context
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
func (s *StackitVaultProvider) configureSubnets(envType string, reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
	phaseName := "subnets-" + envType
	phaseStart := time.Now()

	if reporter != nil {
		reporter.ReportPhaseStart(phaseName, phaseNum, totalPhases)
	}

	s.logger.Infow("Configuring subnets", "env_type", envType)

	subnetsPath := s.PathBuilder.GetSubnetsPath(envType)

	// Determine which subnet list to use
	subnets := s.Config.Subnets

	// Fallback: check Network.Subnets if top-level Subnets is empty
	// This handles cases where bootstrap populated Network.Subnets but it wasn't copied
	if len(subnets) == 0 && len(s.Config.Network.Subnets) > 0 {
		s.logger.Infow("Using subnets from Network.Subnets", "count", len(s.Config.Network.Subnets))
		subnets = s.Config.Network.Subnets
	}

	// If still no subnets, create default virtual subnet for STACKIT
	if len(subnets) == 0 {
		s.logger.Warn("No subnets configured in Config.Subnets or Network.Subnets, using fallback")

		if reporter != nil {
			reporter.ReportSubtaskProgress(phaseName, 1, 1, "Writing fallback subnets")
		}

		err := s.configureFallbackSubnet(envType)

		if reporter != nil {
			reporter.ReportPhaseComplete(phaseName, time.Since(phaseStart))
		}

		return err
	}

	totalSubnets := len(subnets)
	for i, subnet := range subnets { //nolint:varnamelen // i is clear in context
		if reporter != nil {
			label := fmt.Sprintf("Writing subnet %s-%d", subnet.Type, i)
			reporter.ReportSubtaskProgress(phaseName, i+1, totalSubnets, label)
		}

		err := s.configureSubnet(envType, i, subnet)
		if err != nil {
			return err
		}
	}

	s.logger.Infow("Subnets configuration completed", "path", subnetsPath)

	if reporter != nil {
		reporter.ReportPhaseComplete(phaseName, time.Since(phaseStart))
	}

	return nil
}

// configureFallbackSubnet creates a default virtual subnet when no subnets are configured.
// This ensures reserved IPs are always written to vault for STACKIT deployments.
func (s *StackitVaultProvider) configureFallbackSubnet(envType string) error {
	// Use configured Network CIDR or default to match Perl implementation
	cidr := s.Config.Network.CIDR
	if cidr == "" {
		cidr = "10.4.0.0/20" // Default CIDR from Perl reference
	}

	// Create default virtual subnet with type "ocfp"
	fallbackSubnet := config.Subnet{
		CIDR: cidr,
		Type: DefaultSubnetType,
	}

	s.logger.Infow("Creating fallback virtual subnet", "cidr", cidr, "type", DefaultSubnetType, "index", 0)

	// Use existing configureSubnet logic which will create reserved IPs
	return s.configureSubnet(envType, 0, fallbackSubnet)
}
func (s *StackitVaultProvider) configureSubnet(envType string, subnetNum int, subnet config.Subnet) error {
	subnetType := subnet.Type
	if subnetType == "" {
		subnetType = DefaultSubnetType // Default subnet type
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

	if subnetType == DefaultSubnetType {
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

	// Sort AZ names to ensure deterministic ordering (Go map iteration is random)
	// This ensures the first AZ is always consistently selected for BOSH directors
	sort.Strings(azNames)

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

	// Get network_id from API (fallback to state, then ProjectID)
	// This ensures subnets get the correct parent network UUID
	networkID, err := s.getNetworkIDFromAPI()
	if err != nil {
		if s.logger != nil {
			s.logger.Debugw("Failed to fetch network ID from API for subnet, using ProjectID", "error", err)
		}

		networkID = s.Config.ProjectID
	}

	if networkID == "" {
		networkID = s.Config.ProjectID
	}

	// Use first DNS entry or default to '1.1.1.1' (matches Perl implementation)
	dnsString := DefaultDNSServer
	if len(s.Config.DNS) > 0 {
		dnsString = s.Config.DNS[0]
	}

	// Build base subnet data
	subnetData := map[string]interface{}{
		"id":          fmt.Sprintf("%s-%s-%d", s.BlocName, subnetType, subnetNum),
		"cidr_block":  cidr,
		"cidr_prefix": networkInfo.cidrPrefix,
		"ip_0":        networkInfo.network,
		"ip_n":        networkInfo.lastHost,
		"gateway":     networkInfo.gateway,
		"dns":         dnsString,
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
		"network_id":    networkID,                                   // Parent network ID (required by Perl contract)
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

// getNetworkCIDRFromState retrieves the network_cidr from state manager.
func (s *StackitVaultProvider) getNetworkCIDRFromState() string {
	stateManager := s.loadStateManager()
	if stateManager == nil {
		return ""
	}

	networkCIDR, err := stateManager.GetOutput("network_cidr")
	if err != nil {
		return ""
	}

	if cidr, ok := networkCIDR.(string); ok {
		return cidr
	}

	return ""
}

// getNetworkIDFromAPI fetches the network ID from STACKIT API by network name.
// This matches the Perl implementation in _get_network_id (lines 290-324).
//
//nolint:funlen // network ID lookup tries multiple API sources
func (s *StackitVaultProvider) getNetworkIDFromAPI() (string, error) {
	// Network name follows the pattern: <bloc>-net
	networkName := s.BlocName + "-net"

	if s.logger != nil {
		s.logger.Infow("Fetching network ID from STACKIT API",
			"network_name", networkName,
			"project_id", s.Config.ProjectID)
	}

	// Get STACKIT client
	networkManager, err := s.getStackitClient()
	if err != nil {
		if s.logger != nil {
			s.logger.Errorw("Failed to create STACKIT client for network lookup",
				"error", err,
				"network_name", networkName,
				"help", "Ensure STACKIT credentials are configured correctly")
		}

		return "", fmt.Errorf("failed to create STACKIT client: %w", err)
	}

	// List all networks (no filters - filters are for labels, not name)
	// We filter by name in the results instead
	ctx := context.Background()

	if s.logger != nil {
		s.logger.Infow("Querying STACKIT API for all networks in project",
			"project_id", s.Config.ProjectID)
	}

	networks, err := networkManager.ListNetworks(ctx, nil)
	if err != nil {
		if s.logger != nil {
			s.logger.Errorw("Failed to list networks from STACKIT API",
				"error", err,
				"network_name", networkName,
				"project_id", s.Config.ProjectID,
				"help", "Verify network access and API permissions")
		}

		return "", fmt.Errorf("failed to list networks: %w", err)
	}

	if s.logger != nil {
		s.logger.Infow("STACKIT API returned networks", "count", len(networks))

		for i, net := range networks {
			s.logger.Debugw("Network from API",
				"index", i,
				"id", net.ID,
				"name", net.Name,
				"state", net.State)
		}
	}

	// Find matching network by name (case-insensitive)
	for _, network := range networks {
		if strings.EqualFold(network.Name, networkName) {
			if s.logger != nil {
				s.logger.Infow("Found matching network from STACKIT API",
					"network_id", network.ID,
					"network_name", network.Name,
					"state", network.State)
			}

			return network.ID, nil
		}
	}

	// If no match found, return empty string (will be populated during bootstrap)
	if s.logger != nil {
		s.logger.Warnw("Network not found in STACKIT API - ID will be empty until network is created",
			"network_name", networkName,
			"searched_count", len(networks),
			"help", "Network will be created during bootstrap, then run 'vault populate' again")
	}

	return "", nil
}

// configureNetwork configures network settings in vault.
func (s *StackitVaultProvider) configureNetwork(envType string, reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
	phaseName := "networks-" + envType
	phaseStart := time.Now()

	if reporter != nil {
		reporter.ReportPhaseStart(phaseName, phaseNum, totalPhases)
	}

	netPath := s.PathBuilder.GetNetPath(envType)

	// Use first DNS entry or default to '1.1.1.1' (matches Perl implementation)
	dnsString := DefaultDNSServer
	if len(s.Config.DNS) > 0 {
		dnsString = s.Config.DNS[0]
	}

	// Fetch the actual network ID from STACKIT API (not ProjectID)
	if reporter != nil {
		reporter.ReportSubtaskProgress(phaseName, 1, 2, fmt.Sprintf("Writing %s-net", s.BlocName)) //nolint:mnd
	}

	networkID, err := s.fetchAndValidateNetworkID(envType)
	if err != nil {
		return err
	}

	// Resolve network CIDR from state, config, or API fallback
	networkCIDR := s.resolveNetworkCIDR()

	// Build network data map
	networkData := s.buildNetworkData(networkID, networkCIDR, dnsString)

	// Enrich with API data (CIDR, status, created_at) if possible
	s.enrichNetworkDataFromAPI(networkID, networkData)

	// Apply default CIDR if still empty after all attempts
	s.applyDefaultCIDR(networkData)

	setErr := s.Safe.SetMultiple(netPath, networkData)
	if setErr != nil {
		return fmt.Errorf("failed to set network data: %w", setErr)
	}

	// Configure availability zones
	if reporter != nil {
		reporter.ReportSubtaskProgress(phaseName, 2, 2, "Writing availability zones") //nolint:mnd
	}

	err = s.configureAvailabilityZones(envType)
	if err != nil {
		return err
	}

	s.logger.Infow("Network configuration completed", "path", netPath)

	if reporter != nil {
		reporter.ReportPhaseComplete(phaseName, time.Since(phaseStart))
	}

	return nil
}

// fetchAndValidateNetworkID fetches the network ID from the STACKIT API and validates it.
func (s *StackitVaultProvider) fetchAndValidateNetworkID(envType string) (string, error) {
	s.logger.Infow("Fetching network ID for vault populate",
		"env_type", envType,
		"bloc", s.BlocName,
		"expected_network_name", s.BlocName+"-net")

	networkID, err := s.getNetworkIDFromAPI()
	if err != nil {
		s.logger.Errorw("Failed to fetch network ID from API - vault populate will fail",
			"error", err,
			"env_type", envType,
			"help", "Ensure: 1) Network exists in STACKIT 2) API credentials are correct 3) API permissions allow network access")

		return "", fmt.Errorf("network ID is required but could not be fetched: %w", err)
	}

	if networkID == "" {
		s.logger.Errorw("Network not found in STACKIT - cannot populate vault",
			"expected_network_name", s.BlocName+"-net",
			"env_type", envType,
			"help", "Run 'ocfp bootstrap' first to create the network, then run 'vault populate' again")

		return "", fmt.Errorf("%w: '%s-net' in project %s", ErrNetworkNotFound, s.BlocName, s.Config.ProjectID)
	}

	s.logger.Infow("Successfully retrieved network ID",
		"network_id", networkID,
		"env_type", envType)

	return networkID, nil
}

// resolveNetworkCIDR resolves the network CIDR from multiple sources in priority order:
// 1. From state (populated during bootstrap)
// 2. From config CIDR field
// 3. From config NetworkCIDR field.
func (s *StackitVaultProvider) resolveNetworkCIDR() string {
	networkCIDR := s.getNetworkCIDRFromState()
	if networkCIDR != "" {
		return networkCIDR
	}

	if s.Config.Network.CIDR != "" {
		return s.Config.Network.CIDR
	}

	return s.Config.Network.NetworkCIDR
}

// buildNetworkData constructs the base network data map for vault storage.
func (s *StackitVaultProvider) buildNetworkData(networkID, networkCIDR, dnsString string) map[string]interface{} {
	return map[string]interface{}{
		"id":          networkID,
		"cidr_block":  networkCIDR,
		"dns":         dnsString,
		"region":      s.Config.Region,
		"provider":    "stackit",
		"name":        s.BlocName + "-net",
		"ipv4_cidr":   networkCIDR,
		"project_id":  s.Config.ProjectID,
		"description": "Primary STACKIT network for environment " + s.BlocName,
	}
}

// enrichNetworkDataFromAPI fetches additional network fields (CIDR, status, created_at) from the
// STACKIT API and merges them into the network data map. Failures are logged but do not cause errors.
func (s *StackitVaultProvider) enrichNetworkDataFromAPI(networkID string, networkData map[string]interface{}) {
	if networkID == "" || networkID == s.Config.ProjectID {
		return
	}

	s.logger.Debugw("Fetching network details from API", "network_id", networkID)

	networkManager, err := s.getStackitClient()
	if err != nil {
		s.logger.Warnw("Failed to create STACKIT client, using basic network fields only", "error", err)

		return
	}

	ctx := context.Background()

	network, err := networkManager.GetNetwork(ctx, networkID)
	if err != nil {
		s.logger.Warnw("Failed to fetch network details from API, using basic fields only",
			"network_id", networkID, "error", err)

		return
	}

	if network == nil {
		return
	}

	// Populate CIDR from API if not already set from state/config
	if cidr, ok := networkData["cidr_block"].(string); (!ok || cidr == "") && network.CIDR != "" {
		networkData["cidr_block"] = network.CIDR
		networkData["ipv4_cidr"] = network.CIDR
		s.logger.Debugw("Populated network CIDR from API", "cidr", network.CIDR)
	}

	if network.State != "" {
		networkData["status"] = network.State
		s.logger.Debugw("Added network status from API", "status", network.State)
	}

	if !network.CreatedAt.IsZero() {
		networkData["created_at"] = network.CreatedAt.Format(time.RFC3339)
		s.logger.Debugw("Added network created_at from API", "created_at", network.CreatedAt)
	}
}

// applyDefaultCIDR sets a default CIDR if the network data map still has an empty cidr_block.
func (s *StackitVaultProvider) applyDefaultCIDR(networkData map[string]interface{}) {
	cidr, ok := networkData["cidr_block"].(string)
	if ok && cidr != "" {
		return
	}

	defaultCIDR := "10.4.0.0/20" // STACKIT default CIDR
	networkData["cidr_block"] = defaultCIDR
	networkData["ipv4_cidr"] = defaultCIDR
	s.logger.Warnw("Using default network CIDR as fallback", "cidr", defaultCIDR)
}

// configureAvailabilityZones writes availability zone data to vault.
func (s *StackitVaultProvider) configureAvailabilityZones(envType string) error {
	for azName, azData := range s.Config.AZs {
		azPath := s.PathBuilder.GetAZPath(envType, azName)

		// Format cloud_properties as JSON string matching Perl format
		azInfo := map[string]interface{}{
			"cloud_properties": fmt.Sprintf(`{"availability_zone": "%s"}`, azData.Zone),
		}

		err := s.Safe.SetMultiple(azPath, azInfo)
		if err != nil {
			return fmt.Errorf("failed to set AZ data for %s: %w", azName, err)
		}
	}

	return nil
}

// configureBlobstores configures blobstore settings for systems (BOSH, CF).
// This writes blobstore configuration to vault paths that Genesis expects.
// Path format: /config/{bloc}/{env}/{system}/blobstores/{name}.
func (s *StackitVaultProvider) configureBlobstores(envType string) error {
	s.logger.Infow("Configuring blobstores", "env_type", envType)

	// Determine which systems need blobstores
	// BOSH and OCFP blobstores are needed for both mgmt and ocf
	// CF blobstores are only needed for ocf environment
	systems := []string{boshSystem, DefaultSubnetType}
	if envType == OCFEnvType {
		systems = append(systems, "cf")
	}

	for _, system := range systems {
		systemBlobstores := s.getBlobstoresForSystem(system, envType)

		for blobstoreName, blobstoreConfig := range systemBlobstores {
			// CRITICAL: Use system-direct path, NOT services path
			// Genesis expects: /config/{bloc}/{env}/{system}/blobstores/{name}
			// Example: /config/scf-stackit-eu01-004-dev/mgmt/bosh/blobstores/bosh
			// Example: /config/scf-stackit-eu01-004-dev/mgmt/ocfp/blobstores/artifacts
			blobstorePath := fmt.Sprintf("secret/config/%s/%s/%s/blobstores/%s",
				s.BlocName, envType, system, blobstoreName)

			s.logger.Infow("Writing blobstore configuration",
				"system", system,
				"name", blobstoreName,
				"path", blobstorePath)

			err := s.Safe.SetMultiple(blobstorePath, blobstoreConfig)
			if err != nil {
				return fmt.Errorf("failed to set blobstore %s for %s: %w", blobstoreName, system, err)
			}
		}
	}

	s.logger.Infow("Blobstores configuration completed", "env_type", envType)

	return nil
}

// getBlobstoresForSystem returns blobstore configuration for a system.
func (s *StackitVaultProvider) getBlobstoresForSystem(system, envType string) map[string]map[string]interface{} {
	blobstores := make(map[string]map[string]interface{})

	// Get S3 credentials from config s3 map
	var accessKeyID, secretAccessKey string
	if s.Config.S3 != nil {
		accessKeyID = s.Config.S3["access_key_id"]
		secretAccessKey = s.Config.S3["secret_access_key"]
	}

	region := s.Config.Region

	// S3 host configuration for STACKIT
	host := fmt.Sprintf("s3.%s.stackit.cloud", region)

	switch system {
	case "bosh":
		// BOSH uses a single blobstore named "bosh"
		bucketName := fmt.Sprintf("%s-%s-bosh", s.BlocName, envType)
		blobstores["bosh"] = s.createBlobstoreConfig(bucketName, region, host, accessKeyID, secretAccessKey)

	case "cf":
		// Cloud Foundry uses separate blobstores for different artifact types
		cfBlobstores := []string{"buildpacks", "droplets", "app-packages", "resources"}
		for _, name := range cfBlobstores {
			bucketName := fmt.Sprintf("%s-%s-cf-%s", s.BlocName, envType, name)
			blobstores[name] = s.createBlobstoreConfig(bucketName, region, host, accessKeyID, secretAccessKey)
		}

	case DefaultSubnetType:
		// OCFP artifacts blobstore
		bucketName := fmt.Sprintf("%s-%s-artifacts", s.BlocName, envType)
		blobstores["artifacts"] = s.createBlobstoreConfig(bucketName, region, host, accessKeyID, secretAccessKey)
	}

	return blobstores
}

// createBlobstoreConfig creates a complete blobstore configuration with all required fields.
// This matches the Perl implementation which generates all 11 fields for S3-compatible storage.
func (s *StackitVaultProvider) createBlobstoreConfig(
	bucketName, region, host, accessKeyID, secretAccessKey string) map[string]interface{} {
	// Generate all URL variants for STACKIT S3-compatible storage
	baseURL := fmt.Sprintf("https://%s/%s", host, bucketName)
	pathStyleURL := fmt.Sprintf("https://object.storage.%s.onstackit.cloud/%s", region, bucketName)
	virtualHostedURL := fmt.Sprintf("https://%s.object.storage.%s.onstackit.cloud", bucketName, region)

	return map[string]interface{}{
		"name":                     bucketName,
		"provider":                 "stackit",
		"region":                   region,
		"access_key_id":            accessKeyID,
		"secret_access_key":        secretAccessKey,
		"host":                     host,
		"storage_class":            "STANDARD",
		"pathstyle":                "true",
		"url":                      baseURL,
		"url_path_style":           pathStyleURL,
		"url_virtual_hosted_style": virtualHostedURL,
	}
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
//nolint:unparam,funlen // error return for future implementation; maintains interface consistency
func (s *StackitVaultProvider) configureUsers(envType string) error {
	// Users configuration is only relevant for mgmt environment (jumpbox)
	if envType != MgmtEnvType {
		return nil
	}

	// Prefer jumpbox.users; fall back to deprecated top-level users
	users := s.Config.Jumpbox.Users
	if len(users) == 0 {
		users = s.Config.Users // deprecated fallback
	}

	if len(users) == 0 {
		s.logger.Infow("No users configured, skipping jumpbox user configuration")

		return nil
	}

	usersPath := s.PathBuilder.GetJumpboxUsersPath()
	s.logger.Infow("Configuring jumpbox users", "path", usersPath, "user_count", len(users))

	// HTTP client for fetching keys from GitHub/GitLab
	httpClient := &http.Client{
		Timeout: 30 * time.Second, //nolint:mnd
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

	for username, keySpec := range users {
		if keySpec == "" {
			s.logger.Warnw("No SSH key provided for user, skipping", "username", username)

			continue
		}

		var (
			sshKeys string
			err     error
		)

		// Check if this is a GitHub/GitLab lookup

		switch {
		case strings.HasPrefix(keySpec, "github/"):
			remoteUser := strings.TrimPrefix(keySpec, "github/")

			sshKeys, err = s.fetchSSHKeysFromProvider(httpClient, "github", remoteUser, username, validSSHKeyPrefixes)
			if err != nil {
				s.logger.Warnw("Failed to fetch SSH keys from GitHub, skipping user",
					"username", username,
					"remote_user", remoteUser,
					"error", err)

				continue
			}
		case strings.HasPrefix(keySpec, "gitlab/"):
			remoteUser := strings.TrimPrefix(keySpec, "gitlab/")

			sshKeys, err = s.fetchSSHKeysFromProvider(httpClient, "gitlab", remoteUser, username, validSSHKeyPrefixes)
			if err != nil {
				s.logger.Warnw("Failed to fetch SSH keys from GitLab, skipping user",
					"username", username,
					"remote_user", remoteUser,
					"error", err)

				continue
			}
		default:
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

			err := s.Safe.SetMultiple(usersPath, userData)
			if err != nil {
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
//
//nolint:funlen // SSH key fetch requires multiple provider calls
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
		return "", ErrUnsupportedProvider(provider)
	}

	s.logger.Infow("Fetching SSH keys from provider",
		"username", username,
		"provider", provider,
		"remote_user", remoteUser)

	// Fetch keys
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request for %s: %w", url, err)
	}

	resp, err := client.Do(req) //nolint:gosec // URL is from trusted provider config
	if err != nil {
		return "", fmt.Errorf("failed to fetch keys from %s: %w", url, err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: status %d", ErrHTTPErrorFetchingKeys, resp.StatusCode)
	}

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}

	fetchedKeys := strings.TrimSpace(string(body))
	if fetchedKeys == "" {
		return "", fmt.Errorf("%w: %s", ErrNoKeysReturned, provider)
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
		return "", fmt.Errorf("%w for %s user %s", ErrNoValidSSHKeys, provider, remoteUser)
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
			if len(parts) >= 2 { //nolint:mnd
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
	dnsNS := DefaultDNSServer
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
		"name":     b.blocName + "-ocf-cf-ssh",
		"protocol": "tcp",
		"port":     SSHAltPort,
		"targets":  targets,
	}
}
