package artifacts

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"time"
)

const (
	tlsCertValidityYears = 5
	tlsSerialBits        = 128

	// pemBlockCertificate is the PEM block type header for X.509 certificates.
	pemBlockCertificate = "CERTIFICATE"
	// pemBlockECPrivateKey is the PEM block type header for EC private keys.
	pemBlockECPrivateKey = "EC PRIVATE KEY"
)

// TLSMaterial is the cert/key pair plus a stable SHA-256 fingerprint of the
// DER-encoded certificate. The fingerprint is recorded in state so operators
// can verify they are talking to the expected RustFS instance.
type TLSMaterial struct {
	CertPEM     string
	KeyPEM      string
	Fingerprint string // sha256, lowercase hex, colon-free
	NotAfter    string // RFC3339; the leaf's expiry (task 6.2, leaf-expiry visibility)
}

// GenerateSelfSignedTLS issues an ECDSA P-256 self-signed certificate suitable
// for RustFS native TLS. SAN list includes the provided DNS names and IPs so
// `aws s3 --endpoint-url` works against either the FQDN or the static IP.
func GenerateSelfSignedTLS(commonName string, dnsNames []string, ipAddrs []net.IP) (TLSMaterial, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return TLSMaterial{}, fmt.Errorf("generating ECDSA key: %w", err)
	}

	serialLimit := new(big.Int).Lsh(big.NewInt(1), tlsSerialBits)

	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return TLSMaterial{}, fmt.Errorf("generating cert serial: %w", err)
	}

	notBefore := time.Now().Add(-time.Hour)
	notAfter := notBefore.AddDate(tlsCertValidityYears, 0, 0)

	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              dnsNames,
		IPAddresses:           ipAddrs,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return TLSMaterial{}, fmt.Errorf("creating certificate: %w", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return TLSMaterial{}, fmt.Errorf("marshaling private key: %w", err)
	}

	certPEM := string(pem.EncodeToMemory(&pem.Block{Type: pemBlockCertificate, Bytes: der}))
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: pemBlockECPrivateKey, Bytes: keyDER}))

	sum := sha256.Sum256(der)

	return TLSMaterial{
		CertPEM:     certPEM,
		KeyPEM:      keyPEM,
		Fingerprint: hex.EncodeToString(sum[:]),
		NotAfter:    notAfter.UTC().Format(time.RFC3339),
	}, nil
}
