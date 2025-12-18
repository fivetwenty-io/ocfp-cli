package vault

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	gcpcpi "github.com/ocfp/ocfp-cli-go/internal/cpi/gcp"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/providers"
	"go.uber.org/zap"
)

// GCPVaultProvider implements vault operations for GCP.
type GCPVaultProvider struct {
	*providers.BaseVaultProvider

	Safe        SafeInterface
	PathBuilder *PathBuilder
	logger      *zap.SugaredLogger
}

// NewGCPVaultProvider creates a new GCP vault provider.
func NewGCPVaultProvider(cfg *config.Config, safe SafeInterface, blocName string) *GCPVaultProvider {
	return &GCPVaultProvider{
		BaseVaultProvider: providers.NewBaseVaultProvider(cfg, blocName),
		Safe:              safe,
		PathBuilder:       NewPathBuilder(cfg, blocName),
		logger:            logger.Get(),
	}
}

// Configure performs full vault configuration for GCP.
func (g *GCPVaultProvider) Configure(reporter providers.ProgressReporter) error {
	g.logger.Infow("Starting GCP vault configuration", "bloc", g.BlocName)

	// Track phase numbers across entire configuration
	phaseIndex := 0

	// Total phases: 1 (config) + 7 (mgmt) + 7 (ocf) + 2 (shared) = 17
	totalPhases := 17

	// Save OCFP configuration to vault first
	err := g.SaveConfigToVault(reporter, phaseIndex, totalPhases)
	if err != nil {
		return fmt.Errorf("failed to save config to vault: %w", err)
	}
	phaseIndex++

	// Configure both management and OCF environments
	for _, envType := range []string{"mgmt", "ocf"} {
		if err := g.configureEnvironment(envType, reporter, &phaseIndex, totalPhases); err != nil {
			return err
		}
	}

	// Configure shared components
	err = g.configureSharedComponents(reporter, &phaseIndex, totalPhases)
	if err != nil {
		return err
	}

	// Report final summary
	if reporter != nil {
		reporter.ReportFinalSummary(true, 0, totalPhases, 0)
	}

	g.logger.Infow("GCP vault configuration completed", "bloc", g.BlocName)

	return nil
}

// configureEnvironment configures all components for a given environment type.
func (g *GCPVaultProvider) configureEnvironment(envType string, reporter providers.ProgressReporter, phaseIndex *int, totalPhases int) error {
	envPath := g.PathBuilder.GetEnvironmentPath(envType)

	// Configure networks
	if err := g.ConfigureIAAS(envPath, envType, reporter, phaseIndex, totalPhases); err != nil {
		return fmt.Errorf("failed to configure IAAS for %s: %w", envType, err)
	}
	*phaseIndex++

	// Configure subnets
	if err := g.configureSubnets(envPath, envType, reporter, *phaseIndex, totalPhases); err != nil {
		return fmt.Errorf("failed to configure subnets for %s: %w", envType, err)
	}
	*phaseIndex++

	// Configure security groups
	if err := g.configureSecurityGroups(envPath, envType, reporter, *phaseIndex, totalPhases); err != nil {
		return fmt.Errorf("failed to configure security groups for %s: %w", envType, err)
	}
	*phaseIndex++

	// Configure blobstores
	if err := g.ConfigureBlobstores(envPath, envType, reporter, *phaseIndex, totalPhases); err != nil {
		return fmt.Errorf("failed to configure blobstores for %s: %w", envType, err)
	}
	*phaseIndex++

	// Configure databases
	if err := g.ConfigureDatabases(envPath, envType, reporter, *phaseIndex, totalPhases); err != nil {
		return fmt.Errorf("failed to configure databases for %s: %w", envType, err)
	}
	*phaseIndex++

	// Configure load balancers
	if err := g.ConfigureLoadBalancers(envPath, envType, reporter, *phaseIndex, totalPhases); err != nil {
		return fmt.Errorf("failed to configure load balancers for %s: %w", envType, err)
	}
	*phaseIndex++

	// Configure FQDNs
	if err := g.ConfigureFQDNs(envPath, envType, reporter, *phaseIndex, totalPhases); err != nil {
		return fmt.Errorf("failed to configure FQDNs for %s: %w", envType, err)
	}
	*phaseIndex++

	return nil
}

