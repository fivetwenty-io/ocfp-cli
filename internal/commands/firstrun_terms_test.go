package commands

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/pflag"

	"github.com/fivetwenty-io/ocfp-cli/firstrun"
	"github.com/fivetwenty-io/ocfp-cli/internal/vault"
)

// The vector below pins one statement and its signature, made with the
// published first code over a fixed challenge, terms link, address, and
// server key. The managing system pins the same bytes, so a drift in
// either side shows up here first. The challenge opens with its own
// label, ocfp-setup-claim-v1, and holds a nonce of the bytes 0 to 31 and
// an issue time of 0x6afeba90, which makes it 59 bytes.
const (
	termsVectorAudience  = "https://core.example"
	termsVectorChallenge = "6f6366702d73657475702d636c61696d2d7631" +
		"000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f" +
		"000000006afeba90"
	termsVectorTerms     = "https://acme.example/terms-v2.pdf"
	termsVectorStatement = "136f6366702d73657475702d7465726d732d7632005e6f6366702d73657475702d636c61696d2d7631" +
		"000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f" +
		"000000006afeba90002168747470733a2f2f61636d652e6578616d706c652f7465726d732d76322e706466" +
		"001468747470733a2f2f636f72652e6578616d706c65" +
		"5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a"
	termsVectorSignature = "cf45f8f58714048c22a6bbb20e9ef2d9abfd2fc8dd9cc994e51f1c98042e7f83" +
		"7af69f7ef5b1e05178de2c45ac5a053f05d0f15ecdafcfef8ddb3359fde28908"
)

func mustHex(t *testing.T, text string) []byte {
	t.Helper()

	b, err := hex.DecodeString(text)
	if err != nil {
		t.Fatal(err)
	}

	return b
}

func TestTermsStatementMatchesThePinnedVector(t *testing.T) {
	t.Parallel()

	got, err := termsStatement(mustHex(t, termsVectorChallenge), termsVectorTerms, termsBinding{
		Audience:  termsVectorAudience,
		ServerKey: bytes.Repeat([]byte{0x5a}, serverKeySize),
	})
	if err != nil {
		t.Fatal(err)
	}

	if hex.EncodeToString(got) != termsVectorStatement {
		t.Fatalf("the statement is %x, want %s", got, termsVectorStatement)
	}

	_, priv, err := firstrun.KeyPair(firstRunCmdCode)
	if err != nil {
		t.Fatal(err)
	}

	defer firstrun.Wipe(priv)

	if sig := hex.EncodeToString(ed25519.Sign(priv, got)); sig != termsVectorSignature {
		t.Fatalf("the signature is %s, want %s", sig, termsVectorSignature)
	}

	if termsStatementLabel != "ocfp-setup-terms-v2" || len(termsStatementLabel) != 19 {
		t.Fatalf("the label is %q", termsStatementLabel)
	}
}

func TestTermsStatementRefusesWhatTheCoreWouldNotTake(t *testing.T) {
	t.Parallel()

	challenge := mustHex(t, termsVectorChallenge)
	good := termsBinding{Audience: "https://core.example", ServerKey: bytes.Repeat([]byte{1}, serverKeySize)}

	cases := map[string]struct {
		challenge []byte
		terms     string
		bind      termsBinding
		want      error
	}{
		"no challenge":          {nil, termsVectorTerms, good, errTermsChallenge},
		"an overlong challenge": {make([]byte, maxChallenge+1), termsVectorTerms, good, errTermsChallenge},
		"no terms":              {challenge, "", good, errTermsURL},
		"overlong terms":        {challenge, "https://acme.example/" + strings.Repeat("a", maxTermsURL), good, errTermsURL},
		"terms with a space":    {challenge, "https://acme.example/a b", good, errTermsURL},
		"terms with a control":  {challenge, "https://acme.example/\n", good, errTermsURL},
		"terms over http":       {challenge, "http://acme.example/t.pdf", good, errTermsURL},
		"terms with a fragment": {challenge, "https://acme.example/t.pdf#x", good, errTermsURL},
		"terms with a user":     {challenge, "https://u:p@acme.example/t.pdf", good, errTermsURL},
		"terms with no host":    {challenge, "https:///t.pdf", good, errTermsURL},
		"no audience":           {challenge, termsVectorTerms, termsBinding{ServerKey: good.ServerKey}, errTermsBinding},
		"a spaced audience":     {challenge, termsVectorTerms, termsBinding{Audience: "https://a b", ServerKey: good.ServerKey}, errTermsBinding},
		"no server key":         {challenge, termsVectorTerms, termsBinding{Audience: good.Audience}, errTermsBinding},
		"a short server key":    {challenge, termsVectorTerms, termsBinding{Audience: good.Audience, ServerKey: good.ServerKey[1:]}, errTermsBinding},
	}

	for name, c := range cases {
		_, err := termsStatement(c.challenge, c.terms, c.bind)
		if !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
}

func TestNormalizeCoreURL(t *testing.T) {
	t.Parallel()

	for raw, want := range map[string]string{
		"https://Core.Example":          "https://core.example",
		"https://core.example/":         "https://core.example",
		"https://core.example:443":      "https://core.example",
		"https://core.example:8443":     "https://core.example:8443",
		"HTTPS://core.example":          "https://core.example",
		"https://127.0.0.1:9000":        "https://127.0.0.1:9000",
		"https://[::1]":                 "https://[::1]",
		"https://[::1]:9000":            "https://[::1]:9000",
		"https://xn--bcher-kva.example": "https://xn--bcher-kva.example",
	} {
		got, err := normalizeCoreURL(raw)
		if err != nil || got != want {
			t.Errorf("%q: got %q, %v, want %q", raw, got, err, want)
		}
	}

	for _, raw := range []string{
		"",
		"core.example",
		"http://core.example",
		"https://",
		"https://user:secret@core.example",
		"https://core.example/path",
		"https://core.example?x=1",
		"https://core.example#frag",
		"https://core.example:",
		"https://core.example:0",
		"https://core.example:08443",
		"https://core.example:70000",
		"https://core.example.",
		"https://core..example",
		"https://bücher.example",
		"https://core.example.123",
		"https://core.example.0x1f",
		"https://010.1.1.1",
		"https://[::ffff:1.2.3.4]",
		"https://[FE80::1%25eth0]",
		"https://" + strings.Repeat("a", 64) + ".example",
	} {
		got, err := normalizeCoreURL(raw)
		if err == nil {
			t.Errorf("%q was taken as %q", raw, got)

			continue
		}

		if strings.Contains(err.Error(), "secret") {
			t.Errorf("%q: the error repeats the password: %v", raw, err)
		}
	}
}

// termsTestCert is a self-signed certificate for 127.0.0.1 and its server
// key.
type termsTestCert struct {
	cert      tls.Certificate
	pem       []byte
	serverKey []byte
}

func newTermsTestCert(t *testing.T) termsTestCert {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	serial, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		t.Fatal(err)
	}

	//nolint:exhaustruct // Using zero values for optional fields
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "terms test core"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}

	sum := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)

	return termsTestCert{
		cert:      tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf},
		pem:       pemOfCert(der),
		serverKey: sum[:],
	}
}

