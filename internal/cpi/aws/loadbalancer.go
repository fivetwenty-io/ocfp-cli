package aws

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

const (
	// Default timeout for load balancer operations.
	loadBalancerWaitTimeout = 5 * time.Minute
	// Default interval for checking load balancer state.
	loadBalancerCheckInterval = 10 * time.Second
	// Default health check configuration.
	defaultHealthCheckProtocol = "HTTP"
	defaultHealthCheckPath     = "/"
	defaultHealthCheckInterval = 30
	defaultHealthCheckTimeout  = 5
	defaultHealthyThreshold    = 2
	defaultUnhealthyThreshold  = 2
	// AWS ELB requirements.
	minRequiredSubnets       = 2
	maxTargetGroupNameLength = 32
	defaultHTTPPort          = 80
)

var (
	// ErrLoadBalancerNotFound is returned when a load balancer is not found during wait.
	ErrLoadBalancerNotFound = errors.New("load balancer not found")
	// ErrLoadBalancerProvisionFailed is returned when a load balancer fails to provision.
	ErrLoadBalancerProvisionFailed = errors.New("load balancer failed to provision")
	// ErrLoadBalancerTimeout is returned when waiting for a load balancer times out.
	ErrLoadBalancerTimeout = errors.New("timeout waiting for load balancer to become active")
)

// LoadBalancerManager handles AWS Application Load Balancer operations.
type LoadBalancerManager struct {
	client *Client
}

// CreateLoadBalancer creates a new Application Load Balancer.
func (m *LoadBalancerManager) CreateLoadBalancer(ctx context.Context, req *cpi.CreateLoadBalancerRequest) (*cpi.LoadBalancer, error) {
	elbClient, err := m.client.getELBClient(ctx)
	if err != nil {
		return nil, WrapAWSError(err, "failed to get ELBv2 client")
	}

	// Validate request
	if req.Name == "" {
		return nil, fmt.Errorf("load balancer name is required: %w", ErrInvalidRequest)
	}

	if len(req.SubnetIDs) < minRequiredSubnets {
		return nil, fmt.Errorf("at least 2 subnets are required for ALB: %w", ErrInvalidRequest)
	}

	// Determine scheme (internet-facing or internal)
	scheme := elbv2types.LoadBalancerSchemeEnumInternetFacing
	if req.Scheme == "internal" {
		scheme = elbv2types.LoadBalancerSchemeEnumInternal
	}

	// Determine type (application or network)
	lbType := elbv2types.LoadBalancerTypeEnumApplication
	if req.Type == "network" {
		lbType = elbv2types.LoadBalancerTypeEnumNetwork
	}

	// Build tags
	tags := []elbv2types.Tag{
		{Key: aws.String("Name"), Value: aws.String(req.Name)},
		{Key: aws.String("managed-by"), Value: aws.String("ocfp")},
	}
	for k, v := range req.Tags {
		tags = append(tags, elbv2types.Tag{
			Key:   aws.String(k),
			Value: aws.String(v),
		})
	}

	// Create load balancer
	input := &elbv2.CreateLoadBalancerInput{
		Name:           aws.String(req.Name),
		Subnets:        req.SubnetIDs,
		SecurityGroups: req.SecurityGroups,
		Scheme:         scheme,
		Type:           lbType,
		Tags:           tags,
	}

	result, err := elbClient.CreateLoadBalancer(ctx, input)
	if err != nil {
		return nil, WrapAWSError(err, "failed to create load balancer")
	}

	if len(result.LoadBalancers) == 0 {
		return nil, WrapAWSError(ErrNotFound, "no load balancer returned")
	}

	loadBalancer := result.LoadBalancers[0]

	// Wait for load balancer to become active
	err = m.waitForLoadBalancerActive(ctx, elbClient, aws.ToString(loadBalancer.LoadBalancerArn))
	if err != nil {
		return nil, WrapAWSError(err, "load balancer did not become active")
	}

	return m.convertLoadBalancer(&loadBalancer), nil
}

