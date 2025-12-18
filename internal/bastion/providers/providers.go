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

// Placeholder implementations for other providers
// These would be implemented in their respective files

// AzureBastionInit implements bastion initialization for Azure.
type AzureBastionInit struct {
	config *config.Config
	log    logger.Logger
}

func NewAzureBastionInit(cfg *config.Config) *AzureBastionInit {
	return &AzureBastionInit{config: cfg, log: logger.Get()}
}

func (a *AzureBastionInit) Validate() error {
	return ErrAzureProviderNotImplemented
}

func (a *AzureBastionInit) PrepareEnvironment() map[string]string {
	return map[string]string{"OCFP_PROVIDER": "azure"}
}

func (a *AzureBastionInit) GetConnectionDetails() (*ConnectionDetails, error) {
	return nil, ErrAzureProviderNotImplemented
}

func (a *AzureBastionInit) Initialize(ctx context.Context) error {
	return ErrAzureProviderNotImplemented
}

// NOTE: GCPBastionInit is implemented in gcp.go

// OpenStackBastionInit implements bastion initialization for OpenStack.
type OpenStackBastionInit struct {
	config *config.Config
	log    logger.Logger
}

func NewOpenStackBastionInit(cfg *config.Config) *OpenStackBastionInit {
	return &OpenStackBastionInit{config: cfg, log: logger.Get()}
}

func (o *OpenStackBastionInit) Validate() error {
	return ErrOpenStackProviderNotImplemented
}

func (o *OpenStackBastionInit) PrepareEnvironment() map[string]string {
	return map[string]string{"OCFP_PROVIDER": "openstack"}
}

func (o *OpenStackBastionInit) GetConnectionDetails() (*ConnectionDetails, error) {
	return nil, ErrOpenStackProviderNotImplemented
}

func (o *OpenStackBastionInit) Initialize(ctx context.Context) error {
	return ErrOpenStackProviderNotImplemented
}

// VMwareBastionInit implements bastion initialization for VMware.
type VMwareBastionInit struct {
	config *config.Config
	log    logger.Logger
}

func NewVMwareBastionInit(cfg *config.Config) *VMwareBastionInit {
	return &VMwareBastionInit{config: cfg, log: logger.Get()}
}

func (v *VMwareBastionInit) Validate() error {
	return ErrVMwareProviderNotImplemented
}

func (v *VMwareBastionInit) PrepareEnvironment() map[string]string {
	return map[string]string{"OCFP_PROVIDER": "vmware"}
}

func (v *VMwareBastionInit) GetConnectionDetails() (*ConnectionDetails, error) {
	return nil, ErrVMwareProviderNotImplemented
}

func (v *VMwareBastionInit) Initialize(ctx context.Context) error {
	return ErrVMwareProviderNotImplemented
}
