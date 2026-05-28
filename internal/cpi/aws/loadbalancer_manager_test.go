package aws

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

// lbFakeELB is a test stub for ELBv2API.
// Only the methods exercised by individual tests need non-nil responses;
// all others return a zero-value output and a nil error unless overridden
// via the corresponding Fn field.
type lbFakeELB struct {
	CreateLoadBalancerFn    func(ctx context.Context, params *elbv2.CreateLoadBalancerInput, optFns ...func(*elbv2.Options)) (*elbv2.CreateLoadBalancerOutput, error)
	DescribeLoadBalancersFn func(ctx context.Context, params *elbv2.DescribeLoadBalancersInput, optFns ...func(*elbv2.Options)) (*elbv2.DescribeLoadBalancersOutput, error)
	DeleteLoadBalancerFn    func(ctx context.Context, params *elbv2.DeleteLoadBalancerInput, optFns ...func(*elbv2.Options)) (*elbv2.DeleteLoadBalancerOutput, error)
	SetSecurityGroupsFn     func(ctx context.Context, params *elbv2.SetSecurityGroupsInput, optFns ...func(*elbv2.Options)) (*elbv2.SetSecurityGroupsOutput, error)
	AddTagsFn               func(ctx context.Context, params *elbv2.AddTagsInput, optFns ...func(*elbv2.Options)) (*elbv2.AddTagsOutput, error)
	CreateTargetGroupFn     func(ctx context.Context, params *elbv2.CreateTargetGroupInput, optFns ...func(*elbv2.Options)) (*elbv2.CreateTargetGroupOutput, error)
	DescribeTargetGroupsFn  func(ctx context.Context, params *elbv2.DescribeTargetGroupsInput, optFns ...func(*elbv2.Options)) (*elbv2.DescribeTargetGroupsOutput, error)
	ModifyTargetGroupFn     func(ctx context.Context, params *elbv2.ModifyTargetGroupInput, optFns ...func(*elbv2.Options)) (*elbv2.ModifyTargetGroupOutput, error)
	DeleteTargetGroupFn     func(ctx context.Context, params *elbv2.DeleteTargetGroupInput, optFns ...func(*elbv2.Options)) (*elbv2.DeleteTargetGroupOutput, error)
	RegisterTargetsFn       func(ctx context.Context, params *elbv2.RegisterTargetsInput, optFns ...func(*elbv2.Options)) (*elbv2.RegisterTargetsOutput, error)
	DeregisterTargetsFn     func(ctx context.Context, params *elbv2.DeregisterTargetsInput, optFns ...func(*elbv2.Options)) (*elbv2.DeregisterTargetsOutput, error)
	DescribeTargetHealthFn  func(ctx context.Context, params *elbv2.DescribeTargetHealthInput, optFns ...func(*elbv2.Options)) (*elbv2.DescribeTargetHealthOutput, error)
	DescribeListenersFn     func(ctx context.Context, params *elbv2.DescribeListenersInput, optFns ...func(*elbv2.Options)) (*elbv2.DescribeListenersOutput, error)
	CreateListenerFn        func(ctx context.Context, params *elbv2.CreateListenerInput, optFns ...func(*elbv2.Options)) (*elbv2.CreateListenerOutput, error)
}

func (f *lbFakeELB) CreateLoadBalancer(ctx context.Context, params *elbv2.CreateLoadBalancerInput, optFns ...func(*elbv2.Options)) (*elbv2.CreateLoadBalancerOutput, error) {
	if f.CreateLoadBalancerFn != nil {
		return f.CreateLoadBalancerFn(ctx, params, optFns...)
	}
	return &elbv2.CreateLoadBalancerOutput{}, nil
}

func (f *lbFakeELB) DescribeLoadBalancers(ctx context.Context, params *elbv2.DescribeLoadBalancersInput, optFns ...func(*elbv2.Options)) (*elbv2.DescribeLoadBalancersOutput, error) {
	if f.DescribeLoadBalancersFn != nil {
		return f.DescribeLoadBalancersFn(ctx, params, optFns...)
	}
	return &elbv2.DescribeLoadBalancersOutput{}, nil
}

func (f *lbFakeELB) DeleteLoadBalancer(ctx context.Context, params *elbv2.DeleteLoadBalancerInput, optFns ...func(*elbv2.Options)) (*elbv2.DeleteLoadBalancerOutput, error) {
	if f.DeleteLoadBalancerFn != nil {
		return f.DeleteLoadBalancerFn(ctx, params, optFns...)
	}
	return &elbv2.DeleteLoadBalancerOutput{}, nil
}

func (f *lbFakeELB) SetSecurityGroups(ctx context.Context, params *elbv2.SetSecurityGroupsInput, optFns ...func(*elbv2.Options)) (*elbv2.SetSecurityGroupsOutput, error) {
	if f.SetSecurityGroupsFn != nil {
		return f.SetSecurityGroupsFn(ctx, params, optFns...)
	}
	return &elbv2.SetSecurityGroupsOutput{}, nil
}

