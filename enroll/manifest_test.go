package enroll

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fivetwenty-io/ocfp-cli/sigchain"
	"golang.org/x/crypto/ssh"
)

var (
	setNotBefore = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	setNotAfter  = time.Date(2027, 1, 31, 0, 0, 0, 0, time.UTC)
	setNow       = time.Date(2026, 12, 1, 12, 0, 0, 0, time.UTC)
)

const (
	goldenChunkSize = 256
	goldenVersion   = "1.4.0"
	goldenMinimum   = "1.2.0"
)

// goldenBinary is a stand-in worker binary for arch: 1,000 bytes, which
// is three whole chunks and one short one at the golden chunk size.
func goldenBinary(arch string) []byte {
	b := make([]byte, 1000)
	for i := range b {
		b[i] = byte(i*7+i/256*13) ^ arch[len(arch)-1]
	}

	return b
}

func binaryFor(arch string, data []byte, chunk int64) Binary {
	sum := sha256.Sum256(data)
	b := Binary{Arch: arch, Name: "bloc-worker-linux-" + arch, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:]), ChunkSize: chunk}

	for off := int64(0); off < int64(len(data)); off += chunk {
		c := sha256.Sum256(data[off:min(off+chunk, int64(len(data)))])
		b.Chunks = append(b.Chunks, hex.EncodeToString(c[:]))
	}

	return b
}

func goldenManifest() Manifest {
	return Manifest{
		Domain: ManifestDomain, Version: goldenVersion, MinimumVersion: goldenMinimum,
		BuildTime: time.Date(2026, 11, 20, 9, 30, 0, 0, time.UTC),
		Binaries: []Binary{
			binaryFor("amd64", goldenBinary("amd64"), goldenChunkSize),
			binaryFor("arm64", goldenBinary("arm64"), goldenChunkSize),
		},
	}
}

func sign(t testing.TB, key ed25519.PrivateKey, ns string, b []byte) []byte {
	t.Helper()

	sig, err := sigchain.Sign(key, ns, b)
	if err != nil {
		t.Fatal(err)
	}

	return sig
}

func statementBytes(t testing.TB, online ed25519.PrivateKey, serial uint64, from, to time.Time) []byte {
	t.Helper()

	pub, err := ssh.NewPublicKey(online.Public())
	if err != nil {
		t.Fatal(err)
	}

	b, err := sigchain.Statement{
		Domain: sigchain.WorkerManifest.StatementDomain, Key: sigchain.KeyLine(pub), Serial: serial, NotBefore: from, NotAfter: to,
	}.Canonical()
	if err != nil {
		t.Fatal(err)
	}

	return b
}

func revocationSigned(t testing.TB, root ed25519.PrivateKey, seq uint64, serials ...uint64) sigchain.Signed {
	t.Helper()

	b, err := sigchain.Revocation{Domain: sigchain.RevocationDomain, Sequence: seq, SignedAt: setNotBefore, Serials: serials}.Canonical()
	if err != nil {
		t.Fatal(err)
	}

	return sigchain.Signed{Bytes: b, Signature: sign(t, root, sigchain.RevocationDomain, b)}
}

// buildSet signs m with online key 1 under a statement that root key 0
// signs, with a revocation of sequence 2 that withdraws serial 5.
func buildSet(t testing.TB, m Manifest, serial uint64, from, to time.Time) ManifestSet {
	t.Helper()

	root, online := rfcKey(t, 0), rfcKey(t, 1)

	mb, err := m.Canonical()
	if err != nil {
		t.Fatal(err)
	}

	st := statementBytes(t, online, serial, from, to)
	rev := revocationSigned(t, root, 2, 5)

	return ManifestSet{
		Manifest: mb, ManifestSig: sign(t, online, ManifestDomain, mb),
		Statement: st, StatementSig: sign(t, root, sigchain.WorkerManifest.StatementDomain, st),
		Revocation: rev.Bytes, RevocationSig: rev.Signature,
	}
}

func testRoots(t testing.TB) sigchain.Trust {
	t.Helper()

	pub, err := ssh.NewPublicKey(rfcKey(t, 0).Public())
	if err != nil {
		t.Fatal(err)
	}

	return sigchain.Trust{Roots: []ssh.PublicKey{pub}}
}