func pemOfCert(der []byte) []byte {
	const header, footer = "-----BEGIN CERTIFICATE-----\n", "-----END CERTIFICATE-----\n"

	encoded := base64.StdEncoding.EncodeToString(der)

	var b strings.Builder

	b.WriteString(header)

	for len(encoded) > 64 {
		b.WriteString(encoded[:64] + "\n")

		encoded = encoded[64:]
	}

	b.WriteString(encoded + "\n" + footer)

	return []byte(b.String())
}

// fakeTermsCore is a TLS server that plays the core's three routes. It
// serves certs[n] on its nth connection, or the last one once it runs out.
type fakeTermsCore struct {
	mu       sync.Mutex
	certs    []termsTestCert
	conns    int
	requests []string
	bodies   map[string][]byte

	base   string
	caFile string

	statusCode, challengeCode, claimCode int
	statusBody, challengeBody, claimBody string
	dropClaim                            bool
}

const fakeTermsWaiting = `{"tls":"none","tlsReason":"TLS_TERMS_NOT_ACCEPTED","tlsTerms":"` + termsVectorTerms + `"}`

func startFakeTermsCore(t *testing.T, certs ...termsTestCert) *fakeTermsCore {
	t.Helper()

	if len(certs) == 0 {
		certs = []termsTestCert{newTermsTestCert(t)}
	}

	core := &fakeTermsCore{
		certs:         certs,
		bodies:        map[string][]byte{},
		statusCode:    http.StatusOK,
		challengeCode: http.StatusOK,
		claimCode:     http.StatusOK,
		statusBody:    fakeTermsWaiting,
	}

	core.challengeBody = core.challengeJSON(t)
	core.claimBody = `{"code":"TLS_TERMS_ACCEPTED","terms":"` + termsVectorTerms + `"}`

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS13,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			core.mu.Lock()
			defer core.mu.Unlock()

			i := min(core.conns, len(core.certs)-1)
			core.conns++

			return &core.certs[i].cert, nil
		},
	}

	//nolint:exhaustruct // Using zero values for optional fields
	server := &http.Server{Handler: http.HandlerFunc(core.serve), ReadHeaderTimeout: 5 * time.Second}

	go func() { _ = server.Serve(tls.NewListener(listener, tlsConfig)) }()

	t.Cleanup(func() { _ = server.Close() })

	core.base = "https://" + listener.Addr().String()

	var pool bytes.Buffer
	for _, c := range certs {
		pool.Write(c.pem)
	}

	core.caFile = filepath.Join(t.TempDir(), "ca.pem")

	err = os.WriteFile(core.caFile, pool.Bytes(), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	return core
}

// challengeJSON is the challenge the core hands out, in the shape of its
// reply, with the vector's challenge bytes and a MAC of 32 bytes.
func (c *fakeTermsCore) challengeJSON(t *testing.T) string {
	t.Helper()

	reply := struct {
		Label     string    `json:"label"`
		Nonce     []byte    `json:"nonce"`
		Issued    time.Time `json:"issued"`
		Challenge []byte    `json:"challenge"`
		MAC       []byte    `json:"mac"`
	}{
		Label:     "challenge",
		Nonce:     mustHex(t, termsVectorChallenge)[19:51],
		Issued:    time.Unix(0x6afeba90, 0).UTC(),
		Challenge: mustHex(t, termsVectorChallenge),
		MAC:       bytes.Repeat([]byte{0xa7}, macSize),
	}

	b, err := json.Marshal(reply)
	if err != nil {
		t.Fatal(err)
	}

	return string(b)
}

