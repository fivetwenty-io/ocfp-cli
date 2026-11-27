package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fivetwenty-io/ocfp-cli/internal/cloudflare"
	"github.com/fivetwenty-io/ocfp-cli/internal/config"
	"github.com/fivetwenty-io/ocfp-cli/internal/vault"
)

const (
	dnsTestBloc    = "lab"
	dnsTestPath    = "secret/lab-operator/dns-challenge"
	dnsTestSecret  = "dns-SECRET-token-9876543210"
	dnsBlocToken   = "bloc-admin-SECRET-token"
	dnsBlocTokRef  = "secret/config/lab/cloudflare:api_token"
	dnsTraceBody   = "fl=1\nip=203.0.113.9\nts=2\n"
	dnsZonesBody   = `{"success":true,"result":[{"id":"zone-abc","name":"example.test","account":{"id":"acct-1","name":"a"}}]}`
	dnsGroupsBody  = `{"success":true,"result":[{"id":"g-dns","name":"DNS Write","scopes":["com.cloudflare.api.account.zone"]}]}`
	dnsCreatedBody = `{"success":true,"result":{"id":"tok-id","value":"` + dnsTestSecret + `"}}`
)

// dnsFakeVault is an in-memory vault. It embeds the interface, so a method
// the DNS challenge code should never call panics in a test.
type dnsFakeVault struct {
	vault.SafeInterface

	mu       sync.Mutex
	data     map[string]map[string]interface{}
	writeErr error
}

func newDNSFakeVault() *dnsFakeVault {
	return &dnsFakeVault{data: map[string]map[string]interface{}{
		"secret/config/lab/cloudflare": {"api_token": dnsBlocToken},
	}}
}

func (f *dnsFakeVault) GetAll(path string) (map[string]interface{}, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	leaf, ok := f.data[path]
	if !ok {
		return nil, vault.ErrNoSecretFoundAtPath(path)
	}

	out := make(map[string]interface{}, len(leaf))
	for k, v := range leaf {
		out[k] = v
	}

	return out, nil
}

func (f *dnsFakeVault) GetString(path, key string) (string, error) {
	leaf, err := f.GetAll(path)
	if err != nil {
		return "", err
	}

	s, _ := leaf[key].(string)

	return s, nil
}

func (f *dnsFakeVault) SetMultiple(path string, values map[string]interface{}) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.writeErr != nil {
		return f.writeErr
	}

	if f.data[path] == nil {
		f.data[path] = map[string]interface{}{}
	}

	for k, v := range values {
		f.data[path][k] = v
	}

	return nil
}

// dnsFakeNet answers the trace and the Cloudflare API from a script and
// records every request with its body.
type dnsFakeNet struct {
	mu        sync.Mutex
	trace     func() (int, string, error)
	createErr bool
	requests  []string
	bodies    map[string][]byte
}

func newDNSFakeNet() *dnsFakeNet {
	return &dnsFakeNet{
		trace:  func() (int, string, error) { return http.StatusOK, dnsTraceBody, nil },
		bodies: map[string][]byte{},
	}
}

func (n *dnsFakeNet) Do(req *http.Request) (*http.Response, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	key := req.Method + " " + req.URL.Host + req.URL.Path
	n.requests = append(n.requests, key)

	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		n.bodies[key] = b
	}

	status, body := http.StatusOK, ""

	switch key {
	case "GET api.cloudflare.com/cdn-cgi/trace":
		var err error

		status, body, err = n.trace()
		if err != nil {
			return nil, err
		}
	case "GET api.cloudflare.com/client/v4/zones":
		body = dnsZonesBody
	case "GET api.cloudflare.com/client/v4/user/tokens/permission_groups":
		body = dnsGroupsBody
	case "POST api.cloudflare.com/client/v4/user/tokens":
		if n.createErr {
			status, body = http.StatusForbidden, `{"success":false,"errors":[{"code":9109}]}`
		} else {
			body = dnsCreatedBody
		}
	default:
		status, body = http.StatusNotFound, ""
	}

	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}, nil
}

func (n *dnsFakeNet) count(key string) int {
	n.mu.Lock()
	defer n.mu.Unlock()

	c := 0

	for _, r := range n.requests {
		if r == key {
			c++
		}
	}

	return c
}

