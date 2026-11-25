package enroll

import (
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/fivetwenty-io/ocfp-cli/sigchain"
)

// ManifestDomain is the domain the manifest names, which is also the
// namespace its signature is made under.
const ManifestDomain = "bloc-worker-manifest-v1"

// The bounds of a manifest.
const (
	// MaxManifest bounds the manifest's bytes.
	MaxManifest = 256 << 10
	// MaxChunkSize bounds one chunk, so a chunk answer fits the bus's
	// payload limit with room to spare.
	MaxChunkSize = 512 << 10
	// DefaultChunkSize is the chunk size a release uses unless it names
	// another.
	DefaultChunkSize = 256 << 10
	// MaxChunks bounds the chunks of one file.
	MaxChunks = 2048
	// maxFiles bounds the architectures a manifest names.
	maxFiles = 8
)

// MinimumWorkerVersion is the embedded floor: no manifest below it
// installs, however it is signed.
const MinimumWorkerVersion = "0.0.0"

// The six files of a manifest set, as a release carries them.
const (
	ManifestFile      = "bloc-worker-manifest.json"
	ManifestSigFile   = "bloc-worker-manifest.json.sig"
	StatementFile     = "bloc-worker-signing-key.json"
	StatementSigFile  = "bloc-worker-signing-key.json.sig"
	RevocationFile    = "signing-key-revocation.json"
	RevocationSigFile = "signing-key-revocation.json.sig"
)

var (
	archForm     = regexp.MustCompile(`^[a-z0-9_]{1,16}$`)
	fileNameForm = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	digestForm   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Binary is one architecture's worker binary as the manifest names it:
// its file name, its size, its SHA-256, its chunk size, and the SHA-256
// of every chunk, which let a download resume from the last chunk that
// matched. Digests are lower-case hex.
type Binary struct {
	Arch      string   `json:"arch"`
	Name      string   `json:"name"`
	Size      int64    `json:"size"`
	SHA256    string   `json:"sha256"`
	ChunkSize int64    `json:"chunkSize"`
	Chunks    []string `json:"chunks"`
}

// ChunkLen returns the length of chunk i.
func (b Binary) ChunkLen(i int) int64 {
	if i == len(b.Chunks)-1 {
		return b.Size - int64(i)*b.ChunkSize
	}

	return b.ChunkSize
}

func (b Binary) validate() error {
	switch {
	case !archForm.MatchString(b.Arch):
		return fmt.Errorf("the architecture %q is not in its form", b.Arch)
	case !fileNameForm.MatchString(b.Name):
		return fmt.Errorf("the %s file name is not in its form", b.Arch)
	case b.ChunkSize <= 0 || b.ChunkSize > MaxChunkSize:
		return fmt.Errorf("the %s chunk size is not between 1 and %d", b.Arch, MaxChunkSize)
	case b.Size <= 0 || b.Size > MaxChunks*b.ChunkSize:
		return fmt.Errorf("the %s size is not between 1 and %d chunks", b.Arch, MaxChunks)
	case !digestForm.MatchString(b.SHA256):
		return fmt.Errorf("the %s digest is not the hex of a SHA-256", b.Arch)
	case int64(len(b.Chunks)) != (b.Size+b.ChunkSize-1)/b.ChunkSize:
		return fmt.Errorf("the %s chunk count does not cover its size", b.Arch)
	}

	for _, c := range b.Chunks {
		if !digestForm.MatchString(c) {
			return fmt.Errorf("a %s chunk digest is not the hex of a SHA-256", b.Arch)
		}
	}

	return nil
}

// Manifest is the signed list of a release's worker binaries. Version is
// the release's version, and MinimumVersion the lowest version a
// bastion that installed this one may take later. BuildTime is a UTC
// second. Binaries are in ascending order of architecture, with no
// repeats.
type Manifest struct {
	Domain         string    `json:"domain"`
	Version        string    `json:"version"`
	MinimumVersion string    `json:"minimumVersion"`
	BuildTime      time.Time `json:"buildTime"`
	Binaries       []Binary  `json:"binaries"`
}

// Binary returns the manifest's binary for arch.
func (m Manifest) Binary(arch string) (Binary, bool) {
	for _, b := range m.Binaries {
		if b.Arch == arch {
			return b, true
		}
	}

	return Binary{}, false
}

// Validate holds the manifest to its form.
func (m Manifest) Validate() error {
	switch {
	case m.Domain != ManifestDomain:
		return fmt.Errorf("the manifest's domain is not %s", ManifestDomain)
	case !ValidVersion(m.Version) || !ValidVersion(m.MinimumVersion):
		return errors.New("the manifest's versions are not semantic versions")
	case CompareVersions(m.MinimumVersion, m.Version) > 0:
		return errors.New("the manifest's minimum version is above its version")
	case m.BuildTime.IsZero() || m.BuildTime.Location() != time.UTC || !m.BuildTime.Equal(m.BuildTime.Truncate(time.Second)):
		return errors.New("the manifest's build time is not a UTC second")
	case len(m.Binaries) == 0 || len(m.Binaries) > maxFiles:
		return fmt.Errorf("the manifest names no binary or more than %d", maxFiles)
	}

	for i, b := range m.Binaries {
		if err := b.validate(); err != nil {
			return err
		}

		if i > 0 && b.Arch <= m.Binaries[i-1].Arch {
			return errors.New("the manifest's architectures are not ascending and unique")
		}
	}

	return nil
}

// Canonical returns the manifest's canonical bytes, which the online key
// signs.
func (m Manifest) Canonical() ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}

	return canonicalJSON(m)
}

