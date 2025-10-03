package providers

import (
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"go.uber.org/zap"
)

// PlaceholderProvider implements VaultProvider for providers that are not yet implemented.
type PlaceholderProvider struct {
	*BaseVaultProvider

	providerName string
	logger       *zap.SugaredLogger
}

// NewPlaceholderProvider creates a new placeholder provider.
func NewPlaceholderProvider(providerName string, cfg *config.Config, safe interface{}, blocName string) *PlaceholderProvider {
	return &PlaceholderProvider{
		BaseVaultProvider: NewBaseVaultProvider(cfg, blocName),
		providerName:      providerName,
		logger:            logger.Get(),
	}
}

// GetProviderName returns the provider name.
func (p *PlaceholderProvider) GetProviderName() string {
	return p.providerName
}

// Configure logs not implemented message.
func (p *PlaceholderProvider) Configure() error {
	p.logger.Warnw("Provider vault configuration not implemented", "provider", p.providerName)

	return p.NotImplementedError("Configure")
}

// ConfigurePublicIPs logs not implemented message.
func (p *PlaceholderProvider) ConfigurePublicIPs() error {
	p.logger.Warnw("Provider public IPs configuration not implemented", "provider", p.providerName)

	return p.NotImplementedError("ConfigurePublicIPs")
}

// SaveConfigToVault logs not implemented message.
func (p *PlaceholderProvider) SaveConfigToVault() error {
	p.logger.Warnw("Provider save config not implemented", "provider", p.providerName)

	return p.NotImplementedError("SaveConfigToVault")
}

// ConfigureIAAS logs not implemented message.
func (p *PlaceholderProvider) ConfigureIAAS(envPath, envType string) error {
	p.logger.Warnw("Provider IaaS configuration not implemented", "provider", p.providerName, "env_type", envType)

	return p.NotImplementedError("ConfigureIAAS")
}

// ConfigureBlobstores logs not implemented message.
func (p *PlaceholderProvider) ConfigureBlobstores(envPath, envType string) error {
	p.logger.Warnw("Provider blobstores configuration not implemented", "provider", p.providerName, "env_type", envType)

	return p.NotImplementedError("ConfigureBlobstores")
}

// ConfigureDatabases logs not implemented message.
func (p *PlaceholderProvider) ConfigureDatabases(envPath, envType string) error {
	p.logger.Warnw("Provider databases configuration not implemented", "provider", p.providerName, "env_type", envType)

	return p.NotImplementedError("ConfigureDatabases")
}

// ConfigureLoadBalancers logs not implemented message.
func (p *PlaceholderProvider) ConfigureLoadBalancers(envPath, envType string) error {
	p.logger.Warnw("Provider load balancers configuration not implemented", "provider", p.providerName, "env_type", envType)

	return p.NotImplementedError("ConfigureLoadBalancers")
}

// ConfigureFQDNs logs not implemented message.
func (p *PlaceholderProvider) ConfigureFQDNs(envPath, envType string) error {
	p.logger.Warnw("Provider FQDNs configuration not implemented", "provider", p.providerName, "env_type", envType)

	return p.NotImplementedError("ConfigureFQDNs")
}

// ConfigureCertificates logs not implemented message.
func (p *PlaceholderProvider) ConfigureCertificates(envPath, envType string) error {
	p.logger.Warnw("Provider certificates configuration not implemented", "provider", p.providerName, "env_type", envType)

	return p.NotImplementedError("ConfigureCertificates")
}
