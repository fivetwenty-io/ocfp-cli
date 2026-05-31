package aws

import (
	"context"
	"testing"
	"time"

	ocfpconfig "github.com/ocfp/ocfp-cli-go/internal/config"
)

func TestNewClient(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		wantErr bool
	}{
		{
			name:    "nil config",
			config:  nil,
			wantErr: false,
		},
		{
			name: "valid config",
			config: &Config{
				Region: "us-west-2",
			},
			wantErr: false,
		},
		{
			name: "invalid config - missing region",
			config: &Config{
				AccessKeyID: "test",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := NewClient(tt.config)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if client == nil {
				t.Error("expected client, got nil")
				return
			}

			if tt.config != nil && client.config != tt.config {
				t.Error("client config not set correctly")
			}
		})
	}
}

func TestClientName(t *testing.T) {
	client, err := NewClient(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if client.Name() != ProviderName {
		t.Errorf("expected provider name '%s', got '%s'", ProviderName, client.Name())
	}
}

func TestClientRegion(t *testing.T) {
	tests := []struct {
		name   string
		config *Config
		want   string
	}{
		{
			name:   "nil config",
			config: nil,
			want:   "",
		},
		{
			name: "us-west-2",
			config: &Config{
				Region: "us-west-2",
			},
			want: "us-west-2",
		},
		{
			name: "us-east-1",
			config: &Config{
				Region: "us-east-1",
			},
			want: "us-east-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := NewClient(tt.config)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if client.Region() != tt.want {
				t.Errorf("expected region '%s', got '%s'", tt.want, client.Region())
			}
		})
	}
}

