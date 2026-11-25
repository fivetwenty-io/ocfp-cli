package sigchain

import (
	"bytes"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// updateGolden rewrites the golden files from the code when set. A
// change to a golden file is a change to the wire contract, so only a
// deliberate change to the format runs with it.
var updateGolden = os.Getenv("SIGCHAIN_UPDATE_GOLDEN") == "1"

var (
	goldenNotBefore = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	goldenNotAfter  = time.Date(2027, 1, 31, 0, 0, 0, 0, time.UTC)
	goldenNow       = time.Date(2026, 12, 1, 12, 0, 0, 0, time.UTC)
)

func signDoc(t testing.TB, key ed25519.PrivateKey, ns string, b []byte) Signed {
	t.Helper()

	sig, err := Sign(key, ns, b)
	if err != nil {
		t.Fatal(err)
	}

	return Signed{Bytes: b, Signature: sig}
}

func statementFor(t testing.TB, kind Kind, root, online ed25519.PrivateKey, serial uint64, from, to time.Time) Signed {
	t.Helper()

	st := Statement{
		Domain: kind.StatementDomain, Key: KeyLine(sshPub(t, online)), Serial: serial,
		NotBefore: from, NotAfter: to,
	}

	b, err := st.Canonical()
	if err != nil {
		t.Fatal(err)
	}

	return signDoc(t, root, kind.StatementDomain, b)
}

func revocationFor(t testing.TB, root ed25519.PrivateKey, seq uint64, serials ...uint64) Signed {
	t.Helper()

	if serials == nil {
		serials = []uint64{}
	}

	r := Revocation{Domain: RevocationDomain, Sequence: seq, SignedAt: goldenNotBefore, Serials: serials}

	b, err := r.Canonical()
	if err != nil {
		t.Fatal(err)
	}

	return signDoc(t, root, RevocationDomain, b)
}

// goldenSet is the chain the golden files hold: root RFC 8032 key 1,
// online key RFC 8032 key 2 with serial 1, a payload, and a revocation
// of sequence 2 that withdraws serial 5.
func goldenSet(t testing.TB) (Trust, Set) {
	t.Helper()

	root, online := rfcKey(t, 0), rfcKey(t, 1)
	payload := []byte("bloc worker manifest bytes\n")

	return Trust{Roots: []ssh.PublicKey{sshPub(t, root)}}, Set{
		Statement:   statementFor(t, WorkerManifest, root, online, 1, goldenNotBefore, goldenNotAfter),
		Payload:     signDoc(t, online, WorkerManifest.PayloadNamespace, payload),
		Revocations: []Signed{revocationFor(t, root, 2, 5)},
	}
}

func checkGolden(t *testing.T, name string, got []byte) {
	t.Helper()

	path := filepath.Join("testdata", name)
	if updateGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}

		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from the code's output:\ngot  %q\nwant %q", name, got, want)
	}
}

// TestGoldenChain pins every format of the chain to its golden file:
// the root key, the statement of each kind and its signature, the
// payload's signature, and the revocation and its signature. Ed25519
// signatures are deterministic, so the files are stable.
func TestGoldenChain(t *testing.T) {
	t.Parallel()

	trust, set := goldenSet(t)
	root, online := rfcKey(t, 0), rfcKey(t, 1)
	release := statementFor(t, CLIRelease, root, online, 2, goldenNotBefore, goldenNotAfter)

	checkGolden(t, "chain/root.pub", []byte(KeyLine(trust.Roots[0])+"\n"))
	checkGolden(t, "chain/worker-signing-key.json", set.Statement.Bytes)
	checkGolden(t, "chain/worker-signing-key.json.sig", set.Statement.Signature)
	checkGolden(t, "chain/release-signing-key.json", release.Bytes)
	checkGolden(t, "chain/release-signing-key.json.sig", release.Signature)
	checkGolden(t, "chain/payload.txt", set.Payload.Bytes)
	checkGolden(t, "chain/payload.txt.sig", set.Payload.Signature)
	checkGolden(t, "chain/signing-key-revocation.json", set.Revocations[0].Bytes)
	checkGolden(t, "chain/signing-key-revocation.json.sig", set.Revocations[0].Signature)
}

