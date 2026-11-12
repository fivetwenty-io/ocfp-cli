package ownerrecord_test

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fivetwenty-io/ocfp-cli/internal/ownerrecord"
	"github.com/fivetwenty-io/ocfp-cli/internal/ownerrecord/ownerrecordtest"
	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
	"github.com/fivetwenty-io/ocfp-cli/internal/strictjson"
)

func liveRecord() ownerrecord.Record {
	return ownerrecord.Record{
		InstallationID:  "inst-fixture-01",
		OwnerInstanceID: "instance-fixture-a",
		OwnerPublicKey:  ownerrecordtest.TestKey().Public().(ed25519.PublicKey),
		Epoch:           3,
		Mode:            ownerrecord.ModeLive,
		Generation:      7,
		HeadPosition:    1042,
		HeadHash:        "sha256:" + strings.Repeat("ab", 32),
		SignerID:        "instance-fixture-a",
		SignedAt:        time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC),
	}
}

func testSign(msg []byte) ([]byte, error) {
	return ed25519.Sign(ownerrecordtest.TestKey(), msg), nil
}

func seal(t *testing.T, r ownerrecord.Record) ownerrecord.Signed {
	t.Helper()

	sf, err := ownerrecord.Seal(r, testSign)
	if err != nil {
		t.Fatal(err)
	}

	return sf
}

// resign signs payload with the test key under the record's context.
func resign(payload []byte) ownerrecord.Signed {
	return ownerrecord.Signed{
		Format:    ownerrecord.FormatV1,
		Payload:   payload,
		Signature: ed25519.Sign(ownerrecordtest.TestKey(), ownerrecord.SigningBytes(payload)),
	}
}

func wantRefusal(t *testing.T, err error, code progress.Code) *progress.Refusal {
	t.Helper()

	if err == nil {
		t.Fatalf("got no error, want a %s refusal", code)
	}

	r, ok := progress.AsRefusal(err)
	if !ok {
		t.Fatalf("error %v is not a *progress.Refusal", err)
	}

	if r.Code != code {
		t.Fatalf("refusal code = %s, want %s (%v)", r.Code, code, err)
	}

	return r
}

func TestSealOpenRoundTrip(t *testing.T) {
	t.Parallel()

	r := liveRecord()
	r.Relocation = &ownerrecord.Relocation{
		ID:               "reloc-fixture-01",
		TargetInstanceID: "instance-fixture-b",
		CutOverPosition:  ptr(int64(1040)),
		CutOverHash:      "sha256:" + strings.Repeat("cd", 32),
	}

	sf := seal(t, r)

	if sf.Format != ownerrecord.FormatV1 {
		t.Errorf("format = %d, want %d", sf.Format, ownerrecord.FormatV1)
	}

	got, err := ownerrecord.Open(sf)
	if err != nil {
		t.Fatal(err)
	}

	if !recordsEqual(got, r) {
		t.Errorf("opened record differs\ngot:  %+v\nwant: %+v", got, r)
	}
}

func ptr[T any](v T) *T { return &v }

func recordsEqual(a, b ownerrecord.Record) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)

	return errA == nil && errB == nil && bytes.Equal(ja, jb) && a.SignedAt.Equal(b.SignedAt)
}

