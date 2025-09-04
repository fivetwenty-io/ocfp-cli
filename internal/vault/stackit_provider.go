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

// StackitVaultProvider implements vault operations for STACKIT
type StackitVaultProvider struct {
	*providers.BaseVaultProvider
	Safe        SafeInterface
	PathBuilder *PathBuilder
	logger      *zap.SugaredLogger
}

// NewStackitVaultProvider creates a new STACKIT vault provider
func NewStackitVaultProvider(cfg *config.Config, safe SafeInterface, blocName string) *StackitVaultProvider {
	return &StackitVaultProvider{
		BaseVaultProvider: providers.NewBaseVaultProvider(cfg, blocName),
		Safe:              safe,
		PathBuilder:       NewPathBuilder(cfg, blocName),
		logger:            logger.Get(),
	}
}

// GetProviderName returns the provider name
func (s *StackitVaultProvider) GetProviderName() string {
	return "stackit"
}

// Configure performs full vault configuration for STACKIT
func (s *StackitVaultProvider) Configure() error {
	s.logger.Info("Starting STACKIT vault configuration", "bloc", s.BlocName)

	// Save OCFP configuration to vault first
	if err := s.SaveConfigToVault(); err != nil {
		return fmt.Errorf("failed to save config to vault: %w", err)
	}

	// Configure both management and OCF environments
	for _, envType := range []string{"mgmt", "ocf"} {
		s.logger.Info("Configuring environment", "env_type", envType)

		envPath := s.PathBuilder.GetEnvironmentPath(envType)

		// Configure IaaS components
		if err := s.ConfigureIAAS(envPath, envType); err != nil {
			return fmt.Errorf("failed to configure IaaS for %s: %w", envType, err)
		}

		// Configure services
		if err := s.ConfigureBlobstores(envPath, envType); err != nil {
			return fmt.Errorf("failed to configure blobstores for %s: %w", envType, err)
		}

		if err := s.ConfigureDatabases(envPath, envType); err != nil {
			return fmt.Errorf("failed to configure databases for %s: %w", envType, err)
		}

		if err := s.ConfigureLoadBalancers(envPath, envType); err != nil {
			return fmt.Errorf("failed to configure load balancers for %s: %w", envType, err)
		}

		if err := s.ConfigureFQDNs(envPath, envType); err != nil {
			return fmt.Errorf("failed to configure FQDNs for %s: %w", envType, err)
		}

		// Configure BOSH-specific components for each environment
		boshPath := s.PathBuilder.GetBOSHPath(envType)
		if err := s.configureBOSH(boshPath, envType); err != nil {
			return fmt.Errorf("failed to configure BOSH for %s: %w", envType, err)
		}
	}

	// Configure certificates (shared between environments)
	if err := s.ConfigureCertificates("", ""); err != nil {
		return fmt.Errorf("failed to configure certificates: %w", err)
	}

	// Configure public IPs (OCF environment only)
	if err := s.ConfigurePublicIPs(); err != nil {
		return fmt.Errorf("failed to configure public IPs: %w", err)
	}

	s.logger.Info("STACKIT vault configuration completed", "bloc", s.BlocName)
	return nil
}

// SaveConfigToVault saves the OCFP configuration to vault
func (s *StackitVaultProvider) SaveConfigToVault() error {
	s.logger.Info("Saving OCFP configuration to vault")

	// Convert config to JSON
	jsonConfig, err := json.Marshal(s.Config)
	if err != nil {
		return fmt.Errorf("failed to marshal config to JSON: %w", err)
	}

	// Base64 encode (compression removed for simplicity as noted in Perl)
	encoded := base64.StdEncoding.EncodeToString(jsonConfig)

	// Save to vault at secret/config/{bloc}/ocfp:config
	configPath := s.PathBuilder.GetOCFPConfigPath()
	if err := s.Safe.Set(configPath, "config", encoded); err != nil {
		return fmt.Errorf("failed to save config to vault: %w", err)
	}

	s.logger.Info("OCFP configuration saved to vault", "path", configPath)
	return nil
}

