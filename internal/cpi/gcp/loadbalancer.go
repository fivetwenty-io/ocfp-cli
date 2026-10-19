package gcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"cloud.google.com/go/compute/apiv1/computepb"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"google.golang.org/api/iterator"
)

const lbOwnerVersion = "ocfp-lb-v1"

type lbOwnership struct {
	Managed   string                         `json:"managed"`
	Name      string                         `json:"name"`
	Component string                         `json:"component"`
	Request   *cpi.CreateLoadBalancerRequest `json:"request,omitempty"`
	Endpoint  *lbEndpoint                    `json:"endpoint,omitempty"`
}

func lbDescription(name, component string, req *cpi.CreateLoadBalancerRequest, endpoint *lbEndpoint) string {
	data, err := json.Marshal(lbOwnership{Managed: lbOwnerVersion, Name: name, Component: component, Request: req, Endpoint: endpoint})
	if err != nil {
		panic(err)
	} // Struct contains only JSON-compatible fields.

	return string(data)
}
func parseLBOwner(description, name, component string) (lbOwnership, error) {
	var owner lbOwnership
	if err := json.Unmarshal([]byte(description), &owner); err != nil {
		return owner, fmt.Errorf("unmanaged load-balancer resource: %w", err)
	}

	if owner.Managed != lbOwnerVersion || owner.Name != name || owner.Component != component {
		return owner, errors.New("load-balancer resource ownership mismatch")
	}

	return owner, nil
}
func lbName(id string) string { return ExtractNameFromURL(id) }
func (m *LoadBalancerManager) lbURL(kind, name string) string {
	return fmt.Sprintf("projects/%s/global/%s/%s", m.client.getConfig().ProjectID, kind, name)
}
func normalizeLBRequest(req *cpi.CreateLoadBalancerRequest) (*cpi.CreateLoadBalancerRequest, error) {
	if req == nil {
		return nil, errors.New("load-balancer request is required")
	}

	copyReq := *req

	copyReq.Protocol = strings.ToLower(req.Protocol)
	if !regexp.MustCompile(`^[a-z][a-z0-9-]{0,42}[a-z0-9]$`).MatchString(req.Name) {
		return nil, errors.New("load-balancer name must be 2-44 lowercase letters, digits or hyphens")
	}

	if err := validateLBMode(&copyReq); err != nil {
		return nil, err
	}

	if req.Port < 1 || req.Port > 65535 {
		return nil, errors.New("listener port must be 1-65535")
	}

	if copyReq.TargetPort == 0 {
		copyReq.TargetPort = req.Port
	}

	if copyReq.TargetPort < 1 || copyReq.TargetPort > 65535 {
		return nil, errors.New("target port must be 1-65535")
	}

	copyReq.Scheme = "internet-facing"

	copyReq.Algorithm = "round-robin"
	if copyReq.Protocol == "http" {
		copyReq.Type = "application"
	} else {
		copyReq.Type = "network"
	}

	check, err := normalizeLBHealth(req.HealthCheck, copyReq.Protocol, copyReq.TargetPort)
	if err != nil {
		return nil, err
	}

	copyReq.HealthCheck = check
	if len(lbDescription(req.Name, "backendServices", &copyReq, nil)) > 2048 {
		return nil, errors.New("load-balancer metadata exceeds GCP description limit")
	}

	return &copyReq, nil
}
func validateLBMode(req *cpi.CreateLoadBalancerRequest) error {
	if req.Scheme != "" && req.Scheme != "internet-facing" && req.Scheme != "external" {
		return fmt.Errorf("%w: GCP internal proxy load balancing requires a proxy-only subnet", ErrNotImplemented)
	}

	if req.Protocol != "http" && req.Protocol != "tcp" {
		return fmt.Errorf("%w: supported GCP listener protocols are http and tcp; HTTPS termination requires certificates", ErrNotImplemented)
	}

	if req.Type != "" && req.Type != "application" && req.Type != "network" && req.Type != "external" {
		return errors.New("invalid load-balancer type")
	}

	if req.Algorithm != "" && req.Algorithm != "round-robin" {
		return fmt.Errorf("%w: GCP proxy load balancing uses native capacity distribution", ErrNotImplemented)
	}

	if len(req.SecurityGroups) > 0 {
		return fmt.Errorf("%w: GCP proxy frontends do not accept instance security groups", ErrNotImplemented)
	}

	return nil
}
func normalizeLBHealth(check *cpi.HealthCheck, protocol string, port int) (*cpi.HealthCheck, error) {
	result := defaultLBHealth(check, protocol, port)
	if result.Protocol != "tcp" && result.Protocol != "http" && result.Protocol != "https" {
		return nil, errors.New("unsupported health protocol")
	}

	if result.Port < 1 || result.Port > 65535 || result.Interval < 1 || result.Interval > 300 || result.Timeout < 1 || result.Timeout > result.Interval || result.HealthyThreshold < 1 || result.HealthyThreshold > 10 || result.UnhealthyThreshold < 1 || result.UnhealthyThreshold > 10 {
		return nil, errors.New("invalid health-check timing, threshold or port")
	}

	return &result, nil
}
func defaultLBHealth(check *cpi.HealthCheck, protocol string, port int) cpi.HealthCheck {
	result := cpi.HealthCheck{Protocol: protocol, Port: port, Path: "/", Interval: 10, Timeout: 5, HealthyThreshold: 2, UnhealthyThreshold: 2}
	if check != nil {
		result = *check
		if result.Protocol == "" {
			result.Protocol = protocol
		}

		if result.Port == 0 {
			result.Port = port
		}

		if result.Path == "" {
			result.Path = "/"
		}

		if result.Interval == 0 {
			result.Interval = 10
		}

		if result.Timeout == 0 {
			result.Timeout = 5
		}

		if result.HealthyThreshold == 0 {
			result.HealthyThreshold = 2
		}

		if result.UnhealthyThreshold == 0 {
			result.UnhealthyThreshold = 2
		}
	}

	return result
}

