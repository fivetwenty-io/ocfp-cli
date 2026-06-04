package cloudflare

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

// Tunnel is a created-or-fetched cfd tunnel plus its connector token.
type Tunnel struct {
	ID    string
	Token string
}

// IngressRule mirrors the cloudflared remotely-managed ingress schema.
type IngressRule struct {
	Hostname      string         `json:"hostname,omitempty"`
	Service       string         `json:"service"`
	OriginRequest *OriginRequest `json:"originRequest,omitempty"`
}

type OriginRequest struct {
	NoTLSVerify      bool   `json:"noTLSVerify,omitempty"`
	OriginServerName string `json:"originServerName,omitempty"`
}

// EnsureTunnel returns the tunnel named name, creating it (remotely-managed,
// config_src=cloudflare) if absent. Idempotent across re-bootstrap.
func (c *Client) EnsureTunnel(ctx context.Context, accountID, name string) (Tunnel, error) {
	base := "/accounts/" + accountID + "/cfd_tunnel"
	var existing []idName
	if err := c.do(ctx, http.MethodGet, base+"?name="+url.QueryEscape(name)+"&is_deleted=false", nil, &existing); err != nil {
		return Tunnel{}, fmt.Errorf("list tunnels: %w", err)
	}
	var id string
	for _, t := range existing {
		if t.Name == name {
			id = t.ID
			break
		}
	}
	if id == "" {
		var created idName
		body := map[string]any{"name": name, "config_src": "cloudflare"}
		if err := c.do(ctx, http.MethodPost, base, body, &created); err != nil {
			return Tunnel{}, fmt.Errorf("create tunnel %q: %w", name, err)
		}
		id = created.ID
	}
	var token string
	if err := c.do(ctx, http.MethodGet, base+"/"+id+"/token", nil, &token); err != nil {
		return Tunnel{}, fmt.Errorf("get tunnel token: %w", err)
	}
	return Tunnel{ID: id, Token: token}, nil
}

// IngressParams are the inputs to BuildIngress.
type IngressParams struct {
	AppsDomain       string
	SystemDomain     string
	SSHHostname      string
	Origin           string
	SSHOrigin        string
	OriginServerName string

	// OriginNoTLSVerify disables TLS verification to the origin on the *.system
	// rule (which otherwise verifies via OriginServerName). Required when the
	// origin presents a self-signed cert (e.g. the PVE lab haproxy); without it
	// cloudflared 502s. *.apps is always noTLSVerify regardless.
	OriginNoTLSVerify bool
}

// BuildIngress returns the ordered ingress rules: *.apps (noTLSVerify),
// *.system (originServerName, or noTLSVerify when OriginNoTLSVerify is set),
// ssh hostname (tcp), and the required catch-all.
func BuildIngress(p IngressParams) []IngressRule {
	systemOrigin := &OriginRequest{OriginServerName: p.OriginServerName}
	if p.OriginNoTLSVerify {
		// Self-signed origin: skip verification (cert name is irrelevant then).
		systemOrigin = &OriginRequest{NoTLSVerify: true}
	}
	rules := []IngressRule{
		{
			Hostname:      "*." + p.AppsDomain,
			Service:       p.Origin,
			OriginRequest: &OriginRequest{NoTLSVerify: true},
		},
		{
			Hostname:      "*." + p.SystemDomain,
			Service:       p.Origin,
			OriginRequest: systemOrigin,
		},
	}
	if p.SSHHostname != "" && p.SSHOrigin != "" {
		rules = append(rules, IngressRule{Hostname: p.SSHHostname, Service: p.SSHOrigin})
	}
	rules = append(rules, IngressRule{Service: "http_status:404"})
	return rules
}

// PutTunnelConfig pushes remotely-managed ingress for the tunnel.
func (c *Client) PutTunnelConfig(ctx context.Context, accountID, tunnelID string, ingress []IngressRule) error {
	path := "/accounts/" + accountID + "/cfd_tunnel/" + tunnelID + "/configurations"
	body := map[string]any{"config": map[string]any{"ingress": ingress}}
	if err := c.do(ctx, http.MethodPut, path, body, nil); err != nil {
		return fmt.Errorf("put tunnel config: %w", err)
	}
	return nil
}

// DeleteTunnel removes connector connections then the tunnel. ErrNotFound is
// treated as success (idempotent teardown).
func (c *Client) DeleteTunnel(ctx context.Context, accountID, tunnelID string) error {
	base := "/accounts/" + accountID + "/cfd_tunnel/" + tunnelID
	// Best-effort: clean stale connections so delete is not blocked.
	_ = c.do(ctx, http.MethodDelete, base+"/connections", nil, nil)
	if err := c.do(ctx, http.MethodDelete, base, nil, nil); err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("delete tunnel: %w", err)
	}
	return nil
}