// ConfigureIAAS configures IaaS-specific settings
func (s *StackitVaultProvider) ConfigureIAAS(envPath, envType string) error {
	s.logger.Info("Configuring IaaS components", "env_type", envType)

	// Configure VPC
	if err := s.configureVPC(envPath, envType); err != nil {
		return fmt.Errorf("failed to configure VPC: %w", err)
	}

	// Configure subnets
	if err := s.configureSubnets(envPath, envType); err != nil {
		return fmt.Errorf("failed to configure subnets: %w", err)
	}

	// Configure security groups
	if err := s.configureSecurityGroups(envPath, envType); err != nil {
		return fmt.Errorf("failed to configure security groups: %w", err)
	}

	// Configure region
	if err := s.configureRegion(envPath, envType); err != nil {
		return fmt.Errorf("failed to configure region: %w", err)
	}

	return nil
}

// configureVPC configures VPC settings in vault
func (s *StackitVaultProvider) configureVPC(envPath, envType string) error {
	vpcPath := s.PathBuilder.GetVPCPath(envType)

	// STACKIT network configuration
	networkData := map[string]interface{}{
		"id":         s.Config.ProjectID, // Use project ID as network identifier
		"cidr_block": s.Config.Network.CIDR,
		"dns":        s.Config.DNS,
		"region":     s.Config.Region,
	}

	if err := s.Safe.SetMultiple(vpcPath, networkData); err != nil {
		return fmt.Errorf("failed to set VPC data: %w", err)
	}

	// Configure availability zones
	for azName, azData := range s.Config.AZs {
		azPath := s.PathBuilder.GetAZPath(envType, azName)
		azInfo := map[string]interface{}{
			"name": azName,
			"zone": azData.Zone,
		}
		if err := s.Safe.SetMultiple(azPath, azInfo); err != nil {
			return fmt.Errorf("failed to set AZ data for %s: %w", azName, err)
		}
	}

	s.logger.Info("VPC configuration completed", "path", vpcPath)
	return nil
}

// configureSubnets configures subnet settings in vault
func (s *StackitVaultProvider) configureSubnets(envPath, envType string) error {
	s.logger.Info("Configuring subnets", "env_type", envType)

	subnetsPath := s.PathBuilder.GetSubnetsPath(envType)

	// Process each subnet from configuration
	for i, subnet := range s.Config.Subnets {
		subnetNum := i
		subnetType := subnet.Type
		if subnetType == "" {
			subnetType = "ocfp" // Default subnet type
		}

		subnetPath := s.PathBuilder.GetSubnetPath(envType, subnetType, subnetNum)

		// Parse CIDR for additional information
		cidr := subnet.CIDR
		cidrParts := strings.Split(cidr, "/")
		if len(cidrParts) != 2 {
			return fmt.Errorf("invalid CIDR format: %s", cidr)
		}

		network := cidrParts[0]
		networkParts := strings.Split(network, ".")
		if len(networkParts) != 4 {
			return fmt.Errorf("invalid network address: %s", network)
		}

		// Calculate gateway and network info
		cidrPrefix := strings.Join(networkParts[:3], ".")
		lastOctet, _ := strconv.Atoi(networkParts[3])
		gateway := fmt.Sprintf("%s.%d", cidrPrefix, lastOctet+1)
		lastHost := fmt.Sprintf("%s.254", cidrPrefix)

		// Get AZ for this subnet
		var az string
		if len(s.Config.AZs) > subnetNum {
			azNames := make([]string, 0, len(s.Config.AZs))
			for name := range s.Config.AZs {
				azNames = append(azNames, name)
			}
			if subnetNum < len(azNames) {
				az = azNames[subnetNum]
			}
		}

		subnetData := map[string]interface{}{
			"id":          fmt.Sprintf("%s-%s-%d", s.BlocName, subnetType, subnetNum),
			"cidr_block":  cidr,
			"cidr_prefix": cidrPrefix,
			"ip_0":        network,
			"ip_n":        lastHost,
			"gateway":     gateway,
			"dns":         s.Config.DNS,
			"az":          az,
			"type":        subnetType,
		}

		if err := s.Safe.SetMultiple(subnetPath, subnetData); err != nil {
			return fmt.Errorf("failed to set subnet data for %s-%d: %w", subnetType, subnetNum, err)
		}

		// Configure reserved IPs for ocfp subnets
		if subnetType == "ocfp" {
			if err := s.configureSubnetReservedIPs(cidr, subnetType, subnetNum, envType); err != nil {
				return fmt.Errorf("failed to configure reserved IPs: %w", err)
			}
		}
	}

	s.logger.Info("Subnets configuration completed", "path", subnetsPath)
	return nil
}