// ParseManifest reads a manifest's bytes and refuses any bytes but the
// canonical form of a valid manifest.
func ParseManifest(b []byte) (Manifest, error) {
	if len(b) == 0 || len(b) > MaxManifest {
		return Manifest{}, fmt.Errorf("the manifest is empty or longer than %d bytes", MaxManifest)
	}

	var m Manifest
	if err := decodeStrict(b, &m); err != nil {
		return Manifest{}, errors.New("the manifest does not decode")
	}

	canon, err := m.Canonical()
	if err != nil {
		return Manifest{}, err
	}

	if string(canon) != string(b) {
		return Manifest{}, errors.New("the manifest is not in its canonical form")
	}

	return m, nil
}

// ManifestSet is the six files of a manifest set: the manifest and its
// signature, the online key's statement and its signature, and the
// newest revocation statement the release knows and its signature.
// Revocation and RevocationSig are both empty when the set carries no
// revocation.
type ManifestSet struct {
	Manifest      []byte `json:"manifest"`
	ManifestSig   []byte `json:"manifestSig"`
	Statement     []byte `json:"statement"`
	StatementSig  []byte `json:"statementSig"`
	Revocation    []byte `json:"revocation,omitempty"`
	RevocationSig []byte `json:"revocationSig,omitempty"`
}

func (s ManifestSet) chainSet() sigchain.Set {
	set := sigchain.Set{
		Statement: sigchain.Signed{Bytes: s.Statement, Signature: s.StatementSig},
		Payload:   sigchain.Signed{Bytes: s.Manifest, Signature: s.ManifestSig},
	}

	if len(s.Revocation) > 0 || len(s.RevocationSig) > 0 {
		set.Revocations = []sigchain.Signed{{Bytes: s.Revocation, Signature: s.RevocationSig}}
	}

	return set
}

// Verified is what VerifyManifestSet found: the manifest, and the chain
// result whose newest revocation the caller records.
type Verified struct {
	Manifest Manifest
	Chain    sigchain.Result
}

// VerifyManifestSet checks a manifest set in the six steps of the chain,
// at the clock reading now, and refuses with a coded Error at the first
// that fails. Steps 1 to 5 are sigchain.Verify's, against roots and
// every revocation statement in trust and in the set. Step 6 refuses,
// with CodeVersionBelowFloor, a manifest whose version is below the
// highest of the embedded MinimumWorkerVersion and the two floors that
// trust records. A signed manifest that does not hold to its form is
// refused with CodeSignatureInvalid.
func VerifyManifestSet(roots sigchain.Trust, trust TrustState, set ManifestSet, now time.Time) (Verified, error) {
	t := sigchain.Trust{Roots: roots.Roots, Revocations: append([]sigchain.Signed{}, roots.Revocations...)}
	if trust.Revocation != nil {
		t.Revocations = append(t.Revocations, *trust.Revocation)
	}

	res, err := sigchain.Verify(sigchain.WorkerManifest, t, set.chainSet(), now)
	if err != nil {
		return Verified{Chain: res}, err
	}

	m, err := ParseManifest(set.Manifest)
	if err != nil {
		return Verified{Chain: res}, refuse(CodeSignatureInvalid, err.Error())
	}

	floor := trust.Floor()
	if CompareVersions(m.Version, floor) < 0 {
		return Verified{Manifest: m, Chain: res}, refuse(CodeVersionBelowFloor,
			fmt.Sprintf("the manifest's version %s is below the floor %s", m.Version, floor))
	}

	return Verified{Manifest: m, Chain: res}, nil
}