func TestOpenRefusesBadSignature(t *testing.T) {
	t.Parallel()

	sf := seal(t, liveRecord())

	for i := range sf.Payload {
		tampered := sf
		tampered.Payload = bytes.Clone(sf.Payload)
		tampered.Payload[i] ^= 0x01

		_, err := ownerrecord.Open(tampered)
		if err == nil {
			t.Fatalf("payload byte %d flipped: Open accepted the record", i)
		}

		r, ok := progress.AsRefusal(err)
		if !ok {
			t.Fatalf("payload byte %d flipped: error %v is not a *progress.Refusal", i, err)
		}

		// A flip that breaks the JSON or the key's encoding is refused as
		// invalid before any signature check. Every other flip must fail the
		// signature check.
		if r.Code != progress.CodeRequestUnsigned && r.Code != progress.CodeJobRequestInvalid {
			t.Fatalf("payload byte %d flipped: code = %s", i, r.Code)
		}
	}

	// Flip a byte inside a value that stays valid JSON, so only the signature
	// can catch it.
	idx := bytes.Index(sf.Payload, []byte(`"inst-fixture-01"`))
	if idx < 0 {
		t.Fatal("payload does not hold the installation id")
	}

	tampered := sf
	tampered.Payload = bytes.Clone(sf.Payload)
	tampered.Payload[idx+1] = 'j'

	_, err := ownerrecord.Open(tampered)
	wantRefusal(t, err, progress.CodeRequestUnsigned)

	badSig := sf
	badSig.Signature = bytes.Clone(sf.Signature)
	badSig.Signature[0] ^= 0x80

	_, err = ownerrecord.Open(badSig)
	wantRefusal(t, err, progress.CodeRequestUnsigned)
}

func TestOpenRefusesUnknownFormat(t *testing.T) {
	t.Parallel()

	for _, format := range []int{0, 2, -1} {
		sf := seal(t, liveRecord())
		sf.Format = format

		_, err := ownerrecord.Open(sf)
		r := wantRefusal(t, err, progress.CodeJobRequestInvalid)

		if !strings.Contains(r.Message, "format") || !strings.Contains(r.Message, itoa(format)) {
			t.Errorf("format %d: message %q does not name the format", format, r.Message)
		}
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)

	return string(b)
}

func TestOpenKeepsNewerFields(t *testing.T) {
	t.Parallel()

	sf, err := ownerrecord.Parse(ownerrecordtest.Fixture("record-newer-fields-v1.json"))
	if err != nil {
		t.Fatal(err)
	}

	before := bytes.Clone(sf.Payload)

	got, err := ownerrecord.Open(sf)
	if err != nil {
		t.Fatalf("a record with a newer field did not open: %v", err)
	}

	if got.InstallationID != "inst-fixture-01" || got.Mode != ownerrecord.ModeLive {
		t.Errorf("opened record = %+v", got)
	}

	if !bytes.Equal(sf.Payload, before) {
		t.Error("Open changed the payload bytes")
	}

	// The checks below test the fixture, not Open. They make sure the
	// fixture really holds a field that Record cannot carry, so that a
	// re-encoding of the opened record would differ from the signed bytes and
	// the signature check above could only have passed over the bytes as
	// received.
	if !bytes.Contains(sf.Payload, []byte(`"futureField"`)) {
		t.Fatal("fixture check: the payload does not hold the newer field")
	}

	again, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}

	if bytes.Equal(again, sf.Payload) {
		t.Error("fixture check: a re-encoding of the opened record equals the payload")
	}
}

func TestSigningBytesHaveContext(t *testing.T) {
	t.Parallel()

	p := []byte(`{"installationId":"inst-fixture-01"}`)

	got := ownerrecord.SigningBytes(p)
	want := append([]byte("ocfp-owner-record-v1\n"), p...)

	if !bytes.Equal(got, want) {
		t.Errorf("SigningBytes = %q, want %q", got, want)
	}

	if ownerrecord.SigningContext != "ocfp-owner-record-v1\n" {
		t.Errorf("SigningContext = %q", ownerrecord.SigningContext)
	}

	// The result must not alias the payload, so a caller that changes one
	// never changes the other.
	got[len(got)-1] = 'X'
	if p[len(p)-1] == 'X' {
		t.Error("SigningBytes aliases its argument")
	}
}