// GetLoadBalancer retrieves a load balancer by ARN or name.
func (m *LoadBalancerManager) GetLoadBalancer(ctx context.Context, lbID string) (*cpi.LoadBalancer, error) {
	elbClient, err := m.client.getELBClient(ctx)
	if err != nil {
		return nil, WrapAWSError(err, "failed to get ELBv2 client")
	}

	// Try to describe by ARN or name
	var input *elbv2.DescribeLoadBalancersInput
	if strings.HasPrefix(lbID, "arn:aws:elasticloadbalancing:") {
		input = &elbv2.DescribeLoadBalancersInput{
			LoadBalancerArns: []string{lbID},
		}
	} else {
		input = &elbv2.DescribeLoadBalancersInput{
			Names: []string{lbID},
		}
	}

	result, err := elbClient.DescribeLoadBalancers(ctx, input)
	if err != nil {
		return nil, WrapAWSError(err, "failed to describe load balancer")
	}

	if len(result.LoadBalancers) == 0 {
		return nil, WrapAWSError(ErrNotFound, "load balancer not found")
	}

	loadBalancer := result.LoadBalancers[0]

	// Get target groups for this load balancer
	targetGroups, err := m.getTargetGroupsForLoadBalancer(ctx, elbClient, aws.ToString(loadBalancer.LoadBalancerArn))
	if err != nil {
		return nil, err
	}

	out := m.convertLoadBalancer(&loadBalancer)

	// Get backends from target groups
	for _, targetGroup := range targetGroups {
		backends, err := m.getBackendsFromTargetGroup(ctx, elbClient, aws.ToString(targetGroup.TargetGroupArn))
		if err != nil {
			continue
		}

		out.Backends = append(out.Backends, backends...)
	}

	// Get health check from first target group (if any)
	if len(targetGroups) > 0 {
		out.HealthCheck = m.convertHealthCheck(&targetGroups[0])
	}

	return out, nil
}

// ListLoadBalancers lists all load balancers with optional filters.
func (m *LoadBalancerManager) ListLoadBalancers(ctx context.Context, filters map[string]string) ([]*cpi.LoadBalancer, error) {
	elbClient, err := m.client.getELBClient(ctx)
	if err != nil {
		return nil, WrapAWSError(err, "failed to get ELBv2 client")
	}

	input := &elbv2.DescribeLoadBalancersInput{}

	// Handle pagination
	var loadBalancers []elbv2types.LoadBalancer

	paginator := elbv2.NewDescribeLoadBalancersPaginator(elbClient, input)

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, WrapAWSError(err, "failed to list load balancers")
		}

		loadBalancers = append(loadBalancers, page.LoadBalancers...)
	}

	// Convert to CPI type
	var result []*cpi.LoadBalancer

	for i := range loadBalancers {
		lb := m.convertLoadBalancer(&loadBalancers[i])

		// Apply filters
		if m.matchFilters(lb, filters) {
			result = append(result, lb)
		}
	}

	return result, nil
}

// UpdateLoadBalancer updates a load balancer's configuration.
func (m *LoadBalancerManager) UpdateLoadBalancer(ctx context.Context, lbID string, req *cpi.UpdateLoadBalancerRequest) error {
	elbClient, err := m.client.getELBClient(ctx)
	if err != nil {
		return WrapAWSError(err, "failed to get ELBv2 client")
	}

	// Get load balancer ARN
	lbArn, err := m.getLoadBalancerArn(ctx, elbClient, lbID)
	if err != nil {
		return err
	}

	// Update security groups if provided
	if len(req.SecurityGroups) > 0 {
		input := &elbv2.SetSecurityGroupsInput{
			LoadBalancerArn: aws.String(lbArn),
			SecurityGroups:  req.SecurityGroups,
		}

		_, err := elbClient.SetSecurityGroups(ctx, input)
		if err != nil {
			return WrapAWSError(err, "failed to update security groups")
		}
	}

	// Update tags if provided
	if len(req.Tags) > 0 {
		tags := make([]elbv2types.Tag, 0, len(req.Tags))
		for k, v := range req.Tags {
			tags = append(tags, elbv2types.Tag{
				Key:   aws.String(k),
				Value: aws.String(v),
			})
		}

		input := &elbv2.AddTagsInput{
			ResourceArns: []string{lbArn},
			Tags:         tags,
		}

		_, err := elbClient.AddTags(ctx, input)
		if err != nil {
			return WrapAWSError(err, "failed to update tags")
		}
	}

	return nil
}