// TestGoldenChainVerifiesFromFiles checks the chain as a caller does,
// from the golden files alone.
func TestGoldenChainVerifiesFromFiles(t *testing.T) {
	t.Parallel()

	roots, err := ParseRoots(string(readTestdata(t, "chain/root.pub")))
	if err != nil {
		t.Fatal(err)
	}

	pair := func(name string) Signed {
		return Signed{Bytes: readTestdata(t, "chain/"+name), Signature: readTestdata(t, "chain/"+name+".sig")}
	}
	set := Set{
		Statement:   pair("worker-signing-key.json"),
		Payload:     pair("payload.txt"),
		Revocations: []Signed{pair("signing-key-revocation.json")},
	}

	res, err := Verify(WorkerManifest, Trust{Roots: roots}, set, goldenNow)
	if err != nil {
		t.Fatalf("Verify = %v", err)
	}

	if res.Statement.Serial != 1 || !res.HasRevocation || res.Revocation.Sequence != 2 {
		t.Errorf("Verify found %+v", res)
	}

	set.Statement = pair("release-signing-key.json")
	if _, err := Verify(CLIRelease, Trust{Roots: roots}, set, goldenNow); CodeOf(err) != CodeSignatureInvalid {
		t.Errorf("a release key verified a worker manifest: %v", err)
	}
}

func TestVerifyStepsAndCodes(t *testing.T) {
	t.Parallel()

	root, online, other := rfcKey(t, 0), rfcKey(t, 1), rfcKey(t, 2)

	cases := map[string]struct {
		edit func(*Trust, *Set)
		now  time.Time
		code string
	}{
		"good":     {func(*Trust, *Set) {}, goldenNow, ""},
		"no roots": {func(tr *Trust, _ *Set) { tr.Roots = nil }, goldenNow, CodeSignatureInvalid},
		"statement not signed by a root": {func(_ *Trust, s *Set) {
			s.Statement = statementFor(t, WorkerManifest, other, online, 1, goldenNotBefore, goldenNotAfter)
		}, goldenNow, CodeSignatureInvalid},
		"statement of the other kind": {func(_ *Trust, s *Set) {
			s.Statement = statementFor(t, CLIRelease, root, online, 1, goldenNotBefore, goldenNotAfter)
		}, goldenNow, CodeSignatureInvalid},
		"statement signed under the wrong namespace": {func(_ *Trust, s *Set) {
			s.Statement = signDoc(t, root, CLIRelease.StatementDomain, s.Statement.Bytes)
		}, goldenNow, CodeSignatureInvalid},
		"statement not canonical": {func(_ *Trust, s *Set) {
			b := append([]byte(" "), s.Statement.Bytes...)
			s.Statement = signDoc(t, root, WorkerManifest.StatementDomain, b)
		}, goldenNow, CodeSignatureInvalid},
		"revoked in the set": {func(_ *Trust, s *Set) {
			s.Revocations = append(s.Revocations, revocationFor(t, root, 3, 1, 5))
		}, goldenNow, CodeSigningKeyRevoked},
		"revoked in the trust": {func(tr *Trust, _ *Set) {
			tr.Revocations = []Signed{revocationFor(t, root, 9, 1)}
		}, goldenNow, CodeSigningKeyRevoked},
		"older revocation lists it, newer does not": {func(tr *Trust, _ *Set) {
			tr.Revocations = []Signed{revocationFor(t, root, 1, 1)}
		}, goldenNow, ""},
		"revocation not signed by a root is passed over": {func(_ *Trust, s *Set) {
			s.Revocations = []Signed{revocationFor(t, other, 50, 1)}
		}, goldenNow, ""},
		"revocation signed by the online key is passed over": {func(_ *Trust, s *Set) {
			s.Revocations = []Signed{revocationFor(t, online, 50, 1)}
		}, goldenNow, ""},
		"payload by another key": {func(_ *Trust, s *Set) {
			s.Payload = signDoc(t, other, WorkerManifest.PayloadNamespace, s.Payload.Bytes)
		}, goldenNow, CodeSignatureInvalid},
		"payload under the release namespace": {func(_ *Trust, s *Set) {
			s.Payload = signDoc(t, online, CLIRelease.PayloadNamespace, s.Payload.Bytes)
		}, goldenNow, CodeSignatureInvalid},
		"payload changed": {func(_ *Trust, s *Set) {
			s.Payload.Bytes = append([]byte{}, "other bytes"...)
		}, goldenNow, CodeSignatureInvalid},
		"window of 93 days": {func(_ *Trust, s *Set) {
			s.Statement = statementFor(t, WorkerManifest, root, online, 1, goldenNotBefore, goldenNotBefore.Add(93*24*time.Hour))
		}, goldenNow, CodeSigningKeyWindowInvalid},
		"window of exactly 92 days": {func(_ *Trust, s *Set) {
			s.Statement = statementFor(t, WorkerManifest, root, online, 1, goldenNotBefore, goldenNotBefore.Add(MaxWindow))
		}, goldenNow, ""},
		"within a day after the end":       {func(*Trust, *Set) {}, goldenNotAfter.Add(23 * time.Hour), ""},
		"more than a day after the end":    {func(*Trust, *Set) {}, goldenNotAfter.Add(25 * time.Hour), CodeSignatureExpired},
		"within a day before the start":    {func(*Trust, *Set) {}, goldenNotBefore.Add(-23 * time.Hour), ""},
		"more than a day before the start": {func(*Trust, *Set) {}, goldenNotBefore.Add(-25 * time.Hour), CodeSignatureExpired},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			trust, set := goldenSet(t)
			c.edit(&trust, &set)

			_, err := Verify(WorkerManifest, trust, set, c.now)
			if got := CodeOf(err); got != c.code {
				t.Errorf("Verify = %v, want code %q", err, c.code)
			}
		})
	}
}

