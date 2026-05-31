package vault

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/artifacts"
)

// ErrBlocCAMalformed is returned when the persisted bloc CA material cannot be parsed.
var ErrBlocCAMalformed = errors.New("bloc CA material is malformed")

// LoadOrGenerateBlocCA reads the per-bloc internal CA from
// secret/ocfp/{bloc}/ca. If absent, it generates a fresh CA, persists cert+
// key+fingerprint, and returns it. Repeated invocations are idempotent and
// preserve the original fingerprint.
func LoadOrGenerateBlocCA(safe SafeInterface, blocName string) (artifacts.CAMaterial, error) {
	if safe == nil {
		return artifacts.CAMaterial{}, fmt.Errorf("safe interface required")
	}

	if blocName == "" {
		return artifacts.CAMaterial{}, fmt.Errorf("bloc name required")
	}

	path := blocCAPath(blocName)

	existing, err := safe.GetAll(path)
	if err == nil && existing != nil {
		mat, perr := caMaterialFromMap(existing)
		if perr == nil && mat.CertPEM != "" && mat.KeyPEM != "" {
			return mat, nil
		}

		// Path exists but is missing fields → treat as corrupted, fail loud so
		// the operator can investigate rather than overwrite secret material.
		if perr != nil {
			return artifacts.CAMaterial{}, fmt.Errorf("%w: %v", ErrBlocCAMalformed, perr)
		}
	}

	mat, err := artifacts.GenerateInternalCA(blocName)
	if err != nil {
		return artifacts.CAMaterial{}, fmt.Errorf("generating bloc CA: %w", err)
	}

	body := map[string]interface{}{
		"cert":        mat.CertPEM,
		"key":         mat.KeyPEM,
		"fingerprint": mat.Fingerprint,
		"created_at":  time.Now().UTC().Format(time.RFC3339),
	}

	err = safe.SetMultiple(path, body)
	if err != nil {
		return artifacts.CAMaterial{}, fmt.Errorf("persisting bloc CA at %s: %w", path, err)
	}

	return mat, nil
}

func blocCAPath(blocName string) string {
	return filepath.Join("secret/ocfp", blocName, "ca")
}

func caMaterialFromMap(data map[string]interface{}) (artifacts.CAMaterial, error) {
	cert, _ := data["cert"].(string)
	key, _ := data["key"].(string)
	fp, _ := data["fingerprint"].(string)

	if cert == "" || key == "" {
		return artifacts.CAMaterial{}, fmt.Errorf("cert or key missing")
	}

	return artifacts.CAMaterial{
		CertPEM:     cert,
		KeyPEM:      key,
		Fingerprint: fp,
	}, nil
}
