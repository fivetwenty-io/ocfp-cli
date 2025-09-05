package stackit

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewClient(t *testing.T) {
	cfg := &Config{
		Region:    "eu01",
		ProjectID: "test-project",
		AuthToken: "test-token",
	}

	client, err := NewClient(cfg)
	require.NoError(t, err)
	assert.NotNil(t, client)
	assert.Equal(t, cfg, client.config)
}

func TestClient_Authenticate(t *testing.T) {
	t.Skip("STACKIT auth now uses SDK; requires real credentials.")
}

func TestClient_ValidateCredentials(t *testing.T) {
	t.Skip("STACKIT credential validation uses SDK; skipping")
}

func TestClient_Name(t *testing.T) {
	client := &Client{}
	assert.Equal(t, "stackit", client.Name())
}

func TestClient_Region(t *testing.T) {
	client := &Client{
		config: &Config{
			Region: "eu02",
		},
	}
	assert.Equal(t, "eu02", client.Region())
}

func TestClient_Initialize(t *testing.T) { t.Skip("STACKIT Initialize authenticates via SDK; skip.") }

func TestClient_parseError(t *testing.T) { t.Skip("Removed raw HTTP helpers; no parseError") }
