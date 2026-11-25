package workerunit

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// The worker bundle is the one archive the bloc's workers panel offers for
// download. Its layout is fixed here, and the panel that builds it must
// match it exactly.
//
//   - It is a tar archive compressed with gzip.
//   - It holds exactly two regular files at its top level, BundleJoinFile
//     and BundleBinary. A name may carry a leading "./", and a "./"
//     directory entry is ignored, so `tar -C dir -czf bundle.tar.gz .`
//     builds a valid bundle.
//   - Nothing else is allowed in it: no other file, no directory below the
//     top level, no link of either kind, and no entry named twice.
//   - BundleBinary is the worker built for the bastion's platform, and its
//     SHA256 is the one the join file lists for that platform.
const (
	// BundleJoinFile is the join file's name in the bundle, and also its
	// name in the worker's directory on the bastion.
	BundleJoinFile = "join.json"

	// BundleBinary is the worker binary's name in the bundle.
	BundleBinary = "bloc-worker"

	// maxJoinFile is the most a join file may hold.
	maxJoinFile = 64 << 10

	// maxBinary is the most the worker binary may hold.
	maxBinary = 512 << 20

	// privateFileMode is the mode the extracted files are written with.
	privateFileMode = 0o600

	// ArchAMD64 and ArchARM64 are the only keys the join file's
	// binary.sha256 map may use, as the join contract defines them.
	ArchAMD64 = "amd64"
	ArchARM64 = "arm64"
)

var (
	// ErrBundleLayout reports a bundle that is not a gzip tar archive of
	// exactly the join file and the worker binary.
	ErrBundleLayout = errors.New("the worker bundle is not a gzip tar archive of join.json and bloc-worker")

	// ErrJoinFile reports a join file that lacks a field the bastion
	// needs, or holds one in the wrong form.
	ErrJoinFile = errors.New("the worker bundle's join file is refused")

	// ErrBundleSum reports a worker binary whose SHA256 is not one the join
	// file lists.
	ErrBundleSum = errors.New("the worker binary's SHA256 is not one the join file lists")

	// ErrBundlePlatform reports a platform the join file lists no sum for,
	// or a machine name with no known platform.
	ErrBundlePlatform = errors.New("the join file lists no worker binary for this platform")
)

// JoinFile holds the join file's fields as the join contract defines them,
// with the same names, types, and rules. The join key, which is the
// file's only secret, is the one field it leaves out: DecodeJoinFile
// requires it and checks its form, and then lets it go, so no copy of it is
// held, formatted, or logged on this side. The file itself is installed on
// the bastion byte for byte.
type JoinFile struct {
	URL          string    `json:"url"`
	CAPEM        string    `json:"caPEM,omitempty"`
	Installation string    `json:"installation"`
	Org          string    `json:"org"`
	Bloc         string    `json:"bloc"`
	JoinAccount  string    `json:"joinAccount"`
	Binary       Binary    `json:"binary"`
	IssuedAt     time.Time `json:"issuedAt"`
}

// Binary is the worker binary a join file names. SHA256 maps a bare Linux
// architecture, ArchAMD64 or ArchARM64, to the lowercase hex SHA256 of the
// worker binary built for it. Any other key is refused.
type Binary struct {
	Version string            `json:"version"`
	SHA256  map[string]string `json:"sha256"`
}

// Bundle is an extracted worker bundle.
type Bundle struct {
	// Join is the join file's non-secret fields.
	Join JoinFile
	// JoinPath is the extracted join file, mode 0600.
	JoinPath string
	// JoinSHA256 is the join file's SHA256.
	JoinSHA256 string
	// BinaryPath is the extracted worker binary, mode 0600.
	BinaryPath string
	// BinarySHA256 is the worker binary's SHA256.
	BinarySHA256 string
}

// SumFor returns the SHA256 the join file lists for arch, a bare
// architecture key such as ArchAMD64 that Platform returns.
func (j JoinFile) SumFor(arch string) (string, error) {
	sha, ok := j.Binary.SHA256[arch]
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrBundlePlatform, arch)
	}

	return sha, nil
}

// Platform maps the machine name uname -m prints to the architecture key
// the join file lists its sums under.
func Platform(machine string) (string, error) {
	switch strings.TrimSpace(machine) {
	case "x86_64", ArchAMD64:
		return ArchAMD64, nil
	case "aarch64", ArchARM64:
		return ArchARM64, nil
	default:
		return "", fmt.Errorf("%w: machine %q", ErrBundlePlatform, machine)
	}
}

