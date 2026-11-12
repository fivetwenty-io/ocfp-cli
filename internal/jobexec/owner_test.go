package jobexec

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fivetwenty-io/ocfp-cli/internal/ownerrecord"
	"github.com/fivetwenty-io/ocfp-cli/internal/ownerrecord/ownerrecordtest"
	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
)

// testNow is the time the test clock reads.
var testNow = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

func ownerKey() ed25519.PrivateKey { return ownerrecordtest.TestKey() }

// otherKey is the key of an instance that does not own the bloc. Its seed is
// the SHA-256 digest of a fixed text, so it signs nothing real.
func otherKey() ed25519.PrivateKey {
	seed := sha256.Sum256([]byte("jobexec test vector: another instance, signs nothing real"))

	return ed25519.NewKeyFromSeed(seed[:])
}

func liveRecord() ownerrecord.Record {
	return ownerrecord.Record{
		InstallationID:  "inst-fixture-01",
		Org:             "org-fixture",
		Bloc:            "bloc-fixture",
		OwnerInstanceID: "instance-fixture-a",
		OwnerPublicKey:  ownerKey().Public().(ed25519.PublicKey),
		Epoch:           3,
		Mode:            ownerrecord.ModeLive,
		Generation:      7,
		HeadPosition:    1042,
		HeadHash:        "sha256:" + strings.Repeat("ab", 32),
		SignerID:        "instance-fixture-a",
		SignedAt:        time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC),
	}
}

func sealRecord(t testing.TB, r ownerrecord.Record) ownerrecord.Signed {
	t.Helper()

	sf, err := ownerrecord.Seal(r, func(msg []byte) ([]byte, error) {
		return ed25519.Sign(ownerKey(), msg), nil
	})
	if err != nil {
		t.Fatal(err)
	}

	return sf
}

func goodRequest() Request {
	return Request{
		WireContract:   progress.WireContract,
		JobID:          "job-0001",
		Verb:           "noop",
		InstallationID: "inst-fixture-01",
		InstanceID:     "instance-fixture-a",
		Epoch:          3,
		Org:            "org-fixture",
		Bloc:           "bloc-fixture",
		Env:            map[string]string{"LANG": "C"},
		IssuedAt:       testNow.Add(-time.Minute),
	}
}

func mustMarshal(t testing.TB, v any) []byte {
	t.Helper()

	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}

	return b
}

// signPayload signs payload with key under the job request context.
func signPayload(payload []byte, key ed25519.PrivateKey) SignedRequest {
	return SignedRequest{
		Format:    RequestFormatV1,
		Payload:   payload,
		Signature: ed25519.Sign(key, RequestSigningBytes(payload)),
	}
}

// emptyStore is a store whose directory exists and holds nothing.
func emptyStore(t *testing.T) (*ownerrecord.DirStore, string) {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "owner")

	err := os.Mkdir(dir, 0o750)
	if err != nil {
		t.Fatal(err)
	}

	return ownerrecord.NewDirStore(dir), dir
}

// storeWith is a store that holds r, signed by its owner.
func storeWith(t *testing.T, r ownerrecord.Record) *ownerrecord.DirStore {
	t.Helper()

	s := ownerrecord.NewDirStore(filepath.Join(t.TempDir(), "owner"))

	err := s.Replace(t.Context(), nil, sealRecord(t, r))
	if err != nil {
		t.Fatal(err)
	}

	return s
}

// scenario is everything CheckOwner sees.
type scenario struct {
	record  *ownerrecord.Record // nil means an empty owner directory
	request Request
	key     ed25519.PrivateKey
	noSig   bool
	format  int
	now     time.Time

	// extraKey, when set, is added to the payload as a key that names no
	// field.
	extraKey string
}

func baseScenario() *scenario {
	r := liveRecord()

	return &scenario{record: &r, request: goodRequest(), key: ownerKey(), format: RequestFormatV1, now: testNow}
}

func (s *scenario) store(t *testing.T) *ownerrecord.DirStore {
	t.Helper()

	if s.record == nil {
		st, _ := emptyStore(t)

		return st
	}

	return storeWith(t, *s.record)
}

func (s *scenario) signed(t *testing.T) SignedRequest {
	t.Helper()

	payload := mustMarshal(t, s.request)
	if s.extraKey != "" {
		payload = append(append([]byte(`{`), mustMarshal(t, s.extraKey)...), append([]byte(`:1,`), payload[1:]...)...)
	}

	sr := signPayload(payload, s.key)
	sr.Format = s.format

	if s.noSig {
		sr.Signature = nil
	}

	return sr
}

func (s *scenario) check(t *testing.T) (*ownerrecord.DirStore, SignedRequest, Request, func() error, error) {
	t.Helper()

	st := s.store(t)
	sr := s.signed(t)
	req, release, err := checkOwner(t.Context(), sr, st, fixedClock{s.now})

	return st, sr, req, release, err
}

func handover(r *ownerrecord.Record) {
	r.Mode = ownerrecord.ModeHandover
	r.Relocation = &ownerrecord.Relocation{ID: "reloc-01", TargetInstanceID: "instance-fixture-b"}
}

// fault is one row of the owner check's table: a change to a good scenario,
// the code it must draw, and a phrase that only its message holds.
type fault struct {
	name   string
	code   progress.Code
	phrase string
	apply  func(*scenario)
}

