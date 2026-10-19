package aws

import (
	"context"
	"errors"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"testing"
)

func TestParityAWSNetworkLBGraph(t *testing.T) {
	lb := types.LoadBalancer{LoadBalancerArn: aws.String("arn:aws:elasticloadbalancing:us-east-1:123:loadbalancer/net/test/123"), LoadBalancerName: aws.String("test"), VpcId: aws.String("vpc-1"), Type: types.LoadBalancerTypeEnumNetwork, State: &types.LoadBalancerState{Code: types.LoadBalancerStateEnumActive}}
	var tg *elbv2.CreateTargetGroupInput
	var listener *elbv2.CreateListenerInput
	var registered *elbv2.RegisterTargetsInput
	fake := &lbFakeELB{
		CreateLoadBalancerFn: func(context.Context, *elbv2.CreateLoadBalancerInput, ...func(*elbv2.Options)) (*elbv2.CreateLoadBalancerOutput, error) {
			return &elbv2.CreateLoadBalancerOutput{LoadBalancers: []types.LoadBalancer{lb}}, nil
		},
		DescribeLoadBalancersFn: func(context.Context, *elbv2.DescribeLoadBalancersInput, ...func(*elbv2.Options)) (*elbv2.DescribeLoadBalancersOutput, error) {
			return &elbv2.DescribeLoadBalancersOutput{LoadBalancers: []types.LoadBalancer{lb}}, nil
		},
		CreateTargetGroupFn: func(_ context.Context, in *elbv2.CreateTargetGroupInput, _ ...func(*elbv2.Options)) (*elbv2.CreateTargetGroupOutput, error) {
			tg = in
			return &elbv2.CreateTargetGroupOutput{TargetGroups: []types.TargetGroup{{TargetGroupArn: aws.String("tg-1")}}}, nil
		},
		CreateListenerFn: func(_ context.Context, in *elbv2.CreateListenerInput, _ ...func(*elbv2.Options)) (*elbv2.CreateListenerOutput, error) {
			listener = in
			return &elbv2.CreateListenerOutput{}, nil
		},
		RegisterTargetsFn: func(_ context.Context, in *elbv2.RegisterTargetsInput, _ ...func(*elbv2.Options)) (*elbv2.RegisterTargetsOutput, error) {
			registered = in
			return &elbv2.RegisterTargetsOutput{}, nil
		},
	}
	m := &LoadBalancerManager{elb: fake}
	if _, err := m.CreateLoadBalancer(context.Background(), &cpi.CreateLoadBalancerRequest{Name: "test", Type: "network", SubnetIDs: []string{"subnet-1"}}); err != nil {
		t.Errorf("single subnet NLB: %v", err)
	}
	if err := m.AddBackend(context.Background(), aws.ToString(lb.LoadBalancerArn), &cpi.Backend{Address: "10.0.0.10", Port: 443}); err != nil {
		t.Fatal(err)
	}
	if tg.Protocol != types.ProtocolEnumTcp || tg.TargetType != types.TargetTypeEnumIp || aws.ToInt32(tg.Port) != 443 {
		t.Errorf("invalid NLB target graph: %+v", tg)
	}
	if listener.Protocol != types.ProtocolEnumTcp || aws.ToInt32(listener.Port) != 443 {
		t.Errorf("invalid listener: %+v", listener)
	}
	if registered == nil || aws.ToString(registered.Targets[0].Id) != "10.0.0.10" {
		t.Fatal("backend not registered")
	}
}

func TestParityAWSListenerFailureCleansTarget(t *testing.T) {
	deleted := false
	fake := &lbFakeELB{
		DescribeLoadBalancersFn: func(context.Context, *elbv2.DescribeLoadBalancersInput, ...func(*elbv2.Options)) (*elbv2.DescribeLoadBalancersOutput, error) {
			return &elbv2.DescribeLoadBalancersOutput{LoadBalancers: []types.LoadBalancer{{LoadBalancerArn: aws.String("arn:aws:elasticloadbalancing:us-east-1:123:loadbalancer/net/test/123"), LoadBalancerName: aws.String("test"), VpcId: aws.String("vpc-1"), Type: types.LoadBalancerTypeEnumNetwork}}}, nil
		},
		CreateTargetGroupFn: func(context.Context, *elbv2.CreateTargetGroupInput, ...func(*elbv2.Options)) (*elbv2.CreateTargetGroupOutput, error) {
			return &elbv2.CreateTargetGroupOutput{TargetGroups: []types.TargetGroup{{TargetGroupArn: aws.String("tg-1")}}}, nil
		},
		CreateListenerFn: func(context.Context, *elbv2.CreateListenerInput, ...func(*elbv2.Options)) (*elbv2.CreateListenerOutput, error) {
			return nil, errors.New("listener failed")
		},
		DeleteTargetGroupFn: func(context.Context, *elbv2.DeleteTargetGroupInput, ...func(*elbv2.Options)) (*elbv2.DeleteTargetGroupOutput, error) {
			deleted = true
			return &elbv2.DeleteTargetGroupOutput{}, nil
		},
	}
	err := (&LoadBalancerManager{elb: fake}).AddBackend(context.Background(), "test", &cpi.Backend{Address: "10.0.0.10", Port: 443})
	if err == nil || !deleted {
		t.Fatalf("target group leaked after listener error: %v", err)
	}
}

