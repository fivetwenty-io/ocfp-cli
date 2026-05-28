package vault

import "time"

// SetSleepFn replaces the package-level sleepFn used by retry logic.
// Returns a restore closure; call it in a defer to reset after the test.
func SetSleepFn(fn func(time.Duration)) func() {
	orig := sleepFn
	sleepFn = fn
	return func() { sleepFn = orig }
}

// PolicyManager export shims — expose private helpers for pure-logic tests
// without modifying production code.

// GetPolicyNameExport exposes getPolicyName for testing.
func (pm *PolicyManager) GetPolicyNameExport(policyType string) string {
	return pm.getPolicyName(policyType)
}

// GetOCFPPolicyTemplatesExport exposes getOCFPPolicyTemplates for testing.
func (pm *PolicyManager) GetOCFPPolicyTemplatesExport() []*PolicyTemplate {
	return pm.getOCFPPolicyTemplates()
}

// GeneratePolicyHCLExport exposes generatePolicyHCL for testing.
func (pm *PolicyManager) GeneratePolicyHCLExport(template *PolicyTemplate) string {
	return pm.generatePolicyHCL(template)
}

// HasRootPolicyExport exposes hasRootPolicy for testing.
func (pm *PolicyManager) HasRootPolicyExport(policies []string) bool {
	return pm.hasRootPolicy(policies)
}

// FindMissingPoliciesExport exposes findMissingPolicies for testing.
func (pm *PolicyManager) FindMissingPoliciesExport(required, have []string) []string {
	return pm.findMissingPolicies(required, have)
}

// ValidateRequiredPoliciesExport exposes validateRequiredPolicies for testing.
func (pm *PolicyManager) ValidateRequiredPoliciesExport(required, have []string) error {
	return pm.validateRequiredPolicies(required, have)
}

// ConvertPoliciesListToStringsExport exposes convertPoliciesListToStrings for testing.
func (pm *PolicyManager) ConvertPoliciesListToStringsExport(list []interface{}) []string {
	return pm.convertPoliciesListToStrings(list)
}

// CreateAdminPolicyExport exposes createAdminPolicy for testing.
func (pm *PolicyManager) CreateAdminPolicyExport() *PolicyTemplate {
	return pm.createAdminPolicy()
}

// CreateBOSHPolicyExport exposes createBOSHPolicy for testing.
func (pm *PolicyManager) CreateBOSHPolicyExport() *PolicyTemplate {
	return pm.createBOSHPolicy()
}

// CreateCloudFoundryPolicyExport exposes createCloudFoundryPolicy for testing.
func (pm *PolicyManager) CreateCloudFoundryPolicyExport() *PolicyTemplate {
	return pm.createCloudFoundryPolicy()
}

// CreateMigrationPolicyExport exposes createMigrationPolicy for testing.
func (pm *PolicyManager) CreateMigrationPolicyExport() *PolicyTemplate {
	return pm.createMigrationPolicy()
}

// CreateOperatorPolicyExport exposes createOperatorPolicy for testing.
func (pm *PolicyManager) CreateOperatorPolicyExport() *PolicyTemplate {
	return pm.createOperatorPolicy()
}