// TestVerifyChecksRevocationBeforeThePayload holds the step order: a
// revoked key is refused as revoked even when its payload's signature is
// also bad, and a bad payload is refused before a long window.
func TestVerifyChecksRevocationBeforeThePayload(t *testing.T) {
	t.Parallel()

	root, online, other := rfcKey(t, 0), rfcKey(t, 1), rfcKey(t, 2)
	trust, set := goldenSet(t)
	set.Revocations = []Signed{revocationFor(t, root, 3, 1)}
	set.Payload = signDoc(t, other, WorkerManifest.PayloadNamespace, set.Payload.Bytes)

	if _, err := Verify(WorkerManifest, trust, set, goldenNow); CodeOf(err) != CodeSigningKeyRevoked {
		t.Errorf("Verify = %v, want the revocation first", err)
	}

	trust, set = goldenSet(t)
	set.Statement = statementFor(t, WorkerManifest, root, online, 1, goldenNotBefore, goldenNotBefore.Add(200*24*time.Hour))
	set.Payload = signDoc(t, other, WorkerManifest.PayloadNamespace, set.Payload.Bytes)

	if _, err := Verify(WorkerManifest, trust, set, goldenNow); CodeOf(err) != CodeSignatureInvalid {
		t.Errorf("Verify = %v, want the payload's signature before the window", err)
	}
}

// TestVerifyExpiredNamesTheEndAndTheClock gives a report both the
// window's end and the clock reading, so an operator can tell a lapsed
// signature from a wrong clock.
func TestVerifyExpiredNamesTheEndAndTheClock(t *testing.T) {
	t.Parallel()

	trust, set := goldenSet(t)
	now := goldenNotAfter.Add(72 * time.Hour)

	_, err := Verify(WorkerManifest, trust, set, now)

	e, ok := err.(*Error)
	if !ok || e.Code != CodeSignatureExpired {
		t.Fatalf("Verify = %v", err)
	}

	if !e.NotAfter.Equal(goldenNotAfter) || !e.Clock.Equal(now) {
		t.Errorf("the refusal names %s and %s", e.NotAfter, e.Clock)
	}

	if !strings.Contains(e.Error(), "2027-01-31") || !strings.Contains(e.Error(), "2027-02-03") {
		t.Errorf("the detail %q names neither the end nor the clock", e.Error())
	}
}

func TestNewestRevocation(t *testing.T) {
	t.Parallel()

	root, other := rfcKey(t, 0), rfcKey(t, 2)
	roots := []ssh.PublicKey{sshPub(t, root)}

	if _, _, ok := NewestRevocation(roots, nil); ok {
		t.Error("found a revocation among none")
	}

	r1, r3, forged := revocationFor(t, root, 1, 4), revocationFor(t, root, 3, 4, 6), revocationFor(t, other, 99)

	got, signed, ok := NewestRevocation(roots, []Signed{r1, forged, r3, {Bytes: []byte("{}"), Signature: r1.Signature}})
	if !ok || got.Sequence != 3 || !bytes.Equal(signed.Bytes, r3.Bytes) {
		t.Errorf("NewestRevocation = %+v, %v", got, ok)
	}

	tie := revocationFor(t, root, 3, 4)

	got, _, _ = NewestRevocation(roots, []Signed{tie, r3})
	if len(got.Serials) != 2 {
		t.Errorf("a tie kept %v, want the longer list", got.Serials)
	}
}

