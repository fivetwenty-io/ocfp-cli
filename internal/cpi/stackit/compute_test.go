//go:build disable_stackit_http_tests

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

func setupTestComputeManager(t *testing.T, handler http.HandlerFunc) (*ComputeManager, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)

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

	return client.compute, server
}

func TestComputeManager_CreateInstance(t *testing.T) {
	expectedInstance := &cpi.Instance{
		ID:    "instance-123",
		Name:  "test-instance",
		State: cpi.ResourceStateActive,
	}

	callCount := 0
	manager, server := setupTestComputeManager(t, func(w http.ResponseWriter, r *http.Request) {
		callCount++

		switch callCount {
		case 1: // Create instance call
			assert.Equal(t, "/v1/projects/test-project/instances", r.URL.Path)
			assert.Equal(t, "POST", r.Method)

			var reqBody map[string]interface{}
			err := json.NewDecoder(r.Body).Decode(&reqBody)
			require.NoError(t, err, "Failed to decode request body")
			require.NotEmpty(t, reqBody, "Request body is empty")
			assert.Equal(t, "test-instance", reqBody["name"])

			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(expectedInstance)

		case 2: // Get instance status call (waiting for active)
			assert.Equal(t, "/v1/projects/test-project/instances/instance-123", r.URL.Path)
			assert.Equal(t, "GET", r.Method)

			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(expectedInstance)
		}
	})
	defer server.Close()

	req := &cpi.CreateInstanceRequest{
		Name:           "test-instance",
		Flavor:         "m1.small",
		Image:          "ubuntu-20.04",
		NetworkID:      "net-123",
		SubnetID:       "subnet-123",
		SecurityGroups: []string{"sg-123"},
		KeyPair:        "test-key",
	}

	instance, err := manager.CreateInstance(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, expectedInstance.ID, instance.ID)
	assert.Equal(t, expectedInstance.Name, instance.Name)
}

func TestComputeManager_GetInstance(t *testing.T) {
	expectedInstance := &cpi.Instance{
		ID:    "instance-123",
		Name:  "test-instance",
		State: cpi.ResourceStateActive,
	}

	manager, server := setupTestComputeManager(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/projects/test-project/instances/instance-123", r.URL.Path)
		assert.Equal(t, "GET", r.Method)

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(expectedInstance)
	})
	defer server.Close()

	instance, err := manager.GetInstance(context.Background(), "instance-123")
	require.NoError(t, err)
	assert.Equal(t, expectedInstance.ID, instance.ID)
	assert.Equal(t, expectedInstance.Name, instance.Name)
}

func TestComputeManager_GetInstance_NotFound(t *testing.T) {
	manager, server := setupTestComputeManager(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": "instance not found",
		})
	})
	defer server.Close()

	instance, err := manager.GetInstance(context.Background(), "instance-123")
	assert.Error(t, err)
	assert.Nil(t, instance)

	perr, ok := err.(*cpi.ProviderError)
	require.True(t, ok)
	assert.Equal(t, "NotFound", perr.Code)
}

func TestComputeManager_ListInstances(t *testing.T) {
	expectedInstances := []*cpi.Instance{
		{
			ID:   "instance-1",
			Name: "test-1",
		},
		{
			ID:   "instance-2",
			Name: "test-2",
		},
	}

	manager, server := setupTestComputeManager(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/projects/test-project/instances", r.URL.Path)
		assert.Equal(t, "GET", r.Method)
		assert.Contains(t, r.URL.RawQuery, "label=test")

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"instances": expectedInstances,
		})
	})
	defer server.Close()

	filters := map[string]string{
		"label": "test",
	}

	instances, err := manager.ListInstances(context.Background(), filters)
	require.NoError(t, err)
	assert.Len(t, instances, 2)
	assert.Equal(t, expectedInstances[0].ID, instances[0].ID)
	assert.Equal(t, expectedInstances[1].ID, instances[1].ID)
}

func TestComputeManager_DeleteInstance(t *testing.T) {
	manager, server := setupTestComputeManager(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/projects/test-project/instances/instance-123", r.URL.Path)
		assert.Equal(t, "DELETE", r.Method)

		w.WriteHeader(http.StatusNoContent)
	})
	defer server.Close()

	err := manager.DeleteInstance(context.Background(), "instance-123")
	assert.NoError(t, err)
}

func TestComputeManager_DeleteInstance_NotFound(t *testing.T) {
	manager, server := setupTestComputeManager(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	defer server.Close()

	// Should not return error for not found (idempotent)
	err := manager.DeleteInstance(context.Background(), "instance-123")
	assert.NoError(t, err)
}

func TestComputeManager_StartInstance(t *testing.T) {
	manager, server := setupTestComputeManager(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/projects/test-project/instances/instance-123/action", r.URL.Path)
		assert.Equal(t, "POST", r.Method)

		var reqBody map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Errorf("Failed to decode request body: %v", err)
		}
		assert.Equal(t, "start", reqBody["action"])

		w.WriteHeader(http.StatusOK)
	})
	defer server.Close()

	err := manager.StartInstance(context.Background(), "instance-123")
	assert.NoError(t, err)
}

