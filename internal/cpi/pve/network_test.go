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

// TestCreateSubnet_SDNReusesExactMatchSubnet verifies the idempotency guard:
// when an SDN subnet with the exact requested CIDR already exists on the
// vnet, CreateSubnet short-circuits to a synthesized success without a POST
// (PVE rejects a duplicate create). Only an exact CIDR match may short-
// circuit — a merely-containing parent must not (see the test below).
func TestCreateSubnet_SDNReusesExactMatchSubnet(t *testing.T) {
	t.Parallel()

	cidr := "10.4.0.0/24"
	vnet := "vnet-test"

	fake := &fakePVEClient{
		getResponses: map[string]interface{}{
			"/cluster/sdn/vnets/" + vnet + "/subnets": []interface{}{
				map[string]interface{}{"subnet": cidr},
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
		CIDR:      cidr,
	})
	if err != nil {
		t.Fatalf("CreateSubnet returned error: %v", err)
	}

	if subnet == nil {
		t.Fatalf("CreateSubnet returned nil subnet")
	}

	if subnet.CIDR != cidr {
		t.Errorf("subnet CIDR = %q, want %q", subnet.CIDR, cidr)
	}

	if fake.postCalls != 0 {
		t.Errorf("expected 0 POST calls, got %d (CreateSubnet must not POST when the exact subnet exists)", fake.postCalls)
	}

	if fake.putCalls != 0 {
		t.Errorf("expected 0 PUT calls, got %d", fake.putCalls)
	}
}

// TestCreateSubnet_SDNCreatesChildDespiteContainingParent guards the lab
// incident where BOSH compilation VMs on the ocfp-* /22 bands timed out:
// the vault provider records a per-/22 gateway (.1 of each band) that only
// exists if the /22 is a real SDN subnet, but CreateSubnet treated the
// pre-existing /20 vnet subnet as already covering the /22 and skipped the
// POST — so the /22's own gateway was never provisioned. A containing
// parent is not the requested subnet; the /22 must be created alongside it
// with its own gateway and SNAT.
func TestCreateSubnet_SDNCreatesChildDespiteContainingParent(t *testing.T) {
	t.Parallel()

	parentCIDR := "10.253.16.0/20"
	childCIDR := "10.253.20.0/22"
	vnet := "ocfp"

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
		Name:      "ocfp-0",
		NetworkID: vnet,
		CIDR:      childCIDR,
		Gateway:   "10.253.20.1",
		SNAT:      true,
	})
	if err != nil {
		t.Fatalf("CreateSubnet returned error: %v", err)
	}

	if subnet == nil || subnet.CIDR != childCIDR {
		t.Fatalf("CreateSubnet result = %+v, want CIDR=%s", subnet, childCIDR)
	}

	if fake.postCalls != 1 {
		t.Fatalf("expected 1 POST call (child must be created despite containing parent), got %d", fake.postCalls)
	}

	params := fake.postParams[0]
	if params["subnet"] != childCIDR {
		t.Errorf("POST subnet = %v, want %s", params["subnet"], childCIDR)
	}

	if params["gateway"] != "10.253.20.1" {
		t.Errorf("POST gateway = %v, want 10.253.20.1", params["gateway"])
	}

	if params["snat"] != 1 {
		t.Errorf("POST snat = %v, want 1", params["snat"])
	}

	if fake.putCalls != 1 {
		t.Errorf("expected 1 PUT call (SDN apply), got %d", fake.putCalls)
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

// TestCreateNetwork_AdoptsBridgeVisibleOnlyUnderAnyBridgeFilter guards the
// production incident where bootstrap wrote an `iface ocfp` stanza into
// /etc/network/interfaces.new carrying the bloc's network address. The bloc
// bridge is supplied by PVE SDN out of /etc/network/interfaces.d/sdn, and the
// unfiltered /nodes/{node}/network listing — the one EnsureBridge consults —
// omits it. Asking the same endpoint for type=any_bridge does report it, so
// that is what the create path must consult.
//
// default_bridge is deliberately set to something else here: only the
// any_bridge lookup can keep this bridge from being written.
func TestCreateNetwork_AdoptsBridgeVisibleOnlyUnderAnyBridgeFilter(t *testing.T) {
	t.Parallel()

	fake := &fakePVEClient{
		getResponses: map[string]interface{}{
			"/nodes/pve1/network": []interface{}{
				map[string]interface{}{"iface": "vmbr0", "type": "bridge"},
				map[string]interface{}{"iface": "ens18", "type": "eth"},
			},
			"/nodes/pve1/network?type=any_bridge": []interface{}{
				map[string]interface{}{"iface": "vmbr0", "type": "bridge"},
				map[string]interface{}{"iface": "ocfp", "type": "bridge"},
			},
		},
	}

	client := newBridgeTestClient(fake)
	client.config.DefaultBridge = "vmbr0"

	netMgr := &NetworkManager{client: client}

	network, err := netMgr.CreateNetwork(context.Background(), &cpi.NetworkRequest{
		Name: "ocfp",
		CIDR: "10.111.16.0/20",
	})
	if err != nil {
		t.Fatalf("CreateNetwork(ocfp) = %v, want nil (adopt SDN-supplied bridge)", err)
	}

	if network == nil || network.ID != "ocfp" {
		t.Fatalf("CreateNetwork result = %+v, want ID=ocfp", network)
	}

	if network.CIDR != "10.111.16.0/20" {
		t.Errorf("network CIDR = %q, want the requested %q", network.CIDR, "10.111.16.0/20")
	}

	if fake.postCalls != 0 {
		t.Errorf("expected 0 POST calls, got %d (an adopted bridge must never be written)", fake.postCalls)
	}
}

// TestCreateNetwork_AdoptsConfiguredBridgeInvisibleToBothListings covers the
// case that re-arms the defect on a fresh host: SDN has not rendered the vnet
// yet, so it is absent from both the plain and the any_bridge listing. The
// bloc still named it as its default_bridge, and that is enough — a bridge
// the bloc was configured to attach to is a device someone else provides.
// Without this fallback the fix would only hold on hosts whose SDN happened
// to render before bootstrap ran.
func TestCreateNetwork_AdoptsConfiguredBridgeInvisibleToBothListings(t *testing.T) {
	t.Parallel()

	fake := &fakePVEClient{
		getResponses: map[string]interface{}{
			"/nodes/pve1/network":                 []interface{}{},
			"/nodes/pve1/network?type=any_bridge": []interface{}{},
		},
	}

	netMgr := &NetworkManager{client: newBridgeTestClient(fake)}

	_, err := netMgr.CreateNetwork(context.Background(), &cpi.NetworkRequest{
		Name: "ocfp",
		CIDR: "10.111.16.0/20",
	})
	if err != nil {
		t.Fatalf("CreateNetwork(ocfp) = %v, want nil (adopt configured bridge)", err)
	}

	if fake.postCalls != 0 {
		t.Errorf("expected 0 POST calls, got %d (unrendered SDN must not be overwritten)", fake.postCalls)
	}
}

// TestCreateNetwork_CreatesBridgeNeitherVisibleNorConfigured pins the other
// side of the adoption rule: a bridge that no listing reports and that the
// bloc never named is not somebody else's device, so the create path must
// still run.
func TestCreateNetwork_CreatesBridgeNeitherVisibleNorConfigured(t *testing.T) {
	t.Parallel()

	fake := &fakePVEClient{
		getResponses: map[string]interface{}{
			"/nodes/pve1/network":                 []interface{}{},
			"/nodes/pve1/network?type=any_bridge": []interface{}{},
		},
	}

	netMgr := &NetworkManager{client: newBridgeTestClient(fake)}

	_, err := netMgr.CreateNetwork(context.Background(), &cpi.NetworkRequest{
		Name: "vmbr9",
		CIDR: "10.111.16.0/20",
	})
	if err != nil {
		t.Fatalf("CreateNetwork(vmbr9) returned error: %v", err)
	}

	if fake.postCalls == 0 {
		t.Errorf("expected the bridge create path to POST, got %d POST calls", fake.postCalls)
	}

	// The incident itself: the request CIDR names the bloc's network, and
	// sending it as the bridge address made 10.111.16.0/20 primary on the
	// device, demoting the real gateway to secondary. NetworkRequest carries
	// no gateway, so the create path must send no address at all rather than
	// guess one.
	for _, params := range fake.postParams {
		for _, key := range []string{"cidr", "address", "netmask"} {
			if value, ok := params[key]; ok {
				t.Errorf("POST body carried %s=%v; the create path must not set an interface address", key, value)
			}
		}
	}
}

// TestListNetworks_BridgeModeReportsSDNBridges fixes the same blindness at
// the discovery end: bootstrap's existing-network lookup went through
// ListNetworks, so an SDN-supplied bridge read as absent there too and
// bootstrap fell through to the create path in the first place.
func TestListNetworks_BridgeModeReportsSDNBridges(t *testing.T) {
	t.Parallel()

	fake := &fakePVEClient{
		getResponses: map[string]interface{}{
			"/nodes/pve1/network": []interface{}{
				map[string]interface{}{"iface": "vmbr0", "type": "bridge"},
			},
			"/nodes/pve1/network?type=any_bridge": []interface{}{
				map[string]interface{}{"iface": "vmbr0", "type": "bridge"},
				map[string]interface{}{"iface": "ocfp", "type": "bridge"},
			},
		},
	}

	netMgr := &NetworkManager{client: newBridgeTestClient(fake)}

	networks, err := netMgr.ListNetworks(context.Background(), map[string]string{"name": "ocfp"})
	if err != nil {
		t.Fatalf("ListNetworks returned error: %v", err)
	}

	if len(networks) != 1 || networks[0].Name != "ocfp" {
		t.Fatalf("ListNetworks(name=ocfp) = %+v, want the SDN bridge ocfp", networks)
	}
}

// TestCreateNetwork_BridgeCreateOmitsNetworkAddress pins that a bloc CIDR is
// never handed to PVE as the bridge's interface address. The bloc CIDR names
// the network (10.111.16.0/20); the address the bridge would need is the
// gateway (10.111.16.1/20). Passing the former makes the network address
// primary on the bridge, and host-originated traffic sourced from it is
// discarded by peers. NetworkRequest carries no gateway, so there is nothing
// valid to send and the field must be omitted.
func TestCreateNetwork_BridgeCreateOmitsNetworkAddress(t *testing.T) {
	t.Parallel()

	fake := &fakePVEClient{
		getResponses: map[string]interface{}{
			"/nodes/pve1/network":                 []interface{}{},
			"/nodes/pve1/network?type=any_bridge": []interface{}{},
		},
	}

	netMgr := &NetworkManager{client: newBridgeTestClient(fake)}

	_, err := netMgr.CreateNetwork(context.Background(), &cpi.NetworkRequest{
		Name: "vmbr9",
		CIDR: "10.111.16.0/20",
	})
	if err != nil {
		t.Fatalf("CreateNetwork(vmbr9) returned error: %v", err)
	}

	for _, params := range fake.postParams {
		if got, ok := params["cidr"]; ok {
			t.Errorf("bridge create sent cidr=%v, want the field omitted", got)
		}
	}
}

// TestCreateNetwork_BridgeReloadFailureSurfaces pins that a failed network
// reload is reported. A bridge that was written but not reloaded exists only
// in /etc/network/interfaces.new; reporting success would have bootstrap
// record it as an active resource while the host has unapplied, staged
// config — the exact silent state this whole area of code got wrong before.
func TestCreateNetwork_BridgeReloadFailureSurfaces(t *testing.T) {
	t.Parallel()

	fake := &fakePVEClient{
		getResponses: map[string]interface{}{
			"/nodes/pve1/network":                 []interface{}{},
			"/nodes/pve1/network?type=any_bridge": []interface{}{},
		},
		postErr: func(_ string, params map[string]interface{}) error {
			if _, isReload := params["reload"]; isReload {
				return errFakeReloadFailed
			}

			return nil
		},
	}

	netMgr := &NetworkManager{client: newBridgeTestClient(fake)}

	_, err := netMgr.CreateNetwork(context.Background(), &cpi.NetworkRequest{
		Name: "vmbr9",
		CIDR: "10.111.16.0/20",
	})
	if !errors.Is(err, errFakeReloadFailed) {
		t.Fatalf("CreateNetwork error = %v, want it to wrap the reload failure", err)
	}
}

// newBridgeTestClient builds a bridge-mode client pinned to a single node so
// tests exercise the create path without a node-discovery round trip.
func newBridgeTestClient(fake *fakePVEClient) *Client {
	return &Client{
		config: &Config{
			NetworkMode:   "bridge",
			DefaultBridge: "ocfp",
			Node:          "pve1",
		},
		pveClient: fake,
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
	// postParams records the body of every POST so tests can assert on the
	// parameters sent, not just the call count.
	postParams []map[string]interface{}
	// postErr, when set, decides whether a given POST fails.
	postErr func(path string, params map[string]interface{}) error
}

var errFakeReloadFailed = errors.New("fakePVEClient: network reload failed")

var errFakeNoCannedResponse = errors.New("fakePVEClient: no canned response for path")

// GetCtx keys canned responses by path, plus the "type" query parameter when
// one is sent — /nodes/{node}/network answers differently depending on it,
// which is the whole point of the bridge-visibility tests.
func (f *fakePVEClient) GetCtx(_ context.Context, path string, params map[string]interface{}) (interface{}, error) {
	key := path
	if typeFilter, ok := params["type"].(string); ok {
		key = path + "?type=" + typeFilter
	}

	if resp, ok := f.getResponses[key]; ok {
		return resp, nil
	}

	return nil, errFakeNoCannedResponse
}

func (f *fakePVEClient) PostCtx(_ context.Context, path string, params map[string]interface{}) (interface{}, error) {
	f.postCalls++
	f.postParams = append(f.postParams, params)

	if f.postErr != nil {
		if err := f.postErr(path, params); err != nil {
			return nil, err
		}
	}

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
