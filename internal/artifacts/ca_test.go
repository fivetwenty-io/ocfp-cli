package artifacts

import (
	"crypto/x509"
	"encoding/pem"
	"net"
	"strings"
	"testing"
	"time"
)

func parseCert(t *testing.T, s string) *x509.Certificate {
	t.Helper()

	block, _ := pem.Decode([]byte(s))
	if block == nil {
		t.Fatalf("PEM decode failed")
	}

	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}

	return c
}

func TestGenerateInternalCA_HasCAFlagAndKeyUsage(t *testing.T) {
	t.Parallel()

	mat, err := GenerateInternalCA("pve-wayne")
	if err != nil {
		t.Fatalf("GenerateInternalCA: %v", err)
	}

	c := parseCert(t, mat.CertPEM)

	if !c.IsCA {
		t.Errorf("IsCA = false, want true")
	}

	if !c.BasicConstraintsValid {
		t.Errorf("BasicConstraintsValid = false")
	}

	if c.KeyUsage&x509.KeyUsageCertSign == 0 {
		t.Errorf("KeyUsageCertSign not set")
	}

	if c.KeyUsage&x509.KeyUsageCRLSign == 0 {
		t.Errorf("KeyUsageCRLSign not set")
	}

	if !strings.Contains(c.Subject.CommonName, "pve-wayne") {
		t.Errorf("CommonName = %q, want it to contain bloc name", c.Subject.CommonName)
	}

	validityYears := c.NotAfter.Sub(c.NotBefore).Hours() / 24 / 365
	if validityYears < 9.5 || validityYears > 10.5 {
		t.Errorf("CA validity = %.2f years, want ~10", validityYears)
	}

	if mat.Fingerprint == "" {
		t.Errorf("fingerprint empty")
	}

	if len(mat.Fingerprint) != 64 {
		t.Errorf("fingerprint len = %d, want 64", len(mat.Fingerprint))
	}
}

func TestGenerateInternalCA_RequiresBlocName(t *testing.T) {
	t.Parallel()

	_, err := GenerateInternalCA("")
	if err == nil {
		t.Errorf("expected error for empty bloc name")
	}
}

func TestIssueLeafCert_SignedByCAVerifies(t *testing.T) {
	t.Parallel()

	ca, err := GenerateInternalCA("pve-wayne")
	if err != nil {
		t.Fatalf("GenerateInternalCA: %v", err)
	}

	leaf, err := IssueLeafCert(ca, "pve-wayne-artifacts", []string{"pve-wayne-artifacts"}, []net.IP{net.ParseIP("10.0.0.11")})
	if err != nil {
		t.Fatalf("IssueLeafCert: %v", err)
	}

	caCert := parseCert(t, ca.CertPEM)
	leafCert := parseCert(t, leaf.CertPEM)

	// Issuer DN of leaf must equal Subject DN of CA.
	if leafCert.Issuer.String() != caCert.Subject.String() {
		t.Errorf("leaf issuer = %q, want %q", leafCert.Issuer, caCert.Subject)
	}

	if len(leafCert.AuthorityKeyId) == 0 {
		t.Errorf("AuthorityKeyId not set on leaf")
	}

	// Verify the leaf against a pool containing only the CA.
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(ca.CertPEM)) {
		t.Fatalf("appending CA to pool failed")
	}

	_, err = leafCert.Verify(x509.VerifyOptions{
		Roots:       pool,
		CurrentTime: time.Now(),
		KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSName:     "pve-wayne-artifacts",
	})
	if err != nil {
		t.Errorf("leaf failed to verify against CA pool: %v", err)
	}

	validityYears := leafCert.NotAfter.Sub(leafCert.NotBefore).Hours() / 24 / 365
	if validityYears < 0.9 || validityYears > 1.1 {
		t.Errorf("leaf validity = %.2f years, want ~1", validityYears)
	}
}

func TestIssueLeafCert_RejectsEmptyCA(t *testing.T) {
	t.Parallel()

	_, err := IssueLeafCert(CAMaterial{}, "cn", nil, nil)
	if err == nil {
		t.Errorf("expected error for empty CA")
	}
}
