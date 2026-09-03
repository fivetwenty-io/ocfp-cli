package vault

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/config"
)

const (
	// MinVaultPathParts is the minimum number of path segments in a valid vault path.
	MinVaultPathParts = 3
	// MinConfigPathParts is the minimum number of path segments in a valid config path.
	MinConfigPathParts = 4
	// MinNetworkParts is the minimum number of parts in a network address.
	MinNetworkParts = 4
	// MaxPathParts is the maximum number of path segments supported.
	MaxPathParts = 5
	// NetworkPartsForIP is the number of parts when splitting an IP address for network derivation.
	NetworkPartsForIP = 2
	// MaxPathsCapacity is the initial slice capacity for path collection builders.
	MaxPathsCapacity = 30
)

// PathBuilder provides utilities for constructing vault paths according to OCFP conventions
// This ensures consistency with the Perl implementation's path structure.
type PathBuilder struct {
	config   *config.Config
	blocName string
}

// NewPathBuilder creates a new path builder.
func NewPathBuilder(cfg *config.Config, blocName string) *PathBuilder {
	return &PathBuilder{
		config:   cfg,
		blocName: blocName,
	}
}

// Standard OCFP vault path prefixes.
const (
	SecretPrefix = "secret"
	ConfigPrefix = "config"
)

// Environment types.
const (
	MgmtEnvType = "mgmt"
	OCFEnvType  = "ocf"
)

const inceptionComponent = "inception"

// Phase name constants for provider operations.
const (
	PhaseCertificates = "certificates"
	PhasePublicIPs    = "public-ips"
	PhaseReservedIPs  = "reserved-ips"
	PhaseConfig       = "config"
)

// GetConfigPath returns the base configuration path for a bloc
// Format: secret/config/{bloc}.
func (pb *PathBuilder) GetConfigPath() string {
	return filepath.Join(SecretPrefix, ConfigPrefix, pb.blocName)
}

// GetOCFPConfigPath returns the OCFP-specific configuration path
// Format: secret/config/{bloc}/ocfp.
func (pb *PathBuilder) GetOCFPConfigPath() string {
	return filepath.Join(pb.GetConfigPath(), "ocfp")
}

// GetEnvironmentPath returns the environment-specific path
// Format: secret/config/{bloc}/{env-type}.
func (pb *PathBuilder) GetEnvironmentPath(envType string) string {
	return filepath.Join(pb.GetConfigPath(), envType)
}

// GetNetPath returns the network configuration path for an environment
// Format: secret/config/{bloc}/{env-type}/net.
func (pb *PathBuilder) GetNetPath(envType string) string {
	return filepath.Join(pb.GetEnvironmentPath(envType), "net")
}

// GetSubnetsPath returns the subnets configuration path
// Format: secret/config/{bloc}/{env-type}/net/subnets.
func (pb *PathBuilder) GetSubnetsPath(envType string) string {
	return filepath.Join(pb.GetNetPath(envType), "subnets")
}

// GetSubnetPath returns the path for a specific subnet
// Format: secret/config/{bloc}/{env-type}/net/subnets/{subnet-type}-{subnet-num}.
func (pb *PathBuilder) GetSubnetPath(envType, subnetType string, subnetNum int) string {
	subnetName := fmt.Sprintf("%s-%d", subnetType, subnetNum)

	return filepath.Join(pb.GetSubnetsPath(envType), subnetName)
}

// GetReservedIPsPath returns the path for reserved IPs in a subnet
// Format: secret/config/{bloc}/{env-type}/net/subnets/{subnet-type}-{subnet-num}/reserved-ips.
func (pb *PathBuilder) GetReservedIPsPath(envType, subnetType string, subnetNum int) string {
	return filepath.Join(pb.GetSubnetPath(envType, subnetType, subnetNum), "reserved-ips")
}

// GetSecurityGroupsPath returns the security groups path
// Format: secret/config/{bloc}/{env-type}/net/sgs.
func (pb *PathBuilder) GetSecurityGroupsPath(envType string) string {
	return filepath.Join(pb.GetNetPath(envType), "sgs")
}

