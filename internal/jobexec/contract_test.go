package jobexec

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fivetwenty-io/ocfp-cli/internal/ownerrecord"
	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
)

// contractRequest is a wire contract query from the owner, which names no
// verb, env, or interactive.
func contractRequest() Request {
	req := goodRequest()
	req.Action = ActionWireContract
	req.Verb = ""
	req.Env = nil

	return req
}

func contractScenario() *scenario {
	s := baseScenario()
	s.request = contractRequest()

	return s
}

// legacyOnlyStore is a store whose directory holds r only under the record's
// old file name, as an earlier release left it.
func legacyOnlyStore(t *testing.T, r ownerrecord.Record) *ownerrecord.DirStore {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "owner")

	err := ownerrecord.NewDirStore(dir).Replace(t.Context(), nil, sealRecord(t, r))
	if err != nil {
		t.Fatal(err)
	}

	err = os.Rename(filepath.Join(dir, ownerrecord.RecordFile), filepath.Join(dir, ownerrecord.LegacyRecordFile))
	if err != nil {
		t.Fatal(err)
	}

	return ownerrecord.NewDirStore(dir)
}

// brokenRecordStore is a store whose record's signature does not verify.
func brokenRecordStore(t *testing.T) *ownerrecord.DirStore {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "owner")
	sf := sealRecord(t, liveRecord())
	sf.Signature = slices.Clone(sf.Signature)
	sf.Signature[0] ^= 0x01

	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, ownerrecord.RecordFile), mustMarshal(t, sf), 0o640); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(filepath.Join(dir, ownerrecord.RecordFile), 0o640); err != nil {
		t.Fatal(err)
	}

	return ownerrecord.NewDirStore(dir)
}

// requestLine is sr as one line on stdin.
func requestLine(t *testing.T, sr SignedRequest) io.Reader {
	t.Helper()

	return bytes.NewReader(append(mustMarshal(t, sr), '\n'))
}

// answer runs AnswerWireContract for stdin against st at now, and returns
// what it wrote and its error.
func answer(t *testing.T, st *ownerrecord.DirStore, stdin io.Reader, now time.Time) (string, error) {
	t.Helper()

	var out bytes.Buffer

	err := AnswerWireContract(t.Context(), ContractOptions{Owner: st, Stdin: stdin, Stdout: &out, Clock: fixedClock{now}})

	return out.String(), err
}

// refusalLine decodes out, which must be exactly one refusal line, and
// returns it. It fails on any top-level key but format, jobId, and error.
func refusalLine(t *testing.T, out string) WireContractRefusal {
	t.Helper()

	if strings.Count(out, "\n") != 1 || !strings.HasSuffix(out, "\n") {
		t.Fatalf("stdout = %q, want exactly one line", out)
	}

	var keys map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &keys); err != nil {
		t.Fatalf("stdout %q does not decode: %v", out, err)
	}

	for k := range keys {
		if k != "format" && k != "jobId" && k != "error" {
			t.Fatalf("stdout %q holds the key %q", out, k)
		}
	}

	var ref WireContractRefusal

	dec := json.NewDecoder(strings.NewReader(out))
	dec.DisallowUnknownFields()

	if err := dec.Decode(&ref); err != nil {
		t.Fatalf("stdout %q does not decode as a refusal: %v", out, err)
	}

	if ref.Format != WireContractAnswerFormat {
		t.Fatalf("refusal format = %d, want %d", ref.Format, WireContractAnswerFormat)
	}

	return ref
}

func TestAnswerWireContractMatch(t *testing.T) {
	t.Parallel()

	for _, contract := range []int{progress.WireContract, progress.WireContract + 1, progress.WireContract + 40} {
		s := contractScenario()
		s.request.WireContract = contract

		st := s.store(t)

		out, err := answer(t, st, requestLine(t, s.signed(t)), s.now)
		if err != nil {
			t.Fatalf("contract %d: AnswerWireContract = %v", contract, err)
		}

		if want := `{"format":1,"wireContract":3,"jobId":"job-0001"}` + "\n"; out != want {
			t.Fatalf("contract %d: stdout = %q, want %q", contract, out, want)
		}

		wantUnlocked(t, st)
	}
}

// contractFault is one row of the refusal table. echo says whether the
// refusal names the job, which it does only once the signature has verified
// and the payload has decoded to a job id.
type contractFault struct {
	name   string
	code   progress.Code
	phrase string
	echo   bool
	apply  func(*scenario)
	store  func(t *testing.T) *ownerrecord.DirStore
}

