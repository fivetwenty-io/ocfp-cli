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

func setupTestNetworkManager(t *testing.T, handler http.HandlerFunc) (*NetworkManager, *httptest.Server) {
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

	return client.network, server
}

func TestNetworkManager_CreateNetwork(t *testing.T) {
	expectedNetwork := &cpi.Network{
		ID:    "net-123",
		Name:  "test-network",
		CIDR:  "10.0.0.0/16",
		State: cpi.ResourceStateActive,
	}

	callCount := 0
	manager, server := setupTestNetworkManager(t, func(w http.ResponseWriter, r *http.Request) {
		callCount++

		switch callCount {
		case 1: // Create network call
			assert.Equal(t, "/v1/projects/test-project/networks", r.URL.Path)
			assert.Equal(t, "POST", r.Method)

			var reqBody map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&reqBody)
			assert.Equal(t, "test-network", reqBody["name"])
			assert.Equal(t, "10.0.0.0/16", reqBody["cidr"])

			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(expectedNetwork)

		case 2: // Get network status call
			assert.Equal(t, "/v1/projects/test-project/networks/net-123", r.URL.Path)
			assert.Equal(t, "GET", r.Method)

			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(expectedNetwork)
		}
	})
	defer server.Close()

	req := &cpi.CreateNetworkRequest{
		Name: "test-network",
		CIDR: "10.0.0.0/16",
		Tags: map[string]string{"env": "test"},
	}

	network, err := manager.CreateNetwork(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, expectedNetwork.ID, network.ID)
	assert.Equal(t, expectedNetwork.Name, network.Name)
	assert.Equal(t, expectedNetwork.CIDR, network.CIDR)
}

func TestNetworkManager_CreateSubnet(t *testing.T) {
	expectedSubnet := &cpi.Subnet{
		ID:        "subnet-123",
		Name:      "test-subnet",
		NetworkID: "net-123",
		CIDR:      "10.0.1.0/24",
		State:     cpi.ResourceStateActive,
	}

	callCount := 0
	manager, server := setupTestNetworkManager(t, func(w http.ResponseWriter, r *http.Request) {
		callCount++

		switch callCount {
		case 1: // Create subnet call
			assert.Equal(t, "/v1/projects/test-project/subnets", r.URL.Path)
			assert.Equal(t, "POST", r.Method)

			var reqBody map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&reqBody)
			assert.Equal(t, "test-subnet", reqBody["name"])
			assert.Equal(t, "10.0.1.0/24", reqBody["cidr"])
			assert.Equal(t, "net-123", reqBody["network_id"])

			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(expectedSubnet)

		case 2: // Get subnet status call
			assert.Equal(t, "/v1/projects/test-project/subnets/subnet-123", r.URL.Path)
			assert.Equal(t, "GET", r.Method)

			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(expectedSubnet)
		}
	})
	defer server.Close()

	req := &cpi.CreateSubnetRequest{
		Name:             "test-subnet",
		NetworkID:        "net-123",
		CIDR:             "10.0.1.0/24",
		AvailabilityZone: "eu01a",
		Tags:             map[string]string{"type": "private"},
	}

	subnet, err := manager.CreateSubnet(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, expectedSubnet.ID, subnet.ID)
	assert.Equal(t, expectedSubnet.Name, subnet.Name)
	assert.Equal(t, expectedSubnet.CIDR, subnet.CIDR)
}

func TestNetworkManager_AllocateFloatingIP(t *testing.T) {
	expectedFloatingIP := &cpi.FloatingIP{
		ID:      "fip-123",
		Address: "1.2.3.4",
		Status:  "available",
	}

	manager, server := setupTestNetworkManager(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/projects/test-project/floating-ips", r.URL.Path)
		assert.Equal(t, "POST", r.Method)

		var reqBody map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&reqBody)
		assert.Equal(t, "net-123", reqBody["network_id"])

		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(expectedFloatingIP)
	})
	defer server.Close()

	req := &cpi.AllocateFloatingIPRequest{
		NetworkID: "net-123",
		Tags:      map[string]string{"purpose": "bastion"},
	}

	floatingIP, err := manager.AllocateFloatingIP(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, expectedFloatingIP.ID, floatingIP.ID)
	assert.Equal(t, expectedFloatingIP.Address, floatingIP.Address)
}

func TestNetworkManager_AssociateFloatingIP(t *testing.T) {
	manager, server := setupTestNetworkManager(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/projects/test-project/floating-ips/fip-123/associate", r.URL.Path)
		assert.Equal(t, "POST", r.Method)

		var reqBody map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&reqBody)
		assert.Equal(t, "instance-123", reqBody["instance_id"])

		w.WriteHeader(http.StatusOK)
	})
	defer server.Close()

	err := manager.AssociateFloatingIP(context.Background(), "fip-123", "instance-123")
	assert.NoError(t, err)
}

