package gcp

import (
	"context"
	"fmt"
	"strings"

	"cloud.google.com/go/compute/apiv1/computepb"
	"google.golang.org/api/iterator"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// CreateLoadBalancer creates a load balancer (forwarding rule + backend service).
func (m *LoadBalancerManager) CreateLoadBalancer(ctx context.Context, req *cpi.CreateLoadBalancerRequest) (*cpi.LoadBalancer, error) {
	if err := m.client.ensureClientsLoaded(ctx); err != nil {
		return nil, err
	}

	config := m.client.getConfig()
	projectID := config.ProjectID
	region := config.Region

	// Create health check first
	healthCheckName := fmt.Sprintf("%s-health", req.Name)
	healthCheck := &computepb.HealthCheck{
		Name:               proto(healthCheckName),
		Type:               proto("HTTP"),
		CheckIntervalSec:   proto(int32(10)),
		TimeoutSec:         proto(int32(5)),
		HealthyThreshold:   proto(int32(2)),
		UnhealthyThreshold: proto(int32(3)),
		HttpHealthCheck: &computepb.HTTPHealthCheck{
			Port:        proto(int32(80)),
			RequestPath: proto("/"),
		},
	}

	hcOp, err := m.client.getHealthChecksClient().Insert(ctx, &computepb.InsertHealthCheckRequest{
		Project:             projectID,
		HealthCheckResource: healthCheck,
	})
	if err != nil {
		return nil, WrapGCPError(err, "CreateLoadBalancer.HealthCheck")
	}

	if err := hcOp.Wait(ctx); err != nil {
		return nil, WrapGCPError(err, "CreateLoadBalancer.HealthCheck.Wait")
	}

	// Create backend service
	backendServiceName := fmt.Sprintf("%s-backend", req.Name)
	healthCheckURL := fmt.Sprintf("projects/%s/global/healthChecks/%s", projectID, healthCheckName)

	backendService := &computepb.BackendService{
		Name:                proto(backendServiceName),
		Protocol:            proto("HTTP"),
		PortName:            proto("http"),
		TimeoutSec:          proto(int32(30)),
		HealthChecks:        []string{healthCheckURL},
		LoadBalancingScheme: proto("EXTERNAL"),
	}

	bsOp, err := m.client.getBackendServicesClient().Insert(ctx, &computepb.InsertBackendServiceRequest{
		Project:                projectID,
		BackendServiceResource: backendService,
	})
	if err != nil {
		return nil, WrapGCPError(err, "CreateLoadBalancer.BackendService")
	}

	if err := bsOp.Wait(ctx); err != nil {
		return nil, WrapGCPError(err, "CreateLoadBalancer.BackendService.Wait")
	}

	// Create forwarding rule
	scheme := "EXTERNAL"
	if req.Scheme == "internal" {
		scheme = "INTERNAL"
	}

	forwardingRule := &computepb.ForwardingRule{
		Name:                proto(req.Name),
		LoadBalancingScheme: proto(scheme),
		PortRange:           proto("80-80"),
		Target:              proto(fmt.Sprintf("projects/%s/global/backendServices/%s", projectID, backendServiceName)),
	}

	frOp, err := m.client.getForwardingRulesClient().Insert(ctx, &computepb.InsertForwardingRuleRequest{
		Project:                projectID,
		Region:                 region,
		ForwardingRuleResource: forwardingRule,
	})
	if err != nil {
		return nil, WrapGCPError(err, "CreateLoadBalancer.ForwardingRule")
	}

	if err := frOp.Wait(ctx); err != nil {
		return nil, WrapGCPError(err, "CreateLoadBalancer.ForwardingRule.Wait")
	}

	logger.Debugw("Created load balancer", "name", req.Name, "region", region)

	return m.GetLoadBalancer(ctx, req.Name)
}

// GetLoadBalancer retrieves a load balancer by name.
func (m *LoadBalancerManager) GetLoadBalancer(ctx context.Context, id string) (*cpi.LoadBalancer, error) {
	if err := m.client.ensureClientsLoaded(ctx); err != nil {
		return nil, err
	}

	config := m.client.getConfig()
	projectID := config.ProjectID
	region := config.Region

	fr, err := m.client.getForwardingRulesClient().Get(ctx, &computepb.GetForwardingRuleRequest{
		Project:        projectID,
		Region:         region,
		ForwardingRule: id,
	})
	if err != nil {
		return nil, WrapGCPError(err, "GetLoadBalancer")
	}

	return m.convertForwardingRule(fr), nil
}

// ListLoadBalancers lists load balancers.
func (m *LoadBalancerManager) ListLoadBalancers(ctx context.Context, filters map[string]string) ([]*cpi.LoadBalancer, error) {
	if err := m.client.ensureClientsLoaded(ctx); err != nil {
		return nil, err
	}

	config := m.client.getConfig()
	projectID := config.ProjectID
	region := config.Region

	var loadBalancers []*cpi.LoadBalancer
	it := m.client.getForwardingRulesClient().List(ctx, &computepb.ListForwardingRulesRequest{
		Project: projectID,
		Region:  region,
	})

	for {
		fr, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, WrapGCPError(err, "ListLoadBalancers")
		}

		loadBalancers = append(loadBalancers, m.convertForwardingRule(fr))
	}

	return loadBalancers, nil
}

