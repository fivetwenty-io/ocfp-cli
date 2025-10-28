package vault

import (
	"bytes"
	"compress/gzip"
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
	// DefaultSubnetType is the default subnet type used for OCFP infrastructure.
	DefaultSubnetType = "ocfp"
)

// AWSVaultProvider implements vault operations for AWS.
// This provider follows the same pattern as StackitVaultProvider to maintain consistency.
type AWSVaultProvider struct {
	*providers.BaseVaultProvider

	Safe        SafeInterface
	PathBuilder *PathBuilder
	logger      *zap.SugaredLogger
}

// NewAWSVaultProvider creates a new AWS vault provider.
func NewAWSVaultProvider(cfg *config.Config, safe SafeInterface, blocName string) *AWSVaultProvider {
	return &AWSVaultProvider{
		BaseVaultProvider: providers.NewBaseVaultProvider(cfg, blocName),
		Safe:              safe,
		PathBuilder:       NewPathBuilder(cfg, blocName),
		logger:            logger.Get(),
	}
}

// Configure performs full vault configuration for AWS.
// This is the main entry point that orchestrates all configuration steps.
func (a *AWSVaultProvider) Configure() error {
	a.logger.Infow("Starting AWS vault configuration", "bloc", a.BlocName)

	// Save OCFP configuration to vault first
	err := a.SaveConfigToVault()
	if err != nil {
		return fmt.Errorf("failed to save config to vault: %w", err)
	}

	// Configure both management and OCF environments
	for _, envType := range []string{"mgmt", "ocf"} {
		//nolint:noinlineerr // error is returned directly from configureEnvironment
		if err := a.configureEnvironment(envType); err != nil {
			return err
		}
	}

	// Configure shared components
	err = a.configureSharedComponents()
	if err != nil {
		return err
	}

	a.logger.Infow("AWS vault configuration completed", "bloc", a.BlocName)

	return nil
}

// ConfigurePublicIPs configures public IP addresses (AWS Elastic IPs).
func (a *AWSVaultProvider) ConfigurePublicIPs() error {
	a.logger.Info("Configuring AWS Elastic IPs")

	publicIPsPath := a.PathBuilder.GetPublicIPsPath()

	// Try to get public IPs from state first
	publicIPs := a.getPublicIPsFromState()

	// If no IPs in state, create placeholder mapping
	if len(publicIPs) == 0 {
		a.logger.Warn("No public IPs found in state, creating placeholder configuration")
		publicIPs = map[string]interface{}{
			"cf_router_0":     fmt.Sprintf("eip-router-0-%s.%s", a.BlocName, a.Config.Region),
			"cf_router_1":     fmt.Sprintf("eip-router-1-%s.%s", a.BlocName, a.Config.Region),
			"cf_tcp_router_0": fmt.Sprintf("eip-tcp-router-0-%s.%s", a.BlocName, a.Config.Region),
		}
	}

	err := a.Safe.SetMultiple(publicIPsPath, publicIPs)
	if err != nil {
		return fmt.Errorf("failed to set public IPs: %w", err)
	}

	a.logger.Infow("Public IP configuration completed", "path", publicIPsPath, "count", len(publicIPs))

	return nil
}

// GetProviderName returns the provider name.
func (a *AWSVaultProvider) GetProviderName() string {
	return "aws"
}

// SaveConfigToVault saves the OCFP configuration to vault.
// Format: Base64(gzip(JSON)) - matches Perl implementation for compatibility.
func (a *AWSVaultProvider) SaveConfigToVault() error {
	a.logger.Info("Saving OCFP configuration to vault")

	// Convert config to JSON
	jsonConfig, err := json.Marshal(a.Config) //nolint:musttag // Config struct has json tags
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
	configPath := a.PathBuilder.GetOCFPConfigPath()

	err = a.Safe.Set(configPath, "config", encoded)
	if err != nil {
		return fmt.Errorf("failed to save config to vault: %w", err)
	}

	a.logger.Infow("OCFP configuration saved to vault", "path", configPath)

	return nil
}

