package enroll

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/fivetwenty-io/ocfp-cli/sigchain"
)

// ConfirmDomain is the domain the confirmation code hashes before the
// host key's digest, so the code is never the digest itself.
const ConfirmDomain = "ocfp-bastion-host-key-confirm-v1"

// The confirmation code's shape: 12 characters of RFC 4648 base32, 60
// bits, printed as three groups of four joined by hyphens.
const (
	codeChars = 12
	codeGroup = 4
)

// FingerprintPrefix begins every fingerprint, as ssh-keygen -l prints it.
const FingerprintPrefix = "SHA256:"

var codeEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// HostKeyDigest returns D, the SHA-256 of an ssh-ed25519 host key's SSH
// wire-format blob, from the key as a .pub file or an authorized_keys
// line gives it. It refuses every other key type with
// CodeHostKeyUnsupported.
func HostKeyDigest(hostKey string) ([sha256.Size]byte, error) {
	fields := strings.Fields(hostKey)
	if len(fields) >= 1 && fields[0] != ssh.KeyAlgoED25519 {
		return [sha256.Size]byte{}, refuse(CodeHostKeyUnsupported, "the host key is not an ssh-ed25519 key")
	}

	pub, err := sigchain.ParsePublicKey(hostKey)
	if err != nil {
		return [sha256.Size]byte{}, refuse(CodeRequestInvalid, "the host key does not parse")
	}

	return sha256.Sum256(pub.Marshal()), nil
}

// Fingerprint returns the fingerprint of the host key whose digest is
// d, as ssh-keygen -l prints it: SHA256: and the unpadded standard
// base64 of d.
func Fingerprint(d [sha256.Size]byte) string {
	return FingerprintPrefix + base64.RawStdEncoding.EncodeToString(d[:])
}

// ParseFingerprint returns the digest a fingerprint encodes. It takes
// only the exact form Fingerprint prints.
func ParseFingerprint(fp string) ([sha256.Size]byte, error) {
	var d [sha256.Size]byte

	b64, ok := strings.CutPrefix(fp, FingerprintPrefix)
	if !ok {
		return d, errors.New("enroll: the fingerprint does not begin with SHA256")
	}

	raw, err := base64.RawStdEncoding.Strict().DecodeString(b64)
	if err != nil || len(raw) != sha256.Size {
		return d, errors.New("enroll: the fingerprint is not the base64 of a SHA-256")
	}

	copy(d[:], raw)

	return d, nil
}

// ConfirmationCode returns the confirmation code of the host key whose
// digest is d. H is the SHA-256 of ConfirmDomain, one zero byte, and d.
// The code is the first 12 characters of the upper-case, unpadded RFC
// 4648 base32 of H, printed as three groups of four joined by hyphens.
func ConfirmationCode(d [sha256.Size]byte) string {
	raw := confirmRaw(d)

	return raw[:codeGroup] + "-" + raw[codeGroup:2*codeGroup] + "-" + raw[2*codeGroup:]
}

func confirmRaw(d [sha256.Size]byte) string {
	h := sha256.New()
	h.Write([]byte(ConfirmDomain))
	h.Write([]byte{0})
	h.Write(d[:])

	return codeEncoding.EncodeToString(h.Sum(nil))[:codeChars]
}

// NormalizeCode returns a typed confirmation code in its bare form of 12
// characters. It drops ASCII spaces and hyphens, upper-cases ASCII
// letters, and reads 0 as O and 1 as I. It reads the input byte by byte,
// so a tab, a non-ASCII space, or a letter that Unicode folds into the
// alphabet is refused. It also refuses what is then not exactly 12
// characters of the base32 alphabet.
func NormalizeCode(typed string) (string, error) {
	var b strings.Builder

	for i := range len(typed) {
		c := typed[i]

		switch {
		case c == ' ' || c == '-':
		case c == '0':
			b.WriteByte('O')
		case c == '1':
			b.WriteByte('I')
		case c >= 'a' && c <= 'z':
			b.WriteByte(c - ('a' - 'A'))
		case (c >= 'A' && c <= 'Z') || (c >= '2' && c <= '7'):
			b.WriteByte(c)
		default:
			return "", errors.New("enroll: the confirmation code holds a character outside its alphabet")
		}
	}

	if b.Len() != codeChars {
		return "", errors.New("enroll: the confirmation code is not 12 characters")
	}

	return b.String(), nil
}

// CodeMatches reports whether typed, once normalized, is the
// confirmation code of the host key whose digest is d. It compares in
// constant time.
func CodeMatches(typed string, d [sha256.Size]byte) bool {
	norm, err := NormalizeCode(typed)
	if err != nil {
		return false
	}

	return subtle.ConstantTimeCompare([]byte(norm), []byte(confirmRaw(d))) == 1
}
