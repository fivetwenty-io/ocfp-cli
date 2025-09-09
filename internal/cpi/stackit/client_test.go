package stackit_test

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/cpi/stackit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewClient(t *testing.T) {
	t.Parallel()

	cfg := &stackit.Config{
		ProjectID:           "test-project",
		OrgID:               "",
		AuthToken:           "test-token",
		ServiceAccountToken: "",
		ServiceAccountJSON:  "",
		Region:              "eu01",
		BaseURL:             "",
		Timeout:             0,
		MaxRetries:          0,
	}

	client, err := stackit.NewClient(cfg)
	require.NoError(t, err)
	assert.NotNil(t, client)
	assert.Equal(t, cfg.Region, client.Region())
}

func TestClient_Authenticate(t *testing.T) {
	t.Parallel()
	t.Skip("STACKIT auth now uses SDK; requires real credentials.")
}

func TestClient_ValidateCredentials(t *testing.T) {
	t.Parallel()
	t.Skip("STACKIT credential validation uses SDK; skipping")
}

func TestClient_Name(t *testing.T) {
	t.Parallel()

	client := &stackit.Client{}
	assert.Equal(t, "stackit", client.Name())
}

func TestClient_Region(t *testing.T) {
	t.Parallel()

	cfg := &stackit.Config{
		ProjectID:           "",
		OrgID:               "",
		AuthToken:           "",
		ServiceAccountToken: "",
		ServiceAccountJSON:  "",
		Region:              "eu02",
		BaseURL:             "",
		Timeout:             0,
		MaxRetries:          0,
	}
	client, err := stackit.NewClient(cfg)
	require.NoError(t, err)
	assert.Equal(t, "eu02", client.Region())
}

func TestClient_Initialize(t *testing.T) {
	t.Parallel()
	t.Skip("STACKIT Initialize authenticates via SDK; skip.")
}

func TestClient_parseError(t *testing.T) {
	t.Parallel()
	t.Skip("Removed raw HTTP helpers; no parseError")
}