// faults returns the table's rows in the order CheckOwner checks them.
func faults() []fault {
	return []fault{
		{"no record", progress.CodeOwnerRecordMissing, "no owner record", func(s *scenario) { s.record = nil }},
		{"bad signature", progress.CodeRequestUnsigned, "signature", func(s *scenario) { s.key = otherKey() }},
		{"empty signature", progress.CodeRequestUnsigned, "signature", func(s *scenario) { s.noSig = true }},
		{"other installation", progress.CodeOwnerMismatch, "names installation", func(s *scenario) {
			s.request.InstallationID = "inst-fixture-02"
		}},
		{"other instance", progress.CodeOwnerMismatch, "names instance", func(s *scenario) {
			s.request.InstanceID = "instance-fixture-b"
		}},
		{"other epoch", progress.CodeOwnerMismatch, "names epoch", func(s *scenario) { s.request.Epoch = 4 }},
		{"handover", progress.CodeOwnerHandover, "handover", func(s *scenario) {
			if s.record != nil {
				handover(s.record)
			}
		}},
		{"newer contract", progress.CodeWireContractUnsupported, "wire contract", func(s *scenario) {
			s.request.WireContract = progress.WireContract + 1
		}},
		{"older contract", progress.CodeWireContractUnsupported, "names no org or bloc", func(s *scenario) {
			s.request.WireContract = progress.WireContract - 1
		}},
		{"other org", progress.CodeTenantMismatch, "names org", func(s *scenario) { s.request.Org = "org-other" }},
		{"other bloc", progress.CodeTenantMismatch, "names bloc", func(s *scenario) { s.request.Bloc = "bloc-other" }},
		{"stale request", progress.CodeJobRequestInvalid, "issued", func(s *scenario) {
			s.request.IssuedAt = s.now.Add(-11 * time.Minute)
		}},
		{"bad job id", progress.CodeJobRequestInvalid, "job id", func(s *scenario) { s.request.JobID = "../x" }},
		{"unknown format", progress.CodeJobRequestInvalid, "format", func(s *scenario) { s.format = 2 }},
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

// exclusiveAttempt is a Replace that takes the exclusive lock and then gives
// it back without writing, because the generation it expects never matches.
func exclusiveAttempt(t testing.TB) func(ctx context.Context, s ownerrecord.Store) error {
	t.Helper()

	next := sealRecord(t, liveRecord())
	never := uint64(1 << 40)

	return func(ctx context.Context, s ownerrecord.Store) error {
		return s.Replace(ctx, &never, next)
	}
}

// tookExclusive reports whether err comes from a Replace that held the
// exclusive lock.
func tookExclusive(err error) bool {
	return errors.Is(err, ownerrecord.ErrRecordChanged) || errors.Is(err, ownerrecord.ErrStoredRecordInvalid)
}

// wantUnlocked proves that no shared lock is left on s by taking the
// exclusive lock.
func wantUnlocked(t *testing.T, s ownerrecord.Store) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	err := exclusiveAttempt(t)(ctx, s)
	if !tookExclusive(err) {
		t.Fatalf("the exclusive lock was not granted after a refusal: %v", err)
	}
}

// wantNoSecrets checks that msg holds neither the signature nor a key, in any
// encoding a message might use.
func wantNoSecrets(t *testing.T, msg string, secrets ...[]byte) {
	t.Helper()

	for _, s := range secrets {
		if len(s) < 16 {
			continue
		}

		for _, enc := range []string{
			string(s),
			hex.EncodeToString(s),
			base64.StdEncoding.EncodeToString(s),
			base64.RawURLEncoding.EncodeToString(s),
			strings.Trim(mustMarshalString(s), `"`),
		} {
			if strings.Contains(msg, enc) {
				t.Fatalf("message %q holds key or signature material", msg)
			}
		}
	}
}

func mustMarshalString(b []byte) string {
	out, _ := json.Marshal(b) //nolint:errchkjson // a byte slice always encodes

	return string(out)
}

func requestsEqual(a, b Request) bool {
	return a.WireContract == b.WireContract && a.JobID == b.JobID && a.Verb == b.Verb &&
		a.InstallationID == b.InstallationID && a.InstanceID == b.InstanceID && a.Epoch == b.Epoch &&
		a.Org == b.Org && a.Bloc == b.Bloc &&
		maps.Equal(a.Env, b.Env) && a.Interactive == b.Interactive && a.IssuedAt.Equal(b.IssuedAt)
}

func TestCheckOwner(t *testing.T) {
	t.Parallel()

	for _, f := range faults() {
		t.Run(f.name, func(t *testing.T) {
			t.Parallel()

			s := baseScenario()
			f.apply(s)

			st, sr, req, release, err := s.check(t)
			wantRefusal(t, err, f.code)

			if release != nil || !requestsEqual(req, Request{}) {
				t.Errorf("a refusal returned the request %+v and a release", req)
			}

			wantNoSecrets(t, err.Error(), sr.Signature, ownerKey().Public().(ed25519.PublicKey),
				otherKey().Public().(ed25519.PublicKey), ownerKey().Seed())
			wantUnlocked(t, st)
		})
	}

	t.Run("good", func(t *testing.T) {
		t.Parallel()

		s := baseScenario()
		s.request.Interactive = true

		st, _, req, release, err := s.check(t)
		if err != nil {
			t.Fatal(err)
		}

		if !requestsEqual(req, s.request) {
			t.Errorf("request = %+v, want %+v", req, s.request)
		}

		wantHeldUntilRelease(t, st, release)
	})
}

// wantHeldUntilRelease proves that the shared lock is held until release is
// called, and that release may be called again.
func wantHeldUntilRelease(t *testing.T, st ownerrecord.Store, release func() error) {
	t.Helper()

	if release == nil {
		t.Fatal("no release for an accepted request")
	}

	attempt := exclusiveAttempt(t)

	// An exclusive lock gives up when its deadline passes first.
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	err := attempt(ctx, st)

	cancel()

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("an exclusive lock was taken under the shared lock: %v", err)
	}

	// An exclusive lock waits, and is granted once release is called.
	done := make(chan error, 1)

	go func() { done <- attempt(t.Context(), st) }()

	select {
	case err := <-done:
		t.Fatalf("an exclusive lock was taken under the shared lock: %v", err)
	case <-time.After(200 * time.Millisecond):
	}

	err = release()
	if err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-done:
		if !tookExclusive(err) {
			t.Fatalf("the exclusive lock was not granted after release: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the exclusive lock was still waiting after release")
	}

	err = release()
	if err != nil {
		t.Errorf("a second release: %v", err)
	}
}

