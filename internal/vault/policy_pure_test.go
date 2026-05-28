package vault_test

import (
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestPolicyManager builds a PolicyManager with a nil vault client.
// Only safe for tests that call pure helpers (no pm.client dereference).
func newTestPolicyManager(blocName string) *vault.PolicyManager {
	return vault.NewPolicyManager(nil, config.NewTestConfig().Build(), blocName)
}

// TestGetPolicyName verifies the naming convention ocfp-<bloc>-<type>.
func TestGetPolicyName(t *testing.T) {
	t.Parallel()

	pm := newTestPolicyManager("mybloc")

	tests := []struct {
		policyType string
		want       string
	}{
		{"admin", "ocfp-mybloc-admin"},
		{"bosh", "ocfp-mybloc-bosh"},
		{"cf", "ocfp-mybloc-cf"},
		{"migration", "ocfp-mybloc-migration"},
		{"readonly", "ocfp-mybloc-readonly"},
		{"custom", "ocfp-mybloc-custom"},
	}

	for _, tt := range tests {
		t.Run(tt.policyType, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, pm.GetPolicyNameExport(tt.policyType))
		})
	}
}

// TestGetOCFPPolicyTemplatesCount asserts exactly PolicyTemplateCapacity templates are returned.
func TestGetOCFPPolicyTemplatesCount(t *testing.T) {
	t.Parallel()

	pm := newTestPolicyManager("testbloc")
	templates := pm.GetOCFPPolicyTemplatesExport()

	assert.Len(t, templates, vault.PolicyTemplateCapacity)
}

// TestGetOCFPPolicyTemplatesNames asserts all expected policy names are present.
func TestGetOCFPPolicyTemplatesNames(t *testing.T) {
	t.Parallel()

	pm := newTestPolicyManager("testbloc")
	templates := pm.GetOCFPPolicyTemplatesExport()

	names := make([]string, len(templates))
	for i, tmpl := range templates {
		names[i] = tmpl.Name
	}

	assert.Contains(t, names, "ocfp-testbloc-admin")
	assert.Contains(t, names, "ocfp-testbloc-bosh")
	assert.Contains(t, names, "ocfp-testbloc-cf")
	assert.Contains(t, names, "ocfp-testbloc-migration")
	assert.Contains(t, names, "ocfp-testbloc-readonly")
}

// TestCreateAdminPolicy asserts non-empty name, description, and at least one rule.
func TestCreateAdminPolicy(t *testing.T) {
	t.Parallel()

	pm := newTestPolicyManager("mybloc")
	tmpl := pm.CreateAdminPolicyExport()

	require.NotNil(t, tmpl)
	assert.Equal(t, "ocfp-mybloc-admin", tmpl.Name)
	assert.NotEmpty(t, tmpl.Description)
	assert.NotEmpty(t, tmpl.Rules, "admin policy must have at least one rule")

	for _, rule := range tmpl.Rules {
		assert.NotEmpty(t, rule.Path, "each rule must have a path")
		assert.NotEmpty(t, rule.Capabilities, "each rule must have capabilities")
	}
}

// TestCreateBOSHPolicy asserts BOSH policy has expected shape.
func TestCreateBOSHPolicy(t *testing.T) {
	t.Parallel()

	pm := newTestPolicyManager("mybloc")
	tmpl := pm.CreateBOSHPolicyExport()

	require.NotNil(t, tmpl)
	assert.Equal(t, "ocfp-mybloc-bosh", tmpl.Name)
	assert.NotEmpty(t, tmpl.Description)
	assert.NotEmpty(t, tmpl.Rules)

	// BOSH policy should be read-only
	for _, rule := range tmpl.Rules {
		assert.Equal(t, []string{"read"}, rule.Capabilities,
			"BOSH policy path %q should be read-only", rule.Path)
	}
}