// ConfigureIAAS configures GCP-specific IAAS settings.
func (g *GCPVaultProvider) ConfigureIAAS(envPath, envType string, reporter providers.ProgressReporter, phaseNum *int, totalPhases int) error {
	currentPhase := 0
	if phaseNum != nil {
		currentPhase = *phaseNum
	}
	phaseName := fmt.Sprintf("networks-%s", envType)
	phaseStart := time.Now()

	if reporter != nil {
		reporter.ReportPhaseStart(phaseName, currentPhase, totalPhases)
	}

	g.logger.Infow("Configuring GCP IAAS", "env_type", envType)

	// Configure GCP CPI settings
	cpiPath := fmt.Sprintf("secret/config/%s/%s/cpi/gcp", g.BlocName, envType)

	cpiConfig := map[string]interface{}{
		"project":                  g.Config.ProjectID,
		"zone":                     g.Config.Region, // GCP uses zone for compute
		"json_key":                 g.Config.ServiceAccountJSON,
		"default_key_name":         fmt.Sprintf("%s-bastion", g.BlocName),
		"default_security_groups":  fmt.Sprintf(`["%s-default","%s-ocfp"]`, g.BlocName, g.BlocName),
	}

	if err := g.Safe.SetMultiple(cpiPath, cpiConfig); err != nil {
		return fmt.Errorf("failed to set CPI config: %w", err)
	}

	// Configure VPC network
	networkPath := g.PathBuilder.GetNetPath(envType)
	networkConfig := map[string]interface{}{
		"name":   fmt.Sprintf("%s-%s-network", g.BlocName, envType),
		"region": gcpcpi.GetRegionFromZone(g.Config.Region),
		"zone":   g.Config.Region,
	}

	if err := g.Safe.SetMultiple(networkPath, networkConfig); err != nil {
		return fmt.Errorf("failed to set network config: %w", err)
	}

	if reporter != nil {
		reporter.ReportPhaseComplete(phaseName, time.Since(phaseStart))
	}

	return nil
}

// configureSubnets configures subnet settings.
func (g *GCPVaultProvider) configureSubnets(envPath, envType string, reporter providers.ProgressReporter, phaseNum int, totalPhases int) error {
	currentPhase := phaseNum
	phaseName := fmt.Sprintf("subnets-%s", envType)
	phaseStart := time.Now()

	if reporter != nil {
		reporter.ReportPhaseStart(phaseName, currentPhase, totalPhases)
	}

	g.logger.Infow("Configuring subnets", "env_type", envType)

	// Standard subnet configuration for GCP
	subnets := []struct {
		name string
		cidr string
	}{
		{"compilation", "10.0.0.0/24"},
		{"core", "10.0.1.0/24"},
		{"edge", "10.0.2.0/24"},
	}

	for i, subnet := range subnets {
		if reporter != nil {
			reporter.ReportSubtaskProgress(phaseName, i+1, len(subnets), fmt.Sprintf("Writing %s subnet", subnet.name))
		}

		subnetPath := fmt.Sprintf("%s/subnets/%s", envPath, subnet.name)
		subnetConfig := map[string]interface{}{
			"name":                     fmt.Sprintf("%s-%s-%s", g.BlocName, envType, subnet.name),
			"cidr":                     subnet.cidr,
			"region":                   gcpcpi.GetRegionFromZone(g.Config.Region),
			"private_google_access":   true,
		}

		if err := g.Safe.SetMultiple(subnetPath, subnetConfig); err != nil {
			return fmt.Errorf("failed to set subnet %s: %w", subnet.name, err)
		}
	}

	if reporter != nil {
		reporter.ReportPhaseComplete(phaseName, time.Since(phaseStart))
	}

	return nil
}