// GetSecurityGroupPath returns the path for a specific security group
// Format: secret/config/{bloc}/{env-type}/net/sgs/{sg-name}.
func (pb *PathBuilder) GetSecurityGroupPath(envType, sgName string) string {
	return filepath.Join(pb.GetSecurityGroupsPath(envType), sgName)
}

// GetAZsPath returns the availability zones path
// Format: secret/config/{bloc}/{env-type}/net/azs.
func (pb *PathBuilder) GetAZsPath(envType string) string {
	return filepath.Join(pb.GetNetPath(envType), "azs")
}

// GetAZPath returns the path for a specific availability zone
// Format: secret/config/{bloc}/{env-type}/net/azs/{az-name}.
func (pb *PathBuilder) GetAZPath(envType, azName string) string {
	return filepath.Join(pb.GetAZsPath(envType), azName)
}

// GetBOSHPath returns the BOSH configuration path
// Format: secret/config/{bloc}/{env-type}/bosh.
func (pb *PathBuilder) GetBOSHPath(envType string) string {
	return filepath.Join(pb.GetEnvironmentPath(envType), "bosh")
}

// GetIAMPath returns the IAM configuration path
// Format: secret/config/{bloc}/{env-type}/bosh/iam.
func (pb *PathBuilder) GetIAMPath(envType string) string {
	return filepath.Join(pb.GetBOSHPath(envType), "iam")
}

// GetIAMBoshPath returns the IAM BOSH credentials path
// Format: secret/config/{bloc}/{env-type}/bosh/iam/bosh.
func (pb *PathBuilder) GetIAMBoshPath(envType string) string {
	return filepath.Join(pb.GetIAMPath(envType), "bosh")
}

// GetIAMS3Path returns the IAM S3 credentials path
// Format: secret/config/{bloc}/{env-type}/bosh/iam/s3.
func (pb *PathBuilder) GetIAMS3Path(envType string) string {
	return filepath.Join(pb.GetIAMPath(envType), "s3")
}

// GetS3Path returns the S3 credentials path
// Format: secret/config/{bloc}/{env-type}/bosh/s3.
func (pb *PathBuilder) GetS3Path(envType string) string {
	return filepath.Join(pb.GetBOSHPath(envType), "s3")
}

// GetKeysPath returns the keys configuration path
// Format: secret/config/{bloc}/{env-type}/bosh/keys.
func (pb *PathBuilder) GetKeysPath(envType string) string {
	return filepath.Join(pb.GetBOSHPath(envType), "keys")
}

// GetBOSHKeyPath returns the BOSH key path
// Format: secret/config/{bloc}/{env-type}/bosh/keys/bosh.
func (pb *PathBuilder) GetBOSHKeyPath(envType string) string {
	return filepath.Join(pb.GetKeysPath(envType), "bosh")
}

// GetKMSPath returns the KMS configuration path
// Format: secret/config/{bloc}/{env-type}/bosh/kms.
func (pb *PathBuilder) GetKMSPath(envType string) string {
	return filepath.Join(pb.GetBOSHPath(envType), "kms")
}

// GetBlobstoresPath returns the blobstores configuration path
// Format: secret/config/{bloc}/{env-type}/blobstores.
func (pb *PathBuilder) GetBlobstoresPath(envType string) string {
	return filepath.Join(pb.GetEnvironmentPath(envType), "blobstores")
}

// GetSystemBlobstorePath returns the path for a system's blobstore
// Format: secret/config/{bloc}/{env-type}/{system}/blobstores/{blobstore-name}.
func (pb *PathBuilder) GetSystemBlobstorePath(envType, system, blobstoreName string) string {
	return filepath.Join(pb.GetEnvironmentPath(envType), system, "blobstores", blobstoreName)
}

// GetDatabasesPath returns the databases configuration path
// Format: secret/config/{bloc}/{env-type}/dbs.
func (pb *PathBuilder) GetDatabasesPath(envType string) string {
	return filepath.Join(pb.GetEnvironmentPath(envType), "dbs")
}

// GetDatabasePath returns the path for a specific database
// Format: secret/config/{bloc}/{env-type}/dbs/{db-name}.
func (pb *PathBuilder) GetDatabasePath(envType, dbName string) string {
	return filepath.Join(pb.GetDatabasesPath(envType), dbName)
}