// configureSubnetReservedIPs configures reserved IP addresses for a subnet
func (s *StackitVaultProvider) configureSubnetReservedIPs(cidr, subnetType string, subnetNum int, envType string) error {
	s.logger.Info("Configuring reserved IPs", "subnet", fmt.Sprintf("%s-%d", subnetType, subnetNum))

	// This is a simplified implementation - in a real system, you'd calculate
	// system IPs based on the systems configuration from data files
	systemIPs := s.calculateSystemIPs(cidr, subnetType, subnetNum, envType)

	reservedPath := s.PathBuilder.GetReservedIPsPath(envType, subnetType, subnetNum)
	if err := s.Safe.SetMultiple(reservedPath, systemIPs); err != nil {
		return fmt.Errorf("failed to set reserved IPs: %w", err)
	}

	return nil
}

// calculateSystemIPs calculates IP addresses for system components
func (s *StackitVaultProvider) calculateSystemIPs(cidr, subnetType string, subnetNum int, envType string) map[string]interface{} {
	// Simplified system IP calculation
	// In the Perl version, this reads from data files to determine system mappings
	cidrParts := strings.Split(cidr, "/")
	network := cidrParts[0]
	networkParts := strings.Split(network, ".")
	baseIP := strings.Join(networkParts[:3], ".")

	systemIPs := make(map[string]interface{})

	// Standard system IP assignments (simplified)
	switch envType {
	case "mgmt":
		systemIPs["bosh_ip"] = fmt.Sprintf("%s.6", baseIP)
		systemIPs["jumpbox_ip"] = fmt.Sprintf("%s.5", baseIP)
	case "ocf":
		systemIPs["cf_router_0_ip"] = fmt.Sprintf("%s.10", baseIP)
		systemIPs["cf_router_1_ip"] = fmt.Sprintf("%s.11", baseIP)
		systemIPs["diego_cell_0_ip"] = fmt.Sprintf("%s.20", baseIP)
		systemIPs["diego_cell_1_ip"] = fmt.Sprintf("%s.21", baseIP)
	}

	return systemIPs
}

// configureSecurityGroups configures security group settings
func (s *StackitVaultProvider) configureSecurityGroups(envPath, envType string) error {
	s.logger.Info("Configuring security groups", "env_type", envType)

	// Default security group
	defaultSGPath := s.PathBuilder.GetSecurityGroupPath(envType, "default")
	defaultSGData := map[string]interface{}{
		"id":          fmt.Sprintf("%s-default", s.BlocName),
		"name":        "default",
		"description": "Default security group",
	}

	if err := s.Safe.SetMultiple(defaultSGPath, defaultSGData); err != nil {
		return fmt.Errorf("failed to set default security group: %w", err)
	}

	// Environment-specific security group
	envSGPath := s.PathBuilder.GetSecurityGroupPath(envType, envType)
	envSGData := map[string]interface{}{
		"id":          fmt.Sprintf("%s-%s", s.BlocName, envType),
		"name":        envType,
		"description": fmt.Sprintf("Security group for %s environment", envType),
	}

	if err := s.Safe.SetMultiple(envSGPath, envSGData); err != nil {
		return fmt.Errorf("failed to set %s security group: %w", envType, err)
	}

	return nil
}

