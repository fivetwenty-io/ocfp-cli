package pve

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

const (
	// termproxyWSHandshakeTimeout is the WebSocket dial handshake deadline.
	termproxyWSHandshakeTimeout = 10 * time.Second
	// termproxyReadChanSize is the buffer depth of the reader goroutine channel.
	termproxyReadChanSize = 16
)

// PVE's serial-console over WebSocket is the only API-accessible execution
// channel into a VM that lacks qemu-guest-agent. We use it during
// ProvisionTemplate to seed the firstboot units into the cloned image before
// converting to a template, since PVE 9.x blocks snippet uploads and the
// upstream Ubuntu Noble cloud image ships without qemu-guest-agent.
//
// Auth: API tokens DO work for the WebSocket upgrade (verified on PVE 9.1.11)
// when the Authorization header is sent without any auth cookie. The PVE
// noVNC client uses cookie-auth because it operates inside a logged-in
// browser session; token clients must NOT send a cookie or PVE returns 401.

// TermproxySession carries an authenticated PVE serial-console WebSocket plus
// a small read buffer. Calls are NOT goroutine-safe.
type TermproxySession struct {
	conn    *websocket.Conn
	buf     bytes.Buffer
	closed  bool
	readCh  chan []byte
	readErr error
}

// OpenTermproxy requests a termproxy ticket for the VM's serial0 console and
// opens the resulting WebSocket. The caller's context controls the dial
// timeout; the returned session has its own per-Read deadline.
func OpenTermproxy(ctx context.Context, apiEndpoint, tokenHeader, node string, vmid int, verifySSL bool) (*TermproxySession, error) {
	ticket, port, err := requestTermproxyTicket(ctx, apiEndpoint, tokenHeader, node, vmid, verifySSL)
	if err != nil {
		return nil, fmt.Errorf("termproxy ticket: %w", err)
	}

	wsURL, err := buildVNCWebsocketURL(apiEndpoint, node, vmid, port, ticket)
	if err != nil {
		return nil, err
	}

	dialer := &websocket.Dialer{
		HandshakeTimeout: termproxyWSHandshakeTimeout,
		TLSClientConfig:  &tls.Config{InsecureSkipVerify: !verifySSL}, // #nosec G402 -- operator-controlled: verifySSL is a local CLI/config flag, not attacker input
	}

	hdr := http.Header{}
	hdr.Set("Authorization", tokenHeader)

	conn, resp, err := dialer.DialContext(ctx, wsURL, hdr)
	if resp != nil {
		defer func() {
			cerr := resp.Body.Close()
			if cerr != nil {
				logger.Debugf("close websocket upgrade response body: %v", cerr)
			}
		}()
	}

	if err != nil {
		return nil, fmt.Errorf("websocket dial: %w", err)
	}

	sess := &TermproxySession{conn: conn}

	if err := sess.authenticate(tokenUser(tokenHeader), ticket); err != nil {
		_ = conn.Close()

		return nil, fmt.Errorf("termproxy auth: %w", err)
	}

	return sess, nil
}

// Send writes a chunk to the serial console using PVE's xterm.js wire
// format: "0:<byte-count>:<bytes>" for data messages (verified against
// pve-xtermjs/xterm.js/src/main.js). The leading "0:" discriminator is
// required — proxmox-termproxy also accepts "1:<cols>:<rows>:" for resize
// and "2" for ping, neither of which we need.
func (s *TermproxySession) Send(b []byte) error {
	if s.closed {
		return errors.New("termproxy: session closed") //nolint:err113 // sentinel-like; promoted in next iteration
	}

	framed := append([]byte(fmt.Sprintf("0:%d:", len(b))), b...)

	if os.Getenv("TERMPROXY_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "[termproxy ws->pve %d bytes] %q\n", len(framed), framed)
	}

	err := s.conn.WriteMessage(websocket.BinaryMessage, framed)
	if err != nil {
		return fmt.Errorf("termproxy write: %w", err)
	}

	return nil
}

// SendLine appends \r\n and Sends.
func (s *TermproxySession) SendLine(line string) error {
	return s.Send([]byte(line + "\r\n"))
}

// ExpectRegex blocks until the cumulative output matches re or the deadline
// elapses. Returns the accumulated buffer (including the match). Useful for
// "wait for login: prompt" and "wait for shell prompt after command".
//
// Implementation note: gorilla/websocket marks the connection
// unrecoverably-broken after the first SetReadDeadline expiry. To support an
// "expect with timeout" semantic we drive reads in a goroutine, fan messages
// through a channel, and select against a timer — the websocket conn itself
// never sees a deadline so it stays healthy across many Expect calls.
func (s *TermproxySession) ExpectRegex(re *regexp.Regexp, timeout time.Duration) (string, error) {
	if s.readCh == nil {
		s.startReader()
	}

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()

	for {
		if re.Match(s.buf.Bytes()) {
			out := s.buf.String()
			s.buf.Reset()

			return out, nil
		}

		select {
		case data, ok := <-s.readCh:
			if !ok {
				if s.readErr != nil {
					return s.buf.String(), fmt.Errorf("termproxy read: %w", s.readErr)
				}

				return s.buf.String(), errors.New("termproxy: connection closed") //nolint:err113 // sentinel-like; promoted in next iteration
			}

			s.buf.Write(data)
		case <-deadline.C:
			return s.buf.String(), fmt.Errorf("termproxy: timeout waiting for %s; buffer tail: %q", //nolint:err113 // descriptive error, not caller-testable
				re.String(), tail(s.buf.String(), 200))
		}
	}
}

