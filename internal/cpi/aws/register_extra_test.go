package aws

import (
	"testing"
)

// ---- detectRegionFromEnv ----------------------------------------------------

// ---- getInt / getDuration / getStringSlice edge cases -----------------------

func TestGetInt_WrongTypeFallthrough(t *testing.T) {
	t.Parallel()

	result := getInt(map[string]interface{}{"key": "not-an-int"}, "key")
	if result != 0 {
		t.Errorf("getInt(string value) = %d, want 0", result)
	}
}

func TestGetDuration_Float64Branch(t *testing.T) {
	t.Parallel()

	result := getDuration(map[string]interface{}{"key": float64(5000000000)}, "key")
	if result != 5000000000 {
		t.Errorf("getDuration(float64) = %v, want 5000000000ns", result)
	}
}

func TestGetDuration_InvalidStringFallthrough(t *testing.T) {
	t.Parallel()

	result := getDuration(map[string]interface{}{"key": "not-a-duration"}, "key")
	if result != 0 {
		t.Errorf("getDuration(invalid string) = %v, want 0", result)
	}
}

func TestGetStringSlice_WrongTypeFallthrough(t *testing.T) {
	t.Parallel()

	result := getStringSlice(map[string]interface{}{"key": 42}, "key")
	if result != nil {
		t.Errorf("getStringSlice(int value) = %v, want nil", result)
	}
}

func TestDetectRegionFromEnv_AlreadySet(t *testing.T) {
	cfg := &Config{Region: "us-east-1"}
	detectRegionFromEnv(cfg)
	if cfg.Region != "us-east-1" {
		t.Errorf("Region changed when already set: got %q", cfg.Region)
	}
}

func TestDetectRegionFromEnv_AWSRegion(t *testing.T) {
	t.Setenv("AWS_REGION", "eu-west-1")
	t.Setenv("AWS_DEFAULT_REGION", "")

	cfg := &Config{}
	detectRegionFromEnv(cfg)
	if cfg.Region != "eu-west-1" {
		t.Errorf("Region = %q, want eu-west-1 (from AWS_REGION)", cfg.Region)
	}
}

func TestDetectRegionFromEnv_AWSDefaultRegion(t *testing.T) {
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_DEFAULT_REGION", "ap-southeast-1")

	cfg := &Config{}
	detectRegionFromEnv(cfg)
	if cfg.Region != "ap-southeast-1" {
		t.Errorf("Region = %q, want ap-southeast-1 (from AWS_DEFAULT_REGION)", cfg.Region)
	}
}

func TestDetectRegionFromEnv_NoEnvVars(t *testing.T) {
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_DEFAULT_REGION", "")

	cfg := &Config{}
	detectRegionFromEnv(cfg)
	if cfg.Region != "" {
		t.Errorf("Region = %q, want empty (no env vars set)", cfg.Region)
	}
}

// ---- detectCredentialsFromEnv -----------------------------------------------

func TestDetectCredentialsFromEnv_AlreadyHasAccessKey(t *testing.T) {
	cfg := &Config{AccessKeyID: "existing-key"}
	detectCredentialsFromEnv(cfg)
	if cfg.AccessKeyID != "existing-key" {
		t.Errorf("AccessKeyID changed when already set")
	}
}

func TestDetectCredentialsFromEnv_AlreadyHasProfile(t *testing.T) {
	cfg := &Config{Profile: "my-profile"}
	detectCredentialsFromEnv(cfg)
	if cfg.Profile != "my-profile" {
		t.Errorf("Profile changed when already set")
	}
}

func TestDetectCredentialsFromEnv_AlreadyHasRoleARN(t *testing.T) {
	cfg := &Config{RoleARN: "arn:aws:iam::123:role/test"}
	detectCredentialsFromEnv(cfg)
	if cfg.RoleARN != "arn:aws:iam::123:role/test" {
		t.Errorf("RoleARN changed when already set")
	}
}

