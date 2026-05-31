// Package providers implements cloud provider-specific bastion initialization.
package providers

import (
	"context"
	"errors"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// Provider implementation errors.
var (
	ErrAzureProviderNotImplemented     = errors.New("azure provider not fully implemented")
	ErrOpenStackProviderNotImplemented = errors.New("OpenStack provider not fully implemented")
	ErrVMwareProviderNotImplemented    = errors.New("VMware provider not fully implemented")
)

// AzureBastionInit implements bastion initialization for Azure.
type AzureBastionInit struct {
	config *config.Config
	log    logger.Logger
}

// NewAzureBastionInit creates a new Azure bastion initializer.
func NewAzureBastionInit(cfg *config.Config) *AzureBastionInit {
	return &AzureBastionInit{config: cfg, log: logger.Get()}
}

// Validate validates the Azure configuration.
func (a *AzureBastionInit) Validate() error {
	return ErrAzureProviderNotImplemented
}

// PrepareEnvironment prepares Azure-specific environment variables.
func (a *AzureBastionInit) PrepareEnvironment() map[string]string {
	return map[string]string{"OCFP_PROVIDER": "azure"}
}

// GetConnectionDetails returns SSH connection details for the Azure bastion.
func (a *AzureBastionInit) GetConnectionDetails(_ context.Context) (*ConnectionDetails, error) {
	return nil, ErrAzureProviderNotImplemented
}

// OpenStackBastionInit implements bastion initialization for OpenStack.
type OpenStackBastionInit struct {
	config *config.Config
	log    logger.Logger
}

// NewOpenStackBastionInit creates a new OpenStack bastion initializer.
func NewOpenStackBastionInit(cfg *config.Config) *OpenStackBastionInit {
	return &OpenStackBastionInit{config: cfg, log: logger.Get()}
}

// Validate validates the OpenStack configuration.
func (o *OpenStackBastionInit) Validate() error {
	return ErrOpenStackProviderNotImplemented
}

// PrepareEnvironment prepares OpenStack-specific environment variables.
func (o *OpenStackBastionInit) PrepareEnvironment() map[string]string {
	return map[string]string{"OCFP_PROVIDER": "openstack"}
}

// GetConnectionDetails returns SSH connection details for the OpenStack bastion.
func (o *OpenStackBastionInit) GetConnectionDetails(_ context.Context) (*ConnectionDetails, error) {
	return nil, ErrOpenStackProviderNotImplemented
}

// VMwareBastionInit implements bastion initialization for VMware.
type VMwareBastionInit struct {
	config *config.Config
	log    logger.Logger
}

// NewVMwareBastionInit creates a new VMware bastion initializer.
func NewVMwareBastionInit(cfg *config.Config) *VMwareBastionInit {
	return &VMwareBastionInit{config: cfg, log: logger.Get()}
}

// Validate validates the VMware configuration.
func (v *VMwareBastionInit) Validate() error {
	return ErrVMwareProviderNotImplemented
}

// PrepareEnvironment prepares VMware-specific environment variables.
func (v *VMwareBastionInit) PrepareEnvironment() map[string]string {
	return map[string]string{"OCFP_PROVIDER": "vmware"}
}

// GetConnectionDetails returns SSH connection details for the VMware bastion.
func (v *VMwareBastionInit) GetConnectionDetails(_ context.Context) (*ConnectionDetails, error) {
	return nil, ErrVMwareProviderNotImplemented
}