func TestParityAWSTargetDiscoveryErrorIsNotAbsence(t *testing.T) {
	created := false
	fake := &lbFakeELB{
		DescribeListenersFn: func(context.Context, *elbv2.DescribeListenersInput, ...func(*elbv2.Options)) (*elbv2.DescribeListenersOutput, error) {
			return &elbv2.DescribeListenersOutput{Listeners: []types.Listener{{DefaultActions: []types.Action{{TargetGroupArn: aws.String("existing")}}}}}, nil
		},
		DescribeTargetGroupsFn: func(context.Context, *elbv2.DescribeTargetGroupsInput, ...func(*elbv2.Options)) (*elbv2.DescribeTargetGroupsOutput, error) {
			return nil, errors.New("permission denied")
		},
		CreateTargetGroupFn: func(context.Context, *elbv2.CreateTargetGroupInput, ...func(*elbv2.Options)) (*elbv2.CreateTargetGroupOutput, error) {
			created = true
			return nil, errors.New("unexpected create")
		},
		DescribeLoadBalancersFn: func(context.Context, *elbv2.DescribeLoadBalancersInput, ...func(*elbv2.Options)) (*elbv2.DescribeLoadBalancersOutput, error) {
			return &elbv2.DescribeLoadBalancersOutput{LoadBalancers: []types.LoadBalancer{{LoadBalancerArn: aws.String("arn:aws:elasticloadbalancing:us-east-1:123:loadbalancer/net/test/123"), LoadBalancerName: aws.String("test")}}}, nil
		},
	}
	err := (&LoadBalancerManager{elb: fake}).AddBackend(context.Background(), "test", &cpi.Backend{Address: "10.0.0.10", Port: 443})
	if err == nil || created {
		t.Fatalf("failed discovery treated as absence: %v created=%v", err, created)
	}
}

func TestParityAWSLegacyLBRejectsIgnoredSettingsBeforeMutation(t *testing.T) {
	for _, cfg := range []*cpi.LoadBalancer{
		{Protocol: "https"}, {Port: -1}, {TargetPort: 65536}, {Backends: []*cpi.Backend{{Address: "not-an-ip", Port: 80}}}, {Tags: []string{"invalid-tag"}}, {Algorithm: "least-connections"},
	} {
		t.Run(fmt.Sprintf("%+v", cfg), func(t *testing.T) {
			defer func() {
				if recover() != nil {
					t.Error("request reached mutation path before validation")
				}
			}()
			_, err := (&NetworkManager{}).CreateLoadBalancer(context.Background(), cfg)
			if err == nil {
				t.Error("unsupported request silently accepted")
			}
		})
	}
}

