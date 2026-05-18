package providers

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
)

// TestAWSPrepareEnvironment_GenesisEnvironmentPresent verifies that
// PrepareEnvironment exports GENESIS_ENVIRONMENT (Genesis v3.2+).
func TestAWSPrepareEnvironment_GenesisEnvironmentPresent(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Name:            "my-bloc",
		AccessKeyID:     "AKIAIOSFODNN7EXAMPLE",
		SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		Region:          "us-east-1",
		Bastion: config.Bastion{
			Genesis: config.Genesis{
				Enabled: true,
			},
		},
	}

	provider := NewAWSBastionInit(cfg)
	env := provider.PrepareEnvironment()

	val, ok := env["GENESIS_ENVIRONMENT"]
	if !ok {
		t.Fatal("GENESIS_ENVIRONMENT key missing from AWS PrepareEnvironment result")
	}

	if val != "my-bloc" {
		t.Errorf("expected GENESIS_ENVIRONMENT=my-bloc, got %q", val)
	}
}

// TestAWSPrepareEnvironment_GenesisEnvBareAbsent verifies the deprecated
// GENESIS_ENV key is not present in the AWS environment map.
func TestAWSPrepareEnvironment_GenesisEnvBareAbsent(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Name:            "my-bloc",
		AccessKeyID:     "AKIAIOSFODNN7EXAMPLE",
		SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		Region:          "us-east-1",
		Bastion: config.Bastion{
			Genesis: config.Genesis{
				Enabled: true,
			},
		},
	}

	provider := NewAWSBastionInit(cfg)
	env := provider.PrepareEnvironment()

	if _, ok := env["GENESIS_ENV"]; ok {
		t.Error("deprecated GENESIS_ENV key must not be present in AWS PrepareEnvironment result")
	}
}
