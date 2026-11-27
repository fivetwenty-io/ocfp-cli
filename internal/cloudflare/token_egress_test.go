package cloudflare

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// traceDoer answers the trace call with a scripted body, status, or
// transport error, and records every request it sees.
type traceDoer struct {
	body   string
	status int
	err    error
	seen   []*http.Request
}

func (d *traceDoer) Do(req *http.Request) (*http.Response, error) {
	d.seen = append(d.seen, req)

	if d.err != nil {
		return nil, d.err
	}

	status := d.status
	if status == 0 {
		status = http.StatusOK
	}

	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(d.body)),
		Header:     make(http.Header),
	}, nil
}

const traceBody = "fl=123f45\nh=api.cloudflare.com\nip=203.0.113.9\nts=1760000000.123\nvisit_scheme=https\nuag=curl/8.0\n"

// TestResolveEgressUsesConfiguredRangesWithoutACall checks the air-gapped
// case: a configured range is used as written, and the doer is never asked.
func TestResolveEgressUsesConfiguredRangesWithoutACall(t *testing.T) {
	t.Parallel()

	d := &traceDoer{err: errors.New("the network must not be touched")}

	got, err := ResolveEgress(context.Background(), d, []netip.Prefix{
		netip.MustParsePrefix("203.0.113.0/24"),
		netip.MustParsePrefix("198.51.100.7/32"),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"203.0.113.0/24", "198.51.100.7/32"}, got.Ranges)
	assert.Equal(t, EgressFromSetting, got.Source)
	assert.Empty(t, d.seen, "a configured range makes no network call")
}

// TestResolveEgressReadsTheTraceWhenUnset checks the fallback: the address
// comes from the trace body and the filter is that one address.
func TestResolveEgressReadsTheTraceWhenUnset(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"ipv4", traceBody, "203.0.113.9"},
		{"ipv6", "fl=1\nip=2001:db8::7\nts=2\n", "2001:db8::7"},
		{"carriage returns", "fl=1\r\nip=198.51.100.4\r\nts=2\r\n", "198.51.100.4"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			d := &traceDoer{body: tc.body}

			got, err := ResolveEgress(context.Background(), d, nil)
			require.NoError(t, err)
			assert.Equal(t, []string{tc.want}, got.Ranges)
			assert.Equal(t, EgressFromTrace, got.Source)
			require.Len(t, d.seen, 1)
			assert.Equal(t, http.MethodGet, d.seen[0].Method)
			assert.Equal(t, "api.cloudflare.com", d.seen[0].URL.Host)
			assert.Equal(t, "/cdn-cgi/trace", d.seen[0].URL.Path)
			assert.Empty(t, d.seen[0].Header.Get("Authorization"), "the trace call carries no credential")
		})
	}
}

// TestResolveEgressRefusesWhenTheTraceFails checks that every way the
// trace can fail ends in the coded refusal that names the setting, and
// that no range comes back.
func TestResolveEgressRefusesWhenTheTraceFails(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		d    *traceDoer
	}{
		{"transport error", &traceDoer{err: errors.New("dial tcp: no route to host")}},
		{"server error", &traceDoer{status: http.StatusBadGateway, body: "bad gateway"}},
		{"no ip line", &traceDoer{body: "fl=1\nts=2\n"}},
		{"empty body", &traceDoer{}},
		{"malformed address", &traceDoer{body: "ip=not-an-address\n"}},
		{"unspecified address", &traceDoer{body: "ip=0.0.0.0\n"}},
		{"private address", &traceDoer{body: "ip=10.0.0.4\n"}},
		{"loopback address", &traceDoer{body: "ip=127.0.0.1\n"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := ResolveEgress(context.Background(), tc.d, nil)
			require.Error(t, err)
			assert.Equal(t, CodeEgressUnknown, CodeOf(err))
			assert.Contains(t, err.Error(), "egress_ranges", "the refusal names the setting")
			assert.Empty(t, got.Ranges)
		})
	}
}

// TestResolveEgressStopsOnCanceledContext checks that a canceled context
// is a refusal and not a pinned guess.
func TestResolveEgressStopsOnCanceledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := ResolveEgress(ctx, &traceDoer{err: context.Canceled}, nil)
	require.Error(t, err)
	assert.Equal(t, CodeEgressUnknown, CodeOf(err))
}