func dnsTestConfig() *config.Config {
	return &config.Config{
		Cloudflare: &config.CloudflareConfig{Zone: "example.test", APITokenVaultPath: dnsBlocTokRef},
	}
}

func runDNS(t *testing.T, store vault.SafeInterface, net *dnsFakeNet, cfg *config.Config, dryRun bool) (string, error) {
	t.Helper()

	var out bytes.Buffer

	err := runPopulateDNSChallenge(context.Background(), &out, store, cfg, dnsTestBloc, dryRun,
		dnsChallengeDeps{doer: net})

	return out.String(), err
}

func assertNoSecrets(t *testing.T, texts ...string) {
	t.Helper()

	for _, s := range texts {
		assert.NotContains(t, s, dnsTestSecret)
		assert.NotContains(t, s, dnsBlocToken)
	}
}

func TestDNSChallengePathBeneathTheOperatorTree(t *testing.T) {
	t.Parallel()

	assert.Equal(t, dnsTestPath, dnsChallengePath(dnsTestBloc))
}

// TestPopulateDNSChallengeUsesConfiguredRanges checks the air-gapped
// route: the setting is used, no trace call goes out, the token is stored
// at the operator leaf, and the output names the range but not the token.
func TestPopulateDNSChallengeUsesConfiguredRanges(t *testing.T) {
	t.Parallel()

	store, net, cfg := newDNSFakeVault(), newDNSFakeNet(), dnsTestConfig()
	cfg.EgressRanges = []string{"198.51.100.0/24", "203.0.113.7/32"}

	out, err := runDNS(t, store, net, cfg, false)
	require.NoError(t, err)

	assert.Zero(t, net.count("GET api.cloudflare.com/cdn-cgi/trace"), "a configured range makes no trace call")
	assert.Equal(t, dnsTestSecret, store.data[dnsTestPath]["token"])
	assert.Contains(t, out, "198.51.100.0/24")
	assert.Contains(t, out, "203.0.113.7/32")
	assert.Contains(t, out, "egress_ranges")
	assert.Contains(t, out, dnsTestPath)
	assertNoSecrets(t, out)

	var sent struct {
		Condition struct {
			RequestIP struct {
				In []string `json:"in"`
			} `json:"request_ip"`
		} `json:"condition"`
	}

	require.NoError(t, json.Unmarshal(net.bodies["POST api.cloudflare.com/client/v4/user/tokens"], &sent))
	assert.Equal(t, []string{"198.51.100.0/24", "203.0.113.7/32"}, sent.Condition.RequestIP.In)
}

// TestPopulateDNSChallengePinsTheTraceAddress checks the default route:
// the bastion's own address is read, printed, and pinned as the one entry.
func TestPopulateDNSChallengePinsTheTraceAddress(t *testing.T) {
	t.Parallel()

	store, net := newDNSFakeVault(), newDNSFakeNet()

	out, err := runDNS(t, store, net, dnsTestConfig(), false)
	require.NoError(t, err)

	assert.Equal(t, 1, net.count("GET api.cloudflare.com/cdn-cgi/trace"))
	assert.Contains(t, out, "203.0.113.9")
	assert.Contains(t, out, "trace")
	assert.Equal(t, dnsTestSecret, store.data[dnsTestPath]["token"])
	assertNoSecrets(t, out)

	var sent struct {
		Condition struct {
			RequestIP struct {
				In []string `json:"in"`
			} `json:"request_ip"`
		} `json:"condition"`
	}

	require.NoError(t, json.Unmarshal(net.bodies["POST api.cloudflare.com/client/v4/user/tokens"], &sent))
	assert.Equal(t, []string{"203.0.113.9"}, sent.Condition.RequestIP.In)
}

// TestPopulateDNSChallengeRefusesWhenTheTraceFails checks that an absent
// setting plus a failed trace is a coded refusal that creates and stores
// nothing.
func TestPopulateDNSChallengeRefusesWhenTheTraceFails(t *testing.T) {
	t.Parallel()

	store, net := newDNSFakeVault(), newDNSFakeNet()
	net.trace = func() (int, string, error) { return 0, "", errors.New("dial tcp: network is unreachable") }

	out, err := runDNS(t, store, net, dnsTestConfig(), false)
	require.Error(t, err)
	assert.Equal(t, cloudflare.CodeEgressUnknown, cloudflare.CodeOf(err))
	assert.Contains(t, err.Error(), "egress_ranges")
	assert.Zero(t, net.count("POST api.cloudflare.com/client/v4/user/tokens"), "no token is created without a filter")
	assert.NotContains(t, store.data, dnsTestPath)
	assertNoSecrets(t, out, err.Error())
}

