package enroll

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

var goldenCreatedAt = time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)

func goldenAttestation(t testing.TB) Attestation {
	t.Helper()

	a, err := NewAttestation(goldenInstallation, goldenOrg, goldenBloc,
		nkeyPublic(t, nkeyUser, 1), nkeyPublic(t, nkeyUser, 2), hostKeyLine(t, 0)+" root@bastion", goldenCreatedAt)
	if err != nil {
		t.Fatal(err)
	}

	return a
}

func goldenRequest(t testing.TB) Request {
	t.Helper()

	r, err := SignAttestation(rfcKey(t, 0), goldenAttestation(t))
	if err != nil {
		t.Fatal(err)
	}

	return r
}

// requestVector pins the attestation, the signed request, and its id.
type requestVector struct {
	Attestation string `json:"attestation"`
	Request     string `json:"request"`
	RequestID   string `json:"requestId"`
	Signature   string `json:"signature"`
}

func TestRequestGolden(t *testing.T) {
	t.Parallel()

	r := goldenRequest(t)

	body, err := r.Encode()
	if err != nil {
		t.Fatal(err)
	}

	checkGolden(t, "attestation.json", r.Attestation)
	checkGolden(t, "attestation.json.sig", []byte(r.Signature))
	checkGoldenJSON(t, "request.json", requestVector{
		Attestation: string(r.Attestation), Request: string(body), RequestID: r.ID(), Signature: r.Signature,
	})

	var v requestVector
	if err := json.Unmarshal(readTestdata(t, "request.json"), &v); err != nil {
		t.Fatal(err)
	}

	got, a, err := DecodeRequest([]byte(v.Request))
	if err != nil {
		t.Fatal(err)
	}

	if got.ID() != v.RequestID || RequestID(readTestdata(t, "attestation.json")) != v.RequestID {
		t.Errorf("request id = %s, want %s", got.ID(), v.RequestID)
	}

	if a != goldenAttestation(t) {
		t.Errorf("attestation = %+v", a)
	}

	if err := VerifyRequest(got, a); err != nil {
		t.Errorf("VerifyRequest: %v", err)
	}
}

func TestAttestationRefusesOneKeyTwice(t *testing.T) {
	t.Parallel()

	key := nkeyPublic(t, nkeyUser, 1)

	_, err := NewAttestation(goldenInstallation, goldenOrg, goldenBloc, key, key, hostKeyLine(t, 0), goldenCreatedAt)
	if CodeOf(err) != CodeRequestInvalid {
		t.Errorf("NewAttestation with one key twice = %v", err)
	}

	a := goldenAttestation(t)
	a.InstallKey = a.WorkerKey

	b, _ := canonicalJSON(a)
	if _, err := ParseAttestation(b); CodeOf(err) != CodeRequestInvalid {
		t.Errorf("ParseAttestation with one key twice = %v", err)
	}
}

func TestParseAttestationRefuses(t *testing.T) {
	t.Parallel()

	good, err := goldenAttestation(t).Canonical()
	if err != nil {
		t.Fatal(err)
	}

	s := string(good)
	mutate := func(f func(*Attestation)) string {
		a := goldenAttestation(t)
		f(&a)

		b, _ := canonicalJSON(a)

		return string(b)
	}

	cases := map[string]string{
		"empty":          "",
		"too long":       s + strings.Repeat(" ", MaxAttestation),
		"spaced":         strings.Replace(s, `"domain":`, `"domain": `, 1),
		"unknown field":  strings.Replace(s, `{"domain"`, `{"x":1,"domain"`, 1),
		"trailing":       s + "{}",
		"wrong domain":   mutate(func(a *Attestation) { a.Domain = "ocfp-bastion-join-attestation-v2" }),
		"account key":    mutate(func(a *Attestation) { a.InstallKey = nkeyPublic(t, nkeyAccount, 2) }),
		"no install key": mutate(func(a *Attestation) { a.InstallKey = "" }),
		"bad bloc":       mutate(func(a *Attestation) { a.Bloc = "acme" }),
		"bad host key":   mutate(func(a *Attestation) { a.HostKey = "ssh-ed25519" }),
		"local time":     mutate(func(a *Attestation) { a.CreatedAt = goldenCreatedAt.In(time.FixedZone("x", 3600)) }),
		"fraction":       mutate(func(a *Attestation) { a.CreatedAt = goldenCreatedAt.Add(time.Millisecond) }),
	}
	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, err := ParseAttestation([]byte(b)); CodeOf(err) != CodeRequestInvalid {
				t.Errorf("ParseAttestation = %v", err)
			}
		})
	}
}

