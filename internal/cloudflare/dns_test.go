package cloudflare

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// requestBody finds the first captured request for method and unmarshals its
// JSON body, so tests can assert on actual field values rather than just the
// method/path a fakeDoer response key encodes.
func requestBody(t *testing.T, f *fakeDoer, method string) map[string]any {
	t.Helper()

	for _, r := range f.requests {
		if r.Method == method {
			var payload map[string]any

			require.NoError(t, json.Unmarshal(r.Body, &payload))

			return payload
		}
	}

	t.Fatalf("no %s request captured", method)

	return nil
}

func TestUpsertCNAME_CreatesWhenAbsent(t *testing.T) {
	t.Parallel()
	f := &fakeDoer{responses: map[string]string{
		"GET /client/v4/zones/z/dns_records":  `{"success":true,"result":[]}`,
		"POST /client/v4/zones/z/dns_records": `{"success":true,"result":{"id":"rec-1"}}`,
	}}
	c := NewClient("t", f)
	err := c.UpsertCNAME(context.Background(), "z", "*.apps.ocf.wayne.lab.fivetwenty.io", "tun-1.cfargotunnel.com")
	require.NoError(t, err)
}

func TestDeleteCNAME_NotFoundIsSuccess(t *testing.T) {
	t.Parallel()
	f := &fakeDoer{responses: map[string]string{
		"GET /client/v4/zones/z/dns_records": `{"success":true,"result":[]}`,
	}}
	c := NewClient("t", f)
	// no record to delete -> nil error
	assert.NoError(t, c.DeleteCNAME(context.Background(), "z", "*.apps.ocf.wayne.lab.fivetwenty.io"))
}

func TestUpsertA_CreatesWhenAbsent(t *testing.T) {
	t.Parallel()
	f := &fakeDoer{responses: map[string]string{
		"GET /client/v4/zones/z/dns_records":  `{"success":true,"result":[]}`,
		"POST /client/v4/zones/z/dns_records": `{"success":true,"result":{"id":"rec-1"}}`,
	}}
	c := NewClient("t", f)
	require.NoError(t, c.UpsertA(context.Background(), "z", "*.wayneeseguin.lab.fivetwenty.io", "100.111.160.81"))

	created := requestBody(t, f, http.MethodPost)
	assert.Equal(t, "A", created["type"])
	assert.Equal(t, "100.111.160.81", created["content"])
	assert.InDelta(t, 60, created["ttl"], 0)
	assert.Equal(t, false, created["proxied"])
}

func TestUpsertA_NoopWhenUnchanged(t *testing.T) {
	t.Parallel()
	f := &fakeDoer{responses: map[string]string{
		"GET /client/v4/zones/z/dns_records": `{"success":true,"result":[{"id":"rec-1","type":"A","name":"*.wayneeseguin.lab.fivetwenty.io","content":"100.111.160.81"}]}`,
	}}
	c := NewClient("t", f)
	require.NoError(t, c.UpsertA(context.Background(), "z", "*.wayneeseguin.lab.fivetwenty.io", "100.111.160.81"))
}

func TestUpsertA_UpdatesWhenChanged(t *testing.T) {
	t.Parallel()
	f := &fakeDoer{responses: map[string]string{
		"GET /client/v4/zones/z/dns_records":       `{"success":true,"result":[{"id":"rec-1","type":"A","name":"*.wayneeseguin.lab.fivetwenty.io","content":"100.0.0.1"}]}`,
		"PUT /client/v4/zones/z/dns_records/rec-1": `{"success":true,"result":{"id":"rec-1"}}`,
	}}
	c := NewClient("t", f)
	require.NoError(t, c.UpsertA(context.Background(), "z", "*.wayneeseguin.lab.fivetwenty.io", "100.111.160.81"))

	updated := requestBody(t, f, http.MethodPut)
	assert.Equal(t, "A", updated["type"])
	assert.Equal(t, "100.111.160.81", updated["content"])
	assert.InDelta(t, 60, updated["ttl"], 0)
	assert.Equal(t, false, updated["proxied"])
}

func TestDeleteA_NotFoundIsSuccess(t *testing.T) {
	t.Parallel()
	f := &fakeDoer{responses: map[string]string{
		"GET /client/v4/zones/z/dns_records": `{"success":true,"result":[]}`,
	}}
	c := NewClient("t", f)
	assert.NoError(t, c.DeleteA(context.Background(), "z", "wayneeseguin.lab.fivetwenty.io"))
}
