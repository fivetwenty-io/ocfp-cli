package pve

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"

	pveclient "github.com/fivetwenty-io/pve-apiclient-go/v3/pkg/client"
	pmetrics "github.com/fivetwenty-io/pve-apiclient-go/v3/pkg/metrics"
)

// TestDeleteNetwork_BridgeModeIsNoOp guards against the production
// incident where ocfp teardown --all deleted vmbr0/vmbr1 on the PVE host
// because state had recorded them as "managed" networks during a prior
// bootstrap (EnsureBridge adopted them). Bridges are operator infra;
// teardown must never touch them.
//
// The test calls DeleteNetwork with no underlying PVE client wired up —
// if the code attempted the actual delete it would dereference a nil
// pveClient and panic. A successful no-op means the guard short-circuits
// before any HTTP call.
func TestDeleteNetwork_BridgeModeIsNoOp(t *testing.T) {
	t.Parallel()

	client := &Client{
		config: &Config{
			NetworkMode: "bridge",
		},
	}

	netMgr := &NetworkManager{client: client}

	err := netMgr.DeleteNetwork(context.Background(), "vmbr0")
	if err != nil {
		t.Fatalf("DeleteNetwork(vmbr0) in bridge mode = %v, want nil (no-op)", err)
	}

	err = netMgr.DeleteNetwork(context.Background(), "vmbr1")
	if err != nil {
		t.Fatalf("DeleteNetwork(vmbr1) in bridge mode = %v, want nil (no-op)", err)
	}
}

// TestCreateSubnet_SDNReusesExistingVnetSubnet verifies that when CreateSubnet
// is called in SDN mode and the requested CIDR is fully contained within an
// existing SDN subnet on the parent vnet, the call short-circuits to a
// synthesized success without issuing a POST. PVE SDN simple zones present
// exactly one L3 subnet per vnet; bootstrap logically carves AZ-sized
// children out of that single CIDR. Without this guard, any caller that
// bypasses bootstrap's virtual-subnet path would attempt a duplicate create
// and the PVE API would reject it.
func TestCreateSubnet_SDNReusesExistingVnetSubnet(t *testing.T) {
	t.Parallel()

	parentCIDR := "10.4.0.0/22"
	childCIDR := "10.4.0.0/24"
	vnet := "vnet-test"

	fake := &fakePVEClient{
		getResponses: map[string]interface{}{
			"/cluster/sdn/vnets/" + vnet + "/subnets": []interface{}{
				map[string]interface{}{"subnet": parentCIDR},
			},
		},
	}

	client := &Client{
		config: &Config{
			NetworkMode: networkModeSDN,
		},
		pveClient: fake,
	}

	netMgr := &NetworkManager{client: client}

	subnet, err := netMgr.CreateSubnet(context.Background(), &cpi.SubnetRequest{
		Name:      "child",
		NetworkID: vnet,
		CIDR:      childCIDR,
	})
	if err != nil {
		t.Fatalf("CreateSubnet returned error: %v", err)
	}

	if subnet == nil {
		t.Fatalf("CreateSubnet returned nil subnet")
	}

	if subnet.CIDR != parentCIDR {
		t.Errorf("subnet CIDR = %q, want parent %q", subnet.CIDR, parentCIDR)
	}

	if fake.postCalls != 0 {
		t.Errorf("expected 0 POST calls, got %d (CreateSubnet must not POST when parent exists)", fake.postCalls)
	}

	if fake.putCalls != 0 {
		t.Errorf("expected 0 PUT calls, got %d", fake.putCalls)
	}
}

// TestCreateSubnet_SDNFallsBackToPOSTWhenNoParent verifies the cold-start
// path: if no SDN subnet exists yet for the vnet, the original POST path
// runs so first-time provisioning still works.
func TestCreateSubnet_SDNFallsBackToPOSTWhenNoParent(t *testing.T) {
	t.Parallel()

	vnet := "vnet-cold"

	fake := &fakePVEClient{
		getResponses: map[string]interface{}{
			"/cluster/sdn/vnets/" + vnet + "/subnets": []interface{}{},
		},
	}

	client := &Client{
		config: &Config{
			NetworkMode: networkModeSDN,
		},
		pveClient: fake,
	}

	netMgr := &NetworkManager{client: client}

	subnet, err := netMgr.CreateSubnet(context.Background(), &cpi.SubnetRequest{
		Name:      "child",
		NetworkID: vnet,
		CIDR:      "10.4.0.0/24",
	})
	if err != nil {
		t.Fatalf("CreateSubnet returned error: %v", err)
	}

	if subnet == nil || subnet.CIDR != "10.4.0.0/24" {
		t.Fatalf("CreateSubnet result = %+v, want CIDR=10.4.0.0/24", subnet)
	}

	if fake.postCalls != 1 {
		t.Errorf("expected 1 POST call (fallback), got %d", fake.postCalls)
	}
}

