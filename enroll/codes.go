package enroll

import "github.com/fivetwenty-io/ocfp-cli/sigchain"

// Error is a coded refusal, the same type the sigchain package refuses
// with, so one CodeOf reads the code of either.
type Error = sigchain.Error

// CodeOf returns the code of the first Error in err's chain, or the
// empty string when err holds none.
func CodeOf(err error) string { return sigchain.CodeOf(err) }

// The codes of the enrollment service. Each names one reason the
// service stopped short of installing the worker, and the service
// reports the code to the managing system so a person can see it.
const (
	// CodeAddressInvalid refuses an enrollment address that does not
	// decode or does not hold to its form.
	CodeAddressInvalid = "ENROLLMENT_ADDRESS_INVALID"
	// CodeSignatureInvalid refuses a signature in the chain that does not
	// verify, or a statement whose domain is wrong.
	CodeSignatureInvalid = sigchain.CodeSignatureInvalid
	// CodeSigningKeyRevoked refuses an online key that the newest
	// revocation statement lists.
	CodeSigningKeyRevoked = sigchain.CodeSigningKeyRevoked
	// CodeSigningKeyWindowInvalid refuses an online key whose statement
	// claims a window longer than 92 days.
	CodeSigningKeyWindowInvalid = sigchain.CodeSigningKeyWindowInvalid
	// CodeSignatureExpired refuses a signature whose window has ended by
	// this machine's clock.
	CodeSignatureExpired = sigchain.CodeSignatureExpired
	// CodeVersionBelowFloor refuses a manifest whose version is below one
	// of the three floors.
	CodeVersionBelowFloor = "WORKER_VERSION_BELOW_FLOOR"
	// CodeDigestMismatch refuses a chunk or a whole file that does not
	// match the manifest.
	CodeDigestMismatch = "WORKER_DIGEST_MISMATCH"
)

// ServiceCodes are the codes of the enrollment service, in the order
// of the checks that produce them.
var ServiceCodes = []string{
	CodeAddressInvalid, CodeSignatureInvalid, CodeSigningKeyRevoked, CodeSigningKeyWindowInvalid,
	CodeSignatureExpired, CodeVersionBelowFloor, CodeDigestMismatch,
}

func refuse(code, detail string) *Error { return &Error{Code: code, Detail: detail} }

// The codes an admission request's answer may refuse with. The
// enrollment service reads them, and the managing system's join service
// answers with them.
const (
	// CodeRequestInvalid refuses a request whose subject, size, encoding,
	// or canonical form is wrong, or whose attestation names one key
	// twice.
	CodeRequestInvalid = "JOIN_REQUEST_INVALID"
	// CodeWrongInstallation refuses an attestation that names another
	// installation, or an org other than the service that received it.
	CodeWrongInstallation = "JOIN_WRONG_INSTALLATION"
	// CodeBlocMismatch refuses a request whose subject and attestation
	// name different blocs.
	CodeBlocMismatch = "JOIN_BLOC_MISMATCH"
	// CodeBlocUnknown refuses a bloc the org doesn't hold. The service
	// asks again, because the bloc may be registered later.
	CodeBlocUnknown = "JOIN_BLOC_UNKNOWN"
	// CodeAttestationInvalid refuses a signature that doesn't verify, or
	// an attestation whose keys don't include the subject's key.
	CodeAttestationInvalid = "JOIN_ATTESTATION_INVALID"
	// CodeHostKeyUnsupported refuses a host key of a type other than
	// ssh-ed25519.
	CodeHostKeyUnsupported = "JOIN_HOST_KEY_UNSUPPORTED"
	// CodeWorkerKeyInUse refuses a worker key enrolled on another bloc.
	CodeWorkerKeyInUse = "JOIN_WORKER_KEY_IN_USE"
	// CodePendingFull refuses a request when its lane's cap is full and
	// nothing can be evicted.
	CodePendingFull = "JOIN_PENDING_FULL"
	// CodeRateLimited refuses a request that a rate limit held back.
	CodeRateLimited = "JOIN_RATE_LIMITED"
	// CodeAlreadyEnrolled refuses the install key once the worker has
	// taken its first full credential.
	CodeAlreadyEnrolled = "WORKER_ALREADY_ENROLLED"
	// CodeUnavailable refuses a request the managing system couldn't
	// decide on now. Its answer carries no detail.
	CodeUnavailable = "SERVICE_UNAVAILABLE"
	// CodeAnswerInvalid refuses, on the asking side, an answer that does
	// not decode, does not hold to its form, or names another request.
	CodeAnswerInvalid = "JOIN_ANSWER_INVALID"
)

// RefusalCodes are the codes a refused admission answer may carry.
var RefusalCodes = []string{
	CodeRequestInvalid, CodeWrongInstallation, CodeBlocMismatch, CodeBlocUnknown,
	CodeAttestationInvalid, CodeHostKeyUnsupported, CodeWorkerKeyInUse, CodePendingFull,
	CodeRateLimited, CodeAlreadyEnrolled, CodeUnavailable,
}

// The codes a release read's answer may refuse with.
const (
	// CodeReleaseSignatureExpired refuses a manifest request when the
	// managing system's newest signature set for its version has lapsed
	// by its own clock. The answer names the version and the end date.
	CodeReleaseSignatureExpired = "RELEASE_SIGNATURE_EXPIRED"
	// CodeReleaseVersionUnavailable refuses a request for a version other
	// than the one the answering copy runs.
	CodeReleaseVersionUnavailable = "RELEASE_VERSION_UNAVAILABLE"
	// CodeReleaseRateLimited refuses a request over a rate limit.
	CodeReleaseRateLimited = "RELEASE_RATE_LIMITED"
	// CodeReleaseRequestInvalid refuses a malformed request.
	CodeReleaseRequestInvalid = "RELEASE_REQUEST_INVALID"
	// CodeWorkerRevoked refuses a worker a person removed. It is the only
	// code that stops a fetch.
	CodeWorkerRevoked = "WORKER_REVOKED"
)

// ReleaseCodes are the codes a refused release answer may carry.
var ReleaseCodes = []string{
	CodeReleaseSignatureExpired, CodeReleaseVersionUnavailable, CodeReleaseRateLimited,
	CodeReleaseRequestInvalid, CodeWorkerRevoked, CodeUnavailable,
}

// StopsFetch reports whether err ends a worker fetch for good. Only
// CodeWorkerRevoked does. Every other error, coded or not, including a
// lapsed signature and a closed connection, is retried after the
// backoff, from a fresh manifest.
func StopsFetch(err error) bool { return CodeOf(err) == CodeWorkerRevoked }