func TestDetectCredentialsFromEnv_AccessKeyAndSecret(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIAIOSFODNN7EXAMPLE")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY")
	t.Setenv("AWS_SESSION_TOKEN", "session-tok")
	t.Setenv("AWS_PROFILE", "")

	cfg := &Config{}
	detectCredentialsFromEnv(cfg)

	if cfg.AccessKeyID != "AKIAIOSFODNN7EXAMPLE" {
		t.Errorf("AccessKeyID = %q, want AKIAIOSFODNN7EXAMPLE", cfg.AccessKeyID)
	}
	if cfg.SecretAccessKey != "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY" {
		t.Errorf("SecretAccessKey not set correctly")
	}
	if cfg.SessionToken != "session-tok" {
		t.Errorf("SessionToken = %q, want session-tok", cfg.SessionToken)
	}
}

func TestDetectCredentialsFromEnv_AccessKeyNoSecret(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIAIOSFODNN7EXAMPLE")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	t.Setenv("AWS_SESSION_TOKEN", "")
	t.Setenv("AWS_PROFILE", "")

	cfg := &Config{}
	detectCredentialsFromEnv(cfg)

	if cfg.AccessKeyID != "AKIAIOSFODNN7EXAMPLE" {
		t.Errorf("AccessKeyID = %q, want AKIAIOSFODNN7EXAMPLE", cfg.AccessKeyID)
	}
	if cfg.SecretAccessKey != "" {
		t.Errorf("SecretAccessKey should be empty when AWS_SECRET_ACCESS_KEY not set")
	}
}

func TestDetectCredentialsFromEnv_ProfileFallback(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_PROFILE", "dev-profile")

	cfg := &Config{}
	detectCredentialsFromEnv(cfg)

	if cfg.Profile != "dev-profile" {
		t.Errorf("Profile = %q, want dev-profile", cfg.Profile)
	}
}

// ---- DiscoverProvider (additional branches) ---------------------------------

func TestDiscoverProvider_DefaultRegion(t *testing.T) {
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_DEFAULT_REGION", "eu-central-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_PROFILE", "")

	cfg, err := DiscoverProvider()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Region != "eu-central-1" {
		t.Errorf("Region = %q, want eu-central-1", cfg.Region)
	}
}

func TestDiscoverProvider_FallsBackToUsEast1(t *testing.T) {
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_DEFAULT_REGION", "")
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_PROFILE", "")

	cfg, err := DiscoverProvider()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Region != "us-east-1" {
		t.Errorf("Region = %q, want us-east-1 (default fallback)", cfg.Region)
	}
}

func TestDiscoverProvider_FullCredentials(t *testing.T) {
	t.Setenv("AWS_REGION", "us-west-2")
	t.Setenv("AWS_ACCESS_KEY_ID", "AKID")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "SECRET")
	t.Setenv("AWS_SESSION_TOKEN", "TOKEN")

	cfg, err := DiscoverProvider()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.SecretAccessKey != "SECRET" {
		t.Errorf("SecretAccessKey = %q, want SECRET", cfg.SecretAccessKey)
	}
	if cfg.SessionToken != "TOKEN" {
		t.Errorf("SessionToken = %q, want TOKEN", cfg.SessionToken)
	}
}

func TestDiscoverProvider_ProfileCredentials(t *testing.T) {
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_PROFILE", "prod-profile")

	cfg, err := DiscoverProvider()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Profile != "prod-profile" {
		t.Errorf("Profile = %q, want prod-profile", cfg.Profile)
	}
}

func TestDetectCredentialsFromEnv_NoEnvVars(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_PROFILE", "")

	cfg := &Config{}
	detectCredentialsFromEnv(cfg)

	if cfg.AccessKeyID != "" || cfg.Profile != "" {
		t.Errorf("Credentials set unexpectedly from empty env vars")
	}
}
