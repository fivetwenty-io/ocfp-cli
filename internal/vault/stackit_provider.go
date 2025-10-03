package vault

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/config"
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

	// IP allocation offsets.
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
		s.logger.Infow("Configuring environment", "env_type", envType)

		envPath := s.PathBuilder.GetEnvironmentPath(envType)

		// Configure IaaS components
		err := s.ConfigureIAAS(envPath, envType)
		if err != nil {
			return fmt.Errorf("failed to configure IaaS for %s: %w", envType, err)
		}

		// Configure services
		err = s.ConfigureBlobstores(envPath, envType)
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

		// Configure BOSH-specific components for each environment
		err = s.configureBOSH(envType)
		if err != nil {
			return fmt.Errorf("failed to configure BOSH for %s: %w", envType, err)
		}
	}

	// Configure certificates (shared between environments)
	err = s.ConfigureCertificates("", "")
	if err != nil {
		return fmt.Errorf("failed to configure certificates: %w", err)
	}

	// Configure public IPs (OCF environment only)
	err = s.ConfigurePublicIPs()
	if err != nil {
		return fmt.Errorf("failed to configure public IPs: %w", err)
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

	err := s.Safe.SetMultiple(fqdnPath, data)
	if err != nil {
		return fmt.Errorf("failed to set FQDNs: %w", err)
	}

	return nil
}

