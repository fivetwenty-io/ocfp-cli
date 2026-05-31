package artifacts

import (
	"errors"
	"testing"
)

// newS3Client is the only unit-testable function in buckets.go.
// EnsureBuckets and Probe require a live S3 endpoint.
// ensureBucket requires a live *s3.Client.

func TestNewS3Client_DefaultTLS(t *testing.T) {
	t.Parallel()

	ep := Endpoint{
		URL:       "https://10.0.0.42:9000",
		PathStyle: true,
	}
	creds := Credentials{AccessKey: "ak", SecretKey: "sk"}

	cli, err := newS3Client(ep, creds)
	if err != nil {
		t.Fatalf("newS3Client (default TLS): %v", err)
	}

	if cli == nil {
		t.Fatal("expected non-nil client")
	}
}

func TestNewS3Client_SkipTLSVerify(t *testing.T) {
	t.Parallel()

	ep := Endpoint{
		URL:           "https://10.0.0.42:9000",
		PathStyle:     true,
		SkipTLSVerify: true,
	}
	creds := Credentials{AccessKey: "ak", SecretKey: "sk"}

	cli, err := newS3Client(ep, creds)
	if err != nil {
		t.Fatalf("newS3Client (SkipTLSVerify): %v", err)
	}

	if cli == nil {
		t.Fatal("expected non-nil client")
	}
}

func TestNewS3Client_WithValidCACert(t *testing.T) {
	t.Parallel()

	// Generate a real self-signed cert so AppendCertsFromPEM succeeds.
	mat, err := GenerateSelfSignedTLS("artifacts.test", []string{"artifacts.test"}, nil)
	if err != nil {
		t.Fatalf("GenerateSelfSignedTLS: %v", err)
	}

	ep := Endpoint{
		URL:       "https://10.0.0.42:9000",
		PathStyle: true,
		CACert:    mat.CertPEM,
	}
	creds := Credentials{AccessKey: "ak", SecretKey: "sk"}

	cli, err := newS3Client(ep, creds)
	if err != nil {
		t.Fatalf("newS3Client (valid CA cert): %v", err)
	}

	if cli == nil {
		t.Fatal("expected non-nil client")
	}
}

func TestNewS3Client_InvalidCACert(t *testing.T) {
	t.Parallel()

	ep := Endpoint{
		URL:       "https://10.0.0.42:9000",
		PathStyle: true,
		CACert:    "not-a-pem-block",
	}
	creds := Credentials{AccessKey: "ak", SecretKey: "sk"}

	_, err := newS3Client(ep, creds)
	if err == nil {
		t.Fatal("expected error for invalid CA cert PEM, got nil")
	}

	if !errors.Is(err, ErrCACertNoPEM) {
		t.Errorf("err = %v, want ErrCACertNoPEM", err)
	}
}

func TestNewS3Client_EmptyCACert(t *testing.T) {
	t.Parallel()

	// Empty string takes the default-TLS branch, not the CA branch.
	ep := Endpoint{
		URL:    "https://10.0.0.42:9000",
		CACert: "",
	}
	creds := Credentials{AccessKey: "ak", SecretKey: "sk"}

	cli, err := newS3Client(ep, creds)
	if err != nil {
		t.Fatalf("newS3Client (empty CACert): %v", err)
	}

	if cli == nil {
		t.Fatal("expected non-nil client")
	}
}

func TestNewS3Client_RegionDefaultsToUsEast1(t *testing.T) {
	t.Parallel()

	// newS3Client fills empty Region with "us-east-1" internally.
	// Verify it does not error — the region value is baked into awsCfg;
	// there is no exported accessor on *s3.Client to read it back, so
	// success without error is the observable assertion.
	ep := Endpoint{
		URL:    "https://10.0.0.42:9000",
		Region: "",
	}
	creds := Credentials{AccessKey: "ak", SecretKey: "sk"}

	cli, err := newS3Client(ep, creds)
	if err != nil {
		t.Fatalf("newS3Client (empty region): %v", err)
	}

	if cli == nil {
		t.Fatal("expected non-nil client")
	}
}

func TestNewS3Client_ExplicitRegion(t *testing.T) {
	t.Parallel()

	ep := Endpoint{
		URL:    "https://10.0.0.42:9000",
		Region: "eu-west-1",
	}
	creds := Credentials{AccessKey: "ak", SecretKey: "sk"}

	cli, err := newS3Client(ep, creds)
	if err != nil {
		t.Fatalf("newS3Client (explicit region): %v", err)
	}

	if cli == nil {
		t.Fatal("expected non-nil client")
	}
}

func TestNewS3Client_PathStyleFalse(t *testing.T) {
	t.Parallel()

	ep := Endpoint{
		URL:       "https://bucket.10.0.0.42:9000",
		PathStyle: false,
	}
	creds := Credentials{AccessKey: "ak", SecretKey: "sk"}

	cli, err := newS3Client(ep, creds)
	if err != nil {
		t.Fatalf("newS3Client (PathStyle=false): %v", err)
	}

	if cli == nil {
		t.Fatal("expected non-nil client")
	}
}

func TestNewS3Client_EmptyCredentials(t *testing.T) {
	t.Parallel()

	// Empty creds are syntactically valid — SDK accepts them; auth fails at
	// request time, not at client construction.
	ep := Endpoint{URL: "https://10.0.0.42:9000"}
	creds := Credentials{}

	cli, err := newS3Client(ep, creds)
	if err != nil {
		t.Fatalf("newS3Client (empty creds): %v", err)
	}

	if cli == nil {
		t.Fatal("expected non-nil client")
	}
}

func TestNewS3Client_CACertTakesPrecedenceOverSkipTLS(t *testing.T) {
	t.Parallel()

	// When both CACert and SkipTLSVerify are set, CACert branch runs first
	// (switch-case order). Invalid PEM must still return ErrCACertNoPEM.
	ep := Endpoint{
		URL:           "https://10.0.0.42:9000",
		CACert:        "garbage-pem",
		SkipTLSVerify: true,
	}
	creds := Credentials{AccessKey: "ak", SecretKey: "sk"}

	_, err := newS3Client(ep, creds)
	if err == nil {
		t.Fatal("expected ErrCACertNoPEM when CACert is invalid, even with SkipTLSVerify=true")
	}

	if !errors.Is(err, ErrCACertNoPEM) {
		t.Errorf("err = %v, want ErrCACertNoPEM", err)
	}
}