// TestCreateCloudFoundryPolicy asserts CF policy shape.
func TestCreateCloudFoundryPolicy(t *testing.T) {
	t.Parallel()

	pm := newTestPolicyManager("mybloc")
	tmpl := pm.CreateCloudFoundryPolicyExport()

	require.NotNil(t, tmpl)
	assert.Equal(t, "ocfp-mybloc-cf", tmpl.Name)
	assert.NotEmpty(t, tmpl.Description)
	assert.NotEmpty(t, tmpl.Rules)
}

// TestCreateMigrationPolicy asserts migration policy has delete capability.
func TestCreateMigrationPolicy(t *testing.T) {
	t.Parallel()

	pm := newTestPolicyManager("mybloc")
	tmpl := pm.CreateMigrationPolicyExport()

	require.NotNil(t, tmpl)
	assert.Equal(t, "ocfp-mybloc-migration", tmpl.Name)
	assert.NotEmpty(t, tmpl.Description)
	assert.NotEmpty(t, tmpl.Rules)

	// At least one rule must include delete (migration removes inception secrets)
	hasDelete := false
	for _, rule := range tmpl.Rules {
		for _, cap := range rule.Capabilities {
			if cap == "delete" {
				hasDelete = true
			}
		}
	}
	assert.True(t, hasDelete, "migration policy must include delete capability")
}

// TestCreateOperatorPolicy asserts read-only operator policy shape.
func TestCreateOperatorPolicy(t *testing.T) {
	t.Parallel()

	pm := newTestPolicyManager("mybloc")
	tmpl := pm.CreateOperatorPolicyExport()

	require.NotNil(t, tmpl)
	assert.Equal(t, "ocfp-mybloc-readonly", tmpl.Name)
	assert.NotEmpty(t, tmpl.Description)
	assert.NotEmpty(t, tmpl.Rules)
}

// TestGeneratePolicyHCL asserts key structural clauses appear in HCL output.
func TestGeneratePolicyHCL(t *testing.T) {
	t.Parallel()

	pm := newTestPolicyManager("mybloc")
	tmpl := &vault.PolicyTemplate{
		Name:        "ocfp-mybloc-test",
		Description: "Test policy for HCL generation",
		Rules: []vault.PolicyRule{
			{
				Path:         "secret/config/mybloc/*",
				Capabilities: []string{"read", "list"},
				Description:  "Read bloc config",
			},
			{
				Path:         "sys/health",
				Capabilities: []string{"read"},
			},
		},
	}

	hcl := pm.GeneratePolicyHCLExport(tmpl)

	assert.Contains(t, hcl, `path "secret/config/mybloc/*"`)
	assert.Contains(t, hcl, `path "sys/health"`)
	assert.Contains(t, hcl, `capabilities = ["read", "list"]`)
	assert.Contains(t, hcl, `capabilities = ["read"]`)
	assert.Contains(t, hcl, "# Test policy for HCL generation")
	assert.Contains(t, hcl, "# Generated for OCFP bloc: mybloc")
	assert.Contains(t, hcl, "# Read bloc config")
	// Rule with no description should not emit a stray comment line
	lines := strings.Split(hcl, "\n")
	healthRuleIdx := -1
	for i, l := range lines {
		if strings.Contains(l, `path "sys/health"`) {
			healthRuleIdx = i
			break
		}
	}
	require.Greater(t, healthRuleIdx, 0)
	prevLine := strings.TrimSpace(lines[healthRuleIdx-1])
	assert.False(t, strings.HasPrefix(prevLine, "#"),
		"rule with empty description should not emit a comment: got %q", prevLine)
}

// TestHasRootPolicy verifies root detection and absence.
func TestHasRootPolicy(t *testing.T) {
	t.Parallel()

	pm := newTestPolicyManager("mybloc")

	assert.True(t, pm.HasRootPolicyExport([]string{"default", "root", "custom"}))
	assert.True(t, pm.HasRootPolicyExport([]string{"root"}))
	assert.False(t, pm.HasRootPolicyExport([]string{"admin", "readonly"}))
	assert.False(t, pm.HasRootPolicyExport([]string{}))
	assert.False(t, pm.HasRootPolicyExport(nil))
}

