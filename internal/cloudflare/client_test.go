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

// capturedRequest is a recorded call, including its raw JSON body, so tests
// can assert on what was actually sent rather than just the response status.
type capturedRequest struct {
	Method string
	Path   string
	Body   []byte
}

// fakeDoer records requests and returns scripted responses keyed by "METHOD path".
type fakeDoer struct {
	responses map[string]string // body JSON
	status    map[string]int
	seen      []*http.Request
	requests  []capturedRequest
}

func (f *fakeDoer) Do(req *http.Request) (*http.Response, error) {
	f.seen = append(f.seen, req)

	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
	}

	f.requests = append(f.requests, capturedRequest{
		Method: req.Method,
		Path:   req.URL.Path,
		Body:   body,
	})

	key := req.Method + " " + req.URL.Path
	code := 200
	if f.status != nil {
		if c, ok := f.status[key]; ok {
			code = c
		}
	}
	respBody := f.responses[key]
	return &http.Response{
		StatusCode: code,
		Body:       io.NopCloser(strings.NewReader(respBody)),
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