func (c *fakeTermsCore) serve(w http.ResponseWriter, r *http.Request) {
	body := new(bytes.Buffer)
	_, _ = body.ReadFrom(http.MaxBytesReader(w, r.Body, 1<<20))

	key := r.Method + " " + r.URL.Path

	c.mu.Lock()
	c.requests = append(c.requests, key)
	c.bodies[key] = body.Bytes()
	c.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")

	switch key {
	case "GET " + termsPathStatus:
		w.WriteHeader(c.statusCode)
		_, _ = w.Write([]byte(c.statusBody))
	case "GET " + termsPathChallenge:
		w.WriteHeader(c.challengeCode)
		_, _ = w.Write([]byte(c.challengeBody))
	case "POST " + termsPathClaim:
		if c.dropClaim {
			hijacker, ok := w.(http.Hijacker)
			if ok {
				conn, _, err := hijacker.Hijack()
				if err == nil {
					_ = conn.Close()
				}
			}

			return
		}

		w.WriteHeader(c.claimCode)
		_, _ = w.Write([]byte(c.claimBody))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (c *fakeTermsCore) seen() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]string(nil), c.requests...)
}

func (c *fakeTermsCore) options() acceptTermsOptions {
	return acceptTermsOptions{bloc: firstRunCmdBloc, address: c.base, caFile: c.caFile}
}

func termsDepsOf(d *firstRunTestDeps) acceptTermsDeps {
	return acceptTermsDeps{firstRunDeps: d.deps()}
}

// termsTestCode is the first code, as the vault holds it and as the
// normalized text and the public half the tests must never see printed.
var termsTestSecrets = []string{firstRunCmdCode, "AAAQEAYEAUDAOCAJBIFQYDIO", "AAAQ EAYE", "19564ac9b84c8389", "cnsi1P1Ys5tWdhu"}

func assertNoSecret(t *testing.T, what, text string, extra ...string) {
	t.Helper()

	for _, secret := range append(append([]string(nil), termsTestSecrets...), extra...) {
		if secret != "" && strings.Contains(text, secret) {
			t.Errorf("%s shows a secret (%d bytes starting %.4s)", what, len(secret), secret)
		}
	}
}

// u16 is n as two big-endian bytes.
func u16(n int) []byte { return binary.BigEndian.AppendUint16(nil, uint16(n)) } // #nosec G115 -- test lengths are small

// assembleTermsStatement builds the statement a second way from
// termsStatement, straight from the layout the managing system documents.
func assembleTermsStatement(label string, challenge []byte, terms, audience string, serverKey []byte) []byte {
	payload := append(append(append([]byte{}, challenge...), u16(len(terms))...), terms...)

	var b bytes.Buffer

	b.WriteByte(byte(len(label)))
	b.WriteString(label)
	b.Write(u16(len(payload)))
	b.Write(payload)
	b.Write(u16(len(audience)))
	b.WriteString(audience)
	b.Write(serverKey)

	return b.Bytes()
}

func TestAcceptTermsSendsTheSignedAcceptance(t *testing.T) {
	t.Parallel()

	cert := newTermsTestCert(t)
	core := startFakeTermsCore(t, cert)
	d := newFirstRunTestDeps(false, "y\n")

	err := runFirstRunAcceptTerms(context.Background(), termsDepsOf(d), core.options())
	if err != nil {
		t.Fatalf("the acceptance failed: %v", err)
	}

	want := []string{"GET " + termsPathStatus, "GET " + termsPathChallenge, "POST " + termsPathClaim}
	if got := core.seen(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("the core saw %v, want %v", got, want)
	}

	raw := core.bodies["POST "+termsPathClaim]
	if len(raw) == 0 || len(raw) >= maxTermsBody {
		t.Fatalf("the body is %d bytes, want 1 to %d", len(raw), maxTermsBody-1)
	}

	var body map[string]string

	err = json.Unmarshal(raw, &body)
	if err != nil {
		t.Fatalf("the body is not a flat object of strings: %v", err)
	}

	fields := []string{"statement", "challenge", "mac", "audience", "server_key", "terms", "signature"}
	if len(body) != len(fields) {
		t.Fatalf("the body has %d fields, want %v", len(body), fields)
	}

	for _, field := range fields {
		if body[field] == "" {
			t.Errorf("the body lacks %s", field)
		}
	}

	decode := func(field string, size int) []byte {
		t.Helper()

		b, decErr := base64.StdEncoding.DecodeString(body[field])
		if decErr != nil || (size > 0 && len(b) != size) {
			t.Fatalf("%s is not standard padded base64 of %d bytes: %v", field, size, decErr)
		}

		return b
	}

	challenge := decode("challenge", 0)
	if !bytes.Equal(challenge, mustHex(t, termsVectorChallenge)) {
		t.Errorf("the challenge changed on its way back")
	}

	if !bytes.Equal(decode("mac", macSize), bytes.Repeat([]byte{0xa7}, macSize)) {
		t.Errorf("the MAC changed on its way back")
	}

	serverKey := decode("server_key", serverKeySize)
	if !bytes.Equal(serverKey, cert.serverKey) {
		t.Errorf("the server key is not the key of the certificate the core presented")
	}

	if body["statement"] != termsStatementLabel || body["audience"] != core.base || body["terms"] != termsVectorTerms {
		t.Errorf("the statement, audience, or terms is wrong: %q %q %q", body["statement"], body["audience"], body["terms"])
	}

	signed := assembleTermsStatement(termsStatementLabel, challenge, termsVectorTerms, core.base, cert.serverKey)

	pub, _, err := firstrun.KeyPair(firstRunCmdCode)
	if err != nil {
		t.Fatal(err)
	}

	signature := decode("signature", ed25519.SignatureSize)
	if !ed25519.Verify(pub, signed, signature) {
		t.Fatal("the signature doesn't cover the statement the managing system checks")
	}

	out := d.out.String()
	if !strings.Contains(out, "terms of service are at "+termsVectorTerms) || !strings.Contains(out, "[y/N]") ||
		!strings.Contains(out, "accepted the terms") {
		t.Errorf("the output doesn't show the link, the question, and the result: %q", out)
	}

	assertNoSecret(t, "the output", out, body["signature"], hex.EncodeToString(signature), body["server_key"])
}

