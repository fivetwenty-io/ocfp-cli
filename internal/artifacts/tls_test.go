package artifacts

import (
	"crypto/x509"
	"encoding/pem"
	"net"
	"strings"
	"testing"
	"time"
)

func TestGenerateSelfSignedTLS_ParseableCert(t *testing.T) {
	t.Parallel()

	cn := "artifacts.lab.example"
	dns := []string{cn, "myblock-artifacts"}
	ips := []net.IP{net.ParseIP("10.0.0.11")}

	mat, err := GenerateSelfSignedTLS(cn, dns, ips)
	if err != nil {
		t.Fatalf("GenerateSelfSignedTLS: %v", err)
	}

	if !strings.Contains(mat.CertPEM, "BEGIN CERTIFICATE") {
		t.Fatalf("cert PEM missing header: %s", mat.CertPEM)
	}

	if !strings.Contains(mat.KeyPEM, "BEGIN EC PRIVATE KEY") {
		t.Fatalf("key PEM missing header: %s", mat.KeyPEM)
	}

	if len(mat.Fingerprint) != 64 {
		t.Errorf("fingerprint length = %d, want 64 hex chars", len(mat.Fingerprint))
	}

	block, _ := pem.Decode([]byte(mat.CertPEM))
	if block == nil {
		t.Fatal("cert PEM did not decode")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}

	if cert.Subject.CommonName != cn {
		t.Errorf("CN = %q, want %q", cert.Subject.CommonName, cn)
	}

	if len(cert.DNSNames) != 2 {
		t.Errorf("DNSNames = %v, want 2 entries", cert.DNSNames)
	}

	if len(cert.IPAddresses) != 1 || !cert.IPAddresses[0].Equal(ips[0]) {
		t.Errorf("IPAddresses = %v, want %v", cert.IPAddresses, ips)
	}
}

// TestGenerateSelfSignedTLS_NotAfterMatchesCert (task 6.2 gap 1) asserts
// TLSMaterial.NotAfter is populated at issuance and matches the certificate's
// own NotAfter, in RFC3339, so vault.WriteArtifacts can persist it without a
// separate PEM-parse step.
func TestGenerateSelfSignedTLS_NotAfterMatchesCert(t *testing.T) {
	t.Parallel()

	mat, err := GenerateSelfSignedTLS("artifacts.lab.example", []string{"artifacts.lab.example"}, nil)
	if err != nil {
		t.Fatalf("GenerateSelfSignedTLS: %v", err)
	}

	if mat.NotAfter == "" {
		t.Fatal("expected non-empty NotAfter")
	}

	parsed, err := time.Parse(time.RFC3339, mat.NotAfter)
	if err != nil {
		t.Fatalf("NotAfter %q did not parse as RFC3339: %v", mat.NotAfter, err)
	}

	block, _ := pem.Decode([]byte(mat.CertPEM))
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}

	if !parsed.Equal(cert.NotAfter) {
		t.Errorf("NotAfter %v does not match the certificate's own NotAfter %v", parsed, cert.NotAfter)
	}
}
