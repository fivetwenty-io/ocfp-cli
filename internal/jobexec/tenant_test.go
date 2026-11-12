package jobexec

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
)

// payloadWithout returns req's JSON with the named keys removed and the wire
// contract set to contract, the shape a requester of an older contract sends.
func payloadWithout(t *testing.T, req Request, contract int, keys ...string) []byte {
	t.Helper()

	var m map[string]json.RawMessage

	if err := json.Unmarshal(mustMarshal(t, req), &m); err != nil {
		t.Fatal(err)
	}

	for _, k := range keys {
		if _, ok := m[k]; !ok {
			t.Fatalf("the request has no key %q to remove", k)
		}

		delete(m, k)
	}

	m["wireContract"] = mustMarshal(t, contract)

	return mustMarshal(t, m)
}

// TestCheckOwnerOrgAndBloc checks that a request must name an org and a bloc
// of the safe name form, and that a missing or malformed value is refused as
// invalid before it is compared with the owner record.
func TestCheckOwnerOrgAndBloc(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		apply  func(*Request)
		phrase string
	}{
		{"no org", func(r *Request) { r.Org = "" }, "names no org"},
		{"no bloc", func(r *Request) { r.Bloc = "" }, "names no bloc"},
		{"no org and another bloc", func(r *Request) { r.Org, r.Bloc = "", "bloc-other" }, "names no org"},
		{"org with a separator", func(r *Request) { r.Org = "../org-fixture" }, "org \"../org-fixture\" is not of the form"},
		{"org with a dot first", func(r *Request) { r.Org = ".org" }, "is not of the form"},
		{"org in upper case", func(r *Request) { r.Org = "ORG-FIXTURE" }, "is not of the form"},
		{"org too long", func(r *Request) { r.Org = strings.Repeat("o", 65) }, "is not of the form"},
		{"bloc with a separator", func(r *Request) { r.Bloc = "bloc/fixture" }, "bloc \"bloc/fixture\" is not of the form"},
		{"bloc with a dash first", func(r *Request) { r.Bloc = "-bloc" }, "is not of the form"},
		{"bloc with a newline", func(r *Request) { r.Bloc = "bloc-fixture\n" }, "is not of the form"},
		{"org with U+FFFD", func(r *Request) { r.Org = "org-�" }, "org holds U+FFFD"},
		{"bloc with U+FFFD", func(r *Request) { r.Bloc = "bloc-�" }, "bloc holds U+FFFD"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s := baseScenario()
			tc.apply(&s.request)

			st, _, _, release, err := s.check(t)

			r := wantRefusal(t, err, progress.CodeJobRequestInvalid)
			if !strings.Contains(r.Message, tc.phrase) {
				t.Errorf("message %q does not hold %q", r.Message, tc.phrase)
			}

			if release != nil {
				t.Error("a refusal returned a release")
			}

			wantUnlocked(t, st)
		})
	}

	// A key left out of a current-contract payload is refused the same way as
	// an empty one.
	for _, key := range []string{"org", "bloc"} {
		t.Run("payload without "+key, func(t *testing.T) {
			t.Parallel()

			st := storeWith(t, liveRecord())
			payload := payloadWithout(t, goodRequest(), progress.WireContract, key)

			_, release, err := checkOwner(t.Context(), signPayload(payload, ownerKey()), st, fixedClock{testNow})

			r := wantRefusal(t, err, progress.CodeJobRequestInvalid)
			if !strings.Contains(r.Message, "names no "+key) {
				t.Errorf("message %q does not say the request names no %s", r.Message, key)
			}

			if release != nil {
				t.Error("a refusal returned a release")
			}

			wantUnlocked(t, st)
		})
	}
}

// TestCheckOwnerRefusesAnotherTenant checks that a request signed by the owner
// for another org or bloc is refused, and names both values in its message.
func TestCheckOwnerRefusesAnotherTenant(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		apply func(*Request)
		want  []string
	}{
		{"other org", func(r *Request) { r.Org = "org-other" }, []string{`names org "org-other"`, `serves org "org-fixture"`}},
		{"other bloc", func(r *Request) { r.Bloc = "bloc-other" }, []string{`names bloc "bloc-other"`, `serves bloc "bloc-fixture"`}},
		{"other org and bloc", func(r *Request) { r.Org, r.Bloc = "org-other", "bloc-other" }, []string{`names org "org-other"`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s := baseScenario()
			tc.apply(&s.request)

			st, _, req, release, err := s.check(t)

			r := wantRefusal(t, err, progress.CodeTenantMismatch)
			for _, w := range tc.want {
				if !strings.Contains(r.Message, w) {
					t.Errorf("message %q does not hold %q", r.Message, w)
				}
			}

			if release != nil || !requestsEqual(req, Request{}) {
				t.Errorf("a refusal returned the request %+v and a release", req)
			}

			wantUnlocked(t, st)
		})
	}
}