// configureSecurityGroups configures firewall rules.
func (g *GCPVaultProvider) configureSecurityGroups(envPath, envType string, reporter providers.ProgressReporter, phaseNum int, totalPhases int) error {
	currentPhase := phaseNum
	phaseName := fmt.Sprintf("security-groups-%s", envType)
	phaseStart := time.Now()

	if reporter != nil {
		reporter.ReportPhaseStart(phaseName, currentPhase, totalPhases)
	}

	g.logger.Infow("Configuring security groups (firewall rules)", "env_type", envType)

	// GCP uses firewall rules with network tags
	securityGroups := []struct {
		name        string
		description string
	}{
		{"default", "Default firewall rules"},
		{"ocfp", "OCFP management firewall rules"},
		{"bosh", "BOSH director firewall rules"},
	}

	if envType == OCFEnvType {
		securityGroups = append(securityGroups, struct {
			name        string
			description string
		}{"cf", "Cloud Foundry firewall rules"})
	}

	for i, sg := range securityGroups {
		if reporter != nil {
			reporter.ReportSubtaskProgress(phaseName, i+1, len(securityGroups), fmt.Sprintf("Writing %s", sg.name))
		}

		sgPath := fmt.Sprintf("%s/security-groups/%s", envPath, sg.name)
		sgConfig := map[string]interface{}{
			"name":        fmt.Sprintf("%s-%s", g.BlocName, sg.name),
			"description": sg.description,
			"network_tag": fmt.Sprintf("sg-%s-%s", g.BlocName, sg.name),
		}

		if err := g.Safe.SetMultiple(sgPath, sgConfig); err != nil {
			return fmt.Errorf("failed to set security group %s: %w", sg.name, err)
		}
	}

	if reporter != nil {
		reporter.ReportPhaseComplete(phaseName, time.Since(phaseStart))
	}

	return nil
}

// ConfigureBlobstores configures Cloud Storage bucket settings.
func (g *GCPVaultProvider) ConfigureBlobstores(envPath, envType string, reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
	currentPhase := phaseNum
	phaseName := fmt.Sprintf("blobstores-%s", envType)
	phaseStart := time.Now()

	if reporter != nil {
		reporter.ReportPhaseStart(phaseName, currentPhase, totalPhases)
	}

	g.logger.Infow("Configuring blobstores (Cloud Storage)", "env_type", envType)

	// Configure blobstores for different systems
	systems := []string{"bosh"}
	if envType == OCFEnvType {
		systems = append(systems, "cf")
	}

	totalBlobstores := 0
	for _, system := range systems {
		systemBlobstores := g.getBlobstoresForSystem(system, envType)
		totalBlobstores += len(systemBlobstores)
	}

	currentBlobstore := 0
	for _, system := range systems {
		systemBlobstores := g.getBlobstoresForSystem(system, envType)
		for blobstoreName, blobstoreConfig := range systemBlobstores {
			currentBlobstore++

			if reporter != nil {
				label := fmt.Sprintf("Writing %s/%s", system, blobstoreName)
				reporter.ReportSubtaskProgress(phaseName, currentBlobstore, totalBlobstores, label)
			}

			blobstorePath := g.PathBuilder.GetSystemBlobstorePath(envType, system, blobstoreName)

			if err := g.Safe.SetMultiple(blobstorePath, blobstoreConfig); err != nil {
				return fmt.Errorf("failed to set blobstore %s for %s: %w", blobstoreName, system, err)
			}
		}
	}

	if reporter != nil {
		reporter.ReportPhaseComplete(phaseName, time.Since(phaseStart))
	}

	return nil
}

// getBlobstoresForSystem returns blobstore configuration for a system.
func (g *GCPVaultProvider) getBlobstoresForSystem(system, envType string) map[string]map[string]interface{} {
	blobstores := make(map[string]map[string]interface{})
	region := gcpcpi.GetRegionFromZone(g.Config.Region)

	switch system {
	case "bosh":
		blobstores["artifacts"] = map[string]interface{}{
			"bucket_name":   fmt.Sprintf("%s-%s-bosh-artifacts", g.BlocName, envType),
			"region":        region,
			"storage_class": "STANDARD",
		}
	case "cf":
		cfBlobstores := []string{"buildpacks", "droplets", "packages", "resources"}
		for _, name := range cfBlobstores {
			blobstores[name] = map[string]interface{}{
				"bucket_name":   fmt.Sprintf("%s-%s-cf-%s", g.BlocName, envType, name),
				"region":        region,
				"storage_class": "STANDARD",
			}
		}
	}

	return blobstores
}