func TestNetworkManager_DisassociateFloatingIP(t *testing.T) {
	manager, server := setupTestNetworkManager(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/projects/test-project/floating-ips/fip-123/disassociate", r.URL.Path)
		assert.Equal(t, "POST", r.Method)

		w.WriteHeader(http.StatusOK)
	})
	defer server.Close()

	err := manager.DisassociateFloatingIP(context.Background(), "fip-123")
	assert.NoError(t, err)
}

func TestNetworkManager_CreateRouter(t *testing.T) {
	expectedRouter := &cpi.Router{
		ID:    "router-123",
		Name:  "test-router",
		State: cpi.ResourceStateActive,
	}

	callCount := 0
	manager, server := setupTestNetworkManager(t, func(w http.ResponseWriter, r *http.Request) {
		callCount++

		switch callCount {
		case 1: // Create router call
			assert.Equal(t, "/v1/projects/test-project/routers", r.URL.Path)
			assert.Equal(t, "POST", r.Method)

			var reqBody map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&reqBody)
			assert.Equal(t, "test-router", reqBody["name"])

			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(expectedRouter)

		case 2: // Get router status call
			assert.Equal(t, "/v1/projects/test-project/routers/router-123", r.URL.Path)
			assert.Equal(t, "GET", r.Method)

			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(expectedRouter)
		}
	})
	defer server.Close()

	req := &cpi.CreateRouterRequest{
		Name:            "test-router",
		NetworkID:       "net-123",
		ExternalGateway: "ext-net",
		Tags:            map[string]string{"env": "test"},
	}

	router, err := manager.CreateRouter(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, expectedRouter.ID, router.ID)
	assert.Equal(t, expectedRouter.Name, router.Name)
}

func TestNetworkManager_AttachRouterInterface(t *testing.T) {
	manager, server := setupTestNetworkManager(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/projects/test-project/routers/router-123/attach-interface", r.URL.Path)
		assert.Equal(t, "POST", r.Method)

		var reqBody map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&reqBody)
		assert.Equal(t, "subnet-123", reqBody["subnet_id"])

		w.WriteHeader(http.StatusOK)
	})
	defer server.Close()

	err := manager.AttachRouterInterface(context.Background(), "router-123", "subnet-123")
	assert.NoError(t, err)
}

func TestNetworkManager_DetachRouterInterface(t *testing.T) {
	manager, server := setupTestNetworkManager(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/projects/test-project/routers/router-123/detach-interface", r.URL.Path)
		assert.Equal(t, "POST", r.Method)

		var reqBody map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&reqBody)
		assert.Equal(t, "subnet-123", reqBody["subnet_id"])

		w.WriteHeader(http.StatusOK)
	})
	defer server.Close()

	err := manager.DetachRouterInterface(context.Background(), "router-123", "subnet-123")
	assert.NoError(t, err)
}

func TestNetworkManager_ListNetworks(t *testing.T) {
	expectedNetworks := []*cpi.Network{
		{
			ID:   "net-1",
			Name: "network-1",
			CIDR: "10.0.0.0/16",
		},
		{
			ID:   "net-2",
			Name: "network-2",
			CIDR: "10.1.0.0/16",
		},
	}

	manager, server := setupTestNetworkManager(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/projects/test-project/networks", r.URL.Path)
		assert.Equal(t, "GET", r.Method)

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"networks": expectedNetworks,
		})
	})
	defer server.Close()

	networks, err := manager.ListNetworks(context.Background(), nil)
	require.NoError(t, err)
	assert.Len(t, networks, 2)
	assert.Equal(t, expectedNetworks[0].ID, networks[0].ID)
	assert.Equal(t, expectedNetworks[1].CIDR, networks[1].CIDR)
}

func TestNetworkManager_DeleteNetwork(t *testing.T) {
	manager, server := setupTestNetworkManager(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/projects/test-project/networks/net-123", r.URL.Path)
		assert.Equal(t, "DELETE", r.Method)

		w.WriteHeader(http.StatusNoContent)
	})
	defer server.Close()

	err := manager.DeleteNetwork(context.Background(), "net-123")
	assert.NoError(t, err)
}

func TestNetworkManager_DeleteNetwork_NotFound(t *testing.T) {
	manager, server := setupTestNetworkManager(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	defer server.Close()

	// Should not return error for not found (idempotent)
	err := manager.DeleteNetwork(context.Background(), "net-123")
	assert.NoError(t, err)
}