func TestComputeManager_StopInstance(t *testing.T) {
	manager, server := setupTestComputeManager(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/projects/test-project/instances/instance-123/action", r.URL.Path)
		assert.Equal(t, "POST", r.Method)

		var reqBody map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Errorf("Failed to decode request body: %v", err)
		}
		assert.Equal(t, "stop", reqBody["action"])

		w.WriteHeader(http.StatusOK)
	})
	defer server.Close()

	err := manager.StopInstance(context.Background(), "instance-123")
	assert.NoError(t, err)
}

func TestComputeManager_CreateKeyPair(t *testing.T) {
	expectedKeyPair := &cpi.KeyPair{
		Name:       "test-key",
		PublicKey:  "ssh-rsa AAAAB3...",
		PrivateKey: "-----BEGIN RSA PRIVATE KEY-----",
	}

	manager, server := setupTestComputeManager(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/projects/test-project/keypairs", r.URL.Path)
		assert.Equal(t, "POST", r.Method)

		var reqBody map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Errorf("Failed to decode request body: %v", err)
		}
		assert.Equal(t, "test-key", reqBody["name"])

		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(expectedKeyPair)
	})
	defer server.Close()

	keyPair, err := manager.CreateKeyPair(context.Background(), "test-key")
	require.NoError(t, err)
	assert.Equal(t, expectedKeyPair.Name, keyPair.Name)
	assert.Equal(t, expectedKeyPair.PublicKey, keyPair.PublicKey)
}

func TestComputeManager_ImportKeyPair(t *testing.T) {
	manager, server := setupTestComputeManager(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/projects/test-project/keypairs", r.URL.Path)
		assert.Equal(t, "POST", r.Method)

		var reqBody map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Errorf("Failed to decode request body: %v", err)
		}
		assert.Equal(t, "test-key", reqBody["name"])
		assert.Equal(t, "ssh-rsa AAAAB3...", reqBody["public_key"])

		w.WriteHeader(http.StatusCreated)
	})
	defer server.Close()

	err := manager.ImportKeyPair(context.Background(), "test-key", "ssh-rsa AAAAB3...")
	assert.NoError(t, err)
}

func TestComputeManager_ListImages(t *testing.T) {
	expectedImages := []*cpi.Image{
		{
			ID:   "img-1",
			Name: "ubuntu-20.04",
		},
		{
			ID:   "img-2",
			Name: "ubuntu-22.04",
		},
	}

	manager, server := setupTestComputeManager(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/projects/test-project/images", r.URL.Path)
		assert.Equal(t, "GET", r.Method)

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"images": expectedImages,
		})
	})
	defer server.Close()

	images, err := manager.ListImages(context.Background(), nil)
	require.NoError(t, err)
	assert.Len(t, images, 2)
	assert.Equal(t, expectedImages[0].ID, images[0].ID)
}

func TestComputeManager_ListFlavors(t *testing.T) {
	expectedFlavors := []*cpi.Flavor{
		{
			ID:    "flv-1",
			Name:  "m1.small",
			VCPUs: 2,
			RAM:   4096,
			Disk:  40,
		},
		{
			ID:    "flv-2",
			Name:  "m1.large",
			VCPUs: 4,
			RAM:   8192,
			Disk:  80,
		},
	}

	manager, server := setupTestComputeManager(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/projects/test-project/flavors", r.URL.Path)
		assert.Equal(t, "GET", r.Method)

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"flavors": expectedFlavors,
		})
	})
	defer server.Close()

	flavors, err := manager.ListFlavors(context.Background())
	require.NoError(t, err)
	assert.Len(t, flavors, 2)
	assert.Equal(t, expectedFlavors[0].ID, flavors[0].ID)
	assert.Equal(t, expectedFlavors[1].RAM, flavors[1].RAM)
}

func TestComputeManager_buildCreateServerPayload_SecurityGroups(t *testing.T) {
	tests := []struct {
		name           string
		nicID          string
		securityGroups []string
		wantSGSet      bool
		description    string
	}{
		{
			name:           "DHCP path sets security groups",
			nicID:          "",
			securityGroups: []string{"sg-123", "sg-456"},
			wantSGSet:      true,
			description:    "When nicID is empty (DHCP), security groups should be set on server payload",
		},
		{
			name:           "NIC path does NOT set security groups",
			nicID:          "nic-789",
			securityGroups: []string{"sg-123", "sg-456"},
			wantSGSet:      false,
			description:    "When nicID is provided, security groups are on NIC, not server payload",
		},
		{
			name:           "DHCP path with no security groups",
			nicID:          "",
			securityGroups: []string{},
			wantSGSet:      false,
			description:    "When no security groups provided, payload should not have them set",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager, server := setupTestComputeManager(t, func(w http.ResponseWriter, r *http.Request) {})
			defer server.Close()

			req := &cpi.InstanceRequest{
				Name:             "test-instance",
				Flavor:           "m1.small",
				Image:            "ubuntu-20.04",
				NetworkID:        "net-123",
				SecurityGroupIDs: tt.securityGroups,
			}

			payload := manager.buildCreateServerPayload(req, tt.nicID)

			if tt.wantSGSet {
				require.NotNil(t, payload.SecurityGroups, tt.description)
				// Check that security groups were set correctly
				sgList := *payload.SecurityGroups
				assert.Equal(t, tt.securityGroups, sgList, "Security groups should match request")
			} else {
				if payload.SecurityGroups != nil {
					sgList := *payload.SecurityGroups
					assert.Empty(t, sgList, tt.description)
				}
			}
		})
	}
}