// ConfigureDatabases configures Cloud SQL settings.
func (g *GCPVaultProvider) ConfigureDatabases(envPath, envType string, reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
	currentPhase := phaseNum
	phaseName := fmt.Sprintf("databases-%s", envType)
	phaseStart := time.Now()

	if reporter != nil {
		reporter.ReportPhaseStart(phaseName, currentPhase, totalPhases)
	}

	g.logger.Infow("Configuring databases (Cloud SQL)", "env_type", envType)

	if reporter != nil {
		reporter.ReportSubtaskProgress(phaseName, 1, 1, "Writing database configuration")
	}

	dbPath := fmt.Sprintf("%s/databases/postgres", envPath)
	region := gcpcpi.GetRegionFromZone(g.Config.Region)

	dbConfig := map[string]interface{}{
		"hostname": fmt.Sprintf("%s-%s-postgres.%s.sql.gcloud.com", g.BlocName, envType, region),
		"port":     5432,
		"type":     "postgres",
		"region":   region,
	}

	if err := g.Safe.SetMultiple(dbPath, dbConfig); err != nil {
		return fmt.Errorf("failed to set database config: %w", err)
	}

	if reporter != nil {
		reporter.ReportPhaseComplete(phaseName, time.Since(phaseStart))
	}

	return nil
}

// ConfigureLoadBalancers configures Cloud Load Balancing settings.
func (g *GCPVaultProvider) ConfigureLoadBalancers(envPath, envType string, reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
	currentPhase := phaseNum
	phaseName := fmt.Sprintf("load-balancers-%s", envType)
	phaseStart := time.Now()

	if reporter != nil {
		reporter.ReportPhaseStart(phaseName, currentPhase, totalPhases)
	}

	g.logger.Infow("Configuring load balancers", "env_type", envType)

	// Define load balancers based on environment type
	loadBalancers := g.getLoadBalancersForEnv(envType)

	for i, lb := range loadBalancers {
		if reporter != nil {
			reporter.ReportSubtaskProgress(phaseName, i+1, len(loadBalancers), fmt.Sprintf("Writing %s", lb.name))
		}

		lbPath := fmt.Sprintf("%s/load-balancers/%s", envPath, lb.name)
		lbConfig := map[string]interface{}{
			"name":    fmt.Sprintf("%s-%s-%s", g.BlocName, envType, lb.name),
			"type":    lb.lbType,
			"port":    lb.port,
			"backend": lb.backend,
		}

		if err := g.Safe.SetMultiple(lbPath, lbConfig); err != nil {
			return fmt.Errorf("failed to set load balancer %s: %w", lb.name, err)
		}
	}

	if reporter != nil {
		reporter.ReportPhaseComplete(phaseName, time.Since(phaseStart))
	}

	return nil
}

type loadBalancerConfig struct {
	name    string
	lbType  string
	port    int
	backend string
}

func (g *GCPVaultProvider) getLoadBalancersForEnv(envType string) []loadBalancerConfig {
	var lbs []loadBalancerConfig

	if envType == MgmtEnvType {
		lbs = []loadBalancerConfig{
			{name: "concourse", lbType: "https", port: 443, backend: "concourse"},
			{name: "vault", lbType: "https", port: 443, backend: "vault"},
			{name: "prometheus", lbType: "https", port: 443, backend: "prometheus"},
		}
	} else {
		lbs = []loadBalancerConfig{
			{name: "router", lbType: "https", port: 443, backend: "router"},
			{name: "tcp-router", lbType: "tcp", port: 1024, backend: "tcp-router"},
			{name: "ssh", lbType: "tcp", port: 2222, backend: "diego-ssh"},
		}
	}

	return lbs
}

