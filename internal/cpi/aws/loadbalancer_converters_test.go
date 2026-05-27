package aws

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

func nilLoadBalancerManager() *LoadBalancerManager {
	return &LoadBalancerManager{client: nil}
}

// ---- mapLoadBalancerState ---------------------------------------------------

func TestMapLoadBalancerState(t *testing.T) {
	t.Parallel()

	m := nilLoadBalancerManager()
	tests := []struct {
		name  string
		input elbv2types.LoadBalancerStateEnum
		want  cpi.ResourceState
	}{
		{"active", elbv2types.LoadBalancerStateEnumActive, cpi.ResourceStateActive},
		{"provisioning", elbv2types.LoadBalancerStateEnumProvisioning, cpi.ResourceStateCreating},
		{"failed", elbv2types.LoadBalancerStateEnumFailed, cpi.ResourceStateError},
		{"active_impaired", elbv2types.LoadBalancerStateEnumActiveImpaired, cpi.ResourceStateError},
		{"unknown enum", elbv2types.LoadBalancerStateEnum("bogus"), cpi.ResourceStateUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := m.mapLoadBalancerState(tt.input)
			if got != tt.want {
				t.Errorf("mapLoadBalancerState(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// ---- convertLoadBalancer ----------------------------------------------------

func TestConvertLoadBalancer_InternetFacing(t *testing.T) {
	t.Parallel()

	created := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	m := nilLoadBalancerManager()
	lb := &elbv2types.LoadBalancer{
		LoadBalancerArn:  aws.String("arn:aws:elasticloadbalancing:us-east-1:123:loadbalancer/app/test-lb/abc"),
		LoadBalancerName: aws.String("test-lb"),
		Type:             elbv2types.LoadBalancerTypeEnumApplication,
		Scheme:           elbv2types.LoadBalancerSchemeEnumInternetFacing,
		VpcId:            aws.String("vpc-lb"),
		DNSName:          aws.String("test-lb.elb.us-east-1.amazonaws.com"),
		CreatedTime:      &created,
		State:            &elbv2types.LoadBalancerState{Code: elbv2types.LoadBalancerStateEnumActive},
		SecurityGroups:   []string{"sg-lb1", "sg-lb2"},
		AvailabilityZones: []elbv2types.AvailabilityZone{
			{SubnetId: aws.String("subnet-a")},
			{SubnetId: aws.String("subnet-b")},
			{SubnetId: nil}, // nil SubnetId must be skipped
		},
	}

	got := m.convertLoadBalancer(lb)

	if got.ID != "arn:aws:elasticloadbalancing:us-east-1:123:loadbalancer/app/test-lb/abc" {
		t.Errorf("ID = %q, unexpected", got.ID)
	}
	if got.Name != "test-lb" {
		t.Errorf("Name = %q, want test-lb", got.Name)
	}
	if got.Type != "external" {
		t.Errorf("Type = %q, want external (internet-facing)", got.Type)
	}
	if got.State != cpi.ResourceStateActive {
		t.Errorf("State = %q, want %q", got.State, cpi.ResourceStateActive)
	}
	if got.NetworkID != "vpc-lb" {
		t.Errorf("NetworkID = %q, want vpc-lb", got.NetworkID)
	}
	if got.IPAddress != "test-lb.elb.us-east-1.amazonaws.com" {
		t.Errorf("IPAddress = %q, unexpected", got.IPAddress)
	}
	if len(got.SubnetIDs) != 2 {
		t.Errorf("SubnetIDs len = %d, want 2 (nil SubnetId skipped)", len(got.SubnetIDs))
	}
	if got.CreatedAt != created {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, created)
	}
}

func TestConvertLoadBalancer_Internal(t *testing.T) {
	t.Parallel()

	m := nilLoadBalancerManager()
	lb := &elbv2types.LoadBalancer{
		LoadBalancerArn:  aws.String("arn:internal"),
		LoadBalancerName: aws.String("int-lb"),
		Scheme:           elbv2types.LoadBalancerSchemeEnumInternal,
		VpcId:            aws.String("vpc-int"),
		State:            &elbv2types.LoadBalancerState{Code: elbv2types.LoadBalancerStateEnumProvisioning},
	}

	got := m.convertLoadBalancer(lb)

	if got.Type != "internal" {
		t.Errorf("Type = %q, want internal", got.Type)
	}
	if got.State != cpi.ResourceStateCreating {
		t.Errorf("State = %q, want %q", got.State, cpi.ResourceStateCreating)
	}
}

func TestConvertLoadBalancer_NilDNS(t *testing.T) {
	t.Parallel()

	m := nilLoadBalancerManager()
	lb := &elbv2types.LoadBalancer{
		LoadBalancerArn:  aws.String("arn:nodns"),
		LoadBalancerName: aws.String("no-dns"),
		Scheme:           elbv2types.LoadBalancerSchemeEnumInternal,
		State:            &elbv2types.LoadBalancerState{Code: elbv2types.LoadBalancerStateEnumActive},
		DNSName:          nil,
	}

	got := m.convertLoadBalancer(lb)

	if got.IPAddress != "" {
		t.Errorf("IPAddress = %q, want empty (nil DNSName)", got.IPAddress)
	}
}

// ---- convertHealthCheck -----------------------------------------------------

func TestConvertHealthCheck_Basic(t *testing.T) {
	t.Parallel()

	m := nilLoadBalancerManager()
	interval := int32(30)
	timeout := int32(5)
	healthy := int32(2)
	unhealthy := int32(3)
	port := "8080"

	tg := &elbv2types.TargetGroup{
		HealthCheckProtocol:        elbv2types.ProtocolEnumHttp,
		HealthCheckPath:            aws.String("/health"),
		HealthCheckIntervalSeconds: &interval,
		HealthCheckTimeoutSeconds:  &timeout,
		HealthyThresholdCount:      &healthy,
		UnhealthyThresholdCount:    &unhealthy,
		HealthCheckPort:            &port,
	}

	got := m.convertHealthCheck(tg)

	if got.Protocol != string(elbv2types.ProtocolEnumHttp) {
		t.Errorf("Protocol = %q, want %q", got.Protocol, string(elbv2types.ProtocolEnumHttp))
	}
	if got.Path != "/health" {
		t.Errorf("Path = %q, want /health", got.Path)
	}
	if got.Interval != 30 {
		t.Errorf("Interval = %d, want 30", got.Interval)
	}
	if got.Timeout != 5 {
		t.Errorf("Timeout = %d, want 5", got.Timeout)
	}
	if got.HealthyThreshold != 2 {
		t.Errorf("HealthyThreshold = %d, want 2", got.HealthyThreshold)
	}
	if got.UnhealthyThreshold != 3 {
		t.Errorf("UnhealthyThreshold = %d, want 3", got.UnhealthyThreshold)
	}
	if got.Port != 8080 {
		t.Errorf("Port = %d, want 8080", got.Port)
	}
}

func TestConvertHealthCheck_InvalidPort(t *testing.T) {
	t.Parallel()

	m := nilLoadBalancerManager()
	port := "not-a-port"
	tg := &elbv2types.TargetGroup{
		HealthCheckPort: &port,
	}

	got := m.convertHealthCheck(tg)
	// invalid port string — Port stays 0 (strconv.Atoi fails silently)
	if got.Port != 0 {
		t.Errorf("Port = %d, want 0 (non-numeric port string)", got.Port)
	}
}

// ---- matchFilters -----------------------------------------------------------

func TestMatchFilters_EmptyFilters(t *testing.T) {
	t.Parallel()

	m := nilLoadBalancerManager()
	lb := &cpi.LoadBalancer{Name: "test", Type: "external"}
	if !m.matchFilters(lb, map[string]string{}) {
		t.Errorf("matchFilters with empty filters must return true")
	}
}

func TestMatchFilters_NameMatch(t *testing.T) {
	t.Parallel()

	m := nilLoadBalancerManager()
	lb := &cpi.LoadBalancer{Name: "prod-lb"}
	if !m.matchFilters(lb, map[string]string{"name": "prod-lb"}) {
		t.Errorf("matchFilters: expected true for matching name")
	}
	if m.matchFilters(lb, map[string]string{"name": "other"}) {
		t.Errorf("matchFilters: expected false for non-matching name")
	}
}

func TestMatchFilters_TypeMatch(t *testing.T) {
	t.Parallel()

	m := nilLoadBalancerManager()
	lb := &cpi.LoadBalancer{Type: "external"}
	if !m.matchFilters(lb, map[string]string{"type": "external"}) {
		t.Errorf("matchFilters: expected true for matching type")
	}
	if m.matchFilters(lb, map[string]string{"type": "internal"}) {
		t.Errorf("matchFilters: expected false for non-matching type")
	}
}

func TestMatchFilters_StateMatch(t *testing.T) {
	t.Parallel()

	m := nilLoadBalancerManager()
	lb := &cpi.LoadBalancer{State: cpi.ResourceStateActive}
	if !m.matchFilters(lb, map[string]string{"state": string(cpi.ResourceStateActive)}) {
		t.Errorf("matchFilters: expected true for matching state")
	}
}

func TestMatchFilters_NetworkIDMatch(t *testing.T) {
	t.Parallel()

	m := nilLoadBalancerManager()
	lb := &cpi.LoadBalancer{NetworkID: "vpc-abc"}
	if !m.matchFilters(lb, map[string]string{"network-id": "vpc-abc"}) {
		t.Errorf("matchFilters: expected true for network-id match")
	}
	if !m.matchFilters(lb, map[string]string{"vpc-id": "vpc-abc"}) {
		t.Errorf("matchFilters: expected true for vpc-id alias")
	}
}

func TestMatchFilters_UnknownKey(t *testing.T) {
	t.Parallel()

	m := nilLoadBalancerManager()
	lb := &cpi.LoadBalancer{Name: "test"}
	// unknown key (tag filter) — safely returns false
	if m.matchFilters(lb, map[string]string{"bloc": "prod"}) {
		t.Errorf("matchFilters: expected false for unknown tag key")
	}
}

func TestConvertHealthCheck_NilPort(t *testing.T) {
	t.Parallel()

	m := nilLoadBalancerManager()
	tg := &elbv2types.TargetGroup{
		HealthCheckPort: nil,
	}

	got := m.convertHealthCheck(tg)
	if got.Port != 0 {
		t.Errorf("Port = %d, want 0 (nil HealthCheckPort)", got.Port)
	}
}
