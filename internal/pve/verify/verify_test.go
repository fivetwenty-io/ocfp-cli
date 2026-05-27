package verify_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/pve/verify"
)

// pveData wraps a data payload in the PVE API envelope.
func pveData(t *testing.T, payload interface{}) []byte {
	t.Helper()
	env := map[string]interface{}{"data": payload}
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("pveData marshal: %v", err)
	}
	return b
}

// newVerifier builds a PVEVerifier pointed at ts.URL with a fixed node and
// token auth so tests never need real PVE credentials.
func newVerifier(ts *httptest.Server) *verify.PVEVerifier {
	return &verify.PVEVerifier{
		Client:   ts.Client(),
		Base:     ts.URL,
		Node:     "pve",
		APIToken: "root@pam!test=00000000-0000-0000-0000-000000000000",
	}
}

// recordPath is a helper that records the request path and responds with body.
func serveJSON(t *testing.T, body []byte) (*httptest.Server, *string) {
	t.Helper()
	var recorded string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorded = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(ts.Close)
	return ts, &recorded
}

// ---------- VMExists ----------

// T31 TestVMExists_FoundInList — VM appears in /nodes/{node}/qemu by vmid.
func TestVMExists_FoundInList(t *testing.T) {
	vms := []map[string]interface{}{
		{"vmid": 900, "name": "other"},
		{"vmid": 901, "name": "bosh-director"},
	}
	ts, path := serveJSON(t, pveData(t, vms))
	v := newVerifier(ts)

	got, err := v.VMExists(context.Background(), "901")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Error("expected VMExists to return true for vmid 901")
	}
	if !strings.Contains(*path, "/nodes/pve/qemu") {
		t.Errorf("expected path /nodes/pve/qemu, got %q", *path)
	}
}

// T34 TestVMExists_NotInList — VM absent from list.
func TestVMExists_NotInList(t *testing.T) {
	vms := []map[string]interface{}{
		{"vmid": 900, "name": "other"},
	}
	ts, _ := serveJSON(t, pveData(t, vms))
	v := newVerifier(ts)

	got, err := v.VMExists(context.Background(), "901")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Error("expected VMExists to return false for vmid 901")
	}
}

// TestVMExists_FoundByName — VM found when matching by name field.
func TestVMExists_FoundByName(t *testing.T) {
	vms := []map[string]interface{}{
		{"vmid": 901, "name": "bosh-director"},
	}
	ts, _ := serveJSON(t, pveData(t, vms))
	v := newVerifier(ts)

	got, err := v.VMExists(context.Background(), "bosh-director")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Error("expected VMExists to return true when matching by name")
	}
}

// TestVMExists_EmptyList — empty data array returns false.
func TestVMExists_EmptyList(t *testing.T) {
	ts, _ := serveJSON(t, pveData(t, []map[string]interface{}{}))
	v := newVerifier(ts)

	got, err := v.VMExists(context.Background(), "901")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Error("expected VMExists to return false for empty list")
	}
}

// TestVMExists_EmptyNameOrID — rejects empty argument.
func TestVMExists_EmptyNameOrID(t *testing.T) {
	ts, _ := serveJSON(t, pveData(t, []map[string]interface{}{}))
	v := newVerifier(ts)

	_, err := v.VMExists(context.Background(), "")
	if err == nil {
		t.Error("expected error for empty nameOrID")
	}
}

// TestVMExists_NoNode — rejects missing Node.
func TestVMExists_NoNode(t *testing.T) {
	ts, _ := serveJSON(t, pveData(t, []map[string]interface{}{}))
	v := newVerifier(ts)
	v.Node = ""

	_, err := v.VMExists(context.Background(), "901")
	if err == nil {
		t.Error("expected error when Node is empty")
	}
}

// TestVMExists_HTTPError — propagates HTTP error status.
func TestVMExists_HTTPError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	t.Cleanup(ts.Close)
	v := newVerifier(ts)

	_, err := v.VMExists(context.Background(), "901")
	if err == nil {
		t.Error("expected error on HTTP 500")
	}
}

// ---------- VNetExists ----------

