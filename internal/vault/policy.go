package vault

import (
	"fmt"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"go.uber.org/zap"
)

const (
	// Policy template capacity.
	PolicyTemplateCapacity = 5
)

// PolicyManager handles vault policy creation and management.
type PolicyManager struct {
	client   *Client
	config   *config.Config
	blocName string
	logger   *zap.SugaredLogger
}

// NewPolicyManager creates a new policy manager.
func NewPolicyManager(client *Client, cfg *config.Config, blocName string) *PolicyManager {
	return &PolicyManager{
		client:   client,
		config:   cfg,
		blocName: blocName,
		logger:   logger.Get(),
	}
}

// PolicyTemplate represents a vault policy template.
type PolicyTemplate struct {
	Name        string
	Description string
	Rules       []PolicyRule
}

// PolicyRule represents a single policy rule.
type PolicyRule struct {
	Path         string
	Capabilities []string
	Description  string
}

// CreateOCFPPolicies creates all necessary policies for OCFP operations.
func (pm *PolicyManager) CreateOCFPPolicies() error {
	pm.logger.Infow("Creating OCFP vault policies", "bloc", pm.blocName)

	policies := pm.getOCFPPolicyTemplates()

	for _, policy := range policies {
		err := pm.CreatePolicy(policy)
		if err != nil {
			return fmt.Errorf("failed to create policy %s: %w", policy.Name, err)
		}
	}

	pm.logger.Infow("OCFP policies created successfully", "count", len(policies))

	return nil
}

// CreatePolicy creates a vault policy.
func (pm *PolicyManager) CreatePolicy(template *PolicyTemplate) error {
	pm.logger.Debugw("Creating vault policy", "name", template.Name)

	// Generate policy HCL
	policyHCL := pm.generatePolicyHCL(template)

	// Create the policy
	err := pm.client.client.Sys().PutPolicy(template.Name, policyHCL)
	if err != nil {
		return fmt.Errorf("failed to create policy: %w", err)
	}

	pm.logger.Infow("Created vault policy", "name", template.Name, "rules", len(template.Rules))

	return nil
}

// ListPolicies lists all vault policies.
func (pm *PolicyManager) ListPolicies() ([]string, error) {
	policies, err := pm.client.client.Sys().ListPolicies()
	if err != nil {
		return nil, fmt.Errorf("failed to list policies: %w", err)
	}

	return policies, nil
}

// GetPolicy retrieves a specific policy.
func (pm *PolicyManager) GetPolicy(name string) (string, error) {
	policy, err := pm.client.client.Sys().GetPolicy(name)
	if err != nil {
		return "", fmt.Errorf("failed to get policy %s: %w", name, err)
	}

	return policy, nil
}

// DeletePolicy deletes a vault policy.
func (pm *PolicyManager) DeletePolicy(name string) error {
	pm.logger.Infow("Deleting vault policy", "name", name)

	err := pm.client.client.Sys().DeletePolicy(name)
	if err != nil {
		return fmt.Errorf("failed to delete policy %s: %w", name, err)
	}

	pm.logger.Infow("Deleted vault policy", "name", name)

	return nil
}

// UpdatePolicy updates an existing vault policy.
func (pm *PolicyManager) UpdatePolicy(template *PolicyTemplate) error {
	pm.logger.Debugw("Updating vault policy", "name", template.Name)

	// Check if policy exists
	existing, err := pm.GetPolicy(template.Name)
	if err != nil {
		if strings.Contains(err.Error(), "no such policy") {
			// Policy doesn't exist, create it
			return pm.CreatePolicy(template)
		}

		return fmt.Errorf("failed to check existing policy: %w", err)
	}

	// Generate new policy content
	newPolicyHCL := pm.generatePolicyHCL(template)

	// Only update if content has changed
	if strings.TrimSpace(existing) == strings.TrimSpace(newPolicyHCL) {
		pm.logger.Debugw("Policy content unchanged, skipping update", "name", template.Name)

		return nil
	}

	// Update the policy
	err = pm.client.client.Sys().PutPolicy(template.Name, newPolicyHCL)
	if err != nil {
		return fmt.Errorf("failed to update policy: %w", err)
	}

	pm.logger.Infow("Updated vault policy", "name", template.Name)

	return nil
}

