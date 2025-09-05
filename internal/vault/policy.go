package vault

import (
	"fmt"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"go.uber.org/zap"
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
	pm.logger.Info("Creating OCFP vault policies", "bloc", pm.blocName)

	policies := pm.getOCFPPolicyTemplates()

	for _, policy := range policies {
		err := pm.CreatePolicy(policy)
		if err != nil {
			return fmt.Errorf("failed to create policy %s: %w", policy.Name, err)
		}
	}

	pm.logger.Info("OCFP policies created successfully", "count", len(policies))

	return nil
}

// getOCFPPolicyTemplates returns all policy templates for OCFP.
func (pm *PolicyManager) getOCFPPolicyTemplates() []*PolicyTemplate {
	var policies []*PolicyTemplate

	// Admin policy for full OCFP management
	policies = append(policies, &PolicyTemplate{
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
	})

	// BOSH policy for BOSH director access
	policies = append(policies, &PolicyTemplate{
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
	})

	// Cloud Foundry policy
	policies = append(policies, &PolicyTemplate{
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
	})

	// Migration policy for vault migration operations
	policies = append(policies, &PolicyTemplate{
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
	})

	// Read-only policy for monitoring and validation
	policies = append(policies, &PolicyTemplate{
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
	})

	return policies
}

// getPolicyName generates a policy name with bloc prefix.
func (pm *PolicyManager) getPolicyName(policyType string) string {
	return fmt.Sprintf("ocfp-%s-%s", pm.blocName, policyType)
}

// CreatePolicy creates a vault policy.
func (pm *PolicyManager) CreatePolicy(template *PolicyTemplate) error {
	pm.logger.Debug("Creating vault policy", "name", template.Name)

	// Generate policy HCL
	policyHCL := pm.generatePolicyHCL(template)

	// Create the policy
	err := pm.client.client.Sys().PutPolicy(template.Name, policyHCL)
	if err != nil {
		return fmt.Errorf("failed to create policy: %w", err)
	}

	pm.logger.Info("Created vault policy", "name", template.Name, "rules", len(template.Rules))

	return nil
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
	pm.logger.Info("Deleting vault policy", "name", name)

	err := pm.client.client.Sys().DeletePolicy(name)
	if err != nil {
		return fmt.Errorf("failed to delete policy %s: %w", name, err)
	}

	pm.logger.Info("Deleted vault policy", "name", name)

	return nil
}

// UpdatePolicy updates an existing vault policy.
func (pm *PolicyManager) UpdatePolicy(template *PolicyTemplate) error {
	pm.logger.Debug("Updating vault policy", "name", template.Name)

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
		pm.logger.Debug("Policy content unchanged, skipping update", "name", template.Name)

		return nil
	}

	// Update the policy
	if err := pm.client.client.Sys().PutPolicy(template.Name, newPolicyHCL); err != nil {
		return fmt.Errorf("failed to update policy: %w", err)
	}

	pm.logger.Info("Updated vault policy", "name", template.Name)

	return nil
}

// ValidateTokenPolicies validates that a token has the required policies.
func (pm *PolicyManager) ValidateTokenPolicies(requiredPolicies []string) error {
	pm.logger.Debug("Validating token policies", "required", requiredPolicies)

	// Get current token info
	secret, err := pm.client.client.Auth().Token().LookupSelf()
	if err != nil {
		return fmt.Errorf("failed to lookup token: %w", err)
	}

	if secret == nil || secret.Data == nil {
		return errors.New("no token data returned")
	}

	// Extract token policies
	tokenPoliciesRaw, tokenPoliciesOK := secret.Data["policies"]
	if !tokenPoliciesOK {
		return errors.New("no policies found in token")
	}

	tokenPoliciesList, ok := tokenPoliciesRaw.([]interface{})
	if !ok {
		return errors.New("invalid policies format in token")
	}

	// Convert to string slice
	var tokenPolicies []string
	for _, policy := range tokenPoliciesList {
		if policyStr, ok := policy.(string); ok {
			tokenPolicies = append(tokenPolicies, policyStr)
		}
	}

	pm.logger.Debug("Token policies", "policies", tokenPolicies)

	// Check if token has root policy (grants everything)
	for _, policy := range tokenPolicies {
		if policy == "root" {
			pm.logger.Debug("Token has root policy, validation passed")

			return nil
		}
	}

	// Check for required policies
	var missing []string

	for _, required := range requiredPolicies {
		found := false

		for _, tokenPolicy := range tokenPolicies {
			if tokenPolicy == required {
				found = true

				break
			}
		}

		if !found {
			missing = append(missing, required)
		}
	}

	if len(missing) > 0 {
		return fmt.Errorf("token missing required policies: %s", strings.Join(missing, ", "))
	}

	pm.logger.Debug("Token policy validation passed")

	return nil
}

// CreateTokenWithPolicies creates a new token with specific policies.
func (pm *PolicyManager) CreateTokenWithPolicies(policies []string, ttl string, renewable bool) (string, error) {
	pm.logger.Info("Creating token with policies", "policies", policies, "ttl", ttl)

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
		return "", errors.New("no auth information in token response")
	}

	token := secret.Auth.ClientToken

	pm.logger.Info("Created token successfully", "policies", len(policies))

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

	pm.logger.Info("OCFP policies are current", "count", len(policies))

	return nil
}

// CleanupOCFPPolicies removes all OCFP policies for this bloc.
func (pm *PolicyManager) CleanupOCFPPolicies() error {
	pm.logger.Info("Cleaning up OCFP policies", "bloc", pm.blocName)

	// List all policies
	allPolicies, err := pm.ListPolicies()
	if err != nil {
		return fmt.Errorf("failed to list policies: %w", err)
	}

	// Find OCFP policies for this bloc
	ocfpPolicyPrefix := fmt.Sprintf("ocfp-%s-", pm.blocName)

	var ocfpPolicies []string

	for _, policy := range allPolicies {
		if strings.HasPrefix(policy, ocfpPolicyPrefix) {
			ocfpPolicies = append(ocfpPolicies, policy)
		}
	}

	// Delete OCFP policies
	for _, policy := range ocfpPolicies {
		err := pm.DeletePolicy(policy)
		if err != nil {
			pm.logger.Warn("Failed to delete policy", "policy", policy, "error", err)
		}
	}

	pm.logger.Info("OCFP policy cleanup completed", "deleted", len(ocfpPolicies))

	return nil
}
