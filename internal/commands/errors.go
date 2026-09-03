package commands

import (
	"errors"
	"fmt"
)

// Command errors.
var (
	// ErrBootstrapStateRequired is returned when createBOSHManifest cannot find
	// required outputs (subnet ID, BOSH static IP) in state. Run `ocfp bootstrap`
	// before `ocfp init bosh` to populate these values.
	ErrBootstrapStateRequired = errors.New("bootstrap state is required: run `ocfp bootstrap` before `ocfp init bosh` to populate subnet and IP state")

	ErrProviderDoesNotSupportStorageOperations = errors.New("provider does not support storage operations")
	ErrNoPreviousBackupFound                   = errors.New("no previous backup found")
	ErrNoProviderConfigured                    = errors.New("no provider configured")
	ErrNoBlocsFoundInConfigAndBlocNotProvided  = errors.New("no blocs found in config and --bloc not provided")
	ErrNoMatchingBlocsFoundForSelection        = errors.New("no matching blocs found for selection")
	ErrBlocIsRequired                          = errors.New("bloc is required")
	ErrProviderDoesNotSupportSecurityMgmt      = errors.New("provider does not support security management")
	ErrProviderDoesNotSupportNetworkMgmt       = errors.New("provider does not support network management")
	ErrProviderDoesNotSupportComputeMgmt       = errors.New("provider does not support compute management")
	ErrNoEnvironmentSpecifiedAndNoCurrentSet   = errors.New("no environment specified and no current environment set")
	ErrProviderLacksLoadBalancerManager        = errors.New("provider lacks load balancer manager")
	ErrProviderLacksNetworkManager             = errors.New("provider lacks network manager")
	ErrProviderNotSpecified                    = errors.New("provider not specified. Use --iaas flag, OCFP_PROVIDER environment variable, or specify in config")
	ErrLoadBalancerNameRequired                = errors.New("load balancer name is required")
	ErrLoadBalancerNameRequiredOrUseAll        = errors.New("load balancer name required (or use --all)")
	ErrInvalidReservedFormat                   = errors.New("invalid reserved format; expected reserved:<key>[:index]")
	ErrInvalidPublicIPToken                    = errors.New("invalid public-ip token; expected public-ip:<job>[:index]")
	ErrNameIsRequired                          = errors.New("--name is required")
	ErrBlocFlagOrEnvVarRequired                = errors.New("--bloc flag or OCFP_BLOC environment variable required")
	ErrCouldNotRetrieveStackitCredentials      = errors.New("could not retrieve STACKIT service account credentials from config or vault")
	ErrPublicIPListingSupportedForStackitOnly  = errors.New("public IP listing is currently supported for STACKIT only")
	ErrProviderDoesNotSupportPublicIPListing   = errors.New("provider does not support public IP listing")
	ErrPopulateFromFileNotImplemented          = errors.New("populate from file not yet implemented")
	ErrVaultPathIsRequired                     = errors.New("vault path is required")
	ErrVaultPathAndInputFileRequired           = errors.New("vault path and input file are required")
	ErrUnableToParseFileAsYAMLOrJSON           = errors.New("unable to parse file as YAML or JSON")
	ErrSecretsFileNotFoundInBackup             = errors.New("secrets file not found in backup")
	ErrInvalidRsyncCommand                     = errors.New("invalid rsync command")
	ErrCountMustBeNonNegative                  = errors.New("count must be non-negative")
	ErrGenericInstanceScalingNotImplemented    = errors.New("generic instance scaling not yet implemented")
	ErrNoLoadBalancersFound                    = errors.New("no load balancers found")
	ErrDatabaseScalingNotImplemented           = errors.New("database scaling not yet implemented")
	ErrInvalidSCPCommand                       = errors.New("invalid SCP command")
	ErrInvalidSSHCommand                       = errors.New("invalid SSH command")
	ErrNukeRequiresForceForSafety              = errors.New("--nuke requires --force for safety")
	ErrNoStateLoaded                           = errors.New("no state loaded")
	ErrProviderDoesNotSupportStorageMgmt       = errors.New("provider does not support storage management")
	ErrProviderDoesNotSupportCredGroupDeletion = errors.New("provider does not support credentials group deletion")
	ErrProviderDoesNotSupportPublicIPDeletion  = errors.New("provider does not support public IP deletion")
	ErrTmuxNotInstalled                        = errors.New("tmux is not installed. Please install tmux to use this command")
	ErrTmuxNotFound                            = errors.New("'tmux' command not found - please install tmux")
	ErrTmuxSessionFailed                       = errors.New("failed to create tmux session")
	ErrBlocRequired                            = errors.New("--bloc flag is required")
	ErrAWSCredentialsNotFound                  = errors.New("AWS credentials not found in config or vault")
	ErrInvalidMergeStrategy                    = errors.New("invalid merge strategy")
	ErrInvalidOutputFormat                     = errors.New("invalid output format")
	ErrOutputFileRequired                      = errors.New("output file required")
	ErrFileAlreadyExists                       = errors.New("file already exists")
	ErrUnsupportedOutputFormat                 = errors.New("unsupported output format")
	ErrEmptyFilterQuery                        = errors.New("empty filter query")
	ErrInvalidFilterSyntax                     = errors.New("invalid filter syntax")
	ErrFilterMissingOperator                   = errors.New("filter must contain operator")
	ErrFilterKeyEmpty                          = errors.New("filter key cannot be empty")
	ErrSortByEmpty                             = errors.New("sort-by cannot be empty")
	ErrInvalidSortByFormat                     = errors.New("invalid sort-by format")
	ErrInvalidSortField                        = errors.New("invalid sort field")
	ErrInvalidSortDirection                    = errors.New("invalid sort direction")
	ErrStateIsNil                              = errors.New("state is nil")
	ErrInvalidHealthCheckTimeout               = errors.New("invalid health check timeout")
)