// UpdateLoadBalancer updates a load balancer.
func (m *LoadBalancerManager) UpdateLoadBalancer(ctx context.Context, id string, req *cpi.UpdateLoadBalancerRequest) error {
	// GCP load balancer updates typically require updating individual components
	// (backend service, health check, etc.)
	return fmt.Errorf("%w: UpdateLoadBalancer - update individual components", ErrNotImplemented)
}

// DeleteLoadBalancer deletes a load balancer.
func (m *LoadBalancerManager) DeleteLoadBalancer(ctx context.Context, id string) error {
	if err := m.client.ensureClientsLoaded(ctx); err != nil {
		return err
	}

	config := m.client.getConfig()
	projectID := config.ProjectID
	region := config.Region

	// Delete forwarding rule
	frOp, err := m.client.getForwardingRulesClient().Delete(ctx, &computepb.DeleteForwardingRuleRequest{
		Project:        projectID,
		Region:         region,
		ForwardingRule: id,
	})
	if err != nil {
		return WrapGCPError(err, "DeleteLoadBalancer.ForwardingRule")
	}

	if err := frOp.Wait(ctx); err != nil {
		return WrapGCPError(err, "DeleteLoadBalancer.ForwardingRule.Wait")
	}

	// Delete backend service
	backendServiceName := fmt.Sprintf("%s-backend", id)
	bsOp, err := m.client.getBackendServicesClient().Delete(ctx, &computepb.DeleteBackendServiceRequest{
		Project:        projectID,
		BackendService: backendServiceName,
	})
	if err != nil {
		// Log but don't fail if backend service doesn't exist
		logger.Warnw("Failed to delete backend service", "name", backendServiceName, "error", err)
	} else {
		if err := bsOp.Wait(ctx); err != nil {
			logger.Warnw("Failed waiting for backend service deletion", "name", backendServiceName, "error", err)
		}
	}

	// Delete health check
	healthCheckName := fmt.Sprintf("%s-health", id)
	hcOp, err := m.client.getHealthChecksClient().Delete(ctx, &computepb.DeleteHealthCheckRequest{
		Project:     projectID,
		HealthCheck: healthCheckName,
	})
	if err != nil {
		// Log but don't fail if health check doesn't exist
		logger.Warnw("Failed to delete health check", "name", healthCheckName, "error", err)
	} else {
		if err := hcOp.Wait(ctx); err != nil {
			logger.Warnw("Failed waiting for health check deletion", "name", healthCheckName, "error", err)
		}
	}

	logger.Debugw("Deleted load balancer", "name", id, "region", region)

	return nil
}

// AddBackend adds a backend to the load balancer.
func (m *LoadBalancerManager) AddBackend(ctx context.Context, lbID string, backend *cpi.Backend) error {
	if err := m.client.ensureClientsLoaded(ctx); err != nil {
		return err
	}

	config := m.client.getConfig()
	projectID := config.ProjectID

	backendServiceName := fmt.Sprintf("%s-backend", lbID)

	// Verify backend service exists
	_, err := m.client.getBackendServicesClient().Get(ctx, &computepb.GetBackendServiceRequest{
		Project:        projectID,
		BackendService: backendServiceName,
	})
	if err != nil {
		return WrapGCPError(err, "AddBackend.GetBackendService")
	}

	// Add new backend (instance group or NEG)
	// For simplicity, we'll use target pool pattern here
	// In production, you'd typically use instance groups
	logger.Debugw("AddBackend called - use instance groups for production", "lb", lbID, "backend", backend.Name)

	return fmt.Errorf("%w: AddBackend - configure instance groups", ErrNotImplemented)
}

// RemoveBackend removes a backend from the load balancer.
func (m *LoadBalancerManager) RemoveBackend(ctx context.Context, lbID string, backendID string) error {
	return fmt.Errorf("%w: RemoveBackend - configure instance groups", ErrNotImplemented)
}

