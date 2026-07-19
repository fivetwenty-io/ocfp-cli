package bootstrap

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/vault"
)

// fakeCloudflareDoer is a minimal HTTP round-tripper stub for injecting
// scripted Cloudflare API responses (or transport errors) into
// ConfigureIngressDNS without a real network call. Keyed by "METHOD path",
// mirroring the cloudflare package's own fakeDoer test pattern.
type fakeCloudflareDoer struct {
	responses map[string]string
	status    map[string]int
	err       error
	seen      []string
}

func (f *fakeCloudflareDoer) Do(req *http.Request) (*http.Response, error) {
	if f.err != nil {
		return nil, f.err
	}

	key := req.Method + " " + req.URL.Path
	f.seen = append(f.seen, key)

	code := 200

	if f.status != nil {
		if c, ok := f.status[key]; ok {
			code = c
		}
	}

	return &http.Response{
		StatusCode: code,
		Body:       io.NopCloser(strings.NewReader(f.responses[key])),
		Header:     make(http.Header),
	}, nil
}

// seenRequests returns the "METHOD path" keys of every request the fake
// received, in order.
func (f *fakeCloudflareDoer) seenRequests() []string {
	return f.seen
}

// fakeIngressSafe is a recording SafeInterface stub: SetMultiple captures the
// path and body so tests can assert the vault write ConfigureIngressDNS
// performs without a real vault.
type fakeIngressSafe struct {
	path string
	body map[string]interface{}
}

func (f *fakeIngressSafe) Set(string, string, interface{}) error { return nil }

func (f *fakeIngressSafe) SetMultiple(path string, body map[string]interface{}) error {
	f.path = path
	f.body = body

	return nil
}

func (f *fakeIngressSafe) Get(string, string) (interface{}, error)         { return nil, nil }
func (f *fakeIngressSafe) GetAll(string) (map[string]interface{}, error)   { return nil, nil }
func (f *fakeIngressSafe) Exists(string) (bool, error)                     { return false, nil }
func (f *fakeIngressSafe) Delete(string, string) error                     { return nil }
func (f *fakeIngressSafe) List(string) ([]string, error)                   { return nil, nil }
func (f *fakeIngressSafe) Export(string) (map[string]interface{}, error)   { return nil, nil }
func (f *fakeIngressSafe) Import(string, map[string]interface{}) error     { return nil }
func (f *fakeIngressSafe) GetEngineInfo(string) (*vault.EngineInfo, error) { return nil, nil }
func (f *fakeIngressSafe) MustGet(string, string) interface{}              { return nil }
func (f *fakeIngressSafe) GetString(string, string) (string, error)        { return "", nil }
func (f *fakeIngressSafe) GetJSON(string, string) ([]byte, error)          { return nil, nil }

// ingressTestConfig returns a minimal tailscale-ingress-ready config: explicit
// provider, a cloudflare zone/token, and a base domain. Tests mutate fields
// off this to exercise skip paths.
func ingressTestConfig() *config.Config {
	return &config.Config{
		Ingress:    &config.IngressConfig{Provider: config.IngressProviderTailscale},
		Cloudflare: &config.CloudflareConfig{Zone: "example.com", APIToken: "tok"},
		FQDNs:      &config.FQDNConfig{Base: "example.com"},
	}
}

// stubTailnetStatus replaces the package-level tailnetStatusJSON var for the
// duration of the test, so discoverBastionTailnetIP resolves immediately
// instead of polling a real tailscale CLI.
func stubTailnetStatus(t *testing.T, hostname, ip string) {
	t.Helper()

	orig := tailnetStatusJSON
	tailnetStatusJSON = func(context.Context) ([]byte, error) {
		return []byte(`{"Self":{"HostName":"` + hostname + `","TailscaleIPs":["` + ip + `"]}}`), nil
	}
	t.Cleanup(func() { tailnetStatusJSON = orig })
}

