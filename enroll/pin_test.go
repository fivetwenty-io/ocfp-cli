package enroll

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"testing"
	"time"
)

type testCA struct {
	cert *x509.Certificate
	key  ed25519.PrivateKey
}

func newCA(t *testing.T, name string) testCA {
	t.Helper()

	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}

	return testCA{cert, key}
}

// leaf returns a server certificate for host that ca signs, as a TLS
// certificate whose chain is the leaf followed by extra.
func (ca testCA) leaf(t *testing.T, host string, extra ...*x509.Certificate) tls.Certificate {
	t.Helper()

	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: host}, DNSNames: []string{host},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, key.Public(), ca.key)
	if err != nil {
		t.Fatal(err)
	}

	chain := [][]byte{der}
	for _, c := range extra {
		chain = append(chain, c.Raw)
	}

	return tls.Certificate{Certificate: chain, PrivateKey: key}
}

func parsed(t *testing.T, c tls.Certificate) []*x509.Certificate {
	t.Helper()

	out := make([]*x509.Certificate, 0, len(c.Certificate))

	for _, der := range c.Certificate {
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			t.Fatal(err)
		}

		out = append(out, cert)
	}

	return out
}

const busHost = "bus.example.com"

func TestVerifyPinned(t *testing.T) {
	t.Parallel()

	root := newCA(t, "private root")
	pin := CAPinOf(root.cert)

	got, err := VerifyPinned(parsed(t, root.leaf(t, busHost, root.cert)), busHost, pin)
	if err != nil || !got.Equal(root.cert) {
		t.Fatalf("VerifyPinned = %v, %v", got, err)
	}

	for name, c := range map[string]struct {
		chain []*x509.Certificate
		host  string
	}{
		// The leaf the root did not sign, presented with a copy of the
		// real root, which anyone can copy.
		"unsigned leaf": {parsed(t, newCA(t, "attacker").leaf(t, busHost, root.cert)), busHost},
		"no root":       {parsed(t, root.leaf(t, busHost)), busHost},
		"other host":    {parsed(t, root.leaf(t, "other.example.com", root.cert)), busHost},
		"nothing":       {nil, busHost},
	} {
		if _, err := VerifyPinned(c.chain, c.host, pin); !errors.Is(err, ErrCAPin) {
			t.Errorf("%s: VerifyPinned = %v", name, err)
		}
	}

	// A pinned leaf is not a pinned root.
	leaf := parsed(t, root.leaf(t, busHost, root.cert))
	if _, err := VerifyPinned(leaf, busHost, CAPinOf(leaf[0])); !errors.Is(err, ErrCAPin) {
		t.Errorf("a pinned leaf: %v", err)
	}
}

// handshake runs a TLS handshake over an in-memory pipe, with no network.
func handshake(t *testing.T, server tls.Certificate, client *tls.Config) error {
	t.Helper()

	sc, cc := net.Pipe()
	defer sc.Close()
	defer cc.Close()

	done := make(chan struct{})

	go func() {
		defer close(done)

		s := tls.Server(sc, &tls.Config{Certificates: []tls.Certificate{server}, MinVersion: tls.VersionTLS12})
		_ = s.Handshake()
		_ = s.Close()
	}()

	err := tls.Client(cc, client).Handshake()
	_ = cc.Close()

	<-done

	return err
}

func TestBusTLSConfigPinned(t *testing.T) {
	t.Parallel()

	root := newCA(t, "private root")
	a := goldenAddress(t)
	a.CAPin = CAPinOf(root.cert)

	cfg, pinned, err := BusTLSConfig(a, nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := handshake(t, newCA(t, "attacker").leaf(t, busHost, root.cert), cfg); err == nil {
		t.Fatal("a handshake with a leaf the root did not sign passed")
	}

	if pinned.PEM() != nil {
		t.Fatal("a refused handshake left a root to save")
	}

	if err := handshake(t, root.leaf(t, busHost, root.cert), cfg); err != nil {
		t.Fatalf("the pinned handshake: %v", err)
	}

	saved := pinned.PEM()
	if saved == nil {
		t.Fatal("the pinned handshake left no root to save")
	}

	later, _, err := BusTLSConfig(a, saved)
	if err != nil {
		t.Fatal(err)
	}

	if later.InsecureSkipVerify || later.RootCAs == nil {
		t.Fatal("a saved root does not become the only root")
	}

	if err := handshake(t, root.leaf(t, busHost), later); err != nil {
		t.Errorf("a later handshake against the saved root: %v", err)
	}

	if err := handshake(t, newCA(t, "attacker").leaf(t, busHost, root.cert), later); err == nil {
		t.Error("a later handshake took a leaf the root did not sign")
	}

	other := newCA(t, "other")
	if _, _, err := BusTLSConfig(a, (&PinnedRoot{cert: other.cert}).PEM()); !errors.Is(err, ErrCAPin) {
		t.Errorf("a saved root that is not the pinned one: %v", err)
	}

	if _, _, err := BusTLSConfig(a, []byte("not pem")); !errors.Is(err, ErrCAPin) {
		t.Errorf("a saved root that is not PEM: %v", err)
	}
}

func TestBusTLSConfigUnpinned(t *testing.T) {
	t.Parallel()

	a := goldenAddress(t)
	a.CAPin = ""

	cfg, pinned, err := BusTLSConfig(a, nil)
	if err != nil || pinned != nil || cfg.InsecureSkipVerify || cfg.ServerName != busHost {
		t.Fatalf("BusTLSConfig = %+v, %v, %v", cfg, pinned, err)
	}

	if err := handshake(t, newCA(t, "private").leaf(t, busHost), cfg); err == nil {
		t.Error("an unpinned handshake took a private root")
	}
}
