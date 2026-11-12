package strictjson_test

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/fivetwenty-io/ocfp-cli/internal/strictjson"
)

type inner struct {
	ID   string `json:"id"`
	Note string `json:"note,omitempty"`
}

type doc struct {
	Name    string            `json:"name"`
	Count   int               `json:"count"`
	Inner   *inner            `json:"inner,omitempty"`
	List    []inner           `json:"list,omitempty"`
	Labels  map[string]string `json:"labels,omitempty"`
	At      time.Time         `json:"at"`
	Skipped string            `json:"-"`
}

const good = `{"name":"a","count":1,"inner":{"id":"x"},"list":[{"id":"y"}],"labels":{"k":"v"},"at":"2026-11-11T12:00:00Z"}`

func wantRefused(t *testing.T, in, phrase string) {
	t.Helper()

	var v doc

	err := strictjson.Decode([]byte(in), &v)
	if err == nil {
		t.Fatalf("Decode accepted %q", in)
	}

	if !strings.Contains(err.Error(), phrase) {
		t.Fatalf("Decode(%q) = %v, want an error holding %q", in, err, phrase)
	}

	if _, err := strictjson.DecodeWithUnknown([]byte(in), &v); err == nil && phrase != "unknown field" {
		t.Fatalf("DecodeWithUnknown accepted %q", in)
	}
}

func TestDecodeAcceptsAStrictDocument(t *testing.T) {
	t.Parallel()

	var got doc

	err := strictjson.Decode([]byte(" \t\r\n"+good+" \n"), &got)
	if err != nil {
		t.Fatal(err)
	}

	want := doc{
		Name: "a", Count: 1, Inner: &inner{ID: "x"}, List: []inner{{ID: "y"}},
		Labels: map[string]string{"k": "v"}, At: time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("decoded %+v, want %+v", got, want)
	}
}

func TestDecodeRefusesInvalidUTF8(t *testing.T) {
	t.Parallel()

	wantRefused(t, strings.Replace(good, `"name":"a"`, "\"name\":\"a\xffb\"", 1), "not valid UTF-8")
	wantRefused(t, strings.Replace(good, `"name"`, "\"\xff\":1,\"name\"", 1), "not valid UTF-8")
	wantRefused(t, strings.Replace(good, `"k":"v"`, "\"k\":\"\xc0\x80\"", 1), "not valid UTF-8")

	// The check runs before the decoder, so it wins over a syntax error and
	// over an unknown field later in the input.
	wantRefused(t, "{\"name\":\"\xff\",", "not valid UTF-8")
	wantRefused(t, "{\"name\":\"\xff\",\"extra\":1}", "not valid UTF-8")
}

func TestDecodeRefusesTrailingData(t *testing.T) {
	t.Parallel()

	for _, tail := range []string{"x", "{}", " {}", good, "]", "null", "\n1"} {
		wantRefused(t, good+tail, "data after the JSON object")
	}
}

func TestDecodeRefusesDuplicateKeys(t *testing.T) {
	t.Parallel()

	for name, in := range map[string]string{
		"top level":       strings.Replace(good, `"count":1`, `"count":1,"count":2`, 1),
		"escaped":         strings.Replace(good, `"count":1`, `"count":1,"\u0063ount":2`, 1),
		"in a struct":     strings.Replace(good, `{"id":"x"}`, `{"id":"x","id":"z"}`, 1),
		"in a list":       strings.Replace(good, `[{"id":"y"}]`, `[{"id":"y","id":"z"}]`, 1),
		"in a map":        strings.Replace(good, `{"k":"v"}`, `{"k":"v","k":"w"}`, 1),
		"in a null field": strings.Replace(good, `"count":1`, `"count":1,"name":null`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			wantRefused(t, in, "repeats")
		})
	}

	// DecodeWithUnknown also refuses a repeat inside a field it does not know.
	_, err := strictjson.DecodeWithUnknown([]byte(`{"future":[{"a":1,"a":2}],"name":"a"}`), new(doc))
	if err == nil || !strings.Contains(err.Error(), "repeats") {
		t.Fatalf("DecodeWithUnknown = %v, want a repeat refused inside an unknown field", err)
	}
}

func TestDecodeRefusesUnknownFields(t *testing.T) {
	t.Parallel()

	wantRefused(t, strings.Replace(good, `"count":1`, `"count":1,"extra":1`, 1), "unknown field")
	wantRefused(t, strings.Replace(good, `{"id":"x"}`, `{"id":"x","extra":1}`, 1), "unknown field")
	wantRefused(t, strings.Replace(good, `[{"id":"y"}]`, `[{"id":"y","extra":1}]`, 1), "unknown field")

	// A field tagged "-" is not a field of the document.
	wantRefused(t, strings.Replace(good, `"count":1`, `"count":1,"-":"x"`, 1), "unknown field")
	wantRefused(t, strings.Replace(good, `"count":1`, `"count":1,"Skipped":"x"`, 1), "unknown field")
}