// TestCheckOwnerOrder applies each row's fault together with every fault of
// the rows after it, and checks that the row's own refusal comes back.
func TestCheckOwnerOrder(t *testing.T) {
	t.Parallel()

	all := faults()
	for i, f := range all {
		t.Run(f.name, func(t *testing.T) {
			t.Parallel()

			s := baseScenario()
			for _, later := range all[i:] {
				later.apply(s)
			}

			st, _, _, release, err := s.check(t)
			r := wantRefusal(t, err, f.code)

			if !strings.Contains(r.Message, f.phrase) {
				t.Errorf("message %q does not hold %q, so another check ran first", r.Message, f.phrase)
			}

			if release != nil {
				t.Error("a refusal returned a release")
			}

			wantUnlocked(t, st)
		})
	}
}

// TestCheckOwnerZeroContractAndFormat checks that a wire contract or a format
// that is missing, zero, or negative is refused, and not only one above the
// supported value.
func TestCheckOwnerZeroContractAndFormat(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		apply  func(*scenario)
		code   progress.Code
		phrase string
	}{
		{"wire contract 0", func(s *scenario) { s.request.WireContract = 0 }, progress.CodeWireContractUnsupported, "wire contract is 0"},
		{"wire contract -1", func(s *scenario) { s.request.WireContract = -1 }, progress.CodeWireContractUnsupported, "wire contract is -1"},
		{"format 0", func(s *scenario) { s.format = 0 }, progress.CodeJobRequestInvalid, "format 0"},
		{"format -1", func(s *scenario) { s.format = -1 }, progress.CodeJobRequestInvalid, "format -1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s := baseScenario()
			tc.apply(s)

			st, _, _, release, err := s.check(t)
			r := wantRefusal(t, err, tc.code)

			if !strings.Contains(r.Message, tc.phrase) {
				t.Errorf("message %q does not hold %q", r.Message, tc.phrase)
			}

			if release != nil {
				t.Error("a refusal returned a release")
			}

			wantUnlocked(t, st)
		})
	}

	t.Run("no wire contract", func(t *testing.T) {
		t.Parallel()

		st := storeWith(t, liveRecord())
		payload := replaceOnce(t, mustMarshal(t, goodRequest()), `"wireContract":2,`, ``)

		_, _, err := checkOwner(t.Context(), signPayload(payload, ownerKey()), st, fixedClock{testNow})
		wantRefusal(t, err, progress.CodeWireContractUnsupported)
		wantUnlocked(t, st)
	})
}

// TestCheckOwnerNamesTheFirstBadString checks that the message names the same
// field every time when several fields hold U+FFFD.
func TestCheckOwnerNamesTheFirstBadString(t *testing.T) {
	t.Parallel()

	st := storeWith(t, liveRecord())
	payload := mustMarshal(t, goodRequest())
	payload = replaceOnce(t, payload, `"noop"`, `"\ud800"`)
	payload = replaceOnce(t, payload, `"instance-fixture-a"`, `"\ud800"`)
	payload = replaceOnce(t, payload, `"inst-fixture-01"`, `"\ud800"`)

	for range 32 {
		_, _, err := checkOwner(t.Context(), signPayload(payload, ownerKey()), st, fixedClock{testNow})

		r := wantRefusal(t, err, progress.CodeJobRequestInvalid)
		if !strings.Contains(r.Message, "verb holds U+FFFD") {
			t.Fatalf("message %q does not name the verb, the first field that holds U+FFFD", r.Message)
		}
	}
}

func TestCheckOwnerIssuedAtWindow(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		offset time.Duration
		ok     bool
	}{
		{"now", 0, true},
		{"ten minutes before", -10 * time.Minute, true},
		{"just over ten minutes before", -10*time.Minute - time.Nanosecond, false},
		{"eleven minutes before", -11 * time.Minute, false},
		{"one minute after", time.Minute, true},
		{"just over one minute after", time.Minute + time.Nanosecond, false},
		{"an hour after", time.Hour, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s := baseScenario()
			s.request.IssuedAt = testNow.Add(tc.offset)

			st, _, _, release, err := s.check(t)
			if tc.ok {
				if err != nil {
					t.Fatal(err)
				}

				wantHeldUntilRelease(t, st, release)

				return
			}

			wantRefusal(t, err, progress.CodeJobRequestInvalid)
			wantUnlocked(t, st)
		})
	}

	t.Run("zero time", func(t *testing.T) {
		t.Parallel()

		s := baseScenario()
		s.request.IssuedAt = time.Time{}

		_, _, _, _, err := s.check(t)
		wantRefusal(t, err, progress.CodeJobRequestInvalid)
	})

	t.Run("another zone", func(t *testing.T) {
		t.Parallel()

		s := baseScenario()
		s.request.IssuedAt = testNow.Add(-5 * time.Minute).In(time.FixedZone("east", 5*3600))

		_, _, req, release, err := s.check(t)
		if err != nil {
			t.Fatal(err)
		}

		defer func() { _ = release() }()

		if !req.IssuedAt.Equal(s.request.IssuedAt) {
			t.Errorf("issuedAt = %v, want %v", req.IssuedAt, s.request.IssuedAt)
		}
	})
}

func TestCheckOwnerJobID(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		id string
		ok bool
	}{
		{"a", true},
		{"0", true},
		{"job-0001", true},
		{"a-", true},
		{strings.Repeat("a", 64), true},
		{"", false},
		{strings.Repeat("a", 65), false},
		{"-a", false},
		{"A", false},
		{"a_b", false},
		{"a.b", false},
		{"a/b", false},
		{"../x", false},
		{"..", false},
		{"a\n", false},
		{"a b", false},
		{"é", false},
	} {
		t.Run(tc.id, func(t *testing.T) {
			t.Parallel()

			s := baseScenario()
			s.request.JobID = tc.id

			st, _, _, release, err := s.check(t)
			if tc.ok {
				if err != nil {
					t.Fatal(err)
				}

				_ = release()

				return
			}

			wantRefusal(t, err, progress.CodeJobRequestInvalid)
			wantUnlocked(t, st)
		})
	}
}