var setFiles = []string{ManifestFile, ManifestSigFile, StatementFile, StatementSigFile, RevocationFile, RevocationSigFile}

func (s ManifestSet) files() [][]byte {
	return [][]byte{s.Manifest, s.ManifestSig, s.Statement, s.StatementSig, s.Revocation, s.RevocationSig}
}

// TestManifestSetGolden pins the six files of a manifest set and the
// stand-in binaries they describe, and verifies the set from the files.
func TestManifestSetGolden(t *testing.T) {
	t.Parallel()

	set := buildSet(t, goldenManifest(), 1, setNotBefore, setNotAfter)
	for i, name := range setFiles {
		checkGolden(t, filepath.Join("manifest-set", name), set.files()[i])
	}

	for _, arch := range []string{"amd64", "arm64"} {
		checkGolden(t, filepath.Join("manifest-set", "bloc-worker-linux-"+arch), goldenBinary(arch))
	}

	var fromFiles ManifestSet

	ptrs := []*[]byte{
		&fromFiles.Manifest, &fromFiles.ManifestSig, &fromFiles.Statement,
		&fromFiles.StatementSig, &fromFiles.Revocation, &fromFiles.RevocationSig,
	}
	for i, name := range setFiles {
		*ptrs[i] = readTestdata(t, filepath.Join("manifest-set", name))
	}

	v, err := VerifyManifestSet(testRoots(t), TrustState{}, fromFiles, setNow)
	if err != nil {
		t.Fatal(err)
	}

	if v.Manifest.Version != goldenVersion || !v.Chain.HasRevocation || v.Chain.Revocation.Sequence != 2 {
		t.Errorf("VerifyManifestSet = %+v", v)
	}

	for _, b := range v.Manifest.Binaries {
		data := readTestdata(t, filepath.Join("manifest-set", b.Name))

		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != b.SHA256 || int64(len(data)) != b.Size {
			t.Errorf("%s does not match the manifest", b.Name)
		}
	}
}

// TestVerifyManifestSetSteps walks the six steps and their codes.
func TestVerifyManifestSetSteps(t *testing.T) {
	t.Parallel()

	good := buildSet(t, goldenManifest(), 1, setNotBefore, setNotAfter)
	roots := testRoots(t)
	other := rfcKey(t, 2)

	withSig := func(f func(*ManifestSet)) ManifestSet {
		s := good
		f(&s)

		return s
	}

	low := goldenManifest()
	low.Version, low.MinimumVersion = "1.3.0", "1.0.0"

	cases := map[string]struct {
		set   ManifestSet
		trust TrustState
		now   time.Time
		code  string
	}{
		"1 statement by another root": {
			withSig(func(s *ManifestSet) {
				s.StatementSig = sign(t, other, sigchain.WorkerManifest.StatementDomain, s.Statement)
			}), TrustState{}, setNow, CodeSignatureInvalid,
		},
		"2 revoked by the recorded statement": {
			good, TrustState{Revocation: ptr(revocationSigned(t, rfcKey(t, 0), 3, 1))}, setNow, CodeSigningKeyRevoked,
		},
		"3 manifest by another key": {
			withSig(func(s *ManifestSet) { s.ManifestSig = sign(t, other, ManifestDomain, s.Manifest) }),
			TrustState{}, setNow, CodeSignatureInvalid,
		},
		"4 window of 93 days": {
			buildSet(t, goldenManifest(), 1, setNotBefore, setNotBefore.Add(93*24*time.Hour)), TrustState{}, setNow, CodeSigningKeyWindowInvalid,
		},
		"5 after the window": {good, TrustState{}, setNotAfter.Add(25 * time.Hour), CodeSignatureExpired},
		"6 below the installed floor": {
			good, TrustState{Floors: Floors{Installed: "1.5.0"}}, setNow, CodeVersionBelowFloor,
		},
		"6 below the minimum floor": {
			buildSet(t, low, 1, setNotBefore, setNotAfter), TrustState{Floors: Floors{Minimum: "1.4.0"}}, setNow, CodeVersionBelowFloor,
		},
		"signed manifest out of form": {
			withSig(func(s *ManifestSet) {
				s.Manifest = []byte(`{"domain":"bloc-worker-manifest-v1"}`)
				s.ManifestSig = sign(t, rfcKey(t, 1), ManifestDomain, s.Manifest)
			}), TrustState{}, setNow, CodeSignatureInvalid,
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, err := VerifyManifestSet(roots, c.trust, c.set, c.now); CodeOf(err) != c.code {
				t.Errorf("VerifyManifestSet = %v, want %s", err, c.code)
			}
		})
	}

	// A day of skew at each end still passes.
	for _, now := range []time.Time{setNotBefore.Add(-23 * time.Hour), setNotAfter.Add(23 * time.Hour)} {
		if _, err := VerifyManifestSet(roots, TrustState{}, good, now); err != nil {
			t.Errorf("at %s: %v", now, err)
		}
	}

	// A version at the floor passes.
	if _, err := VerifyManifestSet(roots, TrustState{Floors: Floors{Installed: goldenVersion}}, good, setNow); err != nil {
		t.Errorf("at the floor: %v", err)
	}
}

