package sigchain

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"strings"

	"golang.org/x/crypto/ssh"
)

// The SSHSIG format, as OpenSSH's PROTOCOL.sshsig lays it out. Sign
// writes what ssh-keygen -Y sign writes, and Verify reads what it
// writes.
const (
	sigMagic   = "SSHSIG"
	sigVersion = 1
	armorBegin = "-----BEGIN SSH SIGNATURE-----"
	armorEnd   = "-----END SSH SIGNATURE-----"
	// armorWidth is the width at which ssh-keygen wraps the base64 body.
	armorWidth = 70
	// hashSHA512 is the hash ssh-keygen uses by default, and the one Sign
	// uses.
	hashSHA512 = "sha512"
	hashSHA256 = "sha256"
)

// MaxSignature bounds an armored signature. An Ed25519 SSHSIG is about
// 300 bytes, so anything near this bound is not one we made.
const MaxSignature = 2 << 10

// ErrSignature is the error every failed signature check wraps.
var ErrSignature = errors.New("sigchain: the signature does not verify")

// Sign returns the armored SSHSIG of msg under namespace, made with key
// and the sha512 hash, byte for byte as ssh-keygen -Y sign makes it.
func Sign(key ed25519.PrivateKey, namespace string, msg []byte) ([]byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, errors.New("sigchain: the signing key is not an Ed25519 key")
	}

	if namespace == "" {
		return nil, errors.New("sigchain: the namespace is empty")
	}

	pub, err := ssh.NewPublicKey(key.Public())
	if err != nil {
		return nil, fmt.Errorf("sigchain: the signing key has no public key: %w", err)
	}

	digest := sha512.Sum512(msg)
	raw := ed25519.Sign(key, signedData(namespace, hashSHA512, digest[:]))

	var sig bytes.Buffer
	writeString(&sig, []byte(ssh.KeyAlgoED25519))
	writeString(&sig, raw)

	var blob bytes.Buffer
	blob.WriteString(sigMagic)
	_ = binary.Write(&blob, binary.BigEndian, uint32(sigVersion))
	writeString(&blob, pub.Marshal())
	writeString(&blob, []byte(namespace))
	writeString(&blob, nil)
	writeString(&blob, []byte(hashSHA512))
	writeString(&blob, sig.Bytes())

	return armor(blob.Bytes()), nil
}

// Verify checks that armored is an SSHSIG of msg under namespace, made
// by pub. It accepts the sha512 and sha256 hashes, as ssh-keygen does,
// and only an Ed25519 key. Every refusal wraps ErrSignature.
func Verify(pub ssh.PublicKey, namespace string, msg, armored []byte) error {
	if pub == nil || pub.Type() != ssh.KeyAlgoED25519 {
		return fmt.Errorf("%w: the key is not an Ed25519 key", ErrSignature)
	}

	blob, err := dearmor(armored)
	if err != nil {
		return err
	}

	parsed, err := parseSigBlob(blob)
	if err != nil {
		return err
	}

	if !bytes.Equal(parsed.publicKey, pub.Marshal()) {
		return fmt.Errorf("%w: the signature names another key", ErrSignature)
	}

	if parsed.namespace != namespace {
		return fmt.Errorf("%w: the signature's namespace is not %s", ErrSignature, namespace)
	}

	var h hash.Hash

	switch parsed.hashAlg {
	case hashSHA512:
		h = sha512.New()
	case hashSHA256:
		h = sha256.New()
	default:
		return fmt.Errorf("%w: the signature's hash is not sha512 or sha256", ErrSignature)
	}

	h.Write(msg)

	sig := &ssh.Signature{Format: parsed.sigFormat, Blob: parsed.sigBlob}
	if sig.Format != ssh.KeyAlgoED25519 {
		return fmt.Errorf("%w: the signature is not an Ed25519 signature", ErrSignature)
	}

	if err := pub.Verify(signedData(namespace, parsed.hashAlg, h.Sum(nil)), sig); err != nil {
		return fmt.Errorf("%w: %w", ErrSignature, err)
	}

	return nil
}