// T32 TestVNetExists_Found — vnet in /cluster/sdn/vnets.
func TestVNetExists_Found(t *testing.T) {
	vnets := []map[string]interface{}{
		{"vnet": "itvnet", "type": "vnet"},
		{"vnet": "other", "type": "vnet"},
	}
	ts, path := serveJSON(t, pveData(t, vnets))
	v := newVerifier(ts)

	got, err := v.VNetExists(context.Background(), "itvnet")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Error("expected VNetExists to return true for itvnet")
	}
	if *path != "/cluster/sdn/vnets" {
		t.Errorf("expected path /cluster/sdn/vnets, got %q", *path)
	}
}

// T35 TestVNetExists_Absent — vnet not in list.
func TestVNetExists_Absent(t *testing.T) {
	vnets := []map[string]interface{}{
		{"vnet": "other", "type": "vnet"},
	}
	ts, _ := serveJSON(t, pveData(t, vnets))
	v := newVerifier(ts)

	got, err := v.VNetExists(context.Background(), "itvnet")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Error("expected VNetExists to return false for missing vnet")
	}
}

// TestVNetExists_EmptyID — rejects empty vnetID.
func TestVNetExists_EmptyID(t *testing.T) {
	ts, _ := serveJSON(t, pveData(t, []map[string]interface{}{}))
	v := newVerifier(ts)

	_, err := v.VNetExists(context.Background(), "")
	if err == nil {
		t.Error("expected error for empty vnetID")
	}
}

// ---------- ZoneExists ----------

// T54 TestZoneExists_Present — zone in /cluster/sdn/zones.
func TestZoneExists_Present(t *testing.T) {
	zones := []map[string]interface{}{
		{"zone": "simple", "type": "simple"},
		{"zone": "other", "type": "vlan"},
	}
	ts, path := serveJSON(t, pveData(t, zones))
	v := newVerifier(ts)

	got, err := v.ZoneExists(context.Background(), "simple")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Error("expected ZoneExists to return true for zone 'simple'")
	}
	if *path != "/cluster/sdn/zones" {
		t.Errorf("expected path /cluster/sdn/zones, got %q", *path)
	}
}

// T55 TestZoneExists_Absent — zone not in list.
func TestZoneExists_Absent(t *testing.T) {
	zones := []map[string]interface{}{
		{"zone": "other", "type": "vlan"},
	}
	ts, _ := serveJSON(t, pveData(t, zones))
	v := newVerifier(ts)

	got, err := v.ZoneExists(context.Background(), "simple")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Error("expected ZoneExists to return false for missing zone")
	}
}

// TestZoneExists_EmptyZone — rejects empty zone argument.
func TestZoneExists_EmptyZone(t *testing.T) {
	ts, _ := serveJSON(t, pveData(t, []map[string]interface{}{}))
	v := newVerifier(ts)

	_, err := v.ZoneExists(context.Background(), "")
	if err == nil {
		t.Error("expected error for empty zone")
	}
}

// ---------- SubnetPresent ----------

// T56 TestSubnetPresent_Match — subnet CIDR in /cluster/sdn/vnets/{vnet}/subnets.
func TestSubnetPresent_Match(t *testing.T) {
	subnets := []map[string]interface{}{
		{"cidr": "10.250.0.0/24", "subnet": "simple-10.250.0.0-24"},
	}
	ts, path := serveJSON(t, pveData(t, subnets))
	v := newVerifier(ts)

	got, err := v.SubnetPresent(context.Background(), "itvnet", "10.250.0.0/24")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Error("expected SubnetPresent to return true for matching CIDR")
	}
	if !strings.Contains(*path, "/cluster/sdn/vnets/itvnet/subnets") {
		t.Errorf("expected path containing /cluster/sdn/vnets/itvnet/subnets, got %q", *path)
	}
}

// TestSubnetPresent_MatchByDashedID — matches when CIDR in dashed subnet ID.
func TestSubnetPresent_MatchByDashedID(t *testing.T) {
	subnets := []map[string]interface{}{
		{"cidr": "", "subnet": "simple-10.250.0.0-24"},
	}
	ts, _ := serveJSON(t, pveData(t, subnets))
	v := newVerifier(ts)

	got, err := v.SubnetPresent(context.Background(), "itvnet", "10.250.0.0/24")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Error("expected SubnetPresent to return true matching dashed subnet ID")
	}
}

// TestSubnetPresent_Absent — CIDR not in any entry.
func TestSubnetPresent_Absent(t *testing.T) {
	subnets := []map[string]interface{}{
		{"cidr": "192.168.1.0/24", "subnet": "simple-192.168.1.0-24"},
	}
	ts, _ := serveJSON(t, pveData(t, subnets))
	v := newVerifier(ts)

	got, err := v.SubnetPresent(context.Background(), "itvnet", "10.250.0.0/24")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Error("expected SubnetPresent to return false for missing CIDR")
	}
}