func TestParityAWSBackendReadFailureIsNotEmptyHealth(t *testing.T) {
	fake := &lbFakeELB{
		DescribeLoadBalancersFn: func(context.Context, *elbv2.DescribeLoadBalancersInput, ...func(*elbv2.Options)) (*elbv2.DescribeLoadBalancersOutput, error) {
			return &elbv2.DescribeLoadBalancersOutput{LoadBalancers: []types.LoadBalancer{{State: &types.LoadBalancerState{Code: types.LoadBalancerStateEnumActive}, LoadBalancerArn: aws.String("arn:aws:elasticloadbalancing:us-east-1:123:loadbalancer/net/test/123")}}}, nil
		},
		DescribeListenersFn: func(context.Context, *elbv2.DescribeListenersInput, ...func(*elbv2.Options)) (*elbv2.DescribeListenersOutput, error) {
			return &elbv2.DescribeListenersOutput{Listeners: []types.Listener{{DefaultActions: []types.Action{{TargetGroupArn: aws.String("tg-1")}}}}}, nil
		},
		DescribeTargetGroupsFn: func(context.Context, *elbv2.DescribeTargetGroupsInput, ...func(*elbv2.Options)) (*elbv2.DescribeTargetGroupsOutput, error) {
			return &elbv2.DescribeTargetGroupsOutput{TargetGroups: []types.TargetGroup{{TargetGroupArn: aws.String("tg-1")}}}, nil
		},
		DescribeTargetHealthFn: func(context.Context, *elbv2.DescribeTargetHealthInput, ...func(*elbv2.Options)) (*elbv2.DescribeTargetHealthOutput, error) {
			return nil, errors.New("permission denied")
		},
	}
	m := &LoadBalancerManager{elb: fake}
	if _, err := m.GetLoadBalancer(context.Background(), "test"); err == nil {
		t.Error("backend read error hidden")
	}
	if _, err := m.GetHealthStatus(context.Background(), "test"); err == nil {
		t.Error("health read error hidden")
	}
}

func TestParityAWSLegacyLBGraph(t *testing.T) {
	arn := "arn:aws:elasticloadbalancing:us-east-1:123:loadbalancer/net/test/123"
	var created *elbv2.CreateLoadBalancerInput
	var target *elbv2.CreateTargetGroupInput
	var listener *elbv2.CreateListenerInput
	var health *elbv2.ModifyTargetGroupInput
	var targets *elbv2.RegisterTargetsInput
	lb := types.LoadBalancer{LoadBalancerArn: aws.String(arn), LoadBalancerName: aws.String("test"), VpcId: aws.String("vpc-1"), Type: types.LoadBalancerTypeEnumNetwork, State: &types.LoadBalancerState{Code: types.LoadBalancerStateEnumActive}}
	fake := &lbFakeELB{
		CreateLoadBalancerFn: func(_ context.Context, in *elbv2.CreateLoadBalancerInput, _ ...func(*elbv2.Options)) (*elbv2.CreateLoadBalancerOutput, error) {
			created = in
			return &elbv2.CreateLoadBalancerOutput{LoadBalancers: []types.LoadBalancer{lb}}, nil
		},
		DescribeLoadBalancersFn: func(context.Context, *elbv2.DescribeLoadBalancersInput, ...func(*elbv2.Options)) (*elbv2.DescribeLoadBalancersOutput, error) {
			return &elbv2.DescribeLoadBalancersOutput{LoadBalancers: []types.LoadBalancer{lb}}, nil
		},
		CreateTargetGroupFn: func(_ context.Context, in *elbv2.CreateTargetGroupInput, _ ...func(*elbv2.Options)) (*elbv2.CreateTargetGroupOutput, error) {
			target = in
			return &elbv2.CreateTargetGroupOutput{TargetGroups: []types.TargetGroup{{TargetGroupArn: aws.String("tg-1")}}}, nil
		},
		CreateListenerFn: func(_ context.Context, in *elbv2.CreateListenerInput, _ ...func(*elbv2.Options)) (*elbv2.CreateListenerOutput, error) {
			listener = in
			return &elbv2.CreateListenerOutput{}, nil
		},
		ModifyTargetGroupFn: func(_ context.Context, in *elbv2.ModifyTargetGroupInput, _ ...func(*elbv2.Options)) (*elbv2.ModifyTargetGroupOutput, error) {
			health = in
			return &elbv2.ModifyTargetGroupOutput{}, nil
		},
		RegisterTargetsFn: func(_ context.Context, in *elbv2.RegisterTargetsInput, _ ...func(*elbv2.Options)) (*elbv2.RegisterTargetsOutput, error) {
			targets = in
			return &elbv2.RegisterTargetsOutput{}, nil
		},
	}
	m := &NetworkManager{client: &Client{loadBalancer: &LoadBalancerManager{elb: fake}}}
	got, err := m.CreateLoadBalancer(context.Background(), &cpi.LoadBalancer{Name: "test", Type: "internal", Protocol: "tcp", Port: 443, TargetPort: 8443, SubnetIDs: []string{"subnet-1"}, Tags: []string{"ocfp:bloc=prod"}, Backends: []*cpi.Backend{{Address: "10.0.0.10", Port: 8443}}, HealthCheck: &cpi.HealthCheck{Protocol: "tcp", Port: 8443}})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != arn || created.Scheme != types.LoadBalancerSchemeEnumInternal || created.Type != types.LoadBalancerTypeEnumNetwork {
		t.Fatalf("wrong load balancer mapping: %+v", created)
	}
	if target == nil || aws.ToInt32(target.Port) != 8443 || target.Protocol != types.ProtocolEnumTcp || target.TargetType != types.TargetTypeEnumIp {
		t.Fatalf("target mapping: %+v", target)
	}
	if listener == nil || aws.ToInt32(listener.Port) != 443 || listener.Protocol != types.ProtocolEnumTcp {
		t.Fatalf("listener mapping: %+v", listener)
	}
	if health == nil || aws.ToString(health.HealthCheckPort) != "8443" || targets == nil || len(targets.Targets) != 1 {
		t.Fatal("health/backends omitted")
	}
	found := false
	for _, tag := range created.Tags {
		if aws.ToString(tag.Key) == "ocfp:bloc" && aws.ToString(tag.Value) == "prod" {
			found = true
		}
	}
	if !found {
		t.Fatal("tags omitted")
	}
}

