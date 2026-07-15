package config

import (
	"fmt"
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/ocfp/ocfp-cli-go/internal/version"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// minimalValidConfig returns a Config that passes every existing validate()
// check (non-PVE provider, no blobstore/tailscale/cloudflare/artifacts
// config set) so the config-schema guard can be tested in isolation without
// tripping unrelated validation errors.
func minimalValidConfig() *Config {
	return &Config{
		Name:     "test-schema",
		Provider: "aws",
	}
}

// TestValidate_ConfigSchema is a table-driven test for the config_schema
// forward-compatibility guard added to validate(). It exercises the full
// range of inputs the field can take: absent (zero value), equal to the
// supported schema, and above the supported schema (both by one and by a
// larger margin, to prove the comparison isn't an off-by-one accident).
func TestValidate_ConfigSchema(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		configSchema int
		wantErr      bool
	}{
		{
			name:         "absent (zero value) always passes",
			configSchema: 0,
			wantErr:      false,
		},
		{
			name:         "equal to supported schema passes",
			configSchema: SupportedConfigSchema,
			wantErr:      false,
		},
		{
			name:         "one above supported schema fails",
			configSchema: SupportedConfigSchema + 1,
			wantErr:      true,
		},
		{
			name:         "far above supported schema fails",
			configSchema: SupportedConfigSchema + 100,
			wantErr:      true,
		},
		{
			name:         "below supported schema (older config) passes",
			configSchema: SupportedConfigSchema - 1,
			wantErr:      false,
		},
	}

	for _, tt := range tests {
		tt := tt

		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := minimalValidConfig()
			cfg.ConfigSchema = tt.configSchema

			err := validate(cfg)

			if !tt.wantErr {
				require.NoError(t, err, "config_schema=%d should not fail validation", tt.configSchema)

				return
			}

			require.Error(t, err, "config_schema=%d should fail validation", tt.configSchema)
			assertConfigSchemaErrorMessage(t, err)
		})
	}
}

// assertConfigSchemaErrorMessage asserts the rejected-schema error names the
// running binary's version/build time and points at the real cause (a stale
// binary), matching the message contract in ErrConfigSchemaTooNew.
func assertConfigSchemaErrorMessage(t *testing.T, err error) {
	t.Helper()

	info := version.Get()

	assert.Contains(t, err.Error(), "this config requires a newer ocfp build",
		"error must name the real cause so operators know to upgrade ocfp")
	assert.Contains(t, err.Error(), fmt.Sprintf("v%s", info.Version),
		"error must include the running binary's version")
	assert.Contains(t, err.Error(), info.BuildTime,
		"error must include the running binary's build time")
}

// TestErrConfigSchemaTooNew_Direct exercises the error constructor directly
// (bypassing validate()) to pin the exact message shape independent of any
// future changes to validate()'s calling convention.
func TestErrConfigSchemaTooNew_Direct(t *testing.T) {
	t.Parallel()

	err := ErrConfigSchemaTooNew(5, 1)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "config_schema 5")
	assert.Contains(t, err.Error(), "supports (1)")
	assert.Contains(t, err.Error(), "this config requires a newer ocfp build")
}

// TestConfig_ConfigSchema_YAMLRoundTrip asserts the config_schema field
// round-trips through YAML marshal/unmarshal and is omitted when zero,
// matching every other optional field in Config (see
// TestConfig_VMStorage_YAMLRoundTrip for the established pattern).
func TestConfig_ConfigSchema_YAMLRoundTrip(t *testing.T) {
	t.Parallel()

	t.Run("omitted when zero", func(t *testing.T) {
		t.Parallel()

		cfg := Config{Provider: "aws", Region: "us-east-1"}

		data, err := yaml.Marshal(&cfg)
		require.NoError(t, err)
		assert.NotContains(t, string(data), "config_schema")
	})

	t.Run("present when set", func(t *testing.T) {
		t.Parallel()

		cfg := Config{Provider: "aws", Region: "us-east-1", ConfigSchema: 2}

		data, err := yaml.Marshal(&cfg)
		require.NoError(t, err)
		assert.Contains(t, string(data), "config_schema: 2")
	})
}
