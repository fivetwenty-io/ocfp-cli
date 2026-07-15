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
	caCertValidityYears   = 10
	leafCertValidityYears = 1
)

// CAMaterial is a CA certificate, its private key, and a stable SHA-256
// fingerprint of the DER-encoded certificate. The fingerprint identifies the
// bloc CA for verification by operators and downstream genesis kits.
type CAMaterial struct {
	CertPEM     string
	KeyPEM      string
	Fingerprint string // sha256, lowercase hex, colon-free
}

// GenerateInternalCA mints a fresh per-bloc CA certificate and key pair. The
// CA is ECDSA P-256 with 10-year validity, CN "ocfp-{bloc}-internal-ca", and
// key usage CertSign|CRLSign. IsCA is true and BasicConstraints are marked
// critical so downstream verifiers honor the constraint.
func GenerateInternalCA(blocName string) (CAMaterial, error) {
	if blocName == "" {
		return CAMaterial{}, ErrCABlocNameRequired
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return CAMaterial{}, fmt.Errorf("generating ECDSA key: %w", err)
	}

	serialLimit := new(big.Int).Lsh(big.NewInt(1), tlsSerialBits)

	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return CAMaterial{}, fmt.Errorf("generating CA serial: %w", err)
	}

	subjectKeyID, err := computeSubjectKeyID(&key.PublicKey)
	if err != nil {
		return CAMaterial{}, fmt.Errorf("computing subject key id: %w", err)
	}

	notBefore := time.Now().Add(-time.Hour)
	notAfter := notBefore.AddDate(caCertValidityYears, 0, 0)

	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: fmt.Sprintf("ocfp-%s-internal-ca", blocName),
		},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
		SubjectKeyId:          subjectKeyID,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return CAMaterial{}, fmt.Errorf("creating CA certificate: %w", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return CAMaterial{}, fmt.Errorf("marshaling CA private key: %w", err)
	}

	certPEM := string(pem.EncodeToMemory(&pem.Block{Type: pemBlockCertificate, Bytes: der}))
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: pemBlockECPrivateKey, Bytes: keyDER}))

	sum := sha256.Sum256(der)

	return CAMaterial{
		CertPEM:     certPEM,
		KeyPEM:      keyPEM,
		Fingerprint: hex.EncodeToString(sum[:]),
	}, nil
}

// IssueLeafCert signs a 1-year leaf certificate for `leafCN` using the supplied
// CA. SAN list includes `dnsNames` and `ips`. The returned TLSMaterial carries
// the leaf cert and its private key — the caller is responsible for trusting
// the CA (the leaf's CertPEM is NOT a trust anchor).
func IssueLeafCert(ca CAMaterial, leafCN string, dnsNames []string, ips []net.IP) (TLSMaterial, error) {
	if ca.CertPEM == "" || ca.KeyPEM == "" {
		return TLSMaterial{}, ErrCAMaterialIncomplete
	}

	caCert, caKey, err := parseCA(ca)
	if err != nil {
		return TLSMaterial{}, err
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return TLSMaterial{}, fmt.Errorf("generating leaf key: %w", err)
	}

	serialLimit := new(big.Int).Lsh(big.NewInt(1), tlsSerialBits)

	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return TLSMaterial{}, fmt.Errorf("generating leaf serial: %w", err)
	}

	notBefore := time.Now().Add(-time.Hour)
	notAfter := notBefore.AddDate(leafCertValidityYears, 0, 0)

	subjectKeyID, err := computeSubjectKeyID(&leafKey.PublicKey)
	if err != nil {
		return TLSMaterial{}, fmt.Errorf("computing subject key id: %w", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: leafCN},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              dnsNames,
		IPAddresses:           ips,
		SubjectKeyId:          subjectKeyID,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		return TLSMaterial{}, fmt.Errorf("signing leaf certificate: %w", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		return TLSMaterial{}, fmt.Errorf("marshaling leaf private key: %w", err)
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

func parseCA(ca CAMaterial) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	certBlock, _ := pem.Decode([]byte(ca.CertPEM))
	if certBlock == nil || certBlock.Type != pemBlockCertificate {
		return nil, nil, ErrCACertPEMInvalid
	}

	caCert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing CA certificate: %w", err)
	}

	keyBlock, _ := pem.Decode([]byte(ca.KeyPEM))
	if keyBlock == nil {
		return nil, nil, ErrCAKeyPEMInvalid
	}

	caKey, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing CA EC private key: %w", err)
	}

	return caCert, caKey, nil
}

func computeSubjectKeyID(pub *ecdsa.PublicKey) ([]byte, error) {
	derPub, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, fmt.Errorf("marshaling PKIX public key: %w", err)
	}

	sum := sha256.Sum256(derPub)

	return sum[:20], nil
}