func TestDecodeRefusesCaseVariants(t *testing.T) {
	t.Parallel()

	for name, in := range map[string]string{
		"alone":             strings.Replace(good, `"count"`, `"COUNT"`, 1),
		"beside the field":  strings.Replace(good, `"count":1`, `"count":1,"Count":2`, 1),
		"long s":            strings.Replace(good, `"labels"`, `"labelſ"`, 1),
		"in a struct":       strings.Replace(good, `{"id":"x"}`, `{"ID":"x"}`, 1),
		"in a list element": strings.Replace(good, `[{"id":"y"}]`, `[{"Id":"y"}]`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			wantRefused(t, in, "only in case")
		})
	}

	// Map keys are data, not field names, so keys that differ only in case
	// are two keys.
	var v doc
	if err := strictjson.Decode([]byte(strings.Replace(good, `{"k":"v"}`, `{"k":"v","K":"w"}`, 1)), &v); err != nil {
		t.Fatalf("map keys that differ only in case were refused: %v", err)
	}
}

func TestDecodeRefusesAnythingButOneObject(t *testing.T) {
	t.Parallel()

	for _, in := range []string{"", " \n", "null", "[]", `"x"`, "1", "true", good[:len(good)-1]} {
		var v doc
		if err := strictjson.Decode([]byte(in), &v); err == nil {
			t.Errorf("Decode accepted %q", in)
		}
	}
}

func TestDecodeWithUnknownReturnsTopLevelUnknownKeys(t *testing.T) {
	t.Parallel()

	in := `{"zeta":1,"name":"a","inner":{"id":"x","deep":1},"alpha":{"NAME":1},"count":2}`

	var v doc

	unknown, err := strictjson.DecodeWithUnknown([]byte(in), &v)
	if err != nil {
		t.Fatal(err)
	}

	if want := []string{"zeta", "alpha"}; !slices.Equal(unknown, want) {
		t.Fatalf("unknown = %q, want %q in document order", unknown, want)
	}

	if v.Name != "a" || v.Count != 2 || v.Inner == nil || v.Inner.ID != "x" {
		t.Fatalf("decoded %+v", v)
	}

	// A case variant is refused even where an unknown field is not.
	if _, err := strictjson.DecodeWithUnknown([]byte(`{"NAME":"a"}`), &v); err == nil || !strings.Contains(err.Error(), "only in case") {
		t.Fatalf("DecodeWithUnknown = %v, want a case variant refused", err)
	}

	if err := strictjson.Decode([]byte(in), &v); err == nil {
		t.Fatal("Decode accepted unknown fields")
	}
}

type Embedded struct {
	Shared string `json:"shared"`
}

type outer struct {
	Embedded

	Plain string
	_     int
}

func TestDecodeFollowsTheStandardFieldNames(t *testing.T) {
	t.Parallel()

	var v outer

	err := strictjson.Decode([]byte(`{"shared":"s","Plain":"p"}`), &v)
	if err != nil || v.Shared != "s" || v.Plain != "p" {
		t.Fatalf("Decode = %+v, %v", v, err)
	}

	for _, in := range []string{`{"SHARED":"s"}`, `{"plain":"p"}`} {
		if err := strictjson.Decode([]byte(in), &v); err == nil || !strings.Contains(err.Error(), "only in case") {
			t.Errorf("Decode(%s) = %v, want a case variant refused", in, err)
		}
	}
}

type node struct {
	Name     string  `json:"name"`
	Children []*node `json:"children"`
}

func TestDecodeHandlesARecursiveType(t *testing.T) {
	t.Parallel()

	var v node

	err := strictjson.Decode([]byte(`{"name":"a","children":[{"name":"b","children":[]}]}`), &v)
	if err != nil || len(v.Children) != 1 || v.Children[0].Name != "b" {
		t.Fatalf("Decode = %+v, %v", v, err)
	}

	err = strictjson.Decode([]byte(`{"name":"a","children":[{"NAME":"b"}]}`), &v)
	if err == nil || !strings.Contains(err.Error(), "only in case") {
		t.Fatalf("Decode = %v, want a nested case variant refused", err)
	}
}