// ConfigureFQDNs configures DNS domain names.
func (g *GCPVaultProvider) ConfigureFQDNs(envPath, envType string, reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
	currentPhase := phaseNum
	phaseName := fmt.Sprintf("fqdns-%s", envType)
	phaseStart := time.Now()

	if reporter != nil {
		reporter.ReportPhaseStart(phaseName, currentPhase, totalPhases)
	}

	g.logger.Infow("Configuring FQDNs", "env_type", envType)

	// Get FQDNs for this environment
	fqdns := g.getFQDNsForEnv(envType)

	for i, fqdn := range fqdns {
		if reporter != nil {
			reporter.ReportSubtaskProgress(phaseName, i+1, len(fqdns), fmt.Sprintf("Writing %s", fqdn.name))
		}

		fqdnPath := fmt.Sprintf("%s/fqdns/%s", envPath, fqdn.name)
		fqdnConfig := map[string]interface{}{
			"fqdn":   fqdn.fqdn,
			"system": fqdn.system,
		}

		if err := g.Safe.SetMultiple(fqdnPath, fqdnConfig); err != nil {
			return fmt.Errorf("failed to set FQDN %s: %w", fqdn.name, err)
		}
	}

	if reporter != nil {
		reporter.ReportPhaseComplete(phaseName, time.Since(phaseStart))
	}

	return nil
}

type fqdnConfig struct {
	name   string
	fqdn   string
	system string
}

func (g *GCPVaultProvider) getFQDNsForEnv(envType string) []fqdnConfig {
	baseDomain := ""
	if g.Config.FQDNs != nil && g.Config.FQDNs.Base != "" {
		baseDomain = g.Config.FQDNs.Base
	}
	if baseDomain == "" {
		baseDomain = fmt.Sprintf("%s.example.com", g.BlocName)
	}

	var fqdns []fqdnConfig

	if envType == MgmtEnvType {
		fqdns = []fqdnConfig{
			{name: "concourse", fqdn: fmt.Sprintf("concourse.%s", baseDomain), system: "concourse"},
			{name: "vault", fqdn: fmt.Sprintf("vault.%s", baseDomain), system: "vault"},
			{name: "prometheus", fqdn: fmt.Sprintf("prometheus.%s", baseDomain), system: "prometheus"},
		}
	} else {
		fqdns = []fqdnConfig{
			{name: "system", fqdn: fmt.Sprintf("sys.%s", baseDomain), system: "cf"},
			{name: "apps", fqdn: fmt.Sprintf("apps.%s", baseDomain), system: "cf"},
			{name: "login", fqdn: fmt.Sprintf("login.%s", baseDomain), system: "cf"},
			{name: "uaa", fqdn: fmt.Sprintf("uaa.%s", baseDomain), system: "cf"},
			{name: "api", fqdn: fmt.Sprintf("api.%s", baseDomain), system: "cf"},
		}
	}

	return fqdns
}

// configureSharedComponents configures components shared across environments.
func (g *GCPVaultProvider) configureSharedComponents(reporter providers.ProgressReporter, phaseIndex *int, totalPhases int) error {
	// Configure certificates
	if err := g.ConfigureCertificates("", "", reporter, *phaseIndex, totalPhases); err != nil {
		return fmt.Errorf("failed to configure certificates: %w", err)
	}
	*phaseIndex++

	// Configure public IPs
	if err := g.ConfigurePublicIPs(reporter, *phaseIndex, totalPhases); err != nil {
		return fmt.Errorf("failed to configure public IPs: %w", err)
	}
	*phaseIndex++

	return nil
}

// ConfigureCertificates configures TLS certificates.
func (g *GCPVaultProvider) ConfigureCertificates(envPath, envType string, reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
	currentPhase := phaseNum
	phaseName := "certificates"
	phaseStart := time.Now()

	if reporter != nil {
		reporter.ReportPhaseStart(phaseName, currentPhase, totalPhases)
	}

	g.logger.Info("Configuring certificates")

	if reporter != nil {
		reporter.ReportSubtaskProgress(phaseName, 1, 1, "Writing certificate configuration")
	}

	certsPath := g.PathBuilder.GetCertsPath()
	certConfig := map[string]interface{}{
		"provider": "letsencrypt",
		"region":   gcpcpi.GetRegionFromZone(g.Config.Region),
		"domains":  g.Config.FQDNs,
	}

	if err := g.Safe.SetMultiple(certsPath, certConfig); err != nil {
		return fmt.Errorf("failed to set certificate configuration: %w", err)
	}

	if reporter != nil {
		reporter.ReportPhaseComplete(phaseName, time.Since(phaseStart))
	}

	return nil
}