func (f *lbFakeELB) AddTags(ctx context.Context, params *elbv2.AddTagsInput, optFns ...func(*elbv2.Options)) (*elbv2.AddTagsOutput, error) {
	if f.AddTagsFn != nil {
		return f.AddTagsFn(ctx, params, optFns...)
	}
	return &elbv2.AddTagsOutput{}, nil
}

func (f *lbFakeELB) CreateTargetGroup(ctx context.Context, params *elbv2.CreateTargetGroupInput, optFns ...func(*elbv2.Options)) (*elbv2.CreateTargetGroupOutput, error) {
	if f.CreateTargetGroupFn != nil {
		return f.CreateTargetGroupFn(ctx, params, optFns...)
	}
	return &elbv2.CreateTargetGroupOutput{}, nil
}

func (f *lbFakeELB) DescribeTargetGroups(ctx context.Context, params *elbv2.DescribeTargetGroupsInput, optFns ...func(*elbv2.Options)) (*elbv2.DescribeTargetGroupsOutput, error) {
	if f.DescribeTargetGroupsFn != nil {
		return f.DescribeTargetGroupsFn(ctx, params, optFns...)
	}
	return &elbv2.DescribeTargetGroupsOutput{}, nil
}

func (f *lbFakeELB) ModifyTargetGroup(ctx context.Context, params *elbv2.ModifyTargetGroupInput, optFns ...func(*elbv2.Options)) (*elbv2.ModifyTargetGroupOutput, error) {
	if f.ModifyTargetGroupFn != nil {
		return f.ModifyTargetGroupFn(ctx, params, optFns...)
	}
	return &elbv2.ModifyTargetGroupOutput{}, nil
}

func (f *lbFakeELB) DeleteTargetGroup(ctx context.Context, params *elbv2.DeleteTargetGroupInput, optFns ...func(*elbv2.Options)) (*elbv2.DeleteTargetGroupOutput, error) {
	if f.DeleteTargetGroupFn != nil {
		return f.DeleteTargetGroupFn(ctx, params, optFns...)
	}
	return &elbv2.DeleteTargetGroupOutput{}, nil
}

func (f *lbFakeELB) RegisterTargets(ctx context.Context, params *elbv2.RegisterTargetsInput, optFns ...func(*elbv2.Options)) (*elbv2.RegisterTargetsOutput, error) {
	if f.RegisterTargetsFn != nil {
		return f.RegisterTargetsFn(ctx, params, optFns...)
	}
	return &elbv2.RegisterTargetsOutput{}, nil
}

func (f *lbFakeELB) DeregisterTargets(ctx context.Context, params *elbv2.DeregisterTargetsInput, optFns ...func(*elbv2.Options)) (*elbv2.DeregisterTargetsOutput, error) {
	if f.DeregisterTargetsFn != nil {
		return f.DeregisterTargetsFn(ctx, params, optFns...)
	}
	return &elbv2.DeregisterTargetsOutput{}, nil
}

func (f *lbFakeELB) DescribeTargetHealth(ctx context.Context, params *elbv2.DescribeTargetHealthInput, optFns ...func(*elbv2.Options)) (*elbv2.DescribeTargetHealthOutput, error) {
	if f.DescribeTargetHealthFn != nil {
		return f.DescribeTargetHealthFn(ctx, params, optFns...)
	}
	return &elbv2.DescribeTargetHealthOutput{}, nil
}

func (f *lbFakeELB) DescribeListeners(ctx context.Context, params *elbv2.DescribeListenersInput, optFns ...func(*elbv2.Options)) (*elbv2.DescribeListenersOutput, error) {
	if f.DescribeListenersFn != nil {
		return f.DescribeListenersFn(ctx, params, optFns...)
	}
	return &elbv2.DescribeListenersOutput{}, nil
}

func (f *lbFakeELB) CreateListener(ctx context.Context, params *elbv2.CreateListenerInput, optFns ...func(*elbv2.Options)) (*elbv2.CreateListenerOutput, error) {
	if f.CreateListenerFn != nil {
		return f.CreateListenerFn(ctx, params, optFns...)
	}
	return &elbv2.CreateListenerOutput{}, nil
}

// compile-time: lbFakeELB must satisfy ELBv2API.
var _ ELBv2API = (*lbFakeELB)(nil)

// newLBManager builds a LoadBalancerManager wired to the given fake.
func newLBManager(fake ELBv2API) *LoadBalancerManager {
	return &LoadBalancerManager{client: nil, elb: fake}
}

// ---- CreateLoadBalancer -----------------------------------------------------

