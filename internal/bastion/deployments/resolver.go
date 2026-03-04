// Package deployments resolves deployment modes and paths for bastion provisioning.
package deployments

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/config"
)

// Resolver determines deployment modes and derived paths for bastion provisioning.
type Resolver struct {
	cfg  *config.Config
	home string
}

// NewResolver creates a new deployment resolver for the supplied configuration.
func NewResolver(cfg *config.Config) *Resolver {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}

	return &Resolver{
		cfg:  cfg,
		home: home,
	}
}

// SetHome overrides the home directory used for path calculations (primarily for tests).
func (r *Resolver) SetHome(home string) {
	r.home = home
}

// GlobalURL returns the configured global deployments repository URL.
func (r *Resolver) GlobalURL() string {
	if r == nil || r.cfg == nil {
		return ""
	}

	return r.cfg.GetDeploymentsURL()
}

// Mode returns the effective mode for the given deployment name.
func (r *Resolver) Mode(name string) string {
	if r == nil || r.cfg == nil {
		return config.DeploymentModeDev
	}

	return r.cfg.GetDeploymentMode(name)
}

// IsRelease returns true when the named deployment should operate in release mode.
func (r *Resolver) IsRelease(name string) bool {
	return r.Mode(name) == config.DeploymentModeRelease
}

// IsDev returns true when the named deployment should operate in dev mode.
func (r *Resolver) IsDev(name string) bool {
	return r.Mode(name) != config.DeploymentModeRelease
}

// Configured returns the configured deployment identifiers (sorted).
func (r *Resolver) Configured() []string {
	if r == nil || r.cfg == nil {
		return nil
	}

	names := append([]string{}, r.cfg.GetConfiguredDeployments()...)
	sort.Strings(names)

	return names
}

// DeploymentsRoot returns the root directory for checked-out deployments.
func (r *Resolver) DeploymentsRoot() string {
	return filepath.Join(r.home, "ocfp", "deployments")
}

// KitsRoot returns the root directory for local kit checkouts.
func (r *Resolver) KitsRoot() string {
	return filepath.Join(r.home, "ocfp", "kits")
}

// DeploymentPath returns the expected filesystem path for a given deployment.
func (r *Resolver) DeploymentPath(name string) string {
	if name == "" {
		return r.DeploymentsRoot()
	}

	return filepath.Join(r.DeploymentsRoot(), name)
}

// KitPath returns the local kit path for a dev-mode deployment. Empty string for release mode.
func (r *Resolver) KitPath(name string) string {
	if !r.IsDev(name) {
		return ""
	}

	if strings.TrimSpace(r.GlobalURL()) != "" {
		return filepath.Join(r.KitsRoot(), name)
	}

	return filepath.Join(r.KitsRoot(), name, "dev")
}

// ShouldCloneDeploymentRepo indicates whether the global deployments repository should be cloned.
func (r *Resolver) ShouldCloneDeploymentRepo() bool {
	return strings.TrimSpace(r.GlobalURL()) != ""
}

// ShouldCloneKit reports whether the given deployment requires a kit checkout.
func (r *Resolver) ShouldCloneKit(name string) bool {
	return r.IsDev(name)
}

// Validate ensures deployment mode configuration is consistent.
func (r *Resolver) Validate(names ...string) error {
	if r == nil || r.cfg == nil {
		return nil
	}

	if len(names) == 0 {
		names = r.cfg.GetConfiguredDeployments()
	}

	url := strings.TrimSpace(r.GlobalURL())
	if url == "" {
		for _, name := range names {
			if r.IsRelease(name) {
				//nolint:err113 // Dynamic error with deployment name context
				return fmt.Errorf("deployment %s is set to release mode but deployments.url is not configured", name)
			}
		}
	}

	return nil
}