// GetLoadBalancersPath returns the load balancers configuration path
// Format: secret/config/{bloc}/{env-type}/lbs.
func (pb *PathBuilder) GetLoadBalancersPath(envType string) string {
	return filepath.Join(pb.GetEnvironmentPath(envType), "lbs")
}

// GetLoadBalancerPath returns the path for a specific load balancer
// Format: secret/config/{bloc}/{env-type}/lbs/{lb-name}.
func (pb *PathBuilder) GetLoadBalancerPath(envType, lbName string) string {
	return filepath.Join(pb.GetLoadBalancersPath(envType), lbName)
}

// GetFQDNsPath returns the FQDNs configuration path
// Format: secret/config/{bloc}/{env-type}/fqdns.
func (pb *PathBuilder) GetFQDNsPath(envType string) string {
	return filepath.Join(pb.GetEnvironmentPath(envType), "fqdns")
}

// GetBaseFQDNPath returns the shared base FQDN path
// Format: secret/config/{bloc}/fqdns/base.
func (pb *PathBuilder) GetBaseFQDNPath() string {
	return filepath.Join(pb.GetConfigPath(), "fqdns", "base")
}

// GetPublicIPsPath returns the public IPs path
// Format: secret/config/{bloc}/ocf/public-ips.
func (pb *PathBuilder) GetPublicIPsPath() string {
	return filepath.Join(pb.GetEnvironmentPath(OCFEnvType), "public-ips")
}

// GetCertsPath returns the certificates path
// Format: secret/config/{bloc}/certs.
func (pb *PathBuilder) GetCertsPath() string {
	return filepath.Join(pb.GetConfigPath(), "certs")
}

// GetInceptionPath returns the inception vault path
// Format: secret/{bloc}-inception or secret/inception.
func (pb *PathBuilder) GetInceptionPath() string {
	if pb.blocName != "" {
		return filepath.Join(SecretPrefix, pb.blocName+"-inception")
	}

	return filepath.Join(SecretPrefix, "inception")
}

// GetJumpboxUsersPath returns the jumpbox users path
// Format: secret/config/{bloc}/mgmt/jumpbox/users.
func (pb *PathBuilder) GetJumpboxUsersPath() string {
	return filepath.Join(pb.GetEnvironmentPath(MgmtEnvType), "jumpbox", "users")
}

// PathInfo contains information about a vault path.
type PathInfo struct {
	Path        string
	Environment string
	Component   string
	Subpath     string
}

// ParsePath parses a vault path and extracts information about it.
func (pb *PathBuilder) ParsePath(path string) (*PathInfo, error) {
	// Normalize path
	path = strings.TrimPrefix(path, "/")
	path = filepath.Clean(path)

	parts := strings.Split(path, "/")
	if len(parts) < MinVaultPathParts {
		return nil, ErrInvalidVaultPathFormat(path)
	}

	info := &PathInfo{
		Path:        path,
		Environment: "",
		Component:   "",
		Subpath:     "",
	}

	// Check if it's a config path
	if parts[0] == SecretPrefix && parts[1] == ConfigPrefix {
		return pb.parseConfigPath(parts, info, path)
	}

	if parts[0] == SecretPrefix {
		return pb.parseSecretPath(parts, info)
	}

	return info, nil
}

// IsConfigPath checks if a path is a configuration path.
func (pb *PathBuilder) IsConfigPath(path string) bool {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")

	return len(parts) >= 3 && parts[0] == SecretPrefix && parts[1] == ConfigPrefix
}

// IsInceptionPath checks if a path is an inception vault path.
func (pb *PathBuilder) IsInceptionPath(path string) bool {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")

	return len(parts) >= 2 && parts[0] == SecretPrefix &&
		(parts[1] == inceptionComponent || strings.HasSuffix(parts[1], "-inception"))
}

