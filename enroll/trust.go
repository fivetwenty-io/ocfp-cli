package enroll

import (
	"bytes"
	"errors"
	"fmt"
	"os"

	"github.com/fivetwenty-io/ocfp-cli/sigchain"
)

// TrustDir is where a bastion keeps its floors and its newest revocation
// statement. Only the root steps write it, so the enrollment service can
// read the floors but never lower them.
const TrustDir = "/var/lib/bloc-worker-trust"

// The files of the trust directory.
const (
	FloorsFile = "floors.json"
)

// Floors are the two floors a bastion records. Minimum is the highest
// minimum version of any manifest that passed every step of the chain on
// this bastion, and Installed is the version installed there. Either is
// empty when nothing set it.
type Floors struct {
	Minimum   string `json:"minimum"`
	Installed string `json:"installed"`
}

// TrustState is what the trust directory holds: the floors, and the
// newest revocation statement the bastion has verified, if any.
type TrustState struct {
	Floors     Floors
	Revocation *sigchain.Signed
}

// Floor returns the highest of the embedded minimum and the two
// recorded floors.
func (t TrustState) Floor() string {
	return MaxVersion(MinimumWorkerVersion, t.Floors.Minimum, t.Floors.Installed)
}

// Raise returns the trust state after v was installed: the minimum
// floor rises to v's minimum version, the installed floor to v's
// version, and the revocation to v's newest one, and none of them ever
// falls. Only a manifest set that passed every step may raise it.
func (t TrustState) Raise(v Verified) TrustState {
	out := TrustState{
		Floors: Floors{
			Minimum:   MaxVersion(t.Floors.Minimum, v.Manifest.MinimumVersion),
			Installed: MaxVersion(t.Floors.Installed, v.Manifest.Version),
		},
		Revocation: t.Revocation,
	}

	if v.Chain.HasRevocation {
		newer := true

		if t.Revocation != nil {
			if cur, err := sigchain.ParseRevocation(t.Revocation.Bytes); err == nil && cur.Sequence >= v.Chain.Revocation.Sequence {
				newer = false
			}
		}

		if newer {
			r := v.Chain.RevocationSigned
			out.Revocation = &r
		}
	}

	return out
}

func (f Floors) validate() error {
	for _, v := range []string{f.Minimum, f.Installed} {
		if v != "" && !ValidVersion(v) {
			return errors.New("a floor is not a semantic version")
		}
	}

	return nil
}

// EncodeFloors returns the floors' canonical bytes.
func EncodeFloors(f Floors) ([]byte, error) {
	if err := f.validate(); err != nil {
		return nil, err
	}

	return canonicalJSON(f)
}

// ParseFloors reads the floors' canonical bytes.
func ParseFloors(b []byte) (Floors, error) {
	var f Floors
	if err := decodeStrict(b, &f); err != nil {
		return Floors{}, errors.New("the floors do not decode")
	}

	canon, err := EncodeFloors(f)
	if err != nil {
		return Floors{}, err
	}

	if !bytes.Equal(canon, b) {
		return Floors{}, errors.New("the floors are not in their canonical form")
	}

	return f, nil
}

// ReadTrust reads the trust state from dir under the file rules. A
// missing file is an empty part of the state. A file that is present
// but broken is an error, because a floor that can't be read must not
// read as no floor. A revocation statement is kept only with its
// signature; which of several statements is newest, and whether it
// verifies, is the chain's step 2 to decide. The directory itself must
// exist and be a directory, not a link: the root step that installs the
// worker creates it, so a missing one is refused rather than read as a
// bastion with no floor.
func ReadTrust(dir string) (TrustState, error) {
	var t TrustState

	if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() {
		return TrustState{}, fmt.Errorf("%w: %s is not a directory", ErrFileRule, dir)
	}

	b, err := readOptional(dir, FloorsFile)
	if err != nil {
		return TrustState{}, err
	}

	if b != nil {
		if t.Floors, err = ParseFloors(b); err != nil {
			return TrustState{}, fmt.Errorf("enroll: %s/%s: %w", dir, FloorsFile, err)
		}
	}

	rev, err := readOptional(dir, RevocationFile)
	if err != nil {
		return TrustState{}, err
	}

	sig, err := readOptional(dir, RevocationSigFile)
	if err != nil {
		return TrustState{}, err
	}

	switch {
	case rev != nil && sig != nil:
		t.Revocation = &sigchain.Signed{Bytes: rev, Signature: sig}
	case rev != nil || sig != nil:
		return TrustState{}, fmt.Errorf("enroll: %s holds a revocation statement without its signature, or the reverse", dir)
	}

	return t, nil
}

func readOptional(dir, name string) ([]byte, error) {
	limit := int64(MaxSmallFile)
	if name == RevocationFile {
		limit = sigchain.MaxRevocation
	}

	b, err := ReadNoFollow(dir, name, limit)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}

	return b, err
}

// WriteTrust writes the trust state into dir, which the root step owns,
// as fresh files that replace the old ones, each at mode 0644 and owned
// by the caller. Each file is written whole before it takes the old
// one's place, so a reader never sees half a floor.
func WriteTrust(dir string, t TrustState) error {
	floors, err := EncodeFloors(t.Floors)
	if err != nil {
		return err
	}

	files := map[string][]byte{FloorsFile: floors}
	if t.Revocation != nil {
		files[RevocationFile] = t.Revocation.Bytes
		files[RevocationSigFile] = t.Revocation.Signature
	}

	for _, name := range []string{FloorsFile, RevocationFile, RevocationSigFile} {
		b, ok := files[name]
		if !ok {
			continue
		}

		if err := replaceFile(dir, name, b); err != nil {
			return err
		}
	}

	return nil
}

// replaceFile writes b as a fresh file beside name and renames it over
// name. dir belongs to the root step, so a rename there is safe.
func replaceFile(dir, name string, b []byte) error {
	tmp := "." + name + ".new"

	_ = os.Remove(dir + "/" + tmp)

	if err := CreateExclusive(dir, tmp, b, -1, -1, 0o644); err != nil {
		return err
	}

	if err := os.Rename(dir+"/"+tmp, dir+"/"+name); err != nil {
		_ = os.Remove(dir + "/" + tmp)

		return fmt.Errorf("enroll: %s/%s: %w", dir, name, err)
	}

	return nil
}