// ConfigureBlobstores configures blobstore settings (AWS S3).
func (a *AWSVaultProvider) ConfigureBlobstores(envPath, envType string) error {
	a.logger.Infow("Configuring S3 blobstores", "env_type", envType)

	// AWS uses S3 for blobstore
	systems := []string{"bosh"}
	if envType == OCFEnvType {
		systems = append(systems, "cf")
	}

	for _, system := range systems {
		systemBlobstores := a.getBlobstoresForSystem(system, envType)
		for blobstoreName, blobstoreConfig := range systemBlobstores {
			blobstorePath := a.PathBuilder.GetSystemBlobstorePath(envType, system, blobstoreName)

			err := a.Safe.SetMultiple(blobstorePath, blobstoreConfig)
			if err != nil {
				return fmt.Errorf("failed to set blobstore %s for %s: %w", blobstoreName, system, err)
			}
		}
	}

	return nil
}

// ConfigureCertificates configures TLS certificates (AWS ACM or Let's Encrypt).
func (a *AWSVaultProvider) ConfigureCertificates(envPath, envType string) error {
	a.logger.Info("Configuring certificates")

	certsPath := a.PathBuilder.GetCertsPath()

	// Certificate configuration for AWS
	// Can use AWS Certificate Manager (ACM) or Let's Encrypt
	certConfig := map[string]interface{}{
		"provider": "acm", // AWS Certificate Manager
		"region":   a.Config.Region,
		"domains":  a.Config.FQDNs,
	}

	err := a.Safe.SetMultiple(certsPath, certConfig)
	if err != nil {
		return fmt.Errorf("failed to set certificate configuration: %w", err)
	}

	return nil
}

// ConfigureDatabases configures database settings (AWS RDS).
func (a *AWSVaultProvider) ConfigureDatabases(envPath, envType string) error {
	a.logger.Infow("Configuring RDS databases", "env_type", envType)

	databases := a.getDatabasesForEnv(envType)

	for dbName, dbConfig := range databases {
		dbPath := a.PathBuilder.GetDatabasePath(envType, dbName)

		err := a.Safe.SetMultiple(dbPath, dbConfig)
		if err != nil {
			return fmt.Errorf("failed to set database %s: %w", dbName, err)
		}
	}

	return nil
}

// ConfigureFQDNs configures fully qualified domain names (AWS Route53).
func (a *AWSVaultProvider) ConfigureFQDNs(envPath, envType string) error {
	a.logger.Infow("Configuring FQDNs", "env_type", envType)

	// Get FQDNs from configuration
	if a.Config.FQDNs == nil {
		a.logger.Info("No FQDNs configured")

		return nil
	}

	envFQDNs, exists := a.Config.FQDNs[envType]
	if !exists {
		a.logger.Infow("No FQDNs configured for environment", "env_type", envType)

		return nil
	}

	fqdnPath := a.PathBuilder.GetFQDNsPath(envType)

	data, ok := envFQDNs.(map[string]interface{})
	if !ok {
		return ErrInvalidFQDNsConfigType(envType, envFQDNs)
	}

	err := a.Safe.SetMultiple(fqdnPath, data)
	if err != nil {
		return fmt.Errorf("failed to set FQDNs: %w", err)
	}

	return nil
}

// ConfigureIAAS configures IaaS-specific settings (AWS VPC, Subnets, Security Groups).
func (a *AWSVaultProvider) ConfigureIAAS(envPath, envType string) error {
	a.logger.Infow("Configuring AWS IaaS components", "env_type", envType)

	// Configure VPC
	err := a.configureVPC(envType)
	if err != nil {
		return fmt.Errorf("failed to configure VPC: %w", err)
	}

	// Configure subnets
	err = a.configureSubnets(envType)
	if err != nil {
		return fmt.Errorf("failed to configure subnets: %w", err)
	}

	// Configure security groups
	err = a.configureSecurityGroups(envType)
	if err != nil {
		return fmt.Errorf("failed to configure security groups: %w", err)
	}

	// Configure region
	err = a.configureRegion(envType)
	if err != nil {
		return fmt.Errorf("failed to configure region: %w", err)
	}

	return nil
}

// ConfigureLoadBalancers configures load balancer settings (AWS ELB/ALB).
func (a *AWSVaultProvider) ConfigureLoadBalancers(envPath, envType string) error {
	a.logger.Infow("Configuring AWS load balancers", "env_type", envType)

	// Export service targets backed by reserved IPs (AWS ELB/ALB)
	services := a.buildLBServiceTargetsFromState()
	if len(services) > 0 {
		for serviceName, cfg := range services {
			svcPath := a.PathBuilder.GetLoadBalancerPath(envType, serviceName)

			err := a.Safe.SetMultiple(svcPath, cfg)
			if err != nil {
				return fmt.Errorf("failed to set LB service %s: %w", serviceName, err)
			}
		}
	}

	return nil
}