func TestOpenRefusesSignatureWithoutContext(t *testing.T) {
	t.Parallel()

	sf := seal(t, liveRecord())

	cases := map[string][]byte{
		"bare payload":    sf.Payload,
		"release context": append([]byte("ocfp-owner-release-v1\n"), sf.Payload...),
		"other context":   append([]byte("ocfp-job-request-v1\n"), sf.Payload...),
	}

	for name, msg := range cases {
		forged := sf
		forged.Signature = ed25519.Sign(ownerrecordtest.TestKey(), msg)

		_, err := ownerrecord.Open(forged)
		if r, ok := progress.AsRefusal(err); !ok || r.Code != progress.CodeRequestUnsigned {
			t.Errorf("%s: err = %v, want a REQUEST_UNSIGNED refusal", name, err)
		}
	}
}

func TestOpenRefusesOtherKey(t *testing.T) {
	t.Parallel()

	other := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x42}, ed25519.SeedSize))
	sf := seal(t, liveRecord())
	sf.Signature = ed25519.Sign(other, ownerrecord.SigningBytes(sf.Payload))

	_, err := ownerrecord.Open(sf)
	wantRefusal(t, err, progress.CodeRequestUnsigned)
}

func TestOpenRefusesBadSignatureLength(t *testing.T) {
	t.Parallel()

	sf := seal(t, liveRecord())

	for _, sig := range [][]byte{nil, {}, sf.Signature[:63], append(bytes.Clone(sf.Signature), 0)} {
		bad := sf
		bad.Signature = sig

		_, err := ownerrecord.Open(bad)
		wantRefusal(t, err, progress.CodeRequestUnsigned)
	}
}

func TestOpenRefusesMalformedPayload(t *testing.T) {
	t.Parallel()

	good := seal(t, liveRecord())
	key := ownerrecordtest.TestKey().Public().(ed25519.PublicKey)

	withKey := func(k []byte) []byte {
		r := liveRecord()
		r.OwnerPublicKey = k

		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}

		return b
	}

	cases := map[string][]byte{
		"empty":              {},
		"null":               []byte("null"),
		"array":              []byte("[]"),
		"string":             []byte(`"x"`),
		"truncated":          good.Payload[:len(good.Payload)/2],
		"trailing object":    append(bytes.Clone(good.Payload), []byte(`{}`)...),
		"trailing garbage":   append(bytes.Clone(good.Payload), 'x'),
		"no key":             withKey(nil),
		"short key":          withKey(key[:31]),
		"long key":           withKey(append(bytes.Clone(key), 0)),
		"key not base64":     bytes.Replace(good.Payload, []byte(`"ownerPublicKey":"`), []byte(`"ownerPublicKey":"!!`), 1),
		"key wrong type":     bytes.Replace(good.Payload, []byte(`"ownerPublicKey":"`), []byte(`"ownerPublicKey":7,"x":"`), 1),
		"epoch wrong type":   bytes.Replace(good.Payload, []byte(`"epoch":3`), []byte(`"epoch":"3"`), 1),
		"negative epoch":     bytes.Replace(good.Payload, []byte(`"epoch":3`), []byte(`"epoch":-3`), 1),
		"bad signed time":    bytes.Replace(good.Payload, []byte(`"signedAt":"2026`), []byte(`"signedAt":"x2026`), 1),
		"relocation as list": bytes.Replace(good.Payload, []byte(`"headHash"`), []byte(`"relocation":[],"headHash"`), 1),
	}

	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := ownerrecord.Open(resign(payload))

			// Every case is signed by the right key over its own bytes, so
			// each one must be refused for its content, never for its
			// signature.
			wantRefusal(t, err, progress.CodeJobRequestInvalid)
		})
	}
}

