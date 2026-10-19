package providers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	bastionssh "github.com/ocfp/ocfp-cli-go/internal/bastion/ssh"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	gcpcpi "github.com/ocfp/ocfp-cli-go/internal/cpi/gcp"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

// GCP provider errors.
var (
	ErrGCPBastionIPNotDetermined = errors.New("could not determine bastion IP: check configuration or set GCP_BASTION_IP")
	ErrNoSSHKeyFound             = errors.New("no SSH key found")
)

// GCPBastionInit implements bastion initialization for GCP.
type GCPBastionInit struct {
	config *config.Config
	log    logger.Logger
}

// NewGCPBastionInit creates a new GCP bastion initializer.
func NewGCPBastionInit(cfg *config.Config) *GCPBastionInit {
	return &GCPBastionInit{
		config: cfg,
		log:    logger.Get(),
	}
}

// Validate validates the GCP configuration for bastion initialization.
func (g *GCPBastionInit) Validate() error {
	if g.projectID() == "" {
		return ErrGCPProjectIDRequired
	}

	if _, err := g.CredentialsJSON(); err != nil {
		return err
	}

	if g.zone() == "" {
		return ErrGCPZoneRequired
	}

	g.log.Debug("GCP configuration validated successfully")

	return nil
}

// PrepareEnvironment prepares the environment variables for GCP.
func (g *GCPBastionInit) PrepareEnvironment() map[string]string {
	env := map[string]string{
		"OCFP_BLOC":     g.config.Name,
		"OCFP_PROVIDER": "gcp",
	}

	// Set GCP-specific environment variables
	if project := g.projectID(); project != "" {
		env["GOOGLE_PROJECT"] = project
		env["GOOGLE_CLOUD_PROJECT"] = project
		env["CLOUDSDK_CORE_PROJECT"] = project
	}

	// Set zone and region
	zone := g.zone()
	if zone != "" {
		env["GOOGLE_ZONE"] = zone
		env["CLOUDSDK_COMPUTE_ZONE"] = zone
		// Derive region from zone
		region := gcpcpi.GetRegionFromZone(zone)
		env["GOOGLE_REGION"] = region
		env["CLOUDSDK_COMPUTE_REGION"] = region
	}

	// Carry encoded JSON, never a local file path the remote host cannot read.
	// The provisioning wrapper materializes this into an owner-only file.
	if raw, err := g.CredentialsJSON(); err == nil && raw != "" {
		env["OCFP_GCP_CREDENTIALS_B64"] = base64.StdEncoding.EncodeToString([]byte(raw))
	}

	// Add Genesis-specific environment variables
	g.addGenesisEnv(env)

	return env
}

// GetConnectionDetails returns the SSH connection details for the bastion.
func (g *GCPBastionInit) GetConnectionDetails(_ context.Context) (*ConnectionDetails, error) {
	bastionIP, err := g.getBastionIP()
	if err != nil {
		return nil, fmt.Errorf("failed to get bastion IP: %w", err)
	}

	// Determine SSH user - GCP default depends on the image
	sshUser := g.config.Bastion.SSHUser
	if sshUser == "" {
		sshUser = os.Getenv("OCFP_BASTION_USER")
	}

	if sshUser == "" {
		sshUser = defaultSSHUser // Common default for Ubuntu images on GCP
	}

	// Find SSH key
	sshKey, err := g.findSSHKey()
	if err != nil {
		return nil, fmt.Errorf("failed to find SSH key: %w", err)
	}

	return &ConnectionDetails{
		Host:           bastionIP,
		User:           sshUser,
		Port:           22, //nolint:mnd
		PrivateKeyPath: sshKey,
		SSHOptions:     g.getSSHOptions(),
	}, nil
}

// addGenesisEnv adds Genesis-specific environment variables.
func (g *GCPBastionInit) addGenesisEnv(env map[string]string) {
	env["GENESIS_ENVIRONMENT"] = g.config.Name
	if project := g.projectID(); project != "" {
		env["GCP_PROJECT"] = project
	}

	if zone := g.zone(); zone != "" {
		env["GCP_ZONE"] = zone
		env["GCP_REGION"] = gcpcpi.GetRegionFromZone(zone)
	}

	genesis := g.config.Bastion.Genesis
	if !genesis.Enabled {
		env["GENESIS_SKIP_INSTALL"] = "1"

		return
	}

	for k, v := range map[string]string{"GENESIS_BRANCH": genesis.Branch, "GENESIS_COMMIT": genesis.Commit, "GENESIS_VERSION_PREFIX": genesis.VersionPrefix, "GENESIS_REPO": genesis.Repo} {
		if v != "" {
			env[k] = v
		}
	}
}

func (g *GCPBastionInit) projectID() string {
	for _, v := range []string{g.config.ProjectID, os.Getenv("GOOGLE_PROJECT"), os.Getenv("GOOGLE_CLOUD_PROJECT")} {
		if v != "" {
			return v
		}
	}

	return ""
}