// DeleteLoadBalancer deletes a load balancer.
func (m *LoadBalancerManager) DeleteLoadBalancer(ctx context.Context, lbID string) error {
	elbClient, err := m.client.getELBClient(ctx)
	if err != nil {
		return WrapAWSError(err, "failed to get ELBv2 client")
	}

	// Get load balancer ARN
	lbArn, err := m.getLoadBalancerArn(ctx, elbClient, lbID)
	if err != nil {
		return err
	}

	// Get target groups to delete them after load balancer deletion
	targetGroups, err := m.getTargetGroupsForLoadBalancer(ctx, elbClient, lbArn)
	if err != nil {
		return err
	}

	// Delete load balancer
	input := &elbv2.DeleteLoadBalancerInput{
		LoadBalancerArn: aws.String(lbArn),
	}

	_, err = elbClient.DeleteLoadBalancer(ctx, input)
	if err != nil {
		return WrapAWSError(err, "failed to delete load balancer")
	}

	// Delete associated target groups
	for _, tg := range targetGroups {
		delInput := &elbv2.DeleteTargetGroupInput{
			TargetGroupArn: tg.TargetGroupArn,
		}
		_, _ = elbClient.DeleteTargetGroup(ctx, delInput)
	}

	return nil
}

// AddBackend adds a backend to the load balancer.
func (m *LoadBalancerManager) AddBackend(ctx context.Context, lbID string, backend *cpi.Backend) error {
	elbClient, err := m.client.getELBClient(ctx)
	if err != nil {
		return WrapAWSError(err, "failed to get ELBv2 client")
	}

	// Get load balancer ARN
	lbArn, err := m.getLoadBalancerArn(ctx, elbClient, lbID)
	if err != nil {
		return err
	}

	// Get or create target group
	targetGroups, err := m.getTargetGroupsForLoadBalancer(ctx, elbClient, lbArn)
	if err != nil {
		return err
	}

	var targetGroupArn string

	if len(targetGroups) == 0 {
		// Create default target group
		tgArn, err := m.createDefaultTargetGroup(ctx, elbClient, lbID, backend.Port)
		if err != nil {
			return err
		}

		targetGroupArn = tgArn

		// Create default listener if none exists
		err = m.ensureDefaultListener(ctx, elbClient, lbArn, targetGroupArn)
		if err != nil {
			return err
		}
	} else {
		targetGroupArn = aws.ToString(targetGroups[0].TargetGroupArn)
	}

	// Register target
	if backend.Port < 0 || backend.Port > 65535 {
		return fmt.Errorf("invalid port number %d: must be between 0 and 65535: %w", backend.Port, ErrInvalidRequest)
	}

	targets := []elbv2types.TargetDescription{
		{
			Id:   aws.String(backend.Address),
			Port: aws.Int32(int32(backend.Port)),
		},
	}

	input := &elbv2.RegisterTargetsInput{
		TargetGroupArn: aws.String(targetGroupArn),
		Targets:        targets,
	}

	_, err = elbClient.RegisterTargets(ctx, input)
	if err != nil {
		return WrapAWSError(err, "failed to register target")
	}

	return nil
}