func TestOpenRefusesInvalidRecord(t *testing.T) {
	t.Parallel()

	cases := map[string]func(*ownerrecord.Record){
		"unknown mode":        func(r *ownerrecord.Record) { r.Mode = "paused" },
		"empty mode":          func(r *ownerrecord.Record) { r.Mode = "" },
		"no installation":     func(r *ownerrecord.Record) { r.InstallationID = "" },
		"no owner instance":   func(r *ownerrecord.Record) { r.OwnerInstanceID = "" },
		"no signer":           func(r *ownerrecord.Record) { r.SignerID = "" },
		"no signed time":      func(r *ownerrecord.Record) { r.SignedAt = time.Time{} },
		"handover without id": func(r *ownerrecord.Record) { r.Mode = ownerrecord.ModeHandover },
		"handover empty id": func(r *ownerrecord.Record) {
			r.Mode, r.Relocation = ownerrecord.ModeHandover, &ownerrecord.Relocation{}
		},
		"relocation without id": func(r *ownerrecord.Record) {
			r.Relocation = &ownerrecord.Relocation{TargetInstanceID: "instance-fixture-b"}
		},
		"negative head position": func(r *ownerrecord.Record) { r.HeadPosition = -1 },
		"cut-over hash without position": func(r *ownerrecord.Record) {
			r.Relocation = &ownerrecord.Relocation{ID: "reloc-fixture-01", CutOverHash: "sha256:" + strings.Repeat("cd", 32)}
		},
		"cut-over position without hash": func(r *ownerrecord.Record) {
			r.Relocation = &ownerrecord.Relocation{ID: "reloc-fixture-01", CutOverPosition: ptr(int64(5))}
		},
		"negative cut-over position": func(r *ownerrecord.Record) {
			r.Relocation = &ownerrecord.Relocation{
				ID: "reloc-fixture-01", CutOverPosition: ptr(int64(-1)), CutOverHash: "sha256:" + strings.Repeat("cd", 32),
			}
		},
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			r := liveRecord()
			mutate(&r)

			if _, err := ownerrecord.Seal(r, testSign); err == nil {
				t.Error("Seal accepted a record that Open must refuse")
			}

			payload, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}

			_, err = ownerrecord.Open(resign(payload))
			wantRefusal(t, err, progress.CodeJobRequestInvalid)
		})
	}
}

func TestSealIsDeterministic(t *testing.T) {
	t.Parallel()

	r := liveRecord()
	a := seal(t, r)
	b := seal(t, r)

	if !bytes.Equal(a.Payload, b.Payload) || !bytes.Equal(a.Signature, b.Signature) {
		t.Error("two seals of the same record differ")
	}

	// The same instant in another zone seals to the same bytes.
	zoned := r
	zoned.SignedAt = r.SignedAt.In(time.FixedZone("fixture", -5*60*60))

	if c := seal(t, zoned); !bytes.Equal(a.Payload, c.Payload) {
		t.Errorf("a zoned time changed the payload\n%s\n%s", a.Payload, c.Payload)
	}

	// A time read from the wall clock carries a monotonic reading, and it
	// seals to the same bytes as the same instant without one.
	now := time.Now()
	if now.String() == now.Round(0).String() {
		t.Fatal("time.Now carries no monotonic reading on this platform")
	}

	withMono, withoutMono := r, r
	withMono.SignedAt, withoutMono.SignedAt = now, now.Round(0)

	if m, w := seal(t, withMono), seal(t, withoutMono); !bytes.Equal(m.Payload, w.Payload) {
		t.Errorf("a monotonic reading changed the payload\n%s\n%s", m.Payload, w.Payload)
	}
}

func TestSealRefusesBadSigner(t *testing.T) {
	t.Parallel()

	failing := func([]byte) ([]byte, error) { return nil, errors.New("signer offline") }
	if _, err := ownerrecord.Seal(liveRecord(), failing); err == nil || !strings.Contains(err.Error(), "signer offline") {
		t.Errorf("err = %v, want the signer's error", err)
	}

	if _, err := ownerrecord.Seal(liveRecord(), nil); err == nil {
		t.Error("Seal accepted a nil signer")
	}

	short := func([]byte) ([]byte, error) { return make([]byte, 10), nil }
	if _, err := ownerrecord.Seal(liveRecord(), short); err == nil {
		t.Error("Seal accepted a short signature")
	}

	// A signer whose key is not the record's owner key yields a record that
	// Open would refuse, so Seal refuses it too.
	other := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x42}, ed25519.SeedSize))
	wrongKey := func(msg []byte) ([]byte, error) { return ed25519.Sign(other, msg), nil }

	if _, err := ownerrecord.Seal(liveRecord(), wrongKey); err == nil {
		t.Error("Seal accepted a signature by a key other than the owner key")
	}
}

