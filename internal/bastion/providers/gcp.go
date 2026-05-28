package providers

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	gcpcpi "github.com/ocfp/ocfp-cli-go/internal/cpi/gcp"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
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
	// Check for required GCP configuration
	if g.config.ProjectID == "" {
		// Try to get from environment
		if projectID := os.Getenv("GOOGLE_PROJECT"); projectID == "" {
			if projectID = os.Getenv("GOOGLE_CLOUD_PROJECT"); projectID == "" {
				return ErrGCPProjectIDRequired
			}
		}
	}

	// Check for service account credentials
	if g.config.ServiceAccountJSON == "" && g.config.ServiceAccountKeyPath == "" {
		// Try to get from environment
		if os.Getenv("GOOGLE_APPLICATION_CREDENTIALS") == "" {
			return ErrGCPServiceAccountRequired
		}
	}

	// Check for zone/region
	if g.config.Region == "" {
		if os.Getenv("GOOGLE_ZONE") == "" && os.Getenv("CLOUDSDK_COMPUTE_ZONE") == "" {
			return ErrGCPZoneRequired
		}
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
	if g.config.ProjectID != "" {
		env["GOOGLE_PROJECT"] = g.config.ProjectID
		env["GOOGLE_CLOUD_PROJECT"] = g.config.ProjectID
		env["CLOUDSDK_CORE_PROJECT"] = g.config.ProjectID
	}

	// Set zone and region
	zone := g.config.Region
	if zone != "" {
		env["GOOGLE_ZONE"] = zone
		env["CLOUDSDK_COMPUTE_ZONE"] = zone
		// Derive region from zone
		region := gcpcpi.GetRegionFromZone(zone)
		env["GOOGLE_REGION"] = region
		env["CLOUDSDK_COMPUTE_REGION"] = region
	}

	// Set credentials path
	if g.config.ServiceAccountKeyPath != "" {
		env["GOOGLE_APPLICATION_CREDENTIALS"] = g.config.ServiceAccountKeyPath
	} else if g.config.ServiceAccountJSON != "" {
		// If inline JSON, we might need to write it to a temp file
		// For now, just note that it's configured
		env["OCFP_GCP_CREDENTIALS_INLINE"] = "true"
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
	// Genesis expects these variables
	if g.config.ProjectID != "" {
		env["GCP_PROJECT"] = g.config.ProjectID
	}

	zone := g.config.Region
	if zone != "" {
		env["GCP_ZONE"] = zone
		env["GCP_REGION"] = gcpcpi.GetRegionFromZone(zone)
	}
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
	// Look for state file in standard locations
	statePaths := []string{
		filepath.Join(config.OcfpBlocDir(g.config.Name), "state.json"),
		filepath.Join(".", "state.json"),
	}

	for _, statePath := range statePaths {
		_, statErr := os.Stat(statePath) //nolint:gosec // path from trusted config
		if statErr == nil {              //nolint:gosec // path components are from trusted config
			// State file exists - would parse and extract bastion IP
			// For now, return empty to fall through to other methods
			g.log.Debugw("Found state file", "path", statePath)

			break
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
		_, statErr := os.Stat(keyPath) //nolint:gosec // path from trusted env variable
		if statErr == nil {
			return keyPath, nil
		}
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
		_, statErr := os.Stat(keyPath) //nolint:gosec // path components are from trusted HOME env
		if statErr == nil {
			return keyPath, nil
		}
	}

	return "", fmt.Errorf("%w, checked: %s", ErrNoSSHKeyFound, strings.Join(keyPaths, ", "))
}

// getSSHOptions returns SSH options for GCP connections.
func (g *GCPBastionInit) getSSHOptions() []string {
	return []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-o", "ConnectTimeout=30",
		"-o", "ServerAliveInterval=60",
		"-o", "ServerAliveCountMax=3",
	}
}

// GCP-specific errors.
var (
	ErrGCPProjectIDRequired      = errors.New("GCP project ID is required")
	ErrGCPServiceAccountRequired = errors.New("GCP service account JSON or key path is required")
	ErrGCPZoneRequired           = errors.New("GCP zone is required")
)