func ptr[T any](v T) *T { return &v }

func TestManifestForm(t *testing.T) {
	t.Parallel()

	good := goldenManifest()

	b, err := good.Canonical()
	if err != nil {
		t.Fatal(err)
	}

	if _, err := ParseManifest(b); err != nil {
		t.Fatal(err)
	}

	for name, f := range map[string]func(*Manifest){
		"domain":          func(m *Manifest) { m.Domain = "x" },
		"version":         func(m *Manifest) { m.Version = "v1.4.0" },
		"minimum above":   func(m *Manifest) { m.MinimumVersion = "2.0.0" },
		"local time":      func(m *Manifest) { m.BuildTime = m.BuildTime.Local() },
		"no binaries":     func(m *Manifest) { m.Binaries = nil },
		"unsorted":        func(m *Manifest) { m.Binaries[0], m.Binaries[1] = m.Binaries[1], m.Binaries[0] },
		"repeated arch":   func(m *Manifest) { m.Binaries[1] = m.Binaries[0] },
		"path in name":    func(m *Manifest) { m.Binaries[0].Name = "../bloc-worker" },
		"chunk too big":   func(m *Manifest) { m.Binaries[0].ChunkSize = MaxChunkSize + 1 },
		"missing chunk":   func(m *Manifest) { m.Binaries[0].Chunks = m.Binaries[0].Chunks[1:] },
		"upper-case hex":  func(m *Manifest) { m.Binaries[0].SHA256 = "AB" + m.Binaries[0].SHA256[2:] },
		"zero size":       func(m *Manifest) { m.Binaries[0].Size = 0 },
		"too many chunks": func(m *Manifest) { m.Binaries[0].ChunkSize = 1 },
	} {
		m := goldenManifest()
		m.Binaries = append([]Binary{}, m.Binaries...)
		f(&m)

		if _, err := m.Canonical(); err == nil {
			t.Errorf("%s: Canonical took it", name)
		}
	}

	if _, err := ParseManifest(append(b, ' ')); err == nil {
		t.Error("ParseManifest took trailing space")
	}

	if _, err := ParseManifest(bytes.Replace(b, []byte(`{"domain"`), []byte(`{"x":1,"domain"`), 1)); err == nil {
		t.Error("ParseManifest took an unknown field")
	}

	bin, _ := good.Binary("arm64")
	if bin.ChunkLen(3) != 1000-3*goldenChunkSize || bin.ChunkLen(0) != goldenChunkSize {
		t.Error("ChunkLen")
	}

	if _, ok := good.Binary("riscv64"); ok {
		t.Error("Binary found a missing architecture")
	}
}

func TestCompareVersions(t *testing.T) {
	t.Parallel()

	ordered := []string{
		"0.0.0", "0.9.0", "1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta",
		"1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.2.0", "1.10.0", "2.0.0", "99999999999999999999.0.0",
	}
	for i := range ordered {
		for j := range ordered {
			want := 0

			switch {
			case i < j:
				want = -1
			case i > j:
				want = 1
			}

			if got := CompareVersions(ordered[i], ordered[j]); got != want {
				t.Errorf("CompareVersions(%s, %s) = %d, want %d", ordered[i], ordered[j], got, want)
			}
		}
	}

	for _, bad := range []string{"", "1", "1.0", "v1.0.0", "01.0.0", "1.0.0+build", "1.0.0-", "1.0.0-a..b"} {
		if ValidVersion(bad) || CompareVersions(bad, "0.0.0") != -1 {
			t.Errorf("%q passed as a version", bad)
		}
	}

	if MaxVersion("", "bad", "1.2.0", "1.10.0", "1.9.9") != "1.10.0" || MaxVersion("x") != "" {
		t.Error("MaxVersion")
	}
}

