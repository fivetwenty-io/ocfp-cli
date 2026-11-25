package sigchain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"
)

// RevocationDomain is the domain of the revocation statement and the
// namespace its signature is made under. One statement covers every
// online key the root has signed, of every kind.
const RevocationDomain = "signing-key-revocation-v1"

// The bounds of the two statements' bytes.
const (
	MaxStatement  = 4 << 10
	MaxRevocation = 64 << 10
)

// Statement is the root's statement about one online key. Key is the
// online key as its type and base64 blob. Serial is unique across every
// online key the root signs, of every kind. The key may sign from
// NotBefore to NotAfter, both UTC seconds.
type Statement struct {
	Domain    string    `json:"domain"`
	Key       string    `json:"key"`
	Serial    uint64    `json:"serial"`
	NotBefore time.Time `json:"notBefore"`
	NotAfter  time.Time `json:"notAfter"`
}

// Canonical returns the statement's canonical bytes, which the root
// signs: compact JSON with the fields in the order of the type and no
// trailing newline.
func (s Statement) Canonical() ([]byte, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}

	return canonical(s)
}

// Window returns how long the statement lets its key sign.
func (s Statement) Window() time.Duration { return s.NotAfter.Sub(s.NotBefore) }

func (s Statement) validate() error {
	switch {
	case s.Domain == "":
		return errors.New("sigchain: the statement has no domain")
	case s.Serial == 0:
		return errors.New("sigchain: the statement's serial is zero")
	case !utcSecond(s.NotBefore) || !utcSecond(s.NotAfter):
		return errors.New("sigchain: the statement's window is not two UTC seconds")
	case !s.NotAfter.After(s.NotBefore):
		return errors.New("sigchain: the statement's window ends before it begins")
	}

	pub, err := ParsePublicKey(s.Key)
	if err != nil {
		return err
	}

	if KeyLine(pub) != s.Key {
		return errors.New("sigchain: the statement's key is not a type and a blob alone")
	}

	return nil
}

// ParseStatement reads a statement's bytes and refuses any bytes but
// the canonical form of a statement whose domain is domain.
func ParseStatement(b []byte, domain string) (Statement, error) {
	if len(b) == 0 || len(b) > MaxStatement {
		return Statement{}, fmt.Errorf("sigchain: the statement is empty or longer than %d bytes", MaxStatement)
	}

	s, err := decodeCanonical[Statement](b)
	if err != nil {
		return Statement{}, fmt.Errorf("sigchain: the statement: %w", err)
	}

	if s.Domain != domain {
		return Statement{}, fmt.Errorf("sigchain: the statement's domain is %q, not %q", s.Domain, domain)
	}

	return s, nil
}

// Revocation is the root's statement of every online key it withdrew
// before its window ended. Sequence rises by one with each statement the
// root signs, and Serials is in ascending order with no repeats.
type Revocation struct {
	Domain   string    `json:"domain"`
	Sequence uint64    `json:"sequence"`
	SignedAt time.Time `json:"signedAt"`
	Serials  []uint64  `json:"serials"`
}

// Canonical returns the revocation's canonical bytes, which the root
// signs.
func (r Revocation) Canonical() ([]byte, error) {
	if err := r.validate(); err != nil {
		return nil, err
	}

	return canonical(r)
}

// Revokes reports whether the revocation lists serial.
func (r Revocation) Revokes(serial uint64) bool {
	_, found := slices.BinarySearch(r.Serials, serial)

	return found
}

func (r Revocation) validate() error {
	switch {
	case r.Domain != RevocationDomain:
		return fmt.Errorf("sigchain: the revocation's domain is not %s", RevocationDomain)
	case r.Sequence == 0:
		return errors.New("sigchain: the revocation's sequence is zero")
	case !utcSecond(r.SignedAt):
		return errors.New("sigchain: the revocation's time is not a UTC second")
	case r.Serials == nil:
		return errors.New("sigchain: the revocation has no serial list")
	}

	for i, s := range r.Serials {
		if s == 0 || (i > 0 && s <= r.Serials[i-1]) {
			return errors.New("sigchain: the revocation's serials are not ascending and unique")
		}
	}

	return nil
}

// ParseRevocation reads a revocation's bytes and refuses any bytes but
// its canonical form.
func ParseRevocation(b []byte) (Revocation, error) {
	if len(b) == 0 || len(b) > MaxRevocation {
		return Revocation{}, fmt.Errorf("sigchain: the revocation is empty or longer than %d bytes", MaxRevocation)
	}

	r, err := decodeCanonical[Revocation](b)
	if err != nil {
		return Revocation{}, fmt.Errorf("sigchain: the revocation: %w", err)
	}

	return r, nil
}

func utcSecond(t time.Time) bool {
	return !t.IsZero() && t.Location() == time.UTC && t.Equal(t.Truncate(time.Second))
}

// canonical encodes v as compact JSON with no HTML escaping and no
// trailing newline.
func canonical(v any) ([]byte, error) {
	var buf bytes.Buffer

	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)

	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("sigchain: encode: %w", err)
	}

	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// canonicaler is a document with a canonical form.
type canonicaler interface {
	Canonical() ([]byte, error)
}

// decodeCanonical decodes b into a T, refusing an unknown field and data
// after the value, and then refuses b unless it equals the T's canonical
// bytes.
func decodeCanonical[T canonicaler](b []byte) (T, error) {
	var v T

	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()

	if err := dec.Decode(&v); err != nil {
		return v, fmt.Errorf("does not decode: %w", err)
	}

	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return v, errors.New("has data after its value")
	}

	want, err := v.Canonical()
	if err != nil {
		return v, err
	}

	if !bytes.Equal(want, b) {
		return v, errors.New("is not in its canonical form")
	}

	return v, nil
}
