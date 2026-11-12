// Package strictjson decodes a JSON object only when every reader would read
// it the same way.
//
// The standard decoder replaces invalid UTF-8 with U+FFFD, keeps the last of
// two equal keys, and matches a key to a field without regard to case. A
// reader in another language may keep the bad bytes, take the first key, or
// match names exactly, and so read different values from the same bytes. When
// those bytes are signed, both readers would trust what they read. Decode
// refuses every such document instead.
package strictjson

import (
	"bytes"
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"unicode/utf8"
)

// Decode decodes b into v, which should be a pointer to a struct. b must hold
// exactly one JSON object with nothing around it but white space. Decode
// refuses, in this order, b that is not valid UTF-8, b that is not one
// object, a value that does not decode into its field, a key that names no
// field at any depth, data after the object, a key that repeats within any
// object, and a key that equals a field's name only when case is ignored.
//
// The UTF-8 check runs on the raw bytes before anything is decoded, because
// the decoder would hide bad bytes behind U+FFFD. Every message escapes the
// input it quotes and bounds its length, so a caller can pass it on as it is.
// On an error v may hold part of the document, and the caller must not use
// it.
func Decode(b []byte, v any) error {
	_, err := decode(b, v, true)

	return err
}

// DecodeWithUnknown is Decode for a document that a newer writer may have
// extended. It runs every check of Decode, but a key that names no field is
// decoded past rather than refused. It returns the top-level keys that name
// no field, in the order they appear, so that the caller can decide what an
// unknown field means. Keys inside an unknown field are still checked for
// repeats, and keys inside a known one are still checked against its fields'
// names.
func DecodeWithUnknown(b []byte, v any) ([]string, error) {
	return decode(b, v, false)
}

func decode(b []byte, v any, refuseUnknown bool) ([]string, error) {
	if !utf8.Valid(b) {
		return nil, errors.New("not valid UTF-8")
	}

	trimmed := bytes.TrimLeft(b, " \t\r\n")
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, errors.New("not a JSON object")
	}

	dec := json.NewDecoder(bytes.NewReader(trimmed))
	if refuseUnknown {
		dec.DisallowUnknownFields()
	}

	if err := dec.Decode(v); err != nil {
		return nil, fmt.Errorf("does not decode: %s", quote(err.Error()))
	}

	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("data after the JSON object")
	}

	w := walker{dec: json.NewDecoder(bytes.NewReader(trimmed))}
	w.dec.UseNumber()

	if err := w.value(shapeOf(reflect.TypeOf(v)), "", true); err != nil {
		return nil, err
	}

	return w.unknown, nil
}

// walker reads a document token by token, after the decoder has accepted it,
// to find what the decoder lets pass.
type walker struct {
	dec     *json.Decoder
	unknown []string
}

// value reads one value whose expected shape is s, which is nil when nothing
// is known about it. path names the value in messages, and top is true only
// for the document itself.
func (w *walker) value(s *shape, path string, top bool) error {
	tok, err := w.dec.Token()
	if err != nil {
		return fmt.Errorf("does not decode: %s", quote(err.Error()))
	}

	switch tok {
	case json.Delim('{'):
		if err := w.object(s, path, top); err != nil {
			return err
		}
	case json.Delim('['):
		var elem *shape
		if s != nil {
			elem = s.elem
		}

		for w.dec.More() {
			if err := w.value(elem, path+"[].", false); err != nil {
				return err
			}
		}
	default:
		return nil
	}

	if _, err := w.dec.Token(); err != nil {
		return fmt.Errorf("does not decode: %s", quote(err.Error()))
	}

	return nil
}

// object reads the keys and values of an object whose opening brace has been
// read.
func (w *walker) object(s *shape, path string, top bool) error {
	seen := map[string]bool{}

	for w.dec.More() {
		tok, err := w.dec.Token()
		if err != nil {
			return fmt.Errorf("does not decode: %s", quote(err.Error()))
		}

		key, ok := tok.(string)
		if !ok {
			return errors.New("an object key is not a string")
		}

		if seen[key] {
			return fmt.Errorf("key %s repeats in %s", quote(path+key), objectName(path))
		}

		seen[key] = true

		child, known, err := s.field(key, path)
		if err != nil {
			return err
		}

		if top && !known {
			w.unknown = append(w.unknown, key)
		}

		if err := w.value(child, path+key+".", false); err != nil {
			return err
		}
	}

	return nil
}