// RemoveBackend removes a backend from the load balancer.
func (m *LoadBalancerManager) RemoveBackend(ctx context.Context, lbID string, backendID string) error {
	elbClient, err := m.client.getELBClient(ctx)
	if err != nil {
		return WrapAWSError(err, "failed to get ELBv2 client")
	}

	// Get load balancer ARN
	lbArn, err := m.getLoadBalancerArn(ctx, elbClient, lbID)
	if err != nil {
		return err
	}

	// Get target groups
	targetGroups, err := m.getTargetGroupsForLoadBalancer(ctx, elbClient, lbArn)
	if err != nil {
		return err
	}

	// Deregister from all target groups
	for _, targetGroup := range targetGroups {
		targets := []elbv2types.TargetDescription{
			{
				Id: aws.String(backendID),
			},
		}

		input := &elbv2.DeregisterTargetsInput{
			TargetGroupArn: targetGroup.TargetGroupArn,
			Targets:        targets,
		}

		_, err := elbClient.DeregisterTargets(ctx, input)
		if err != nil {
			// Log error but continue to try other target groups
			continue
		}
	}

	return nil
}

// EnableBackend enables a backend in the load balancer.
func (m *LoadBalancerManager) EnableBackend(ctx context.Context, lbID string, backendID string) error {
	// AWS doesn't have a separate enable/disable; targets are either registered or not
	// We could implement this by registering the target if it's not already registered
	return ErrNotImplemented
}

// DisableBackend disables a backend in the load balancer.
func (m *LoadBalancerManager) DisableBackend(ctx context.Context, lbID string, backendID string) error {
	// AWS doesn't have a separate enable/disable; targets are either registered or not
	// We could implement this by deregistering the target
	return ErrNotImplemented
}

// ConfigureHealthCheck configures health check for the load balancer.
func (m *LoadBalancerManager) ConfigureHealthCheck(ctx context.Context, lbID string, check *cpi.HealthCheck) error {
	elbClient, err := m.client.getELBClient(ctx)
	if err != nil {
		return WrapAWSError(err, "failed to get ELBv2 client")
	}

	lbArn, err := m.getLoadBalancerArn(ctx, elbClient, lbID)
	if err != nil {
		return err
	}

	targetGroups, err := m.getTargetGroupsForLoadBalancer(ctx, elbClient, lbArn)
	if err != nil {
		return err
	}

	if len(targetGroups) == 0 {
		return fmt.Errorf("no target groups found for load balancer: %w", ErrNotFound)
	}

	return m.updateTargetGroupHealthChecks(ctx, elbClient, targetGroups, check)
}

// GetHealthStatus retrieves the health status of backends.
func (m *LoadBalancerManager) GetHealthStatus(ctx context.Context, lbID string) (*cpi.HealthStatus, error) {
	elbClient, err := m.client.getELBClient(ctx)
	if err != nil {
		return nil, WrapAWSError(err, "failed to get ELBv2 client")
	}

	// Get load balancer ARN
	lbArn, err := m.getLoadBalancerArn(ctx, elbClient, lbID)
	if err != nil {
		return nil, err
	}

	// Get target groups
	targetGroups, err := m.getTargetGroupsForLoadBalancer(ctx, elbClient, lbArn)
	if err != nil {
		return nil, err
	}

	status := &cpi.HealthStatus{
		LoadBalancerID: lbID,
		Backends:       make(map[string]string),
	}

	// Get health for each target group
	for _, targetGroup := range targetGroups {
		input := &elbv2.DescribeTargetHealthInput{
			TargetGroupArn: targetGroup.TargetGroupArn,
		}

		result, err := elbClient.DescribeTargetHealth(ctx, input)
		if err != nil {
			continue
		}

		for _, targetHealth := range result.TargetHealthDescriptions {
			targetID := aws.ToString(targetHealth.Target.Id)
			healthState := string(targetHealth.TargetHealth.State)

			status.Backends[targetID] = healthState
			status.Total++

			if targetHealth.TargetHealth.State == elbv2types.TargetHealthStateEnumHealthy {
				status.Healthy++
			} else {
				status.Unhealthy++
			}
		}
	}

	return status, nil
}