func TestAcceptTermsPinsTheSignedBodyToAFixedChallenge(t *testing.T) {
	t.Parallel()

	// Ed25519 is deterministic, so one challenge, one server key, and one
	// address give one body. Check the body against the vector's own
	// signature by signing the vector statement the way the command does.
	cert := newTermsTestCert(t)
	core := startFakeTermsCore(t, cert)
	d := newFirstRunTestDeps(false, "")

	opts := core.options()
	opts.assumeYes = true

	err := runFirstRunAcceptTerms(context.Background(), termsDepsOf(d), opts)
	if err != nil {
		t.Fatal(err)
	}

	var body acceptRequest

	err = json.Unmarshal(core.bodies["POST "+termsPathClaim], &body)
	if err != nil {
		t.Fatal(err)
	}

	statement, err := termsStatement(body.Challenge, body.Terms, termsBinding{Audience: body.Audience, ServerKey: body.ServerKey})
	if err != nil {
		t.Fatal(err)
	}

	_, priv, err := firstrun.KeyPair(firstRunCmdCode)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(ed25519.Sign(priv, statement), body.Signature) {
		t.Fatal("the body's signature is not the first code's signature over its own statement")
	}

	if strings.Contains(d.out.String(), "[y/N]") {
		t.Error("--yes still asked the question")
	}
}

func TestAcceptTermsReadsTheAddressFromTheBloc(t *testing.T) {
	t.Parallel()

	core := startFakeTermsCore(t)
	d := newFirstRunTestDeps(false, "yes\n")
	d.store.put(vault.FirstRunPublicPath(firstRunCmdBloc), vault.FirstRunURLKey, core.base+"/")

	opts := core.options()
	opts.address = ""

	err := runFirstRunAcceptTerms(context.Background(), termsDepsOf(d), opts)
	if err != nil {
		t.Fatalf("the acceptance failed: %v", err)
	}

	if len(core.seen()) != 3 {
		t.Fatalf("the core saw %v", core.seen())
	}
}

func TestAcceptTermsNeedsAnAddress(t *testing.T) {
	t.Parallel()

	core := startFakeTermsCore(t)
	d := newFirstRunTestDeps(false, "y\n")

	opts := core.options()
	opts.address = ""

	err := runFirstRunAcceptTerms(context.Background(), termsDepsOf(d), opts)
	if firstrun.CodeOf(err) != codeFirstRunNotDeployed {
		t.Fatalf("got %v, want %s", err, codeFirstRunNotDeployed)
	}

	if len(core.seen()) != 0 {
		t.Fatalf("the core saw %v", core.seen())
	}
}

func TestAcceptTermsReadsTheCodeFromStandardInput(t *testing.T) {
	t.Parallel()

	core := startFakeTermsCore(t)
	d := newFirstRunTestDeps(false, "aaaq eaye auda ocaj bifq yd1o\n")
	d.store = newFakeFirstRunVault()

	opts := core.options()
	opts.codeSource = "-"
	opts.assumeYes = true

	err := runFirstRunAcceptTerms(context.Background(), termsDepsOf(d), opts)
	if err != nil {
		t.Fatalf("the acceptance failed: %v", err)
	}

	var body acceptRequest

	err = json.Unmarshal(core.bodies["POST "+termsPathClaim], &body)
	if err != nil {
		t.Fatal(err)
	}

	pub, _, err := firstrun.KeyPair(firstRunCmdCode)
	if err != nil {
		t.Fatal(err)
	}

	statement, err := termsStatement(body.Challenge, body.Terms, termsBinding{Audience: body.Audience, ServerKey: body.ServerKey})
	if err != nil || !ed25519.Verify(pub, statement, body.Signature) {
		t.Fatalf("the typed code didn't sign as the vault's code does: %v", err)
	}

	if d.opens != 0 {
		t.Error("the vault was opened though the code came from standard input")
	}
}

func TestAcceptTermsRefusesABadCodeSource(t *testing.T) {
	t.Parallel()

	core := startFakeTermsCore(t)

	cases := map[string]struct {
		mutate func(*acceptTermsOptions)
		input  string
		want   string
	}{
		"a code on the command line": {func(o *acceptTermsOptions) { o.codeSource = firstRunCmdCode }, "", codeFirstRunCodeSource},
		"standard input with no URL": {func(o *acceptTermsOptions) { o.codeSource = "-"; o.address = "" }, firstRunCmdCode, codeFirstRunAddress},
		"empty input":                {func(o *acceptTermsOptions) { o.codeSource = "-" }, "  \n", firstrun.CodeMalformed},
		"a malformed code":           {func(o *acceptTermsOptions) { o.codeSource = "-" }, "NOT-A-CODE\n", firstrun.CodeMalformed},
		"input longer than any code": {func(o *acceptTermsOptions) { o.codeSource = "-" }, strings.Repeat("A", maxTermsCode+10), firstrun.CodeMalformed},
		"no bloc":                    {func(o *acceptTermsOptions) { o.bloc = "" }, "", ""},
	}

	for name, c := range cases {
		d := newFirstRunTestDeps(false, c.input)
		opts := core.options()
		c.mutate(&opts)

		err := runFirstRunAcceptTerms(context.Background(), termsDepsOf(d), opts)
		if err == nil {
			t.Errorf("%s: no refusal", name)

			continue
		}

		if c.want == "" {
			if !errors.Is(err, ErrBlocFlagOrEnvVarRequired) {
				t.Errorf("%s: %v", name, err)
			}
		} else if firstrun.CodeOf(err) != c.want {
			t.Errorf("%s: got %v, want %s", name, err, c.want)
		}

		assertNoSecret(t, name, err.Error(), "NOT-A-CODE")
	}

	if got := core.seen(); len(got) != 0 {
		t.Fatalf("the core saw %v before the code was settled", got)
	}
}

