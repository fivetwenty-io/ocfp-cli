package cloudflare

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"time"
)

// Refusal codes of the DNS challenge token.
const (
	// CodeEgressUnknown refuses a token because no egress range is known,
	// which the operator fixes by setting egress_ranges.
	CodeEgressUnknown = "DNS_CHALLENGE_EGRESS_UNKNOWN"
	// CodeTokenInvalid refuses a token request that has no name or no zone.
	CodeTokenInvalid = "DNS_CHALLENGE_TOKEN_INVALID"
	// CodeTokenRefused reports that Cloudflare would not make the token,
	// usually because the bloc's own token may not create tokens. The
	// operator then makes the token once in the dashboard and stores it.
	CodeTokenRefused = "DNS_CHALLENGE_TOKEN_REFUSED"
)

// zoneScope is the scope that marks a permission group as zone-wide, and
// the prefix of a single zone's resource name.
const zoneScope = "com.cloudflare.api.account.zone"

// dnsEditGroup is the name of the permission group that edits DNS records.
const dnsEditGroup = "DNS Write"

// traceURL is Cloudflare's trace endpoint, which answers with the address
// that the caller's request arrived from.
const traceURL = "https://api.cloudflare.com/cdn-cgi/trace"

// traceTimeout bounds the trace call when the caller passes no doer.
const traceTimeout = 15 * time.Second

// traceLimit caps how much of the trace answer ocfp reads.
const traceLimit = 64 * 1024

// Where an Egress came from.
const (
	// EgressFromSetting marks ranges that the bloc's egress_ranges setting
	// supplied, which took no network call.
	EgressFromSetting = "egress_ranges"
	// EgressFromTrace marks the one address that Cloudflare's trace
	// endpoint reported for this host.
	EgressFromTrace = "trace"
)

// TokenError is a coded refusal of the DNS challenge token. Its detail
// never holds a token or any other secret.
type TokenError struct {
	Code   string
	Detail string
}

func (e *TokenError) Error() string { return e.Code + ": " + e.Detail }

// CodeOf returns the code of the first *TokenError in err's chain, or the
// empty string when err holds none.
func CodeOf(err error) string {
	var e *TokenError

	if errors.As(err, &e) {
		return e.Code
	}

	return ""
}

// Egress is the set of addresses that the token's client IP filter admits,
// and where the set came from.
type Egress struct {
	// Ranges holds CIDRs, or bare addresses for the one address the trace
	// reported.
	Ranges []string
	Source string
}

// ResolveEgress decides the client IP filter of the DNS challenge token.
// A configured range wins, and then no network call is made, which is how
// an air-gapped site works. Without one, it asks the trace endpoint for
// this host's egress address and pins the filter to that one address. When
// that fails, it refuses with CodeEgressUnknown and returns no range, so a
// caller can never create a token without a filter. A nil doer makes a
// default client with a timeout.
func ResolveEgress(ctx context.Context, doer httpDoer, configured []netip.Prefix) (Egress, error) {
	if len(configured) > 0 {
		ranges := make([]string, 0, len(configured))

		for _, p := range configured {
			ranges = append(ranges, p.Masked().String())
		}

		return Egress{Ranges: ranges, Source: EgressFromSetting}, nil
	}

	addr, err := traceAddress(ctx, doer)
	if err != nil {
		return Egress{}, &TokenError{
			Code: CodeEgressUnknown,
			Detail: "this host's egress address could not be read from Cloudflare's trace endpoint (" +
				err.Error() + "); set egress_ranges in the bloc's config to the CIDRs that this bloc leaves from, " +
				"and no token was created",
		}
	}

	return Egress{Ranges: []string{addr.String()}, Source: EgressFromTrace}, nil
}

// traceAddress reads this host's public egress address from the trace
// endpoint. It accepts only an address that the public internet could have
// seen, so a private, loopback, or unspecified answer is a failure.
func traceAddress(ctx context.Context, doer httpDoer) (netip.Addr, error) {
	if doer == nil {
		doer = &http.Client{Timeout: traceTimeout}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, traceURL, nil)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("building the trace request: %w", err)
	}

	resp, err := doer.Do(req)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("calling the trace endpoint: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return netip.Addr{}, fmt.Errorf("the trace endpoint answered status %d", resp.StatusCode)
	}

	scanner := bufio.NewScanner(io.LimitReader(resp.Body, traceLimit))

	for scanner.Scan() {
		value, found := strings.CutPrefix(strings.TrimSpace(scanner.Text()), "ip=")
		if !found {
			continue
		}

		addr, err := netip.ParseAddr(strings.TrimSpace(value))
		if err != nil {
			return netip.Addr{}, errors.New("the trace endpoint's ip line is not an address")
		}

		addr = addr.Unmap()

		if !addr.IsGlobalUnicast() || addr.IsPrivate() {
			return netip.Addr{}, errors.New("the trace endpoint reported an address that is not public")
		}

		return addr, nil
	}

	err = scanner.Err()
	if err != nil {
		return netip.Addr{}, fmt.Errorf("reading the trace answer: %w", err)
	}

	return netip.Addr{}, errors.New("the trace answer has no ip line")
}