// updateTargetGroupHealthChecks updates health checks for target groups.
func (m *LoadBalancerManager) updateTargetGroupHealthChecks(
	ctx context.Context,
	elbClient *elbv2.Client,
	targetGroups []elbv2types.TargetGroup,
	check *cpi.HealthCheck,
) error {
	for _, tg := range targetGroups {
		input, err := buildHealthCheckInput(tg.TargetGroupArn, check)
		if err != nil {
			return err
		}

		_, err = elbClient.ModifyTargetGroup(ctx, input)
		if err != nil {
			return WrapAWSError(err, "failed to modify target group health check")
		}
	}

	return nil
}

// buildHealthCheckInput creates modify target group input from health check config.
func buildHealthCheckInput(targetGroupArn *string, check *cpi.HealthCheck) (*elbv2.ModifyTargetGroupInput, error) {
	input := &elbv2.ModifyTargetGroupInput{
		TargetGroupArn: targetGroupArn,
	}

	if check.Protocol != "" {
		protocol := elbv2types.ProtocolEnum(strings.ToUpper(check.Protocol))
		input.HealthCheckProtocol = protocol
	}

	if check.Port > 0 {
		input.HealthCheckPort = aws.String(strconv.Itoa(check.Port))
	}

	if check.Path != "" {
		input.HealthCheckPath = aws.String(check.Path)
	}

	err := setHealthCheckInterval(input, check.Interval)
	if err != nil {
		return nil, err
	}

	err = setHealthCheckTimeout(input, check.Timeout)
	if err != nil {
		return nil, err
	}

	err = setHealthyThreshold(input, check.HealthyThreshold)
	if err != nil {
		return nil, err
	}

	err = setUnhealthyThreshold(input, check.UnhealthyThreshold)
	if err != nil {
		return nil, err
	}

	return input, nil
}

// setHealthCheckInterval sets and validates health check interval.
func setHealthCheckInterval(input *elbv2.ModifyTargetGroupInput, interval int) error {
	const maxInterval = 300
	if interval > 0 {
		if interval < 0 || interval > maxInterval {
			return fmt.Errorf("invalid interval %d: must be between 0 and 300: %w", interval, ErrInvalidRequest)
		}

		input.HealthCheckIntervalSeconds = aws.Int32(int32(interval))
	}

	return nil
}

// setHealthCheckTimeout sets and validates health check timeout.
func setHealthCheckTimeout(input *elbv2.ModifyTargetGroupInput, timeout int) error {
	const maxTimeout = 120
	if timeout > 0 {
		if timeout < 0 || timeout > maxTimeout {
			return fmt.Errorf("invalid timeout %d: must be between 0 and 120: %w", timeout, ErrInvalidRequest)
		}

		input.HealthCheckTimeoutSeconds = aws.Int32(int32(timeout))
	}

	return nil
}

// setHealthyThreshold sets and validates healthy threshold.
func setHealthyThreshold(input *elbv2.ModifyTargetGroupInput, threshold int) error {
	const maxThreshold = 10
	if threshold > 0 {
		if threshold < 0 || threshold > maxThreshold {
			return fmt.Errorf("invalid healthy threshold %d: must be between 0 and 10: %w", threshold, ErrInvalidRequest)
		}

		input.HealthyThresholdCount = aws.Int32(int32(threshold))
	}

	return nil
}

// setUnhealthyThreshold sets and validates unhealthy threshold.
func setUnhealthyThreshold(input *elbv2.ModifyTargetGroupInput, threshold int) error {
	const maxThreshold = 10
	if threshold > 0 {
		if threshold < 0 || threshold > maxThreshold {
			return fmt.Errorf("invalid unhealthy threshold %d: must be between 0 and 10: %w", threshold, ErrInvalidRequest)
		}

		input.UnhealthyThresholdCount = aws.Int32(int32(threshold))
	}

	return nil
}

// Helper functions