// TestCheckOwnerSignatureCoversOrgAndBloc takes a request the owner signed for
// another org or bloc and rewrites that value to the one this host serves.
// The rewritten request names this host, so only the signature can refuse it.
func TestCheckOwnerSignatureCoversOrgAndBloc(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		apply     func(*Request)
		old, repl string
	}{
		{"org", func(r *Request) { r.Org = "org-other" }, `"org":"org-other"`, `"org":"org-fixture"`},
		{"bloc", func(r *Request) { r.Bloc = "bloc-other" }, `"bloc":"bloc-other"`, `"bloc":"bloc-fixture"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req := goodRequest()
			tc.apply(&req)

			signed := signPayload(mustMarshal(t, req), ownerKey())
			signed.Payload = replaceOnce(t, signed.Payload, tc.old, tc.repl)

			st := storeWith(t, liveRecord())

			_, release, err := checkOwner(t.Context(), signed, st, fixedClock{testNow})
			wantRefusal(t, err, progress.CodeRequestUnsigned)

			if release != nil {
				t.Error("a refusal returned a release")
			}

			wantUnlocked(t, st)
		})
	}
}

// TestCheckOwnerRefusesAnOldFormatRequest signs a request in the shape of the
// first wire contract, which has no org or bloc, with the owner's own key.
func TestCheckOwnerRefusesAnOldFormatRequest(t *testing.T) {
	t.Parallel()

	st := storeWith(t, liveRecord())
	payload := payloadWithout(t, goodRequest(), 1, "org", "bloc")

	_, release, err := checkOwner(t.Context(), signPayload(payload, ownerKey()), st, fixedClock{testNow})

	r := wantRefusal(t, err, progress.CodeWireContractUnsupported)
	if !strings.Contains(r.Message, "wire contract is 1, this CLI reads 2") || !strings.Contains(r.Message, "names no org or bloc") {
		t.Errorf("message %q does not say why a contract 1 request is refused", r.Message)
	}

	if release != nil {
		t.Error("a refusal returned a release")
	}

	wantUnlocked(t, st)
}

// TestRunRefusesAnotherTenantBeforeTheSpool checks that a request for another
// org or bloc, or in the old format, is refused before anything is made under
// the spool root.
func TestRunRefusesAnotherTenantBeforeTheSpool(t *testing.T) {
	t.Parallel()

	other := func(apply func(*Request)) SignedRequest {
		req := goodRequest()
		apply(&req)

		return signPayload(mustMarshal(t, req), ownerKey())
	}

	for _, tc := range []struct {
		name string
		sr   SignedRequest
		code progress.Code
	}{
		{"other org", other(func(r *Request) { r.Org = "org-other" }), progress.CodeTenantMismatch},
		{"other bloc", other(func(r *Request) { r.Bloc = "bloc-other" }), progress.CodeTenantMismatch},
		{"no bloc", other(func(r *Request) { r.Bloc = "" }), progress.CodeJobRequestInvalid},
		{"old format", signPayload(payloadWithout(t, goodRequest(), 1, "org", "bloc"), ownerKey()), progress.CodeWireContractUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			root := spoolRoot(t)
			st := storeWith(t, liveRecord())
			rc := newRun(t, st, root, tc.sr)

			wantRefusal(t, rc.run(t), tc.code)
			wantRefusalStream(t, rc.stdout.Bytes(), tc.code, "")

			entries, err := os.ReadDir(root)
			if err != nil {
				t.Fatal(err)
			}

			if len(entries) != 0 {
				t.Fatalf("a refused request left %d entries under the spool root", len(entries))
			}

			if n := rc.host.calls.Load(); n != 0 {
				t.Fatalf("a refused run called Hostname %d times", n)
			}

			wantUnlocked(t, st)
		})
	}

	t.Run("this org and bloc", func(t *testing.T) {
		t.Parallel()

		root := spoolRoot(t)
		rc := newRun(t, storeWith(t, liveRecord()), root, signedRequest(t, goodRequest()))

		if err := rc.run(t); err != nil {
			t.Fatalf("Run = %v", err)
		}

		if _, err := os.Stat(filepath.Join(root, "job-0001", "exit.json")); err != nil {
			t.Fatalf("the accepted job has no exit.json: %v", err)
		}
	})
}
