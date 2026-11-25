package sigchain

import (
	"bytes"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/ssh"
)

// The codes Verify refuses with. A caller reports the code, and the
// detail is for its log.
const (
	// CodeSignatureInvalid refuses a signature in the chain that does
	// not verify, or a statement whose domain is wrong.
	CodeSignatureInvalid = "WORKER_SIGNATURE_INVALID"
	// CodeSigningKeyRevoked refuses an online key whose serial the newest
	// revocation statement lists.
	CodeSigningKeyRevoked = "WORKER_SIGNING_KEY_REVOKED"
	// CodeSigningKeyWindowInvalid refuses a statement whose window is
	// longer than MaxWindow.
	CodeSigningKeyWindowInvalid = "WORKER_SIGNING_KEY_WINDOW_INVALID"
	// CodeSignatureExpired refuses a statement whose window, widened by
	// ClockSkew at each end, does not hold the caller's clock reading.
	CodeSignatureExpired = "WORKER_SIGNATURE_EXPIRED"
)

const (
	// MaxWindow is the longest window a statement may give its key.
	MaxWindow = 92 * 24 * time.Hour
	// ClockSkew is how far the caller's clock may stray at each end of a
	// statement's window.
	ClockSkew = 24 * time.Hour
	// MaxRoots is the most root keys a trust may hold, which lets a new
	// root overlap the old one.
	MaxRoots = 2
)

// Error is a coded refusal. Code is one of the package's codes, and
// Detail says what failed. NotAfter and Clock are set on
// CodeSignatureExpired, so a report can name both the window's end and
// the clock reading that fell outside it.
type Error struct {
	Code     string
	Detail   string
	NotAfter time.Time
	Clock    time.Time
}

func (e *Error) Error() string { return e.Code + ": " + e.Detail }

// CodeOf returns the code of the first Error in err's chain, or the
// empty string when err holds none.
func CodeOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}

	return ""
}

func refuse(code, format string, args ...any) *Error {
	return &Error{Code: code, Detail: fmt.Sprintf(format, args...)}
}

// Kind names the domain of an online key's statement, which is also the
// namespace the root signs it under, and the namespace the online key
// signs its payloads under. Each kind's domains differ, so a key the
// root signed for one kind never verifies a payload of another.
type Kind struct {
	StatementDomain  string
	PayloadNamespace string
}

// The two kinds of online key.
var (
	// WorkerManifest is the kind of key that signs a bloc worker's
	// manifest.
	WorkerManifest = Kind{StatementDomain: "bloc-worker-signing-key-v1", PayloadNamespace: "bloc-worker-manifest-v1"}
	// CLIRelease is the kind of key that signs the checksum file of an
	// ocfp CLI release.
	CLIRelease = Kind{StatementDomain: "ocfp-release-signing-key-v1", PayloadNamespace: "ocfp-release-v1"}
)

// Signed is a document's exact bytes and the armored SSHSIG over them.
type Signed struct {
	Bytes     []byte
	Signature []byte
}

// Set is what a caller verifies: the online key's statement, the payload
// the online key signed, and any revocation statements that came with
// them.
type Set struct {
	Statement   Signed
	Payload     Signed
	Revocations []Signed
}

// Trust is what the caller trusts already: the root keys, and the
// revocation statements it holds, such as the embedded one and any it
// recorded earlier.
type Trust struct {
	Roots       []ssh.PublicKey
	Revocations []Signed
}

// Result is what Verify found. Revocation is the newest revocation
// statement that verified, which the caller records so that a later
// check can't go back to an older one, and HasRevocation says whether
// one did.
type Result struct {
	Statement        Statement
	HasRevocation    bool
	Revocation       Revocation
	RevocationSigned Signed
}