// convertLoadBalancer converts AWS load balancer to CPI type.
func (m *LoadBalancerManager) convertLoadBalancer(loadBalancer *elbv2types.LoadBalancer) *cpi.LoadBalancer {
	out := &cpi.LoadBalancer{
		ID:             aws.ToString(loadBalancer.LoadBalancerArn),
		Name:           aws.ToString(loadBalancer.LoadBalancerName),
		Type:           string(loadBalancer.Type),
		Status:         string(loadBalancer.State.Code),
		State:          m.mapLoadBalancerState(loadBalancer.State.Code),
		NetworkID:      aws.ToString(loadBalancer.VpcId),
		SubnetIDs:      []string{},
		SecurityGroups: loadBalancer.SecurityGroups,
		Backends:       []*cpi.Backend{},
		Tags:           []string{},
		CreatedAt:      aws.ToTime(loadBalancer.CreatedTime),
		UpdatedAt:      time.Now(),
	}

	// Extract subnets
	for _, az := range loadBalancer.AvailabilityZones {
		if az.SubnetId != nil {
			out.SubnetIDs = append(out.SubnetIDs, aws.ToString(az.SubnetId))
		}
	}

	// Extract IP address
	if loadBalancer.DNSName != nil {
		out.IPAddress = aws.ToString(loadBalancer.DNSName)
	}

	// Set scheme
	if loadBalancer.Scheme == elbv2types.LoadBalancerSchemeEnumInternetFacing {
		out.Type = "external"
	} else {
		out.Type = "internal"
	}

	return out
}

// convertHealthCheck converts target group to health check.
func (m *LoadBalancerManager) convertHealthCheck(targetGroup *elbv2types.TargetGroup) *cpi.HealthCheck {
	check := &cpi.HealthCheck{
		Protocol:           string(targetGroup.HealthCheckProtocol),
		Path:               aws.ToString(targetGroup.HealthCheckPath),
		Interval:           int(aws.ToInt32(targetGroup.HealthCheckIntervalSeconds)),
		Timeout:            int(aws.ToInt32(targetGroup.HealthCheckTimeoutSeconds)),
		HealthyThreshold:   int(aws.ToInt32(targetGroup.HealthyThresholdCount)),
		UnhealthyThreshold: int(aws.ToInt32(targetGroup.UnhealthyThresholdCount)),
	}

	// Parse health check port
	if targetGroup.HealthCheckPort != nil {
		port, err := strconv.Atoi(aws.ToString(targetGroup.HealthCheckPort))
		if err == nil {
			check.Port = port
		}
	}

	return check
}

// mapLoadBalancerState maps AWS load balancer state to CPI state.
func (m *LoadBalancerManager) mapLoadBalancerState(state elbv2types.LoadBalancerStateEnum) cpi.ResourceState {
	switch state {
	case elbv2types.LoadBalancerStateEnumActive:
		return cpi.ResourceStateActive
	case elbv2types.LoadBalancerStateEnumProvisioning:
		return cpi.ResourceStateCreating
	case elbv2types.LoadBalancerStateEnumFailed:
		return cpi.ResourceStateError
	case elbv2types.LoadBalancerStateEnumActiveImpaired:
		return cpi.ResourceStateError
	default:
		return cpi.ResourceStateUnknown
	}
}

// waitForLoadBalancerActive waits for a load balancer to become active.
func (m *LoadBalancerManager) waitForLoadBalancerActive(ctx context.Context, client *elbv2.Client, lbArn string) error {
	deadline := time.Now().Add(loadBalancerWaitTimeout)

	for time.Now().Before(deadline) {
		input := &elbv2.DescribeLoadBalancersInput{
			LoadBalancerArns: []string{lbArn},
		}

		result, err := client.DescribeLoadBalancers(ctx, input)
		if err != nil {
			return WrapAWSError(err, "failed to describe load balancers while waiting")
		}

		if len(result.LoadBalancers) == 0 {
			return ErrLoadBalancerNotFound
		}

		loadBalancer := result.LoadBalancers[0]
		if loadBalancer.State.Code == elbv2types.LoadBalancerStateEnumActive {
			return nil
		}

		if loadBalancer.State.Code == elbv2types.LoadBalancerStateEnumFailed {
			return fmt.Errorf("%w: %s", ErrLoadBalancerProvisionFailed, aws.ToString(loadBalancer.State.Reason))
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("context cancelled while waiting for load balancer: %w", ctx.Err())
		case <-time.After(loadBalancerCheckInterval):
			// Continue waiting
		}
	}

	return ErrLoadBalancerTimeout
}