func TestSealDoesNotKeepSignerBuffer(t *testing.T) {
	t.Parallel()

	var seen []byte

	sign := func(msg []byte) ([]byte, error) {
		seen = msg

		return testSign(msg)
	}

	sf, err := ownerrecord.Seal(liveRecord(), sign)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(seen, ownerrecord.SigningBytes(sf.Payload)) {
		t.Error("the signer was not handed SigningBytes(payload)")
	}

	// Scribbling over the signer's message must not reach the sealed form.
	for i := range seen {
		seen[i] = 0
	}

	if _, err := ownerrecord.Open(sf); err != nil {
		t.Errorf("the sealed form shares memory with the signer's message: %v", err)
	}
}

func TestParse(t *testing.T) {
	t.Parallel()

	sf := seal(t, liveRecord())

	doc, err := json.Marshal(sf)
	if err != nil {
		t.Fatal(err)
	}

	got, err := ownerrecord.Parse(append(bytes.Clone(doc), '\n'))
	if err != nil {
		t.Fatal(err)
	}

	if got.Format != sf.Format || !bytes.Equal(got.Payload, sf.Payload) || !bytes.Equal(got.Signature, sf.Signature) {
		t.Error("Parse did not return the encoded form")
	}

	bad := map[string][]byte{
		"empty":          {},
		"whitespace":     []byte(" \n"),
		"null":           []byte("null"),
		"truncated":      doc[:len(doc)-3],
		"trailing data":  append(bytes.Clone(doc), []byte(`{}`)...),
		"trailing junk":  append(bytes.Clone(doc), 'x'),
		"unknown field":  bytes.Replace(doc, []byte(`{"format"`), []byte(`{"extra":1,"format"`), 1),
		"no format":      bytes.Replace(doc, []byte(`"format":1,`), nil, 1),
		"string format":  bytes.Replace(doc, []byte(`"format":1`), []byte(`"format":"1"`), 1),
		"payload number": bytes.Replace(doc, []byte(`"payload":"`), []byte(`"payload":5,"x":"`), 1),
		"array":          []byte("[]"),
	}

	for name, in := range bad {
		if _, err := ownerrecord.Parse(in); err == nil {
			t.Errorf("%s: Parse accepted %q", name, in)
		} else if _, ok := progress.AsRefusal(err); !ok {
			t.Errorf("%s: error %v is not a *progress.Refusal", name, err)
		}
	}
}

func TestFixturesOpen(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		mode ownerrecord.Mode
		code progress.Code
	}{
		{"record-v1.json", ownerrecord.ModeLive, ""},
		{"record-newer-fields-v1.json", ownerrecord.ModeLive, ""},
		{"record-handover-v1.json", ownerrecord.ModeHandover, ""},
		{"record-bad-signature-v1.json", "", progress.CodeRequestUnsigned},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sf, err := ownerrecord.Parse(ownerrecordtest.Fixture(tc.name))
			if err != nil {
				t.Fatal(err)
			}

			got, err := ownerrecord.Open(sf)
			if tc.code != "" {
				wantRefusal(t, err, tc.code)

				return
			}

			if err != nil {
				t.Fatal(err)
			}

			if got.Mode != tc.mode {
				t.Errorf("mode = %s, want %s", got.Mode, tc.mode)
			}
		})
	}
}