func TestLBManager_CreateLoadBalancer_Happy(t *testing.T) {
	t.Parallel()

	const lbARN = "arn:aws:elasticloadbalancing:us-east-1:123:loadbalancer/app/my-lb/abc"

	fake := &lbFakeELB{
		CreateLoadBalancerFn: func(_ context.Context, params *elbv2.CreateLoadBalancerInput, _ ...func(*elbv2.Options)) (*elbv2.CreateLoadBalancerOutput, error) {
			return &elbv2.CreateLoadBalancerOutput{
				LoadBalancers: []elbv2types.LoadBalancer{
					{
						LoadBalancerArn:  aws.String(lbARN),
						LoadBalancerName: aws.String(aws.ToString(params.Name)),
						State:            &elbv2types.LoadBalancerState{Code: elbv2types.LoadBalancerStateEnumActive},
						Type:             elbv2types.LoadBalancerTypeEnumApplication,
						Scheme:           elbv2types.LoadBalancerSchemeEnumInternetFacing,
					},
				},
			}, nil
		},
		// waitForLoadBalancerActive calls DescribeLoadBalancers once.
		DescribeLoadBalancersFn: func(_ context.Context, _ *elbv2.DescribeLoadBalancersInput, _ ...func(*elbv2.Options)) (*elbv2.DescribeLoadBalancersOutput, error) {
			return &elbv2.DescribeLoadBalancersOutput{
				LoadBalancers: []elbv2types.LoadBalancer{
					{
						LoadBalancerArn:  aws.String(lbARN),
						LoadBalancerName: aws.String("my-lb"),
						State:            &elbv2types.LoadBalancerState{Code: elbv2types.LoadBalancerStateEnumActive},
						Scheme:           elbv2types.LoadBalancerSchemeEnumInternetFacing,
					},
				},
			}, nil
		},
	}

	m := newLBManager(fake)
	lb, err := m.CreateLoadBalancer(context.Background(), &cpi.CreateLoadBalancerRequest{
		Name:      "my-lb",
		SubnetIDs: []string{"subnet-1", "subnet-2"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lb.ID != lbARN {
		t.Errorf("ID: got %q, want %q", lb.ID, lbARN)
	}
}

func TestLBManager_CreateLoadBalancer_MissingName(t *testing.T) {
	t.Parallel()

	m := newLBManager(&lbFakeELB{})
	_, err := m.CreateLoadBalancer(context.Background(), &cpi.CreateLoadBalancerRequest{
		SubnetIDs: []string{"subnet-1", "subnet-2"},
	})
	if err == nil {
		t.Fatal("expected error for missing name")
	}
	if !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("want ErrInvalidRequest, got %v", err)
	}
}

func TestLBManager_CreateLoadBalancer_TooFewSubnets(t *testing.T) {
	t.Parallel()

	m := newLBManager(&lbFakeELB{})
	_, err := m.CreateLoadBalancer(context.Background(), &cpi.CreateLoadBalancerRequest{
		Name:      "my-lb",
		SubnetIDs: []string{"subnet-1"},
	})
	if err == nil {
		t.Fatal("expected error for fewer than 2 subnets")
	}
	if !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("want ErrInvalidRequest, got %v", err)
	}
}

// ---- GetLoadBalancer --------------------------------------------------------

func TestLBManager_GetLoadBalancer_Happy(t *testing.T) {
	t.Parallel()

	const lbARN = "arn:aws:elasticloadbalancing:us-east-1:123:loadbalancer/app/my-lb/abc"

	fake := &lbFakeELB{
		DescribeLoadBalancersFn: func(_ context.Context, _ *elbv2.DescribeLoadBalancersInput, _ ...func(*elbv2.Options)) (*elbv2.DescribeLoadBalancersOutput, error) {
			return &elbv2.DescribeLoadBalancersOutput{
				LoadBalancers: []elbv2types.LoadBalancer{
					{
						LoadBalancerArn:  aws.String(lbARN),
						LoadBalancerName: aws.String("my-lb"),
						State:            &elbv2types.LoadBalancerState{Code: elbv2types.LoadBalancerStateEnumActive},
						Scheme:           elbv2types.LoadBalancerSchemeEnumInternal,
					},
				},
			}, nil
		},
		// getTargetGroupsForLoadBalancer → DescribeListeners (no listeners → no TGs).
		DescribeListenersFn: func(_ context.Context, _ *elbv2.DescribeListenersInput, _ ...func(*elbv2.Options)) (*elbv2.DescribeListenersOutput, error) {
			return &elbv2.DescribeListenersOutput{}, nil
		},
	}

	m := newLBManager(fake)
	lb, err := m.GetLoadBalancer(context.Background(), lbARN)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lb.ID != lbARN {
		t.Errorf("ID: got %q, want %q", lb.ID, lbARN)
	}
	if lb.Name != "my-lb" {
		t.Errorf("Name: got %q, want %q", lb.Name, "my-lb")
	}
}

func TestLBManager_GetLoadBalancer_NotFound(t *testing.T) {
	t.Parallel()

	fake := &lbFakeELB{
		DescribeLoadBalancersFn: func(_ context.Context, _ *elbv2.DescribeLoadBalancersInput, _ ...func(*elbv2.Options)) (*elbv2.DescribeLoadBalancersOutput, error) {
			return &elbv2.DescribeLoadBalancersOutput{}, nil
		},
	}

	m := newLBManager(fake)
	_, err := m.GetLoadBalancer(context.Background(), "missing-lb")
	if err == nil {
		t.Fatal("expected error for missing LB")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

// ---- ListLoadBalancers ------------------------------------------------------

func TestLBManager_ListLoadBalancers_Happy(t *testing.T) {
	t.Parallel()

	callCount := 0
	fake := &lbFakeELB{
		DescribeLoadBalancersFn: func(_ context.Context, params *elbv2.DescribeLoadBalancersInput, _ ...func(*elbv2.Options)) (*elbv2.DescribeLoadBalancersOutput, error) {
			callCount++
			if params.Marker == nil {
				// First page: return one LB + marker.
				return &elbv2.DescribeLoadBalancersOutput{
					LoadBalancers: []elbv2types.LoadBalancer{
						{
							LoadBalancerArn:  aws.String("arn:1"),
							LoadBalancerName: aws.String("lb-1"),
							State:            &elbv2types.LoadBalancerState{Code: elbv2types.LoadBalancerStateEnumActive},
							Scheme:           elbv2types.LoadBalancerSchemeEnumInternal,
						},
					},
					NextMarker: aws.String("page2"),
				}, nil
			}
			// Second page: return one LB, no marker.
			return &elbv2.DescribeLoadBalancersOutput{
				LoadBalancers: []elbv2types.LoadBalancer{
					{
						LoadBalancerArn:  aws.String("arn:2"),
						LoadBalancerName: aws.String("lb-2"),
						State:            &elbv2types.LoadBalancerState{Code: elbv2types.LoadBalancerStateEnumActive},
						Scheme:           elbv2types.LoadBalancerSchemeEnumInternetFacing,
					},
				},
			}, nil
		},
	}

	m := newLBManager(fake)
	lbs, err := m.ListLoadBalancers(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(lbs) != 2 {
		t.Errorf("len: got %d, want 2", len(lbs))
	}
	if callCount != 2 {
		t.Errorf("expected 2 DescribeLoadBalancers calls (pagination), got %d", callCount)
	}
}

func TestLBManager_ListLoadBalancers_APIError(t *testing.T) {
	t.Parallel()

	fake := &lbFakeELB{
		DescribeLoadBalancersFn: func(_ context.Context, _ *elbv2.DescribeLoadBalancersInput, _ ...func(*elbv2.Options)) (*elbv2.DescribeLoadBalancersOutput, error) {
			return nil, errors.New("api error")
		},
	}

	m := newLBManager(fake)
	_, err := m.ListLoadBalancers(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---- UpdateLoadBalancer -----------------------------------------------------

func TestLBManager_UpdateLoadBalancer_Happy(t *testing.T) {
	t.Parallel()

	const lbARN = "arn:aws:elasticloadbalancing:us-east-1:123:loadbalancer/app/my-lb/abc"

	sgCalled := false
	tagCalled := false

	fake := &lbFakeELB{
		SetSecurityGroupsFn: func(_ context.Context, params *elbv2.SetSecurityGroupsInput, _ ...func(*elbv2.Options)) (*elbv2.SetSecurityGroupsOutput, error) {
			sgCalled = true
			if aws.ToString(params.LoadBalancerArn) != lbARN {
				return nil, errors.New("wrong ARN")
			}
			return &elbv2.SetSecurityGroupsOutput{}, nil
		},
		AddTagsFn: func(_ context.Context, _ *elbv2.AddTagsInput, _ ...func(*elbv2.Options)) (*elbv2.AddTagsOutput, error) {
			tagCalled = true
			return &elbv2.AddTagsOutput{}, nil
		},
	}

	m := newLBManager(fake)
	err := m.UpdateLoadBalancer(context.Background(), lbARN, &cpi.UpdateLoadBalancerRequest{
		SecurityGroups: []string{"sg-111"},
		Tags:           map[string]string{"env": "prod"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !sgCalled {
		t.Error("SetSecurityGroups not called")
	}
	if !tagCalled {
		t.Error("AddTags not called")
	}
}

func TestLBManager_UpdateLoadBalancer_SGError(t *testing.T) {
	t.Parallel()

	const lbARN = "arn:aws:elasticloadbalancing:us-east-1:123:loadbalancer/app/my-lb/abc"

	fake := &lbFakeELB{
		SetSecurityGroupsFn: func(_ context.Context, _ *elbv2.SetSecurityGroupsInput, _ ...func(*elbv2.Options)) (*elbv2.SetSecurityGroupsOutput, error) {
			return nil, errors.New("sg update failed")
		},
	}

	m := newLBManager(fake)
	err := m.UpdateLoadBalancer(context.Background(), lbARN, &cpi.UpdateLoadBalancerRequest{
		SecurityGroups: []string{"sg-bad"},
	})
	if err == nil {
		t.Fatal("expected error from SetSecurityGroups")
	}
}

// ---- DeleteLoadBalancer -----------------------------------------------------

func TestLBManager_DeleteLoadBalancer_Happy(t *testing.T) {
	t.Parallel()

	const lbARN = "arn:aws:elasticloadbalancing:us-east-1:123:loadbalancer/app/my-lb/abc"
	const tgARN = "arn:aws:elasticloadbalancing:us-east-1:123:targetgroup/my-tg/xyz"

	deleteLBCalled := false
	deleteTGCalled := false

	fake := &lbFakeELB{
		// getTargetGroupsForLoadBalancer: DescribeListeners → one listener referencing TG.
		DescribeListenersFn: func(_ context.Context, _ *elbv2.DescribeListenersInput, _ ...func(*elbv2.Options)) (*elbv2.DescribeListenersOutput, error) {
			return &elbv2.DescribeListenersOutput{
				Listeners: []elbv2types.Listener{
					{
						DefaultActions: []elbv2types.Action{
							{TargetGroupArn: aws.String(tgARN)},
						},
					},
				},
			}, nil
		},
		DescribeTargetGroupsFn: func(_ context.Context, _ *elbv2.DescribeTargetGroupsInput, _ ...func(*elbv2.Options)) (*elbv2.DescribeTargetGroupsOutput, error) {
			return &elbv2.DescribeTargetGroupsOutput{
				TargetGroups: []elbv2types.TargetGroup{
					{TargetGroupArn: aws.String(tgARN)},
				},
			}, nil
		},
		DeleteLoadBalancerFn: func(_ context.Context, _ *elbv2.DeleteLoadBalancerInput, _ ...func(*elbv2.Options)) (*elbv2.DeleteLoadBalancerOutput, error) {
			deleteLBCalled = true
			return &elbv2.DeleteLoadBalancerOutput{}, nil
		},
		DeleteTargetGroupFn: func(_ context.Context, _ *elbv2.DeleteTargetGroupInput, _ ...func(*elbv2.Options)) (*elbv2.DeleteTargetGroupOutput, error) {
			deleteTGCalled = true
			return &elbv2.DeleteTargetGroupOutput{}, nil
		},
	}

	m := newLBManager(fake)
	if err := m.DeleteLoadBalancer(context.Background(), lbARN); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !deleteLBCalled {
		t.Error("DeleteLoadBalancer not called")
	}
	if !deleteTGCalled {
		t.Error("DeleteTargetGroup not called")
	}
}

func TestLBManager_DeleteLoadBalancer_DeleteError(t *testing.T) {
	t.Parallel()

	const lbARN = "arn:aws:elasticloadbalancing:us-east-1:123:loadbalancer/app/my-lb/abc"

	fake := &lbFakeELB{
		DescribeListenersFn: func(_ context.Context, _ *elbv2.DescribeListenersInput, _ ...func(*elbv2.Options)) (*elbv2.DescribeListenersOutput, error) {
			return &elbv2.DescribeListenersOutput{}, nil
		},
		DeleteLoadBalancerFn: func(_ context.Context, _ *elbv2.DeleteLoadBalancerInput, _ ...func(*elbv2.Options)) (*elbv2.DeleteLoadBalancerOutput, error) {
			return nil, errors.New("delete failed")
		},
	}

	m := newLBManager(fake)
	err := m.DeleteLoadBalancer(context.Background(), lbARN)
	if err == nil {
		t.Fatal("expected error from DeleteLoadBalancer")
	}
}

// ---- AddBackend -------------------------------------------------------------

func TestLBManager_AddBackend_Happy(t *testing.T) {
	t.Parallel()

	const lbARN = "arn:aws:elasticloadbalancing:us-east-1:123:loadbalancer/app/my-lb/abc"
	const tgARN = "arn:aws:elasticloadbalancing:us-east-1:123:targetgroup/my-tg/xyz"

	registerCalled := false

	fake := &lbFakeELB{
		// getTargetGroupsForLoadBalancer.
		DescribeListenersFn: func(_ context.Context, _ *elbv2.DescribeListenersInput, _ ...func(*elbv2.Options)) (*elbv2.DescribeListenersOutput, error) {
			return &elbv2.DescribeListenersOutput{
				Listeners: []elbv2types.Listener{
					{DefaultActions: []elbv2types.Action{{TargetGroupArn: aws.String(tgARN)}}},
				},
			}, nil
		},
		DescribeTargetGroupsFn: func(_ context.Context, _ *elbv2.DescribeTargetGroupsInput, _ ...func(*elbv2.Options)) (*elbv2.DescribeTargetGroupsOutput, error) {
			return &elbv2.DescribeTargetGroupsOutput{
				TargetGroups: []elbv2types.TargetGroup{
					{TargetGroupArn: aws.String(tgARN)},
				},
			}, nil
		},
		RegisterTargetsFn: func(_ context.Context, params *elbv2.RegisterTargetsInput, _ ...func(*elbv2.Options)) (*elbv2.RegisterTargetsOutput, error) {
			registerCalled = true
			if aws.ToString(params.TargetGroupArn) != tgARN {
				return nil, errors.New("wrong TG ARN")
			}
			return &elbv2.RegisterTargetsOutput{}, nil
		},
	}

	m := newLBManager(fake)
	err := m.AddBackend(context.Background(), lbARN, &cpi.Backend{
		Address: "10.0.0.1",
		Port:    8080,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !registerCalled {
		t.Error("RegisterTargets not called")
	}
}

func TestLBManager_AddBackend_InvalidPort(t *testing.T) {
	t.Parallel()

	const lbARN = "arn:aws:elasticloadbalancing:us-east-1:123:loadbalancer/app/my-lb/abc"
	const tgARN = "arn:aws:elasticloadbalancing:us-east-1:123:targetgroup/my-tg/xyz"

	fake := &lbFakeELB{
		DescribeListenersFn: func(_ context.Context, _ *elbv2.DescribeListenersInput, _ ...func(*elbv2.Options)) (*elbv2.DescribeListenersOutput, error) {
			return &elbv2.DescribeListenersOutput{
				Listeners: []elbv2types.Listener{
					{DefaultActions: []elbv2types.Action{{TargetGroupArn: aws.String(tgARN)}}},
				},
			}, nil
		},
		DescribeTargetGroupsFn: func(_ context.Context, _ *elbv2.DescribeTargetGroupsInput, _ ...func(*elbv2.Options)) (*elbv2.DescribeTargetGroupsOutput, error) {
			return &elbv2.DescribeTargetGroupsOutput{
				TargetGroups: []elbv2types.TargetGroup{
					{TargetGroupArn: aws.String(tgARN)},
				},
			}, nil
		},
	}

	m := newLBManager(fake)
	err := m.AddBackend(context.Background(), lbARN, &cpi.Backend{
		Address: "10.0.0.1",
		Port:    -1,
	})
	if err == nil {
		t.Fatal("expected error for invalid port")
	}
	if !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("want ErrInvalidRequest, got %v", err)
	}
}

// ---- RemoveBackend ----------------------------------------------------------

func TestLBManager_RemoveBackend_Happy(t *testing.T) {
	t.Parallel()

	const lbARN = "arn:aws:elasticloadbalancing:us-east-1:123:loadbalancer/app/my-lb/abc"
	const tgARN = "arn:aws:elasticloadbalancing:us-east-1:123:targetgroup/my-tg/xyz"

	deregisterCalled := false

	fake := &lbFakeELB{
		DescribeListenersFn: func(_ context.Context, _ *elbv2.DescribeListenersInput, _ ...func(*elbv2.Options)) (*elbv2.DescribeListenersOutput, error) {
			return &elbv2.DescribeListenersOutput{
				Listeners: []elbv2types.Listener{
					{DefaultActions: []elbv2types.Action{{TargetGroupArn: aws.String(tgARN)}}},
				},
			}, nil
		},
		DescribeTargetGroupsFn: func(_ context.Context, _ *elbv2.DescribeTargetGroupsInput, _ ...func(*elbv2.Options)) (*elbv2.DescribeTargetGroupsOutput, error) {
			return &elbv2.DescribeTargetGroupsOutput{
				TargetGroups: []elbv2types.TargetGroup{
					{TargetGroupArn: aws.String(tgARN)},
				},
			}, nil
		},
		DeregisterTargetsFn: func(_ context.Context, params *elbv2.DeregisterTargetsInput, _ ...func(*elbv2.Options)) (*elbv2.DeregisterTargetsOutput, error) {
			deregisterCalled = true
			if aws.ToString(params.TargetGroupArn) != tgARN {
				return nil, errors.New("wrong TG ARN")
			}
			return &elbv2.DeregisterTargetsOutput{}, nil
		},
	}

	m := newLBManager(fake)
	if err := m.RemoveBackend(context.Background(), lbARN, "10.0.0.1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !deregisterCalled {
		t.Error("DeregisterTargets not called")
	}
}

// ---- EnableBackend / DisableBackend -----------------------------------------

func TestLBManager_EnableBackend_NotImplemented(t *testing.T) {
	t.Parallel()

	m := newLBManager(&lbFakeELB{})
	err := m.EnableBackend(context.Background(), "lb-1", "10.0.0.1")
	if !errors.Is(err, ErrNotImplemented) {
		t.Errorf("want ErrNotImplemented, got %v", err)
	}
}

func TestLBManager_DisableBackend_NotImplemented(t *testing.T) {
	t.Parallel()

	m := newLBManager(&lbFakeELB{})
	err := m.DisableBackend(context.Background(), "lb-1", "10.0.0.1")
	if !errors.Is(err, ErrNotImplemented) {
		t.Errorf("want ErrNotImplemented, got %v", err)
	}
}

// ---- ConfigureHealthCheck ---------------------------------------------------

func TestLBManager_ConfigureHealthCheck_Happy(t *testing.T) {
	t.Parallel()

	const lbARN = "arn:aws:elasticloadbalancing:us-east-1:123:loadbalancer/app/my-lb/abc"
	const tgARN = "arn:aws:elasticloadbalancing:us-east-1:123:targetgroup/my-tg/xyz"

	modifyCalled := false

	fake := &lbFakeELB{
		DescribeListenersFn: func(_ context.Context, _ *elbv2.DescribeListenersInput, _ ...func(*elbv2.Options)) (*elbv2.DescribeListenersOutput, error) {
			return &elbv2.DescribeListenersOutput{
				Listeners: []elbv2types.Listener{
					{DefaultActions: []elbv2types.Action{{TargetGroupArn: aws.String(tgARN)}}},
				},
			}, nil
		},
		DescribeTargetGroupsFn: func(_ context.Context, _ *elbv2.DescribeTargetGroupsInput, _ ...func(*elbv2.Options)) (*elbv2.DescribeTargetGroupsOutput, error) {
			return &elbv2.DescribeTargetGroupsOutput{
				TargetGroups: []elbv2types.TargetGroup{
					{TargetGroupArn: aws.String(tgARN)},
				},
			}, nil
		},
		ModifyTargetGroupFn: func(_ context.Context, _ *elbv2.ModifyTargetGroupInput, _ ...func(*elbv2.Options)) (*elbv2.ModifyTargetGroupOutput, error) {
			modifyCalled = true
			return &elbv2.ModifyTargetGroupOutput{}, nil
		},
	}

	m := newLBManager(fake)
	err := m.ConfigureHealthCheck(context.Background(), lbARN, &cpi.HealthCheck{
		Protocol: "HTTP",
		Path:     "/health",
		Interval: 30,
		Timeout:  5,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !modifyCalled {
		t.Error("ModifyTargetGroup not called")
	}
}

func TestLBManager_ConfigureHealthCheck_NoTargetGroups(t *testing.T) {
	t.Parallel()

	const lbARN = "arn:aws:elasticloadbalancing:us-east-1:123:loadbalancer/app/my-lb/abc"

	fake := &lbFakeELB{
		DescribeListenersFn: func(_ context.Context, _ *elbv2.DescribeListenersInput, _ ...func(*elbv2.Options)) (*elbv2.DescribeListenersOutput, error) {
			return &elbv2.DescribeListenersOutput{}, nil // no listeners → no TGs
		},
	}

	m := newLBManager(fake)
	err := m.ConfigureHealthCheck(context.Background(), lbARN, &cpi.HealthCheck{Protocol: "HTTP"})
	if err == nil {
		t.Fatal("expected error when no target groups")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

// ---- GetHealthStatus --------------------------------------------------------

func TestLBManager_GetHealthStatus_Happy(t *testing.T) {
	t.Parallel()

	const lbARN = "arn:aws:elasticloadbalancing:us-east-1:123:loadbalancer/app/my-lb/abc"
	const tgARN = "arn:aws:elasticloadbalancing:us-east-1:123:targetgroup/my-tg/xyz"

	fake := &lbFakeELB{
		DescribeListenersFn: func(_ context.Context, _ *elbv2.DescribeListenersInput, _ ...func(*elbv2.Options)) (*elbv2.DescribeListenersOutput, error) {
			return &elbv2.DescribeListenersOutput{
				Listeners: []elbv2types.Listener{
					{DefaultActions: []elbv2types.Action{{TargetGroupArn: aws.String(tgARN)}}},
				},
			}, nil
		},
		DescribeTargetGroupsFn: func(_ context.Context, _ *elbv2.DescribeTargetGroupsInput, _ ...func(*elbv2.Options)) (*elbv2.DescribeTargetGroupsOutput, error) {
			return &elbv2.DescribeTargetGroupsOutput{
				TargetGroups: []elbv2types.TargetGroup{
					{TargetGroupArn: aws.String(tgARN)},
				},
			}, nil
		},
		DescribeTargetHealthFn: func(_ context.Context, _ *elbv2.DescribeTargetHealthInput, _ ...func(*elbv2.Options)) (*elbv2.DescribeTargetHealthOutput, error) {
			return &elbv2.DescribeTargetHealthOutput{
				TargetHealthDescriptions: []elbv2types.TargetHealthDescription{
					{
						Target: &elbv2types.TargetDescription{Id: aws.String("10.0.0.1"), Port: aws.Int32(8080)},
						TargetHealth: &elbv2types.TargetHealth{
							State: elbv2types.TargetHealthStateEnumHealthy,
						},
					},
					{
						Target: &elbv2types.TargetDescription{Id: aws.String("10.0.0.2"), Port: aws.Int32(8080)},
						TargetHealth: &elbv2types.TargetHealth{
							State: elbv2types.TargetHealthStateEnumUnhealthy,
						},
					},
				},
			}, nil
		},
	}

	m := newLBManager(fake)
	status, err := m.GetHealthStatus(context.Background(), lbARN)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.Total != 2 {
		t.Errorf("Total: got %d, want 2", status.Total)
	}
	if status.Healthy != 1 {
		t.Errorf("Healthy: got %d, want 1", status.Healthy)
	}
	if status.Unhealthy != 1 {
		t.Errorf("Unhealthy: got %d, want 1", status.Unhealthy)
	}
}

// ---- waitForLoadBalancerActive (internal) -----------------------------------

func TestLBManager_WaitForLoadBalancerActive_ImmediateActive(t *testing.T) {
	t.Parallel()

	const lbARN = "arn:aws:elasticloadbalancing:us-east-1:123:loadbalancer/app/my-lb/abc"

	fake := &lbFakeELB{
		DescribeLoadBalancersFn: func(_ context.Context, _ *elbv2.DescribeLoadBalancersInput, _ ...func(*elbv2.Options)) (*elbv2.DescribeLoadBalancersOutput, error) {
			return &elbv2.DescribeLoadBalancersOutput{
				LoadBalancers: []elbv2types.LoadBalancer{
					{
						LoadBalancerArn: aws.String(lbARN),
						State:           &elbv2types.LoadBalancerState{Code: elbv2types.LoadBalancerStateEnumActive},
					},
				},
			}, nil
		},
	}

	m := newLBManager(fake)
	err := m.waitForLoadBalancerActive(context.Background(), fake, lbARN)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLBManager_WaitForLoadBalancerActive_FailedState(t *testing.T) {
	t.Parallel()

	const lbARN = "arn:aws:elasticloadbalancing:us-east-1:123:loadbalancer/app/my-lb/abc"

	fake := &lbFakeELB{
		DescribeLoadBalancersFn: func(_ context.Context, _ *elbv2.DescribeLoadBalancersInput, _ ...func(*elbv2.Options)) (*elbv2.DescribeLoadBalancersOutput, error) {
			return &elbv2.DescribeLoadBalancersOutput{
				LoadBalancers: []elbv2types.LoadBalancer{
					{
						LoadBalancerArn: aws.String(lbARN),
						State: &elbv2types.LoadBalancerState{
							Code:   elbv2types.LoadBalancerStateEnumFailed,
							Reason: aws.String("internal error"),
						},
					},
				},
			}, nil
		},
	}

	m := newLBManager(fake)
	err := m.waitForLoadBalancerActive(context.Background(), fake, lbARN)
	if err == nil {
		t.Fatal("expected error for failed state")
	}
	if !errors.Is(err, ErrLoadBalancerProvisionFailed) {
		t.Errorf("want ErrLoadBalancerProvisionFailed, got %v", err)
	}
}

func TestLBManager_WaitForLoadBalancerActive_ContextCancel(t *testing.T) {
	t.Parallel()

	const lbARN = "arn:aws:elasticloadbalancing:us-east-1:123:loadbalancer/app/my-lb/abc"

	ctx, cancel := context.WithCancel(context.Background())

	callCount := 0
	fake := &lbFakeELB{
		DescribeLoadBalancersFn: func(_ context.Context, _ *elbv2.DescribeLoadBalancersInput, _ ...func(*elbv2.Options)) (*elbv2.DescribeLoadBalancersOutput, error) {
			callCount++
			cancel() // cancel after first call
			return &elbv2.DescribeLoadBalancersOutput{
				LoadBalancers: []elbv2types.LoadBalancer{
					{
						LoadBalancerArn: aws.String(lbARN),
						State:           &elbv2types.LoadBalancerState{Code: elbv2types.LoadBalancerStateEnumProvisioning},
					},
				},
			}, nil
		},
	}

	m := newLBManager(fake)
	err := m.waitForLoadBalancerActive(ctx, fake, lbARN)
	if err == nil {
		t.Fatal("expected context-cancelled error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("want context.Canceled wrapped, got %v", err)
	}
}