func TestAcceptTermsDecliningSendsNothing(t *testing.T) {
	t.Parallel()

	for name, input := range map[string]string{"n": "n\n", "no": "no\n", "blank": "\n", "end of input": "", "other": "maybe\n"} {
		core := startFakeTermsCore(t)
		d := newFirstRunTestDeps(false, input)

		err := runFirstRunAcceptTerms(context.Background(), termsDepsOf(d), core.options())
		if firstrun.CodeOf(err) != codeFirstRunNotConfirmed {
			t.Errorf("%s: got %v, want %s", name, err, codeFirstRunNotConfirmed)
		}

		for _, req := range core.seen() {
			if req != "GET "+termsPathStatus {
				t.Errorf("%s: the core saw %q after the operator declined", name, req)
			}
		}

		if !strings.Contains(d.out.String(), termsVectorTerms) {
			t.Errorf("%s: the terms link was never shown", name)
		}
	}
}

func TestAcceptTermsFromStandardInputNeedsYesToSkipTheQuestion(t *testing.T) {
	t.Parallel()

	core := startFakeTermsCore(t)
	d := newFirstRunTestDeps(false, firstRunCmdCode+"\n")

	opts := core.options()
	opts.codeSource = "-"

	err := runFirstRunAcceptTerms(context.Background(), termsDepsOf(d), opts)
	if firstrun.CodeOf(err) != codeFirstRunNotConfirmed || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("got %v, want %s with the --yes hint", err, codeFirstRunNotConfirmed)
	}

	if got := core.seen(); len(got) != 1 {
		t.Fatalf("the core saw %v", got)
	}
}

func TestAcceptTermsRefusesAServerKeyThatChanged(t *testing.T) {
	t.Parallel()

	a, b := newTermsTestCert(t), newTermsTestCert(t)

	cases := map[string]struct {
		certs []termsTestCert
		seen  []string
	}{
		"before the challenge": {[]termsTestCert{a, b}, []string{"GET " + termsPathStatus}},
		"before the claim":     {[]termsTestCert{a, a, b}, []string{"GET " + termsPathStatus, "GET " + termsPathChallenge}},
	}

	for name, c := range cases {
		core := startFakeTermsCore(t, c.certs...)
		d := newFirstRunTestDeps(false, "y\n")

		err := runFirstRunAcceptTerms(context.Background(), termsDepsOf(d), core.options())
		if firstrun.CodeOf(err) != codeFirstRunServerChanged {
			t.Errorf("%s: got %v, want %s", name, err, codeFirstRunServerChanged)
		}

		if got := core.seen(); strings.Join(got, ",") != strings.Join(c.seen, ",") {
			t.Errorf("%s: the core saw %v, want %v", name, got, c.seen)
		}

		if _, ok := core.bodies["POST "+termsPathClaim]; ok {
			t.Errorf("%s: a claim body reached the core", name)
		}
	}
}