// TestDecodeEnvelopeNamesTheRequest pins step 2 of the check order: once
// the envelope decodes, the request id is known, even when the
// attestation inside it is refused, so the refusal can name it.
func TestDecodeEnvelopeNamesTheRequest(t *testing.T) {
	t.Parallel()

	bad := Request{Attestation: []byte(`{"domain":"not an attestation"}`), Signature: "x"}

	body, err := json.Marshal(bad)
	if err != nil {
		t.Fatal(err)
	}

	_, id, err := DecodeEnvelope(body)
	if err != nil || id != RequestID(bad.Attestation) {
		t.Fatalf("DecodeEnvelope = %q, %v", id, err)
	}

	if _, _, err := DecodeRequest(body); CodeOf(err) != CodeRequestInvalid {
		t.Errorf("DecodeRequest = %v", err)
	}

	answer, err := Refused(id, CodeRequestInvalid).Encode()
	if err != nil {
		t.Fatal(err)
	}

	if a, err := DecodeAnswer(answer, id); err != nil || a.Refusal.Code != CodeRequestInvalid {
		t.Errorf("the asker cannot read its refusal: %v, %v", a, err)
	}

	for name, b := range map[string]string{
		"too long":       `{"attestation":"` + strings.Repeat("A", MaxMessage) + `","signature":"x"}`,
		"not json":       "nope",
		"unknown field":  `{"attestation":"eA==","signature":"x","x":1}`,
		"trailing":       `{"attestation":"eA==","signature":"x"}{}`,
		"no attestation": `{"signature":"x"}`,
	} {
		if _, id, err := DecodeEnvelope([]byte(b)); CodeOf(err) != CodeRequestInvalid || id != "" {
			t.Errorf("%s: DecodeEnvelope = %q, %v", name, id, err)
		}
	}

	good, _ := goldenRequest(t).Encode()
	noSig := bytes.Replace(good, []byte(`"signature":"`), []byte(`"signature":"","x":"`), 1)

	if _, _, err := DecodeRequest(noSig); CodeOf(err) != CodeRequestInvalid {
		t.Errorf("DecodeRequest with no signature = %v", err)
	}
}

func TestVerifyRequestRefuses(t *testing.T) {
	t.Parallel()

	r := goldenRequest(t)
	a := goldenAttestation(t)

	other := a
	other.HostKey = hostKeyLine(t, 1)

	if err := VerifyRequest(r, other); CodeOf(err) != CodeAttestationInvalid {
		t.Errorf("another host key: %v", err)
	}

	rsa := a
	rsa.HostKey = "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAAAgQC7"

	if err := VerifyRequest(r, rsa); CodeOf(err) != CodeHostKeyUnsupported {
		t.Errorf("an rsa host key: %v", err)
	}

	tampered := r
	tampered.Attestation = bytes.Replace(r.Attestation, []byte(goldenBloc), []byte("ocfp-acme-dev1"), 1)

	if err := VerifyRequest(tampered, a); CodeOf(err) != CodeAttestationInvalid {
		t.Errorf("a changed attestation: %v", err)
	}
}

func TestBind(t *testing.T) {
	t.Parallel()

	a := goldenAttestation(t)

	for key, want := range map[string]Asker{a.WorkerKey: AskerWorker, a.InstallKey: AskerInstall} {
		got, err := Bind(a, goldenBloc, key, goldenOrg, goldenInstallation)
		if err != nil || got != want {
			t.Errorf("Bind(%s) = %s, %v, want %s", key, got, err, want)
		}
	}

	for name, c := range map[string]struct {
		bloc, key, org, inst, code string
	}{
		"other key":          {goldenBloc, nkeyPublic(t, nkeyUser, 0), goldenOrg, goldenInstallation, CodeAttestationInvalid},
		"other org":          {goldenBloc, a.WorkerKey, "other", goldenInstallation, CodeWrongInstallation},
		"other installation": {goldenBloc, a.InstallKey, goldenOrg, "inst-0000000000000000", CodeWrongInstallation},
		"other bloc":         {"ocfp-acme-dev1", a.WorkerKey, goldenOrg, goldenInstallation, CodeBlocMismatch},
	} {
		if _, err := Bind(a, c.bloc, c.key, c.org, c.inst); CodeOf(err) != c.code {
			t.Errorf("%s: Bind = %v, want %s", name, err, c.code)
		}
	}

	if AskerWorker.String() != "worker" || AskerInstall.String() != "install" || Asker(0).String() != "none" {
		t.Error("Asker names")
	}
}

const goldenCredential = "eyJhbGciOiJlZDI1NTE5LW5rZXkifQ.eyJzdWIiOiJVIn0.c2lnbmF0dXJl"

