// Package verify provides out-of-band PVE REST API predicates independent of
// the CPI RPC channel. Used by teardown probes, pre-deploy checks, and
// lifecycle harness assertions.
package verify

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultTimeout    = 30 * time.Second
	pveAPIPathTicket  = "/access/ticket"
	pveAPIPathVMs     = "/nodes/%s/qemu"
	pveAPIPathVNets   = "/cluster/sdn/vnets"
	pveAPIPathZones   = "/cluster/sdn/zones"
	pveAPIPathSubnets = "/cluster/sdn/vnets/%s/subnets"
	pveAPIPathNetwork = "/nodes/%s/network"
	pveAPIPathContent = "/nodes/%s/storage/%s/content"
)

// HTTPClient is the interface used by PVEVerifier for HTTP transport.
// Production code uses *http.Client; tests inject httptest-backed fakes.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// PVEVerifier is an out-of-band PVE REST API checker independent of the
// CPI RPC channel. Used by teardown probes, pre-deploy checks, and
// lifecycle harness assertions.
//
// Auth priority: when APIToken is non-empty, it is sent as
// "Authorization: PVEAPIToken=<token>". Otherwise ticket auth is used:
// POST /access/ticket with Username+Password, then
// "Cookie: PVEAuthCookie=<ticket>".
//
// All existence predicates query list endpoints and test membership rather
// than per-ID GETs, mirroring the _pve_verify.py source of truth. This
// sidesteps the 404-vs-500 ambiguity of PVE per-ID GETs.
type PVEVerifier struct {
	// Client is the HTTP transport. When nil, a default client with a 30 s
	// timeout and TLS verification controlled by VerifySSL is constructed
	// lazily on first use.
	Client HTTPClient

	// Base is the API base URL, e.g. "https://pve:8006/api2/json".
	Base string

	// Node is the PVE cluster node name used for node-scoped endpoints.
	Node string

	// APIToken carries the full PVEAPIToken credential string, e.g.
	// "root@pam!mytoken=<uuid>". When non-empty, token auth is used.
	APIToken string

	// Username and Password are used for ticket auth when APIToken is empty.
	// Username may include the realm ("user@pam") or omit it; Realm is
	// appended as "@<Realm>" when Username contains no '@'.
	Username string
	Password string
	Realm    string // default "pam"

	// VerifySSL controls TLS certificate verification for the default client.
	// Ignored when Client is provided explicitly.
	VerifySSL bool

	// cached ticket for password auth; populated lazily.
	ticket string
}

// pveResponse is the envelope returned by all PVE REST API endpoints.
type pveResponse struct {
	Data json.RawMessage `json:"data"`
}

// NewVerifier constructs a PVEVerifier with sensible defaults.
// base must be the API base URL (e.g. "https://pve:8006/api2/json").
// node is the PVE node name. Either apiToken or (username + password) must
// be supplied; providing neither causes predicate calls to return an error.
func NewVerifier(base, node, apiToken, username, password string) *PVEVerifier {
	return &PVEVerifier{
		Base:     base,
		Node:     node,
		APIToken: apiToken,
		Username: username,
		Password: password,
		Realm:    "pam",
	}
}

// httpClient returns the HTTP client to use, constructing a default one on
// first call when v.Client is nil.
func (v *PVEVerifier) httpClient() HTTPClient {
	if v.Client != nil {
		return v.Client
	}

	tlsCfg := &tls.Config{
		InsecureSkipVerify: !v.VerifySSL, //nolint:gosec // lab certs are self-signed; VerifySSL=false is intentional
	}

	v.Client = &http.Client{
		Timeout: defaultTimeout,
		Transport: &http.Transport{
			TLSClientConfig: tlsCfg,
		},
	}

	return v.Client
}

// realm returns the configured realm, defaulting to "pam".
func (v *PVEVerifier) realm() string {
	if v.Realm != "" {
		return v.Realm
	}
	return "pam"
}

// usernameWithRealm mirrors _pve_verify.py: appends "@<realm>" only when
// the username has no '@' already.
func (v *PVEVerifier) usernameWithRealm() string {
	if strings.Contains(v.Username, "@") {
		return v.Username
	}
	return fmt.Sprintf("%s@%s", v.Username, v.realm())
}