func contractFaults() []contractFault {
	return []contractFault{
		{name: "no record", code: progress.CodeOwnerRecordMissing, phrase: "no owner record", apply: func(s *scenario) { s.record = nil }},
		{name: "no owner directory", code: progress.CodeOwnerRecordMissing, phrase: "no owner record", store: func(t *testing.T) *ownerrecord.DirStore {
			return ownerrecord.NewDirStore(filepath.Join(t.TempDir(), "owner"))
		}},
		{name: "legacy record only", code: progress.CodeOwnerRecordMissing, phrase: "no owner record", store: func(t *testing.T) *ownerrecord.DirStore {
			return legacyOnlyStore(t, liveRecord())
		}},
		{name: "record does not open", code: progress.CodeOwnerRecordInvalid, phrase: "does not open", store: brokenRecordStore},
		{name: "bad signature", code: progress.CodeRequestUnsigned, phrase: "signature", apply: func(s *scenario) { s.key = otherKey() }},
		{name: "empty signature", code: progress.CodeRequestUnsigned, phrase: "signature", apply: func(s *scenario) { s.noSig = true }},
		{name: "other installation", code: progress.CodeOwnerMismatch, phrase: "names installation", echo: true, apply: func(s *scenario) {
			s.request.InstallationID = "inst-fixture-02"
		}},
		{name: "other instance", code: progress.CodeOwnerMismatch, phrase: "names instance", echo: true, apply: func(s *scenario) {
			s.request.InstanceID = "instance-fixture-b"
		}},
		{name: "other epoch", code: progress.CodeOwnerMismatch, phrase: "names epoch", echo: true, apply: func(s *scenario) { s.request.Epoch = 4 }},
		{name: "handover", code: progress.CodeOwnerHandover, phrase: "handover", echo: true, apply: func(s *scenario) { handover(s.record) }},
		{name: "older contract", code: progress.CodeWireContractUnsupported, phrase: "names no action", echo: true, apply: func(s *scenario) {
			s.request.WireContract = progress.WireContract - 1
		}},
		{name: "no contract", code: progress.CodeWireContractUnsupported, phrase: "3 or later", echo: true, apply: func(s *scenario) {
			s.request.WireContract = 0
		}},
		{name: "other org", code: progress.CodeOrgBlocMismatch, phrase: "names org", echo: true, apply: func(s *scenario) { s.request.Org = "org-other" }},
		{name: "other bloc", code: progress.CodeOrgBlocMismatch, phrase: "names bloc", echo: true, apply: func(s *scenario) { s.request.Bloc = "bloc-other" }},
		{name: "no org", code: progress.CodeJobRequestInvalid, phrase: "names no org", echo: true, apply: func(s *scenario) { s.request.Org = "" }},
		{name: "unknown key", code: progress.CodeJobRequestInvalid, phrase: "unknown field", echo: true, apply: func(s *scenario) { s.extraKey = "spoolRoot" }},
		{name: "stale request", code: progress.CodeJobRequestInvalid, phrase: "issued", echo: true, apply: func(s *scenario) {
			s.request.IssuedAt = s.now.Add(-11 * time.Minute)
		}},
		{name: "future request", code: progress.CodeJobRequestInvalid, phrase: "issued", echo: true, apply: func(s *scenario) {
			s.request.IssuedAt = s.now.Add(2 * time.Minute)
		}},
		{name: "bad job id", code: progress.CodeJobRequestInvalid, phrase: "job id", apply: func(s *scenario) { s.request.JobID = "../x" }},
		{name: "no job id", code: progress.CodeJobRequestInvalid, phrase: "job id", apply: func(s *scenario) { s.request.JobID = "" }},
		{name: "unknown format", code: progress.CodeJobRequestInvalid, phrase: "format", echo: true, apply: func(s *scenario) { s.format = 2 }},
		{name: "run", code: progress.CodeJobRequestInvalid, phrase: "action", echo: true, apply: func(s *scenario) {
			s.request = goodRequest()
		}},
		{name: "run with no verb", code: progress.CodeJobRequestInvalid, phrase: "action", echo: true, apply: func(s *scenario) {
			s.request.Action = ActionRun
		}},
		{name: "cancel", code: progress.CodeJobRequestInvalid, phrase: "action", echo: true, apply: func(s *scenario) {
			s.request.Action = ActionCancel
		}},
		{name: "no action", code: progress.CodeJobRequestInvalid, phrase: "names no action", echo: true, apply: func(s *scenario) {
			s.request.Action = ""
		}},
		{name: "verb", code: progress.CodeJobRequestInvalid, phrase: "names verb", echo: true, apply: func(s *scenario) {
			s.request.Verb = "noop"
		}},
		{name: "env", code: progress.CodeJobRequestInvalid, phrase: "names env", echo: true, apply: func(s *scenario) {
			s.request.Env = map[string]string{"LANG": "C"}
		}},
		{name: "interactive", code: progress.CodeJobRequestInvalid, phrase: "names interactive", echo: true, apply: func(s *scenario) {
			s.request.Interactive = true
		}},
	}
}

