package provision

import "time"

// Common provider names used in provisioning logic.
const (
	providerStackit   = "stackit"
	providerAWS       = "aws"
	providerAzure     = "azure"
	providerGCP       = "gcp"
	providerOpenStack = "openstack"
	providerVMware    = "vmware"
	providerVsphere   = "vsphere"
	providerPVE       = "pve"
)

// Conditional keys used in config-driven steps.
const (
	condProviderIsStackit   = "provider_is_stackit"
	condProviderIsAWS       = "provider_is_aws"
	condProviderIsAzure     = "provider_is_azure"
	condProviderIsGCP       = "provider_is_gcp"
	condProviderIsOpenstack = "provider_is_openstack"
	condProviderIsVMware    = "provider_is_vmware"
	condProviderIsPVE       = "provider_is_pve"
)

// Paths for the spruce-compatible toolchain: graft is linked as `spruce`,
// upstream spruce is kept alongside it under its own name.
const (
	spruceLinkPath = "/usr/local/bin/spruce"
	spruceOrigPath = "/usr/local/bin/spruce-orig"
)

// File permissions used in provisioning.
const (
	directoryModeStandard = 0755
	directoryModeSSH      = 0700
	fileModeStandard      = 0644
	fileModeExecutable    = 0755
)

// System configuration defaults.
const (
	systemWaitTimeSeconds = 10
)

// Script generation buffer sizes.
const (
	scriptBufferEnvVars             = 6
	scriptBufferBase                = 64
	scriptBufferDirectoriesBase     = 16
	scriptBufferDirectoriesPerItem  = 16
	scriptBufferEnvironmentBase     = 32
	scriptBufferEnvironmentPerItem  = 3
	scriptBufferOCFPBase            = 16
	scriptBufferOCFPPerTool         = 6
	scriptBufferOCFP1               = 32
	scriptBufferOCFP2               = 48
	scriptBufferOCFP3               = 48
	scriptBufferScriptBase          = 4
	scriptBufferScript1             = 64
	scriptBufferScript2             = 8
	scriptBufferScript3             = 8
	scriptBufferScript4             = 8
	scriptBufferScript5             = 4
	scriptBufferSnapBase            = 24
	scriptBufferSnapPerPackage      = 20
	scriptBufferBrewInstall         = 32
	scriptBufferBrewBase            = 24
	scriptBufferBrewPerPackage      = 8
	scriptBufferToolsBase           = 24
	scriptBufferToolsPerItem        = 16
	scriptBufferVerificationBase    = 16
	scriptBufferVerificationPerItem = 12
	scriptBufferVerification1       = 32
	scriptBufferVerification2       = 8
	scriptBufferVerification3       = 48
	scriptBufferCPANBase            = 64
	scriptBufferEnvironment         = 24
)

// Script parsing configuration.
const (
	minScriptParts = 2
	maxScriptParts = 2

	// HTTP client configuration.
	httpClientTimeout = 10 * time.Second

	// Regular expression matching.
	minRegexMatches = 2

	// Mathematical constants.
	decimalBase = 10
)
