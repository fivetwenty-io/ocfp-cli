package enroll

import (
	"encoding/base64"
	"strings"
	"testing"
)

// addressVector pins the enrollment address and every subject it
// builds.
type addressVector struct {
	Address        string            `json:"address"`
	Payload        string            `json:"payload"`
	WorkerKey      string            `json:"workerKey"`
	RequestSubject string            `json:"requestSubject"`
	InboxPrefix    string            `json:"inboxPrefix"`
	ReleaseInbox   string            `json:"releaseInbox"`
	Release        map[string]string `json:"release"`
	ApprovalLink   string            `json:"approvalLink"`
}

func TestAddressGolden(t *testing.T) {
	t.Parallel()

	a := goldenAddress(t)

	s, err := a.Encode()
	if err != nil {
		t.Fatal(err)
	}

	payload, err := a.payload()
	if err != nil {
		t.Fatal(err)
	}

	key := nkeyPublic(t, nkeyUser, 0)
	v := addressVector{Address: s, Payload: string(payload), WorkerKey: key, Release: map[string]string{}}

	v.RequestSubject, err = a.RequestSubject(goldenBloc, key)
	if err != nil {
		t.Fatal(err)
	}

	v.InboxPrefix, err = a.InboxPrefix(key)
	if err != nil {
		t.Fatal(err)
	}

	v.ReleaseInbox, err = a.ReleaseInbox(goldenBloc, goldenWorker)
	if err != nil {
		t.Fatal(err)
	}

	for _, op := range []string{ReleaseManifest, ReleaseChunk, ReleaseStatus} {
		v.Release[op], err = a.ReleaseSubject(goldenBloc, goldenWorker, op)
		if err != nil {
			t.Fatal(err)
		}
	}

	d, err := HostKeyDigest(hostKeyLine(t, 0))
	if err != nil {
		t.Fatal(err)
	}

	v.ApprovalLink, err = a.ApprovalLink(goldenBloc, Fingerprint(d))
	if err != nil {
		t.Fatal(err)
	}

	checkGoldenJSON(t, "address.json", v)
	checkGolden(t, "address.txt", []byte(s+"\n"))

	back, err := ParseAddress(string(readTestdata(t, "address.txt")))
	if err != nil {
		t.Fatal(err)
	}

	if back != a {
		t.Errorf("ParseAddress = %+v, want %+v", back, a)
	}

	bloc, k, err := a.ParseRequestSubject(v.RequestSubject)
	if err != nil || bloc != goldenBloc || k != key {
		t.Errorf("ParseRequestSubject = %q, %q, %v", bloc, k, err)
	}
}

func TestAddressWithoutPin(t *testing.T) {
	t.Parallel()

	a := goldenAddress(t)
	a.CAPin = ""

	s, err := a.Encode()
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(s, "caPin") {
		t.Error("an address with no pin names one")
	}

	back, err := ParseAddress(s)
	if err != nil || back != a {
		t.Errorf("ParseAddress = %+v, %v", back, err)
	}
}

func TestParseAddressRefuses(t *testing.T) {
	t.Parallel()

	good := goldenAddress(t).String()
	_, enc, _ := strings.Cut(good, addressPath)
	prefix := "https://console.example.com" + addressPath

	withPayload := func(p string) string { return prefix + base64.RawURLEncoding.EncodeToString([]byte(p)) }
	raw, _ := goldenAddress(t).payload()
	payload := string(raw)

	cases := map[string]string{
		"empty":            "",
		"http":             "http://" + strings.TrimPrefix(good, "https://"),
		"no fragment":      "https://console.example.com/enroll/v1/" + enc,
		"padded":           good + "==",
		"not base64url":    prefix + "!!!",
		"too long":         good + strings.Repeat("A", MaxAddress),
		"unknown field":    withPayload(strings.Replace(payload, `"version":1`, `"version":1,"extra":true`, 1)),
		"spaced":           withPayload(strings.Replace(payload, `"version":1`, `"version": 1`, 1)),
		"trailing":         withPayload(payload + " {}"),
		"version 2":        withPayload(strings.Replace(payload, `"version":1`, `"version":2`, 1)),
		"ws bus":           withPayload(strings.Replace(payload, "wss://", "ws://", 1)),
		"bad installation": withPayload(strings.Replace(payload, goldenInstallation, "inst-0123456789abcdei", 1)),
		"bad org":          withPayload(strings.Replace(payload, `"org":"acme"`, `"org":"AC"`, 1)),
		"user key":         withPayload(strings.Replace(payload, goldenAddress(t).AdmissionAccount, nkeyPublic(t, nkeyUser, 2), 1)),
		"wildcard root":    withPayload(strings.Replace(payload, "example.acme.svc.join.v1", "example.*.join.v1", 1)),
		"bad pin":          withPayload(strings.Replace(payload, "6db5e9b8", "6DB5E9B8", 1)),
		"host with path":   "https://console.example.com/x" + addressPath + enc,
		"host with user":   "https://me@console.example.com" + addressPath + enc,
	}
	for name, s := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if a, err := ParseAddress(s); CodeOf(err) != CodeAddressInvalid {
				t.Errorf("ParseAddress = %+v, %v, want %s", a, err, CodeAddressInvalid)
			}
		})
	}

	if _, err := ParseAddress("  " + good + "\n"); err != nil {
		t.Errorf("surrounding white space: %v", err)
	}
}