func (g *GCPBastionInit) zone() string {
	// Prefer the subnet actually selected for the bastion over unrelated AZs.
	for _, subnet := range g.config.Network.Subnets {
		if subnet.AvailabilityZone != "" {
			return subnet.AvailabilityZone
		}
	}

	for _, v := range []string{os.Getenv("GOOGLE_ZONE"), os.Getenv("CLOUDSDK_COMPUTE_ZONE"), g.config.Region} {
		parts := strings.Split(v, "-")
		if len(parts) >= 3 && len(parts[len(parts)-1]) == 1 {
			return v
		}
	}

	keys := make([]string, 0, len(g.config.AZs))
	for k := range g.config.AZs {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	for _, k := range keys {
		if z := g.config.AZs[k].Zone; z != "" {
			return z
		}
	}

	return ""
}

// CredentialsJSON resolves local credential sources before transport to the bastion.
func (g *GCPBastionInit) CredentialsJSON() (string, error) {
	source := g.config.ServiceAccountJSON
	if source == "" {
		source = g.config.ServiceAccountKeyPath
	}

	if source == "" {
		source = os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
	}

	if source == "" {
		return "", ErrGCPServiceAccountRequired
	}

	credentialConfig := gcpcpi.Config{ServiceAccountJSON: source}

	data, err := credentialConfig.GetServiceAccountCredentials()
	if err != nil {
		return "", fmt.Errorf("read GCP service account credentials: %w", err)
	}

	raw := string(data)
	if !json.Valid([]byte(raw)) {
		return "", errors.New("GCP service account credentials must be valid JSON")
	}

	return raw, nil
}

// getBastionIP retrieves the bastion IP address.
func (g *GCPBastionInit) getBastionIP() (string, error) {
	// Try multiple sources in order of priority

	// 1. Direct configuration
	if g.config.BastionIP != "" {
		return g.config.BastionIP, nil
	}

	// 2. Environment variable
	if ip := os.Getenv("GCP_BASTION_IP"); ip != "" {
		return ip, nil
	}

	if ip := os.Getenv("OCFP_BASTION_IP"); ip != "" {
		return ip, nil
	}

	// 3. State file
	ip, err := g.getBastionIPFromState()
	if err == nil && ip != "" {
		return ip, nil
	}

	// 4. Could query GCP API here if needed
	// This would require initializing the GCP client

	return "", ErrGCPBastionIPNotDetermined
}

// getBastionIPFromState retrieves the bastion IP from state file.
//
//nolint:unparam // signature kept for interface compatibility
func (g *GCPBastionInit) getBastionIPFromState() (string, error) {
	dir, err := state.GetStateDir(g.config.Name)
	if err != nil {
		return "", err
	}

	sm, err := state.NewManager(dir)
	if err != nil {
		return "", err
	}

	if _, err = sm.Load(g.config.Name); err != nil {
		return "", err
	}

	for _, key := range []string{"bastion_public_ip", "bastion_private_ip"} {
		if raw, e := sm.GetOutput(key); e == nil {
			if ip, ok := raw.(string); ok && ip != "" {
				return ip, nil
			}
		}
	}

	resource, err := sm.GetResource(state.ResourceTypeInstance, g.config.Name+"-bastion")
	if err == nil && resource != nil {
		for _, key := range []string{"public_ip", "private_ip"} {
			if ip, ok := resource.Properties[key].(string); ok && ip != "" {
				return ip, nil
			}
		}
	}
	return "", nil
}

// findSSHKey finds the SSH private key for the bastion.
func (g *GCPBastionInit) findSSHKey() (string, error) {
	// Check configuration first - bastion doesn't have a direct key path field
	// We'll use the SSHKeyDir and SSHKeyName
	if g.config.Bastion.SSHKeyDir != "" && g.config.Bastion.SSHKeyName != "" {
		keyPath := filepath.Join(g.config.Bastion.SSHKeyDir, g.config.Bastion.SSHKeyName)

		_, statErr := os.Stat(keyPath)
		if statErr == nil {
			return keyPath, nil
		}
	}

	// Check environment
	if keyPath := os.Getenv("OCFP_SSH_KEY"); keyPath != "" {
		_, statErr := os.Stat(keyPath) // #nosec G703 -- path from trusted env variable
		if statErr == nil {
			return keyPath, nil
		}
	}

	if key, err := bastionssh.NewKeyManager().FindPrivateKey(g.config.Name); err == nil {
		return key, nil
	}

	if key := g.config.Keys[g.config.Name+"-keypair"]; key != "" {
		return bastionssh.NewKeyManager().RestoreKeyFromConfig(g.config.Name, key)
	}

	// Check standard locations
	homeDir := os.Getenv("HOME")
	keyPaths := []string{
		filepath.Join(homeDir, ".ssh", g.config.Name+"-bastion"),
		filepath.Join(homeDir, ".ssh", g.config.Name+"-bastion.pem"),
		filepath.Join(homeDir, ".ssh", "id_ed25519"),
		filepath.Join(homeDir, ".ssh", "id_rsa"),
		filepath.Join(homeDir, ".ssh", "google_compute_engine"),
	}

	for _, keyPath := range keyPaths {
		_, statErr := os.Stat(keyPath) // #nosec G703 -- path components are from trusted HOME env
		if statErr == nil {
			return keyPath, nil
		}
	}

	return "", fmt.Errorf("%w, checked: %s", ErrNoSSHKeyFound, strings.Join(keyPaths, ", "))
}

// getSSHOptions returns SSH options for GCP connections.
func (g *GCPBastionInit) getSSHOptions() []string {
	return []string{
		"StrictHostKeyChecking=no", "UserKnownHostsFile=/dev/null", "LogLevel=ERROR", "ConnectTimeout=30", "ServerAliveInterval=60", "ServerAliveCountMax=3", "ForwardAgent=yes",
	}
}

// GCP-specific errors.
var (
	ErrGCPProjectIDRequired      = errors.New("GCP project ID is required")
	ErrGCPServiceAccountRequired = errors.New("GCP service account JSON or key path is required")
	ErrGCPZoneRequired           = errors.New("GCP zone is required")
)