// ensureTicket obtains a PVE auth ticket via password auth if one is not
// already cached. It is a no-op when token auth is in use.
func (v *PVEVerifier) ensureTicket(ctx context.Context) error {
	if v.APIToken != "" || v.ticket != "" {
		return nil
	}

	if v.Username == "" || v.Password == "" {
		return errors.New("pve verify: no APIToken and no Username/Password configured") //nolint:err113 // descriptive error, not caller-testable
	}

	body := url.Values{
		"username": {v.usernameWithRealm()},
		"password": {v.Password},
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		v.Base+pveAPIPathTicket,
		strings.NewReader(body.Encode()),
	)
	if err != nil {
		return fmt.Errorf("pve verify: build ticket request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := v.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("pve verify: ticket POST: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("pve verify: read ticket response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("pve verify: ticket auth HTTP %d: %s", resp.StatusCode, string(raw)) //nolint:err113 // descriptive error, not caller-testable
	}

	var envelope struct {
		Data struct {
			Ticket string `json:"ticket"`
		} `json:"data"`
	}
	if err = json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("pve verify: parse ticket response: %w", err)
	}
	if envelope.Data.Ticket == "" {
		return errors.New("pve verify: ticket auth returned empty ticket") //nolint:err113 // descriptive error, not caller-testable
	}

	v.ticket = envelope.Data.Ticket
	return nil
}

// get performs an authenticated GET to <base><path> and returns the parsed
// "data" field as a raw JSON message. Returns an error for any HTTP status
// outside 2xx or on transport/parse failure.
func (v *PVEVerifier) get(ctx context.Context, path string) (json.RawMessage, error) {
	if err := v.ensureTicket(ctx); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.Base+path, nil)
	if err != nil {
		return nil, fmt.Errorf("pve verify: build GET request for %s: %w", path, err)
	}

	if v.APIToken != "" {
		req.Header.Set("Authorization", "PVEAPIToken="+v.APIToken)
	} else {
		req.Header.Set("Cookie", "PVEAuthCookie="+v.ticket)
	}

	resp, err := v.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("pve verify: GET %s: %w", path, err)
	}
	defer resp.Body.Close() //nolint:errcheck

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("pve verify: read GET %s body: %w", path, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("pve verify: GET %s HTTP %d: %s", path, resp.StatusCode, string(raw)) //nolint:err113 // descriptive error, not caller-testable
	}

	var envelope pveResponse
	if err = json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("pve verify: parse GET %s response: %w", path, err)
	}

	return envelope.Data, nil
}

// asList unmarshals a raw JSON array of objects.
// A nil or JSON-null payload returns (nil, nil) — callers treat that as an
// empty list. Any other unmarshal failure propagates so callers can surface
// the ambiguity rather than silently returning "not found".
func asList(raw json.RawMessage) ([]map[string]interface{}, error) {
	if raw == nil {
		return nil, nil
	}

	// PVE occasionally returns the JSON literal null for empty lists.
	if string(raw) == "null" {
		return nil, nil
	}

	var items []map[string]interface{}
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("parse response list: %w", err)
	}

	return items, nil
}

// requireNode returns v.Node or an error when it is empty.
func (v *PVEVerifier) requireNode() (string, error) {
	if v.Node == "" {
		return "", errors.New("pve verify: Node is required for this check but was not set") //nolint:err113 // descriptive error, not caller-testable
	}
	return v.Node, nil
}

// VMExists returns true when a VM with vmid (numeric string or integer string)
// appears in the /nodes/{node}/qemu list. Matches on the "vmid" field of each
// entry, comparing as strings after stripping whitespace.
//
// nameOrID may be a numeric VMID string ("901") or a VM name — the PVE qemu
// list includes both "vmid" and "name" fields, so both are checked.
func (v *PVEVerifier) VMExists(ctx context.Context, nameOrID string) (bool, error) {
	node, err := v.requireNode()
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(nameOrID) == "" {
		return false, errors.New("pve verify VMExists: nameOrID must not be empty") //nolint:err113 // descriptive error, not caller-testable
	}

	want := strings.TrimSpace(nameOrID)
	path := fmt.Sprintf(pveAPIPathVMs, url.PathEscape(node))

	raw, err := v.get(ctx, path)
	if err != nil {
		return false, err
	}

	items, err := asList(raw)
	if err != nil {
		return false, fmt.Errorf("pve verify VMExists: parse list: %w", err)
	}

	for _, item := range items {
		// Match by vmid (numeric) or by name.
		if vmid, ok := item["vmid"]; ok {
			if strings.TrimSpace(fmt.Sprintf("%v", vmid)) == want {
				return true, nil
			}
		}
		if name, ok := item["name"]; ok {
			if strings.TrimSpace(fmt.Sprintf("%v", name)) == want {
				return true, nil
			}
		}
	}

	return false, nil
}

// VNetExists returns true when an SDN vnet with the given ID appears in the
// /cluster/sdn/vnets list. Matches on the "vnet" field.
func (v *PVEVerifier) VNetExists(ctx context.Context, vnetID string) (bool, error) {
	if strings.TrimSpace(vnetID) == "" {
		return false, errors.New("pve verify VNetExists: vnetID must not be empty") //nolint:err113 // descriptive error, not caller-testable
	}

	raw, err := v.get(ctx, pveAPIPathVNets)
	if err != nil {
		return false, err
	}

	items, err := asList(raw)
	if err != nil {
		return false, fmt.Errorf("pve verify VNetExists: parse list: %w", err)
	}

	for _, item := range items {
		if v, ok := item["vnet"]; ok && fmt.Sprintf("%v", v) == vnetID {
			return true, nil
		}
	}

	return false, nil
}