func TestAcceptTermsRefusesACoreItCannotVerify(t *testing.T) {
	t.Parallel()

	core := startFakeTermsCore(t)
	d := newFirstRunTestDeps(false, "y\n")

	opts := core.options()
	other := newTermsTestCert(t)
	opts.caFile = filepath.Join(t.TempDir(), "other.pem")

	err := os.WriteFile(opts.caFile, other.pem, 0o600)
	if err != nil {
		t.Fatal(err)
	}

	err = runFirstRunAcceptTerms(context.Background(), termsDepsOf(d), opts)
	if firstrun.CodeOf(err) != codeFirstRunCoreUnreached {
		t.Fatalf("got %v, want %s", err, codeFirstRunCoreUnreached)
	}

	if len(core.seen()) != 0 {
		t.Fatalf("the core saw %v from a client that couldn't verify it", core.seen())
	}

	opts.caFile = filepath.Join(t.TempDir(), "missing.pem")

	if firstrun.CodeOf(runFirstRunAcceptTerms(context.Background(), termsDepsOf(d), opts)) != codeFirstRunCAInvalid {
		t.Error("a missing CA file was not refused as an invalid CA")
	}

	empty := filepath.Join(t.TempDir(), "empty.pem")

	err = os.WriteFile(empty, []byte("not a certificate"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	opts.caFile = empty

	if firstrun.CodeOf(runFirstRunAcceptTerms(context.Background(), termsDepsOf(d), opts)) != codeFirstRunCAInvalid {
		t.Error("a CA file with no certificate was not refused as an invalid CA")
	}
}

func TestAcceptTermsRefusesAnAddressItCannotUse(t *testing.T) {
	t.Parallel()

	for _, address := range []string{"http://core.example", "https://u:secret@core.example", "https://core.example/x", "core.example"} {
		d := newFirstRunTestDeps(false, "y\n")

		err := runFirstRunAcceptTerms(context.Background(), termsDepsOf(d),
			acceptTermsOptions{bloc: firstRunCmdBloc, address: address})
		if firstrun.CodeOf(err) != codeFirstRunAddress {
			t.Errorf("%q: got %v, want %s", address, err, codeFirstRunAddress)
		} else if strings.Contains(err.Error(), "secret") {
			t.Errorf("%q: the error repeats the password", address)
		}
	}
}

func TestAcceptTermsWaitsOnlyForTermsTheCoreNames(t *testing.T) {
	t.Parallel()

	for name, status := range map[string]string{
		"no terms waiting":    `{"tlsReason":"TLS_PENDING"}`,
		"no reason":           `{}`,
		"another certificate": `{"tlsReason":"TLS_ACME_REFUSED","tlsTerms":"` + termsVectorTerms + `"}`,
	} {
		core := startFakeTermsCore(t)
		core.statusBody = status
		d := newFirstRunTestDeps(false, "y\n")

		err := runFirstRunAcceptTerms(context.Background(), termsDepsOf(d), core.options())
		if firstrun.CodeOf(err) != codeFirstRunTermsNotWait {
			t.Errorf("%s: got %v, want %s", name, err, codeFirstRunTermsNotWait)
		}

		if len(core.seen()) != 1 {
			t.Errorf("%s: the core saw %v", name, core.seen())
		}
	}

	for name, terms := range map[string]string{
		"a plain http link":     "http://acme.example/t.pdf",
		"a script":              "javascript:alert(1)",
		"a link with a space":   "https://acme.example/a b",
		"a link with an escape": "https://acme.example/\\u001b[2J",
		"no link":               "",
	} {
		core := startFakeTermsCore(t)
		core.statusBody = `{"tlsReason":"TLS_TERMS_NOT_ACCEPTED","tlsTerms":"` + terms + `"}`
		d := newFirstRunTestDeps(false, "y\n")

		err := runFirstRunAcceptTerms(context.Background(), termsDepsOf(d), core.options())
		if firstrun.CodeOf(err) != codeFirstRunTermsInvalid {
			t.Errorf("%s: got %v, want %s", name, err, codeFirstRunTermsInvalid)
		}

		if strings.Contains(d.out.String(), "javascript") || strings.Contains(d.out.String(), "\x1b") {
			t.Errorf("%s: the output shows the unsafe link", name)
		}
	}

	core := startFakeTermsCore(t)
	core.statusBody = "not json"

	err := runFirstRunAcceptTerms(context.Background(), termsDepsOf(newFirstRunTestDeps(false, "y\n")), core.options())
	if firstrun.CodeOf(err) != codeFirstRunCoreAnswer {
		t.Errorf("an unreadable status: got %v, want %s", err, codeFirstRunCoreAnswer)
	}
}

// refusalCase is one answer the core can give instead of an acceptance.
type refusalCase struct {
	at       string // "status", "challenge", or "claim"
	httpCode int
	body     string
	wantCode string
	wantText string
}

func TestAcceptTermsShowsEveryRefusalAsItsCodeAndSentence(t *testing.T) {
	t.Parallel()

	const message = `,"message":"SERVER-CHOSEN-TEXT \u001b[31m"`

	cases := map[string]refusalCase{
		"request invalid":       {"claim", 400, `{"code":"REQUEST_INVALID"` + message + `}`, "REQUEST_INVALID", "could not read the acceptance"},
		"refused mac":           {"claim", 403, `{"code":"SETUP_CLAIM_REFUSED","reason":"mac"` + message + `}`, "SETUP_CLAIM_REFUSED", "did not recognize its own challenge"},
		"refused time":          {"claim", 403, `{"code":"SETUP_CLAIM_REFUSED","reason":"time"}`, "SETUP_CLAIM_REFUSED", "more than a minute"},
		"refused key":           {"claim", 403, `{"code":"SETUP_CLAIM_REFUSED","reason":"server-key"}`, "SETUP_CLAIM_REFUSED", "another certificate"},
		"refused sig":           {"claim", 403, `{"code":"SETUP_CLAIM_REFUSED","reason":"signature"}`, "SETUP_CLAIM_REFUSED", "did not accept the signature"},
		"refused spent":         {"claim", 403, `{"code":"SETUP_CLAIM_REFUSED","reason":"spent"}`, "SETUP_CLAIM_REFUSED", "already spent"},
		"refused unnamed":       {"claim", 403, `{"code":"SETUP_CLAIM_REFUSED","reason":"` + strings.Repeat("x", 40) + `"}`, "SETUP_CLAIM_REFUSED", "refused the acceptance"},
		"acceptance":            {"claim", 409, `{"code":"TLS_TERMS_ACCEPTANCE_REFUSED"` + message + `}`, "TLS_TERMS_ACCEPTANCE_REFUSED", "current ones"},
		"initialized":           {"claim", 409, `{"code":"ALREADY_INITIALIZED"}`, "ALREADY_INITIALIZED", "only an administrator who has signed in"},
		"wrong address":         {"claim", 421, `{"code":"SETUP_CLAIM_WRONG_ADDRESS","address":"https://127.0.0.1:9"}`, "SETUP_CLAIM_WRONG_ADDRESS", "use https://127.0.0.1:9"},
		"foreign address":       {"claim", 421, `{"code":"SETUP_CLAIM_WRONG_ADDRESS","address":"https://evil.example"}`, "SETUP_CLAIM_WRONG_ADDRESS", "use the core's public address"},
		"odd address":           {"claim", 421, `{"code":"SETUP_CLAIM_WRONG_ADDRESS","address":"https://127.0.0.1:9/\u001b[2J"}`, "SETUP_CLAIM_WRONG_ADDRESS", "use the core's public address"},
		"rate limited":          {"claim", 429, `{"code":"RATE_LIMITED"}`, "RATE_LIMITED", "too many attempts"},
		"rate at the challenge": {"challenge", 429, `{"code":"RATE_LIMITED"}`, "RATE_LIMITED", "too many attempts"},
		"rate at the status":    {"status", 429, `{"code":"RATE_LIMITED"}`, "RATE_LIMITED", "too many attempts"},
		"no server key":         {"claim", 503, `{"code":"SETUP_CLAIM_NO_SERVER_KEY"}`, "SETUP_CLAIM_NO_SERVER_KEY", "can't name the key"},
		"unavailable":           {"claim", 503, `{"code":"SETUP_CLAIM_UNAVAILABLE"}`, "SETUP_CLAIM_UNAVAILABLE", "no first-run key"},
		"service down":          {"claim", 503, `{"code":"SERVICE_UNAVAILABLE"}`, "SERVICE_UNAVAILABLE", "can't keep the acceptance"},
		"unknown code":          {"claim", 400, `{"code":"BRAND_NEW_CODE"` + message + `}`, "BRAND_NEW_CODE", "update ocfp"},
		"unreadable code":       {"claim", 400, `{"code":"bad code \u001b[2J"}`, codeFirstRunCoreAnswer, "can't read"},
		"no code":               {"claim", 500, `<html>oops</html>`, codeFirstRunCoreAnswer, "HTTP 500"},
	}

	for name, c := range cases {
		core := startFakeTermsCore(t)

		switch c.at {
		case "status":
			core.statusCode, core.statusBody = c.httpCode, c.body
		case "challenge":
			core.challengeCode, core.challengeBody = c.httpCode, c.body
		default:
			core.claimCode, core.claimBody = c.httpCode, c.body
		}

		d := newFirstRunTestDeps(false, "y\n")

		err := runFirstRunAcceptTerms(context.Background(), termsDepsOf(d), core.options())
		if err == nil {
			t.Errorf("%s: the refusal came back as success", name)

			continue
		}

		if firstrun.CodeOf(err) != c.wantCode {
			t.Errorf("%s: got %v, want code %s", name, err, c.wantCode)
		}

		if !strings.Contains(err.Error(), c.wantText) {
			t.Errorf("%s: %q lacks %q", name, err, c.wantText)
		}

		for _, bad := range []string{"SERVER-CHOSEN-TEXT", "\x1b", "evil.example", "bad code"} {
			if strings.Contains(err.Error(), bad) {
				t.Errorf("%s: the error shows text the server chose", name)
			}
		}

		assertNoSecret(t, name, err.Error()+d.out.String())

		if strings.Contains(d.out.String(), "accepted the terms") {
			t.Errorf("%s: the output claims the terms were accepted", name)
		}
	}
}

func TestAcceptTermsDoesNotTrustAnUnreadableAcceptance(t *testing.T) {
	t.Parallel()

	for name, body := range map[string]string{
		"other terms":    `{"code":"TLS_TERMS_ACCEPTED","terms":"https://acme.example/other.pdf"}`,
		"another code":   `{"code":"SETUP_CLAIM_ACCEPTED","terms":"` + termsVectorTerms + `"}`,
		"not json":       `accepted`,
		"an empty reply": `{}`,
	} {
		core := startFakeTermsCore(t)
		core.claimBody = body
		d := newFirstRunTestDeps(false, "y\n")

		err := runFirstRunAcceptTerms(context.Background(), termsDepsOf(d), core.options())
		if firstrun.CodeOf(err) != codeFirstRunCoreAnswer || !strings.Contains(err.Error(), "run 'ocfp first-run accept-terms' again") {
			t.Errorf("%s: got %v", name, err)
		}

		if strings.Contains(d.out.String(), "accepted the terms") {
			t.Errorf("%s: the output claims the terms were accepted", name)
		}
	}
}

func TestAcceptTermsSaysHowToCheckAfterALostAnswer(t *testing.T) {
	t.Parallel()

	core := startFakeTermsCore(t)
	core.dropClaim = true
	d := newFirstRunTestDeps(false, "y\n")

	err := runFirstRunAcceptTerms(context.Background(), termsDepsOf(d), core.options())
	if firstrun.CodeOf(err) != codeFirstRunCoreUnreached || !strings.Contains(err.Error(), "the acceptance landed") {
		t.Fatalf("got %v", err)
	}
}

func TestAcceptTermsRefusesAChallengeItCannotSign(t *testing.T) {
	t.Parallel()

	for name, body := range map[string]string{
		"not json":         `nope`,
		"no challenge":     `{"mac":"` + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, macSize)) + `"}`,
		"a short mac":      `{"challenge":"AAEC","mac":"AAEC"}`,
		"an overlong body": `{"challenge":"` + base64.StdEncoding.EncodeToString(make([]byte, maxChallenge+1)) + `","mac":"` + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, macSize)) + `"}`,
	} {
		core := startFakeTermsCore(t)
		core.challengeBody = body
		d := newFirstRunTestDeps(false, "y\n")

		err := runFirstRunAcceptTerms(context.Background(), termsDepsOf(d), core.options())
		if firstrun.CodeOf(err) != codeFirstRunCoreAnswer {
			t.Errorf("%s: got %v, want %s", name, err, codeFirstRunCoreAnswer)
		}

		for _, req := range core.seen() {
			if req == "POST "+termsPathClaim {
				t.Errorf("%s: a claim was sent after a bad challenge", name)
			}
		}
	}
}

