package stackit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewClient(t *testing.T) {
	cfg := &Config{
		Region:    "eu-central-1",
		ProjectID: "test-project",
		AuthToken: "test-token",
	}

	client, err := NewClient(cfg)
	require.NoError(t, err)
	assert.NotNil(t, client)
	assert.Equal(t, cfg, client.config)
	assert.NotNil(t, client.httpClient)
}

func TestClient_Authenticate(t *testing.T) {
	// Create test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/projects/test-project", r.URL.Path)
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))

		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(map[string]interface{}{
			"id":   "test-project",
			"name": "Test Project",
		}); err != nil {
			t.Errorf("Failed to encode response: %v", err)
		}
	}))
	defer server.Close()

	config := &Config{
		ProjectID:  "test-project",
		AuthToken:  "test-token",
		BaseURL:    server.URL,
		MaxRetries: 3,
		Timeout:    10 * time.Second,
		RateLimit:  10,
	}

	client, err := NewClient(config)
	require.NoError(t, err)

	authErr := client.Authenticate(context.Background())
	assert.NoError(t, authErr)
}

func TestClient_ValidateCredentials(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		response   map[string]interface{}
		wantErr    bool
	}{
		{
			name:       "valid credentials",
			statusCode: http.StatusOK,
			response: map[string]interface{}{
				"valid": true,
			},
			wantErr: false,
		},
		{
			name:       "invalid credentials",
			statusCode: http.StatusUnauthorized,
			response: map[string]interface{}{
				"error": "invalid token",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				if err := json.NewEncoder(w).Encode(tt.response); err != nil {
					t.Errorf("Failed to encode response: %v", err)
				}
			}))
			defer server.Close()

			client := &Client{
				config: &Config{
					AuthToken:  "test-token",
					BaseURL:    server.URL,
					MaxRetries: 3,
				},
				httpClient:  &http.Client{Timeout: 10 * time.Second},
				rateLimiter: cpi.NewRateLimiter(10, 100),
			}

			err := client.ValidateCredentials(context.Background())
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestClient_Name(t *testing.T) {
	client := &Client{}
	assert.Equal(t, "stackit", client.Name())
}

func TestClient_Region(t *testing.T) {
	client := &Client{
		config: &Config{
			Region: "eu-west-1",
		},
	}
	assert.Equal(t, "eu-west-1", client.Region())
}

func TestClient_Initialize(t *testing.T) {
	// Create test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/auth/validate" {
			w.WriteHeader(http.StatusOK)
			if err := json.NewEncoder(w).Encode(map[string]interface{}{
				"valid": true,
				"user":  "test-user",
			}); err != nil {
				t.Errorf("Failed to encode response: %v", err)
			}
		}
	}))
	defer server.Close()

	cfg := &Config{
		Region:    "eu-central-1",
		ProjectID: "test-project",
		AuthToken: "test-token",
		BaseURL:   server.URL,
	}

	client := &Client{}
	err := client.Initialize(context.Background(), cfg)

	assert.NoError(t, err)
	assert.Equal(t, cfg, client.config)
	assert.NotNil(t, client.httpClient)
	assert.NotNil(t, client.rateLimiter)
	assert.NotNil(t, client.network)
	assert.NotNil(t, client.compute)
	assert.NotNil(t, client.storage)
	assert.NotNil(t, client.security)
	assert.NotNil(t, client.loadBalancer)
}

func TestClient_parseError(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		wantCode   string
		wantMsg    string
	}{
		{
			name:       "not found",
			statusCode: http.StatusNotFound,
			body:       `{"error": "resource not found"}`,
			wantCode:   "404",
			wantMsg:    "resource not found",
		},
		{
			name:       "bad request",
			statusCode: http.StatusBadRequest,
			body:       `{"message": "invalid request"}`,
			wantCode:   "400",
			wantMsg:    "invalid request",
		},
		{
			name:       "internal server error",
			statusCode: http.StatusInternalServerError,
			body:       `Internal Server Error`,
			wantCode:   "500",
			wantMsg:    "Internal Server Error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				if _, err := w.Write([]byte(tt.body)); err != nil {
					t.Errorf("Failed to write response: %v", err)
				}
			}))
			defer server.Close()

			resp, err := http.Get(server.URL)
			require.NoError(t, err)
			defer func() {
				if err := resp.Body.Close(); err != nil {
					t.Errorf("Failed to close response body: %v", err)
				}
			}()

			client := &Client{}
			err = client.parseError(resp)

			require.Error(t, err)
			perr, ok := err.(*cpi.ProviderError)
			require.True(t, ok)
			assert.Equal(t, "stackit", perr.Provider)
			assert.Equal(t, tt.wantCode, perr.Code)
			assert.Contains(t, perr.Message, tt.wantMsg)
		})
	}
}