type taggedELB struct {
	lbFakeELB
	tagErr error
}

func (f *taggedELB) DescribeTags(_ context.Context, in *elbv2.DescribeTagsInput, _ ...func(*elbv2.Options)) (*elbv2.DescribeTagsOutput, error) {
	if f.tagErr != nil {
		return nil, f.tagErr
	}
	return &elbv2.DescribeTagsOutput{TagDescriptions: []types.TagDescription{{ResourceArn: aws.String(in.ResourceArns[0]), Tags: []types.Tag{{Key: aws.String("ocfp:bloc"), Value: aws.String("prod")}}}}}, nil
}

func TestParityAWSLoadBalancerOwnershipDiscovery(t *testing.T) {
	fake := &taggedELB{lbFakeELB: lbFakeELB{DescribeLoadBalancersFn: func(context.Context, *elbv2.DescribeLoadBalancersInput, ...func(*elbv2.Options)) (*elbv2.DescribeLoadBalancersOutput, error) {
		return &elbv2.DescribeLoadBalancersOutput{LoadBalancers: []types.LoadBalancer{{LoadBalancerArn: aws.String("arn:aws:elasticloadbalancing:us-east-1:123:loadbalancer/net/test/123"), State: &types.LoadBalancerState{Code: types.LoadBalancerStateEnumActive}}}}, nil
	}}}
	m := &LoadBalancerManager{elb: fake}
	got, err := m.ListLoadBalancers(context.Background(), map[string]string{"ocfp:bloc": "prod"})
	if err != nil || len(got) != 1 {
		t.Fatalf("managed load balancer missing: %v %v", got, err)
	}
	got, err = m.ListLoadBalancers(context.Background(), map[string]string{"ocfp:bloc": "other"})
	if err != nil || len(got) != 0 {
		t.Fatalf("foreign load balancer adopted: %v %v", got, err)
	}
	fake.tagErr = errors.New("tag access denied")
	if _, err = m.ListLoadBalancers(context.Background(), map[string]string{"ocfp:bloc": "prod"}); err == nil {
		t.Fatal("ownership read failure hidden")
	}
}

func TestParityAWSListenerDiscoveryPagination(t *testing.T) {
	calls := 0
	fake := &lbFakeELB{
		DescribeListenersFn: func(_ context.Context, in *elbv2.DescribeListenersInput, _ ...func(*elbv2.Options)) (*elbv2.DescribeListenersOutput, error) {
			calls++
			out := &elbv2.DescribeListenersOutput{Listeners: []types.Listener{{DefaultActions: []types.Action{{TargetGroupArn: aws.String(fmt.Sprintf("tg-%d", calls))}}}}}
			if in.Marker == nil {
				out.NextMarker = aws.String("next")
			}
			return out, nil
		},
		DescribeTargetGroupsFn: func(_ context.Context, in *elbv2.DescribeTargetGroupsInput, _ ...func(*elbv2.Options)) (*elbv2.DescribeTargetGroupsOutput, error) {
			return &elbv2.DescribeTargetGroupsOutput{TargetGroups: []types.TargetGroup{{TargetGroupArn: aws.String(in.TargetGroupArns[0])}}}, nil
		},
	}
	got, err := (&LoadBalancerManager{}).getTargetGroupsForLoadBalancer(context.Background(), fake, "lb-1")
	if err != nil || len(got) != 2 {
		t.Fatalf("incomplete listener discovery: %d %v", len(got), err)
	}
}