// permissionGroup is one entry of Cloudflare's permission group listing.
type permissionGroup struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Scopes []string `json:"scopes"`
}

// tokenPolicy is one policy of a token request.
type tokenPolicy struct {
	Effect           string            `json:"effect"`
	Resources        map[string]string `json:"resources"`
	PermissionGroups []tokenGroupRef   `json:"permission_groups"`
}

type tokenGroupRef struct {
	ID string `json:"id"`
}

type tokenCondition struct {
	RequestIP tokenRequestIP `json:"request_ip"`
}

type tokenRequestIP struct {
	In []string `json:"in"`
}

type tokenRequest struct {
	Name      string         `json:"name"`
	Policies  []tokenPolicy  `json:"policies"`
	Condition tokenCondition `json:"condition"`
}

// createdToken is the part of the creation answer that ocfp reads. The
// value is the secret, and Cloudflare shows it only once.
type createdToken struct {
	ID    string `json:"id"`
	Value string `json:"value"`
}

// checkFilter refuses an empty filter and any entry that isn't an address
// or a CIDR that narrows it. It returns the entries trimmed.
func checkFilter(egress []string) ([]string, error) {
	if len(egress) == 0 {
		return nil, &TokenError{
			Code:   CodeEgressUnknown,
			Detail: "no client IP filter was given, and a token is never made without one; set egress_ranges in the bloc's config",
		}
	}

	out := make([]string, 0, len(egress))

	for i, raw := range egress {
		entry := strings.TrimSpace(raw)

		if prefix, err := netip.ParsePrefix(entry); err == nil {
			if prefix.Bits() == 0 {
				return nil, &TokenError{
					Code:   CodeEgressUnknown,
					Detail: fmt.Sprintf("client IP filter entry %d matches every address, so it would not filter", i),
				}
			}

			out = append(out, entry)

			continue
		}

		if _, err := netip.ParseAddr(entry); err != nil {
			return nil, &TokenError{
				Code:   CodeEgressUnknown,
				Detail: fmt.Sprintf("client IP filter entry %d is not an address or a CIDR; check egress_ranges in the bloc's config", i),
			}
		}

		out = append(out, entry)
	}

	return out, nil
}

// refused builds the refusal for a failed Cloudflare call. It removes the
// bloc's own token from the text, in case an answer ever echoed it.
func (c *Client) refused(step string, err error) error {
	text := err.Error()
	if c.token != "" {
		text = strings.ReplaceAll(text, c.token, "[token]")
	}

	return &TokenError{
		Code: CodeTokenRefused,
		Detail: step + " failed (" + text + "); if the bloc's Cloudflare token may not create tokens, " +
			"make the DNS challenge token once in the Cloudflare dashboard and store it instead",
	}
}

// CreateDNSChallengeToken makes the token that lets the managing system
// write the DNS records of an ACME challenge. The token may edit DNS
// records in the one zone and nothing else, and Cloudflare accepts it only
// from the addresses in egress. It returns the token's value, the one
// secret, and no error ever carries it. It refuses before any call when
// egress is empty or has an entry that filters nothing.
func (c *Client) CreateDNSChallengeToken(ctx context.Context, name, zoneID string, egress []string) (string, error) {
	filter, err := checkFilter(egress)
	if err != nil {
		return "", err
	}

	name, zoneID = strings.TrimSpace(name), strings.TrimSpace(zoneID)
	if name == "" || zoneID == "" {
		return "", &TokenError{Code: CodeTokenInvalid, Detail: "a token needs a name and a zone id"}
	}

	var groups []permissionGroup

	err = c.do(ctx, http.MethodGet, "/user/tokens/permission_groups", nil, &groups)
	if err != nil {
		return "", c.refused("listing Cloudflare's permission groups", err)
	}

	groupID := ""

	for _, g := range groups {
		if g.Name == dnsEditGroup && g.ID != "" && slices.Contains(g.Scopes, zoneScope) {
			groupID = g.ID

			break
		}
	}

	if groupID == "" {
		return "", c.refused("finding the zone-scoped "+dnsEditGroup+" permission group",
			errors.New("cloudflare lists no such group"))
	}

	req := tokenRequest{
		Name: name,
		Policies: []tokenPolicy{{
			Effect:           "allow",
			Resources:        map[string]string{zoneScope + "." + zoneID: "*"},
			PermissionGroups: []tokenGroupRef{{ID: groupID}},
		}},
		Condition: tokenCondition{RequestIP: tokenRequestIP{In: filter}},
	}

	var made createdToken

	err = c.do(ctx, http.MethodPost, "/user/tokens", req, &made)
	if err != nil {
		return "", c.refused("creating the token", err)
	}

	if made.Value == "" {
		return "", c.refused("creating the token", errors.New("cloudflare returned no token value"))
	}

	return made.Value, nil
}