func TestAnswerWireContractRefusals(t *testing.T) {
	t.Parallel()

	for _, f := range contractFaults() {
		t.Run(f.name, func(t *testing.T) {
			t.Parallel()

			s := contractScenario()
			if f.apply != nil {
				f.apply(s)
			}

			var st *ownerrecord.DirStore
			if f.store != nil {
				st = f.store(t)
			} else {
				st = s.store(t)
			}

			sr := s.signed(t)

			out, err := answer(t, st, requestLine(t, sr), s.now)
			ref := wantRefusal(t, err, f.code)

			if !strings.Contains(ref.Message, f.phrase) {
				t.Errorf("message %q does not hold %q", ref.Message, f.phrase)
			}

			line := refusalLine(t, out)
			if line.Error.Code != f.code || line.Error.Message != ref.Message {
				t.Errorf("stdout error = %+v, want %s %q", line.Error, f.code, ref.Message)
			}

			wantJobID := ""
			if f.echo {
				wantJobID = s.request.JobID
			}

			if line.JobID != wantJobID {
				t.Errorf("stdout names job %q, want %q", line.JobID, wantJobID)
			}

			if !f.echo && strings.Contains(out, "job-0001") {
				t.Errorf("stdout %q echoes the job id of a request that did not verify and decode", out)
			}

			wantNoSecrets(t, out, sr.Signature, ownerKey().Public().(ed25519.PublicKey),
				otherKey().Public().(ed25519.PublicKey), ownerKey().Seed(), sr.Payload)
			wantNoSecrets(t, err.Error(), sr.Signature, ownerKey().Public().(ed25519.PublicKey),
				otherKey().Public().(ed25519.PublicKey), ownerKey().Seed(), sr.Payload)
			wantUnlocked(t, st)
		})
	}
}

// Nothing from a payload whose signature does not verify reaches stdout,
// whatever it holds.
func TestAnswerWireContractEchoesNothingUnverified(t *testing.T) {
	t.Parallel()

	marker := "unverified-marker-0001"
	req := contractRequest()
	req.JobID = marker
	req.Org = marker

	for _, sr := range []SignedRequest{
		signPayload(mustMarshal(t, req), otherKey()),
		{Format: RequestFormatV1, Payload: mustMarshal(t, req), Signature: nil},
		signPayload([]byte(`{"jobId":"`+marker+`"`), otherKey()),
	} {
		st := storeWith(t, liveRecord())

		out, err := answer(t, st, requestLine(t, sr), testNow)
		wantRefusal(t, err, progress.CodeRequestUnsigned)

		if strings.Contains(out, marker) || strings.Contains(err.Error(), marker) {
			t.Fatalf("stdout %q or error %v echoes the unverified payload", out, err)
		}

		if line := refusalLine(t, out); line.JobID != "" {
			t.Fatalf("an unverified request names job %q", line.JobID)
		}

		wantUnlocked(t, st)
	}

	// A payload that verifies but does not decode names no job either.
	st := storeWith(t, liveRecord())

	out, err := answer(t, st, requestLine(t, signPayload([]byte(`{"jobId":"`+marker+`","jobId":"x"}`), ownerKey())), testNow)
	wantRefusal(t, err, progress.CodeJobRequestInvalid)

	if line := refusalLine(t, out); line.JobID != "" {
		t.Fatalf("a payload that does not decode names job %q", line.JobID)
	}

	wantUnlocked(t, st)
}

// The store passed in may still fall back to the legacy file, but the query
// never does.
func TestAnswerWireContractIgnoresTheLegacyRecord(t *testing.T) {
	t.Parallel()

	st := legacyOnlyStore(t, liveRecord())
	s := contractScenario()

	out, err := answer(t, st, requestLine(t, s.signed(t)), s.now)
	wantRefusal(t, err, progress.CodeOwnerRecordMissing)

	if line := refusalLine(t, out); line.JobID != "" {
		t.Fatalf("a refusal for a missing record names job %q", line.JobID)
	}

	// A run still reads the same store's legacy record.
	run := baseScenario()

	_, release, err := checkOwner(t.Context(), run.signed(t), st, fixedClock{run.now})
	if err != nil {
		t.Fatalf("checkOwner over the legacy record = %v", err)
	}

	if err := release(); err != nil {
		t.Fatal(err)
	}

	wantUnlocked(t, st)
}