// configureRegion configures region settings
func (s *StackitVaultProvider) configureRegion(envPath, envType string) error {
	vpcPath := s.PathBuilder.GetVPCPath(envType)
	regionData := map[string]interface{}{
		"region": s.Config.Region,
	}

	if err := s.Safe.SetMultiple(vpcPath, regionData); err != nil {
		return fmt.Errorf("failed to set region data: %w", err)
	}

	return nil
}

// configureBOSH configures BOSH-specific components
func (s *StackitVaultProvider) configureBOSH(boshPath, envType string) error {
	s.logger.Info("Configuring BOSH components", "env_type", envType)

	// Configure IAM
	if err := s.configureIAM(boshPath, envType); err != nil {
		return fmt.Errorf("failed to configure IAM: %w", err)
	}

	// Configure keys
	if err := s.configureKeys(boshPath, envType); err != nil {
		return fmt.Errorf("failed to configure keys: %w", err)
	}

	// Configure KMS (no-op for STACKIT)
	if err := s.configureKMS(boshPath, envType); err != nil {
		return fmt.Errorf("failed to configure KMS: %w", err)
	}

	return nil
}

// configureIAM configures IAM credentials
func (s *StackitVaultProvider) configureIAM(boshPath, envType string) error {
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

	if err := s.Safe.SetMultiple(s3Path, s3Data); err != nil {
		return fmt.Errorf("failed to set S3 IAM credentials: %w", err)
	}

	return nil
}

// configureKeys configures SSH keys
func (s *StackitVaultProvider) configureKeys(boshPath, envType string) error {
	// For STACKIT, we configure the bastion keypair for BOSH
	boshKeyPath := s.PathBuilder.GetBOSHKeyPath(envType)

	// This would typically read the actual keypair from the bastion
	// For now, we'll store placeholder information
	keyData := map[string]interface{}{
		"id":           fmt.Sprintf("%s-bosh-key", s.BlocName),
		"keypair_name": fmt.Sprintf("%s-bastion", s.BlocName),
		"private":      fmt.Sprintf("%s-bastion", s.BlocName), // Reference to key name
		"type":         "ssh-rsa",
		"user_id":      s.Config.ProjectID,
	}

	if err := s.Safe.SetMultiple(boshKeyPath, keyData); err != nil {
		return fmt.Errorf("failed to set BOSH key data: %w", err)
	}

	return nil
}

// configureKMS configures KMS settings (no-op for STACKIT)
func (s *StackitVaultProvider) configureKMS(boshPath, envType string) error {
	// STACKIT doesn't have a native KMS service like AWS
	// This is a no-op but we'll log it
	s.logger.Info("KMS configuration not applicable for STACKIT")
	return nil
}

// ConfigureBlobstores configures blobstore settings
func (s *StackitVaultProvider) ConfigureBlobstores(envPath, envType string) error {
	s.logger.Info("Configuring blobstores", "env_type", envType)

	// STACKIT uses S3-compatible object storage
	// Configure blobstores for different systems
	systems := []string{"bosh"}
	if envType == "ocf" {
		systems = append(systems, "cf")
	}

	for _, system := range systems {
		systemBlobstores := s.getBlobstoresForSystem(system, envType)
		for blobstoreName, blobstoreConfig := range systemBlobstores {
			blobstorePath := s.PathBuilder.GetSystemBlobstorePath(envType, system, blobstoreName)
			if err := s.Safe.SetMultiple(blobstorePath, blobstoreConfig); err != nil {
				return fmt.Errorf("failed to set blobstore %s for %s: %w", blobstoreName, system, err)
			}
		}
	}

	return nil
}

// getBlobstoresForSystem returns blobstore configuration for a system
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

// ConfigureDatabases configures database settings
func (s *StackitVaultProvider) ConfigureDatabases(envPath, envType string) error {
	s.logger.Info("Configuring databases", "env_type", envType)

	// STACKIT database configuration - simplified
	databases := s.getDatabasesForEnv(envType)

	for dbName, dbConfig := range databases {
		dbPath := s.PathBuilder.GetDatabasePath(envType, dbName)
		if err := s.Safe.SetMultiple(dbPath, dbConfig); err != nil {
			return fmt.Errorf("failed to set database %s: %w", dbName, err)
		}
	}

	return nil
}

