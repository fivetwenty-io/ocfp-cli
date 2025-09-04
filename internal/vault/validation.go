package vault

import (
	"fmt"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"go.uber.org/zap"
)

// Validator provides validation and safety features for vault operations
type Validator struct {
	client *Client
	safe   *Safe
	config *config.Config
	logger *zap.SugaredLogger
}

// NewValidator creates a new vault validator
func NewValidator(client *Client, safe *Safe, cfg *config.Config) *Validator {
	return &Validator{
		client: client,
		safe:   safe,
		config: cfg,
		logger: logger.Get(),
	}
}

// ValidationResult holds the result of a validation check
type ValidationResult struct {
	Valid      bool
	Warnings   []string
	Errors     []string
	Suggestion string
}

// AddWarning adds a warning to the validation result
func (vr *ValidationResult) AddWarning(warning string) {
	vr.Warnings = append(vr.Warnings, warning)
}

// AddError adds an error to the validation result
func (vr *ValidationResult) AddError(err string) {
	vr.Errors = append(vr.Errors, err)
	vr.Valid = false
}

// HasIssues returns true if there are any warnings or errors
func (vr *ValidationResult) HasIssues() bool {
	return len(vr.Warnings) > 0 || len(vr.Errors) > 0
}

// PreMigrationHealthCheck performs comprehensive health checks before migration
func (v *Validator) PreMigrationHealthCheck(inceptionPath, targetPath string) (*ValidationResult, error) {
	v.logger.Info("Starting pre-migration health check", "inception", inceptionPath, "target", targetPath)

	result := &ValidationResult{Valid: true}

	// Check vault connectivity
	if err := v.validateVaultConnectivity(result); err != nil {
		return result, fmt.Errorf("vault connectivity check failed: %w", err)
	}

	// Check inception vault exists and has data
	if err := v.validateInceptionVault(inceptionPath, result); err != nil {
		return result, fmt.Errorf("inception vault validation failed: %w", err)
	}

	// Check target vault accessibility
	if err := v.validateTargetVault(targetPath, result); err != nil {
		return result, fmt.Errorf("target vault validation failed: %w", err)
	}

	// Check for active BOSH deployments
	if err := v.checkActiveBOSHDeployments(result); err != nil {
		v.logger.Warn("BOSH deployment check failed", "error", err)
		result.AddWarning("Could not verify BOSH deployment status")
	}

	// Check for conflicting secrets
	if err := v.checkSecretConflicts(inceptionPath, targetPath, result); err != nil {
		return result, fmt.Errorf("secret conflict check failed: %w", err)
	}

	// Validate vault policies
	if err := v.validateVaultPolicies(result); err != nil {
		v.logger.Warn("Vault policy validation failed", "error", err)
		result.AddWarning("Could not validate vault policies")
	}

	v.logger.Info("Pre-migration health check completed", "valid", result.Valid, "warnings", len(result.Warnings), "errors", len(result.Errors))
	return result, nil
}

// validateVaultConnectivity checks basic vault connectivity
func (v *Validator) validateVaultConnectivity(result *ValidationResult) error {
	v.logger.Debug("Validating vault connectivity")

	if err := v.client.ValidateConnection(); err != nil {
		result.AddError(fmt.Sprintf("Vault connection failed: %v", err))
		result.Suggestion = "Check VAULT_ADDR and VAULT_TOKEN environment variables"
		return err
	}

	// Check vault seal status
	sealStatus, err := v.client.client.Sys().SealStatus()
	if err != nil {
		result.AddWarning(fmt.Sprintf("Could not check vault seal status: %v", err))
	} else if sealStatus.Sealed {
		result.AddError("Vault is sealed")
		result.Suggestion = "Unseal the vault before proceeding"
	}

	return nil
}