// ValidateTokenPolicies validates that a token has the required policies.
func (pm *PolicyManager) ValidateTokenPolicies(requiredPolicies []string) error {
	pm.logger.Debugw("Validating token policies", "required", requiredPolicies)

	tokenPolicies, err := pm.getTokenPolicies()
	if err != nil {
		return err
	}

	pm.logger.Debugw("Token policies", "policies", tokenPolicies)

	if pm.hasRootPolicy(tokenPolicies) {
		pm.logger.Debug("Token has root policy, validation passed")

		return nil
	}

	return pm.validateRequiredPolicies(requiredPolicies, tokenPolicies)
}

// CreateTokenWithPolicies creates a new token with specific policies.
func (pm *PolicyManager) CreateTokenWithPolicies(policies []string, ttl string, renewable bool) (string, error) {
	pm.logger.Infow("Creating token with policies", "policies", policies, "ttl", ttl)

	// Prepare token creation request
	tokenReq := map[string]interface{}{
		"policies":  policies,
		"renewable": renewable,
	}

	if ttl != "" {
		tokenReq["ttl"] = ttl
	}

	// Create token using the logical interface
	secret, err := pm.client.client.Logical().Write("auth/token/create", tokenReq)
	if err != nil {
		return "", fmt.Errorf("failed to create token: %w", err)
	}

	if secret == nil || secret.Auth == nil {
		return "", ErrNoAuthInformationInTokenResponse
	}

	token := secret.Auth.ClientToken

	pm.logger.Infow("Created token successfully", "policies", len(policies))

	return token, nil
}

// EnsureOCFPPoliciesExist ensures all required OCFP policies exist and are current.
func (pm *PolicyManager) EnsureOCFPPoliciesExist() error {
	pm.logger.Info("Ensuring OCFP policies exist and are current")

	policies := pm.getOCFPPolicyTemplates()

	for _, policy := range policies {
		err := pm.UpdatePolicy(policy)
		if err != nil {
			return fmt.Errorf("failed to ensure policy %s: %w", policy.Name, err)
		}
	}

	pm.logger.Infow("OCFP policies are current", "count", len(policies))

	return nil
}

// CleanupOCFPPolicies removes all OCFP policies for this bloc.
func (pm *PolicyManager) CleanupOCFPPolicies() error {
	pm.logger.Infow("Cleaning up OCFP policies", "bloc", pm.blocName)

	// List all policies
	allPolicies, err := pm.ListPolicies()
	if err != nil {
		return fmt.Errorf("failed to list policies: %w", err)
	}

	// Find OCFP policies for this bloc
	ocfpPolicyPrefix := fmt.Sprintf("ocfp-%s-", pm.blocName)

	ocfpPolicies := make([]string, 0, len(allPolicies))

	for _, policy := range allPolicies {
		if strings.HasPrefix(policy, ocfpPolicyPrefix) {
			ocfpPolicies = append(ocfpPolicies, policy)
		}
	}

	// Delete OCFP policies
	for _, policy := range ocfpPolicies {
		err := pm.DeletePolicy(policy)
		if err != nil {
			pm.logger.Warnw("Failed to delete policy", "policy", policy, "error", err)
		}
	}

	pm.logger.Infow("OCFP policy cleanup completed", "deleted", len(ocfpPolicies))

	return nil
}

func (pm *PolicyManager) getTokenPolicies() ([]string, error) {
	secret, err := pm.client.client.Auth().Token().LookupSelf()
	if err != nil {
		return nil, fmt.Errorf("failed to lookup token: %w", err)
	}

	if secret == nil || secret.Data == nil {
		return nil, ErrNoTokenDataReturned
	}

	tokenPoliciesRaw, tokenPoliciesOK := secret.Data["policies"]
	if !tokenPoliciesOK {
		return nil, ErrNoPoliciesFoundInToken
	}

	tokenPoliciesList, ok := tokenPoliciesRaw.([]interface{})
	if !ok {
		return nil, ErrInvalidPoliciesFormatInToken
	}

	return pm.convertPoliciesListToStrings(tokenPoliciesList), nil
}