// TestSubnetPresent_EmptyVnet — rejects empty vnetID.
func TestSubnetPresent_EmptyVnet(t *testing.T) {
	ts, _ := serveJSON(t, pveData(t, []map[string]interface{}{}))
	v := newVerifier(ts)

	_, err := v.SubnetPresent(context.Background(), "", "10.0.0.0/24")
	if err == nil {
		t.Error("expected error for empty vnetID")
	}
}

// TestSubnetPresent_EmptyCIDR — rejects empty CIDR.
func TestSubnetPresent_EmptyCIDR(t *testing.T) {
	ts, _ := serveJSON(t, pveData(t, []map[string]interface{}{}))
	v := newVerifier(ts)

	_, err := v.SubnetPresent(context.Background(), "itvnet", "")
	if err == nil {
		t.Error("expected error for empty subnetCIDR")
	}
}

// ---------- BridgeExists ----------

// T57 TestBridgeExists_Present — bridge in /nodes/{node}/network.
func TestBridgeExists_Present(t *testing.T) {
	ifaces := []map[string]interface{}{
		{"iface": "eth0", "type": "eth"},
		{"iface": "vmbr0", "type": "bridge"},
	}
	ts, path := serveJSON(t, pveData(t, ifaces))
	v := newVerifier(ts)

	got, err := v.BridgeExists(context.Background(), "vmbr0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Error("expected BridgeExists to return true for vmbr0")
	}
	if !strings.Contains(*path, "/nodes/pve/network") {
		t.Errorf("expected path /nodes/pve/network, got %q", *path)
	}
}

// T58 TestBridgeExists_Absent — bridge not in list.
func TestBridgeExists_Absent(t *testing.T) {
	ifaces := []map[string]interface{}{
		{"iface": "eth0", "type": "eth"},
	}
	ts, _ := serveJSON(t, pveData(t, ifaces))
	v := newVerifier(ts)

	got, err := v.BridgeExists(context.Background(), "vmbr9")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Error("expected BridgeExists to return false for missing bridge")
	}
}

// TestBridgeExists_EmptyBridge — rejects empty bridge argument.
func TestBridgeExists_EmptyBridge(t *testing.T) {
	ts, _ := serveJSON(t, pveData(t, []map[string]interface{}{}))
	v := newVerifier(ts)

	_, err := v.BridgeExists(context.Background(), "")
	if err == nil {
		t.Error("expected error for empty bridge")
	}
}

// TestBridgeExists_NoNode — rejects missing Node.
func TestBridgeExists_NoNode(t *testing.T) {
	ts, _ := serveJSON(t, pveData(t, []map[string]interface{}{}))
	v := newVerifier(ts)
	v.Node = ""

	_, err := v.BridgeExists(context.Background(), "vmbr0")
	if err == nil {
		t.Error("expected error when Node is empty")
	}
}

// ---------- VolumeExists ----------

// T33 TestVolumeExists_Found — volume volid in /nodes/{node}/storage/{storage}/content.
func TestVolumeExists_Found(t *testing.T) {
	contents := []map[string]interface{}{
		{"volid": "local-lvm:vm-901-disk-0", "format": "raw"},
		{"volid": "local-lvm:vm-900-disk-0", "format": "raw"},
	}
	ts, path := serveJSON(t, pveData(t, contents))
	v := newVerifier(ts)

	got, err := v.VolumeExists(context.Background(), "local-lvm:vm-901-disk-0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Error("expected VolumeExists to return true for local-lvm:vm-901-disk-0")
	}
	if !strings.Contains(*path, "/nodes/pve/storage/local-lvm/content") {
		t.Errorf("expected path /nodes/pve/storage/local-lvm/content, got %q", *path)
	}
}

// TestVolumeExists_Absent — volume not in list.
func TestVolumeExists_Absent(t *testing.T) {
	contents := []map[string]interface{}{
		{"volid": "local-lvm:vm-900-disk-0", "format": "raw"},
	}
	ts, _ := serveJSON(t, pveData(t, contents))
	v := newVerifier(ts)

	got, err := v.VolumeExists(context.Background(), "local-lvm:vm-901-disk-0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Error("expected VolumeExists to return false for missing volume")
	}
}