// Verify checks set against trust at the clock reading now, in the five
// steps below, and refuses with a coded Error at the first that fails.
//
//  1. The statement's signature verifies against a root, under the
//     kind's statement domain, and the statement names that domain.
//  2. The newest revocation statement that verifies against a root,
//     from the trust or the set, doesn't list the statement's serial.
//  3. The payload's signature verifies against the statement's key,
//     under the kind's payload namespace.
//  4. The statement's window is no longer than MaxWindow.
//  5. now falls inside the statement's window, widened by ClockSkew at
//     each end.
func Verify(kind Kind, trust Trust, set Set, now time.Time) (Result, error) {
	if len(trust.Roots) == 0 {
		return Result{}, refuse(CodeSignatureInvalid, "no root key is trusted")
	}

	if !verifiesAgainstRoot(trust.Roots, kind.StatementDomain, set.Statement) {
		return Result{}, refuse(CodeSignatureInvalid, "the signing key's statement is not signed by a trusted root")
	}

	st, err := ParseStatement(set.Statement.Bytes, kind.StatementDomain)
	if err != nil {
		return Result{}, refuse(CodeSignatureInvalid, "%v", err)
	}

	res := Result{Statement: st}

	candidates := append(append([]Signed{}, trust.Revocations...), set.Revocations...)
	if rev, signed, ok := NewestRevocation(trust.Roots, candidates); ok {
		res.HasRevocation, res.Revocation, res.RevocationSigned = true, rev, signed
		if rev.Revokes(st.Serial) {
			return res, refuse(CodeSigningKeyRevoked, "the signing key with serial %d is revoked by revocation %d", st.Serial, rev.Sequence)
		}
	}

	key, err := ParsePublicKey(st.Key)
	if err != nil {
		return res, refuse(CodeSignatureInvalid, "%v", err)
	}

	if err := VerifySigned(key, kind.PayloadNamespace, set.Payload); err != nil {
		return res, refuse(CodeSignatureInvalid, "the payload's signature: %v", err)
	}

	if st.Window() > MaxWindow {
		return res, refuse(CodeSigningKeyWindowInvalid, "the signing key's window is %s, longer than %s", st.Window(), MaxWindow)
	}

	clock := now.UTC()
	if clock.Before(st.NotBefore.Add(-ClockSkew)) || clock.After(st.NotAfter.Add(ClockSkew)) {
		e := refuse(CodeSignatureExpired, "the signature is good from %s to %s, and this machine's clock reads %s",
			st.NotBefore.Format(time.RFC3339), st.NotAfter.Format(time.RFC3339), clock.Format(time.RFC3339))
		e.NotAfter, e.Clock = st.NotAfter, clock

		return res, e
	}

	return res, nil
}

// VerifySigned checks one signed document against key under namespace.
func VerifySigned(key ssh.PublicKey, namespace string, s Signed) error {
	return VerifySignature(key, namespace, s.Bytes, s.Signature)
}

// NewestRevocation returns the revocation statement with the highest
// sequence among candidates whose signature verifies against one of
// roots, with its signed bytes. A candidate that does not verify or
// parse is passed over. When two verified candidates share the highest
// sequence, which the root never signs, the one that lists more serials
// wins, so a withdrawal is never lost. ok is false when none verifies.
func NewestRevocation(roots []ssh.PublicKey, candidates []Signed) (Revocation, Signed, bool) {
	var (
		best       Revocation
		bestSigned Signed
		found      bool
	)

	for _, c := range candidates {
		if !verifiesAgainstRoot(roots, RevocationDomain, c) {
			continue
		}

		r, err := ParseRevocation(c.Bytes)
		if err != nil {
			continue
		}

		newer := !found || r.Sequence > best.Sequence ||
			(r.Sequence == best.Sequence && (len(r.Serials) > len(best.Serials) ||
				(len(r.Serials) == len(best.Serials) && bytes.Compare(c.Bytes, bestSigned.Bytes) > 0)))
		if newer {
			best, bestSigned, found = r, c, true
		}
	}

	return best, bestSigned, found
}

func verifiesAgainstRoot(roots []ssh.PublicKey, namespace string, s Signed) bool {
	for _, root := range roots {
		if VerifySigned(root, namespace, s) == nil {
			return true
		}
	}

	return false
}
