package cloudflare

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// Refusal codes of the DNS challenge token.
const (
	// CodeEgressUnknown refuses a token because no egress range is known,
	// which the operator fixes by setting egress_ranges.
	CodeEgressUnknown = "DNS_CHALLENGE_EGRESS_UNKNOWN"
)

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