// TestCheckOwnerQuotesRequestText checks that text from the request reaches a
// message only quoted.
func TestCheckOwnerQuotesRequestText(t *testing.T) {
	t.Parallel()

	short := "a\nb\x1b[31mc\r"
	long := short + strings.Repeat("z", 200)

	for name, apply := range map[string]func(*scenario, string){
		"job id":       func(s *scenario, v string) { s.request.JobID = v },
		"installation": func(s *scenario, v string) { s.request.InstallationID = v },
		"instance":     func(s *scenario, v string) { s.request.InstanceID = v },
		"unknown key": func(s *scenario, v string) {
			s.request.WireContract = progress.WireContract
			s.request.Env = nil
			s.key = ownerKey()
			s.extraKey = v
		},
	} {
		t.Run(name+" short", func(t *testing.T) {
			t.Parallel()

			s := baseScenario()
			apply(s, short)

			_, _, _, _, err := s.check(t)
			if err == nil {
				t.Fatal("a hostile request was accepted")
			}

			msg := err.Error()
			if strings.ContainsAny(msg, "\n\r\x1b") || !strings.Contains(msg, `"a\nb\x1b[31mc\r"`) {
				t.Errorf("message %q does not quote the request's text", msg)
			}
		})

		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s := baseScenario()
			apply(s, long)

			_, _, _, _, err := s.check(t)
			if err == nil {
				t.Fatal("a hostile request was accepted")
			}

			msg := err.Error()
			if strings.ContainsAny(msg, "\n\r\x1b") {
				t.Errorf("message %q holds raw control characters", msg)
			}

			if !strings.Contains(msg, `"a\nb\x1b[31mc\r`) {
				t.Errorf("message %q does not quote the request's text", msg)
			}

			if strings.Contains(msg, strings.Repeat("z", 100)) {
				t.Errorf("message %q quotes the request's text without a bound", msg)
			}
		})
	}
}

// replaceOnce returns the good request's payload with old replaced by new,
// failing when old is not in it exactly once.
func replaceOnce(t *testing.T, payload []byte, old, repl string) []byte {
	t.Helper()

	if bytes.Count(payload, []byte(old)) != 1 {
		t.Fatalf("%q is not in %s exactly once", old, payload)
	}

	return bytes.Replace(payload, []byte(old), []byte(repl), 1)
}