func TestSubjectsRefuseBadNames(t *testing.T) {
	t.Parallel()

	a := goldenAddress(t)
	key := nkeyPublic(t, nkeyUser, 0)
	acct := nkeyPublic(t, nkeyAccount, 0)

	for _, c := range []struct{ bloc, key string }{
		{"acme", key}, {goldenBloc, acct}, {goldenBloc + ".x", key}, {goldenBloc, "*"}, {"ocfp-" + strings.Repeat("a", 40), key},
	} {
		if _, err := a.RequestSubject(c.bloc, c.key); CodeOf(err) != CodeRequestInvalid {
			t.Errorf("RequestSubject(%q, %q) = %v", c.bloc, c.key, err)
		}
	}

	for _, s := range []string{
		"other.root." + goldenBloc + "." + key,
		a.RequestRoot + "." + goldenBloc,
		a.RequestRoot + "." + goldenBloc + "." + key + ".x",
		a.RequestRoot + ".acme." + key,
	} {
		if _, _, err := a.ParseRequestSubject(s); CodeOf(err) != CodeRequestInvalid {
			t.Errorf("ParseRequestSubject(%q) = %v", s, err)
		}
	}

	if _, err := a.ReleaseSubject(goldenBloc, goldenWorker, "upload"); CodeOf(err) != CodeRequestInvalid {
		t.Errorf("an unknown release operation: %v", err)
	}

	if _, err := a.ReleaseSubject(goldenBloc, "W.1", ReleaseChunk); CodeOf(err) != CodeRequestInvalid {
		t.Errorf("a bad worker id: %v", err)
	}

	if _, err := a.ReleaseInbox("x", goldenWorker); CodeOf(err) != CodeRequestInvalid {
		t.Errorf("a bad bloc: %v", err)
	}

	if _, err := a.ApprovalLink(goldenBloc, "AIOW-ADSS-GVKI"); CodeOf(err) != CodeRequestInvalid {
		t.Errorf("an approval link with a code in place of a fingerprint: %v", err)
	}

	prefix, _ := a.InboxPrefix(key)
	if !a.ReplyValid(prefix+".abc", key) || a.ReplyValid(prefix, key) || a.ReplyValid(prefix+"x.abc", key) ||
		a.ReplyValid(prefix+".abc", nkeyPublic(t, nkeyUser, 1)) {
		t.Error("ReplyValid does not hold replies to the key's own prefix")
	}
}

func TestKeyForms(t *testing.T) {
	t.Parallel()

	user, acct := nkeyPublic(t, nkeyUser, 0), nkeyPublic(t, nkeyAccount, 0)
	if !strings.HasPrefix(user, "U") || !strings.HasPrefix(acct, "A") {
		t.Fatalf("user %s, account %s", user, acct)
	}

	if !ValidUserKey(user) || ValidUserKey(acct) || !ValidAccountKey(acct) || ValidAccountKey(user) {
		t.Error("the key forms are crossed")
	}

	flipped := []byte(user)
	flipped[10] ^= 1

	if ValidUserKey(string(flipped)) || ValidUserKey(user[:55]) || ValidUserKey("") {
		t.Error("a broken user key passed")
	}
}