// ConfigureIAAS configures IaaS-specific settings.
func (s *StackitVaultProvider) ConfigureIAAS(envPath, envType string) error {
	s.logger.Infow("Configuring IaaS components", "env_type", envType)

	// Configure VPC
	err := s.configureVPC(envType)
	if err != nil {
		return fmt.Errorf("failed to configure VPC: %w", err)
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

// ConfigurePublicIPs configures public IP addresses.
func (s *StackitVaultProvider) ConfigurePublicIPs() error {
	s.logger.Info("Configuring public IPs")

	// This would typically query the STACKIT API for public IPs
	// For now, we'll create placeholder configuration
	publicIPsPath := s.PathBuilder.GetPublicIPsPath()

	// Standard public IP assignments for Cloud Foundry
	publicIPs := map[string]interface{}{
		"cf_router_0":     fmt.Sprintf("router-0-%s.%s", s.BlocName, s.Config.Region),
		"cf_router_1":     fmt.Sprintf("router-1-%s.%s", s.BlocName, s.Config.Region),
		"cf_tcp_router_0": fmt.Sprintf("tcp-router-0-%s.%s", s.BlocName, s.Config.Region),
	}

	// In a real implementation, this would:
	// 1. Query STACKIT API for existing public IPs
	// 2. Match them to CF components based on tags/names
	// 3. Store the actual IP addresses

	err := s.Safe.SetMultiple(publicIPsPath, publicIPs)
	if err != nil {
		return fmt.Errorf("failed to set public IPs: %w", err)
	}

	s.logger.Infow("Public IP configuration completed", "path", publicIPsPath)

	return nil
}

// GetProviderName returns the provider name.
func (s *StackitVaultProvider) GetProviderName() string {
	return "stackit"
}

// SaveConfigToVault saves the OCFP configuration to vault.
func (s *StackitVaultProvider) SaveConfigToVault() error {
	s.logger.Info("Saving OCFP configuration to vault")

	// Convert config to JSON
	jsonConfig, err := json.Marshal(s.Config) //nolint:musttag // Config struct has json tags
	if err != nil {
		return fmt.Errorf("failed to marshal config to JSON: %w", err)
	}

	// Base64 encode (compression removed for simplicity as noted in Perl)
	encoded := base64.StdEncoding.EncodeToString(jsonConfig)

	// Save to vault at secret/config/{bloc}/ocfp:config
	configPath := s.PathBuilder.GetOCFPConfigPath()

	err = s.Safe.Set(configPath, "config", encoded)
	if err != nil {
		return fmt.Errorf("failed to save config to vault: %w", err)
	}

	s.logger.Infow("OCFP configuration saved to vault", "path", configPath)

	return nil
}

// getDatabasesForEnv returns database configuration for an environment.
func (s *StackitVaultProvider) getDatabasesForEnv(envType string) map[string]map[string]interface{} {
	databases := make(map[string]map[string]interface{})

	switch envType {
	case MgmtEnvType:
		databases["bosh"] = map[string]interface{}{
			"hostname":          fmt.Sprintf("postgres-%s-mgmt.%s", s.BlocName, s.Config.Region),
			"postgres_username": "bosh",
			"postgres_password": "((postgres_password))", // Genesis will generate
			"bosh_username":     "bosh",
			"bosh_password":     "((bosh_db_password))",
			"uaa_username":      "uaa",
			"uaa_password":      "((uaa_db_password))",
			"credhub_username":  "credhub",
			"credhub_password":  "((credhub_db_password))",
		}
	case OCFEnvType:
		databases["cf"] = map[string]interface{}{
			"hostname":                      fmt.Sprintf("postgres-%s-cf.%s", s.BlocName, s.Config.Region),
			"postgres_username":             "postgres",
			"postgres_password":             "((postgres_password))",
			"cloud_controller_username":     "cloud_controller",
			"cloud_controller_password":     "((cc_db_password))",
			"diego_username":                "diego",
			"diego_password":                "((diego_db_password))",
			"routing_api_username":          "routing_api",
			"routing_api_password":          "((routing_api_db_password))",
			"uaa_username":                  "uaa",
			"uaa_password":                  "((uaa_db_password))",
			"locket_username":               "locket",
			"locket_password":               "((locket_db_password))",
			"credhub_username":              "credhub",
			"credhub_password":              "((credhub_db_password))",
			"network_policy_username":       "network_policy",
			"network_policy_password":       "((network_policy_db_password))",
			"network_connectivity_username": "network_connectivity",
			"network_connectivity_password": "((network_connectivity_db_password))",
		}
	}

	return databases
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

// calculateSystemIPs calculates IP addresses for system components.
func (s *StackitVaultProvider) calculateSystemIPs(cidr string, envType string) map[string]interface{} {
	// Simplified system IP calculation
	// In the Perl version, this reads from data files to determine system mappings
	cidrParts := strings.Split(cidr, "/")
	network := cidrParts[0]
	networkParts := strings.Split(network, ".")
	baseIP := strings.Join(networkParts[:NetworkPrefix], ".")

	systemIPs := make(map[string]interface{})

	// Standard system IP assignments (simplified)
	switch envType {
	case MgmtEnvType:
		systemIPs["bosh_ip"] = baseIP + ".6"
		systemIPs["jumpbox_ip"] = baseIP + "." + strconv.Itoa(JumpboxOffset)
	case OCFEnvType:
		systemIPs["cf_router_0_ip"] = baseIP + "." + strconv.Itoa(CFRouterOffset)
		systemIPs["cf_router_1_ip"] = baseIP + ".11"
		systemIPs["diego_cell_0_ip"] = baseIP + "." + strconv.Itoa(DiegoCellOffset)
		systemIPs["diego_cell_1_ip"] = baseIP + "." + strconv.Itoa(DiegoCell1Offset)
	}

	return systemIPs
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
	s3Path := s.PathBuilder.GetS3IAMPath(envType)

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

	// This would typically read the actual keypair from the bastion
	// For now, we'll store placeholder information
	keyData := map[string]interface{}{
		"id":           s.BlocName + "-bosh-key",
		"keypair_name": s.BlocName + "-bastion",
		"private":      s.BlocName + "-bastion", // Reference to key name
		"type":         "ssh-rsa",
		"user_id":      s.Config.ProjectID,
	}

	err := s.Safe.SetMultiple(boshKeyPath, keyData)
	if err != nil {
		return fmt.Errorf("failed to set BOSH key data: %w", err)
	}

	return nil
}

// configureRegion configures region settings.
func (s *StackitVaultProvider) configureRegion(envType string) error {
	vpcPath := s.PathBuilder.GetVPCPath(envType)
	regionData := map[string]interface{}{
		"region": s.Config.Region,
	}

	err := s.Safe.SetMultiple(vpcPath, regionData)
	if err != nil {
		return fmt.Errorf("failed to set region data: %w", err)
	}

	return nil
}

// configureSecurityGroups configures security group settings.
func (s *StackitVaultProvider) configureSecurityGroups(envType string) error {
	s.logger.Infow("Configuring security groups", "env_type", envType)

	// Default security group
	defaultSGPath := s.PathBuilder.GetSecurityGroupPath(envType, "default")
	defaultSGData := map[string]interface{}{
		"id":          s.BlocName + "-default",
		"name":        "default",
		"description": "Default security group",
	}

	err := s.Safe.SetMultiple(defaultSGPath, defaultSGData)
	if err != nil {
		return fmt.Errorf("failed to set default security group: %w", err)
	}

	// Environment-specific security group
	envSGPath := s.PathBuilder.GetSecurityGroupPath(envType, envType)
	envSGData := map[string]interface{}{
		"id":          fmt.Sprintf("%s-%s", s.BlocName, envType),
		"name":        envType,
		"description": fmt.Sprintf("Security group for %s environment", envType),
	}

	err = s.Safe.SetMultiple(envSGPath, envSGData)
	if err != nil {
		return fmt.Errorf("failed to set %s security group: %w", envType, err)
	}

	return nil
}

// configureSubnetReservedIPs configures reserved IP addresses for a subnet.
func (s *StackitVaultProvider) configureSubnetReservedIPs(cidr, subnetType string, subnetNum int, envType string) error {
	s.logger.Infow("Configuring reserved IPs", "subnet", fmt.Sprintf("%s-%d", subnetType, subnetNum))

	// This is a simplified implementation - in a real system, you'd calculate
	// system IPs based on the systems configuration from data files
	systemIPs := s.calculateSystemIPs(cidr, envType)

	reservedPath := s.PathBuilder.GetReservedIPsPath(envType, subnetType, subnetNum)

	err := s.Safe.SetMultiple(reservedPath, systemIPs)
	if err != nil {
		return fmt.Errorf("failed to set reserved IPs: %w", err)
	}

	return nil
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
	return map[string]interface{}{
		"id":          fmt.Sprintf("%s-%s-%d", s.BlocName, subnetType, subnetNum),
		"cidr_block":  cidr,
		"cidr_prefix": networkInfo.cidrPrefix,
		"ip_0":        networkInfo.network,
		"ip_n":        networkInfo.lastHost,
		"gateway":     networkInfo.gateway,
		"dns":         s.Config.DNS,
		"az":          availabilityZone,
		"type":        subnetType,
	}
}

// configureVPC configures VPC settings in vault.
func (s *StackitVaultProvider) configureVPC(envType string) error {
	vpcPath := s.PathBuilder.GetVPCPath(envType)

	// STACKIT network configuration
	networkData := map[string]interface{}{
		"id":         s.Config.ProjectID, // Use project ID as network identifier
		"cidr_block": s.Config.Network.CIDR,
		"dns":        s.Config.DNS,
		"region":     s.Config.Region,
	}

	err := s.Safe.SetMultiple(vpcPath, networkData)
	if err != nil {
		return fmt.Errorf("failed to set VPC data: %w", err)
	}

	// Configure availability zones
	for azName, azData := range s.Config.AZs {
		azPath := s.PathBuilder.GetAZPath(envType, azName)

		azInfo := map[string]interface{}{
			"name": azName,
			"zone": azData.Zone,
		}

		err := s.Safe.SetMultiple(azPath, azInfo)
		if err != nil {
			return fmt.Errorf("failed to set AZ data for %s: %w", azName, err)
		}
	}

	s.logger.Infow("VPC configuration completed", "path", vpcPath)

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