// TestCheckOwnerRefusesAmbiguousPayload signs payloads that do not read as one
// request with the owner's own key, so each reaches the decoder.
func TestCheckOwnerRefusesAmbiguousPayload(t *testing.T) {
	t.Parallel()

	good := mustMarshal(t, goodRequest())

	newer := goodRequest()
	newer.WireContract = progress.WireContract + 1
	newerPayload := mustMarshal(t, newer)

	for _, tc := range []struct {
		name    string
		payload []byte
		code    progress.Code
		phrase  string
	}{
		{"empty", nil, progress.CodeJobRequestInvalid, ""},
		{"white space", []byte("  \n"), progress.CodeJobRequestInvalid, ""},
		{"null", []byte("null"), progress.CodeJobRequestInvalid, ""},
		{"array", []byte("[" + string(good) + "]"), progress.CodeJobRequestInvalid, ""},
		{"string", []byte(`"x"`), progress.CodeJobRequestInvalid, ""},
		{"truncated", good[:len(good)-1], progress.CodeJobRequestInvalid, ""},
		{"trailing object", append(bytes.Clone(good), " {}"...), progress.CodeJobRequestInvalid, ""},
		{"trailing text", append(bytes.Clone(good), "x"...), progress.CodeJobRequestInvalid, ""},
		{"two requests", append(bytes.Clone(good), good...), progress.CodeJobRequestInvalid, ""},
		{"repeated key", replaceOnce(t, good, `{"wireContract"`, `{"jobId":"job-0002","wireContract"`), progress.CodeJobRequestInvalid, ""},
		{"repeated escaped key", replaceOnce(t, good, `{"wireContract"`, `{"job\u0049d":"job-0002","wireContract"`), progress.CodeJobRequestInvalid, ""},
		{"case variant", replaceOnce(t, good, `{"wireContract"`, `{"JobId":"job-0002","wireContract"`), progress.CodeJobRequestInvalid, "only in case"},
		{"case variant only", replaceOnce(t, good, `"jobId"`, `"JOBID"`), progress.CodeJobRequestInvalid, "only in case"},
		// An ambiguous payload is refused before any field is trusted, so
		// even a newer contract does not change the answer.
		{"case variant in a newer contract", replaceOnce(t, newerPayload, `"jobId"`, `"JOBID"`), progress.CodeJobRequestInvalid, "only in case"},
		{"repeated key in a newer contract", replaceOnce(t, newerPayload, `{"wireContract"`, `{"verb":"x","wireContract"`), progress.CodeJobRequestInvalid, ""},
		{"case variant of a requester field", replaceOnce(t, good, `{"wireContract"`, `{"InstallationID":"inst-fixture-02","wireContract"`), progress.CodeJobRequestInvalid, "only in case"},
		{"long s variant", replaceOnce(t, good, `"issuedAt"`, `"i`+"ſſ"+`uedAt"`), progress.CodeJobRequestInvalid, "only in case"},
		{"long s variant in a newer contract", replaceOnce(t, newerPayload, `"issuedAt"`, `"i`+"ſſ"+`uedAt"`), progress.CodeJobRequestInvalid, "only in case"},
		{"repeated env key", replaceOnce(t, good, `"env":{"LANG":"C"}`, `"env":{"LANG":"C","LANG":"D"}`), progress.CodeJobRequestInvalid, ""},
		{"repeated key in an unknown value", replaceOnce(t, good, `{"wireContract"`, `{"x":[{"a":1,"a":2}],"wireContract"`), progress.CodeJobRequestInvalid, ""},
		{"unknown field", replaceOnce(t, good, `{"wireContract"`, `{"argv":["sh"],"wireContract"`), progress.CodeJobRequestInvalid, ""},
		{"unknown field in a newer contract", replaceOnce(t, newerPayload, `{"wireContract"`, `{"argv":["sh"],"wireContract"`), progress.CodeWireContractUnsupported, ""},
		{"invalid UTF-8", replaceOnce(t, good, `"noop"`, "\"no\xffop\""), progress.CodeJobRequestInvalid, "not valid UTF-8"},
		{"invalid UTF-8 in a key", replaceOnce(t, good, `{"wireContract"`, "{\"\xff\":1,\"wireContract\""), progress.CodeJobRequestInvalid, "not valid UTF-8"},
		{"lone surrogate", replaceOnce(t, good, `"noop"`, `"\ud800"`), progress.CodeJobRequestInvalid, ""},
		{"lone surrogate in an env key", replaceOnce(t, good, `"env":{"LANG":"C"}`, `"env":{"\ud800":"x"}`), progress.CodeJobRequestInvalid, "U+FFFD"},
		{"lone surrogate in env", replaceOnce(t, good, `"LANG":"C"`, `"LANG":"\udc00"`), progress.CodeJobRequestInvalid, ""},
		{"replacement character", replaceOnce(t, good, `"noop"`, "\"�\""), progress.CodeJobRequestInvalid, ""},
		{"fractional epoch", replaceOnce(t, good, `"epoch":3`, `"epoch":3.0`), progress.CodeJobRequestInvalid, ""},
		{"negative epoch", replaceOnce(t, good, `"epoch":3`, `"epoch":-3`), progress.CodeJobRequestInvalid, ""},
		{"epoch out of range", replaceOnce(t, good, `"epoch":3`, `"epoch":18446744073709551616`), progress.CodeJobRequestInvalid, ""},
		{"epoch as a string", replaceOnce(t, good, `"epoch":3`, `"epoch":"3"`), progress.CodeJobRequestInvalid, ""},
		{"issuedAt not a time", replaceOnce(t, good, `"issuedAt":"`, `"issuedAt":"x`), progress.CodeJobRequestInvalid, ""},
		{"long number", replaceOnce(t, good, `"epoch":3`, `"epoch":1`+strings.Repeat("1", 500)), progress.CodeJobRequestInvalid, ""},
		{"long time text", replaceOnce(t, good, `"issuedAt":"`, `"issuedAt":"`+strings.Repeat("1", 500)), progress.CodeJobRequestInvalid, ""},
		{"env not an object", replaceOnce(t, good, `"env":{"LANG":"C"}`, `"env":["LANG"]`), progress.CodeJobRequestInvalid, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			st := storeWith(t, liveRecord())

			_, release, err := checkOwner(t.Context(), signPayload(tc.payload, ownerKey()), st, fixedClock{testNow})
			wantRefusal(t, err, tc.code)

			if release != nil {
				t.Error("a refusal returned a release")
			}

			if strings.ContainsAny(err.Error(), "\n\r\x1b\xff") {
				t.Errorf("message %q holds raw control characters", err.Error())
			}

			if !strings.Contains(err.Error(), tc.phrase) {
				t.Errorf("message %q does not hold %q", err.Error(), tc.phrase)
			}

			if strings.Contains(err.Error(), strings.Repeat("1", 100)) {
				t.Errorf("message %q echoes request text without a bound", err.Error())
			}

			wantUnlocked(t, st)
		})
	}

	t.Run("white space around the object", func(t *testing.T) {
		t.Parallel()

		st := storeWith(t, liveRecord())
		payload := append(append([]byte(" \t\r\n"), good...), " \n"...)

		_, release, err := checkOwner(t.Context(), signPayload(payload, ownerKey()), st, fixedClock{testNow})
		if err != nil {
			t.Fatal(err)
		}

		_ = release()
	})
}

func TestCheckOwnerSignatureForms(t *testing.T) {
	t.Parallel()

	payload := mustMarshal(t, goodRequest())
	good := signPayload(payload, ownerKey())

	flipped := bytes.Clone(payload)
	flipped[len(flipped)-2] ^= 0x01

	for _, tc := range []struct {
		name string
		sr   SignedRequest
	}{
		{"nil", SignedRequest{Format: RequestFormatV1, Payload: payload}},
		{"empty", SignedRequest{Format: RequestFormatV1, Payload: payload, Signature: []byte{}}},
		{"short", SignedRequest{Format: RequestFormatV1, Payload: payload, Signature: good.Signature[:63]}},
		{"long", SignedRequest{Format: RequestFormatV1, Payload: payload, Signature: append(bytes.Clone(good.Signature), 0)}},
		{"zero", SignedRequest{Format: RequestFormatV1, Payload: payload, Signature: make([]byte, ed25519.SignatureSize)}},
		{"payload changed", SignedRequest{Format: RequestFormatV1, Payload: flipped, Signature: good.Signature}},
		{"no context", SignedRequest{Format: RequestFormatV1, Payload: payload, Signature: ed25519.Sign(ownerKey(), payload)}},
		{"owner record context", SignedRequest{
			Format: RequestFormatV1, Payload: payload,
			Signature: ed25519.Sign(ownerKey(), ownerrecord.SigningBytes(payload)),
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			st := storeWith(t, liveRecord())

			_, release, err := checkOwner(t.Context(), tc.sr, st, fixedClock{testNow})
			wantRefusal(t, err, progress.CodeRequestUnsigned)

			if release != nil {
				t.Error("a refusal returned a release")
			}

			wantNoSecrets(t, err.Error(), tc.sr.Signature, good.Signature, ownerKey().Public().(ed25519.PublicKey))
			wantUnlocked(t, st)
		})
	}
}