// ExtractBundle reads the bundle at archive, writes its join file and
// worker binary into dir with mode 0600, and checks the join file's fields
// and that the binary's SHA256 is one the join file lists. dir should be
// private to the caller, because the join file holds the join key. No
// error it returns repeats anything the join file holds.
func ExtractBundle(archive, dir string) (Bundle, error) {
	f, err := os.Open(archive) // #nosec G304 -- the operator names the bundle
	if err != nil {
		return Bundle{}, fmt.Errorf("opening the worker bundle: %w", err)
	}

	defer func() { _ = f.Close() }()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return Bundle{}, fmt.Errorf("%w: it is not gzip compressed", ErrBundleLayout)
	}

	defer func() { _ = gz.Close() }()

	bundle := Bundle{
		JoinPath:   filepath.Join(dir, BundleJoinFile),
		BinaryPath: filepath.Join(dir, BundleBinary),
	}

	err = extractEntries(tar.NewReader(gz), &bundle)
	if err != nil {
		return Bundle{}, err
	}

	err = bundle.readJoinFile()
	if err != nil {
		return Bundle{}, err
	}

	if !bundle.Join.listsSum(bundle.BinarySHA256) {
		return Bundle{}, ErrBundleSum
	}

	return bundle, nil
}

// extractEntries writes the archive's two files and records their sums.
func extractEntries(tr *tar.Reader, bundle *Bundle) error {
	seen := map[string]bool{}

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return fmt.Errorf("%w: it is not a tar archive", ErrBundleLayout)
		}

		name, skip, err := entryName(hdr)
		if err != nil {
			return err
		}

		if skip {
			continue
		}

		if seen[name] {
			return fmt.Errorf("%w: it names %s twice", ErrBundleLayout, name)
		}

		seen[name] = true

		target, limit := bundle.BinaryPath, int64(maxBinary)
		if name == BundleJoinFile {
			target, limit = bundle.JoinPath, maxJoinFile
		}

		sha, err := writeEntry(tr, target, limit)
		if err != nil {
			return err
		}

		if name == BundleJoinFile {
			bundle.JoinSHA256 = sha
		} else {
			bundle.BinarySHA256 = sha
		}
	}

	if !seen[BundleJoinFile] || !seen[BundleBinary] {
		return fmt.Errorf("%w: it must hold both %s and %s", ErrBundleLayout, BundleJoinFile, BundleBinary)
	}

	return nil
}

// entryName returns the bundle name an entry stands for, or skip for the
// top-level "./" directory entry, and refuses every other entry.
func entryName(hdr *tar.Header) (string, bool, error) {
	name := strings.TrimPrefix(hdr.Name, "./")

	if hdr.Typeflag == tar.TypeDir && (name == "" || name == ".") {
		return "", true, nil
	}

	if hdr.Typeflag != tar.TypeReg || path.Clean(name) != name || (name != BundleJoinFile && name != BundleBinary) {
		return "", false, fmt.Errorf("%w: it holds an entry other than %s and %s",
			ErrBundleLayout, BundleJoinFile, BundleBinary)
	}

	return name, false, nil
}

// writeEntry copies the current entry to target, refusing one longer than
// limit, and returns its SHA256.
func writeEntry(r io.Reader, target string, limit int64) (string, error) {
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, privateFileMode) // #nosec G304 -- target is under the caller's dir
	if err != nil {
		return "", fmt.Errorf("writing %s from the worker bundle: %w", filepath.Base(target), err)
	}

	hash := sha256.New()

	n, err := io.Copy(io.MultiWriter(out, hash), io.LimitReader(r, limit+1))

	closeErr := out.Close()

	switch {
	case err != nil:
		return "", fmt.Errorf("%w: %s cannot be read", ErrBundleLayout, filepath.Base(target))
	case n > limit:
		return "", fmt.Errorf("%w: %s is longer than %d bytes", ErrBundleLayout, filepath.Base(target), limit)
	case closeErr != nil:
		return "", fmt.Errorf("writing %s from the worker bundle: %w", filepath.Base(target), closeErr)
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}

// readJoinFile parses and checks the extracted join file.
func (b *Bundle) readJoinFile() error {
	body, err := os.ReadFile(b.JoinPath)
	if err != nil {
		return fmt.Errorf("reading the extracted join file: %w", err)
	}

	join, err := DecodeJoinFile(body)
	if err != nil {
		return err
	}

	b.Join = join

	return nil
}

// listsSum reports whether sha is one of the join file's sums.
func (j JoinFile) listsSum(sha string) bool {
	for _, listed := range j.Binary.SHA256 {
		if listed == sha {
			return true
		}
	}

	return false
}