// objectName names the object at path in a message.
func objectName(path string) string {
	if path == "" {
		return "the document"
	}

	return quote(strings.TrimSuffix(path, "."))
}

// shape is what a Go type expects of a JSON value. A struct has fields, a
// slice, array, or map has an element, and anything else has neither.
type shape struct {
	// fields maps each field's JSON name to its value's shape. It is nil
	// unless the type is a struct.
	fields map[string]*shape

	// names lists the keys of fields in a fixed order, so that a message
	// always names the same field.
	names []string

	// elem is the shape of a slice's, array's, or map's elements.
	elem *shape
}

// field returns the shape of key's value inside s, and whether key names a
// field of s. It refuses a key that equals a field's name only when case is
// ignored, because the standard decoder would take it as that field. A key
// inside a map or a value of no known shape names nothing.
func (s *shape) field(key, path string) (*shape, bool, error) {
	if s == nil {
		return nil, false, nil
	}

	if s.fields == nil {
		return s.elem, true, nil
	}

	if child, ok := s.fields[key]; ok {
		return child, true, nil
	}

	for _, name := range s.names {
		if strings.EqualFold(name, key) {
			return nil, false, fmt.Errorf("key %s differs from the field %s only in case", quote(path+key), quote(path+name))
		}
	}

	return nil, false, nil
}

var (
	shapes sync.Map // reflect.Type to *shape

	jsonUnmarshaler = reflect.TypeFor[json.Unmarshaler]()
	textUnmarshaler = reflect.TypeFor[encoding.TextUnmarshaler]()
)

// shapeOf returns the shape of t, building and caching it on first use.
func shapeOf(t reflect.Type) *shape {
	if t == nil {
		return nil
	}

	if s, ok := shapes.Load(t); ok {
		return s.(*shape) //nolint:forcetypeassert // the map holds only shapes
	}

	s := build(t, map[reflect.Type]*shape{})
	shapes.Store(t, s)

	return s
}

// build returns the shape of t. building holds the shapes under construction,
// so that a type that refers to itself ends instead of recursing forever.
func build(t reflect.Type, building map[reflect.Type]*shape) *shape {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	if s, ok := building[t]; ok {
		return s
	}

	// A type that decodes itself takes any JSON, so nothing is known about
	// its keys. The decoder looks for the methods on a pointer as well.
	pt := reflect.PointerTo(t)
	if t.Implements(jsonUnmarshaler) || pt.Implements(jsonUnmarshaler) ||
		t.Implements(textUnmarshaler) || pt.Implements(textUnmarshaler) {
		return nil
	}

	if t.Kind() == reflect.Struct {
		s := &shape{fields: map[string]*shape{}}
		building[t] = s
		addFields(s, t, building)

		return s
	}

	if t.Kind() == reflect.Slice || t.Kind() == reflect.Array || t.Kind() == reflect.Map {
		s := &shape{}
		building[t] = s
		s.elem = build(t.Elem(), building)

		return s
	}

	// Any other kind holds no keys.
	return nil
}

// addFields adds the fields of struct type t to s under the names the
// standard decoder gives them, including the fields of embedded structs.
func addFields(s *shape, t reflect.Type, building map[reflect.Type]*shape) {
	for i := range t.NumField() {
		f := t.Field(i)

		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}

		name, _, _ := strings.Cut(tag, ",")

		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}

		if f.Anonymous && name == "" && ft.Kind() == reflect.Struct {
			addFields(s, ft, building)

			continue
		}

		if !f.IsExported() {
			continue
		}

		if name == "" {
			name = f.Name
		}

		if _, ok := s.fields[name]; ok {
			continue
		}

		s.fields[name] = build(f.Type, building)
		s.names = append(s.names, name)
	}
}

// maxQuoted bounds how many bytes of input a message quotes.
const maxQuoted = 80

// quote returns s quoted with %+q, which escapes every byte outside printable
// ASCII, cut at maxQuoted bytes on a rune boundary, so that input never
// reaches a message unescaped or unbounded.
func quote(s string) string {
	if len(s) <= maxQuoted {
		return fmt.Sprintf("%+q", s)
	}

	cut := maxQuoted
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}

	return fmt.Sprintf("%+q...", s[:cut])
}