func TestAnswerGolden(t *testing.T) {
	t.Parallel()

	id := goldenRequest(t).ID()
	exp := goldenCreatedAt.Add(15 * time.Minute)
	answers := map[string]Answer{
		"pending":  Pending(id, VerdictMatch),
		"approved": Approved(id, goldenWorker, goldenCredential, exp),
		"rejected": Rejected(id),
		"removed":  Removed(id),
		"refused":  Refused(id, CodeAlreadyEnrolled),
	}

	got := map[string]string{}

	for name, a := range answers {
		b, err := a.Encode()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}

		got[name] = string(b)
	}

	checkGoldenJSON(t, "answers.json", got)

	var want map[string]string
	if err := json.Unmarshal(readTestdata(t, "answers.json"), &want); err != nil {
		t.Fatal(err)
	}

	for name, body := range want {
		a, err := DecodeAnswer([]byte(body), id)
		if err != nil {
			t.Errorf("%s: %v", name, err)

			continue
		}

		if a.String() != answers[name].String() {
			t.Errorf("%s: %s, want %s", name, a, answers[name])
		}

		if _, err := DecodeAnswer([]byte(body), RequestID([]byte("another"))); CodeOf(err) != CodeAnswerInvalid {
			t.Errorf("%s: an answer for another request was taken: %v", name, err)
		}
	}
}

func TestDecodeAnswerRefuses(t *testing.T) {
	t.Parallel()

	id := goldenRequest(t).ID()
	exp := goldenCreatedAt

	for name, a := range map[string]Answer{
		"no request":            Refused("", CodeRequestInvalid),
		"unknown state":         {State: "lost", Request: id},
		"pending no verdict":    {State: StatePending, Request: id},
		"pending credential":    {State: StatePending, Request: id, Verdict: VerdictMatch, Credential: goldenCredential},
		"approved no worker":    {State: StateApproved, Request: id, Credential: goldenCredential, ExpiresAt: &exp},
		"approved no expiry":    {State: StateApproved, Request: id, WorkerID: goldenWorker, Credential: goldenCredential},
		"approved bad jwt":      {State: StateApproved, Request: id, WorkerID: goldenWorker, Credential: "x", ExpiresAt: &exp},
		"rejected with verdict": {State: StateRejected, Request: id, Verdict: VerdictMatch},
		"refused unknown code":  Refused(id, "JOIN_KEY_MISMATCH"),
		"refused verdict":       {State: StateRefused, Request: id, Verdict: VerdictMatch, Refusal: &Refusal{Code: CodeRateLimited}},
	} {
		b, err := json.Marshal(a)
		if err != nil {
			t.Fatal(err)
		}

		if _, err := DecodeAnswer(b, id); CodeOf(err) != CodeAnswerInvalid {
			t.Errorf("%s: DecodeAnswer = %v", name, err)
		}
	}

	if _, err := DecodeAnswer([]byte(`{}`), ""); CodeOf(err) != CodeAnswerInvalid {
		t.Error("an empty request id was taken")
	}

	long := append([]byte(`{"state":"rejected","request":"`+id+`"}`), bytes.Repeat([]byte(" "), MaxMessage)...)
	if _, err := DecodeAnswer(long, id); CodeOf(err) != CodeAnswerInvalid {
		t.Error("a long answer was taken")
	}
}

// TestAnswerWithholdsTheCredential checks every way an answer can reach a
// log or an error.
func TestAnswerWithholdsTheCredential(t *testing.T) {
	t.Parallel()

	a := Approved(goldenRequest(t).ID(), goldenWorker, goldenCredential, goldenCreatedAt)

	var logged bytes.Buffer

	slog.New(slog.NewJSONHandler(&logged, nil)).Info("answer", "answer", a)

	for _, s := range []string{
		a.String(), fmt.Sprint(a), fmt.Sprintf("%v %+v %#v %s %q", a, a, a, a, a), fmt.Sprint(&a), logged.String(),
	} {
		if strings.Contains(s, goldenCredential) || strings.Contains(s, "eyJ") {
			t.Errorf("the credential leaked: %s", s)
		}

		if !strings.Contains(s, "credential") {
			t.Errorf("the credential's key name is missing: %s", s)
		}
	}
}

func TestRefusalCodesAreKnown(t *testing.T) {
	t.Parallel()

	seen := map[string]bool{}

	for _, c := range append(append([]string{}, RefusalCodes...), ServiceCodes...) {
		if seen[c] {
			t.Errorf("%s is listed twice", c)
		}

		seen[c] = true
	}

	if !seen[CodeAlreadyEnrolled] || !seen[CodeWorkerKeyInUse] || seen["JOIN_KEY_MISMATCH"] {
		t.Error("the refusal codes are not the spec's set")
	}
}
