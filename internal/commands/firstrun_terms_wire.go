package commands

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// The wire format of a terms acceptance. The managing system checks the
// signature over exactly these bytes, so nothing here may change without
// the managing system changing too.
const (
	// termsPathStatus names the page that says whether terms are waiting.
	termsPathStatus = "/setup/status"
	// termsPathChallenge is where the core hands out a challenge.
	termsPathChallenge = "/setup/claim/challenge"
	// termsPathClaim is where the signed acceptance goes.
	termsPathClaim = "/setup/claim"

	// termsWaitingReason is the reason /setup/status gives while the
	// directory's terms wait for a person.
	termsWaitingReason = "TLS_TERMS_NOT_ACCEPTED"
	// termsAcceptedCode is the code of an accepted acceptance.
	termsAcceptedCode = "TLS_TERMS_ACCEPTED"
	// termsStatementLabel is the label the acceptance's statement and request
	// carry. The managing system compares it byte for byte.
	termsStatementLabel = "ocfp-setup-terms-v2"

	// serverKeySize is the size of a server key, a SHA-256 digest.
	serverKeySize = sha256.Size
	// macSize is the size of the MAC that comes with a challenge.
	macSize = sha256.Size
	// maxChallenge bounds the challenge bytes this client will sign. A real
	// challenge is a few dozen bytes.
	maxChallenge = 512
	// maxTermsURL is the longest terms URL the core takes.
	maxTermsURL = 2048
	// maxAudience bounds the address inside a statement.
	maxAudience = 512
	// maxTermsBody is the largest request body the core reads.
	maxTermsBody = 4 << 10
	// maxTermsAnswer bounds an answer this client reads from the core.
	maxTermsAnswer = 16 << 10
	// termsTimeout bounds each request to the core.
	termsTimeout = 60 * time.Second
	// maxTermsDetail bounds, in bytes, a piece of text this client shows
	// from a network error.
	maxTermsDetail = 300
)

// Why a terms statement can't be built.
var (
	errTermsBinding   = errors.New("the address or the server key is not one a statement can bind")
	errTermsChallenge = errors.New("the challenge is empty or too long to sign")
	errTermsURL       = errors.New("the terms URL is not one the core takes")
)

// termsBinding is what an acceptance's signature names besides its
// challenge and its terms. Audience is the core's address in the form
// normalizeCoreURL gives, and ServerKey is the server key of the
// certificate the core presented there.
type termsBinding struct {
	Audience  string
	ServerKey []byte
}

// checkTermsURL refuses a terms URL that the core wouldn't take, which is
// also one this client shouldn't show an operator. It must be an https
// address of printable ASCII, at most maxTermsURL bytes, with a host and
// no credentials, query, or fragment.
func checkTermsURL(raw string) error {
	if raw == "" || len(raw) > maxTermsURL {
		return errTermsURL
	}

	for i := range len(raw) {
		if raw[i] <= ' ' || raw[i] > '~' {
			return errTermsURL
		}
	}

	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil ||
		parsed.Opaque != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" ||
		strings.Contains(raw, "#") {
		return errTermsURL
	}

	return nil
}

// termsStatement builds the bytes the acceptance signs. They are, in
// order, the label with its length in one byte, then the payload with its
// length in two bytes, then the address with its length in two bytes,
// then the 32-byte server key. The payload is the challenge whole, then
// the terms URL's length in two bytes, then the URL. Every length is big
// endian.
func termsStatement(challenge []byte, terms string, bind termsBinding) ([]byte, error) {
	if len(challenge) == 0 || len(challenge) > maxChallenge {
		return nil, errTermsChallenge
	}

	err := checkTermsURL(terms)
	if err != nil {
		return nil, err
	}

	if bind.Audience == "" || len(bind.Audience) > maxAudience || len(bind.ServerKey) != serverKeySize {
		return nil, errTermsBinding
	}

	for i := range len(bind.Audience) {
		if bind.Audience[i] <= ' ' || bind.Audience[i] > '~' {
			return nil, errTermsBinding
		}
	}

	label := termsStatementLabel
	payloadLen := len(challenge) + 2 + len(terms)

	out := make([]byte, 0, 1+len(label)+2+payloadLen+2+len(bind.Audience)+len(bind.ServerKey))
	out = append(out, byte(len(label))) // #nosec G115 -- the label is 21 bytes
	out = append(out, label...)
	out = binary.BigEndian.AppendUint16(out, uint16(payloadLen)) // #nosec G115 -- bounded by maxChallenge and maxTermsURL
	out = append(out, challenge...)
	out = binary.BigEndian.AppendUint16(out, uint16(len(terms))) // #nosec G115 -- bounded by maxTermsURL
	out = append(out, terms...)
	out = binary.BigEndian.AppendUint16(out, uint16(len(bind.Audience))) // #nosec G115 -- bounded by maxAudience
	out = append(out, bind.Audience...)
	out = append(out, bind.ServerKey...)

	return out, nil
}

