package artifacts

import (
	"crypto/x509"
	"encoding/pem"
	"net"
	"strings"
	"testing"
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