// GetAllStandardPaths returns all standard paths for the bloc.
func (pb *PathBuilder) GetAllStandardPaths() []string {
	// 2 envs * 13 paths each + 1 config + 1 publicIPs + 2 other = 30
	paths := make([]string, 0, MaxPathsCapacity)

	// Config paths
	paths = append(paths, pb.GetOCFPConfigPath())

	// Environment paths
	for _, envType := range []string{MgmtEnvType, OCFEnvType} {
		paths = append(paths, pb.GetEnvironmentPath(envType))
		paths = append(paths, pb.GetNetPath(envType))
		paths = append(paths, pb.GetSubnetsPath(envType))
		paths = append(paths, pb.GetSecurityGroupsPath(envType))
		paths = append(paths, pb.GetAZsPath(envType))
		paths = append(paths, pb.GetBOSHPath(envType))
		paths = append(paths, pb.GetIAMPath(envType))
		paths = append(paths, pb.GetKeysPath(envType))
		paths = append(paths, pb.GetKMSPath(envType))
		paths = append(paths, pb.GetBlobstoresPath(envType))
		paths = append(paths, pb.GetDatabasesPath(envType))
		paths = append(paths, pb.GetLoadBalancersPath(envType))
		paths = append(paths, pb.GetFQDNsPath(envType))
	}

	// OCF-specific paths
	paths = append(paths, pb.GetPublicIPsPath())

	// Other paths
	paths = append(paths, pb.GetCertsPath())
	paths = append(paths, pb.GetJumpboxUsersPath())

	return paths
}

// ValidatePath validates that a path follows OCFP conventions.
func (pb *PathBuilder) ValidatePath(path string) error {
	if path == "" {
		return ErrPathCannotBeEmpty
	}

	// Parse the path
	_, err := pb.ParsePath(path)
	if err != nil {
		return fmt.Errorf("invalid path format: %w", err)
	}

	return nil
}

// NormalizePath normalizes a vault path (removes leading slash, cleans up).
func (pb *PathBuilder) NormalizePath(path string) string {
	path = strings.TrimPrefix(path, "/")

	return filepath.Clean(path)
}

func (pb *PathBuilder) parseConfigPath(parts []string, info *PathInfo, path string) (*PathInfo, error) {
	if len(parts) < MinConfigPathParts {
		return nil, ErrInvalidConfigPathFormat(path)
	}

	// parts[2] is bloc name, parts[3] is environment or component
	if parts[3] == MgmtEnvType || parts[3] == OCFEnvType {
		pb.setEnvironmentInfo(parts, info)

		return info, nil
	}

	pb.setComponentInfo(parts, info)

	return info, nil
}

func (pb *PathBuilder) parseSecretPath(parts []string, info *PathInfo) (*PathInfo, error) {
	// This might be an inception path
	if strings.HasSuffix(parts[1], "-inception") {
		info.Component = inceptionComponent
		if len(parts) > NetworkPartsForIP {
			info.Subpath = strings.Join(parts[2:], "/")
		}
	}

	return info, nil
}

func (pb *PathBuilder) setEnvironmentInfo(parts []string, info *PathInfo) {
	info.Environment = parts[3]
	if len(parts) > MinConfigPathParts {
		info.Component = parts[4]
		if len(parts) > MaxPathParts {
			info.Subpath = strings.Join(parts[5:], "/")
		}
	}
}

func (pb *PathBuilder) setComponentInfo(parts []string, info *PathInfo) {
	info.Component = parts[3]
	if len(parts) > MinConfigPathParts {
		info.Subpath = strings.Join(parts[4:], "/")
	}
}

// HostnameFormatter is a function type that generates database hostnames.
type HostnameFormatter func(envType string) string

// BuildDatabasesForEnv constructs database configuration maps for a given environment type
// using the provided hostname formatter to generate database hostnames.
func BuildDatabasesForEnv(envType string, hostnameFormatter HostnameFormatter) map[string]map[string]interface{} {
	databases := make(map[string]map[string]interface{})

	switch envType {
	case MgmtEnvType:
		databases["bosh"] = map[string]interface{}{ // #nosec G101 -- map keys contain "password" but values are Genesis template placeholders, not real secrets
			"hostname":          hostnameFormatter(MgmtEnvType),
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
		databases["cf"] = map[string]interface{}{ // #nosec G101 -- map keys contain "password" but values are Genesis template placeholders, not real secrets
			"hostname":                      hostnameFormatter(OCFEnvType),
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