// serverKeyOfConn is the server key of the leaf certificate the server of
// state presented, the SHA-256 of its DER SubjectPublicKeyInfo.
func serverKeyOfConn(state *tls.ConnectionState) ([]byte, error) {
	if state == nil || len(state.PeerCertificates) == 0 {
		return nil, errTermsNoPeer
	}

	sum := sha256.Sum256(state.PeerCertificates[0].RawSubjectPublicKeyInfo)

	return sum[:], nil
}

var (
	errTermsNoPeer = errors.New("the server sent no certificate")
	// errTermsServerChanged refuses a connection whose server presents
	// another key than the first connection of the same run did.
	errTermsServerChanged = errors.New("the server presented another key than on the first connection")
	errTermsElsewhere     = errors.New("this command dials only the core's address")
)

// termsPin remembers the server key of a run's first connection and
// refuses any later connection whose server presents another.
type termsPin struct {
	mu  sync.Mutex
	key []byte
}

// verify is the VerifyConnection hook of the client. It runs after the
// ordinary chain and name checks, during the handshake, so a connection
// that fails it never carries a request.
func (p *termsPin) verify(state tls.ConnectionState) error {
	key, err := serverKeyOfConn(&state)
	if err != nil {
		return err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.key == nil {
		p.key = key

		return nil
	}

	if string(p.key) != string(key) {
		return errTermsServerChanged
	}

	return nil
}

// newTermsHTTPClient builds the client for the core at base, the address
// normalizeCoreURL gives. It verifies the server against the CA file when
// one is named and the system roots otherwise, and it never turns
// verification off. Its transport dials only the core's address, uses no
// proxy, opens a fresh connection for each request so the key pin checks
// every one, and follows no redirect, which could carry the request
// elsewhere.
func newTermsHTTPClient(base, caFile string) (*http.Client, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS13, VerifyConnection: (&termsPin{}).verify}

	if caFile != "" {
		text, err := os.ReadFile(filepath.Clean(caFile))
		if err != nil {
			return nil, fmt.Errorf("reading the CA file: %w", err)
		}

		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(text) {
			return nil, errTermsEmptyCA
		}

		cfg.RootCAs = pool
	}

	parsed, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("reading the address: %w", err)
	}

	port := parsed.Port()
	if port == "" {
		port = "443"
	}

	want := net.JoinHostPort(parsed.Hostname(), port)

	//nolint:exhaustruct // Using zero values for optional fields
	transport := &http.Transport{
		TLSClientConfig:   cfg,
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			if addr != want {
				return nil, errTermsElsewhere
			}

			var dialer net.Dialer

			return dialer.DialContext(ctx, "tcp", want)
		},
	}

	//nolint:exhaustruct // Using zero values for optional fields
	return &http.Client{
		Transport:     transport,
		Timeout:       termsTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, nil
}

var errTermsEmptyCA = errors.New("the CA file holds no certificate")

// Why an address is refused. None repeats the address, which could carry
// a password.
var (
	errCoreNotURL    = errors.New("the address is not a URL; give it as https://HOST")
	errCoreNotHTTPS  = errors.New("the address must start with https://, because the acceptance travels only over a verified connection")
	errCoreUser      = errors.New("the address must not carry a user name or password")
	errCorePath      = errors.New("the address must name only the host, with no path, query, or fragment")
	errCoreNoHost    = errors.New("the address names no host")
	errCoreEmptyPort = errors.New("the address names an empty port")
	errCoreBadPort   = errors.New("the address names a port that is not between 1 and 65535")
	errCoreZeroPort  = errors.New("the address names its port with a leading zero; give the port without one")
	errCoreIPForm    = errors.New("the address's IP address must be in its shortest form, with no zone; give an IPv6 address as its shortest text, in lower case")
	errCoreHostForm  = errors.New("the address's host must be an IP address or a name in ASCII; give an internationalized name in its xn-- form")
	errCoreHostLong  = errors.New("the address's host is longer than a host name can be")
	errCoreHostParts = errors.New("the address's host has an empty part, a trailing dot, or a part longer than a host name allows")
	errCoreHostNum   = errors.New("the address's host ends in a number but is not an IPv4 address; give an IPv4 address as four decimal numbers with no leading zeros")
	errCoreHostColon = errors.New("the address's host is not an IP address")
)