// validateInceptionVault checks inception vault exists and has required data
func (v *Validator) validateInceptionVault(inceptionPath string, result *ValidationResult) error {
	v.logger.Debug("Validating inception vault", "path", inceptionPath)

	// Check if inception vault exists
	exists, err := v.safe.Exists(inceptionPath)
	if err != nil {
		result.AddError(fmt.Sprintf("Failed to check inception vault existence: %v", err))
		return err
	}

	if !exists {
		result.AddError(fmt.Sprintf("Inception vault not found at path: %s", inceptionPath))
		result.Suggestion = "Run 'ocfp vault inception' to create inception vault"
		return nil
	}

	// Check for required inception secrets
	requiredSecrets := []string{
		"admin_password",
		"director_password",
		"postgres_password",
		"deployment_name",
	}

	for _, secretKey := range requiredSecrets {
		_, err := v.safe.Get(inceptionPath, secretKey)
		if err != nil {
			result.AddWarning(fmt.Sprintf("Required inception secret missing or inaccessible: %s", secretKey))
		}
	}

	// Count total secrets
	secrets, err := v.safe.GetAll(inceptionPath)
	if err != nil {
		result.AddWarning(fmt.Sprintf("Could not count inception secrets: %v", err))
	} else {
		secretCount := len(secrets)
		v.logger.Debug("Inception vault contains secrets", "count", secretCount)
		if secretCount == 0 {
			result.AddError("Inception vault exists but contains no secrets")
		} else if secretCount < 5 {
			result.AddWarning(fmt.Sprintf("Inception vault contains only %d secrets, expected more", secretCount))
		}
	}

	return nil
}

// validateTargetVault checks target vault accessibility
func (v *Validator) validateTargetVault(targetPath string, result *ValidationResult) error {
	v.logger.Debug("Validating target vault accessibility", "path", targetPath)

	// Test write access by attempting to write a test secret
	testPath := fmt.Sprintf("%s/migration-test", targetPath)
	testKey := "test"
	testValue := fmt.Sprintf("migration-test-%d", time.Now().Unix())

	if err := v.safe.Set(testPath, testKey, testValue); err != nil {
		result.AddError(fmt.Sprintf("Cannot write to target vault path %s: %v", targetPath, err))
		result.Suggestion = "Check vault permissions for the target path"
		return nil
	}

	// Verify we can read it back
	retrievedValue, err := v.safe.Get(testPath, testKey)
	if err != nil {
		result.AddError(fmt.Sprintf("Cannot read from target vault path %s: %v", targetPath, err))
		return nil
	}

	if retrievedValue != testValue {
		result.AddError("Read/write test failed - retrieved value doesn't match written value")
		return nil
	}

	// Clean up test secret
	if err := v.safe.Delete(testPath, testKey); err != nil {
		result.AddWarning(fmt.Sprintf("Could not clean up test secret at %s", testPath))
	}

	return nil
}

// checkActiveBOSHDeployments checks for active BOSH deployments that might be affected
func (v *Validator) checkActiveBOSHDeployments(result *ValidationResult) error {
	v.logger.Debug("Checking for active BOSH deployments")

	// This would typically query BOSH director for active deployments
	// For now, we'll check for BOSH-related secrets in vault as an indicator

	boshPaths := []string{
		"secret/config/" + v.config.Name + "/mgmt/bosh",
		"secret/config/" + v.config.Name + "/ocf/bosh",
	}

	for _, path := range boshPaths {
		exists, err := v.safe.Exists(path)
		if err != nil {
			continue // Skip if we can't check
		}

		if exists {
			result.AddWarning(fmt.Sprintf("Found BOSH configuration at %s - verify no active deployments", path))
		}
	}

	return nil
}

// checkSecretConflicts checks for conflicting secrets between inception and target
func (v *Validator) checkSecretConflicts(inceptionPath, targetPath string, result *ValidationResult) error {
	v.logger.Debug("Checking for secret conflicts", "inception", inceptionPath, "target", targetPath)

	// Get inception secrets
	inceptionSecrets, err := v.safe.GetAll(inceptionPath)
	if err != nil {
		return fmt.Errorf("failed to read inception secrets: %w", err)
	}

	// Check if target already has any of the same secret keys
	targetExists, err := v.safe.Exists(targetPath)
	if err != nil {
		return fmt.Errorf("failed to check target existence: %w", err)
	}

	if !targetExists {
		return nil // No conflicts if target doesn't exist
	}

	targetSecrets, err := v.safe.GetAll(targetPath)
	if err != nil {
		result.AddWarning(fmt.Sprintf("Could not read target secrets for conflict check: %v", err))
		return nil
	}

	// Check for overlapping keys
	var conflicts []string
	for key := range inceptionSecrets {
		if _, exists := targetSecrets[key]; exists {
			conflicts = append(conflicts, key)
		}
	}

	if len(conflicts) > 0 {
		result.AddWarning(fmt.Sprintf("Found %d conflicting secret keys: %s",
			len(conflicts), strings.Join(conflicts, ", ")))
		result.AddWarning("These secrets will be overwritten in the target vault")
	}

	return nil
}

