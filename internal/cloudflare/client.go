package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const apiBase = "https://api.cloudflare.com/client/v4"

// httpDoer is the seam tests inject a fake through.
type httpDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Client is a thin Cloudflare v4 API client.
type Client struct {
	token string
	http  httpDoer
}

// NewClient builds a Client. Pass http.DefaultClient (or a timeout client) in
// production; a fake in tests. A nil doer defaults to a 30s-timeout client.
func NewClient(token string, doer httpDoer) *Client {
	if doer == nil {
		doer = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{token: token, http: doer}
}

type apiEnvelope struct {
	Success bool              `json:"success"`
	Errors  []json.RawMessage `json:"errors"`
	Result  json.RawMessage   `json:"result"`
}

// do issues a request and unmarshals result into out (may be nil). Non-2xx or
// success:false is an error. A 404 returns ErrNotFound so deletes are idempotent.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal %s %s: %w", method, path, err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, apiBase+path, rdr)
	if err != nil {
		return fmt.Errorf("build request %s %s: %w", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	var env apiEnvelope
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &env); err != nil {
			return fmt.Errorf("%s %s: decode: %w (status %d)", method, path, err, resp.StatusCode)
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !env.Success {
		return fmt.Errorf("%s %s: status %d: %s", method, path, resp.StatusCode, string(raw))
	}
	if out != nil && len(env.Result) > 0 {
		if err := json.Unmarshal(env.Result, out); err != nil {
			return fmt.Errorf("%s %s: decode result: %w", method, path, err)
		}
	}
	return nil
}

// ErrNotFound signals a 404 (used so delete is idempotent).
var ErrNotFound = errors.New("cloudflare: not found")

type idName struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// zoneResult is a zone record including the account that owns it. Deriving the
// account from the zone (rather than guessing the first account the token can
// see) keeps multi-account tokens correct.
type zoneResult struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Account idName `json:"account"`
}

// ResolveAccountAndZone returns the account id that owns the named zone and the
// zone id, both read from the zone record.
func (c *Client) ResolveAccountAndZone(ctx context.Context, zone string) (accountID, zoneID string, err error) {
	var zones []zoneResult
	if err = c.do(ctx, http.MethodGet, "/zones?name="+url.QueryEscape(zone), nil, &zones); err != nil {
		return "", "", fmt.Errorf("resolve zone %q: %w", zone, err)
	}
	if len(zones) == 0 {
		return "", "", fmt.Errorf("cloudflare: zone %q not found (token access?)", zone)
	}
	if zones[0].Account.ID == "" {
		return "", "", fmt.Errorf("cloudflare: zone %q response has no account id", zone)
	}
	return zones[0].Account.ID, zones[0].ID, nil
}
