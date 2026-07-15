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

// ErrBlocCANotFound is returned by LoadBlocCA when no bloc CA is configured
// at secret/ocfp/{bloc}/ca. LoadBlocCA never mints a CA; callers that want
// generate-on-demand semantics call LoadOrGenerateBlocCA instead.
var ErrBlocCANotFound = errors.New("bloc CA not found")

// LoadOrGenerateBlocCA reads the per-bloc internal CA from
// secret/ocfp/{bloc}/ca. If absent, it generates a fresh CA, persists cert+
// key+fingerprint, and returns it. Repeated invocations are idempotent and
// preserve the original fingerprint.
func LoadOrGenerateBlocCA(safe SafeInterface, blocName string) (artifacts.CAMaterial, error) {
	if safe == nil {
		return artifacts.CAMaterial{}, errors.New("safe interface required")
	}

	if blocName == "" {
		return artifacts.CAMaterial{}, errors.New("bloc name required")
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
			return artifacts.CAMaterial{}, fmt.Errorf("%w: %w", ErrBlocCAMalformed, perr)
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

// LoadBlocCA reads the per-bloc internal CA from secret/ocfp/{bloc}/ca and
// never mints one. This is the read-only counterpart to
// LoadOrGenerateBlocCA, for callers (e.g. `ocfp artifacts ca`) that must not
// silently generate trust material as a side effect of a read.
//
// Returns ErrBlocCANotFound when the path has no secret. The SafeInterface
// contract does not distinguish "path absent" from "path unreadable" (both
// surface as a non-nil error from GetAll, or a nil map), so both conditions
// are reported as not-found here — the same conflation LoadOrGenerateBlocCA
// already relies on for its cold-path check.
//
// Returns ErrBlocCAMalformed when a secret exists at the path but is missing
// the cert or key field, so operators can distinguish "never provisioned"
// from "corrupted" and investigate rather than have the corruption silently
// overwritten by a fresh mint.
func LoadBlocCA(safe SafeInterface, blocName string) (artifacts.CAMaterial, error) {
	if safe == nil {
		return artifacts.CAMaterial{}, errors.New("safe interface required")
	}

	if blocName == "" {
		return artifacts.CAMaterial{}, errors.New("bloc name required")
	}

	path := blocCAPath(blocName)

	existing, err := safe.GetAll(path)
	if err != nil || existing == nil {
		return artifacts.CAMaterial{}, fmt.Errorf("%w: %s", ErrBlocCANotFound, path)
	}

	mat, perr := caMaterialFromMap(existing)
	if perr != nil {
		return artifacts.CAMaterial{}, fmt.Errorf("%w: %w", ErrBlocCAMalformed, perr)
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
		return artifacts.CAMaterial{}, errors.New("cert or key missing")
	}

	return artifacts.CAMaterial{
		CertPEM:     cert,
		KeyPEM:      key,
		Fingerprint: fp,
	}, nil
}