func TestTrustStateRaisesOnly(t *testing.T) {
	t.Parallel()

	set := buildSet(t, goldenManifest(), 1, setNotBefore, setNotAfter)

	v, err := VerifyManifestSet(testRoots(t), TrustState{}, set, setNow)
	if err != nil {
		t.Fatal(err)
	}

	newer := revocationSigned(t, rfcKey(t, 0), 4, 9)
	start := TrustState{Floors: Floors{Minimum: "1.3.0", Installed: "1.1.0"}, Revocation: &newer}

	got := start.Raise(v)
	if got.Floors.Minimum != "1.3.0" || got.Floors.Installed != goldenVersion {
		t.Errorf("Raise floors = %+v", got.Floors)
	}

	if !bytes.Equal(got.Revocation.Bytes, newer.Bytes) {
		t.Error("Raise lowered the revocation's sequence")
	}

	got = TrustState{}.Raise(v)
	if got.Floors.Minimum != goldenMinimum || got.Revocation == nil || !bytes.Equal(got.Revocation.Bytes, set.Revocation) {
		t.Errorf("Raise from nothing = %+v", got)
	}

	if (TrustState{}).Floor() != MinimumWorkerVersion || got.Floor() != goldenVersion {
		t.Error("Floor")
	}
}

func TestTrustDirRoundTrip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	empty, err := ReadTrust(dir)
	if err != nil || empty.Revocation != nil || empty.Floors != (Floors{}) {
		t.Fatalf("an empty trust dir = %+v, %v", empty, err)
	}

	rev := revocationSigned(t, rfcKey(t, 0), 2, 5)
	want := TrustState{Floors: Floors{Minimum: goldenMinimum, Installed: goldenVersion}, Revocation: &rev}

	for range 2 {
		if err := WriteTrust(dir, want); err != nil {
			t.Fatal(err)
		}
	}

	got, err := ReadTrust(dir)
	if err != nil || got.Floors != want.Floors || !bytes.Equal(got.Revocation.Bytes, rev.Bytes) ||
		!bytes.Equal(got.Revocation.Signature, rev.Signature) {
		t.Fatalf("ReadTrust = %+v, %v", got, err)
	}

	floors, err := os.ReadFile(filepath.Join(dir, FloorsFile))
	if err != nil {
		t.Fatal(err)
	}

	checkGolden(t, "floors.json", floors)

	if fi, err := os.Stat(filepath.Join(dir, FloorsFile)); err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("floors.json = %v, %v", fi, err)
	}
}

func TestReadTrustRefusesABrokenDir(t *testing.T) {
	t.Parallel()

	write := func(dir, name, body string) {
		t.Helper()

		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	broken := t.TempDir()
	write(broken, FloorsFile, `{"minimum":"x","installed":""}`)

	spaced := t.TempDir()
	write(spaced, FloorsFile, `{"minimum": "1.0.0","installed":""}`)

	half := t.TempDir()
	write(half, RevocationFile, `{}`)

	linked := t.TempDir()

	if err := os.Symlink("/dev/null", filepath.Join(linked, FloorsFile)); err != nil {
		t.Fatal(err)
	}

	linkedDir := filepath.Join(t.TempDir(), "trust")
	if err := os.Symlink(spaced, linkedDir); err != nil {
		t.Fatal(err)
	}

	for name, dir := range map[string]string{
		"bad version": broken, "not canonical": spaced, "half a revocation": half, "a link": linked, "a linked dir": linkedDir,
	} {
		if got, err := ReadTrust(dir); err == nil {
			t.Errorf("%s: ReadTrust = %+v", name, got)
		}
	}

	if _, err := EncodeFloors(Floors{Minimum: "v1"}); err == nil {
		t.Error("EncodeFloors took a bad version")
	}

	if err := WriteTrust(t.TempDir(), TrustState{Floors: Floors{Installed: "bad"}}); err == nil {
		t.Error("WriteTrust took a bad floor")
	}

	if _, err := ReadTrust(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, ErrFileRule) {
		t.Error("ReadTrust took a missing directory")
	}
}