func TestAcceptTermsFollowsNoRedirect(t *testing.T) {
	t.Parallel()

	core := startFakeTermsCore(t)
	core.statusCode = http.StatusFound
	core.statusBody = ``

	err := runFirstRunAcceptTerms(context.Background(), termsDepsOf(newFirstRunTestDeps(false, "y\n")), core.options())
	if firstrun.CodeOf(err) != codeFirstRunCoreAnswer {
		t.Fatalf("got %v, want %s", err, codeFirstRunCoreAnswer)
	}
}

func TestAcceptTermsCommandFlags(t *testing.T) {
	t.Parallel()

	cmd := newFirstRunAcceptTermsCmd()

	names := map[string]bool{}

	cmd.Flags().VisitAll(func(f *pflag.Flag) { names[f.Name] = true })

	for _, want := range []string{"bloc", "url", "code", "ca", "yes"} {
		if !names[want] {
			t.Errorf("the command has no --%s", want)
		}
	}

	if len(names) != 5 {
		t.Errorf("the command's flags are %v, want only bloc, url, code, ca, and yes", names)
	}

	if cmd.Flags().Lookup("yes").Shorthand != "y" {
		t.Error("--yes lacks its -y shorthand")
	}

	parent := NewFirstRunCmd()

	found, _, err := parent.Find([]string{"accept-terms"})
	if err != nil || found.Name() != "accept-terms" {
		t.Fatalf("the first-run command doesn't carry accept-terms: %v", err)
	}
}

