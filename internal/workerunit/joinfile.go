package workerunit

import (
	"bytes"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
)

// The join file's forms, which the join contract defines and this side
// holds the file to before it installs anything.
var (
	// blocForm is the form a bloc id takes, within maxBlocName.
	blocForm = regexp.MustCompile(`^ocfp-[a-z0-9]+(-[a-z0-9]+)*$`)

	// orgForm is the form of an org slug.
	orgForm = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{2,31}$`)

	// installationForm is "inst-" and sixteen characters of lowercase
	// Crockford base 32.
	installationForm = regexp.MustCompile(`^inst-[0-9abcdefghjkmnpqrstvwxyz]{16}$`)

	// versionForm is the form of the worker binary's version.
	versionForm = regexp.MustCompile(`^v?[0-9A-Za-z][0-9A-Za-z.+-]{0,63}$`)

	// sumForm is a lowercase hex SHA256.
	sumForm = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

const maxBlocName = 40

// JoinFileVersion is the only join file version this ocfp reads.
const JoinFileVersion = 1

// The nkeys forms. A public key is the base 32 of a prefix byte, the 32
// bytes of the ed25519 key, and a CRC16. A seed is the base 32 of two
// bytes that hold the seed prefix and the key's own prefix, the 32 seed
// bytes, and a CRC16. The join account is an account key, and the join key
// is an account seed.
const (
	nkeyAccount   = 0
	nkeySeed      = 18 << 3
	nkeyPublicLen = 56
	nkeySeedLen   = 58
	nkeyPublicRaw = 35
	nkeySeedRaw   = 36
	crcPoly       = 0x1021
	crcHighBit    = 0x8000
	byteBits      = 8
	seedKindMask  = 0xf8
	seedHighMask  = 0x07
	seedHighShift = 5
	seedLowShift  = 3
)

var nkeyEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// joinFileWire is the join file as it is written, join key included. It
// lives only inside DecodeJoinFile.
type joinFileWire struct {
	JoinFile

	JoinKey string `json:"joinKey"`
}

// DecodeJoinFile reads a join file as the join contract defines it. It
// refuses a file longer than 64 KiB, an unknown field, trailing data, and a
// file that does not hold to the contract's form. The join key is required
// and checked for its form, and then dropped, so the JoinFile it returns
// holds no copy of it. No error it returns repeats anything the file holds.
func DecodeJoinFile(body []byte) (JoinFile, error) {
	if len(body) > maxJoinFile {
		return JoinFile{}, fmt.Errorf("%w: it is longer than %d bytes", ErrJoinFile, maxJoinFile)
	}

	err := checkVersion(body)
	if err != nil {
		return JoinFile{}, err
	}

	var wire joinFileWire

	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()

	// The decoder's own text can quote the file, so a refusal names only
	// what is at fault.
	err = dec.Decode(&wire)
	if err != nil {
		return JoinFile{}, fmt.Errorf("%w: it does not decode as the join contract's JSON", ErrJoinFile)
	}

	_, err = dec.Token()
	if !errors.Is(err, io.EOF) {
		return JoinFile{}, fmt.Errorf("%w: it holds data after the join file", ErrJoinFile)
	}

	seedOK := validSeed(wire.JoinKey, nkeyAccount)
	wire.JoinKey = ""

	if !seedOK {
		return JoinFile{}, fmt.Errorf("%w: joinKey must be an account seed", ErrJoinFile)
	}

	join := wire.JoinFile

	return join, join.validate()
}

// checkVersion reads the file's version before the strict decode, because
// a newer file may hold fields this ocfp does not know, and it should hear
// that this ocfp is too old rather than that a field is unknown. A missing
// or malformed version is left for the strict decode and validate to
// refuse.
func checkVersion(body []byte) error {
	var head struct {
		Version json.Number `json:"version"`
	}

	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()

	v, ok := newerVersion(dec.Decode(&head), head.Version)
	if !ok {
		return nil
	}

	return fmt.Errorf("%w: the join file has version %d, and this ocfp reads only version %d, "+
		"so this ocfp is too old; upgrade ocfp and run it again", ErrJoinFileTooNew, v, JoinFileVersion)
}

// newerVersion reports the version when the lenient decode read one that
// is a whole number above JoinFileVersion.
func newerVersion(decodeErr error, version json.Number) (int64, bool) {
	if decodeErr != nil {
		return 0, false
	}

	v, err := version.Int64()

	return v, err == nil && v > JoinFileVersion
}

// validate holds the join file's fields, all but the join key, to the join
// contract's form.
func (j JoinFile) validate() error {
	refuse := func(field, rule string) error {
		return fmt.Errorf("%w: %s %s", ErrJoinFile, field, rule)
	}

	switch {
	case j.Version != JoinFileVersion:
		return refuse("version", fmt.Sprintf("must be %d", JoinFileVersion))
	case !validURL(j.URL):
		return refuse("url", "must be a wss URL with a host and no user, query, or fragment")
	case j.CAPEM != "" && !onlyCertificates(j.CAPEM):
		return refuse("caPEM", "must be PEM certificates and nothing else")
	case !installationForm.MatchString(j.Installation):
		return refuse("installation", "must be an installation id")
	case !orgForm.MatchString(j.Org):
		return refuse("org", "must be an org slug")
	case len(j.Bloc) > maxBlocName || !blocForm.MatchString(j.Bloc):
		return refuse("bloc", "must be a bloc id such as ocfp-lab-1")
	case !validPublicKey(j.JoinAccount, nkeyAccount):
		return refuse("joinAccount", "must be an account public key")
	case !versionForm.MatchString(j.Binary.Version):
		return refuse("binary.version", "must be the worker binary's version")
	case len(j.Binary.SHA256) == 0:
		return refuse("binary.sha256", "must list the worker binary's SHA256 for at least one architecture")
	case j.IssuedAt.IsZero():
		return refuse("issuedAt", "must be set")
	}

	for arch, sha := range j.Binary.SHA256 {
		if arch != ArchAMD64 && arch != ArchARM64 {
			return fmt.Errorf("%w: the join file's binary.sha256 has the key %q, and only %s and %s are allowed",
				ErrBundleLayout, arch, ArchAMD64, ArchARM64)
		}

		if !sumForm.MatchString(sha) {
			return refuse("binary.sha256", "for "+arch+" must be a lowercase hex SHA256")
		}
	}

	return nil
}

// validURL reports whether raw is a wss URL with a host and no user info,
// query, or fragment.
func validURL(raw string) bool {
	u, err := url.Parse(raw)

	return err == nil && u.Scheme == "wss" && u.Hostname() != "" && u.User == nil &&
		u.RawQuery == "" && u.Fragment == ""
}

// onlyCertificates reports whether s is one or more PEM certificate blocks
// and white space.
func onlyCertificates(s string) bool {
	rest := []byte(s)
	count := 0

	for {
		var block *pem.Block

		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}

		if block.Type != "CERTIFICATE" {
			return false
		}

		count++
	}

	return count > 0 && strings.TrimSpace(string(rest)) == ""
}

// validPublicKey reports whether key is an nkeys public key of the kind
// prefix names.
func validPublicKey(key string, prefix byte) bool {
	if len(key) != nkeyPublicLen {
		return false
	}

	raw, err := nkeyEncoding.DecodeString(key)

	return err == nil && len(raw) == nkeyPublicRaw && raw[0] == prefix && crcOK(raw)
}

// validSeed reports whether seed is an nkeys seed for a key of the kind
// prefix names. It reads the seed only to check its form.
func validSeed(seed string, prefix byte) bool {
	if len(seed) != nkeySeedLen {
		return false
	}

	raw, err := nkeyEncoding.DecodeString(seed)
	defer clear(raw)

	if err != nil || len(raw) != nkeySeedRaw || raw[0]&seedKindMask != nkeySeed {
		return false
	}

	return (raw[0]&seedHighMask)<<seedHighShift|raw[1]>>seedLowShift == prefix && crcOK(raw)
}

// crcOK checks the little-endian XMODEM CRC16 at the end of raw.
func crcOK(raw []byte) bool {
	n := len(raw) - 2

	var crc uint16

	for _, c := range raw[:n] {
		crc ^= uint16(c) << byteBits

		for range byteBits {
			if crc&crcHighBit != 0 {
				crc = crc<<1 ^ crcPoly
			} else {
				crc <<= 1
			}
		}
	}

	return crc == binary.LittleEndian.Uint16(raw[n:])
}