// ZoneExists returns true when an SDN zone with the given name appears in the
// /cluster/sdn/zones list. Matches on the "zone" field.
func (v *PVEVerifier) ZoneExists(ctx context.Context, zone string) (bool, error) {
	if strings.TrimSpace(zone) == "" {
		return false, errors.New("pve verify ZoneExists: zone must not be empty") //nolint:err113 // descriptive error, not caller-testable
	}

	raw, err := v.get(ctx, pveAPIPathZones)
	if err != nil {
		return false, err
	}

	items, err := asList(raw)
	if err != nil {
		return false, fmt.Errorf("pve verify ZoneExists: parse list: %w", err)
	}

	for _, item := range items {
		if z, ok := item["zone"]; ok && fmt.Sprintf("%v", z) == zone {
			return true, nil
		}
	}

	return false, nil
}

// SubnetPresent returns true when the given vnet has a subnet matching
// subnetCIDR in the /cluster/sdn/vnets/{vnet}/subnets list.
//
// Matching logic mirrors _pve_verify.py subnet_present:
//  1. Compare the stored "cidr" field directly against subnetCIDR (trimmed).
//  2. Fall back to checking whether subnetCIDR or its dashed form
//     (e.g. "10.250.0.0-24" for "10.250.0.0/24") appears in the stored
//     "subnet" ID field.
func (v *PVEVerifier) SubnetPresent(ctx context.Context, vnetID, subnetCIDR string) (bool, error) {
	if strings.TrimSpace(vnetID) == "" {
		return false, errors.New("pve verify SubnetPresent: vnetID must not be empty") //nolint:err113 // descriptive error, not caller-testable
	}
	if strings.TrimSpace(subnetCIDR) == "" {
		return false, errors.New("pve verify SubnetPresent: subnetCIDR must not be empty") //nolint:err113 // descriptive error, not caller-testable
	}

	want := strings.TrimSpace(subnetCIDR)
	wantDashed := strings.ReplaceAll(want, "/", "-")

	path := fmt.Sprintf(pveAPIPathSubnets, url.PathEscape(vnetID))

	raw, err := v.get(ctx, path)
	if err != nil {
		return false, err
	}

	items, err := asList(raw)
	if err != nil {
		return false, fmt.Errorf("pve verify SubnetPresent: parse list: %w", err)
	}

	for _, item := range items {
		storedCIDR := strings.TrimSpace(fmt.Sprintf("%v", item["cidr"]))
		storedID := strings.TrimSpace(fmt.Sprintf("%v", item["subnet"]))

		if storedCIDR == want {
			return true, nil
		}
		if strings.Contains(storedID, want) || strings.Contains(storedID, wantDashed) {
			return true, nil
		}
	}

	return false, nil
}

// BridgeExists returns true when a network interface named bridge appears in
// the /nodes/{node}/network list. Matches on the "iface" field.
func (v *PVEVerifier) BridgeExists(ctx context.Context, bridge string) (bool, error) {
	node, err := v.requireNode()
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(bridge) == "" {
		return false, errors.New("pve verify BridgeExists: bridge must not be empty") //nolint:err113 // descriptive error, not caller-testable
	}

	path := fmt.Sprintf(pveAPIPathNetwork, url.PathEscape(node))

	raw, err := v.get(ctx, path)
	if err != nil {
		return false, err
	}

	items, err := asList(raw)
	if err != nil {
		return false, fmt.Errorf("pve verify BridgeExists: parse list: %w", err)
	}

	for _, item := range items {
		if iface, ok := item["iface"]; ok && fmt.Sprintf("%v", iface) == bridge {
			return true, nil
		}
	}

	return false, nil
}

// VolumeExists returns true when a storage volume with volid == diskCID exists
// in the /nodes/{node}/storage/{storage}/content listing.
//
// diskCID must be in "<storage>:<volid>" format. The storage name is extracted
// from the prefix before the first colon and used to build the endpoint path.
// Matching is done on the "volid" field of each list entry against the full
// diskCID string.
func (v *PVEVerifier) VolumeExists(ctx context.Context, diskCID string) (bool, error) {
	node, err := v.requireNode()
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(diskCID) == "" {
		return false, errors.New("pve verify VolumeExists: diskCID must not be empty") //nolint:err113 // descriptive error, not caller-testable
	}

	idx := strings.Index(diskCID, ":")
	if idx <= 0 {
		return false, fmt.Errorf("pve verify VolumeExists: diskCID %q is not '<storage>:<volid>'", diskCID) //nolint:err113 // descriptive error, not caller-testable
	}
	storage := diskCID[:idx]

	path := fmt.Sprintf(pveAPIPathContent, url.PathEscape(node), url.PathEscape(storage))

	raw, err := v.get(ctx, path)
	if err != nil {
		return false, err
	}

	items, err := asList(raw)
	if err != nil {
		return false, fmt.Errorf("pve verify VolumeExists: parse list: %w", err)
	}

	for _, item := range items {
		if volid, ok := item["volid"]; ok && fmt.Sprintf("%v", volid) == diskCID {
			return true, nil
		}
	}

	return false, nil
}
