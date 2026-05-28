package vault

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// --- getEnvOrDefault ---

func TestGetEnvOrDefault_EnvSet(t *testing.T) {
	// t.Setenv and t.Parallel are mutually exclusive.
	t.Setenv("TEST_KEY_GED", "myvalue")
	assert.Equal(t, "myvalue", getEnvOrDefault("TEST_KEY_GED", "fallback"))
}

func TestGetEnvOrDefault_EnvEmpty(t *testing.T) {
	// t.Setenv and t.Parallel are mutually exclusive.
	t.Setenv("TEST_KEY_GED_EMPTY", "")
	assert.Equal(t, "fallback", getEnvOrDefault("TEST_KEY_GED_EMPTY", "fallback"))
}

func TestGetEnvOrDefault_EnvMissing(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "default-val", getEnvOrDefault("NONEXISTENT_ENV_KEY_XYZ_12345", "default-val"))
}

func TestGetEnvOrDefault_EmptyDefault(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "", getEnvOrDefault("NONEXISTENT_ENV_KEY_XYZ_12346", ""))
}

// --- createVaultAPIConfig ---

func TestCreateVaultAPIConfig_Address(t *testing.T) {
	t.Parallel()
	cfg := &Config{Address: "https://vault.example.com:8200"}
	got := createVaultAPIConfig(cfg)
	assert.Equal(t, "https://vault.example.com:8200", got.Address)
}

func TestCreateVaultAPIConfig_ZeroRetries(t *testing.T) {
	t.Parallel()
	cfg := &Config{Address: "https://vault.example.com:8200"}
	got := createVaultAPIConfig(cfg)
	assert.Equal(t, 0, got.MaxRetries)
}

func TestCreateVaultAPIConfig_ZeroTimeout(t *testing.T) {
	t.Parallel()
	cfg := &Config{Address: "https://vault.example.com:8200"}
	got := createVaultAPIConfig(cfg)
	assert.Equal(t, time.Duration(0), got.Timeout)
}

func TestCreateVaultAPIConfig_ZeroRetryWaits(t *testing.T) {
	t.Parallel()
	cfg := &Config{Address: "https://vault.example.com:8200", Token: "tok"}
	got := createVaultAPIConfig(cfg)
	assert.Equal(t, time.Duration(0), got.MinRetryWait)
	assert.Equal(t, time.Duration(0), got.MaxRetryWait)
}

func TestCreateVaultAPIConfig_NoOutputFlags(t *testing.T) {
	t.Parallel()
	cfg := &Config{Address: "https://vault.example.com:8200"}
	got := createVaultAPIConfig(cfg)
	assert.False(t, got.OutputCurlString)
	assert.False(t, got.OutputPolicy)
}

func TestCreateVaultAPIConfig_EmptyAddress(t *testing.T) {
	t.Parallel()
	cfg := &Config{}
	got := createVaultAPIConfig(cfg)
	assert.Equal(t, "", got.Address)
}
