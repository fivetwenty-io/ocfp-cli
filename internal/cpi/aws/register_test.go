package aws

import (
	"os"
	"testing"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

func TestRegister(t *testing.T) {
	// Register should succeed
	err := Register()
	if err != nil {
		t.Fatalf("Failed to register AWS provider: %v", err)
	}

	// Verify provider is registered
	factory, err := cpi.Get("aws")
	if err != nil {
		t.Fatalf("Failed to get AWS provider factory: %v", err)
	}

	if factory == nil {
		t.Fatal("AWS provider factory is nil")
	}

	// Create provider with nil config
	provider, err := factory(nil)
	if err != nil {
		t.Fatalf("Failed to create provider with nil config: %v", err)
	}

	if provider == nil {
		t.Fatal("Created provider is nil")
	}

	if provider.Name() != ProviderName {
		t.Errorf("Expected provider name '%s', got '%s'", ProviderName, provider.Name())
	}
}

func TestNewProvider_WithConfigStruct(t *testing.T) {
	config := &Config{
		Region: "us-west-2",
	}

	provider, err := NewProvider(config)
	if err != nil {
		t.Fatalf("Failed to create provider with config struct: %v", err)
	}

	if provider == nil {
		t.Fatal("Created provider is nil")
	}

	if provider.Region() != "us-west-2" {
		t.Errorf("Expected region 'us-west-2', got '%s'", provider.Region())
	}
}

func TestNewProvider_WithMapConfig(t *testing.T) {
	config := map[string]interface{}{
		"region":            "eu-west-1",
		"access_key_id":     "test-access-key",
		"secret_access_key": "test-secret-key",
		"max_retries":       5,
		"timeout":           "30s",
		"enable_imdsv2":     true,
	}

	provider, err := NewProvider(config)
	if err != nil {
		t.Fatalf("Failed to create provider with map config: %v", err)
	}

	if provider == nil {
		t.Fatal("Created provider is nil")
	}

	if provider.Region() != "eu-west-1" {
		t.Errorf("Expected region 'eu-west-1', got '%s'", provider.Region())
	}
}

func TestNewProvider_MissingRegion(t *testing.T) {
	// Save original env vars
	origRegion := os.Getenv("AWS_REGION")
	origDefaultRegion := os.Getenv("AWS_DEFAULT_REGION")
	defer func() {
		os.Setenv("AWS_REGION", origRegion)
		os.Setenv("AWS_DEFAULT_REGION", origDefaultRegion)
	}()

	// Clear env vars
	os.Unsetenv("AWS_REGION")
	os.Unsetenv("AWS_DEFAULT_REGION")

	config := map[string]interface{}{
		"access_key_id":     "test-access-key",
		"secret_access_key": "test-secret-key",
	}

	_, err := NewProvider(config)
	if err == nil {
		t.Error("Expected error for missing region, got nil")
	}
}

func TestNewProvider_InvalidConfigType(t *testing.T) {
	_, err := NewProvider("invalid-config")
	if err == nil {
		t.Error("Expected error for invalid config type, got nil")
	}
}

func TestGetString(t *testing.T) {
	tests := []struct {
		name     string
		m        map[string]interface{}
		key      string
		expected string
	}{
		{
			name:     "existing string",
			m:        map[string]interface{}{"key": "value"},
			key:      "key",
			expected: "value",
		},
		{
			name:     "missing key",
			m:        map[string]interface{}{},
			key:      "key",
			expected: "",
		},
		{
			name:     "non-string value",
			m:        map[string]interface{}{"key": 123},
			key:      "key",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getString(tt.m, tt.key)
			if result != tt.expected {
				t.Errorf("Expected '%s', got '%s'", tt.expected, result)
			}
		})
	}
}