// ErrInvalidBackupType returns an error for an unrecognized backup type.
func ErrInvalidBackupType(backupType string) error {
	return fmt.Errorf("invalid backup type: %s", backupType) //nolint:err113 // dynamic error with context
}

// ErrInvalidS3Destination returns an error for a malformed S3 destination.
func ErrInvalidS3Destination(destination string) error {
	return fmt.Errorf("invalid S3 destination: %s", destination) //nolint:err113 // dynamic error with context
}

// ErrUnknownBastionAction returns an error for an unrecognized bastion action.
func ErrUnknownBastionAction(action string) error {
	return fmt.Errorf("unknown bastion action: %s. Available actions: init, provision", action) //nolint:err113 // dynamic error with context
}

// ErrScriptNotFound returns an error when a script cannot be located in any search path.
func ErrScriptNotFound(scriptName string) error {
	return fmt.Errorf("script '%s' not found in any search paths", scriptName) //nolint:err113 // dynamic error with context
}

// ErrNoBastionHostFound returns an error when no bastion host is found for the given bloc.
func ErrNoBastionHostFound(blocName string) error {
	return fmt.Errorf("no bastion host found for bloc %s", blocName) //nolint:err113 // dynamic error with context
}

// ErrProviderMustBeSpecifiedInBlocConfig returns an error when a bloc config is missing its provider.
func ErrProviderMustBeSpecifiedInBlocConfig(blocName string) error {
	return fmt.Errorf("provider must be specified in bloc config '%s'", blocName) //nolint:err113 // dynamic error with context
}

// ErrEnvironmentNotFound returns an error when the specified environment does not exist.
func ErrEnvironmentNotFound(envName string) error {
	return fmt.Errorf("environment '%s' not found", envName) //nolint:err113 // dynamic error with context
}

// ErrUnsupportedExportFormat returns an error for an unsupported export format.
func ErrUnsupportedExportFormat(format string) error {
	return fmt.Errorf("unsupported export format: %s", format) //nolint:err113 // dynamic error with context
}

// ErrUnknownComponent returns an error for an unrecognized deployment component.
func ErrUnknownComponent(component string) error {
	return fmt.Errorf("unknown component: %s", component) //nolint:err113 // dynamic error with context
}

// ErrFailedToFetchKeys returns an error when provider key retrieval fails.
func ErrFailedToFetchKeys(provider, status string) error {
	return fmt.Errorf("failed to fetch %s keys: %s", provider, status) //nolint:err113 // dynamic error with context
}

// ErrOutputNotFound returns an error when a state output key is not found.
func ErrOutputNotFound(stateKey string) error {
	return fmt.Errorf("output %s not found", stateKey) //nolint:err113 // dynamic error with context
}

// ErrOutputEmptyOrNotString returns an error when a state output is empty or not a string.
func ErrOutputEmptyOrNotString(stateKey string) error {
	return fmt.Errorf("output %s empty or not string", stateKey) //nolint:err113 // dynamic error with context
}

// ErrNoMatchingPublicIPForJob returns an error when no public IP matches the given job and index.
func ErrNoMatchingPublicIPForJob(job, index string) error {
	return fmt.Errorf("no matching public-ip for job %s index %s", job, index) //nolint:err113 // dynamic error with context
}

// ErrNetworkManagerNotAvailableForProvider returns an error when the provider lacks a network manager.
func ErrNetworkManagerNotAvailableForProvider(provider string) error {
	return fmt.Errorf("network manager not available for provider %s", provider) //nolint:err113 // dynamic error with context
}

// ErrInvalidRestoreMode returns an error for an unrecognized restore mode.
func ErrInvalidRestoreMode(mode string) error {
	return fmt.Errorf("invalid restore mode: %s", mode) //nolint:err113 // dynamic error with context
}

