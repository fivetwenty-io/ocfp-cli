package vault

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"errors"
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

	blobstoreOptions *PopulateOptions

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
	if err := validateCloudState(g.BlocName); err != nil {
		return err
	}

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
		err := g.configureEnvironment(envType, reporter, &phaseIndex, totalPhases)
		if err != nil {
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

// ConfigureIAAS configures GCP-specific IAAS settings.
func (g *GCPVaultProvider) ConfigureIAAS(_envPath, envType string, reporter providers.ProgressReporter, phaseNum *int, totalPhases int) error {
	if err := validateCloudState(g.BlocName); err != nil {
		return err
	}

	currentPhase := 0
	if phaseNum != nil {
		currentPhase = *phaseNum
	}

	phaseName := "networks-" + envType
	phaseStart := time.Now()

	if reporter != nil {
		reporter.ReportPhaseStart(phaseName, currentPhase, totalPhases)
	}

	g.logger.Infow("Configuring GCP IAAS", "env_type", envType)

	credentials, err := g.credentialsJSON()
	if err != nil {
		return err
	}

	region, zone, tags, err := g.cloudLocationAndTags()
	if err != nil {
		return err
	}
	// Configure GCP CPI settings
	cpiPath := fmt.Sprintf("secret/config/%s/%s/cpi/gcp", g.BlocName, envType)

	cpiConfig := map[string]interface{}{
		"project":                 g.Config.ProjectID,
		"zone":                    zone,
		"region":                  region,
		"json_key":                credentials,
		"default_key_name":        g.BlocName + "-bastion",
		"default_security_groups": tags,
	}

	if credentials == "" {
		delete(cpiConfig, "json_key")
	}

	err = g.Safe.SetMultiple(cpiPath, cpiConfig)
	if err != nil {
		return fmt.Errorf("failed to set CPI config: %w", err)
	}

	// Configure VPC network
	networkPath := g.PathBuilder.GetNetPath(envType)
	networkConfig := map[string]interface{}{
		"name":    g.networkName(),
		"project": g.networkProject(),
		"region":  region,
		"zone":    zone,
		"dns":     pveFirstNonEmpty(pveFirstDNS(g.Config.Network.DNS), pveFirstDNS(g.Config.DNS), DefaultDNSServer),
		"id":      g.awsStateReader().getVPCID(),
	}

	err = g.Safe.SetMultiple(networkPath, networkConfig)
	if err != nil {
		return fmt.Errorf("failed to set network config: %w", err)
	}

	if err := g.awsStateReader().configureKeys(envType); err != nil {
		return err
	}

	if reporter != nil {
		reporter.ReportPhaseComplete(phaseName, time.Since(phaseStart))
	}

	return nil
}

// ConfigureBlobstores configures Cloud Storage bucket settings.
func (g *GCPVaultProvider) ConfigureBlobstores(_envPath, envType string, reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
	if err := validateCloudState(g.BlocName); err != nil {
		return err
	}

	if external, err := g.externalBlobstore(); err != nil {
		return err
	} else if external != nil {
		return external.ConfigureBlobstores(_envPath, envType, nil, 1, 1)
	}

	currentPhase := phaseNum
	phaseName := "blobstores-" + envType
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

			err := g.Safe.SetMultiple(blobstorePath, blobstoreConfig)
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

// ConfigureDatabases configures Cloud SQL settings.
func (g *GCPVaultProvider) ConfigureDatabases(envPath, envType string, reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
	if err := validateCloudState(g.BlocName); err != nil {
		return err
	}

	defer cloudPhase(reporter, "databases-"+envType, phaseNum, totalPhases)()

	for name, data := range cloudDatabases(g.awsStateReader().loadStateManager(), envType) {
		if err := g.Safe.SetMultiple(g.PathBuilder.GetDatabasePath(envType, name), data); err != nil {
			return err
		}
	}

	return nil
}

// ConfigureLoadBalancers configures Cloud Load Balancing settings.
func (g *GCPVaultProvider) ConfigureLoadBalancers(envPath, envType string, reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
	if err := validateCloudState(g.BlocName); err != nil {
		return err
	}

	defer cloudPhase(reporter, "load-balancers-"+envType, phaseNum, totalPhases)()

	return writeCloudLoadBalancers(g.Safe, g.PathBuilder, g.awsStateReader().loadStateManager(), envType)
}

// ConfigureFQDNs configures DNS domain names.
func (g *GCPVaultProvider) ConfigureFQDNs(envPath, envType string, reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
	if err := validateCloudState(g.BlocName); err != nil {
		return err
	}

	defer cloudPhase(reporter, "fqdns-"+envType, phaseNum, totalPhases)()

	record := cloudFQDNRecord(g.Config, envType)
	if len(record) == 0 {
		return nil
	}

	return g.Safe.SetMultiple(g.PathBuilder.GetFQDNsPath(envType), record)
}

// ConfigureCertificates configures TLS certificates.
func (g *GCPVaultProvider) ConfigureCertificates(_envPath, _envType string, reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
	if err := validateCloudState(g.BlocName); err != nil {
		return err
	}

	currentPhase := phaseNum
	phaseName := PhaseCertificates
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

	err := g.Safe.SetMultiple(certsPath, certConfig)
	if err != nil {
		return fmt.Errorf("failed to set certificate configuration: %w", err)
	}

	if reporter != nil {
		reporter.ReportPhaseComplete(phaseName, time.Since(phaseStart))
	}

	return nil
}

// ConfigurePublicIPs configures external IP addresses.
func (g *GCPVaultProvider) ConfigurePublicIPs(reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
	if err := validateCloudState(g.BlocName); err != nil {
		return err
	}

	defer cloudPhase(reporter, PhasePublicIPs, phaseNum, totalPhases)()

	data := g.awsStateReader().getPublicIPsFromState()
	if len(data) == 0 {
		return nil
	}

	return g.Safe.SetMultiple(g.PathBuilder.GetPublicIPsPath(), data)
}

// SaveConfigToVault saves the OCFP configuration to vault.
func (g *GCPVaultProvider) SaveConfigToVault(reporter providers.ProgressReporter, phaseNum, totalPhases int) error {
	currentPhase := phaseNum
	phaseName := PhaseConfig
	phaseStart := time.Now()

	if reporter != nil {
		reporter.ReportPhaseStart(phaseName, currentPhase, totalPhases)
	}

	g.logger.Info("Saving configuration to vault")

	if reporter != nil {
		reporter.ReportSubtaskProgress(phaseName, 1, 1, "Writing configuration")
	}

	// Convert config to JSON
	configJSON, err := json.Marshal(g.Config) // #nosec G117 -- intentional secret serialization to vault
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	// Compress with gzip
	var buf bytes.Buffer

	gzWriter := gzip.NewWriter(&buf)

	_, gzErr := gzWriter.Write(configJSON)
	if gzErr != nil {
		return fmt.Errorf("failed to gzip config: %w", gzErr)
	}

	gzErr = gzWriter.Close()
	if gzErr != nil {
		return fmt.Errorf("failed to close gzip writer: %w", gzErr)
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

	saveErr := g.Safe.SetMultiple(configPath, configData)
	if saveErr != nil {
		return fmt.Errorf("failed to save config to vault: %w", saveErr)
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

// configureEnvironment configures all components for a given environment type.
func (g *GCPVaultProvider) configureEnvironment(envType string, reporter providers.ProgressReporter, phaseIndex *int, totalPhases int) error {
	envPath := g.PathBuilder.GetEnvironmentPath(envType)

	// Configure networks
	err := g.ConfigureIAAS(envPath, envType, reporter, phaseIndex, totalPhases)
	if err != nil {
		return fmt.Errorf("failed to configure IAAS for %s: %w", envType, err)
	}

	*phaseIndex++

	// Configure subnets
	err = g.configureSubnets(envType, reporter, *phaseIndex, totalPhases)
	if err != nil {
		return fmt.Errorf("failed to configure subnets for %s: %w", envType, err)
	}

	*phaseIndex++

	// Configure security groups
	err = g.configureSecurityGroups(envType, reporter, *phaseIndex, totalPhases)
	if err != nil {
		return fmt.Errorf("failed to configure security groups for %s: %w", envType, err)
	}

	*phaseIndex++

	// Configure blobstores
	err = g.ConfigureBlobstores(envPath, envType, reporter, *phaseIndex, totalPhases)
	if err != nil {
		return fmt.Errorf("failed to configure blobstores for %s: %w", envType, err)
	}

	*phaseIndex++

	// Configure databases
	err = g.ConfigureDatabases(envPath, envType, reporter, *phaseIndex, totalPhases)
	if err != nil {
		return fmt.Errorf("failed to configure databases for %s: %w", envType, err)
	}

	*phaseIndex++

	// Configure load balancers
	err = g.ConfigureLoadBalancers(envPath, envType, reporter, *phaseIndex, totalPhases)
	if err != nil {
		return fmt.Errorf("failed to configure load balancers for %s: %w", envType, err)
	}

	*phaseIndex++

	// Configure FQDNs
	err = g.ConfigureFQDNs(envPath, envType, reporter, *phaseIndex, totalPhases)
	if err != nil {
		return fmt.Errorf("failed to configure FQDNs for %s: %w", envType, err)
	}

	*phaseIndex++

	return nil
}

// configureSubnets configures subnet settings.
func (g *GCPVaultProvider) configureSubnets(envType string, reporter providers.ProgressReporter, phaseNum int, totalPhases int) error {
	defer cloudPhase(reporter, "subnets-"+envType, phaseNum, totalPhases)()

	subnets, err := cloudSubnets(g.Config, g.awsStateReader().loadStateManager())
	if err != nil {
		return err
	}

	if len(subnets) == 0 {
		return errors.New("GCP subnet configuration requires bootstrap state or configured subnets")
	}

	if err := writeCloudAZs(g.Safe, g.PathBuilder, g.Config, envType, subnets); err != nil {
		return err
	}

	for _, sub := range subnets {
		info, err := g.awsStateReader().parseSubnetCIDR(sub.CIDR)
		if err != nil {
			return err
		}

		data := map[string]interface{}{"name": sub.Name, "cloud_name": cloudResourceName(pveFirstNonEmpty(sub.ID, sub.Name)), "cidr_block": sub.CIDR, "subnet_cidr": sub.CIDR, "gateway": info.gateway, "ip_0": info.network, "ip_n": info.lastHost, "az": cloudAZKey(g.Config, sub.AvailabilityZone), "region": gcpcpi.GetRegionFromZone(g.Config.Region), "provider": "gcp", "type": sub.Type, "subnet_num": sub.Index, "dns": pveFirstNonEmpty(pveFirstDNS(g.Config.Network.DNS), pveFirstDNS(g.Config.DNS), DefaultDNSServer)}
		if sub.ID != "" {
			data["id"] = sub.ID
		}

		if err := g.Safe.SetMultiple(g.PathBuilder.GetSubnetPath(envType, sub.Type, sub.Index), data); err != nil {
			return err
		}

		if sub.Type == DefaultSubnetType {
			if err := g.awsStateReader().configureSubnetReservedIPs(sub.CIDR, sub.Type, sub.Index, envType); err != nil {
				return err
			}
		}
	}

	return nil
}

// configureSecurityGroups configures firewall rules.
func (g *GCPVaultProvider) configureSecurityGroups(envType string, reporter providers.ProgressReporter, phaseNum int, totalPhases int) error {
	defer cloudPhase(reporter, "security-groups-"+envType, phaseNum, totalPhases)()

	return writeCloudSecurityGroups(g.Safe, g.PathBuilder, g.awsStateReader().loadStateManager(), g.BlocName, envType, true)
}

// getBlobstoresForSystem returns blobstore configuration for a system.
func (g *GCPVaultProvider) getBlobstoresForSystem(system, envType string) map[string]map[string]interface{} {
	blobstores := make(map[string]map[string]interface{})
	region := gcpcpi.GetRegionFromZone(g.Config.Region)

	switch system {
	case "bosh":
		blobstores["bosh"] = map[string]interface{}{"bucket_name": fmt.Sprintf("%s-%s-bosh", g.BlocName, envType), "region": region, "storage_class": "STANDARD", "type": "gcs"}
		blobstores["artifacts"] = map[string]interface{}{
			"bucket_name":   fmt.Sprintf("%s-%s-artifacts", g.BlocName, envType),
			"region":        region,
			"storage_class": "STANDARD",
		}
	case "cf":
		cfBlobstores := []string{"buildpacks", "droplets", "packages", "resources"}
		for _, name := range cfBlobstores {
			bucketName := name
			if name == "resources" {
				bucketName = "resource-pool"
			}

			blobstores[name] = map[string]interface{}{
				"bucket_name":   fmt.Sprintf("%s-%s-cf-%s", g.BlocName, envType, bucketName),
				"region":        region,
				"storage_class": "STANDARD",
			}
		}
	}

	for _, blob := range blobstores {
		blob["name"] = blob["bucket_name"]
		blob["type"] = "gcs"
	}

	return blobstores
}

// configureSharedComponents configures components shared across environments.
func (g *GCPVaultProvider) configureSharedComponents(reporter providers.ProgressReporter, phaseIndex *int, totalPhases int) error {
	// Configure certificates
	err := g.ConfigureCertificates("", "", reporter, *phaseIndex, totalPhases)
	if err != nil {
		return fmt.Errorf("failed to configure certificates: %w", err)
	}

	*phaseIndex++

	// Configure public IPs
	err = g.ConfigurePublicIPs(reporter, *phaseIndex, totalPhases)
	if err != nil {
		return fmt.Errorf("failed to configure public IPs: %w", err)
	}

	*phaseIndex++

	return nil
}

// Ensure imports are used.
var (
	_ = sort.Strings
	_ = strings.TrimSpace
	_ = io.EOF
	_ = cpi.ResourceStateActive
)

// awsStateReader shares the provider-independent state and reservation readers.
func (g *GCPVaultProvider) awsStateReader() *AWSVaultProvider {
	return NewAWSVaultProvider(g.Config, g.Safe, g.BlocName)
}
func (g *GCPVaultProvider) credentialsJSON() (string, error) {
	source := pveFirstNonEmpty(g.Config.ServiceAccountJSON, g.Config.ServiceAccountKeyPath)
	if source == "" {
		return "", nil
	}

	cfg := gcpcpi.Config{ServiceAccountJSON: source}

	data, err := cfg.GetServiceAccountCredentials()
	if err != nil {
		return "", fmt.Errorf("resolve GCP CPI credentials: %w", err)
	}

	if !json.Valid(data) {
		return "", errors.New("GCP credential file does not contain valid JSON")
	}

	return string(data), nil
}

func cloudPhase(reporter providers.ProgressReporter, name string, number, total int) func() {
	start := time.Now()

	if reporter != nil {
		reporter.ReportPhaseStart(name, number, total)
	}

	return func() {
		if reporter != nil {
			reporter.ReportPhaseComplete(name, time.Since(start))
		}
	}
}

type loadBalancerConfig struct {
	name    string
	lbType  string
	port    int
	backend string
}

type fqdnConfig struct {
	name   string
	fqdn   string
	system string
}

func (g *GCPVaultProvider) getLoadBalancersForEnv(envType string) []loadBalancerConfig {
	var lbs []loadBalancerConfig

	if envType == MgmtEnvType {
		lbs = []loadBalancerConfig{
			{name: "concourse", lbType: "https", port: 443, backend: "concourse"},   //nolint:mnd
			{name: "vault", lbType: "https", port: 443, backend: "vault"},           //nolint:mnd
			{name: "prometheus", lbType: "https", port: 443, backend: "prometheus"}, //nolint:mnd
		}
	} else {
		lbs = []loadBalancerConfig{
			{name: "router", lbType: "https", port: 443, backend: "router"},        //nolint:mnd
			{name: "tcp-router", lbType: "tcp", port: 1024, backend: "tcp-router"}, //nolint:mnd
			{name: "ssh", lbType: "tcp", port: 2222, backend: "diego-ssh"},         //nolint:mnd
		}
	}

	return lbs
}

func (g *GCPVaultProvider) getFQDNsForEnv(envType string) []fqdnConfig {
	baseDomain := ""
	if g.Config.FQDNs != nil && g.Config.FQDNs.Base != "" {
		baseDomain = g.Config.FQDNs.Base
	}

	if baseDomain == "" {
		baseDomain = g.BlocName + ".example.com"
	}

	var fqdns []fqdnConfig

	if envType == MgmtEnvType {
		fqdns = []fqdnConfig{
			{name: "concourse", fqdn: "concourse." + baseDomain, system: "concourse"},
			{name: "vault", fqdn: "vault." + baseDomain, system: "vault"},
			{name: "prometheus", fqdn: "prometheus." + baseDomain, system: "prometheus"},
		}
	} else {
		fqdns = []fqdnConfig{
			{name: "system", fqdn: "sys." + baseDomain, system: "cf"},
			{name: "apps", fqdn: "apps." + baseDomain, system: "cf"},
			{name: "login", fqdn: "login." + baseDomain, system: "cf"},
			{name: "uaa", fqdn: "uaa." + baseDomain, system: "cf"},
			{name: "api", fqdn: "api." + baseDomain, system: "cf"},
		}
	}

	return fqdns
}