// Drain reads pending bytes for the given window and returns them. Used to
// flush noise after sending a command but before issuing the next one.
func (s *TermproxySession) Drain(window time.Duration) string {
	if s.readCh == nil {
		s.startReader()
	}

	deadline := time.NewTimer(window)
	defer deadline.Stop()

	for {
		select {
		case data, ok := <-s.readCh:
			if !ok {
				goto done
			}

			s.buf.Write(data)
		case <-deadline.C:
			goto done
		}
	}

done:
	out := s.buf.String()

	s.buf.Reset()

	return out
}

// startReader pumps ws messages onto s.readCh. Closed when the conn breaks.
// Called lazily on the first Expect/Drain so unit tests that only exercise
// helpers don't spin up a goroutine.
func (s *TermproxySession) startReader() {
	s.readCh = make(chan []byte, termproxyReadChanSize)

	go func() {
		defer close(s.readCh)

		for {
			_, data, err := s.conn.ReadMessage()
			if err != nil {
				s.readErr = err

				return
			}

			if os.Getenv("TERMPROXY_DEBUG") != "" {
				fmt.Fprintf(os.Stderr, "[termproxy ws<-pve %d bytes] %q\n", len(data), data)
			}

			s.readCh <- data
		}
	}()
}

// Close shuts the WebSocket cleanly. Safe to call multiple times.
func (s *TermproxySession) Close() error {
	if s.closed {
		return nil
	}

	s.closed = true
	_ = s.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))

	err := s.conn.Close()
	if err != nil {
		return fmt.Errorf("termproxy websocket close: %w", err)
	}

	return nil
}

// authenticate completes PVE's first-message auth handshake: "<user>:<ticket>\n".
// Server replies with "OK" on success.
func (s *TermproxySession) authenticate(user, ticket string) error {
	authMsg := fmt.Sprintf("%s:%s\n", user, ticket)
	if err := s.conn.WriteMessage(websocket.BinaryMessage, []byte(authMsg)); err != nil {
		return fmt.Errorf("write auth: %w", err)
	}

	_ = s.conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	_, data, err := s.conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("read auth response: %w", err)
	}

	if !bytes.Equal(bytes.TrimSpace(data), []byte("OK")) {
		return fmt.Errorf("auth rejected: %q", data) //nolint:err113 // descriptive error, not caller-testable
	}

	// Clear deadline: post-auth reads run via the reader goroutine which
	// can't tolerate a stale per-read deadline (gorilla marks the conn
	// broken after the first expiry).
	_ = s.conn.SetReadDeadline(time.Time{})

	return nil
}

// requestTermproxyTicket calls POST /nodes/{node}/qemu/{vmid}/termproxy?serial=serial0
// and returns the ticket + port the WebSocket needs.
func requestTermproxyTicket(ctx context.Context, apiEndpoint, tokenHeader, node string, vmid int, verifySSL bool) (string, string, error) {
	reqURL := fmt.Sprintf("%s/api2/json/nodes/%s/qemu/%d/termproxy", strings.TrimRight(apiEndpoint, "/"), node, vmid)

	body := strings.NewReader("serial=serial0")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, body)
	if err != nil {
		return "", "", fmt.Errorf("build termproxy ticket request: %w", err)
	}

	req.Header.Set("Authorization", tokenHeader)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{
		Timeout: termproxyWSHandshakeTimeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: !verifySSL}, // #nosec G402 -- operator-controlled: verifySSL is a local CLI/config flag, not attacker input
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("termproxy ticket request: %w", err)
	}
	defer func() {
		err := resp.Body.Close()
		if err != nil {
			logger.Debugf("close ticket response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)

		return "", "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b)) //nolint:err113 // descriptive error, not caller-testable
	}

	var parsed struct {
		Data struct {
			Ticket string `json:"ticket"`
			Port   string `json:"port"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", "", fmt.Errorf("decode termproxy response: %w", err)
	}

	if parsed.Data.Ticket == "" || parsed.Data.Port == "" {
		return "", "", errors.New("termproxy response missing ticket/port") //nolint:err113 // descriptive error, not caller-testable
	}

	return parsed.Data.Ticket, parsed.Data.Port, nil
}

// buildVNCWebsocketURL converts the HTTPS API endpoint to a wss:// URL with
// the port + vncticket query params PVE expects.
func buildVNCWebsocketURL(apiEndpoint, node string, vmid int, port, ticket string) (string, error) {
	u, err := url.Parse(apiEndpoint)
	if err != nil {
		return "", fmt.Errorf("parse api endpoint: %w", err)
	}

	switch strings.ToLower(u.Scheme) {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return "", fmt.Errorf("unsupported api endpoint scheme %q", u.Scheme) //nolint:err113 // descriptive error, not caller-testable
	}

	u.Path = fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/vncwebsocket", node, vmid)
	q := u.Query()
	q.Set("port", port)
	q.Set("vncticket", ticket)
	u.RawQuery = q.Encode()

	return u.String(), nil
}

// tokenUser pulls "<user>!<tokenid>" out of a "PVEAPIToken=user!token=secret"
// header value. PVE's serial-console auth wants the user!tokenid form as the
// first-message principal.
func tokenUser(tokenHeader string) string {
	const prefix = "PVEAPIToken="

	v := strings.TrimPrefix(tokenHeader, prefix)
	if eq := strings.IndexByte(v, '='); eq >= 0 {
		return v[:eq]
	}

	return v
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}

	return s[len(s)-n:]
}