func TestRequestSigningBytes(t *testing.T) {
	t.Parallel()

	payload := make([]byte, 3, 16)
	copy(payload, "abc")

	msg := RequestSigningBytes(payload)
	if string(msg) != RequestSigningContext+"abc" {
		t.Fatalf("RequestSigningBytes = %q", msg)
	}

	msg[len(msg)-1] = 'x'
	_ = append(payload, 'y')

	if string(payload) != "abc" || string(msg) != RequestSigningContext+"abx" {
		t.Errorf("RequestSigningBytes shares memory with its payload: %q, %q", payload, msg)
	}

	if RequestSigningContext != "ocfp-job-request-v1\n" || RequestFormatV1 != 1 {
		t.Errorf("RequestSigningContext = %q, RequestFormatV1 = %d", RequestSigningContext, RequestFormatV1)
	}
}

// TestSigningContextsDiffer checks that a signed owner record never passes as
// a signed job request, and a signed job request never as an owner record.
func TestSigningContextsDiffer(t *testing.T) {
	t.Parallel()

	if strings.HasPrefix(RequestSigningContext, ownerrecord.SigningContext) ||
		strings.HasPrefix(ownerrecord.SigningContext, RequestSigningContext) {
		t.Fatalf("the signing contexts %q and %q overlap", RequestSigningContext, ownerrecord.SigningContext)
	}

	t.Run("owner record as a job request", func(t *testing.T) {
		t.Parallel()

		st := storeWith(t, liveRecord())

		sf, _, err := st.Read(t.Context())
		if err != nil {
			t.Fatal(err)
		}

		_, _, err = checkOwner(t.Context(), SignedRequest{Format: RequestFormatV1, Payload: sf.Payload, Signature: sf.Signature}, st, fixedClock{testNow})
		wantRefusal(t, err, progress.CodeRequestUnsigned)

		// A payload that reads as a good request, signed as an owner record,
		// is refused, and the same payload signed as a request is not.
		payload := mustMarshal(t, goodRequest())

		_, _, err = checkOwner(t.Context(), SignedRequest{
			Format: RequestFormatV1, Payload: payload, Signature: ed25519.Sign(ownerKey(), ownerrecord.SigningBytes(payload)),
		}, st, fixedClock{testNow})
		wantRefusal(t, err, progress.CodeRequestUnsigned)

		_, release, err := checkOwner(t.Context(), signPayload(payload, ownerKey()), st, fixedClock{testNow})
		if err != nil {
			t.Fatalf("the control request was refused: %v", err)
		}

		_ = release()
	})

	t.Run("job request as an owner record", func(t *testing.T) {
		t.Parallel()

		// A payload that reads as a good owner record, signed as a job
		// request, does not open, and the same payload signed as a record
		// does.
		payload := sealRecord(t, liveRecord()).Payload

		_, err := ownerrecord.Open(ownerrecord.Signed{
			Format: ownerrecord.FormatV1, Payload: payload, Signature: ed25519.Sign(ownerKey(), RequestSigningBytes(payload)),
		})
		wantRefusal(t, err, progress.CodeRequestUnsigned)

		_, err = ownerrecord.Open(ownerrecord.Signed{
			Format: ownerrecord.FormatV1, Payload: payload, Signature: ed25519.Sign(ownerKey(), ownerrecord.SigningBytes(payload)),
		})
		if err != nil {
			t.Fatalf("the control record does not open: %v", err)
		}

		// A signed job request does not open as an owner record.
		sr := signPayload(mustMarshal(t, goodRequest()), ownerKey())

		_, err = ownerrecord.Open(ownerrecord.Signed{Format: ownerrecord.FormatV1, Payload: sr.Payload, Signature: sr.Signature})
		if _, ok := progress.AsRefusal(err); !ok {
			t.Fatalf("a signed job request opened as an owner record: %v", err)
		}
	})
}

func TestCheckOwnerMissingDirectory(t *testing.T) {
	t.Parallel()

	st := ownerrecord.NewDirStore(filepath.Join(t.TempDir(), "absent"))

	_, release, err := checkOwner(t.Context(), baseScenario().signed(t), st, fixedClock{testNow})
	wantRefusal(t, err, progress.CodeOwnerRecordMissing)

	if release != nil {
		t.Error("a refusal returned a release")
	}
}

// writeStored writes sf as the stored record of a new store, without the
// checks Replace makes.
func writeStored(t *testing.T, data []byte) *ownerrecord.DirStore {
	t.Helper()

	st, dir := emptyStore(t)

	err := os.WriteFile(filepath.Join(dir, ownerrecord.RecordFile), data, 0o640)
	if err != nil {
		t.Fatal(err)
	}

	return st
}

// refusalCodes returns the code of every *progress.Refusal in err's tree,
// in the order errors.As would meet them.
func refusalCodes(err error) []progress.Code {
	if err == nil {
		return nil
	}

	var codes []progress.Code

	if r, ok := err.(*progress.Refusal); ok && r != nil { //nolint:errorlint // walks the tree by hand
		codes = append(codes, r.Code)
	}

	switch u := err.(type) { //nolint:errorlint // walks the tree by hand
	case interface{ Unwrap() error }:
		codes = append(codes, refusalCodes(u.Unwrap())...)
	case interface{ Unwrap() []error }:
		for _, e := range u.Unwrap() {
			codes = append(codes, refusalCodes(e)...)
		}
	}

	return codes
}

