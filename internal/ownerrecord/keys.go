package ownerrecord

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
)

// smallOrder lists the encodings, with the sign bit cleared, of the Edwards25519
// points whose order divides 8, including the non-canonical encodings of 0 and
// 1. A signature under one of these keys can verify with no private key behind
// it, so no such key is ever accepted.
var smallOrder = [...][ed25519.PublicKeySize]byte{
	// y = 0, order 4
	{},
	// y = 1, the identity
	{0x01},
	// The two y values of the points of order 8.
	{
		0x26, 0xe8, 0x95, 0x8f, 0xc2, 0xb2, 0x27, 0xb0, 0x45, 0xc3, 0xf4, 0x89, 0xf2, 0xef, 0x98, 0xf0,
		0xd5, 0xdf, 0xac, 0x05, 0xd3, 0xc6, 0x33, 0x39, 0xb1, 0x38, 0x02, 0x88, 0x6d, 0x53, 0xfc, 0x05,
	},
	{
		0xc7, 0x17, 0x6a, 0x70, 0x3d, 0x4d, 0xd8, 0x4f, 0xba, 0x3c, 0x0b, 0x76, 0x0d, 0x10, 0x67, 0x0f,
		0x2a, 0x20, 0x53, 0xfa, 0x2c, 0x39, 0xcc, 0xc6, 0x4e, 0xc7, 0xfd, 0x77, 0x92, 0xac, 0x03, 0x7a,
	},
	// y = p - 1, order 2
	{
		0xec, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
		0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x7f,
	},
	// y = p, a non-canonical 0
	{
		0xed, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
		0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x7f,
	},
	// y = p + 1, a non-canonical 1
	{
		0xee, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
		0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x7f,
	},
}

// checkPublicKey refuses a key that is not 32 bytes, that encodes a point of
// small order, or whose encoded y is not below p = 2^255 - 19.
func checkPublicKey(k []byte) error {
	if len(k) != ed25519.PublicKeySize {
		return fmt.Errorf("public key is %d bytes, want %d", len(k), ed25519.PublicKeySize)
	}

	var y [ed25519.PublicKeySize]byte

	copy(y[:], k)
	y[31] &= 0x7f

	for _, s := range smallOrder {
		if y == s {
			return errors.New("public key is a point of small order")
		}
	}

	if !belowP(y) {
		return errors.New("public key is not canonically encoded")
	}

	return nil
}

// belowP reports whether the little-endian y, with its sign bit cleared, is
// below p = 2^255 - 19, whose encoding is 0xed followed by 30 bytes of 0xff
// and 0x7f.
func belowP(y [ed25519.PublicKeySize]byte) bool {
	if y[31] != 0x7f {
		return true
	}

	for i := 30; i >= 1; i-- {
		if y[i] != 0xff {
			return true
		}
	}

	return y[0] < 0xed
}

// fieldSet holds the JSON names of a struct's fields and, for fields that are
// themselves structs, their own names.
type fieldSet struct {
	names    []string
	children map[string]*fieldSet
}

var recordFields = fieldsOf(reflect.TypeFor[Record]())

func fieldsOf(t reflect.Type) *fieldSet {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	fs := &fieldSet{children: map[string]*fieldSet{}}

	for i := range t.NumField() {
		f := t.Field(i)

		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}

		fs.names = append(fs.names, name)

		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}

		if ft.Kind() == reflect.Struct && ft != reflect.TypeFor[time.Time]() {
			fs.children[name] = fieldsOf(ft)
		}
	}

	return fs
}

// checkKeys refuses a JSON document that repeats a key within one object, or
// that holds a key equal to a known field's name only when case is ignored.
// The standard decoder keeps the last of two equal keys and matches names
// without regard to case, so either would let two readers see different
// records in the same signed bytes.
func checkKeys(data []byte, fs *fieldSet) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()

	return walk(dec, fs, "")
}

func walk(dec *json.Decoder, fs *fieldSet, path string) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}

	switch tok {
	case json.Delim('{'):
		seen := map[string]bool{}

		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return err
			}

			key, _ := kt.(string)
			if seen[key] {
				return fmt.Errorf("key %q repeats in %s", path+key, objectName(path))
			}

			seen[key] = true

			child, err := fs.match(key, path)
			if err != nil {
				return err
			}

			if err := walk(dec, child, path+key+"."); err != nil {
				return err
			}
		}

		_, err := dec.Token()

		return err
	case json.Delim('['):
		for dec.More() {
			if err := walk(dec, nil, path+"[]."); err != nil {
				return err
			}
		}

		_, err := dec.Token()

		return err
	}

	return nil
}

// match returns the field set for key's value, or an error when key equals a
// known name only when case is ignored.
func (fs *fieldSet) match(key, path string) (*fieldSet, error) {
	if fs == nil {
		return nil, nil
	}

	for _, n := range fs.names {
		if n == key {
			return fs.children[n], nil
		}

		if strings.EqualFold(n, key) {
			return nil, fmt.Errorf("key %q differs from the field %q only in case", path+key, path+n)
		}
	}

	return nil, nil
}

func objectName(path string) string {
	if path == "" {
		return "the record"
	}

	return strings.TrimSuffix(path, ".")
}