func TestStatementAndRevocationForms(t *testing.T) {
	t.Parallel()

	key := KeyLine(sshPub(t, rfcKey(t, 1)))
	good := Statement{Domain: "d", Key: key, Serial: 1, NotBefore: goldenNotBefore, NotAfter: goldenNotAfter}

	bads := map[string]Statement{
		"no domain":   {Key: key, Serial: 1, NotBefore: goldenNotBefore, NotAfter: goldenNotAfter},
		"serial zero": {Domain: "d", Key: key, NotBefore: goldenNotBefore, NotAfter: goldenNotAfter},
		"backwards":   {Domain: "d", Key: key, Serial: 1, NotBefore: goldenNotAfter, NotAfter: goldenNotBefore},
		"not UTC":     {Domain: "d", Key: key, Serial: 1, NotBefore: goldenNotBefore.In(time.FixedZone("x", 3600)), NotAfter: goldenNotAfter},
		"fraction":    {Domain: "d", Key: key, Serial: 1, NotBefore: goldenNotBefore.Add(time.Millisecond), NotAfter: goldenNotAfter},
		"key comment": {Domain: "d", Key: key + " c", Serial: 1, NotBefore: goldenNotBefore, NotAfter: goldenNotAfter},
		"not a key":   {Domain: "d", Key: "ssh-ed25519 AAAA", Serial: 1, NotBefore: goldenNotBefore, NotAfter: goldenNotAfter},
	}
	for name, s := range bads {
		if _, err := s.Canonical(); err == nil {
			t.Errorf("%s: Canonical took it", name)
		}
	}

	b, err := good.Canonical()
	if err != nil {
		t.Fatal(err)
	}

	if _, err := ParseStatement(b, "d"); err != nil {
		t.Errorf("ParseStatement = %v", err)
	}

	for name, in := range map[string][]byte{
		"other domain":  b,
		"unknown field": bytes.Replace(b, []byte(`{"domain"`), []byte(`{"x":1,"domain"`), 1),
		"trailing":      append(append([]byte{}, b...), ' '),
		"spaced":        bytes.Replace(b, []byte(`,"key"`), []byte(`, "key"`), 1),
		"empty":         nil,
	} {
		domain := "d"
		if name == "other domain" {
			domain = "e"
		}

		if _, err := ParseStatement(in, domain); err == nil {
			t.Errorf("ParseStatement took %s", name)
		}
	}

	for name, r := range map[string]Revocation{
		"domain":    {Domain: "x", Sequence: 1, SignedAt: goldenNotBefore, Serials: []uint64{}},
		"sequence":  {Domain: RevocationDomain, SignedAt: goldenNotBefore, Serials: []uint64{}},
		"nil list":  {Domain: RevocationDomain, Sequence: 1, SignedAt: goldenNotBefore},
		"unsorted":  {Domain: RevocationDomain, Sequence: 1, SignedAt: goldenNotBefore, Serials: []uint64{3, 2}},
		"repeated":  {Domain: RevocationDomain, Sequence: 1, SignedAt: goldenNotBefore, Serials: []uint64{2, 2}},
		"serial 0":  {Domain: RevocationDomain, Sequence: 1, SignedAt: goldenNotBefore, Serials: []uint64{0}},
		"zero time": {Domain: RevocationDomain, Sequence: 1, Serials: []uint64{}},
	} {
		if _, err := r.Canonical(); err == nil {
			t.Errorf("revocation %s: Canonical took it", name)
		}
	}

	if _, err := ParseRevocation(bytes.Repeat([]byte(" "), MaxRevocation+1)); err == nil {
		t.Error("ParseRevocation took an oversized revocation")
	}
}

func TestEmbeddedTrustParses(t *testing.T) {
	t.Parallel()

	tr, err := Embedded()
	if err != nil {
		t.Fatalf("Embedded = %v", err)
	}

	if len(tr.Roots) > MaxRoots {
		t.Errorf("%d embedded roots", len(tr.Roots))
	}

	// With no root embedded, every chain fails at its first step rather
	// than passing.
	_, set := goldenSet(t)
	if len(tr.Roots) == 0 {
		if _, err := Verify(WorkerManifest, tr, set, goldenNow); CodeOf(err) != CodeSignatureInvalid {
			t.Errorf("Verify with no embedded root = %v", err)
		}
	}
}

func TestParseRoots(t *testing.T) {
	t.Parallel()

	k := []string{KeyLine(sshPub(t, rfcKey(t, 0))), KeyLine(sshPub(t, rfcKey(t, 1))), KeyLine(sshPub(t, rfcKey(t, 2)))}

	roots, err := ParseRoots("# comment\n\n" + k[0] + " the root\n" + k[1] + "\n")
	if err != nil || len(roots) != 2 {
		t.Errorf("ParseRoots = %d roots, %v", len(roots), err)
	}

	for name, text := range map[string]string{
		"three":  strings.Join(k, "\n"),
		"twice":  k[0] + "\n" + k[0],
		"rsa":    "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAAAgQC7",
		"broken": "ssh-ed25519 !!!",
	} {
		if _, err := ParseRoots(text); err == nil {
			t.Errorf("ParseRoots took %s", name)
		}
	}
}