// ParsePublicKey reads an Ed25519 public key from an authorized_keys
// line or a .pub file's line, with or without a comment.
func ParsePublicKey(line string) (ssh.PublicKey, error) {
	pub, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		return nil, fmt.Errorf("sigchain: the key line does not parse: %w", err)
	}

	if strings.TrimSpace(string(rest)) != "" {
		return nil, errors.New("sigchain: the key line holds more than one key")
	}

	if pub.Type() != ssh.KeyAlgoED25519 {
		return nil, fmt.Errorf("sigchain: the key is %s, not %s", pub.Type(), ssh.KeyAlgoED25519)
	}

	return pub, nil
}

// KeyLine returns a public key as its type and base64 blob, the form a
// statement names it in, with no comment and no newline.
func KeyLine(pub ssh.PublicKey) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
}

type sigBlob struct {
	publicKey []byte
	namespace string
	hashAlg   string
	sigFormat string
	sigBlob   []byte
}

func parseSigBlob(blob []byte) (sigBlob, error) {
	bad := func(what string) error { return fmt.Errorf("%w: %s", ErrSignature, what) }

	rest, ok := bytes.CutPrefix(blob, []byte(sigMagic))
	if !ok {
		return sigBlob{}, bad("the signature does not begin with SSHSIG")
	}

	const versionLen = 4
	if len(rest) < versionLen || binary.BigEndian.Uint32(rest) != sigVersion {
		return sigBlob{}, bad("the signature's version is not 1")
	}

	rest = rest[versionLen:]

	var fields [5][]byte

	for i := range fields {
		var err error

		fields[i], rest, err = readString(rest)
		if err != nil {
			return sigBlob{}, bad("the signature is cut short")
		}
	}

	if len(rest) != 0 {
		return sigBlob{}, bad("the signature has bytes after its fields")
	}

	format, after, err := readString(fields[4])
	if err != nil {
		return sigBlob{}, bad("the signature's inner signature is cut short")
	}

	raw, after, err := readString(after)
	if err != nil || len(after) != 0 {
		return sigBlob{}, bad("the signature's inner signature is malformed")
	}

	return sigBlob{
		publicKey: fields[0],
		namespace: string(fields[1]),
		hashAlg:   string(fields[3]),
		sigFormat: string(format),
		sigBlob:   raw,
	}, nil
}

// signedData is the blob that the key signs, which binds the namespace
// and the hash to the message's digest.
func signedData(namespace, hashAlg string, digest []byte) []byte {
	var b bytes.Buffer
	b.WriteString(sigMagic)
	writeString(&b, []byte(namespace))
	writeString(&b, nil)
	writeString(&b, []byte(hashAlg))
	writeString(&b, digest)

	return b.Bytes()
}

func writeString(b *bytes.Buffer, s []byte) {
	_ = binary.Write(b, binary.BigEndian, uint32(len(s))) //nolint:gosec // every field we write is far below 4 GiB
	b.Write(s)
}

func readString(b []byte) (s, rest []byte, err error) {
	const lenLen = 4
	if len(b) < lenLen {
		return nil, nil, errors.New("short")
	}

	n := binary.BigEndian.Uint32(b)
	if uint64(n) > uint64(len(b)-lenLen) {
		return nil, nil, errors.New("short")
	}

	return b[lenLen : lenLen+int(n)], b[lenLen+int(n):], nil
}

func armor(blob []byte) []byte {
	enc := base64.StdEncoding.EncodeToString(blob)

	var b bytes.Buffer
	b.WriteString(armorBegin + "\n")

	for len(enc) > armorWidth {
		b.WriteString(enc[:armorWidth] + "\n")
		enc = enc[armorWidth:]
	}

	b.WriteString(enc + "\n")
	b.WriteString(armorEnd + "\n")

	return b.Bytes()
}

func dearmor(armored []byte) ([]byte, error) {
	if len(armored) == 0 || len(armored) > MaxSignature {
		return nil, fmt.Errorf("%w: the signature is empty or longer than %d bytes", ErrSignature, MaxSignature)
	}

	text := strings.TrimSpace(string(armored))

	body, ok := strings.CutPrefix(text, armorBegin)
	if !ok {
		return nil, fmt.Errorf("%w: the signature has no armor header", ErrSignature)
	}

	body, ok = strings.CutSuffix(body, armorEnd)
	if !ok {
		return nil, fmt.Errorf("%w: the signature has no armor footer", ErrSignature)
	}

	blob, err := base64.StdEncoding.Strict().DecodeString(strings.Join(strings.Fields(body), ""))
	if err != nil {
		return nil, fmt.Errorf("%w: the signature's body is not base64", ErrSignature)
	}

	return blob, nil
}
