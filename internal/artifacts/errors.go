// Package artifacts orchestrates the ocfp-artifacts VM (RustFS S3 blobstore).
// The VM runs RustFS on a ZFS dataset and serves the BOSH director plus all
// Cloud Foundry blobstore buckets for the bloc. Opt-in via Artifacts.Enabled.
package artifacts

import "errors"

// Sentinel errors for the artifacts package.
var (
	// ErrDisabled is returned when CreateArtifacts is called but the feature is opted-out.
	// Callers use this to short-circuit cleanly without surfacing an error to the user.
	ErrDisabled = errors.New("artifacts feature disabled")

	// ErrProviderUnsupported is returned when a non-PVE provider is in use.
	ErrProviderUnsupported = errors.New("artifacts requires the pve provider in v1")

	// ErrBlocNameRequired is returned when the bloc name is empty.
	ErrBlocNameRequired = errors.New("bloc name is required")

	// ErrSubnetRequired is returned when no usable subnet is configured.
	ErrSubnetRequired = errors.New("bloc subnet is required for artifacts IP allocation")

	// ErrReadinessTimeout is returned when the RustFS S3 endpoint does not respond
	// within the configured wait window after VM boot.
	ErrReadinessTimeout = errors.New("artifacts RustFS endpoint readiness probe timed out")

	// ErrSecretRotationRequiresReset is returned by RotateCredentials when
	// the --reset-all flag is not provided. RustFS issue #2852 invalidates
	// previously issued keys on secret rotation, so the operator must
	// acknowledge the blast radius explicitly.
	ErrSecretRotationRequiresReset = errors.New("rotating RustFS credentials invalidates all keys; pass --reset-all to confirm")

	// ErrCABlocNameRequired is returned when GenerateInternalCA is called with
	// an empty bloc name.
	ErrCABlocNameRequired = errors.New("bloc name required for CA generation")

	// ErrCAMaterialIncomplete is returned when IssueLeafCert is called with
	// a CAMaterial that is missing the cert or key PEM.
	ErrCAMaterialIncomplete = errors.New("CA material missing cert or key")

	// ErrCACertPEMInvalid is returned when the CA cert PEM cannot be decoded
	// as a CERTIFICATE block.
	ErrCACertPEMInvalid = errors.New("CA cert PEM is not a CERTIFICATE block")

	// ErrCAKeyPEMInvalid is returned when the CA key PEM cannot be decoded.
	ErrCAKeyPEMInvalid = errors.New("CA key PEM not decodable")

	// ErrCACertNoPEM is returned by newS3Client when the supplied CA cert
	// PEM contains no parseable certificates.
	ErrCACertNoPEM = errors.New("artifacts CA cert: no certificates parsed")
)