// Limits on a host name, which DNS sets.
const (
	maxHostName  = 253
	maxHostLabel = 63
)

// normalizeCoreURL returns a core's address as scheme and host alone, in
// lower case, with the default port dropped and no trailing slash. The
// acceptance's signature names this form, and the core compares it byte
// for byte with its own public address. It refuses an address that isn't
// https, names no host, has a host that isn't ASCII or a bad port, or
// carries a user, a path, a query, or a fragment. It also refuses every
// address with another spelling of the same place, so one place has one
// form.
func normalizeCoreURL(raw string) (string, error) {
	parsed, err := url.Parse(raw)

	switch {
	case err != nil || parsed.Opaque != "":
		return "", errCoreNotURL
	case !strings.EqualFold(parsed.Scheme, "https"):
		return "", errCoreNotHTTPS
	case parsed.User != nil:
		return "", errCoreUser
	case strings.Trim(parsed.Path, "/") != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "":
		return "", errCorePath
	}

	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return "", errCoreNoHost
	}

	err = checkCoreHost(host)
	if err != nil {
		return "", err
	}

	port, err := normalizeCorePort(parsed)
	if err != nil {
		return "", err
	}

	switch {
	case port != "":
		return "https://" + net.JoinHostPort(host, port), nil
	case strings.Contains(host, ":"):
		return "https://[" + host + "]", nil
	default:
		return "https://" + host, nil
	}
}

// normalizeCorePort returns the port parsed names, or the empty string
// when it names none or names the default, 443. It refuses an empty port,
// one outside 1 to 65535, and one with a leading zero.
func normalizeCorePort(parsed *url.URL) (string, error) {
	port := parsed.Port()
	if port == "" && strings.HasSuffix(parsed.Host, ":") {
		return "", errCoreEmptyPort
	}

	if port == "" {
		return "", nil
	}

	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil || n == 0 {
		return "", errCoreBadPort
	}

	if port != strconv.FormatUint(n, 10) {
		return "", errCoreZeroPort
	}

	if n == 443 {
		return "", nil
	}

	return port, nil
}

// checkCoreHost refuses a host, in lower case, that is not an IP address
// with no zone in its shortest text, or a name of ASCII letters, digits,
// and hyphens in labels that aren't empty, with no trailing dot and a last
// label that isn't a number.
func checkCoreHost(host string) error {
	if addr, err := netip.ParseAddr(host); err == nil {
		if addr.Zone() != "" || addr.Is4In6() || addr.String() != host {
			return errCoreIPForm
		}

		return nil
	}

	if strings.Contains(host, ":") {
		return errCoreHostColon
	}

	for i := range len(host) {
		c := host[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '.' {
			return errCoreHostForm
		}
	}

	if len(host) > maxHostName {
		return errCoreHostLong
	}

	labels := strings.Split(host, ".")
	for _, label := range labels {
		if label == "" || len(label) > maxHostLabel {
			return errCoreHostParts
		}
	}

	if numericLabel(labels[len(labels)-1]) {
		return errCoreHostNum
	}

	return nil
}

// numericLabel reports whether label is a number in decimal, or in hex
// after 0x, which a resolver can read as part of an IPv4 address.
func numericLabel(label string) bool {
	digits := label
	hex := strings.HasPrefix(label, "0x")

	if hex {
		digits = label[2:]
	}

	for i := range len(digits) {
		c := digits[i]
		if (c < '0' || c > '9') && (!hex || c < 'a' || c > 'f') {
			return false
		}
	}

	return true
}

// safeTermsText shows every control or other non-printing character of s,
// and every byte that isn't UTF-8, as an escape such as \x1b, so none
// reaches the terminal, and it cuts the result at maxTermsDetail bytes.
// Every error from the network goes through it, because such an error can
// hold text the server chose.
func safeTermsText(s string) string {
	var b strings.Builder

	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])

		var piece string

		switch {
		case r == utf8.RuneError && size == 1:
			piece = fmt.Sprintf(`\x%02x`, s[i])
		case unicode.IsPrint(r):
			piece = string(r)
		case r < utf8.RuneSelf:
			piece = fmt.Sprintf(`\x%02x`, r)
		case r <= 0xffff:
			piece = fmt.Sprintf(`\u%04x`, r)
		default:
			piece = fmt.Sprintf(`\U%08x`, r)
		}

		if b.Len()+len(piece) > maxTermsDetail {
			b.WriteString("...")

			break
		}

		b.WriteString(piece)

		i += size
	}

	return b.String()
}