// wantStoredRecordInvalid checks that err answers a stored record that does
// not open with OWNER_RECORD_INVALID and no other code, keeps
// ErrStoredRecordInvalid, and carries the reason in its text.
func wantStoredRecordInvalid(t *testing.T, err error, reason string) {
	t.Helper()

	wantRefusal(t, err, progress.CodeOwnerRecordInvalid)

	if !errors.Is(err, ownerrecord.ErrStoredRecordInvalid) {
		t.Errorf("err = %v, want it to wrap ErrStoredRecordInvalid", err)
	}

	if codes := refusalCodes(err); len(codes) != 1 || codes[0] != progress.CodeOwnerRecordInvalid {
		t.Errorf("err carries the refusal codes %v, want only %s", codes, progress.CodeOwnerRecordInvalid)
	}

	if !strings.Contains(err.Error(), reason) {
		t.Errorf("message %q does not hold the reason %q", err.Error(), reason)
	}
}

func TestCheckOwnerStoredRecordDoesNotOpen(t *testing.T) {
	t.Parallel()

	t.Run("bad signature", func(t *testing.T) {
		t.Parallel()

		st := writeStored(t, ownerrecordtest.Fixture("record-bad-signature-v1.json"))

		_, release, err := checkOwner(t.Context(), baseScenario().signed(t), st, fixedClock{testNow})
		wantStoredRecordInvalid(t, err, "REQUEST_UNSIGNED: owner record signature does not verify")

		if release != nil {
			t.Error("a refusal returned a release")
		}

		wantUnlocked(t, st)
	})

	t.Run("not a record", func(t *testing.T) {
		t.Parallel()

		st := writeStored(t, []byte("not json"))

		_, _, err := checkOwner(t.Context(), baseScenario().signed(t), st, fixedClock{testNow})
		wantStoredRecordInvalid(t, err, "JOB_REQUEST_INVALID: signed owner record")

		wantUnlocked(t, st)
	})

	t.Run("handover signed by the outgoing owner", func(t *testing.T) {
		t.Parallel()

		// The record names the new owner but is signed by the old one, so
		// it does not open, and it must still be answered as a hand-over.
		r := liveRecord()
		r.OwnerInstanceID = "instance-fixture-b"
		r.OwnerPublicKey = otherKey().Public().(ed25519.PublicKey)
		handover(&r)

		sf, err := ownerrecord.SealWithKey(r, ownerKey().Public().(ed25519.PublicKey), func(msg []byte) ([]byte, error) {
			return ed25519.Sign(ownerKey(), msg), nil
		})
		if err != nil {
			t.Fatal(err)
		}

		st := writeStored(t, mustMarshal(t, sf))

		// Neither the old owner's nor the new owner's request may pass.
		for _, key := range []ed25519.PrivateKey{ownerKey(), otherKey()} {
			s := baseScenario()
			s.key = key
			s.request.InstanceID = "instance-fixture-b"

			_, release, err := checkOwner(t.Context(), s.signed(t), st, fixedClock{testNow})
			wantRefusal(t, err, progress.CodeOwnerHandover)

			if release != nil {
				t.Error("a refusal returned a release")
			}

			wantUnlocked(t, st)
		}
	})
}

// countingStore wraps a store and counts the shared locks it hands out and
// the calls to their releases. Its releases are not idempotent: each call
// counts.
type countingStore struct {
	ownerrecord.Store

	mu         sync.Mutex
	held       int
	releases   int
	releaseErr error
}

func (s *countingStore) RLock(ctx context.Context) (func() error, error) {
	release, err := s.Store.RLock(ctx)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.held++
	s.mu.Unlock()

	return func() error {
		s.mu.Lock()
		s.held--
		s.releases++
		s.mu.Unlock()

		err := release()
		if err != nil {
			return err
		}

		return s.releaseErr
	}, nil
}

func (s *countingStore) counts() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.held, s.releases
}

func TestCheckOwnerReleasesOnce(t *testing.T) {
	t.Parallel()

	t.Run("accepted", func(t *testing.T) {
		t.Parallel()

		cs := &countingStore{Store: storeWith(t, liveRecord())}

		_, release, err := checkOwner(t.Context(), baseScenario().signed(t), cs, fixedClock{testNow})
		if err != nil {
			t.Fatal(err)
		}

		if held, n := cs.counts(); held != 1 || n != 0 {
			t.Fatalf("after acceptance: %d locks held, %d releases, want 1 and 0", held, n)
		}

		for range 3 {
			err := release()
			if err != nil {
				t.Fatal(err)
			}
		}

		if held, n := cs.counts(); held != 0 || n != 1 {
			t.Errorf("after three releases: %d locks held, %d store releases, want 0 and 1", held, n)
		}
	})

	t.Run("refused", func(t *testing.T) {
		t.Parallel()

		for _, f := range faults()[1:] {
			s := baseScenario()
			f.apply(s)

			cs := &countingStore{Store: s.store(t)}

			_, _, err := checkOwner(t.Context(), s.signed(t), cs, fixedClock{testNow})
			wantRefusal(t, err, f.code)

			if held, n := cs.counts(); held != 0 || n != 1 {
				t.Errorf("%s: %d locks held, %d releases, want 0 and 1", f.name, held, n)
			}
		}
	})

	t.Run("release fails on a refusal", func(t *testing.T) {
		t.Parallel()

		errRelease := errors.New("release failed")
		cs := &countingStore{Store: storeWith(t, liveRecord()), releaseErr: errRelease}

		s := baseScenario()
		s.request.Epoch = 9

		_, release, err := checkOwner(t.Context(), s.signed(t), cs, fixedClock{testNow})
		wantRefusal(t, err, progress.CodeOwnerMismatch)

		if !errors.Is(err, errRelease) {
			t.Errorf("err = %v, want it to carry the release error", err)
		}

		if release != nil {
			t.Error("a refusal returned a release")
		}
	})

	t.Run("release fails after acceptance", func(t *testing.T) {
		t.Parallel()

		errRelease := errors.New("release failed")
		cs := &countingStore{Store: storeWith(t, liveRecord()), releaseErr: errRelease}

		_, release, err := checkOwner(t.Context(), baseScenario().signed(t), cs, fixedClock{testNow})
		if err != nil {
			t.Fatal(err)
		}

		for range 2 {
			if err := release(); !errors.Is(err, errRelease) {
				t.Errorf("release = %v, want the store's release error", err)
			}
		}

		if _, n := cs.counts(); n != 1 {
			t.Errorf("%d store releases, want 1", n)
		}
	})
}

