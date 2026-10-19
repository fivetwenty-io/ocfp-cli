package aws

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

// legacyLoadBalancerRequest validates the complete graph before any resource is created.
func legacyLoadBalancerRequest(cfg *cpi.LoadBalancer) (*cpi.CreateLoadBalancerRequest, int, int, error) {
	if cfg == nil {
		return nil, 0, 0, fmt.Errorf("nil load balancer: %w", ErrInvalidRequest)
	}

	if cfg.Algorithm != "" && cfg.Algorithm != "round-robin" {
		return nil, 0, 0, fmt.Errorf("load balancer algorithm %q: %w", cfg.Algorithm, ErrUnsupportedFeature)
	}

	req := &cpi.CreateLoadBalancerRequest{Name: cfg.Name, NetworkID: cfg.NetworkID, SubnetIDs: cfg.SubnetIDs, SecurityGroups: cfg.SecurityGroups, Type: "application", Scheme: "internet-facing", Tags: map[string]string{}}
	if err := configureLegacyLBType(cfg, req); err != nil {
		return nil, 0, 0, err
	}

	port, target := cfg.Port, cfg.TargetPort
	if port == 0 {
		port = 80
	}

	if target == 0 {
		target = port
	}

	if port < 1 || port > 65535 || target < 1 || target > 65535 {
		return nil, 0, 0, fmt.Errorf("listener and target ports must be 1..65535: %w", ErrInvalidRequest)
	}

	for _, tag := range cfg.Tags {
		k, v, ok := strings.Cut(tag, "=")
		if !ok || k == "" {
			return nil, 0, 0, fmt.Errorf("load balancer tags must be key=value: %w", ErrInvalidRequest)
		}

		req.Tags[k] = v
	}

	if err := validateLegacyBackends(cfg.Backends); err != nil {
		return nil, 0, 0, err
	}

	if err := validateLegacyHealthCheck(req.Type, cfg.HealthCheck); err != nil {
		return nil, 0, 0, err
	}

	return req, port, target, nil
}

func (m *NetworkManager) createLegacyLoadBalancer(ctx context.Context, cfg *cpi.LoadBalancer) (*cpi.LoadBalancer, error) {
	req, port, target, err := legacyLoadBalancerRequest(cfg)
	if err != nil {
		return nil, err
	}

	manager := m.client.loadBalancer

	lb, err := manager.CreateLoadBalancer(ctx, req)
	if err != nil {
		return lb, err
	}

	client, err := manager.getELB(ctx)
	if err != nil {
		return lb, err
	}

	var tg string

	fail := func(cause error) (*cpi.LoadBalancer, error) {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()

		_, lbErr := client.DeleteLoadBalancer(cleanupCtx, &elbv2.DeleteLoadBalancerInput{LoadBalancerArn: aws.String(lb.ID)})

		var tgErr error
		if tg != "" {
			_, tgErr = client.DeleteTargetGroup(cleanupCtx, &elbv2.DeleteTargetGroupInput{TargetGroupArn: aws.String(tg)})
		}

		return lb, errors.Join(cause, lbErr, tgErr)
	}

	address := "i-default"
	if len(cfg.Backends) > 0 {
		address = cfg.Backends[0].Address
	}

	tg, err = manager.createDefaultTargetGroup(ctx, client, lb.ID, target, address)
	if err != nil {
		return fail(err)
	}

	if cfg.HealthCheck != nil {
		if err = manager.updateTargetGroupHealthChecks(ctx, client, []types.TargetGroup{{TargetGroupArn: aws.String(tg)}}, cfg.HealthCheck); err != nil {
			return fail(err)
		}
	}

	if err = manager.ensureDefaultListener(ctx, client, lb.ID, tg, port); err != nil {
		return fail(err)
	}

	if len(cfg.Backends) > 0 {
		targets := make([]types.TargetDescription, 0, len(cfg.Backends))
		for _, b := range cfg.Backends {
			backendPort := b.Port
			if backendPort == 0 {
				backendPort = target
			}

			targets = append(targets, types.TargetDescription{Id: aws.String(b.Address), Port: aws.Int32(int32(backendPort))}) // #nosec G115 -- validated ports are 1..65535 above
		}

		if _, err = client.RegisterTargets(ctx, &elbv2.RegisterTargetsInput{TargetGroupArn: aws.String(tg), Targets: targets}); err != nil {
			return fail(err)
		}
	}

	lb.Port, lb.TargetPort, lb.Protocol = port, target, cfg.Protocol
	lb.Backends, lb.HealthCheck, lb.Tags = cfg.Backends, cfg.HealthCheck, cfg.Tags

	return lb, nil
}

func validateLegacyBackends(backends []*cpi.Backend) error {
	ipTargets := false

	for i, b := range backends {
		if b == nil || b.Port < 0 || b.Port > 65535 || (net.ParseIP(b.Address) == nil && !strings.HasPrefix(b.Address, "i-")) {
			return fmt.Errorf("invalid load balancer backend: %w", ErrInvalidRequest)
		}

		isIP := net.ParseIP(b.Address) != nil
		if i > 0 && ipTargets != isIP {
			return fmt.Errorf("cannot mix IP and instance backends in one target group: %w", ErrInvalidRequest)
		}

		ipTargets = isIP

		if b.Weight != 0 {
			return fmt.Errorf("per-backend weights unsupported: %w", ErrUnsupportedFeature)
		}
	}

	return nil
}

func validateLegacyHealthCheck(lbType string, check *cpi.HealthCheck) error {
	if check != nil {
		p := strings.ToLower(check.Protocol)
		if p != "" && p != "tcp" && p != "http" && p != "https" {
			return fmt.Errorf("invalid health protocol: %w", ErrInvalidRequest)
		}

		if lbType == "application" && p == "tcp" {
			return fmt.Errorf("ALB requires HTTP health checks: %w", ErrInvalidRequest)
		}

		if _, err := buildHealthCheckInput(aws.String("validation"), check); err != nil {
			return err
		}
	}

	return nil
}

func configureLegacyLBType(cfg *cpi.LoadBalancer, req *cpi.CreateLoadBalancerRequest) error {
	switch cfg.Type {
	case "", "external", "application":
	case "internal":
		req.Scheme = "internal"
	case "network":
		req.Type = "network"
	default:
		return fmt.Errorf("unknown load balancer type: %w", ErrInvalidRequest)
	}

	switch strings.ToLower(cfg.Protocol) {
	case "":
	case "http":
		if req.Type == "network" {
			return fmt.Errorf("HTTP listener requires an application load balancer: %w", ErrInvalidRequest)
		}
	case "tcp":
		req.Type = "network"
	default:
		return fmt.Errorf("listener protocol %q requires a certificate or unsupported native protocol: %w", cfg.Protocol, ErrUnsupportedFeature)
	}

	if req.Type == "network" && cfg.Algorithm != "" {
		return fmt.Errorf("network load balancers use native flow hashing: %w", ErrUnsupportedFeature)
	}

	return nil
}