// ConfigurePublicIPs configures external IP addresses.
func (g *GCPVaultProvider) ConfigurePublicIPs(reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
	currentPhase := phaseNum
	phaseName := "public-ips"
	phaseStart := time.Now()

	if reporter != nil {
		reporter.ReportPhaseStart(phaseName, currentPhase, totalPhases)
	}

	g.logger.Info("Configuring public IPs")

	// Get public IPs from the state or configuration
	publicIPs := g.getPublicIPs()

	for i, ip := range publicIPs {
		if reporter != nil {
			reporter.ReportSubtaskProgress(phaseName, i+1, len(publicIPs), fmt.Sprintf("Writing %s IP", ip.job))
		}

		ipPath := fmt.Sprintf("secret/config/%s/public-ips/%s", g.BlocName, ip.job)
		ipConfig := map[string]interface{}{
			"ip":    ip.address,
			"job":   ip.job,
			"index": ip.index,
		}

		if err := g.Safe.SetMultiple(ipPath, ipConfig); err != nil {
			return fmt.Errorf("failed to set public IP %s: %w", ip.job, err)
		}
	}

	if reporter != nil {
		reporter.ReportPhaseComplete(phaseName, time.Since(phaseStart))
	}

	return nil
}

type publicIPConfig struct {
	job     string
	address string
	index   int
}

func (g *GCPVaultProvider) getPublicIPs() []publicIPConfig {
	// In production, this would query the GCP API or state for actual IPs
	// For now, return placeholder configuration
	return []publicIPConfig{
		{job: "bastion", address: "", index: 0},
		{job: "router", address: "", index: 0},
		{job: "tcp-router", address: "", index: 0},
	}
}

// SaveConfigToVault saves the OCFP configuration to vault.
func (g *GCPVaultProvider) SaveConfigToVault(reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
	currentPhase := phaseNum
	phaseName := "config"
	phaseStart := time.Now()

	if reporter != nil {
		reporter.ReportPhaseStart(phaseName, currentPhase, totalPhases)
	}

	g.logger.Info("Saving configuration to vault")

	if reporter != nil {
		reporter.ReportSubtaskProgress(phaseName, 1, 1, "Writing configuration")
	}

	// Convert config to JSON
	configJSON, err := json.Marshal(g.Config)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	// Compress with gzip
	var buf bytes.Buffer
	gzWriter := gzip.NewWriter(&buf)
	if _, err := gzWriter.Write(configJSON); err != nil {
		return fmt.Errorf("failed to gzip config: %w", err)
	}
	if err := gzWriter.Close(); err != nil {
		return fmt.Errorf("failed to close gzip writer: %w", err)
	}

	// Base64 encode
	encoded := base64.StdEncoding.EncodeToString(buf.Bytes())

	// Save to vault
	configPath := fmt.Sprintf("secret/config/%s/ocfp", g.BlocName)
	configData := map[string]interface{}{
		"config":   encoded,
		"provider": "gcp",
		"bloc":     g.BlocName,
	}

	if err := g.Safe.SetMultiple(configPath, configData); err != nil {
		return fmt.Errorf("failed to save config to vault: %w", err)
	}

	if reporter != nil {
		reporter.ReportPhaseComplete(phaseName, time.Since(phaseStart))
	}

	return nil
}

// GetProviderName returns the provider name.
func (g *GCPVaultProvider) GetProviderName() string {
	return "gcp"
}

// Ensure imports are used
var (
	_ = sort.Strings
	_ = strings.TrimSpace
	_ = io.EOF
	_ = cpi.ResourceStateActive
)