// fakePVEClient is a minimal stub of the pve.Client interface for tests that
// need to assert which HTTP verbs were issued by NetworkManager. Methods we
// don't exercise return zero values; the ones we do (GetCtx, PostCtx,
// PutCtx) record call counts and return canned data keyed by path.
type fakePVEClient struct {
	getResponses map[string]interface{}
	postCalls    int
	putCalls     int
	deleteCalls  int
}

var errFakeNoCannedResponse = errors.New("fakePVEClient: no canned response for path")

func (f *fakePVEClient) GetCtx(_ context.Context, path string, _ map[string]interface{}) (interface{}, error) {
	if resp, ok := f.getResponses[path]; ok {
		return resp, nil
	}

	return nil, errFakeNoCannedResponse
}

func (f *fakePVEClient) PostCtx(_ context.Context, _ string, _ map[string]interface{}) (interface{}, error) {
	f.postCalls++

	return nil, nil //nolint:nilnil // test stub
}

func (f *fakePVEClient) PutCtx(_ context.Context, _ string, _ map[string]interface{}) (interface{}, error) {
	f.putCalls++

	return nil, nil //nolint:nilnil // test stub
}

func (f *fakePVEClient) DeleteCtx(_ context.Context, _ string, _ map[string]interface{}) (interface{}, error) {
	f.deleteCalls++

	return nil, nil //nolint:nilnil // test stub
}

// Unused interface methods — zero-value stubs to satisfy pve.Client.

func (f *fakePVEClient) Get(_ string, _ map[string]interface{}) (interface{}, error) {
	return nil, nil //nolint:nilnil // test stub
}

func (f *fakePVEClient) GetRaw(_ string, _ map[string]interface{}) (*pveclient.Response, error) {
	return nil, nil //nolint:nilnil // test stub
}

func (f *fakePVEClient) Post(_ string, _ map[string]interface{}) (interface{}, error) {
	return nil, nil //nolint:nilnil // test stub
}

func (f *fakePVEClient) PostRaw(_ string, _ map[string]interface{}) (*pveclient.Response, error) {
	return nil, nil //nolint:nilnil // test stub
}

func (f *fakePVEClient) Put(_ string, _ map[string]interface{}) (interface{}, error) {
	return nil, nil //nolint:nilnil // test stub
}

func (f *fakePVEClient) PutRaw(_ string, _ map[string]interface{}) (*pveclient.Response, error) {
	return nil, nil //nolint:nilnil // test stub
}

func (f *fakePVEClient) Delete(_ string, _ map[string]interface{}) (interface{}, error) {
	return nil, nil //nolint:nilnil // test stub
}

func (f *fakePVEClient) DeleteRaw(_ string, _ map[string]interface{}) (*pveclient.Response, error) {
	return nil, nil //nolint:nilnil // test stub
}

func (f *fakePVEClient) GetRawCtx(_ context.Context, _ string, _ map[string]interface{}) (*pveclient.Response, error) {
	return nil, nil //nolint:nilnil // test stub
}

func (f *fakePVEClient) PostRawCtx(_ context.Context, _ string, _ map[string]interface{}) (*pveclient.Response, error) {
	return nil, nil //nolint:nilnil // test stub
}

func (f *fakePVEClient) PutRawCtx(_ context.Context, _ string, _ map[string]interface{}) (*pveclient.Response, error) {
	return nil, nil //nolint:nilnil // test stub
}

func (f *fakePVEClient) DeleteRawCtx(_ context.Context, _ string, _ map[string]interface{}) (*pveclient.Response, error) {
	return nil, nil //nolint:nilnil // test stub
}

func (f *fakePVEClient) UploadCtx(_ context.Context, _ string, _ map[string]string, _, _ string, _ io.Reader) (*pveclient.Response, error) {
	return nil, nil //nolint:nilnil // test stub
}

func (f *fakePVEClient) Login() error                          { return nil }
func (f *fakePVEClient) Logout() error                         { return nil }
func (f *fakePVEClient) UpdateTicket(_ string)                 {}
func (f *fakePVEClient) UpdateCSRFToken(_ string)              {}
func (f *fakePVEClient) SetTimeout(_ time.Duration)            {}
func (f *fakePVEClient) SetKeepAlive(_ int)                    {}
func (f *fakePVEClient) SetLogger(_ pveclient.Logger)          {}
func (f *fakePVEClient) SetLogConfig(_ pveclient.LogConfig)    {}
func (f *fakePVEClient) AddLogHook(_ pveclient.Hook)           {}
func (f *fakePVEClient) GetLogConfig() pveclient.LogConfig     { return pveclient.LogConfig{} }
func (f *fakePVEClient) SetMetrics(_ *pmetrics.DefaultMetrics) {}
func (f *fakePVEClient) SetTFAHandler(_ pveclient.TFAHandler)  {}
func (f *fakePVEClient) InvalidateCache(_ string) int          { return 0 }
func (f *fakePVEClient) ClearCache()                           {}
func (f *fakePVEClient) CacheStats() *pveclient.CacheStats     { return nil }

// Compile-time interface conformance.
var _ pveclient.Client = (*fakePVEClient)(nil)