// ErrUnsupportedFormat returns an error for an unsupported output format.
func ErrUnsupportedFormat(format string) error {
	return fmt.Errorf("unsupported format: %s", format) //nolint:err113 // dynamic error with context
}

// ErrLBEntryNotFoundInConfig returns an error when a load balancer entry is missing from configuration.
func ErrLBEntryNotFoundInConfig(name string) error {
	return fmt.Errorf("lbs entry '%s' not found in config", name) //nolint:err113 // dynamic error with context
}

// ErrLBPortMustBeGreaterThanZero returns an error when a load balancer port is zero or negative.
func ErrLBPortMustBeGreaterThanZero(name string) error {
	return fmt.Errorf("lbs.%s.port must be > 0", name) //nolint:err113 // dynamic error with context
}

// ErrUnknownProviderAction returns an error for an unrecognized provider action.
func ErrUnknownProviderAction(action string) error {
	return fmt.Errorf("unknown provider action '%s'. Available actions: login", action) //nolint:err113 // dynamic error with context
}

// ErrUnsupportedProvider returns an error for an unsupported cloud provider.
func ErrUnsupportedProvider(providerName string) error {
	return fmt.Errorf("unsupported provider '%s'", providerName) //nolint:err113 // dynamic error with context
}

// ErrInvalidCount returns an error for an invalid count value.
func ErrInvalidCount(countStr string) error {
	return fmt.Errorf("invalid count: %s", countStr) //nolint:err113 // dynamic error with context
}

// ErrUnknownResourceType returns an error for an unrecognized resource type.
func ErrUnknownResourceType(resource string) error {
	return fmt.Errorf("unknown resource type: %s", resource) //nolint:err113 // dynamic error with context
}

// ErrCouldNotFindSSHKeyForBastion returns an error when the bastion SSH key is not found in any search path.
func ErrCouldNotFindSSHKeyForBastion(searchPaths []string) error {
	return fmt.Errorf("could not find SSH key for bastion. Searched paths: %v", searchPaths) //nolint:err113 // dynamic error with context
}

// ErrSSHKeyNotFound returns an error when the specified SSH key file does not exist.
func ErrSSHKeyNotFound(keyPath string) error {
	return fmt.Errorf("SSH key not found: %s", keyPath) //nolint:err113 // dynamic error with context
}

// ErrSSHKeyIncorrectPermissions returns an error when the SSH key has wrong permissions that cannot be fixed.
func ErrSSHKeyIncorrectPermissions(keyPath string) error {
	return fmt.Errorf("SSH key has incorrect permissions and couldn't fix: %s", keyPath) //nolint:err113 // dynamic error with context
}

// ErrUnsupportedResourceType returns an error for an unsupported resource type.
func ErrUnsupportedResourceType(resourceType string) error {
	return fmt.Errorf("unsupported resource type: %s", resourceType) //nolint:err113 // dynamic error with context
}

// ErrUnsupportedStorageResourceType returns an error for an unsupported storage resource type.
func ErrUnsupportedStorageResourceType(resourceType string) error {
	return fmt.Errorf("unsupported storage resource type: %s", resourceType) //nolint:err113 // dynamic error with context
}

// ErrUnsupportedNetworkResourceType returns an error for an unsupported network resource type.
func ErrUnsupportedNetworkResourceType(resourceType string) error {
	return fmt.Errorf("unsupported network resource type: %s", resourceType) //nolint:err113 // dynamic error with context
}

// ErrUnknownTestType returns an error for an unrecognized test type.
func ErrUnknownTestType(testType string) error {
	return fmt.Errorf("unknown test type: %s", testType) //nolint:err113 // dynamic error with context
}

// ErrTestsFailed returns an error summarizing test results when failures occur.
func ErrTestsFailed(passed, failed, skipped int) error {
	return fmt.Errorf("tests failed: %d passed, %d failed, %d skipped", passed, failed, skipped) //nolint:err113 // dynamic error with context
}

// ErrUnsupportedTestSuite returns an error for an unsupported test suite.
func ErrUnsupportedTestSuite(suite string) error {
	return fmt.Errorf("unsupported test suite: %s", suite) //nolint:err113 // dynamic error with context
}

// ErrTestDirectoryNotFound returns an error when the test directory does not exist.
func ErrTestDirectoryNotFound(dir string) error {
	return fmt.Errorf("test directory not found: %s", dir) //nolint:err113 // dynamic error with context
}

// ErrUnknownTestSuite returns an error for an unrecognized test suite.
func ErrUnknownTestSuite(suite string) error {
	return fmt.Errorf("unknown test suite: %s", suite) //nolint:err113 // dynamic error with context
}