// A request line that cannot be read as a signed request is refused before
// anything is verified, names no job, and writes back none of the line.
func TestAnswerWireContractRefusesABadLine(t *testing.T) {
	t.Parallel()

	const marker = "unsigned-marker-0001"

	for name, line := range map[string]string{
		"not JSON":      "{" + marker + "\n",
		"unknown field": `{"format":1,"payload":"","signature":"","` + marker + `":1}` + "\n",
		"job id field":  `{"format":1,"payload":"","signature":"","jobId":"` + marker + `"}` + "\n",
		"too long":      marker + strings.Repeat("x", maxRequestLine+1),
	} {
		st := storeWith(t, liveRecord())

		out, err := answer(t, st, strings.NewReader(line), testNow)
		wantRefusal(t, err, progress.CodeJobRequestInvalid)

		if l := refusalLine(t, out); l.JobID != "" {
			t.Fatalf("%s: a bad line names job %q", name, l.JobID)
		}

		if strings.Contains(out, marker) || strings.Contains(err.Error(), marker) {
			t.Fatalf("%s: the refusal writes back the unsigned line: %q", name, out)
		}

		wantUnlocked(t, st)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("stdin is gone") }

// A failure that is not a refusal writes nothing.
func TestAnswerWireContractFailures(t *testing.T) {
	t.Parallel()

	st := storeWith(t, liveRecord())

	for name, stdin := range map[string]io.Reader{
		"empty stdin":      strings.NewReader(""),
		"unreadable stdin": failingReader{},
	} {
		out, err := answer(t, st, stdin, testNow)
		if err == nil {
			t.Fatalf("%s: got no error", name)
		}

		if _, ok := progress.AsRefusal(err); ok {
			t.Fatalf("%s: error %v is a refusal", name, err)
		}

		if out != "" {
			t.Fatalf("%s: stdout = %q, want nothing", name, out)
		}
	}

	wantUnlocked(t, st)

	for name, o := range map[string]ContractOptions{
		"no store":  {Stdin: strings.NewReader("x\n"), Stdout: io.Discard},
		"no stdin":  {Owner: st, Stdout: io.Discard},
		"no stdout": {Owner: st, Stdin: strings.NewReader("x\n")},
	} {
		err := AnswerWireContract(t.Context(), o)
		if err == nil {
			t.Fatalf("%s: got no error", name)
		}

		if _, ok := progress.AsRefusal(err); ok {
			t.Fatalf("%s: error %v is a refusal", name, err)
		}
	}
}

// A run reads exactly one line, so the query must too.
func TestAnswerWireContractReadsOneLine(t *testing.T) {
	t.Parallel()

	s := contractScenario()
	st := s.store(t)
	rest := "the rest of stdin\n"
	stdin := bytes.NewReader(append(append(mustMarshal(t, s.signed(t)), '\n'), rest...))

	if _, err := answer(t, st, stdin, s.now); err != nil {
		t.Fatal(err)
	}

	left, err := io.ReadAll(stdin)
	if err != nil {
		t.Fatal(err)
	}

	if string(left) != rest {
		t.Fatalf("the query left %q on stdin, want %q", left, rest)
	}
}

// job exec takes no wire contract query.
func TestCheckOwnerRefusesAWireContractQuery(t *testing.T) {
	t.Parallel()

	s := contractScenario()

	st, _, _, release, err := s.check(t)
	ref := wantRefusal(t, err, progress.CodeJobRequestInvalid)

	if !strings.Contains(ref.Message, ActionWireContract) {
		t.Errorf("message %q does not name the action", ref.Message)
	}

	if release != nil {
		t.Error("a refusal returned a release")
	}

	wantUnlocked(t, st)

	root := spoolRoot(t)
	rst := storeWith(t, liveRecord())
	rc := newRun(t, rst, root, signedRequest(t, contractRequest()))

	wantRefusal(t, rc.run(t), progress.CodeJobRequestInvalid)
	wantRefusalStream(t, rc.stdout.Bytes(), progress.CodeJobRequestInvalid, "")

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}

	if len(entries) != 0 {
		t.Fatalf("a refused query left %d entries under the spool root", len(entries))
	}

	wantUnlocked(t, rst)
}