//nolint:gosec // Health values are bounded by normalizeLBHealth before conversion.
func (m *LoadBalancerManager) healthResource(req *cpi.CreateLoadBalancerRequest) *computepb.HealthCheck {
	check := req.HealthCheck

	health := &computepb.HealthCheck{Name: proto(req.Name + "-hc"), Description: proto(lbDescription(req.Name, "healthChecks", nil, nil)), Type: proto(strings.ToUpper(check.Protocol)), CheckIntervalSec: proto(int32(check.Interval)), TimeoutSec: proto(int32(check.Timeout)), HealthyThreshold: proto(int32(check.HealthyThreshold)), UnhealthyThreshold: proto(int32(check.UnhealthyThreshold))}
	switch check.Protocol {
	case "http":
		health.HttpHealthCheck = &computepb.HTTPHealthCheck{Port: proto(int32(check.Port)), RequestPath: proto(check.Path)}
	case "https":
		health.HttpsHealthCheck = &computepb.HTTPSHealthCheck{Port: proto(int32(check.Port)), RequestPath: proto(check.Path)}
	default:
		health.TcpHealthCheck = &computepb.TCPHealthCheck{Port: proto(int32(check.Port))}
	}

	return health
}
func (m *LoadBalancerManager) graph(req *cpi.CreateLoadBalancerRequest) []lbNode {
	name := req.Name
	nodes := []lbNode{m.nodeHealthCheck(m.healthResource(req)), m.nodeBackendService(&computepb.BackendService{Name: proto(name + "-bs"), Description: proto(lbDescription(name, "backendServices", req, nil)), Protocol: proto(strings.ToUpper(req.Protocol)), LoadBalancingScheme: proto("EXTERNAL_MANAGED"), HealthChecks: []string{m.lbURL("healthChecks", name+"-hc")}, TimeoutSec: proto(int32(30))})}

	proxyKind := "targetTcpProxies"
	if req.Protocol == "http" {
		proxyKind = "targetHttpProxies"
		nodes = append(nodes, m.nodeUrlMap(&computepb.UrlMap{Name: proto(name + "-url"), Description: proto(lbDescription(name, "urlMaps", nil, nil)), DefaultService: proto(m.lbURL("backendServices", name+"-bs"))}), m.nodeTargetHttpProxy(&computepb.TargetHttpProxy{Name: proto(name + "-proxy"), Description: proto(lbDescription(name, proxyKind, nil, nil)), UrlMap: proto(m.lbURL("urlMaps", name+"-url"))}))
	} else {
		nodes = append(nodes, m.nodeTargetTcpProxy(&computepb.TargetTcpProxy{Name: proto(name + "-proxy"), Description: proto(lbDescription(name, proxyKind, nil, nil)), Service: proto(m.lbURL("backendServices", name+"-bs")), ProxyHeader: proto("NONE")}))
	}

	nodes = append(nodes, m.nodeAddress(&computepb.Address{Name: proto(name + "-ip"), Description: proto(lbDescription(name, "addresses", nil, nil)), AddressType: proto("EXTERNAL"), IpVersion: proto("IPV4"), NetworkTier: proto("PREMIUM")}), m.nodeForwardingRule(&computepb.ForwardingRule{Name: proto(name), Description: proto(lbDescription(name, "forwardingRules", nil, nil)), LoadBalancingScheme: proto("EXTERNAL_MANAGED"), Target: proto(m.lbURL(proxyKind, name+"-proxy")), IPAddress: proto(m.lbURL("addresses", name+"-ip")), IPProtocol: proto("TCP"), PortRange: proto(strconv.Itoa(req.Port)), NetworkTier: proto("PREMIUM")}))

	return nodes
}
func (m *LoadBalancerManager) CreateLoadBalancer(ctx context.Context, request *cpi.CreateLoadBalancerRequest) (*cpi.LoadBalancer, error) {
	req, err := normalizeLBRequest(request)
	if err != nil {
		return nil, err
	}

	if err = m.client.ensureClientsLoaded(ctx); err != nil {
		return nil, err
	}

	if req.NetworkID == "" {
		req.NetworkID = m.client.getConfig().NetworkName
	}

	if req.NetworkID == "" {
		return nil, errors.New("GCP load-balancer network is required")
	}

	nodes := m.graph(req)
	missing := []lbNode{}

	for _, node := range nodes {
		actual, getErr := node.read(ctx)
		if IsNotFound(WrapGCPError(getErr, "LoadBalancer.Get")) {
			missing = append(missing, node)

			continue
		}

		if getErr != nil {
			return nil, WrapGCPError(getErr, "LoadBalancer.Get")
		}

		if _, err = parseLBOwner(lbDescriptionOf(actual), req.Name, node.kind); err != nil {
			return nil, err
		}

		if !lbResourceMatches(actual, node.want) {
			return nil, fmt.Errorf("existing %s/%s differs from requested configuration", node.kind, node.name)
		}
	}

	created := []lbNode{}

	for _, node := range missing {
		op, insertErr := node.insert(ctx)
		if insertErr == nil {
			created = append(created, node)
		}

		if err = waitLBOperation(ctx, op, insertErr); err != nil {
			return &cpi.LoadBalancer{ID: m.lbURL("forwardingRules", req.Name), Name: req.Name}, errors.Join(err, rollbackLBNodes(ctx, created))
		}
	}

	return m.GetLoadBalancer(ctx, req.Name)
}
func rollbackLBNodes(ctx context.Context, nodes []lbNode) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()

	var errs []error

	for i := len(nodes) - 1; i >= 0; i-- {
		if err := nodes[i].delete(cleanup); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}