// TestPopulateDNSChallengeLeavesAStoredTokenAlone checks that a rerun
// never makes a second token and makes no call at all.
func TestPopulateDNSChallengeLeavesAStoredTokenAlone(t *testing.T) {
	t.Parallel()

	store, net := newDNSFakeVault(), newDNSFakeNet()
	store.data[dnsTestPath] = map[string]interface{}{"token": "operator-made-token"}

	out, err := runDNS(t, store, net, dnsTestConfig(), false)
	require.NoError(t, err)

	assert.Empty(t, net.requests)
	assert.Equal(t, "operator-made-token", store.data[dnsTestPath]["token"])
	assert.Contains(t, out, dnsTestPath)
	assert.NotContains(t, out, "operator-made-token")
}

// TestPopulateDNSChallengeDryRunTouchesNothing checks that a dry run says
// what it would do and makes no call and no write.
func TestPopulateDNSChallengeDryRunTouchesNothing(t *testing.T) {
	t.Parallel()

	store, net := newDNSFakeVault(), newDNSFakeNet()

	out, err := runDNS(t, store, net, dnsTestConfig(), true)
	require.NoError(t, err)

	assert.Empty(t, net.requests)
	assert.NotContains(t, store.data, dnsTestPath)
	assert.Contains(t, out, "DRY RUN")
}

// TestPopulateDNSChallengeSkipsWhatItCannotDo checks the two cases that
// need no token: a bloc with no Cloudflare zone, and a bloc whose own
// Cloudflare token can't be read. Neither is an error, and neither makes a
// call.
func TestPopulateDNSChallengeSkipsWhatItCannotDo(t *testing.T) {
	t.Parallel()

	noZone := dnsTestConfig()
	noZone.Cloudflare.Zone = ""

	noToken := dnsTestConfig()
	noToken.Cloudflare.APITokenVaultPath = "secret/config/lab/missing:api_token"

	for name, cfg := range map[string]*config.Config{
		"no config":      nil,
		"no cloudflare":  {},
		"no zone":        noZone,
		"unreadable key": noToken,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			store, net := newDNSFakeVault(), newDNSFakeNet()

			out, err := runDNS(t, store, net, cfg, false)
			require.NoError(t, err)
			assert.Empty(t, net.requests)
			assert.NotContains(t, store.data, dnsTestPath)
			assertNoSecrets(t, out)
		})
	}

	t.Run("the unreadable key names the leaf to store", func(t *testing.T) {
		t.Parallel()

		out, err := runDNS(t, newDNSFakeVault(), newDNSFakeNet(), noToken, false)
		require.NoError(t, err)
		assert.Contains(t, out, dnsTestPath)
	})
}

// TestPopulateDNSChallengeRefusals checks the failures after the filter is
// known: Cloudflare refusing the token, and the vault refusing the write.
func TestPopulateDNSChallengeRefusals(t *testing.T) {
	t.Parallel()

	t.Run("cloudflare refuses the token", func(t *testing.T) {
		t.Parallel()

		store, net := newDNSFakeVault(), newDNSFakeNet()
		net.createErr = true

		out, err := runDNS(t, store, net, dnsTestConfig(), false)
		require.Error(t, err)
		assert.Equal(t, cloudflare.CodeTokenRefused, cloudflare.CodeOf(err))
		assert.NotContains(t, store.data, dnsTestPath)
		assertNoSecrets(t, out, err.Error())
	})

	t.Run("the vault refuses the write", func(t *testing.T) {
		t.Parallel()

		store, net := newDNSFakeVault(), newDNSFakeNet()
		store.writeErr = errors.New("permission denied")

		out, err := runDNS(t, store, net, dnsTestConfig(), false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ocfp-dns-challenge-"+dnsTestBloc, "the error names the stray token to delete")
		assert.Contains(t, err.Error(), dnsTestPath)
		assertNoSecrets(t, out, err.Error())
	})
}
