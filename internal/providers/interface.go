// Package providers defines interfaces and base types for cloud provider vault operations.
package providers

import (
	"fmt"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/config"
)

// ProgressReporter defines the interface for progress reporting during vault operations.
type ProgressReporter interface {
	ReportPhaseStart(phase string, index, total int)
	ReportPhaseComplete(phase string, duration time.Duration)
	ReportSubtaskProgress(phase string, current, total int, label string)
	ReportError(phase string, err error, attempt, maxAttempts, number, total int)
	ReportFinalSummary(success bool, duration time.Duration, phases int, errors int)
}

// VaultProvider defines the interface that all cloud providers must implement
// for vault-related operations.
type VaultProvider interface {
	// Configure performs full vault configuration for the provider
	Configure(reporter ProgressReporter) error

	// ConfigurePublicIPs configures public IP addresses in vault
	ConfigurePublicIPs(reporter ProgressReporter, phaseNum, totalPhases int) error

	// SaveConfigToVault saves the OCFP configuration to vault
	SaveConfigToVault(reporter ProgressReporter, phaseNum, totalPhases int) error

	// ConfigureIAAS configures IaaS-specific settings (VPC, subnets, etc.)
	ConfigureIAAS(envPath, envType string, reporter ProgressReporter, phaseNum *int, totalPhases int) error

	// ConfigureBlobstores configures blobstore settings
	ConfigureBlobstores(envPath, envType string, reporter ProgressReporter, phaseNum, totalPhases int) error

	// ConfigureDatabases configures database settings
	ConfigureDatabases(envPath, envType string, reporter ProgressReporter, phaseNum, totalPhases int) error

	// ConfigureLoadBalancers configures load balancer settings
	ConfigureLoadBalancers(envPath, envType string, reporter ProgressReporter, phaseNum, totalPhases int) error

	// ConfigureFQDNs configures fully qualified domain names
	ConfigureFQDNs(envPath, envType string, reporter ProgressReporter, phaseNum, totalPhases int) error

	// ConfigureCertificates configures TLS certificates
	ConfigureCertificates(envPath, envType string, reporter ProgressReporter, phaseNum, totalPhases int) error

	// GetProviderName returns the provider name
	GetProviderName() string
}

// BaseVaultProvider provides common functionality for all providers
// Note: This is now just the interface - implementations need to embed their own Safe and PathBuilder.
type BaseVaultProvider struct {
	Config   *config.Config
	BlocName string
}

// NewBaseVaultProvider creates a new base provider (lightweight now).
func NewBaseVaultProvider(cfg *config.Config, blocName string) *BaseVaultProvider {
	return &BaseVaultProvider{
		Config:   cfg,
		BlocName: blocName,
	}
}

// NotImplementedError is a helper for unimplemented methods.
func (b *BaseVaultProvider) NotImplementedError(method string) error {
	return &NotImplementedError{
		Provider: b.GetProviderName(),
		Method:   method,
	}
}

// NotImplementedError represents an unimplemented method error.
type NotImplementedError struct {
	Provider string
	Method   string
}

func (e *NotImplementedError) Error() string {
	return fmt.Sprintf("%s provider: %s method not implemented", e.Provider, e.Method)
}

// GetProviderName returns a default provider name (should be overridden).
func (b *BaseVaultProvider) GetProviderName() string {
	return "unknown"
}