func (m *LoadBalancerManager) loadLB(ctx context.Context, id string) (*computepb.BackendService, *cpi.CreateLoadBalancerRequest, error) {
	if err := m.client.ensureClientsLoaded(ctx); err != nil {
		return nil, nil, err
	}

	name := lbName(id)

	project, _, _ := resourceScope(m.client.getConfig().ProjectID, "", id)
	if project != m.client.getConfig().ProjectID || (strings.Contains(id, "/") && !strings.Contains(id, "/global/forwardingRules/")) {
		return nil, nil, errors.New("load-balancer handle has foreign project or scope")
	}

	backend, err := m.client.backendServicesClient.Get(ctx, &computepb.GetBackendServiceRequest{Project: m.client.getConfig().ProjectID, BackendService: name + "-bs"})
	if err != nil {
		return nil, nil, WrapGCPError(err, "LoadBalancer.Get")
	}

	owner, err := parseLBOwner(backend.GetDescription(), name, "backendServices")
	if err != nil {
		return nil, nil, err
	}

	if owner.Request == nil {
		return nil, nil, errors.New("load-balancer configuration missing")
	}

	if owner.Request.Name != name {
		return nil, nil, errors.New("load-balancer metadata name mismatch")
	}

	spec, err := normalizeLBRequest(owner.Request)
	if err != nil {
		return nil, nil, err
	}

	return backend, spec, nil
}
func (m *LoadBalancerManager) GetLoadBalancer(ctx context.Context, id string) (*cpi.LoadBalancer, error) {
	backend, req, err := m.loadLB(ctx, id)
	if err != nil {
		return nil, err
	}

	forwarding, err := m.client.globalForwardingRulesClient.Get(ctx, &computepb.GetGlobalForwardingRuleRequest{Project: m.client.getConfig().ProjectID, ForwardingRule: req.Name})
	if err != nil {
		return nil, WrapGCPError(err, "LoadBalancer.GetFrontend")
	}

	if _, err = parseLBOwner(forwarding.GetDescription(), req.Name, "forwardingRules"); err != nil {
		return nil, err
	}

	address, err := m.client.globalAddressesClient.Get(ctx, &computepb.GetGlobalAddressRequest{Project: m.client.getConfig().ProjectID, Address: req.Name + "-ip"})
	if err != nil {
		return nil, WrapGCPError(err, "LoadBalancer.GetAddress")
	}

	if _, err = parseLBOwner(address.GetDescription(), req.Name, "addresses"); err != nil {
		return nil, err
	}

	result := &cpi.LoadBalancer{ID: m.lbURL("forwardingRules", req.Name), Name: req.Name, Type: "external", Protocol: req.Protocol, Port: req.Port, TargetPort: req.TargetPort, Algorithm: req.Algorithm, NetworkID: req.NetworkID, SubnetIDs: req.SubnetIDs, IPAddress: address.GetAddress(), HealthCheck: req.HealthCheck, State: cpi.ResourceStateActive, Status: "active", CreatedAt: ParseTimestamp(backend.GetCreationTimestamp())}
	for k, v := range req.Tags {
		result.Tags = append(result.Tags, k+"="+v)
	}

	return result, nil
}
func (m *LoadBalancerManager) ListLoadBalancers(ctx context.Context, filters map[string]string) ([]*cpi.LoadBalancer, error) {
	if err := m.client.ensureClientsLoaded(ctx); err != nil {
		return nil, err
	}

	it := m.client.backendServicesClient.List(ctx, &computepb.ListBackendServicesRequest{Project: m.client.getConfig().ProjectID})
	result := []*cpi.LoadBalancer{}

	for {
		service, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}

		if err != nil {
			return nil, WrapGCPError(err, "LoadBalancer.List")
		}

		name := strings.TrimSuffix(service.GetName(), "-bs")

		owner, err := parseLBOwner(service.GetDescription(), name, "backendServices")
		if err != nil || owner.Request == nil {
			continue
		}

		if !lbTagsMatch(owner.Request, filters) {
			continue
		}

		lb, err := m.GetLoadBalancer(ctx, name)
		if err != nil {
			return nil, err
		}

		result = append(result, lb)
	}

	return result, nil
}
func lbTagsMatch(req *cpi.CreateLoadBalancerRequest, filters map[string]string) bool {
	for k, v := range filters {
		switch k {
		case "name":
			if req.Name != v {
				return false
			}
		case "network_id":
			if req.NetworkID != v {
				return false
			}
		default:
			if req.Tags[strings.TrimPrefix(k, "tag:")] != v {
				return false
			}
		}
	}

	return true
}