func TestDecodeMessagesAreSafe(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("1", 500)

	decode := func(in string) error {
		var v doc

		return strictjson.Decode([]byte(in), &v)
	}

	withUnknown := func(in string) error {
		var v doc

		_, err := strictjson.DecodeWithUnknown([]byte(in), &v)

		return err
	}

	for _, tc := range []struct {
		in     string
		decode func(string) error
		phrase string
	}{
		{`{"count":1` + long + `}`, decode, "does not decode"},
		{`{"name":` + long + `}`, decode, "does not decode"},
		{"{\"name\":\"a\",\"x\\u001b\\n" + long + "\":1}", decode, "unknown field"},
		{"{\"COUNT\\u001b\":1}", decode, "unknown field"},
		// The standard decoder accepts these, so the message comes from
		// the walker's own quoting.
		{`{"labels":{"\u001b[2J":"a","\u001b[2J":"b"}}`, decode, "repeats"},
		{`{"\u001b[2J":1,"\u001b[2J":2}`, withUnknown, "repeats"},
		{`{"labels":{"x\u001b\n` + long + `":"a","x\u001b\n` + long + `":"b"}}`, decode, "repeats"},
		{`{"x\u001b\n` + long + `":1,"x\u001b\n` + long + `":2}`, withUnknown, "repeats"},
	} {
		err := tc.decode(tc.in)
		if err == nil {
			t.Fatalf("%q was accepted", tc.in)
		}

		msg := err.Error()
		if !strings.Contains(msg, tc.phrase) {
			t.Errorf("message %q does not hold %q", msg, tc.phrase)
		}

		if strings.ContainsAny(msg, "\x1b\n\r") || !utf8.ValidString(msg) {
			t.Errorf("message %q holds raw control text", msg)
		}

		if strings.Contains(msg, strings.Repeat("1", 100)) {
			t.Errorf("message %q echoes input without a bound", msg)
		}
	}
}

func TestDecodeChecksMapElements(t *testing.T) {
	t.Parallel()

	var v struct {
		M map[string]inner `json:"m"`
	}

	err := strictjson.Decode([]byte(`{"m":{"k":{"ID":"x"}}}`), &v)
	if err == nil || !strings.Contains(err.Error(), "only in case") {
		t.Fatalf("Decode = %v, want a case variant inside a map element refused", err)
	}
}

// custom decodes itself and takes any object.
type custom struct {
	Name int `json:"name"`
}

func (c *custom) UnmarshalJSON([]byte) error {
	c.Name = 1

	return nil
}

func TestDecodeLeavesSelfDecodingTypesAlone(t *testing.T) {
	t.Parallel()

	var v struct {
		C custom `json:"c"`
	}

	// The keys differ, so only a check against custom's own field names
	// could refuse them, and custom decides for itself what it reads.
	err := strictjson.Decode([]byte(`{"c":{"NAME":1,"name":2}}`), &v)
	if err != nil || v.C.Name != 1 {
		t.Fatalf("Decode = %+v, %v; want the value handed to UnmarshalJSON", v, err)
	}
}

type withSecret struct {
	A      int `json:"a"`
	secret int
}

func TestDecodeIgnoresUnexportedFields(t *testing.T) {
	t.Parallel()

	var v withSecret

	unknown, err := strictjson.DecodeWithUnknown([]byte(`{"a":1,"secret":2}`), &v)
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(unknown, []string{"secret"}) || v.A != 1 || v.secret != 0 {
		t.Fatalf("unknown = %q, value %+v; want secret unknown and left unset", unknown, v)
	}
}

func FuzzDecode(f *testing.F) {
	f.Add([]byte(good))
	f.Add([]byte(`{"name":"a","name":"b"}`))
	f.Add([]byte(`{"NAME":"a"}`))
	f.Add([]byte("{\"name\":\"\xff\"}"))
	f.Add([]byte(`{"inner":{"id":"x"}} {}`))

	f.Fuzz(func(t *testing.T, in []byte) {
		var strict doc
		if err := strictjson.Decode(in, &strict); err != nil {
			return
		}

		// Whatever Decode accepts, the standard decoder reads the same way,
		// and it is valid UTF-8 with no unknown field.
		if !utf8.Valid(in) {
			t.Fatalf("Decode accepted invalid UTF-8 %q", in)
		}

		var std doc

		dec := json.NewDecoder(strings.NewReader(string(in)))
		dec.DisallowUnknownFields()

		if err := dec.Decode(&std); err != nil {
			t.Fatalf("Decode accepted %q, which the standard decoder refuses: %v", in, err)
		}

		if !reflect.DeepEqual(strict, std) {
			t.Fatalf("Decode read %+v, the standard decoder %+v", strict, std)
		}
	})
}