// validateVaultPolicies validates vault policies for the current token
func (v *Validator) validateVaultPolicies(result *ValidationResult) error {
	v.logger.Debug("Validating vault policies")

	// Get token info to check policies
	secret, err := v.client.client.Auth().Token().LookupSelf()
	if err != nil {
		return fmt.Errorf("failed to lookup token: %w", err)
	}

	if secret == nil || secret.Data == nil {
		return fmt.Errorf("no token data returned")
	}

	// Check policies
	policies, ok := secret.Data["policies"]
	if !ok {
		result.AddWarning("No policies found for current token")
		return nil
	}

	policyList, ok := policies.([]interface{})
	if !ok {
		result.AddWarning("Could not parse token policies")
		return nil
	}

	// Check for required policies
	requiredPolicies := []string{"root", "admin", "migration"}
	hasRequired := false

	for _, policy := range policyList {
		if policyStr, ok := policy.(string); ok {
			for _, required := range requiredPolicies {
				if strings.Contains(policyStr, required) {
					hasRequired = true
					break
				}
			}
		}
	}

	if !hasRequired {
		result.AddWarning("Token may not have sufficient policies for migration operations")
		result.Suggestion = "Ensure token has admin or migration policies"
	}

	return nil
}

// ValidateVaultPath validates a vault path format and accessibility
func (v *Validator) ValidateVaultPath(path string) (*ValidationResult, error) {
	result := &ValidationResult{Valid: true}

	// Basic path validation
	if path == "" {
		result.AddError("Path cannot be empty")
		return result, nil
	}

	if strings.Contains(path, "//") {
		result.AddError("Path contains double slashes")
	}

	if strings.Contains(path, " ") {
		result.AddWarning("Path contains spaces")
	}

	// Check path accessibility
	exists, err := v.safe.Exists(path)
	if err != nil {
		result.AddError(fmt.Sprintf("Cannot access path %s: %v", path, err))
		return result, nil
	}

	if !exists {
		result.AddWarning(fmt.Sprintf("Path %s does not exist", path))
	}

	return result, nil
}

// CreateRollbackPoint creates a rollback point before dangerous operations
func (v *Validator) CreateRollbackPoint(paths []string) (*RollbackPoint, error) {
	v.logger.Info("Creating rollback point", "paths", len(paths))

	rollback := &RollbackPoint{
		Timestamp: time.Now(),
		Paths:     make(map[string]map[string]interface{}),
	}

	for _, path := range paths {
		secrets, err := v.safe.Export(path)
		if err != nil {
			v.logger.Warn("Failed to backup path for rollback", "path", path, "error", err)
			continue
		}

		rollback.Paths[path] = secrets
		v.logger.Debug("Backed up path for rollback", "path", path, "secrets", len(secrets))
	}

	v.logger.Info("Rollback point created", "backed_up_paths", len(rollback.Paths))
	return rollback, nil
}

// RollbackPoint represents a point-in-time backup for rollback
type RollbackPoint struct {
	Timestamp time.Time
	Paths     map[string]map[string]interface{}
}

// ExecuteRollback restores vault state from a rollback point
func (v *Validator) ExecuteRollback(rollback *RollbackPoint) error {
	v.logger.Info("Executing rollback", "timestamp", rollback.Timestamp, "paths", len(rollback.Paths))

	var errors []string

	for path, secrets := range rollback.Paths {
		v.logger.Debug("Restoring path from rollback", "path", path)

		if err := v.safe.Import(path, secrets); err != nil {
			errorMsg := fmt.Sprintf("Failed to restore path %s: %v", path, err)
			errors = append(errors, errorMsg)
			v.logger.Error(errorMsg)
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("rollback partially failed: %s", strings.Join(errors, "; "))
	}

	v.logger.Info("Rollback completed successfully")
	return nil
}