func TestIngressRecordNames(t *testing.T) {
	t.Parallel()
	got := ingressRecordNames("wayneeseguin.lab.fivetwenty.io")
	want := []string{"wayneeseguin.lab.fivetwenty.io", "*.wayneeseguin.lab.fivetwenty.io"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestTailnetIPForHost_MatchesSelfAndPeers(t *testing.T) {
	t.Parallel()
	status := []byte(`{
	  "Self": {"HostName": "workstation", "TailscaleIPs": ["100.64.0.1", "fd7a::1"]},
	  "Peer": {
	    "k1": {"HostName": "ocfp-lab-wayne-bastion", "TailscaleIPs": ["100.111.160.81", "fd7a::2"], "Online": true},
	    "k2": {"HostName": "other", "TailscaleIPs": ["100.64.0.9"], "Online": true}
	  }
	}`)

	if got := tailnetIPForHost(status, "ocfp-lab-wayne-bastion"); got != "100.111.160.81" {
		t.Errorf("peer lookup = %q", got)
	}
	if got := tailnetIPForHost(status, "workstation"); got != "100.64.0.1" {
		t.Errorf("self lookup = %q", got)
	}
	if got := tailnetIPForHost(status, "absent"); got != "" {
		t.Errorf("absent lookup = %q", got)
	}
}

// TestTailnetIPForHost_SkipsOfflinePeer is the deterministic red test: a
// single peer matches the hostname but is offline (a stale device left
// behind by a destroyed VM). The old implementation ignored Online and
// returned its IP; the fix must return "" so the poller in
// discoverBastionTailnetIP keeps waiting for a live device.
func TestTailnetIPForHost_SkipsOfflinePeer(t *testing.T) {
	t.Parallel()
	status := []byte(`{
	  "Self": {"HostName": "workstation", "TailscaleIPs": ["100.64.0.1"]},
	  "Peer": {
	    "stale": {"HostName": "ocfp-lab-wayne-bastion", "TailscaleIPs": ["100.87.207.41"], "Online": false}
	  }
	}`)

	if got := tailnetIPForHost(status, "ocfp-lab-wayne-bastion"); got != "" {
		t.Errorf("offline-only peer lookup = %q, want empty", got)
	}
}

// TestTailnetIPForHost_PrefersOnlinePeer covers two peers sharing a
// HostName, one offline (stale) and one online (live). Only the online
// peer's IP must be returned regardless of map iteration order.
func TestTailnetIPForHost_PrefersOnlinePeer(t *testing.T) {
	t.Parallel()
	status := []byte(`{
	  "Self": {"HostName": "workstation", "TailscaleIPs": ["100.64.0.1"]},
	  "Peer": {
	    "stale": {"HostName": "bastion-x", "TailscaleIPs": ["100.1.1.1"], "Online": false},
	    "live": {"HostName": "bastion-x", "TailscaleIPs": ["100.2.2.2"], "Online": true}
	  }
	}`)

	if got := tailnetIPForHost(status, "bastion-x"); got != "100.2.2.2" {
		t.Errorf("mixed online/offline lookup = %q, want 100.2.2.2", got)
	}
}

func TestConfigureIngressDNS_SkipsWhenProviderNotTailscale(t *testing.T) {
	t.Parallel()
	m := &Manager{config: &config.Config{}, options: &Options{BlocName: "b"}}
	if err := m.ConfigureIngressDNS(context.Background()); err != nil {
		t.Fatalf("expected soft skip, got %v", err)
	}
}

// TestConfigureIngressDNS_ZoneResolveErrorSoftSkips is the regression test for
// finding 1: a Cloudflare API error resolving the account/zone (e.g. a
// transient 5xx) must not fail bootstrap. Before the fix this returned a
// wrapped error, which Execute treats as fatal and offers to roll back an
// otherwise healthy bootstrap.
func TestConfigureIngressDNS_ZoneResolveErrorSoftSkips(t *testing.T) {
	stubTailnetStatus(t, "b-bastion", "100.64.0.5")

	doer := &fakeCloudflareDoer{
		status: map[string]int{"GET /client/v4/zones": 500},
		responses: map[string]string{
			"GET /client/v4/zones": `{"success":false,"errors":[{"message":"internal error"}]}`,
		},
	}

	m := &Manager{
		config:         ingressTestConfig(),
		options:        &Options{BlocName: "b"},
		safe:           &fakeIngressSafe{},
		cloudflareDoer: doer,
	}

	if err := m.ConfigureIngressDNS(context.Background()); err != nil {
		t.Fatalf("expected soft skip on zone-resolve error, got %v", err)
	}
}

// TestConfigureIngressDNS_UpsertErrorSoftSkips is the regression test for
// finding 1's second error path: a Cloudflare API error upserting an A record
// must also soft-skip rather than fail bootstrap.
func TestConfigureIngressDNS_UpsertErrorSoftSkips(t *testing.T) {
	stubTailnetStatus(t, "b-bastion", "100.64.0.5")

	doer := &fakeCloudflareDoer{
		status: map[string]int{
			"GET /client/v4/zones/zone-abc/dns_records": 500,
		},
		responses: map[string]string{
			"GET /client/v4/zones":                      `{"success":true,"result":[{"id":"zone-abc","name":"example.com","account":{"id":"acct-1","name":"acct"}}]}`,
			"GET /client/v4/zones/zone-abc/dns_records": `{"success":false,"errors":[{"message":"internal error"}]}`,
		},
	}

	m := &Manager{
		config:         ingressTestConfig(),
		options:        &Options{BlocName: "b"},
		safe:           &fakeIngressSafe{},
		cloudflareDoer: doer,
	}

	if err := m.ConfigureIngressDNS(context.Background()); err != nil {
		t.Fatalf("expected soft skip on upsert error, got %v", err)
	}
}
