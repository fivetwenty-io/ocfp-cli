package artifacts

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

const (
	accessKeyBytes = 10 // 20 hex chars, matches AKIA…-style length
	secretKeyBytes = 20 // 40 hex chars
)

// Credentials hold the RustFS root access/secret pair. The pair is also the
// effective S3 credential for BOSH + CF buckets in v1 (RustFS lacks an admin
// CLI for per-user keys at beta).
type Credentials struct {
	AccessKey string
	SecretKey string
}

// GenerateCredentials produces a fresh random RustFS credential pair using
// crypto/rand. The hex encoding is S3-compatible and tolerates being written
// verbatim into systemd EnvironmentFile values (no quoting needed).
func GenerateCredentials() (Credentials, error) {
	ak, err := randomHex(accessKeyBytes)
	if err != nil {
		return Credentials{}, fmt.Errorf("generating access key: %w", err)
	}

	sk, err := randomHex(secretKeyBytes)
	if err != nil {
		return Credentials{}, fmt.Errorf("generating secret key: %w", err)
	}

	return Credentials{AccessKey: ak, SecretKey: sk}, nil
}

// ResolveCredentials returns the provided credentials when both fields are
// non-empty, otherwise generates a fresh pair. Mixed states (one provided,
// one empty) are treated as "regenerate both" to avoid orphaning a key.
func ResolveCredentials(provided Credentials) (Credentials, error) {
	if provided.AccessKey != "" && provided.SecretKey != "" {
		return provided, nil
	}

	return GenerateCredentials()
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)

	_, err := rand.Read(buf)
	if err != nil {
		return "", fmt.Errorf("reading random bytes: %w", err)
	}

	return hex.EncodeToString(buf), nil
}