func (pm *PolicyManager) convertPoliciesListToStrings(tokenPoliciesList []interface{}) []string {
	tokenPolicies := make([]string, 0, len(tokenPoliciesList))
	for _, policy := range tokenPoliciesList {
		if policyStr, ok := policy.(string); ok {
			tokenPolicies = append(tokenPolicies, policyStr)
		}
	}

	return tokenPolicies
}

func (pm *PolicyManager) hasRootPolicy(tokenPolicies []string) bool {
	for _, policy := range tokenPolicies {
		if policy == "root" {
			return true
		}
	}

	return false
}

func (pm *PolicyManager) validateRequiredPolicies(requiredPolicies, tokenPolicies []string) error {
	missing := pm.findMissingPolicies(requiredPolicies, tokenPolicies)
	if len(missing) > 0 {
		return ErrTokenMissingRequiredPolicies(missing)
	}

	pm.logger.Debug("Token policy validation passed")

	return nil
}

func (pm *PolicyManager) findMissingPolicies(requiredPolicies, tokenPolicies []string) []string {
	missing := make([]string, 0, len(requiredPolicies))
	for _, required := range requiredPolicies {
		if !pm.policyExists(required, tokenPolicies) {
			missing = append(missing, required)
		}
	}

	return missing
}

func (pm *PolicyManager) policyExists(required string, tokenPolicies []string) bool {
	for _, tokenPolicy := range tokenPolicies {
		if tokenPolicy == required {
			return true
		}
	}

	return false
}

// getOCFPPolicyTemplates returns all policy templates for OCFP.
func (pm *PolicyManager) getOCFPPolicyTemplates() []*PolicyTemplate {
	// Currently 5 policy templates appended below
	policies := make([]*PolicyTemplate, 0, PolicyTemplateCapacity)

	policies = append(policies, pm.createAdminPolicy())
	policies = append(policies, pm.createBOSHPolicy())
	policies = append(policies, pm.createCloudFoundryPolicy())
	policies = append(policies, pm.createMigrationPolicy())
	policies = append(policies, pm.createOperatorPolicy())

	return policies
}

// createAdminPolicy creates the admin policy template.
func (pm *PolicyManager) createAdminPolicy() *PolicyTemplate {
	return &PolicyTemplate{
		Name:        pm.getPolicyName("admin"),
		Description: "Full administrative access for OCFP bloc management",
		Rules: []PolicyRule{
			{
				Path:         fmt.Sprintf("secret/config/%s/*", pm.blocName),
				Capabilities: []string{"create", "read", "update", "delete", "list"},
				Description:  "Full access to bloc configuration",
			},
			{
				Path:         fmt.Sprintf("secret/%s-inception/*", pm.blocName),
				Capabilities: []string{"create", "read", "update", "delete", "list"},
				Description:  "Full access to inception vault",
			},
			{
				Path:         "sys/mounts",
				Capabilities: []string{"read", "list"},
				Description:  "Read mount information for engine detection",
			},
			{
				Path:         "sys/policies/acl/*",
				Capabilities: []string{"create", "read", "update", "delete", "list"},
				Description:  "Manage vault policies",
			},
			{
				Path:         "auth/token/*",
				Capabilities: []string{"create", "read", "update", "delete", "list"},
				Description:  "Manage tokens",
			},
		},
	}
}

// createBOSHPolicy creates the BOSH policy template.
func (pm *PolicyManager) createBOSHPolicy() *PolicyTemplate {
	return &PolicyTemplate{
		Name:        pm.getPolicyName("bosh"),
		Description: "BOSH director access to OCFP secrets",
		Rules: []PolicyRule{
			{
				Path:         fmt.Sprintf("secret/config/%s/mgmt/bosh/*", pm.blocName),
				Capabilities: []string{"read"},
				Description:  "Read BOSH configuration",
			},
			{
				Path:         fmt.Sprintf("secret/config/%s/mgmt/vpc/*", pm.blocName),
				Capabilities: []string{"read"},
				Description:  "Read VPC configuration",
			},
			{
				Path:         fmt.Sprintf("secret/config/%s/mgmt/fqdns/*", pm.blocName),
				Capabilities: []string{"read"},
				Description:  "Read FQDN configuration",
			},
		},
	}
}