// TestVolumeExists_InvalidDiskCID — rejects diskCID without colon separator.
func TestVolumeExists_InvalidDiskCID(t *testing.T) {
	ts, _ := serveJSON(t, pveData(t, []map[string]interface{}{}))
	v := newVerifier(ts)

	_, err := v.VolumeExists(context.Background(), "invalid-no-colon")
	if err == nil {
		t.Error("expected error for diskCID without storage prefix")
	}
}

// TestVolumeExists_EmptyDiskCID — rejects empty diskCID.
func TestVolumeExists_EmptyDiskCID(t *testing.T) {
	ts, _ := serveJSON(t, pveData(t, []map[string]interface{}{}))
	v := newVerifier(ts)

	_, err := v.VolumeExists(context.Background(), "")
	if err == nil {
		t.Error("expected error for empty diskCID")
	}
}

// TestVolumeExists_NoNode — rejects missing Node.
func TestVolumeExists_NoNode(t *testing.T) {
	ts, _ := serveJSON(t, pveData(t, []map[string]interface{}{}))
	v := newVerifier(ts)
	v.Node = ""

	_, err := v.VolumeExists(context.Background(), "local-lvm:vm-901-disk-0")
	if err == nil {
		t.Error("expected error when Node is empty")
	}
}

// ---------- Auth tests ----------

// TestTokenAuth_SetsAuthorizationHeader — token auth sends Authorization header.
func TestTokenAuth_SetsAuthorizationHeader(t *testing.T) {
	var gotAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveData(t, []map[string]interface{}{}))
	}))
	t.Cleanup(ts.Close)

	v := &verify.PVEVerifier{
		Client:   ts.Client(),
		Base:     ts.URL,
		Node:     "pve",
		APIToken: "root@pam!tok=secret",
	}

	_, _ = v.VMExists(context.Background(), "901")

	if !strings.HasPrefix(gotAuth, "PVEAPIToken=root@pam!tok=secret") {
		t.Errorf("expected Authorization header with PVEAPIToken, got %q", gotAuth)
	}
}

// TestTicketAuth_SendsCookieHeader — password auth sends PVEAuthCookie.
func TestTicketAuth_SendsCookieHeader(t *testing.T) {
	var gotCookie string
	callCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		callCount++
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/access/ticket") {
			// Ticket endpoint — return a ticket.
			ticketResp := map[string]interface{}{
				"data": map[string]interface{}{
					"ticket":              "PVE:test-ticket-value",
					"CSRFPreventionToken": "dummy",
				},
			}
			b, _ := json.Marshal(ticketResp)
			_, _ = w.Write(b)
			return
		}
		// List endpoint.
		gotCookie = r.Header.Get("Cookie")
		_, _ = w.Write(pveData(t, []map[string]interface{}{}))
	}))
	t.Cleanup(ts.Close)

	v := &verify.PVEVerifier{
		Client:   ts.Client(),
		Base:     ts.URL,
		Node:     "pve",
		Username: "root",
		Password: "secret",
		Realm:    "pam",
	}

	_, err := v.VMExists(context.Background(), "901")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(gotCookie, "PVEAuthCookie=PVE:test-ticket-value") {
		t.Errorf("expected Cookie header with PVEAuthCookie, got %q", gotCookie)
	}
}

// TestNoAuth_ReturnsError — no token and no credentials returns error.
func TestNoAuth_ReturnsError(t *testing.T) {
	ts, _ := serveJSON(t, pveData(t, []map[string]interface{}{}))
	v := &verify.PVEVerifier{
		Client: ts.Client(),
		Base:   ts.URL,
		Node:   "pve",
		// APIToken, Username, Password all empty.
	}

	_, err := v.VMExists(context.Background(), "901")
	if err == nil {
		t.Error("expected error when no auth configured")
	}
}

// ---------- NewVerifier constructor ----------

// TestNewVerifier_SetsFields — NewVerifier populates all fields correctly.
func TestNewVerifier_SetsFields(t *testing.T) {
	v := verify.NewVerifier("https://pve:8006/api2/json", "node1", "tok", "", "")
	if v.Base != "https://pve:8006/api2/json" {
		t.Errorf("expected Base set, got %q", v.Base)
	}
	if v.Node != "node1" {
		t.Errorf("expected Node set, got %q", v.Node)
	}
	if v.APIToken != "tok" {
		t.Errorf("expected APIToken set, got %q", v.APIToken)
	}
}