func TestCheckOwnerCancelledContext(t *testing.T) {
	t.Parallel()

	st := storeWith(t, liveRecord())

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	req, release, err := checkOwner(ctx, baseScenario().signed(t), st, fixedClock{testNow})
	if !errors.Is(err, context.Canceled) || release != nil || !requestsEqual(req, Request{}) {
		t.Fatalf("a cancelled check returned %+v, release %v, err %v", req, release != nil, err)
	}

	wantUnlocked(t, st)
}

func TestCheckOwnerNilStore(t *testing.T) {
	t.Parallel()

	_, release, err := CheckOwner(t.Context(), baseScenario().signed(t), nil)
	if err == nil || release != nil {
		t.Fatalf("a nil store returned release %v, err %v", release != nil, err)
	}
}

// TestCheckOwnerUsesTheSystemClock checks that CheckOwner reads the wall
// clock, so a request issued now passes and one issued an hour ago does not.
func TestCheckOwnerUsesTheSystemClock(t *testing.T) {
	t.Parallel()

	st := storeWith(t, liveRecord())

	req := goodRequest()
	req.IssuedAt = time.Now()

	_, release, err := CheckOwner(t.Context(), signPayload(mustMarshal(t, req), ownerKey()), st)
	if err != nil {
		t.Fatal(err)
	}

	_ = release()

	req.IssuedAt = time.Now().Add(-time.Hour)

	_, _, err = CheckOwner(t.Context(), signPayload(mustMarshal(t, req), ownerKey()), st)
	wantRefusal(t, err, progress.CodeJobRequestInvalid)
}

// FuzzCheckOwner feeds payloads, signed by the owner or carrying the given
// signature, to the owner check, and checks that every answer is either a
// refusal that holds no lock or a request that meets every rule.
func FuzzCheckOwner(f *testing.F) {
	good := mustMarshal(f, goodRequest())

	f.Add(good, []byte{}, true)
	f.Add(good, make([]byte, ed25519.SignatureSize), false)
	f.Add([]byte(`{"wireContract":1,"jobId":"a","jobId":"b"}`), []byte{}, true)
	f.Add([]byte(`{"JobId":"a"}`), []byte{}, true)
	f.Add([]byte(`{"verb":"\ud800"} {}`), []byte{}, true)
	f.Add([]byte(`{"env":{"A":"1","A":"2"}}`), []byte{}, true)

	st := ownerrecord.NewDirStore(filepath.Join(f.TempDir(), "owner"))

	err := st.Replace(f.Context(), nil, sealRecord(f, liveRecord()))
	if err != nil {
		f.Fatal(err)
	}

	cs := &countingStore{Store: st}
	rec := liveRecord()

	f.Fuzz(func(t *testing.T, payload, sig []byte, sign bool) {
		sr := SignedRequest{Format: RequestFormatV1, Payload: payload, Signature: sig}
		if sign {
			sr.Signature = ed25519.Sign(ownerKey(), RequestSigningBytes(payload))
		}

		req, release, err := checkOwner(t.Context(), sr, cs, fixedClock{testNow})
		if err != nil {
			if _, ok := progress.AsRefusal(err); !ok {
				t.Fatalf("error %v is not a refusal", err)
			}

			if release != nil {
				t.Fatal("a refusal returned a release")
			}

			if held, _ := cs.counts(); held != 0 {
				t.Fatalf("a refusal left %d shared locks held", held)
			}

			if strings.ContainsAny(err.Error(), "\n\r\x1b") {
				t.Fatalf("message %q holds raw control characters", err.Error())
			}

			wantNoSecrets(t, err.Error(), sr.Signature, ownerKey().Public().(ed25519.PublicKey))

			return
		}

		if !sign {
			t.Fatal("a request accepted without the owner's signature")
		}

		age := testNow.Sub(req.IssuedAt)
		if req.InstallationID != rec.InstallationID || req.InstanceID != rec.OwnerInstanceID ||
			req.Epoch != rec.Epoch || req.WireContract != progress.WireContract ||
			!jobIDPattern.MatchString(req.JobID) || age > 10*time.Minute || age < -time.Minute {
			t.Fatalf("accepted a request that breaks a rule: %+v", req)
		}

		if held, _ := cs.counts(); held != 1 {
			t.Fatalf("an accepted request holds %d shared locks, want 1", held)
		}

		if release() != nil || release() != nil {
			t.Fatal("release failed")
		}

		if held, _ := cs.counts(); held != 0 {
			t.Fatalf("release left %d shared locks held", held)
		}
	})
}

// modeStore is a store whose Read reports its record with another mode, as a
// store that did not keep its contract might.
type modeStore struct {
	ownerrecord.Store

	mode ownerrecord.Mode
}

func (s modeStore) Read(ctx context.Context) (ownerrecord.Signed, ownerrecord.Record, error) {
	sf, r, err := s.Store.Read(ctx)
	r.Mode = s.mode

	return sf, r, err
}

func TestCheckOwnerRefusesAModeNotLive(t *testing.T) {
	t.Parallel()

	for _, mode := range []ownerrecord.Mode{"", "paused", "LIVE"} {
		st := storeWith(t, liveRecord())

		_, release, err := checkOwner(t.Context(), baseScenario().signed(t), modeStore{Store: st, mode: mode}, fixedClock{testNow})
		wantRefusal(t, err, progress.CodeOwnerHandover)

		if release != nil {
			t.Errorf("mode %q: a refusal returned a release", mode)
		}

		wantUnlocked(t, st)
	}
}