// configureEnvironment configures a single environment (mgmt or ocf).
func (a *AWSVaultProvider) configureEnvironment(envType string) error {
	a.logger.Infow("Configuring environment", "env_type", envType)

	envPath := a.PathBuilder.GetEnvironmentPath(envType)

	// Configure IaaS components
	err := a.ConfigureIAAS(envPath, envType)
	if err != nil {
		return fmt.Errorf("failed to configure IaaS for %s: %w", envType, err)
	}

	// Configure services
	err = a.configureServices(envPath, envType)
	if err != nil {
		return err
	}

	// Configure environment-specific components
	err = a.configureEnvironmentComponents(envType)
	if err != nil {
		return err
	}

	return nil
}

// configureServices configures all service components for an environment.
func (a *AWSVaultProvider) configureServices(envPath, envType string) error {
	err := a.ConfigureBlobstores(envPath, envType)
	if err != nil {
		return fmt.Errorf("failed to configure blobstores for %s: %w", envType, err)
	}

	err = a.ConfigureDatabases(envPath, envType)
	if err != nil {
		return fmt.Errorf("failed to configure databases for %s: %w", envType, err)
	}

	err = a.ConfigureLoadBalancers(envPath, envType)
	if err != nil {
		return fmt.Errorf("failed to configure load balancers for %s: %w", envType, err)
	}

	err = a.ConfigureFQDNs(envPath, envType)
	if err != nil {
		return fmt.Errorf("failed to configure FQDNs for %s: %w", envType, err)
	}

	return nil
}

// configureEnvironmentComponents configures environment-specific components.
func (a *AWSVaultProvider) configureEnvironmentComponents(envType string) error {
	// Configure Shield admin credentials
	err := a.configureShield(envType)
	if err != nil {
		return fmt.Errorf("failed to configure Shield for %s: %w", envType, err)
	}

	// Configure CPI settings
	err = a.configureCPI(envType)
	if err != nil {
		return fmt.Errorf("failed to configure CPI for %s: %w", envType, err)
	}

	// Configure policies
	err = a.configurePolicies(envType)
	if err != nil {
		return fmt.Errorf("failed to configure policies for %s: %w", envType, err)
	}

	// Configure users (mgmt only)
	err = a.configureUsers(envType)
	if err != nil {
		return fmt.Errorf("failed to configure users for %s: %w", envType, err)
	}

	// Configure BOSH-specific components
	err = a.configureBOSH(envType)
	if err != nil {
		return fmt.Errorf("failed to configure BOSH for %s: %w", envType, err)
	}

	// Configure BOSH metadata
	err = a.configureBOSHMeta(envType)
	if err != nil {
		return fmt.Errorf("failed to configure BOSH meta for %s: %w", envType, err)
	}

	return nil
}

// configureSharedComponents configures components shared between environments.
func (a *AWSVaultProvider) configureSharedComponents() error {
	// Configure certificates (shared between environments)
	err := a.ConfigureCertificates("", "")
	if err != nil {
		return fmt.Errorf("failed to configure certificates: %w", err)
	}

	// Configure public IPs (OCF environment only)
	err = a.ConfigurePublicIPs()
	if err != nil {
		return fmt.Errorf("failed to configure public IPs: %w", err)
	}

	return nil
}

// configureVPC configures VPC settings in vault.
func (a *AWSVaultProvider) configureVPC(envType string) error {
	vpcPath := a.PathBuilder.GetNetPath(envType)

	// AWS VPC configuration
	// Use VPCCIDRBlock if available, otherwise fall back to Network.CIDR
	cidrBlock := a.Config.VPCCIDRBlock
	if cidrBlock == "" {
		cidrBlock = a.Config.Network.CIDR
	}

	networkData := map[string]interface{}{
		"id":         a.getVPCID(), // VPC ID from state or config
		"cidr_block": cidrBlock,
		"dns":        a.Config.DNS,
		"region":     a.Config.Region,
	}

	err := a.Safe.SetMultiple(vpcPath, networkData)
	if err != nil {
		return fmt.Errorf("failed to set VPC data: %w", err)
	}

	// Configure availability zones
	for azName, azData := range a.Config.AZs {
		azPath := a.PathBuilder.GetAZPath(envType, azName)

		azInfo := map[string]interface{}{
			"name": azName,
			"zone": azData.Zone,
		}

		err := a.Safe.SetMultiple(azPath, azInfo)
		if err != nil {
			return fmt.Errorf("failed to set AZ data for %s: %w", azName, err)
		}
	}

	a.logger.Infow("VPC configuration completed", "path", vpcPath)

	return nil
}