func TestAcceptTermsRefusesATerminalWithNoWayToHideTheCode(t *testing.T) {
	t.Parallel()

	core := startFakeTermsCore(t)
	d := newFirstRunTestDeps(false, "")
	deps := termsDepsOf(d)
	deps.stdinTerminal = func() bool { return true }

	opts := core.options()
	opts.codeSource = "-"

	err := runFirstRunAcceptTerms(context.Background(), deps, opts)
	if firstrun.CodeOf(err) != codeFirstRunCodeUnreadable {
		t.Fatalf("got %v, want %s", err, codeFirstRunCodeUnreadable)
	}

	deps.readHidden = func() ([]byte, error) { return []byte(firstRunCmdCode), nil }
	opts.assumeYes = true

	err = runFirstRunAcceptTerms(context.Background(), deps, opts)
	if err != nil {
		t.Fatalf("a code typed at a terminal failed: %v", err)
	}

	assertNoSecret(t, "the output", d.out.String())
}

func TestSafeTermsTextShowsEveryControlByte(t *testing.T) {
	t.Parallel()

	got := safeTermsText("a\x1b[2Jb\xffc​d")
	if strings.ContainsAny(got, "\x1b\xff​") || !strings.Contains(got, `\x1b`) || !strings.Contains(got, `\xff`) {
		t.Errorf("got %q", got)
	}

	if long := safeTermsText(strings.Repeat("a", 1000)); len(long) > maxTermsDetail+3 {
		t.Errorf("the text isn't cut: %d bytes", len(long))
	}
}

// termsTestClosedName is the managing system's name, spelled from its code
// points so this file never holds it.
func termsTestClosedName() string {
	return string([]rune{0x63, 0x6f, 0x73, 0x6d, 0x6f, 0x73})
}

func TestAcceptTermsFilesNeverNameTheManagingSystem(t *testing.T) {
	t.Parallel()

	name := termsTestClosedName()

	paths, err := filepath.Glob("firstrun_terms*.go")
	if err != nil {
		t.Fatal(err)
	}

	paths = append(paths, "firstrun.go")

	if len(paths) < 4 {
		t.Fatalf("found only %v", paths)
	}

	for _, path := range paths {
		text, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}

		if strings.Contains(strings.ToLower(string(text)), name) {
			t.Errorf("%s holds the name", path)
		}
	}
}

func TestTermsStatementTakesTermsWithAQuery(t *testing.T) {
	t.Parallel()

	challenge := mustHex(t, termsVectorChallenge)
	good := termsBinding{Audience: "https://core.example", ServerKey: bytes.Repeat([]byte{1}, serverKeySize)}

	for _, terms := range []string{"https://acme.example/t.pdf?x=1", "https://acme.example/t.pdf?"} {
		if _, err := termsStatement(challenge, terms, good); err != nil {
			t.Errorf("%s: %v", terms, err)
		}
	}
}

func TestAcceptTermsTakesALinkWithAQuery(t *testing.T) {
	t.Parallel()

	core := startFakeTermsCore(t)
	core.statusBody = `{"tlsReason":"TLS_TERMS_NOT_ACCEPTED","tlsTerms":"https://acme.example/t.pdf?x=1"}`
	core.claimBody = `{"code":"TLS_TERMS_ACCEPTED","terms":"https://acme.example/t.pdf?x=1"}`
	d := newFirstRunTestDeps(false, "y\n")

	err := runFirstRunAcceptTerms(context.Background(), termsDepsOf(d), core.options())
	if err != nil {
		t.Fatalf("the acceptance failed: %v", err)
	}

	if len(core.seen()) != 3 {
		t.Fatalf("the core saw %v", core.seen())
	}
}
