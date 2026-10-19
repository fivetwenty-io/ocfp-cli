package commands

import (
	"context"
	"errors"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"testing"
)

type nativeLBStub struct {
	cpi.LoadBalancerManager
	request *cpi.CreateLoadBalancerRequest
	getErr  error
}

func (s *nativeLBStub) GetLoadBalancer(context.Context, string) (*cpi.LoadBalancer, error) {
	return nil, s.getErr
}
func (s *nativeLBStub) CreateLoadBalancer(_ context.Context, req *cpi.CreateLoadBalancerRequest) (*cpi.LoadBalancer, error) {
	s.request = req
	return &cpi.LoadBalancer{ID: "lb"}, nil
}
func TestNativeLBTypedListenerContract(t *testing.T) {
	stub := &nativeLBStub{getErr: &cpi.ProviderError{Provider: "gcp", Code: "NotFound"}}
	if _, err := ensureLoadBalancerByName(context.Background(), stub, "ssh", "tcp", 2222); err != nil {
		t.Fatal(err)
	}
	if stub.request.Port != 2222 || stub.request.Protocol != "tcp" || stub.request.Type != "network" {
		t.Fatalf("lost listener %+v", stub.request)
	}
	stub.request = nil
	stub.getErr = errors.New("permission denied")
	if _, err := ensureLoadBalancerByName(context.Background(), stub, "ssh", "tcp", 2222); err == nil {
		t.Fatal("ignored lookup failure")
	}
	if stub.request != nil {
		t.Fatal("created after failed ownership lookup")
	}
}

type nativeNetworkStub struct {
	cpi.NetworkManager
	listErr, addErr error
}

func (s *nativeNetworkStub) GetBackendPools(context.Context, string) ([]*cpi.BackendPool, error) {
	return nil, s.listErr
}
func (s *nativeNetworkStub) AddBackendMember(context.Context, string, *cpi.BackendMember) error {
	return s.addErr
}
func TestNativeLBReconcilePropagatesFailure(t *testing.T) {
	stub := &nativeNetworkStub{listErr: errors.New("list denied")}
	if err := reconcileLBIPs(context.Background(), stub, &cpi.LoadBalancer{ID: "lb"}, []string{"10.0.0.1"}, 2222, true); err == nil {
		t.Fatal("ignored discovery error")
	}
	stub.listErr = nil
	stub.addErr = errors.New("attach denied")
	if err := reconcileLBIPs(context.Background(), stub, &cpi.LoadBalancer{ID: "lb"}, []string{"10.0.0.1"}, 2222, true); err == nil {
		t.Fatal("ignored attach error")
	}
}