// configureSubnets configures subnet settings in vault.
func (a *AWSVaultProvider) configureSubnets(envType string) error {
	a.logger.Infow("Configuring subnets", "env_type", envType)

	subnetsPath := a.PathBuilder.GetSubnetsPath(envType)

	for i, subnet := range a.Config.Subnets {
		err := a.configureSubnet(envType, i, subnet)
		if err != nil {
			return err
		}
	}

	a.logger.Infow("Subnets configuration completed", "path", subnetsPath)

	return nil
}

// configureSubnet configures a single subnet.
func (a *AWSVaultProvider) configureSubnet(envType string, subnetNum int, subnet config.Subnet) error {
	subnetType := subnet.Type
	if subnetType == "" {
		subnetType = DefaultSubnetType // Default subnet type
	}

	subnetPath := a.PathBuilder.GetSubnetPath(envType, subnetType, subnetNum)

	networkInfo, err := a.parseSubnetCIDR(subnet.CIDR)
	if err != nil {
		return err
	}

	availabilityZone := a.getAvailabilityZone(subnetNum)

	subnetData := a.buildSubnetData(subnetType, subnetNum, subnet.CIDR, networkInfo, availabilityZone)

	err = a.Safe.SetMultiple(subnetPath, subnetData)
	if err != nil {
		return fmt.Errorf("failed to set subnet data for %s-%d: %w", subnetType, subnetNum, err)
	}

	// Configure reserved IPs for ocfp subnets
	if subnetType == DefaultSubnetType {
		err := a.configureSubnetReservedIPs(subnet.CIDR, subnetType, subnetNum, envType)
		if err != nil {
			return fmt.Errorf("failed to configure reserved IPs: %w", err)
		}
	}

	return nil
}