func TestClientSupportsStorage(t *testing.T) {
	client, err := NewClient(&Config{Region: "us-west-2"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !client.SupportsStorage() {
		t.Error("expected SupportsStorage to return true")
	}
}

func TestClientManagers(t *testing.T) {
	client, err := NewClient(&Config{Region: "us-west-2"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Table-driven nil checks and identity comparisons for manager pairs
	type managerPair struct {
		name  string
		short interface{}
		long  interface{}
	}

	pairs := []managerPair{
		{"Network", client.Network(), client.NetworkManager()},
		{"Compute", client.Compute(), client.ComputeManager()},
		{"Storage", client.Storage(), client.StorageManager()},
		{"Security", client.Security(), client.SecurityManager()},
		{"LoadBalancer", client.LoadBalancer(), client.LoadBalancerManager()},
	}

	for _, p := range pairs {
		t.Run(p.name+"_not_nil", func(t *testing.T) {
			if p.short == nil {
				t.Errorf("%s() returned nil", p.name)
			}
			if p.long == nil {
				t.Errorf("%sManager() returned nil", p.name)
			}
		})

		t.Run(p.name+"_same_instance", func(t *testing.T) {
			if p.short != p.long {
				t.Errorf("%s() and %sManager() should return same instance", p.name, p.name)
			}
		})
	}
}

func TestClientInitializeWithStaticCredentials(t *testing.T) {
	config := &Config{
		Region:          "us-west-2",
		AccessKeyID:     "AKIAIOSFODNN7EXAMPLE",
		SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		MaxRetries:      3,
		RetryMode:       "standard",
	}

	client, err := NewClient(nil)
	if err != nil {
		t.Fatalf("unexpected error creating client: %v", err)
	}

	ctx := context.Background()
	err = client.Initialize(ctx, config)
	if err != nil {
		t.Fatalf("unexpected error initializing client: %v", err)
	}

	if client.config.AccessKeyID != config.AccessKeyID {
		t.Errorf("expected AccessKeyID '%s', got '%s'", config.AccessKeyID, client.config.AccessKeyID)
	}

	if client.config.SecretAccessKey != config.SecretAccessKey {
		t.Errorf("expected SecretAccessKey to be set")
	}

	if client.Region() != config.Region {
		t.Errorf("expected region '%s', got '%s'", config.Region, client.Region())
	}
}

func TestClientInitializeWithProfile(t *testing.T) {
	config := &Config{
		Region:     "eu-west-1",
		Profile:    "test-profile",
		MaxRetries: 5,
		RetryMode:  "adaptive",
	}

	client, err := NewClient(nil)
	if err != nil {
		t.Fatalf("unexpected error creating client: %v", err)
	}

	ctx := context.Background()
	_ = client.Initialize(ctx, config) //nolint:errcheck // Profile loading will fail in test environment without actual credentials file

	// Just verify the config was set correctly
	if client.config.Profile != config.Profile {
		t.Errorf("expected profile '%s', got '%s'", config.Profile, client.config.Profile)
	}

	if client.config.RetryMode != config.RetryMode {
		t.Errorf("expected retry mode '%s', got '%s'", config.RetryMode, client.config.RetryMode)
	}
}

func TestClientInitializeWithRoleARN(t *testing.T) {
	config := &Config{
		Region:          "ap-southeast-1",
		RoleARN:         "arn:aws:iam::123456789012:role/test-role",
		RoleSessionName: "test-session",
		Timeout:         30 * time.Minute,
	}

	client, err := NewClient(nil)
	if err != nil {
		t.Fatalf("unexpected error creating client: %v", err)
	}

	ctx := context.Background()
	err = client.Initialize(ctx, config)
	if err != nil {
		t.Fatalf("unexpected error initializing client: %v", err)
	}

	if client.config.RoleARN != config.RoleARN {
		t.Errorf("expected RoleARN '%s', got '%s'", config.RoleARN, client.config.RoleARN)
	}

	if client.config.RoleSessionName != config.RoleSessionName {
		t.Errorf("expected RoleSessionName '%s', got '%s'", config.RoleSessionName, client.config.RoleSessionName)
	}
}

func TestClientInitializeWithEnvironmentVariables(t *testing.T) {
	// t.Setenv restores originals on cleanup
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIAIOSFODNN7EXAMPLE")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY")
	t.Setenv("AWS_REGION", "us-east-1")

	config := &Config{
		Region: "us-east-1",
	}

	client, err := NewClient(nil)
	if err != nil {
		t.Fatalf("unexpected error creating client: %v", err)
	}

	ctx := context.Background()
	err = client.Initialize(ctx, config)
	if err != nil {
		t.Fatalf("unexpected error initializing client: %v", err)
	}

	// The client should use environment variables when no explicit credentials are provided
	if client.Region() != "us-east-1" {
		t.Errorf("expected region 'us-east-1', got '%s'", client.Region())
	}
}

func TestClientInitializeWithOCFPConfig(t *testing.T) {
	ocfpConfig := &ocfpconfig.Config{
		Name:   "my-production-bloc",
		Region: "us-west-2",
	}

	client, err := NewClient(nil)
	if err != nil {
		t.Fatalf("unexpected error creating client: %v", err)
	}

	ctx := context.Background()
	_ = client.Initialize(ctx, ocfpConfig) //nolint:errcheck // Profile loading will fail without actual credentials

	// Profile should default to bloc name when no credentials provided
	if client.config.Profile != "my-production-bloc" {
		t.Errorf("expected profile to default to bloc name 'my-production-bloc', got '%s'", client.config.Profile)
	}

	if client.Region() != "us-west-2" {
		t.Errorf("expected region 'us-west-2', got '%s'", client.Region())
	}
}

func TestClientInitializeWithOCFPConfigAndCredentials(t *testing.T) {
	ocfpConfig := &ocfpconfig.Config{
		Name:            "my-bloc",
		Region:          "eu-central-1",
		AccessKeyID:     "AKIAIOSFODNN7EXAMPLE",
		SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
	}

	client, err := NewClient(nil)
	if err != nil {
		t.Fatalf("unexpected error creating client: %v", err)
	}

	ctx := context.Background()
	err = client.Initialize(ctx, ocfpConfig)
	if err != nil {
		t.Fatalf("unexpected error initializing client: %v", err)
	}

	// Profile should NOT be set when explicit credentials are provided
	if client.config.Profile != "" {
		t.Errorf("expected profile to be empty when credentials provided, got '%s'", client.config.Profile)
	}

	if client.config.AccessKeyID != ocfpConfig.AccessKeyID {
		t.Errorf("expected AccessKeyID '%s', got '%s'", ocfpConfig.AccessKeyID, client.config.AccessKeyID)
	}

	if client.Region() != "eu-central-1" {
		t.Errorf("expected region 'eu-central-1', got '%s'", client.Region())
	}
}

func TestClientInitializeWithConnectionPooling(t *testing.T) {
	config := &Config{
		Region:              "us-west-2",
		MaxIdleConns:        50,
		MaxIdleConnsPerHost: 5,
		IdleConnTimeout:     60 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
		DialTimeout:         15 * time.Second,
		KeepAlive:           30 * time.Second,
	}

	client, err := NewClient(nil)
	if err != nil {
		t.Fatalf("unexpected error creating client: %v", err)
	}

	ctx := context.Background()
	err = client.Initialize(ctx, config)
	if err != nil {
		t.Fatalf("unexpected error initializing client: %v", err)
	}

	if client.config.MaxIdleConns != config.MaxIdleConns {
		t.Errorf("expected MaxIdleConns %d, got %d", config.MaxIdleConns, client.config.MaxIdleConns)
	}

	if client.config.MaxIdleConnsPerHost != config.MaxIdleConnsPerHost {
		t.Errorf("expected MaxIdleConnsPerHost %d, got %d", config.MaxIdleConnsPerHost, client.config.MaxIdleConnsPerHost)
	}
}

func TestClientInitializeWithCustomEndpoints(t *testing.T) {
	config := &Config{
		Region:      "us-west-2",
		STSEndpoint: "https://sts.custom.endpoint",
		EC2Endpoint: "https://ec2.custom.endpoint",
		S3Endpoint:  "https://s3.custom.endpoint",
		ELBEndpoint: "https://elb.custom.endpoint",
	}

	client, err := NewClient(nil)
	if err != nil {
		t.Fatalf("unexpected error creating client: %v", err)
	}

	ctx := context.Background()
	err = client.Initialize(ctx, config)
	if err != nil {
		t.Fatalf("unexpected error initializing client: %v", err)
	}

	if client.config.STSEndpoint != config.STSEndpoint {
		t.Errorf("expected STSEndpoint '%s', got '%s'", config.STSEndpoint, client.config.STSEndpoint)
	}

	if client.config.EC2Endpoint != config.EC2Endpoint {
		t.Errorf("expected EC2Endpoint '%s', got '%s'", config.EC2Endpoint, client.config.EC2Endpoint)
	}
}

func TestClientInitializeWithInvalidConfig(t *testing.T) {
	tests := []struct {
		name    string
		config  interface{}
		wantErr bool
	}{
		{
			name:    "wrong config type",
			config:  "not a config",
			wantErr: true,
		},
		{
			name: "missing region",
			config: &Config{
				AccessKeyID:     "test",
				SecretAccessKey: "test",
			},
			wantErr: true,
		},
		{
			name: "access key without secret",
			config: &Config{
				Region:      "us-west-2",
				AccessKeyID: "test",
			},
			wantErr: true,
		},
		{
			name: "secret key without access",
			config: &Config{
				Region:          "us-west-2",
				SecretAccessKey: "test",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := NewClient(nil)
			if err != nil {
				t.Fatalf("unexpected error creating client: %v", err)
			}

			ctx := context.Background()
			err = client.Initialize(ctx, tt.config)

			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
			}

			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestClientValidateRegion(t *testing.T) {
	tests := []struct {
		name    string
		region  string
		wantErr bool
	}{
		{"us-east-1", "us-east-1", false},
		{"us-west-2", "us-west-2", false},
		{"eu-west-1", "eu-west-1", false},
		{"ap-southeast-1", "ap-southeast-1", false},
		{"invalid region", "invalid-region", true},
		{"empty region", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &Config{Region: tt.region}

			if tt.region == "" {
				// Skip validation for empty region test
				err := config.Validate()
				if !tt.wantErr && err != nil {
					t.Errorf("unexpected validation error: %v", err)
				} else if tt.wantErr && err == nil {
					t.Error("expected validation error, got nil")
				}
				return
			}

			client, err := NewClient(config)
			if err != nil && !tt.wantErr {
				t.Fatalf("unexpected error creating client: %v", err)
			}

			if client != nil {
				ctx := context.Background()
				err = client.validateRegion(ctx)

				if tt.wantErr && err == nil {
					t.Error("expected region validation error, got nil")
				}

				if !tt.wantErr && err != nil {
					t.Errorf("unexpected region validation error: %v", err)
				}
			}
		})
	}
}

func TestClientCleanup(t *testing.T) {
	config := &Config{
		Region: "us-west-2",
	}

	client, err := NewClient(config)
	if err != nil {
		t.Fatalf("unexpected error creating client: %v", err)
	}

	ctx := context.Background()
	err = client.Cleanup(ctx)
	if err != nil {
		t.Errorf("unexpected error during cleanup: %v", err)
	}

	// Verify clients are reset
	if client.clientsLoaded {
		t.Error("expected clientsLoaded to be false after cleanup")
	}
}