func FuzzOpen(f *testing.F) {
	for _, name := range []string{"record-v1.json", "record-newer-fields-v1.json", "record-handover-v1.json", "record-bad-signature-v1.json"} {
		f.Add(ownerrecordtest.Fixture(name))
	}

	f.Add([]byte(`{"format":1,"payload":"","signature":""}`))

	f.Fuzz(func(t *testing.T, in []byte) {
		sf, err := ownerrecord.Parse(in)
		if err != nil {
			if _, ok := progress.AsRefusal(err); !ok {
				t.Fatalf("Parse error %v is not a refusal", err)
			}

			return
		}

		if _, err := ownerrecord.Open(sf); err != nil {
			if _, ok := progress.AsRefusal(err); !ok {
				t.Fatalf("Open error %v is not a refusal", err)
			}
		}
	})
}

// TestOwnerRecordUsesTheSharedHelper replays the fixtures and payloads
// built from them through Parse, Open, and HandoverRefusal. The was column
// holds the code each row got from the decoder this package carried before
// strictjson, and want holds the code it gets now. The two differ only where
// the shared helper refuses more, and an empty code means the row opens.
func TestOwnerRecordUsesTheSharedHelper(t *testing.T) {
	t.Parallel()

	const opens progress.Code = ""

	other := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x42}, ed25519.SeedSize))

	good := seal(t, liveRecord()).Payload

	handover := liveRecord()
	handover.Mode = ownerrecord.ModeHandover
	handover.Relocation = &ownerrecord.Relocation{ID: "reloc-fixture-01", TargetInstanceID: "instance-fixture-b"}
	handoverPayload, err := json.Marshal(handover)
	if err != nil {
		t.Fatal(err)
	}

	replace := func(payload []byte, old, repl string) []byte {
		t.Helper()

		if bytes.Count(payload, []byte(old)) != 1 {
			t.Fatalf("%q is not in %s exactly once", old, payload)
		}

		return bytes.Replace(payload, []byte(old), []byte(repl), 1)
	}

	t.Run("fixtures", func(t *testing.T) {
		t.Parallel()

		for name, want := range map[string]progress.Code{
			"record-v1.json":               opens,
			"record-newer-fields-v1.json":  opens,
			"record-handover-v1.json":      opens,
			"record-bad-signature-v1.json": progress.CodeRequestUnsigned,
		} {
			sf, err := ownerrecord.Parse(ownerrecordtest.Fixture(name))
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}

			_, err = ownerrecord.Open(sf)
			wantOpenCode(t, name, err, want)
		}
	})

	for _, tc := range []struct {
		name      string
		payload   []byte
		key       ed25519.PrivateKey
		was, want progress.Code
	}{
		{"invalid UTF-8 in the installation id", replace(good, `"inst-fixture-01"`, "\"inst-\xff-01\""), nil, opens, progress.CodeJobRequestInvalid},
		{"invalid UTF-8 in a newer field", replace(good, `{"installationId"`, "{\"futureField\":\"\xff\",\"installationId\""), nil, opens, progress.CodeJobRequestInvalid},
		{"invalid UTF-8 in a newer key", replace(good, `{"installationId"`, "{\"\xff\":1,\"installationId\""), nil, opens, progress.CodeJobRequestInvalid},
		// A lone surrogate is valid UTF-8 and still decodes to U+FFFD. The
		// shared helper does not refuse it, so the row still opens.
		{"lone surrogate in the installation id", replace(good, `"inst-fixture-01"`, `"inst-\ud800-01"`), nil, opens, opens},
		{"case variant inside a newer field", replace(good, `{"installationId"`, `{"futureField":{"MODE":1},"installationId"`), nil, opens, opens},
		{"case variant in the relocation", replace(handoverPayload, `{"id"`, `{"ID":"x","id"`), nil, progress.CodeJobRequestInvalid, progress.CodeJobRequestInvalid},
		{"repeated key in a newer field", replace(good, `{"installationId"`, `{"futureField":[{"a":1,"a":2}],"installationId"`), nil, progress.CodeJobRequestInvalid, progress.CodeJobRequestInvalid},
		{"trailing data", append(bytes.Clone(good), 'x'), nil, progress.CodeJobRequestInvalid, progress.CodeJobRequestInvalid},
		{"newer field under another key", replace(good, `{"installationId"`, `{"futureField":1,"installationId"`), other, progress.CodeRequestUnsigned, progress.CodeRequestUnsigned},
		{"repeated key under another key", replace(good, `{"installationId"`, `{"mode":"live","installationId"`), other, progress.CodeJobRequestInvalid, progress.CodeJobRequestInvalid},
		// The UTF-8 check now runs before the payload is decoded, and the
		// payload is decoded before its signature is checked.
		{"invalid UTF-8 under another key", replace(good, `"inst-fixture-01"`, "\"inst-\xff-01\""), other, progress.CodeRequestUnsigned, progress.CodeJobRequestInvalid},
		// HandoverRefusal answers a hand-over that its new owner did not
		// sign. It decodes with the same helper as Open.
		{"handover under another key", handoverPayload, other, progress.CodeOwnerHandover, progress.CodeOwnerHandover},
		{"handover with invalid UTF-8 under another key", replace(handoverPayload, `"inst-fixture-01"`, "\"inst-\xff-01\""), other, progress.CodeOwnerHandover, progress.CodeJobRequestInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			key := ownerrecordtest.TestKey()
			if tc.key != nil {
				key = tc.key
			}

			sf := ownerrecord.Signed{
				Format:    ownerrecord.FormatV1,
				Payload:   tc.payload,
				Signature: ed25519.Sign(key, ownerrecord.SigningBytes(tc.payload)),
			}

			_, err := ownerrecord.Open(sf)
			wantOpenCode(t, "Open", ownerrecord.HandoverRefusal(sf, err), tc.want)

			// A payload that the shared helper refuses is refused before
			// its signature is checked.
			if _, herr := strictjson.DecodeWithUnknown(tc.payload, new(ownerrecord.Record)); herr != nil {
				wantOpenCode(t, "Open", err, progress.CodeJobRequestInvalid)
			}
		})
	}

	doc, err := json.Marshal(seal(t, liveRecord()))
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name      string
		doc       []byte
		was, want progress.Code
	}{
		{"signed form", doc, opens, opens},
		{"unknown field", replace(doc, `{"format"`, `{"extra":1,"format"`), progress.CodeJobRequestInvalid, progress.CodeJobRequestInvalid},
		{"invalid UTF-8 in an unknown key", replace(doc, `{"format"`, "{\"\xff\":1,\"format\""), progress.CodeJobRequestInvalid, progress.CodeJobRequestInvalid},
		{"repeated format", replace(doc, `{"format"`, `{"format":2,"format"`), opens, progress.CodeJobRequestInvalid},
		{"repeated payload", replace(doc, `{"format"`, `{"payload":"","format"`), opens, progress.CodeJobRequestInvalid},
		{"format in another case", replace(doc, `{"format"`, `{"Format"`), opens, progress.CodeJobRequestInvalid},
	} {
		t.Run("parse "+tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := ownerrecord.Parse(tc.doc)
			wantOpenCode(t, "Parse", err, tc.want)

			if herr := strictjson.Decode(tc.doc, new(ownerrecord.Signed)); herr != nil {
				wantOpenCode(t, "Parse", err, progress.CodeJobRequestInvalid)
			}
		})
	}
}

// wantOpenCode checks that err is nil when code is empty, and otherwise that
// it is a refusal with code.
func wantOpenCode(t *testing.T, what string, err error, code progress.Code) {
	t.Helper()

	if code == "" {
		if err != nil {
			t.Fatalf("%s: got %v, want no error", what, err)
		}

		return
	}

	r, ok := progress.AsRefusal(err)
	if !ok || r.Code != code {
		t.Fatalf("%s: got %v, want a %s refusal", what, err, code)
	}
}