// getLoadBalancerArn gets the ARN for a load balancer by name or ARN.
func (m *LoadBalancerManager) getLoadBalancerArn(ctx context.Context, client *elbv2.Client, lbID string) (string, error) {
	// If already an ARN, return it
	if strings.HasPrefix(lbID, "arn:aws:elasticloadbalancing:") {
		return lbID, nil
	}

	// Otherwise, look up by name
	input := &elbv2.DescribeLoadBalancersInput{
		Names: []string{lbID},
	}

	result, err := client.DescribeLoadBalancers(ctx, input)
	if err != nil {
		return "", WrapAWSError(err, "failed to find load balancer")
	}

	if len(result.LoadBalancers) == 0 {
		return "", WrapAWSError(ErrNotFound, "load balancer not found")
	}

	return aws.ToString(result.LoadBalancers[0].LoadBalancerArn), nil
}

// getTargetGroupsForLoadBalancer gets all target groups for a load balancer.
func (m *LoadBalancerManager) getTargetGroupsForLoadBalancer(ctx context.Context, client *elbv2.Client, lbArn string) ([]elbv2types.TargetGroup, error) {
	// Get listeners for the load balancer
	listenersInput := &elbv2.DescribeListenersInput{
		LoadBalancerArn: aws.String(lbArn),
	}

	listenersResult, err := client.DescribeListeners(ctx, listenersInput)
	if err != nil {
		return nil, WrapAWSError(err, "failed to describe listeners")
	}

	// Collect unique target group ARNs
	tgArnSet := make(map[string]bool)

	for _, listener := range listenersResult.Listeners {
		for _, action := range listener.DefaultActions {
			if action.TargetGroupArn != nil {
				tgArnSet[aws.ToString(action.TargetGroupArn)] = true
			}
		}
	}

	// Get target group details
	var targetGroups []elbv2types.TargetGroup

	for arn := range tgArnSet {
		input := &elbv2.DescribeTargetGroupsInput{
			TargetGroupArns: []string{arn},
		}

		result, err := client.DescribeTargetGroups(ctx, input)
		if err != nil {
			continue
		}

		targetGroups = append(targetGroups, result.TargetGroups...)
	}

	return targetGroups, nil
}

// getBackendsFromTargetGroup gets all backends from a target group.
func (m *LoadBalancerManager) getBackendsFromTargetGroup(ctx context.Context, client *elbv2.Client, tgArn string) ([]*cpi.Backend, error) {
	input := &elbv2.DescribeTargetHealthInput{
		TargetGroupArn: aws.String(tgArn),
	}

	result, err := client.DescribeTargetHealth(ctx, input)
	if err != nil {
		return nil, WrapAWSError(err, "failed to describe target health")
	}

	backends := make([]*cpi.Backend, 0, len(result.TargetHealthDescriptions))
	for _, targetHealth := range result.TargetHealthDescriptions {
		backend := &cpi.Backend{
			ID:      aws.ToString(targetHealth.Target.Id),
			Address: aws.ToString(targetHealth.Target.Id),
			Port:    int(aws.ToInt32(targetHealth.Target.Port)),
			Enabled: targetHealth.TargetHealth.State == elbv2types.TargetHealthStateEnumHealthy,
			Health:  string(targetHealth.TargetHealth.State),
		}
		backends = append(backends, backend)
	}

	return backends, nil
}

