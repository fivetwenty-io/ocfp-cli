package enroll

import (
	"bytes"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"regexp"
)

// The forms of the names the formats carry. The managing system holds
// the same rules, and the golden vectors pin both sides to them.
var (
	// orgSlug is the form of an org's name.
	orgSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{2,31}$`)
	// blocName is the form of a bloc's name, within maxBlocName.
	blocName = regexp.MustCompile(`^ocfp-[a-z0-9]+(-[a-z0-9]+)*$`)
	// installationID is "inst-" and sixteen characters of lower-case
	// Crockford base 32.
	installationID = regexp.MustCompile(`^inst-[0-9abcdefghjkmnpqrstvwxyz]{16}$`)
	// workerID is the form of a worker id the managing system assigns.
	workerID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	// requestID is the lower-case hex of a SHA-256.
	requestID = regexp.MustCompile(`^[0-9a-f]{64}$`)
	// subjectRoot is one or more literal bus subject tokens joined by
	// dots, with no wildcard.
	subjectRoot = regexp.MustCompile(`^[A-Za-z0-9_-]+(\.[A-Za-z0-9_-]+)*$`)
	// pinForm is the lower-case hex of a SHA-256.
	pinForm = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

const (
	maxBlocName    = 40
	maxSubjectRoot = 128
)

// ValidOrg reports whether org has the form of an org's name.
func ValidOrg(org string) bool { return orgSlug.MatchString(org) }

// ValidBloc reports whether bloc has the form of a bloc's name.
func ValidBloc(bloc string) bool { return len(bloc) <= maxBlocName && blocName.MatchString(bloc) }

// ValidInstallation reports whether id has the form of an installation
// id.
func ValidInstallation(id string) bool { return installationID.MatchString(id) }

// ValidWorkerID reports whether id has the form of a worker id.
func ValidWorkerID(id string) bool { return workerID.MatchString(id) }

// ValidRequestID reports whether id has the form RequestID gives.
func ValidRequestID(id string) bool { return requestID.MatchString(id) }

func validRoot(root string) bool {
	return len(root) <= maxSubjectRoot && subjectRoot.MatchString(root)
}

// The nkeys forms. A public key is the base32 of a prefix byte, the 32
// bytes of the Ed25519 key, and a CRC16.
const (
	nkeyUser    = 20 << 3
	nkeyAccount = 0
	nkeyPubLen  = 56
	nkeyRawLen  = 35
)

var nkeyEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// ValidUserKey reports whether key is an nkeys user public key, the
// form of a worker key and an install key.
func ValidUserKey(key string) bool { return validPublicKey(key, nkeyUser) }

// ValidAccountKey reports whether key is an nkeys account public key.
func ValidAccountKey(key string) bool { return validPublicKey(key, nkeyAccount) }

func validPublicKey(key string, prefix byte) bool {
	if len(key) != nkeyPubLen {
		return false
	}

	raw, err := nkeyEncoding.DecodeString(key)

	return err == nil && len(raw) == nkeyRawLen && raw[0] == prefix && crcOK(raw)
}

// crcOK checks the little-endian XMODEM CRC16 at the end of raw.
func crcOK(raw []byte) bool {
	n := len(raw) - 2

	var crc uint16

	for _, c := range raw[:n] {
		crc ^= uint16(c) << 8
		for range 8 {
			if crc&0x8000 != 0 {
				crc = crc<<1 ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}

	return crc == binary.LittleEndian.Uint16(raw[n:])
}

// canonicalJSON encodes v as compact JSON with no HTML escaping and no
// trailing newline.
func canonicalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer

	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)

	if err := enc.Encode(v); err != nil {
		return nil, err
	}

	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}
