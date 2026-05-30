package cloudflare

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

type dnsRecord struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
}

// UpsertCNAME creates or updates a proxied CNAME name -> target. Idempotent.
func (c *Client) UpsertCNAME(ctx context.Context, zoneID, name, target string) error {
	q := url.Values{}
	q.Set("type", "CNAME")
	q.Set("name", name)
	list := "/zones/" + zoneID + "/dns_records?" + q.Encode()
	var existing []dnsRecord
	if err := c.do(ctx, http.MethodGet, list, nil, &existing); err != nil {
		return fmt.Errorf("list dns %q: %w", name, err)
	}
	payload := map[string]any{
		"type": "CNAME", "name": name, "content": target,
		"ttl": 1, "proxied": true,
	}
	if len(existing) > 0 {
		if existing[0].Content == target {
			return nil
		}
		path := "/zones/" + zoneID + "/dns_records/" + existing[0].ID
		if err := c.do(ctx, http.MethodPut, path, payload, nil); err != nil {
			return fmt.Errorf("update dns %q: %w", name, err)
		}
		return nil
	}
	if err := c.do(ctx, http.MethodPost, "/zones/"+zoneID+"/dns_records", payload, nil); err != nil {
		return fmt.Errorf("create dns %q: %w", name, err)
	}
	return nil
}

// DeleteCNAME removes the CNAME named name if present. Absent = success.
func (c *Client) DeleteCNAME(ctx context.Context, zoneID, name string) error {
	q := url.Values{}
	q.Set("type", "CNAME")
	q.Set("name", name)
	list := "/zones/" + zoneID + "/dns_records?" + q.Encode()
	var existing []dnsRecord
	if err := c.do(ctx, http.MethodGet, list, nil, &existing); err != nil {
		return fmt.Errorf("list dns %q: %w", name, err)
	}
	if len(existing) == 0 {
		return nil
	}
	path := "/zones/" + zoneID + "/dns_records/" + existing[0].ID
	if err := c.do(ctx, http.MethodDelete, path, nil, nil); err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("delete dns %q: %w", name, err)
	}
	return nil
}