// createDefaultTargetGroup creates a default target group for a load balancer.
func (m *LoadBalancerManager) createDefaultTargetGroup(ctx context.Context, client *elbv2.Client, lbName string, port int) (string, error) {
	// Validate port
	if port < 0 || port > 65535 {
		return "", fmt.Errorf("invalid port number %d: must be between 0 and 65535: %w", port, ErrInvalidRequest)
	}

	// Get VPC ID from load balancer
	lbInput := &elbv2.DescribeLoadBalancersInput{
		Names: []string{lbName},
	}

	lbResult, err := client.DescribeLoadBalancers(ctx, lbInput)
	if err != nil {
		return "", WrapAWSError(err, "failed to describe load balancer")
	}

	if len(lbResult.LoadBalancers) == 0 {
		return "", WrapAWSError(ErrNotFound, "load balancer not found")
	}

	vpcID := lbResult.LoadBalancers[0].VpcId

	// Create target group
	tgName := lbName + "-tg"
	if len(tgName) > maxTargetGroupNameLength {
		tgName = tgName[:maxTargetGroupNameLength]
	}

	tgInput := &elbv2.CreateTargetGroupInput{
		Name:     aws.String(tgName),
		Protocol: elbv2types.ProtocolEnumHttp,
		Port:     aws.Int32(int32(port)), // #nosec G115 -- port validated above

		VpcId:                      vpcID,
		TargetType:                 elbv2types.TargetTypeEnumInstance,
		HealthCheckProtocol:        elbv2types.ProtocolEnum(defaultHealthCheckProtocol),
		HealthCheckPath:            aws.String(defaultHealthCheckPath),
		HealthCheckIntervalSeconds: aws.Int32(int32(defaultHealthCheckInterval)),
		HealthCheckTimeoutSeconds:  aws.Int32(int32(defaultHealthCheckTimeout)),
		HealthyThresholdCount:      aws.Int32(int32(defaultHealthyThreshold)),
		UnhealthyThresholdCount:    aws.Int32(int32(defaultUnhealthyThreshold)),
		Tags: []elbv2types.Tag{
			{Key: aws.String("Name"), Value: aws.String(tgName)},
			{Key: aws.String("managed-by"), Value: aws.String("ocfp")},
		},
	}

	tgResult, err := client.CreateTargetGroup(ctx, tgInput)
	if err != nil {
		return "", WrapAWSError(err, "failed to create target group")
	}

	if len(tgResult.TargetGroups) == 0 {
		return "", WrapAWSError(ErrNotFound, "no target group returned")
	}

	return aws.ToString(tgResult.TargetGroups[0].TargetGroupArn), nil
}

// ensureDefaultListener ensures a default listener exists for the load balancer.
func (m *LoadBalancerManager) ensureDefaultListener(ctx context.Context, client *elbv2.Client, lbArn string, targetGroupArn string) error {
	// Check if listeners already exist
	input := &elbv2.DescribeListenersInput{
		LoadBalancerArn: aws.String(lbArn),
	}

	result, err := client.DescribeListeners(ctx, input)
	if err != nil {
		return WrapAWSError(err, "failed to describe listeners")
	}

	if len(result.Listeners) > 0 {
		return nil // Listener already exists
	}

	// Create default HTTP listener
	listenerInput := &elbv2.CreateListenerInput{
		LoadBalancerArn: aws.String(lbArn),
		Protocol:        elbv2types.ProtocolEnumHttp,
		Port:            aws.Int32(defaultHTTPPort),
		DefaultActions: []elbv2types.Action{
			{
				Type:           elbv2types.ActionTypeEnumForward,
				TargetGroupArn: aws.String(targetGroupArn),
			},
		},
	}

	_, err = client.CreateListener(ctx, listenerInput)
	if err != nil {
		return WrapAWSError(err, "failed to create listener")
	}

	return nil
}

// matchFilters checks if a load balancer matches the given filters.
func (m *LoadBalancerManager) matchFilters(loadBalancer *cpi.LoadBalancer, filters map[string]string) bool {
	if len(filters) == 0 {
		return true
	}

	for key, value := range filters {
		switch key {
		case "name":
			if loadBalancer.Name != value {
				return false
			}
		case "type":
			if loadBalancer.Type != value {
				return false
			}
		case "state":
			if string(loadBalancer.State) != value {
				return false
			}
		case "network-id", "vpc-id":
			if loadBalancer.NetworkID != value {
				return false
			}
		}
	}

	return true
}