// createCloudFoundryPolicy creates the Cloud Foundry policy template.
func (pm *PolicyManager) createCloudFoundryPolicy() *PolicyTemplate {
	return &PolicyTemplate{
		Name:        pm.getPolicyName("cf"),
		Description: "Cloud Foundry access to OCFP secrets",
		Rules: []PolicyRule{
			{
				Path:         fmt.Sprintf("secret/config/%s/ocf/*", pm.blocName),
				Capabilities: []string{"read"},
				Description:  "Read OCF configuration",
			},
			{
				Path:         fmt.Sprintf("secret/config/%s/ocf/public-ips/*", pm.blocName),
				Capabilities: []string{"read"},
				Description:  "Read public IP configuration",
			},
			{
				Path:         fmt.Sprintf("secret/config/%s/ocf/blobstores/*", pm.blocName),
				Capabilities: []string{"read"},
				Description:  "Read blobstore configuration",
			},
			{
				Path:         fmt.Sprintf("secret/config/%s/ocf/dbs/*", pm.blocName),
				Capabilities: []string{"read"},
				Description:  "Read database configuration",
			},
		},
	}
}

// createMigrationPolicy creates the migration policy template.
func (pm *PolicyManager) createMigrationPolicy() *PolicyTemplate {
	return &PolicyTemplate{
		Name:        pm.getPolicyName("migration"),
		Description: "Policy for vault migration operations",
		Rules: []PolicyRule{
			{
				Path:         fmt.Sprintf("secret/%s-inception/*", pm.blocName),
				Capabilities: []string{"read", "delete", "list"},
				Description:  "Read and delete inception secrets for migration",
			},
			{
				Path:         fmt.Sprintf("secret/config/%s/*", pm.blocName),
				Capabilities: []string{"create", "read", "update", "list"},
				Description:  "Write production secrets during migration",
			},
			{
				Path:         "sys/mounts",
				Capabilities: []string{"read"},
				Description:  "Read mount information",
			},
		},
	}
}

// createOperatorPolicy creates the operator (readonly) policy template.
func (pm *PolicyManager) createOperatorPolicy() *PolicyTemplate {
	return &PolicyTemplate{
		Name:        pm.getPolicyName("readonly"),
		Description: "Read-only access for monitoring and validation",
		Rules: []PolicyRule{
			{
				Path:         fmt.Sprintf("secret/config/%s/*", pm.blocName),
				Capabilities: []string{"read", "list"},
				Description:  "Read bloc configuration",
			},
			{
				Path:         "sys/health",
				Capabilities: []string{"read"},
				Description:  "Read vault health status",
			},
			{
				Path:         "sys/seal-status",
				Capabilities: []string{"read"},
				Description:  "Read seal status",
			},
		},
	}
}

// getPolicyName generates a policy name with bloc prefix.
func (pm *PolicyManager) getPolicyName(policyType string) string {
	return fmt.Sprintf("ocfp-%s-%s", pm.blocName, policyType)
}

// generatePolicyHCL generates HCL policy content from template.
func (pm *PolicyManager) generatePolicyHCL(template *PolicyTemplate) string {
	var builder strings.Builder

	// Add policy header comment
	builder.WriteString(fmt.Sprintf("# %s\n", template.Description))
	builder.WriteString(fmt.Sprintf("# Generated for OCFP bloc: %s\n", pm.blocName))
	builder.WriteString(fmt.Sprintf("# Created: %s\n\n", time.Now().Format(time.RFC3339)))

	// Generate rules
	for _, rule := range template.Rules {
		if rule.Description != "" {
			builder.WriteString(fmt.Sprintf("# %s\n", rule.Description))
		}

		builder.WriteString(fmt.Sprintf("path \"%s\" {\n", rule.Path))

		if len(rule.Capabilities) > 0 {
			caps := make([]string, len(rule.Capabilities))
			for i, cap := range rule.Capabilities {
				caps[i] = fmt.Sprintf("\"%s\"", cap)
			}

			builder.WriteString(fmt.Sprintf("  capabilities = [%s]\n", strings.Join(caps, ", ")))
		}

		builder.WriteString("}\n\n")
	}

	return builder.String()
}
