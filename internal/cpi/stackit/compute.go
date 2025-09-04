package stackit

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// CreateInstance creates a new compute instance
func (m *ComputeManager) CreateInstance(ctx context.Context, req *cpi.CreateInstanceRequest) (*cpi.Instance, error) {
	logger.WithOperation("CreateInstance").Infof("Creating instance: %s", req.Name)

	// Prepare API request
	apiReq := map[string]interface{}{
		"name":              req.Name,
		"flavor":            req.Flavor,
		"image":             req.Image,
		"network_id":        req.NetworkID,
		"security_groups":   req.SecurityGroups,
		"key_name":          req.KeyPair,
		"user_data":         req.UserData,
		"availability_zone": req.AvailabilityZone,
		"labels":            req.Tags,
	}
	// For STACKIT, subnet_id is optional; only include if provided
	if req.SubnetID != "" {
		apiReq["subnet_id"] = req.SubnetID
	}

	httpReq, err := m.client.newRequest(ctx, "POST", "/v1/projects/"+m.client.config.ProjectID+"/instances", apiReq)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := m.client.do(ctx, httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to create instance: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated {
		return nil, m.client.parseError(resp)
	}

	// Parse response
	var instance cpi.Instance
	if err := json.NewDecoder(resp.Body).Decode(&instance); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	// Wait for instance to be active
	if err := m.waitForInstanceState(ctx, instance.ID, cpi.ResourceStateActive, 5*time.Minute); err != nil {
		return nil, fmt.Errorf("instance failed to become active: %w", err)
	}

	logger.WithOperation("CreateInstance").Infof("Instance created: %s (%s)", instance.Name, instance.ID)
	return &instance, nil
}

// GetInstance retrieves an instance by ID
func (m *ComputeManager) GetInstance(ctx context.Context, id string) (*cpi.Instance, error) {
	logger.WithOperation("GetInstance").Debugf("Getting instance: %s", id)

	httpReq, err := m.client.newRequest(ctx, "GET", "/v1/projects/"+m.client.config.ProjectID+"/instances/"+id, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := m.client.do(ctx, httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to get instance: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil, &cpi.ProviderError{
			Provider: "stackit",
			Code:     "NotFound",
			Message:  fmt.Sprintf("Instance %s not found", id),
		}
	}

	if resp.StatusCode != http.StatusOK {
		return nil, m.client.parseError(resp)
	}

	var instance cpi.Instance
	if err := json.NewDecoder(resp.Body).Decode(&instance); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &instance, nil
}

// ListInstances lists all instances
func (m *ComputeManager) ListInstances(ctx context.Context, filters map[string]string) ([]*cpi.Instance, error) {
	logger.WithOperation("ListInstances").Debug("Listing instances")

	// Build query parameters
	query := "?"
	for k, v := range filters {
		query += fmt.Sprintf("%s=%s&", k, v)
	}

	httpReq, err := m.client.newRequest(ctx, "GET", "/v1/projects/"+m.client.config.ProjectID+"/instances"+query, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := m.client.do(ctx, httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to list instances: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, m.client.parseError(resp)
	}

	var result struct {
		Instances []*cpi.Instance `json:"instances"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	logger.WithOperation("ListInstances").Debugf("Found %d instances", len(result.Instances))
	return result.Instances, nil
}

// DeleteInstance deletes an instance
func (m *ComputeManager) DeleteInstance(ctx context.Context, id string) error {
	logger.WithOperation("DeleteInstance").Infof("Deleting instance: %s", id)

	httpReq, err := m.client.newRequest(ctx, "DELETE", "/v1/projects/"+m.client.config.ProjectID+"/instances/"+id, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := m.client.do(ctx, httpReq)
	if err != nil {
		return fmt.Errorf("failed to delete instance: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil // Already deleted
	}

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return m.client.parseError(resp)
	}

	logger.WithOperation("DeleteInstance").Infof("Instance deleted: %s", id)
	return nil
}

// CreateKeyPair creates a new SSH key pair
func (m *ComputeManager) CreateKeyPair(ctx context.Context, name string) (*cpi.KeyPair, error) {
	logger.WithOperation("CreateKeyPair").Infof("Creating key pair: %s", name)

	apiReq := map[string]interface{}{
		"name": name,
	}

	httpReq, err := m.client.newRequest(ctx, "POST", "/v1/projects/"+m.client.config.ProjectID+"/keypairs", apiReq)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := m.client.do(ctx, httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to create key pair: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated {
		return nil, m.client.parseError(resp)
	}

	var keyPair cpi.KeyPair
	if err := json.NewDecoder(resp.Body).Decode(&keyPair); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	logger.WithOperation("CreateKeyPair").Infof("Key pair created: %s", name)
	return &keyPair, nil
}

// ImportKeyPair imports an existing public key
func (m *ComputeManager) ImportKeyPair(ctx context.Context, name string, publicKey string) error {
	logger.WithOperation("ImportKeyPair").Infof("Importing key pair: %s", name)

	apiReq := map[string]interface{}{
		"name":       name,
		"public_key": publicKey,
	}

	httpReq, err := m.client.newRequest(ctx, "POST", "/v1/projects/"+m.client.config.ProjectID+"/keypairs", apiReq)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := m.client.do(ctx, httpReq)
	if err != nil {
		return fmt.Errorf("failed to import key pair: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return m.client.parseError(resp)
	}

	logger.WithOperation("ImportKeyPair").Infof("Key pair imported: %s", name)
	return nil
}

// GetKeyPair retrieves a key pair by name
func (m *ComputeManager) GetKeyPair(ctx context.Context, name string) (*cpi.KeyPair, error) {
	logger.WithOperation("GetKeyPair").Debugf("Getting key pair: %s", name)

	httpReq, err := m.client.newRequest(ctx, "GET", "/v1/projects/"+m.client.config.ProjectID+"/keypairs/"+name, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := m.client.do(ctx, httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to get key pair: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil, &cpi.ProviderError{
			Provider: "stackit",
			Code:     "NotFound",
			Message:  fmt.Sprintf("Key pair %s not found", name),
		}
	}

	if resp.StatusCode != http.StatusOK {
		return nil, m.client.parseError(resp)
	}

	var keyPair cpi.KeyPair
	if err := json.NewDecoder(resp.Body).Decode(&keyPair); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &keyPair, nil
}

// ListKeyPairs lists all key pairs
func (m *ComputeManager) ListKeyPairs(ctx context.Context) ([]*cpi.KeyPair, error) {
	logger.WithOperation("ListKeyPairs").Debug("Listing key pairs")

	httpReq, err := m.client.newRequest(ctx, "GET", "/v1/projects/"+m.client.config.ProjectID+"/keypairs", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := m.client.do(ctx, httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to list key pairs: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, m.client.parseError(resp)
	}

	var result struct {
		KeyPairs []*cpi.KeyPair `json:"keypairs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return result.KeyPairs, nil
}

// DeleteKeyPair deletes a key pair
func (m *ComputeManager) DeleteKeyPair(ctx context.Context, name string) error {
	logger.WithOperation("DeleteKeyPair").Infof("Deleting key pair: %s", name)

	httpReq, err := m.client.newRequest(ctx, "DELETE", "/v1/projects/"+m.client.config.ProjectID+"/keypairs/"+name, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := m.client.do(ctx, httpReq)
	if err != nil {
		return fmt.Errorf("failed to delete key pair: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil // Already deleted
	}

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return m.client.parseError(resp)
	}

	logger.WithOperation("DeleteKeyPair").Infof("Key pair deleted: %s", name)
	return nil
}

// waitForInstanceState waits for an instance to reach a specific state
func (m *ComputeManager) waitForInstanceState(ctx context.Context, id string, targetState cpi.ResourceState, timeout time.Duration) error {
	return cpi.WaitForCondition(ctx, 5*time.Second, timeout, func() (bool, error) {
		instance, err := m.GetInstance(ctx, id)
		if err != nil {
			return false, err
		}
		return instance.State == targetState, nil
	})
}

// StartInstance starts a stopped instance
func (m *ComputeManager) StartInstance(ctx context.Context, id string) error {
	logger.WithOperation("StartInstance").Infof("Starting instance: %s", id)

	apiReq := map[string]interface{}{
		"action": "start",
	}

	httpReq, err := m.client.newRequest(ctx, "POST", fmt.Sprintf("/v1/projects/%s/instances/%s/action", m.client.config.ProjectID, id), apiReq)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := m.client.do(ctx, httpReq)
	if err != nil {
		return fmt.Errorf("failed to start instance: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return m.client.parseError(resp)
	}

	return nil
}

// StopInstance stops a running instance
func (m *ComputeManager) StopInstance(ctx context.Context, id string) error {
	logger.WithOperation("StopInstance").Infof("Stopping instance: %s", id)

	apiReq := map[string]interface{}{
		"action": "stop",
	}

	httpReq, err := m.client.newRequest(ctx, "POST", fmt.Sprintf("/v1/projects/%s/instances/%s/action", m.client.config.ProjectID, id), apiReq)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := m.client.do(ctx, httpReq)
	if err != nil {
		return fmt.Errorf("failed to stop instance: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return m.client.parseError(resp)
	}

	return nil
}

// RebootInstance reboots an instance
func (m *ComputeManager) RebootInstance(ctx context.Context, id string) error {
	logger.WithOperation("RebootInstance").Infof("Rebooting instance: %s", id)

	apiReq := map[string]interface{}{
		"action": "reboot",
	}

	httpReq, err := m.client.newRequest(ctx, "POST", fmt.Sprintf("/v1/projects/%s/instances/%s/action", m.client.config.ProjectID, id), apiReq)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := m.client.do(ctx, httpReq)
	if err != nil {
		return fmt.Errorf("failed to reboot instance: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return m.client.parseError(resp)
	}

	return nil
}

// ListImages lists available images
func (m *ComputeManager) ListImages(ctx context.Context, filters map[string]string) ([]*cpi.Image, error) {
	logger.WithOperation("ListImages").Debug("Listing images")

	query := "?"
	for k, v := range filters {
		query += fmt.Sprintf("%s=%s&", k, v)
	}

	httpReq, err := m.client.newRequest(ctx, "GET", "/v1/projects/"+m.client.config.ProjectID+"/images"+query, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := m.client.do(ctx, httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to list images: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, m.client.parseError(resp)
	}

	var result struct {
		Images []*cpi.Image `json:"images"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	logger.WithOperation("ListImages").Debugf("Found %d images", len(result.Images))
	return result.Images, nil
}

// GetImage retrieves an image
func (m *ComputeManager) GetImage(ctx context.Context, id string) (*cpi.Image, error) {
	logger.WithOperation("GetImage").Debugf("Getting image: %s", id)

	httpReq, err := m.client.newRequest(ctx, "GET", "/v1/projects/"+m.client.config.ProjectID+"/images/"+id, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := m.client.do(ctx, httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to get image: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil, &cpi.ProviderError{
			Provider: "stackit",
			Code:     "NotFound",
			Message:  fmt.Sprintf("Image %s not found", id),
		}
	}

	if resp.StatusCode != http.StatusOK {
		return nil, m.client.parseError(resp)
	}

	var image cpi.Image
	if err := json.NewDecoder(resp.Body).Decode(&image); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &image, nil
}

// ListFlavors lists available flavors
func (m *ComputeManager) ListFlavors(ctx context.Context) ([]*cpi.Flavor, error) {
	logger.WithOperation("ListFlavors").Debug("Listing flavors")

	httpReq, err := m.client.newRequest(ctx, "GET", "/v1/projects/"+m.client.config.ProjectID+"/flavors", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := m.client.do(ctx, httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to list flavors: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, m.client.parseError(resp)
	}

	var result struct {
		Flavors []*cpi.Flavor `json:"flavors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	logger.WithOperation("ListFlavors").Debugf("Found %d flavors", len(result.Flavors))
	return result.Flavors, nil
}

// GetFlavor retrieves a flavor
func (m *ComputeManager) GetFlavor(ctx context.Context, id string) (*cpi.Flavor, error) {
	logger.WithOperation("GetFlavor").Debugf("Getting flavor: %s", id)

	httpReq, err := m.client.newRequest(ctx, "GET", "/v1/projects/"+m.client.config.ProjectID+"/flavors/"+id, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := m.client.do(ctx, httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to get flavor: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil, &cpi.ProviderError{
			Provider: "stackit",
			Code:     "NotFound",
			Message:  fmt.Sprintf("Flavor %s not found", id),
		}
	}

	if resp.StatusCode != http.StatusOK {
		return nil, m.client.parseError(resp)
	}

	var flavor cpi.Flavor
	if err := json.NewDecoder(resp.Body).Decode(&flavor); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &flavor, nil
}