// getDatabasesForEnv returns database configuration for an environment
func (s *StackitVaultProvider) getDatabasesForEnv(envType string) map[string]map[string]interface{} {
	databases := make(map[string]map[string]interface{})

	switch envType {
	case "mgmt":
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
	case "ocf":
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

// ConfigureLoadBalancers configures load balancer settings
func (s *StackitVaultProvider) ConfigureLoadBalancers(envPath, envType string) error {
	s.logger.Info("Configuring load balancers", "env_type", envType)

	// Export service targets backed by reserved IPs (STACKIT parity) for both envs
	services := s.buildLBServiceTargetsFromState()
	if len(services) > 0 {
		for serviceName, cfg := range services {
			svcPath := s.PathBuilder.GetLoadBalancerPath(envType, serviceName)
			if err := s.Safe.SetMultiple(svcPath, cfg); err != nil {
				return fmt.Errorf("failed to set LB service %s: %w", serviceName, err)
			}
		}
	}

	return nil
}

// (Removed unused getLoadBalancersForEnv helper)

// buildLBServiceTargetsFromState assembles a small set of LB service definitions
// using reserved IPs persisted by bootstrap for STACKIT. Mirrors Perl service mapping.
func (s *StackitVaultProvider) buildLBServiceTargetsFromState() map[string]map[string]interface{} {
	out := map[string]map[string]interface{}{}

	// Load state for bloc
	sm, err := state.NewManager("")
	if err != nil {
		return out
	}
	if _, err := sm.Load(s.BlocName); err != nil {
		return out
	}

	get := func(idx int, key string) string {
		k := fmt.Sprintf("reserved_%s-ocfp-%d_%s", s.BlocName, idx, key)
		v, err := sm.GetOutput(k)
		if err != nil {
			return ""
		}
		if sv, ok := v.(string); ok {
			return sv
		}
		return ""
	}

	// ops-https: includes vault/prometheus/shield from ocfp-0; doomsday from ocfp-1 if present
	opsTargets := []map[string]interface{}{}
	for _, key := range []string{"vault_ip", "prometheus_ip", "shield_ip"} {
		if ip := get(0, key); ip != "" {
			opsTargets = append(opsTargets, map[string]interface{}{"ip": ip, "name": key})
		}
	}
	if ip := get(1, "doomsday_ip"); ip != "" {
		opsTargets = append(opsTargets, map[string]interface{}{"ip": ip, "name": "doomsday_ip"})
	}
	if len(opsTargets) > 0 {
		out["ops-https"] = map[string]interface{}{
			"name":     fmt.Sprintf("%s-ops-https", s.BlocName),
			"protocol": "tcp",
			"port":     443,
			"targets":  opsTargets,
		}
	}

	// Selected mgmt services (single IP)
	addSingle := func(name string, idx int, key string, port int) {
		if ip := get(idx, key); ip != "" {
			out[name] = map[string]interface{}{
				"name":     fmt.Sprintf("%s-%s", s.BlocName, name),
				"protocol": "tcp",
				"port":     port,
				"targets":  []map[string]interface{}{{"ip": ip, "name": key}},
			}
		}
	}
	addSingle("doomsday-mgmt", 1, "doomsday_ip", 8000)
	addSingle("concourse-mgmt", 0, "concourse_ip", 8080)
	addSingle("prometheus-mgmt", 0, "prometheus_ip", 9090)
	addSingle("shield-mgmt", 0, "shield_ip", 443)

	// Shared IP services based on prometheus_ip
	if ip := get(0, "prometheus_ip"); ip != "" {
		for _, svc := range []struct {
			name string
			port int
		}{
			{"alertmanager-mgmt", 9093},
			{"grafana-mgmt", 3000},
		} {
			out[svc.name] = map[string]interface{}{
				"name":     fmt.Sprintf("%s-%s", s.BlocName, svc.name),
				"protocol": "tcp",
				"port":     svc.port,
				"targets":  []map[string]interface{}{{"ip": ip, "name": "prometheus_ip"}},
			}
		}
	}

	// Routers: consume public IP resources by job label
	getPublicIPs := func(job string) []string {
		res, _ := sm.ListResources("public_ip")
		ips := []string{}
		for _, r := range res {
			if r.Tags != nil && r.Tags["job"] == job {
				if addr, ok := r.Properties["address"].(string); ok && addr != "" {
					ips = append(ips, addr)
				}
				continue
			}
			if j, ok := r.Properties["job"].(string); ok && j == job {
				if addr, ok := r.Properties["address"].(string); ok && addr != "" {
					ips = append(ips, addr)
				}
			}
		}
		return ips
	}

	if ips := getPublicIPs("router"); len(ips) > 0 {
		targets := make([]map[string]interface{}, 0, len(ips))
		for i, ip := range ips {
			targets = append(targets, map[string]interface{}{"ip": ip, "name": fmt.Sprintf("router-%d", i)})
		}
		out["router-80"] = map[string]interface{}{
			"name":     fmt.Sprintf("%s-router-80", s.BlocName),
			"protocol": "http",
			"port":     80,
			"targets":  targets,
		}
		out["router-443"] = map[string]interface{}{
			"name":     fmt.Sprintf("%s-router-443", s.BlocName),
			"protocol": "https",
			"port":     443,
			"targets":  targets,
		}
	}

	if ips := getPublicIPs("tcp-router"); len(ips) > 0 {
		targets := make([]map[string]interface{}, 0, len(ips))
		for i, ip := range ips {
			targets = append(targets, map[string]interface{}{"ip": ip, "name": fmt.Sprintf("tcp-router-%d", i)})
		}
		out["tcp-router"] = map[string]interface{}{
			"name":     fmt.Sprintf("%s-tcp-router", s.BlocName),
			"protocol": "tcp",
			"port":     1024,
			"targets":  targets,
		}
	}
	if ips := getPublicIPs("cf-ssh"); len(ips) > 0 {
		targets := make([]map[string]interface{}, 0, len(ips))
		for i, ip := range ips {
			targets = append(targets, map[string]interface{}{"ip": ip, "name": fmt.Sprintf("cf-ssh-%d", i)})
		}
		out["cf-ssh"] = map[string]interface{}{
			"name":     fmt.Sprintf("%s-cf-ssh", s.BlocName),
			"protocol": "tcp",
			"port":     2222,
			"targets":  targets,
		}
	}

	return out
}

// ConfigureFQDNs configures fully qualified domain names
func (s *StackitVaultProvider) ConfigureFQDNs(envPath, envType string) error {
	s.logger.Info("Configuring FQDNs", "env_type", envType)

	// Get FQDNs from configuration
	if s.Config.FQDNs == nil {
		s.logger.Info("No FQDNs configured")
		return nil
	}

	envFQDNs, exists := s.Config.FQDNs[envType]
	if !exists {
		s.logger.Info("No FQDNs configured for environment", "env_type", envType)
		return nil
	}

	fqdnPath := s.PathBuilder.GetFQDNsPath(envType)
	if err := s.Safe.SetMultiple(fqdnPath, envFQDNs.(map[string]interface{})); err != nil {
		return fmt.Errorf("failed to set FQDNs: %w", err)
	}

	return nil
}

// ConfigureCertificates configures TLS certificates
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

	if err := s.Safe.SetMultiple(certsPath, certConfig); err != nil {
		return fmt.Errorf("failed to set certificate configuration: %w", err)
	}

	return nil
}

// ConfigurePublicIPs configures public IP addresses
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

	if err := s.Safe.SetMultiple(publicIPsPath, publicIPs); err != nil {
		return fmt.Errorf("failed to set public IPs: %w", err)
	}

	s.logger.Info("Public IP configuration completed", "path", publicIPsPath)
	return nil
}