// parseSubnetCIDR parses a CIDR string and extracts network information.
func (a *AWSVaultProvider) parseSubnetCIDR(cidr string) (*subnetNetworkInfo, error) {
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

// getAvailabilityZone returns the availability zone for a subnet by index.
func (a *AWSVaultProvider) getAvailabilityZone(subnetNum int) string {
	if len(a.Config.AZs) <= subnetNum {
		return ""
	}

	azNames := make([]string, 0, len(a.Config.AZs))
	for name := range a.Config.AZs {
		azNames = append(azNames, name)
	}

	if subnetNum < len(azNames) {
		return azNames[subnetNum]
	}

	return ""
}

// buildSubnetData constructs subnet metadata.
func (a *AWSVaultProvider) buildSubnetData(subnetType string, subnetNum int, cidr string, networkInfo *subnetNetworkInfo, availabilityZone string) map[string]interface{} {
	return map[string]interface{}{
		"id":          fmt.Sprintf("%s-%s-%d", a.BlocName, subnetType, subnetNum),
		"cidr_block":  cidr,
		"cidr_prefix": networkInfo.cidrPrefix,
		"ip_0":        networkInfo.network,
		"ip_n":        networkInfo.lastHost,
		"gateway":     networkInfo.gateway,
		"dns":         a.Config.DNS,
		"az":          availabilityZone,
		"type":        subnetType,
	}
}

// configureSubnetReservedIPs configures reserved IP addresses for a subnet.
func (a *AWSVaultProvider) configureSubnetReservedIPs(cidr, subnetType string, subnetNum int, envType string) error {
	a.logger.Infow("Configuring reserved IPs", "subnet", fmt.Sprintf("%s-%d", subnetType, subnetNum))

	systemIPs := a.calculateSystemIPs(cidr, envType)

	reservedPath := a.PathBuilder.GetReservedIPsPath(envType, subnetType, subnetNum)

	err := a.Safe.SetMultiple(reservedPath, systemIPs)
	if err != nil {
		return fmt.Errorf("failed to set reserved IPs: %w", err)
	}

	return nil
}

// calculateSystemIPs calculates IP addresses for system components.
func (a *AWSVaultProvider) calculateSystemIPs(cidr string, envType string) map[string]interface{} {
	cidrParts := strings.Split(cidr, "/")
	network := cidrParts[0]
	networkParts := strings.Split(network, ".")
	baseIP := strings.Join(networkParts[:NetworkPrefix], ".")

	systemIPs := make(map[string]interface{})

	// Standard system IP assignments for AWS
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

// configureSecurityGroups configures security group settings.
func (a *AWSVaultProvider) configureSecurityGroups(envType string) error {
	a.logger.Infow("Configuring security groups", "env_type", envType)

	// Default security group
	defaultSGPath := a.PathBuilder.GetSecurityGroupPath(envType, "default")
	defaultSGData := map[string]interface{}{
		"id":          a.BlocName + "-default",
		"name":        "default",
		"description": "Default security group",
	}

	err := a.Safe.SetMultiple(defaultSGPath, defaultSGData)
	if err != nil {
		return fmt.Errorf("failed to set default security group: %w", err)
	}

	// Environment-specific security group
	envSGPath := a.PathBuilder.GetSecurityGroupPath(envType, envType)
	envSGData := map[string]interface{}{
		"id":          fmt.Sprintf("%s-%s", a.BlocName, envType),
		"name":        envType,
		"description": fmt.Sprintf("Security group for %s environment", envType),
	}

	err = a.Safe.SetMultiple(envSGPath, envSGData)
	if err != nil {
		return fmt.Errorf("failed to set %s security group: %w", envType, err)
	}

	return nil
}

// configureRegion configures region settings.
func (a *AWSVaultProvider) configureRegion(envType string) error {
	vpcPath := a.PathBuilder.GetNetPath(envType)
	regionData := map[string]interface{}{
		"region": a.Config.Region,
	}

	err := a.Safe.SetMultiple(vpcPath, regionData)
	if err != nil {
		return fmt.Errorf("failed to set region data: %w", err)
	}

	return nil
}

// configureBOSH configures BOSH-specific components.
func (a *AWSVaultProvider) configureBOSH(envType string) error {
	a.logger.Infow("Configuring BOSH components", "env_type", envType)

	// Configure IAM
	err := a.configureIAM(envType)
	if err != nil {
		return fmt.Errorf("failed to configure IAM: %w", err)
	}

	// Configure keys
	err = a.configureKeys(envType)
	if err != nil {
		return fmt.Errorf("failed to configure keys: %w", err)
	}

	// Configure KMS
	a.configureKMS(envType)

	return nil
}

// configureIAM configures AWS IAM credentials.
func (a *AWSVaultProvider) configureIAM(envType string) error {
	s3Path := a.PathBuilder.GetS3Path(envType)

	// Use AWS credentials from config
	accessKey := a.Config.AccessKeyID
	secretKey := a.Config.SecretAccessKey

	if accessKey == "" || secretKey == "" {
		a.logger.Warn("No AWS credentials found for S3 access")

		return nil
	}

	s3Data := map[string]interface{}{
		"access_key": accessKey,
		"secret_key": secretKey,
	}

	// Add session token if available
	if a.Config.SessionToken != "" {
		s3Data["session_token"] = a.Config.SessionToken
	}

	err := a.Safe.SetMultiple(s3Path, s3Data)
	if err != nil {
		return fmt.Errorf("failed to set S3 IAM credentials: %w", err)
	}

	return nil
}

// configureKMS configures AWS KMS settings.
func (a *AWSVaultProvider) configureKMS(envType string) {
	// AWS KMS configuration
	// For now, this is a placeholder
	// In a full implementation, this would configure KMS keys for BOSH
	a.logger.Infow("KMS configuration placeholder", "env_type", envType)
}

// configureKeys configures SSH keys.
func (a *AWSVaultProvider) configureKeys(envType string) error {
	boshKeyPath := a.PathBuilder.GetBOSHKeyPath(envType)

	// Read the actual keypair from config if available
	keypairName := a.BlocName + "-bastion"

	keyData := map[string]interface{}{
		"id":           a.BlocName + "-bosh-key",
		"keypair_name": keypairName,
		"private":      keypairName, // Reference to key name
		"type":         "ssh-rsa",
	}

	// Try to get the actual private key from config
	if privateKey, exists := a.Config.Keys[keypairName]; exists && privateKey != "" {
		keyData["private_key_material"] = privateKey
	}

	err := a.Safe.SetMultiple(boshKeyPath, keyData)
	if err != nil {
		return fmt.Errorf("failed to set BOSH key data: %w", err)
	}

	return nil
}

// configureBOSHMeta configures BOSH metadata information.
func (a *AWSVaultProvider) configureBOSHMeta(envType string) error {
	a.logger.Infow("Configuring BOSH meta information", "env_type", envType)

	boshPath := a.PathBuilder.GetBOSHPath(envType)

	// Parse DNS servers from config (defaults to 1.1.1.1)
	dnsNS := "1.1.1.1"
	if len(a.Config.DNS) > 0 {
		dnsNS = strings.Join(a.Config.DNS, ",")
	}

	dnsServers := strings.Split(dnsNS, ",")

	boshMeta := make(map[string]interface{})

	// Store DNS servers as dns.0, dns.1, etc.
	for i, dns := range dnsServers {
		boshMeta[fmt.Sprintf("dns.%d", i)] = strings.TrimSpace(dns)
	}

	// Store key name
	keyName := a.BlocName + "-bastion"
	boshMeta["key_name"] = keyName

	// Store region and availability zone
	boshMeta["region"] = a.Config.Region
	// Default to first AZ
	if len(a.Config.AZs) > 0 {
		for azName := range a.Config.AZs {
			boshMeta["az"] = azName

			break
		}
	} else {
		boshMeta["az"] = a.Config.Region + "a" // Default to first AZ
	}

	// Try to get the private key from the keys/bosh path if it exists
	boshKeysPath := a.PathBuilder.GetBOSHKeyPath(envType)

	boshKeysData, err := a.Safe.GetAll(boshKeysPath)
	if err == nil && boshKeysData != nil {
		if privateKey, ok := boshKeysData["private"]; ok {
			boshMeta["private_key"] = privateKey

			a.logger.Debug("Found and included BOSH private key in meta information")
		}
	}

	// Write to vault at the bosh path
	if len(boshMeta) > 0 {
		err := a.Safe.SetMultiple(boshPath, boshMeta)
		if err != nil {
			return fmt.Errorf("failed to set BOSH meta information: %w", err)
		}

		a.logger.Infow("Successfully configured BOSH meta information", "path", boshPath)
	}

	return nil
}

// configureShield configures Shield admin credentials for an environment.
func (a *AWSVaultProvider) configureShield(envType string) error {
	a.logger.Infow("Configuring Shield admin credentials", "env_type", envType)

	shieldAdminPath := a.PathBuilder.GetEnvironmentPath(envType) + "/shield/admin"

	// Set default Shield admin credentials
	shieldAdminCreds := map[string]interface{}{
		"username": "shieldadmin",
		"password": fmt.Sprintf("shield-password-%s-%s", envType, a.BlocName),
	}

	err := a.Safe.SetMultiple(shieldAdminPath, shieldAdminCreds)
	if err != nil {
		return fmt.Errorf("failed to set Shield admin credentials: %w", err)
	}

	a.logger.Infow("Successfully configured Shield admin credentials", "path", shieldAdminPath)

	return nil
}

// configureCPI configures AWS CPI configuration for an environment.
func (a *AWSVaultProvider) configureCPI(envType string) error {
	a.logger.Infow("Configuring AWS CPI credentials", "env_type", envType)

	cpiPath := a.PathBuilder.GetEnvironmentPath(envType) + "/cpi/aws"

	// Build CPI configuration
	cpiConfig := map[string]interface{}{
		"access_key_id":           a.Config.AccessKeyID,
		"secret_access_key":       a.Config.SecretAccessKey,
		"region":                  a.Config.Region,
		"default_region":          a.Config.Region,
		"default_key_name":        a.BlocName + "-bastion",
		"default_security_groups": fmt.Sprintf(`["default","%s-%s"]`, a.BlocName, DefaultSubnetType),
	}

	// Add session token if available
	if a.Config.SessionToken != "" {
		cpiConfig["session_token"] = a.Config.SessionToken
	}

	// Check for missing required fields
	missingFields := []string{}

	if a.Config.AccessKeyID == "" {
		missingFields = append(missingFields, "access_key_id")
	}

	if a.Config.SecretAccessKey == "" {
		missingFields = append(missingFields, "secret_access_key")
	}

	if a.Config.Region == "" {
		missingFields = append(missingFields, "region")
	}

	if len(missingFields) > 0 {
		a.logger.Warnw("Missing required CPI configuration fields", "env_type", envType, "missing", strings.Join(missingFields, ", "))
		a.logger.Infow("CPI configuration may be incomplete", "env_type", envType)
	}

	err := a.Safe.SetMultiple(cpiPath, cpiConfig)
	if err != nil {
		return fmt.Errorf("failed to set CPI configuration: %w", err)
	}

	a.logger.Infow("Successfully configured AWS CPI credentials", "env_type", envType)

	return nil
}

// configurePolicies configures deployment policies for an environment.
func (a *AWSVaultProvider) configurePolicies(envType string) error {
	a.logger.Infow("Configuring policies", "env_type", envType)

	policiesPath := a.PathBuilder.GetEnvironmentPath(envType) + "/policies"

	// Set default policy values
	userProvidedBoshCreds := "allow" // ignore, allow, require
	deploymentChangeReasonSize := 0

	policies := map[string]interface{}{
		"user_provided_bosh_creds":               userProvidedBoshCreds,
		"deployment_change_reason_required_size": deploymentChangeReasonSize,
	}

	err := a.Safe.SetMultiple(policiesPath, policies)
	if err != nil {
		return fmt.Errorf("failed to set policies: %w", err)
	}

	a.logger.Infow("Configured policies", "env_type", envType,
		"user_provided_bosh_creds", userProvidedBoshCreds,
		"deployment_change_reason_required_size", deploymentChangeReasonSize)

	return nil
}

// configureUsers configures jumpbox users for the management environment.
//
//nolint:unparam // error return for future implementation; maintains interface consistency
func (a *AWSVaultProvider) configureUsers(envType string) error {
	// Users configuration is only relevant for mgmt environment (jumpbox)
	if envType != MgmtEnvType {
		return nil
	}

	a.logger.Infow("Configuring jumpbox users", "env_type", envType)

	usersPath := a.PathBuilder.GetEnvironmentPath(envType) + "/jumpbox/users"

	// Placeholder - in real implementation, this would read users from config
	a.logger.Infow("Jumpbox users path configured", "path", usersPath)

	return nil
}

// getDatabasesForEnv returns database configuration for an environment.
func (a *AWSVaultProvider) getDatabasesForEnv(envType string) map[string]map[string]interface{} {
	// AWS RDS-specific hostname formatter
	hostnameFormatter := func(env string) string {
		switch env {
		case MgmtEnvType:
			return fmt.Sprintf("%s-mgmt-postgres.%s.rds.amazonaws.com", a.BlocName, a.Config.Region)
		case OCFEnvType:
			return fmt.Sprintf("%s-ocf-postgres.%s.rds.amazonaws.com", a.BlocName, a.Config.Region)
		default:
			return ""
		}
	}

	return BuildDatabasesForEnv(envType, hostnameFormatter)
}

// getBlobstoresForSystem returns blobstore configuration for a system.
func (a *AWSVaultProvider) getBlobstoresForSystem(system, envType string) map[string]map[string]interface{} {
	blobstores := make(map[string]map[string]interface{})

	// AWS S3 bucket naming
	switch system {
	case "bosh":
		blobstores["artifacts"] = map[string]interface{}{
			"name":   fmt.Sprintf("%s-%s-bosh-artifacts", a.BlocName, envType),
			"region": a.Config.Region,
			"type":   "s3",
		}
	case "cf":
		cfBlobstores := []string{"buildpacks", "droplets", "packages", "resources"}
		for _, name := range cfBlobstores {
			blobstores[name] = map[string]interface{}{
				"name":   fmt.Sprintf("%s-%s-cf-%s", a.BlocName, envType, name),
				"region": a.Config.Region,
				"type":   "s3",
			}
		}
	}

	return blobstores
}

// buildLBServiceTargetsFromState assembles load balancer service definitions using state.
func (a *AWSVaultProvider) buildLBServiceTargetsFromState() map[string]map[string]interface{} {
	stateManager := a.loadStateManager()
	if stateManager == nil {
		return map[string]map[string]interface{}{}
	}

	builder := &awsLBServiceBuilder{
		stateManager: stateManager,
		blocName:     a.BlocName,
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

// loadStateManager loads the state manager for this bloc.
func (a *AWSVaultProvider) loadStateManager() *state.Manager {
	// Get standard state directory for this bloc
	stateDir, err := state.GetStateDir(a.BlocName)
	if err != nil {
		return nil
	}

	stateManager, err := state.NewManager(stateDir)
	if err != nil {
		return nil
	}

	_, err = stateManager.Load(a.BlocName)
	if err != nil {
		return nil
	}

	return stateManager
}

// getVPCID returns the VPC ID from state or config.
func (a *AWSVaultProvider) getVPCID() string {
	// Try to get VPC ID from state
	stateManager := a.loadStateManager()
	if stateManager != nil {
		vpcID, err := stateManager.GetOutput("vpc_id")
		if err == nil {
			if id, ok := vpcID.(string); ok && id != "" {
				return id
			}
		}
	}

	// Fallback to using bloc name as VPC identifier
	return a.BlocName + "-vpc"
}

// getPublicIPsFromState retrieves public IPs from state.
func (a *AWSVaultProvider) getPublicIPsFromState() map[string]interface{} {
	stateManager := a.loadStateManager()
	if stateManager == nil {
		return nil
	}

	publicIPs := make(map[string]interface{})

	// Try to get router IPs
	if routerIPs := a.getPublicIPsByJob(stateManager, "router"); len(routerIPs) > 0 {
		for i, ip := range routerIPs {
			publicIPs[fmt.Sprintf("cf_router_%d", i)] = ip
		}
	}

	// Try to get TCP router IPs
	if tcpRouterIPs := a.getPublicIPsByJob(stateManager, "tcp-router"); len(tcpRouterIPs) > 0 {
		for i, ip := range tcpRouterIPs {
			publicIPs[fmt.Sprintf("cf_tcp_router_%d", i)] = ip
		}
	}

	// Try to get CF SSH IPs
	if sshIPs := a.getPublicIPsByJob(stateManager, "cf-ssh"); len(sshIPs) > 0 {
		for i, ip := range sshIPs {
			publicIPs[fmt.Sprintf("cf_ssh_%d", i)] = ip
		}
	}

	return publicIPs
}

// getPublicIPsByJob retrieves public IPs for a specific job from state.
func (a *AWSVaultProvider) getPublicIPsByJob(stateManager *state.Manager, job string) []string {
	res, _ := stateManager.ListResources("public_ip")
	ips := []string{}

	for _, resource := range res {
		if a.resourceMatchesJob(*resource, job) {
			if addr, ok := resource.Properties["address"].(string); ok && addr != "" {
				ips = append(ips, addr)
			}
		}
	}

	return ips
}

// resourceMatchesJob checks if a resource matches a specific job.
func (a *AWSVaultProvider) resourceMatchesJob(resource state.Resource, job string) bool {
	if resource.Tags != nil && resource.Tags["job"] == job {
		return true
	}

	if j, ok := resource.Properties["job"].(string); ok && j == job {
		return true
	}

	return false
}

// awsLBServiceBuilder builds load balancer service configurations.
type awsLBServiceBuilder struct {
	stateManager *state.Manager
	blocName     string
	services     map[string]map[string]interface{}
}

func (b *awsLBServiceBuilder) getReservedIP(idx int, key string) string {
	k := fmt.Sprintf("reserved_%s-%s-%d_%s", b.blocName, DefaultSubnetType, idx, key)

	v, err := b.stateManager.GetOutput(k)
	if err != nil {
		return ""
	}

	if sv, ok := v.(string); ok {
		return sv
	}

	return ""
}

func (b *awsLBServiceBuilder) addOpsHTTPSService() {
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

func (b *awsLBServiceBuilder) addManagementServices() {
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

func (b *awsLBServiceBuilder) addPrometheusSharedServices() {
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

func (b *awsLBServiceBuilder) getPublicIPsByJob(job string) []string {
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

func (b *awsLBServiceBuilder) resourceMatchesJob(resource state.Resource, job string) bool {
	if resource.Tags != nil && resource.Tags["job"] == job {
		return true
	}

	if j, ok := resource.Properties["job"].(string); ok && j == job {
		return true
	}

	return false
}

func (b *awsLBServiceBuilder) buildTargetsFromIPs(ips []string, prefix string) []map[string]interface{} {
	targets := make([]map[string]interface{}, 0, len(ips))
	for i, ip := range ips {
		targets = append(targets, map[string]interface{}{
			"ip":   ip,
			"name": fmt.Sprintf("%s-%d", prefix, i),
		})
	}

	return targets
}

func (b *awsLBServiceBuilder) addRouterServices() {
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

func (b *awsLBServiceBuilder) addTCPRouterService() {
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

func (b *awsLBServiceBuilder) addCFSSHService() {
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
