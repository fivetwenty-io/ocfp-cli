package enroll

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"sync"
)

// ErrCAPin is the error every refusal of the bus's certificate under a
// CA pin wraps.
var ErrCAPin = errors.New("enroll: the bus's certificate does not hold to the CA pin")

// MaxCAFile bounds a saved ca.pem.
const MaxCAFile = 64 << 10

// CAPinOf returns the pin of a certificate: the lower-case hex SHA-256 of
// its DER bytes.
func CAPinOf(cert *x509.Certificate) string {
	d := sha256.Sum256(cert.Raw)

	return hex.EncodeToString(d[:])
}

// VerifyPinned checks the chain a bus presented against pin and host in
// three steps, and returns the root to save as ca.pem.
//
//  1. It finds the presented certificate whose SHA-256 equals the pin.
//     A copy of the root proves nothing, since the root is public, so
//     finding it is only the start.
//  2. It verifies the leaf against that certificate, as the only root,
//     with the other presented certificates as intermediates.
//  3. It checks the leaf's name against host.
//
// Every refusal wraps ErrCAPin.
func VerifyPinned(presented []*x509.Certificate, host, pin string) (*x509.Certificate, error) {
	if len(presented) == 0 {
		return nil, fmt.Errorf("%w: the bus presented no certificate", ErrCAPin)
	}

	var root *x509.Certificate

	for _, c := range presented {
		if CAPinOf(c) == pin {
			root = c

			break
		}
	}

	if root == nil {
		return nil, fmt.Errorf("%w: no presented certificate matches the pin", ErrCAPin)
	}

	if !root.BasicConstraintsValid || !root.IsCA {
		return nil, fmt.Errorf("%w: the pinned certificate is not a CA", ErrCAPin)
	}

	roots := x509.NewCertPool()
	roots.AddCert(root)

	inter := x509.NewCertPool()

	for _, c := range presented[1:] {
		if !c.Equal(root) {
			inter.AddCert(c)
		}
	}

	leaf := presented[0]
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots: roots, Intermediates: inter, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		return nil, fmt.Errorf("%w: the pinned root did not sign the leaf: %w", ErrCAPin, err)
	}

	if err := leaf.VerifyHostname(host); err != nil {
		return nil, fmt.Errorf("%w: the leaf does not name %s", ErrCAPin, host)
	}

	return root, nil
}

// PinnedRoot holds the root a pinned handshake verified, which the
// caller saves as ca.pem only once the checks pass.
type PinnedRoot struct {
	mu   sync.Mutex
	cert *x509.Certificate
}

// PEM returns the verified root as PEM, or nil before a handshake
// verified it.
func (p *PinnedRoot) PEM() []byte {
	if p == nil {
		return nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.cert == nil {
		return nil
	}

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: p.cert.Raw})
}

func (p *PinnedRoot) set(c *x509.Certificate) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.cert = c
}

// BusTLSConfig returns the TLS configuration for the bus that a names.
//
// With no pin, the bus must present a certificate the system's roots
// verify. With a pin and a saved ca.pem, which must hold exactly the
// pinned certificate, the saved root is the only root. With a pin and no
// saved root, the handshake runs the checks of VerifyPinned, and the
// returned PinnedRoot holds the root once they pass, so the caller can
// save it. The configuration fetches nothing; it only checks the
// handshake.
func BusTLSConfig(a Address, saved []byte) (*tls.Config, *PinnedRoot, error) {
	u, err := url.Parse(a.BusURL)
	if err != nil || u.Hostname() == "" {
		return nil, nil, refuse(CodeAddressInvalid, "the bus URL has no host")
	}

	host := u.Hostname()
	cfg := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}

	switch {
	case a.CAPin == "":
		return cfg, nil, nil
	case saved != nil:
		root, err := parseSavedRoot(saved, a.CAPin)
		if err != nil {
			return nil, nil, err
		}

		pool := x509.NewCertPool()
		pool.AddCert(root)
		cfg.RootCAs = pool

		return cfg, nil, nil
	}

	pinned := &PinnedRoot{}
	pin := a.CAPin
	// The pinned root is private, so the standard verification cannot
	// run; VerifyConnection runs the pin's checks in its place and fails
	// the handshake when they fail.
	cfg.InsecureSkipVerify = true //nolint:gosec // VerifyConnection checks the chain against the pin
	cfg.VerifyConnection = func(cs tls.ConnectionState) error {
		root, err := VerifyPinned(cs.PeerCertificates, host, pin)
		if err != nil {
			return err
		}

		pinned.set(root)

		return nil
	}

	return cfg, pinned, nil
}

// parseSavedRoot reads a saved ca.pem, which must hold one certificate
// whose pin is pin.
func parseSavedRoot(saved []byte, pin string) (*x509.Certificate, error) {
	if len(saved) > MaxCAFile {
		return nil, fmt.Errorf("%w: the saved root is longer than %d bytes", ErrCAPin, MaxCAFile)
	}

	block, rest := pem.Decode(saved)
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, fmt.Errorf("%w: the saved root is not one PEM certificate", ErrCAPin)
	}

	root, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%w: the saved root does not parse", ErrCAPin)
	}

	if CAPinOf(root) != pin {
		return nil, fmt.Errorf("%w: the saved root is not the pinned certificate", ErrCAPin)
	}

	return root, nil
}
