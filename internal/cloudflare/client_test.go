package cloudflare

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeDoer records requests and returns scripted responses keyed by "METHOD path".
type fakeDoer struct {
	responses map[string]string // body JSON
	status    map[string]int
	seen      []*http.Request
}

func (f *fakeDoer) Do(req *http.Request) (*http.Response, error) {
	f.seen = append(f.seen, req)
	key := req.Method + " " + req.URL.Path
	code := 200
	if f.status != nil {
		if c, ok := f.status[key]; ok {
			code = c
		}
	}
	body := f.responses[key]
	return &http.Response{
		StatusCode: code,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}, nil
}

func TestResolveAccountAndZone(t *testing.T) {
	t.Parallel()
	f := &fakeDoer{responses: map[string]string{
		"GET /client/v4/zones": `{"success":true,"result":[{"id":"zone-abc","name":"fivetwenty.io","account":{"id":"acct-123","name":"FiveTwenty"}}]}`,
	}}
	c := NewClient("tok", f)
	acct, zone, err := c.ResolveAccountAndZone(context.Background(), "fivetwenty.io")
	require.NoError(t, err)
	assert.Equal(t, "acct-123", acct)
	assert.Equal(t, "zone-abc", zone)
	// auth header present
	assert.Equal(t, "Bearer tok", f.seen[0].Header.Get("Authorization"))
}