// TestFindMissingPolicies covers all branches: empty inputs, full match, partial miss.
func TestFindMissingPolicies(t *testing.T) {
	t.Parallel()

	pm := newTestPolicyManager("mybloc")

	tests := []struct {
		name     string
		required []string
		have     []string
		want     []string
	}{
		{
			name:     "all present",
			required: []string{"admin", "bosh"},
			have:     []string{"admin", "bosh", "extra"},
			want:     []string{},
		},
		{
			name:     "all missing",
			required: []string{"admin", "bosh"},
			have:     []string{},
			want:     []string{"admin", "bosh"},
		},
		{
			name:     "partial miss",
			required: []string{"admin", "bosh", "cf"},
			have:     []string{"admin"},
			want:     []string{"bosh", "cf"},
		},
		{
			name:     "empty required",
			required: []string{},
			have:     []string{"admin"},
			want:     []string{},
		},
		{
			name:     "nil inputs",
			required: nil,
			have:     nil,
			want:     []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := pm.FindMissingPoliciesExport(tt.required, tt.have)
			if len(tt.want) == 0 {
				assert.Empty(t, got)
			} else {
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

// TestValidateRequiredPolicies covers pass and fail paths.
func TestValidateRequiredPolicies(t *testing.T) {
	t.Parallel()

	pm := newTestPolicyManager("mybloc")

	t.Run("all policies present returns nil", func(t *testing.T) {
		t.Parallel()

		err := pm.ValidateRequiredPoliciesExport(
			[]string{"admin", "bosh"},
			[]string{"admin", "bosh", "extra"},
		)
		assert.NoError(t, err)
	})

	t.Run("missing policies returns error", func(t *testing.T) {
		t.Parallel()

		err := pm.ValidateRequiredPoliciesExport(
			[]string{"admin", "bosh", "cf"},
			[]string{"admin"},
		)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "bosh")
		assert.Contains(t, err.Error(), "cf")
	})

	t.Run("empty required never fails", func(t *testing.T) {
		t.Parallel()

		err := pm.ValidateRequiredPoliciesExport([]string{}, []string{})
		assert.NoError(t, err)
	})
}

// TestConvertPoliciesListToStrings covers type coercion and non-string dropping.
func TestConvertPoliciesListToStrings(t *testing.T) {
	t.Parallel()

	pm := newTestPolicyManager("mybloc")

	t.Run("all strings", func(t *testing.T) {
		t.Parallel()

		input := []interface{}{"admin", "bosh", "cf"}
		got := pm.ConvertPoliciesListToStringsExport(input)
		assert.Equal(t, []string{"admin", "bosh", "cf"}, got)
	})

	t.Run("non-string entries dropped", func(t *testing.T) {
		t.Parallel()

		input := []interface{}{"admin", 42, nil, "bosh"}
		got := pm.ConvertPoliciesListToStringsExport(input)
		assert.Equal(t, []string{"admin", "bosh"}, got)
	})

	t.Run("empty input", func(t *testing.T) {
		t.Parallel()

		got := pm.ConvertPoliciesListToStringsExport([]interface{}{})
		assert.Empty(t, got)
	})

	t.Run("nil input", func(t *testing.T) {
		t.Parallel()

		got := pm.ConvertPoliciesListToStringsExport(nil)
		assert.Empty(t, got)
	})
}

// TestPolicyTemplatesBlocNamePropagation ensures bloc name appears in all policy paths.
func TestPolicyTemplatesBlocNamePropagation(t *testing.T) {
	t.Parallel()

	const bloc = "prod-east"
	pm := newTestPolicyManager(bloc)
	templates := pm.GetOCFPPolicyTemplatesExport()

	for _, tmpl := range templates {
		assert.Contains(t, tmpl.Name, bloc,
			"policy name %q should contain bloc name", tmpl.Name)
		for _, rule := range tmpl.Rules {
			if strings.HasPrefix(rule.Path, "secret/") {
				assert.Contains(t, rule.Path, bloc,
					"policy %q rule path %q should reference bloc", tmpl.Name, rule.Path)
			}
		}
	}
}