// EnableBackend enables a backend.
func (m *LoadBalancerManager) EnableBackend(ctx context.Context, lbID string, backendID string) error {
	// GCP doesn't have explicit enable/disable - uses health checks
	return fmt.Errorf("%w: EnableBackend - managed by health checks", ErrNotImplemented)
}

// DisableBackend disables a backend.
func (m *LoadBalancerManager) DisableBackend(ctx context.Context, lbID string, backendID string) error {
	// GCP doesn't have explicit enable/disable - uses health checks
	return fmt.Errorf("%w: DisableBackend - managed by health checks", ErrNotImplemented)
}

// ConfigureHealthCheck configures a health check for the load balancer.
func (m *LoadBalancerManager) ConfigureHealthCheck(ctx context.Context, lbID string, check *cpi.HealthCheck) error {
	if err := m.client.ensureClientsLoaded(ctx); err != nil {
		return err
	}

	config := m.client.getConfig()
	projectID := config.ProjectID

	healthCheckName := fmt.Sprintf("%s-health", lbID)

	// Build health check based on protocol
	healthCheck := &computepb.HealthCheck{
		Name:               proto(healthCheckName),
		CheckIntervalSec:   proto(int32(check.Interval)),
		TimeoutSec:         proto(int32(check.Timeout)),
		HealthyThreshold:   proto(int32(check.HealthyThreshold)),
		UnhealthyThreshold: proto(int32(check.UnhealthyThreshold)),
	}

	switch strings.ToUpper(check.Protocol) {
	case "HTTP":
		healthCheck.Type = proto("HTTP")
		healthCheck.HttpHealthCheck = &computepb.HTTPHealthCheck{
			Port:        proto(int32(check.Port)),
			RequestPath: proto(check.Path),
		}
	case "HTTPS":
		healthCheck.Type = proto("HTTPS")
		healthCheck.HttpsHealthCheck = &computepb.HTTPSHealthCheck{
			Port:        proto(int32(check.Port)),
			RequestPath: proto(check.Path),
		}
	case "TCP":
		healthCheck.Type = proto("TCP")
		healthCheck.TcpHealthCheck = &computepb.TCPHealthCheck{
			Port: proto(int32(check.Port)),
		}
	default:
		healthCheck.Type = proto("HTTP")
		healthCheck.HttpHealthCheck = &computepb.HTTPHealthCheck{
			Port:        proto(int32(check.Port)),
			RequestPath: proto(check.Path),
		}
	}

	op, err := m.client.getHealthChecksClient().Update(ctx, &computepb.UpdateHealthCheckRequest{
		Project:             projectID,
		HealthCheck:         healthCheckName,
		HealthCheckResource: healthCheck,
	})
	if err != nil {
		return WrapGCPError(err, "ConfigureHealthCheck")
	}

	if err := op.Wait(ctx); err != nil {
		return WrapGCPError(err, "ConfigureHealthCheck.Wait")
	}

	logger.Debugw("Configured health check", "lb", lbID, "protocol", check.Protocol)

	return nil
}

// GetHealthStatus retrieves the health status of a load balancer.
func (m *LoadBalancerManager) GetHealthStatus(ctx context.Context, lbID string) (*cpi.HealthStatus, error) {
	if err := m.client.ensureClientsLoaded(ctx); err != nil {
		return nil, err
	}

	config := m.client.getConfig()
	projectID := config.ProjectID

	backendServiceName := fmt.Sprintf("%s-backend", lbID)

	// Get backend service health
	bs, err := m.client.getBackendServicesClient().Get(ctx, &computepb.GetBackendServiceRequest{
		Project:        projectID,
		BackendService: backendServiceName,
	})
	if err != nil {
		return nil, WrapGCPError(err, "GetHealthStatus")
	}

	// Count healthy/unhealthy backends
	status := &cpi.HealthStatus{
		LoadBalancerID: lbID,
		Healthy:        0,
		Unhealthy:      0,
		Total:          len(bs.GetBackends()),
		Backends:       make(map[string]string),
	}

	// In a full implementation, you'd query the health of each backend
	// using GetHealth on the backend service

	return status, nil
}

// Helper functions

func (m *LoadBalancerManager) convertForwardingRule(fr *computepb.ForwardingRule) *cpi.LoadBalancer {
	lbType := "external"
	if fr.GetLoadBalancingScheme() == "INTERNAL" {
		lbType = "internal"
	}

	return &cpi.LoadBalancer{
		ID:        fmt.Sprintf("%d", fr.GetId()),
		Name:      fr.GetName(),
		Type:      lbType,
		IPAddress: fr.GetIPAddress(),
		Status:    "active",
		State:     cpi.ResourceStateActive,
		CreatedAt: ParseTimestamp(fr.GetCreationTimestamp()),
	}
}
