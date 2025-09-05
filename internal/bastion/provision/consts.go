package provision

// Common provider names used in provisioning logic.
const (
    providerStackit   = "stackit"
    providerAWS       = "aws"
    providerAzure     = "azure"
    providerGCP       = "gcp"
    providerOpenStack = "openstack"
    providerVMware    = "vmware"
    providerVsphere   = "vsphere"
)

// Conditional keys used in config-driven steps.
const (
    condProviderIsStackit   = "provider_is_stackit"
    condProviderIsAWS       = "provider_is_aws"
    condProviderIsAzure     = "provider_is_azure"
    condProviderIsGCP       = "provider_is_gcp"
    condProviderIsOpenstack = "provider_is_openstack"
    condProviderIsVMware    = "provider_is_vmware"
)

