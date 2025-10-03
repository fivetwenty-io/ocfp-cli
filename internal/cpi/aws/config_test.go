package aws

import (
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Region != "us-east-1" {
		t.Errorf("expected default region 'us-east-1', got %s", cfg.Region)
	}

	if cfg.MaxRetries != 3 {
		t.Errorf("expected MaxRetries 3, got %d", cfg.MaxRetries)
	}

	if cfg.RetryMode != "standard" {
		t.Errorf("expected RetryMode 'standard', got %s", cfg.RetryMode)
	}

	if !cfg.EnableDNSHostnames {
		t.Error("expected EnableDNSHostnames to be true")
	}

	if !cfg.EnableDNSSupport {
		t.Error("expected EnableDNSSupport to be true")
	}

	if !cfg.EnableIMDSv2 {
		t.Error("expected EnableIMDSv2 to be true")
	}

	if cfg.EnableDetailedMonitoring {
		t.Error("expected EnableDetailedMonitoring to be false")
	}

	if cfg.DefaultTags == nil {
		t.Error("expected DefaultTags to be initialized")
	}
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid config with region only",
			config: &Config{
				Region: "us-west-2",
			},
			wantErr: false,
		},
		{
			name: "valid config with static credentials",
			config: &Config{
				Region:          "us-west-2",
				AccessKeyID:     "AKIAIOSFODNN7EXAMPLE",
				SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
			},
			wantErr: false,
		},
		{
			name: "valid config with session token",
			config: &Config{
				Region:          "us-west-2",
				AccessKeyID:     "AKIAIOSFODNN7EXAMPLE",
				SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
				SessionToken:    "FakeSessionToken",
			},
			wantErr: false,
		},
		{
			name: "valid config with VPC CIDR",
			config: &Config{
				Region:       "us-west-2",
				VPCCIDRBlock: "10.0.0.0/16",
			},
			wantErr: false,
		},
		{
			name:    "missing region",
			config:  &Config{},
			wantErr: true,
			errMsg:  "region is required",
		},
		{
			name: "access key without secret key",
			config: &Config{
				Region:      "us-west-2",
				AccessKeyID: "AKIAIOSFODNN7EXAMPLE",
			},
			wantErr: true,
			errMsg:  "secret access key required",
		},
		{
			name: "secret key without access key",
			config: &Config{
				Region:          "us-west-2",
				SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
			},
			wantErr: true,
			errMsg:  "access key ID required",
		},
		{
			name: "invalid VPC CIDR format",
			config: &Config{
				Region:       "us-west-2",
				VPCCIDRBlock: "invalid-cidr",
			},
			wantErr: true,
			errMsg:  "invalid CIDR block format",
		},
		{
			name: "negative max retries",
			config: &Config{
				Region:     "us-west-2",
				MaxRetries: -1,
			},
			wantErr: true,
			errMsg:  "max retries cannot be negative",
		},
		{
			name: "invalid retry mode",
			config: &Config{
				Region:    "us-west-2",
				RetryMode: "invalid",
			},
			wantErr: true,
			errMsg:  "retry mode must be 'standard' or 'adaptive'",
		},
		{
			name: "valid adaptive retry mode",
			config: &Config{
				Region:    "us-west-2",
				RetryMode: "adaptive",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()

			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error containing '%s', got nil", tt.errMsg)
					return
				}

				if tt.errMsg != "" {
					errStr := err.Error()
					contains := false
					for i := 0; i <= len(errStr)-len(tt.errMsg); i++ {
						if errStr[i:i+len(tt.errMsg)] == tt.errMsg {
							contains = true
							break
						}
					}
					if !contains {
						t.Errorf("expected error containing '%s', got '%s'", tt.errMsg, errStr)
					}
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestIsValidCIDR(t *testing.T) {
	tests := []struct {
		name  string
		cidr  string
		valid bool
	}{
		{"valid /16", "10.0.0.0/16", true},
		{"valid /24", "192.168.1.0/24", true},
		{"valid /8", "10.0.0.0/8", true},
		{"valid /32", "10.0.0.1/32", true},
		{"no slash", "10.0.0.0", false},
		{"too short", "10.0/16", false},
		{"too long", "10.0.0.0.0.0.0/16", false},
		{"empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isValidCIDR(tt.cidr)
			if result != tt.valid {
				t.Errorf("isValidCIDR(%s) = %v, want %v", tt.cidr, result, tt.valid)
			}
		})
	}
}

func TestConfigError(t *testing.T) {
	err := &ConfigError{
		Field:   "Region",
		Message: "region is required",
	}

	expected := "config error: Region: region is required"
	if err.Error() != expected {
		t.Errorf("expected '%s', got '%s'", expected, err.Error())
	}
}