func TestGetBool(t *testing.T) {
	tests := []struct {
		name     string
		m        map[string]interface{}
		key      string
		expected bool
	}{
		{
			name:     "existing true",
			m:        map[string]interface{}{"key": true},
			key:      "key",
			expected: true,
		},
		{
			name:     "existing false",
			m:        map[string]interface{}{"key": false},
			key:      "key",
			expected: false,
		},
		{
			name:     "missing key",
			m:        map[string]interface{}{},
			key:      "key",
			expected: false,
		},
		{
			name:     "non-bool value",
			m:        map[string]interface{}{"key": "true"},
			key:      "key",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getBool(tt.m, tt.key)
			if result != tt.expected {
				t.Errorf("Expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestGetInt(t *testing.T) {
	tests := []struct {
		name     string
		m        map[string]interface{}
		key      string
		expected int
	}{
		{
			name:     "existing int",
			m:        map[string]interface{}{"key": 42},
			key:      "key",
			expected: 42,
		},
		{
			name:     "existing int64",
			m:        map[string]interface{}{"key": int64(100)},
			key:      "key",
			expected: 100,
		},
		{
			name:     "existing float64",
			m:        map[string]interface{}{"key": float64(50)},
			key:      "key",
			expected: 50,
		},
		{
			name:     "missing key",
			m:        map[string]interface{}{},
			key:      "key",
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getInt(tt.m, tt.key)
			if result != tt.expected {
				t.Errorf("Expected %d, got %d", tt.expected, result)
			}
		})
	}
}

func TestGetDuration(t *testing.T) {
	tests := []struct {
		name     string
		m        map[string]interface{}
		key      string
		expected time.Duration
	}{
		{
			name:     "existing duration",
			m:        map[string]interface{}{"key": time.Second * 30},
			key:      "key",
			expected: time.Second * 30,
		},
		{
			name:     "existing int64",
			m:        map[string]interface{}{"key": int64(1000000000)},
			key:      "key",
			expected: time.Second,
		},
		{
			name:     "existing string",
			m:        map[string]interface{}{"key": "5m"},
			key:      "key",
			expected: time.Minute * 5,
		},
		{
			name:     "missing key",
			m:        map[string]interface{}{},
			key:      "key",
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getDuration(tt.m, tt.key)
			if result != tt.expected {
				t.Errorf("Expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestGetStringSlice(t *testing.T) {
	tests := []struct {
		name     string
		m        map[string]interface{}
		key      string
		expected []string
	}{
		{
			name:     "existing string slice",
			m:        map[string]interface{}{"key": []string{"a", "b", "c"}},
			key:      "key",
			expected: []string{"a", "b", "c"},
		},
		{
			name:     "existing interface slice",
			m:        map[string]interface{}{"key": []interface{}{"x", "y", "z"}},
			key:      "key",
			expected: []string{"x", "y", "z"},
		},
		{
			name:     "missing key",
			m:        map[string]interface{}{},
			key:      "key",
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getStringSlice(tt.m, tt.key)
			if len(result) != len(tt.expected) {
				t.Errorf("Expected length %d, got %d", len(tt.expected), len(result))
				return
			}
			for i, v := range result {
				if v != tt.expected[i] {
					t.Errorf("Expected %s at index %d, got %s", tt.expected[i], i, v)
				}
			}
		})
	}
}

func TestDiscoverProvider(t *testing.T) {
	// Save original env vars
	origRegion := os.Getenv("AWS_REGION")
	origAccessKey := os.Getenv("AWS_ACCESS_KEY_ID")
	defer func() {
		os.Setenv("AWS_REGION", origRegion)
		os.Setenv("AWS_ACCESS_KEY_ID", origAccessKey)
	}()

	// Test with environment variables
	os.Setenv("AWS_REGION", "us-east-1")
	os.Setenv("AWS_ACCESS_KEY_ID", "test-key")

	config, err := DiscoverProvider()
	if err != nil {
		t.Fatalf("Failed to discover provider: %v", err)
	}

	if config.Region != "us-east-1" {
		t.Errorf("Expected region 'us-east-1', got '%s'", config.Region)
	}

	if config.AccessKeyID != "test-key" {
		t.Errorf("Expected access key 'test-key', got '%s'", config.AccessKeyID)
	}
}

func TestIsAWSEnvironment(t *testing.T) {
	// Save original env vars
	origRegion := os.Getenv("AWS_REGION")
	origAccessKey := os.Getenv("AWS_ACCESS_KEY_ID")
	defer func() {
		os.Setenv("AWS_REGION", origRegion)
		os.Setenv("AWS_ACCESS_KEY_ID", origAccessKey)
	}()

	// Test with AWS region set
	os.Setenv("AWS_REGION", "us-west-2")
	os.Unsetenv("AWS_ACCESS_KEY_ID")

	if !IsAWSEnvironment() {
		t.Error("Expected IsAWSEnvironment to return true with AWS_REGION set")
	}

	// Test with AWS access key set
	os.Unsetenv("AWS_REGION")
	os.Setenv("AWS_ACCESS_KEY_ID", "test-key")

	if !IsAWSEnvironment() {
		t.Error("Expected IsAWSEnvironment to return true with AWS_ACCESS_KEY_ID set")
	}

	// Test with no AWS environment variables
	os.Unsetenv("AWS_REGION")
	os.Unsetenv("AWS_ACCESS_KEY_ID")
	os.Unsetenv("AWS_DEFAULT_REGION")
	os.Unsetenv("AWS_PROFILE")

	if IsAWSEnvironment() {
		t.Error("Expected IsAWSEnvironment to return false with no AWS environment variables")
	}
}
