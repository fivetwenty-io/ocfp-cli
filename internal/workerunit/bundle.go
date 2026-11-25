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
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// The worker bundle is the one archive the bloc's workers panel offers for
// download, and the join contract fixes its layout. This reader holds a
// bundle to it exactly.
//
//   - It is a gzip-compressed tar, which the panel writes as two gzip
//     members that a gzip reader reads as one stream.
//   - It holds exactly two regular files at its top level, BundleBinary
//     and then BundleJoinFile, in that order, with modes 0755 and 0600.
//     Each holds at least one byte and no more than its bound.
//   - The archive, and the tar stream it decompresses to, are each held to
//     maxBundle, and the stream holds only zero bytes after the tar's end.
//   - BundleBinary is the worker for the bastion's architecture, and its
//     SHA256 is the one the join file lists for that architecture.
//
// The reader takes no owner, mode, or time from the archive. It writes
// the extracted files with mode 0600, and the install scripts set the
// installed files' owners and modes themselves.
const (
	// BundleJoinFile is the join file's name in the bundle, and also its
	// name in the worker's directory on the bastion.
	BundleJoinFile = "join.json"

	// BundleBinary is the worker binary's name in the bundle.
	BundleBinary = "bloc-worker"

	// bundleJoinFileMode and bundleBinaryMode are the modes the layout
	// gives the two members.
	bundleJoinFileMode = 0o600
	bundleBinaryMode   = 0o755

	// maxJoinFile is the most a join file may hold.
	maxJoinFile = 64 << 10

	// maxBinary is the most the worker binary may hold.
	maxBinary = 128 << 20

	// maxBundle is the most the archive, and the tar stream it
	// decompresses to, may hold. It leaves one MiB over the two members
	// for the tar's headers, padding, and end, and for gzip's framing.
	maxBundle = maxBinary + maxJoinFile + 1<<20

	// privateFileMode is the mode the extracted files are written with.
	privateFileMode = 0o600

	// ArchAMD64 and ArchARM64 are the only keys the join file's
	// binary.sha256 map may use, as the join contract defines them.
	ArchAMD64 = "amd64"
	ArchARM64 = "arm64"
)

// The bundle's refusals. Each stands for one of the join contract's
// refusal codes, named beside it.
var (
	// ErrBundleLayout reports an archive that is not a gzip-compressed
	// tar or that carries data after the tar's end, a member that is
	// empty or has a mode other than the layout's, or an architecture the
	// layout does not name. It is JOIN_BUNDLE_INVALID.
	ErrBundleLayout = errors.New("the worker bundle is not a gzip tar archive of bloc-worker and join.json")

	// ErrBundleUnsafePath reports a member whose name is empty or
	// absolute, or holds a ".." element, a backslash, a drive letter, or
	// a NUL. It is JOIN_BUNDLE_UNSAFE_PATH.
	ErrBundleUnsafePath = errors.New("the worker bundle has a member whose name could leave its directory")

	// ErrBundleLink reports a member that is a symbolic or hard link. It
	// is JOIN_BUNDLE_LINK.
	ErrBundleLink = errors.New("the worker bundle has a member that is a link")

	// ErrBundleDevice reports a member that is a device or a named pipe.
	// It is JOIN_BUNDLE_DEVICE.
	ErrBundleDevice = errors.New("the worker bundle has a member that is a device or a pipe")

	// ErrBundleDuplicate reports a member that appears twice. It is
	// JOIN_BUNDLE_DUPLICATE.
	ErrBundleDuplicate = errors.New("the worker bundle has a member twice")

	// ErrBundleMembers reports a member the layout does not name, a
	// directory, a missing member, or the two members out of order. It is
	// JOIN_BUNDLE_MEMBERS.
	ErrBundleMembers = errors.New("the worker bundle must hold bloc-worker and then join.json, and nothing else")

	// ErrBundleTooLarge reports a member past its bound, or an archive or
	// tar stream past the archive's bound. It is JOIN_BUNDLE_TOO_LARGE.
	ErrBundleTooLarge = errors.New("the worker bundle is larger than its bounds allow")

	// ErrJoinFile reports a join file that lacks a field the bastion
	// needs, or holds one in the wrong form. It is JOIN_FILE_INVALID.
	ErrJoinFile = errors.New("the worker bundle's join file is refused")

	// ErrJoinFileTooNew reports a join file whose version is newer than
	// the one this ocfp reads. It also matches ErrJoinFile. It is
	// JOIN_FILE_VERSION_UNKNOWN.
	ErrJoinFileTooNew = fmt.Errorf("%w: its version is newer than this ocfp reads", ErrJoinFile)

	// ErrBundleSum reports a worker binary whose SHA256 is not the one the
	// join file lists for the bastion's architecture, or a join file that
	// lists none for it. It is JOIN_BUNDLE_SUM_MISMATCH.
	ErrBundleSum = errors.New("the worker binary's SHA256 is not one the join file lists")

	// ErrBundleArch reports a worker binary the join file lists, but for
	// an architecture other than the bastion's. It also matches
	// ErrBundleSum.
	ErrBundleArch = fmt.Errorf("%w for the bastion's architecture", ErrBundleSum)

	// ErrBundlePlatform reports a machine name with no known
	// architecture.
	ErrBundlePlatform = errors.New("the join file lists no worker binary for this platform")
)

// JoinFile holds the join file's fields as the join contract defines them,
// with the same names, types, and rules. The join key, which is the
// file's only secret, is the one field it leaves out: DecodeJoinFile
// requires it and checks its form, and then lets it go, so no copy of it is
// held, formatted, or logged on this side. The file itself is installed on
// the bastion byte for byte.
type JoinFile struct {
	Version      int       `json:"version"`
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

// ExtractBundle reads the bundle at archive, writes its worker binary and
// join file into dir with mode 0600 whatever the archive's modes say, and
// holds the bundle to the join contract's layout. When arch, a key that
// Platform returns, names the bastion's architecture, the binary's SHA256
// must be the one the join file lists for it. An empty arch, for a dry
// run that never asks the bastion, accepts a binary listed under any
// architecture. dir should be private to the caller, because the join
// file holds the join key, and on any refusal the files it wrote are
// removed. No error it returns repeats anything the join file holds.
func ExtractBundle(archive, dir, arch string) (Bundle, error) {
	return extractBundle(archive, dir, arch, contractBounds)
}

// bundleBounds are the bounds a bundle is held to. contractBounds are the
// join contract's, and a test may pass smaller ones.
type bundleBounds struct {
	binary, join, archive int64
}

var contractBounds = bundleBounds{binary: maxBinary, join: maxJoinFile, archive: maxBundle}

// bundleMember is one member the layout names.
type bundleMember struct {
	name string
	mode int64
	max  int64
}

func extractBundle(archive, dir, arch string, bounds bundleBounds) (Bundle, error) {
	if arch != "" && arch != ArchAMD64 && arch != ArchARM64 {
		return Bundle{}, fmt.Errorf("%w: %q is not an architecture key", ErrBundleLayout, arch)
	}

	f, err := os.Open(archive) // #nosec G304 -- the operator names the bundle
	if err != nil {
		return Bundle{}, fmt.Errorf("opening the worker bundle: %w", err)
	}

	defer func() { _ = f.Close() }()

	bundle := Bundle{
		JoinPath:   filepath.Join(dir, BundleJoinFile),
		BinaryPath: filepath.Join(dir, BundleBinary),
	}

	src := &boundedReader{r: f, left: bounds.archive}
	stream := &boundedReader{left: bounds.archive}

	err = bundle.read(src, stream, arch, bounds)
	if err != nil {
		_ = os.Remove(bundle.JoinPath)
		_ = os.Remove(bundle.BinaryPath)

		return Bundle{}, bundleFault(err, src, stream, bounds.archive)
	}

	return bundle, nil
}

// read decompresses src through stream, extracts the two members, and
// checks the join file and the binary's sum.
func (b *Bundle) read(src, stream *boundedReader, arch string, bounds bundleBounds) error {
	gz, err := gzip.NewReader(src)
	if err != nil {
		return fmt.Errorf("%w: it is not gzip compressed", ErrBundleLayout)
	}

	defer func() { _ = gz.Close() }()

	stream.r = gz

	err = b.extractMembers(tar.NewReader(stream), bounds)
	if err != nil {
		return err
	}

	err = drainZeros(stream)
	if err != nil {
		return err
	}

	err = b.readJoinFile()
	if err != nil {
		return err
	}

	return b.checkBinary(arch)
}

// bundleFault turns a failure to read a bundle into what ExtractBundle
// returns. A read past either bound is ErrBundleTooLarge whatever gzip or
// tar made of it, and a refusal this reader made stands as it is.
func bundleFault(err error, src, stream *boundedReader, limit int64) error {
	if src.over || stream.over {
		return fmt.Errorf("%w: the bundle, or the tar it holds, is longer than %d bytes", ErrBundleTooLarge, limit)
	}

	return err
}

// checkBinary refuses a binary the join file does not list, and, when arch
// is set, one it does not list for arch.
func (b *Bundle) checkBinary(arch string) error {
	if !b.Join.listsSum(b.BinarySHA256) {
		return ErrBundleSum
	}

	if arch == "" {
		return nil
	}

	want, ok := b.Join.Binary.SHA256[arch]
	if !ok {
		return fmt.Errorf("%w: the join file lists no binary for %s", ErrBundleSum, arch)
	}

	if want != b.BinarySHA256 {
		return fmt.Errorf("%w: the bastion is %s, and the bundle's binary has SHA256 %s",
			ErrBundleArch, arch, b.BinarySHA256)
	}

	return nil
}

// extractMembers writes the archive's two members and records their sums.
func (b *Bundle) extractMembers(tr *tar.Reader, bounds bundleBounds) error {
	members := []bundleMember{
		{name: BundleBinary, mode: bundleBinaryMode, max: bounds.binary},
		{name: BundleJoinFile, mode: bundleJoinFileMode, max: bounds.join},
	}
	seen := map[string]bool{}

	for i := 0; ; i++ {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return fmt.Errorf("%w: it is not a tar archive", ErrBundleLayout)
		}

		m, err := checkEntry(hdr, i, members, seen)
		if err != nil {
			return err
		}

		seen[m.name] = true

		target := b.BinaryPath
		if m.name == BundleJoinFile {
			target = b.JoinPath
		}

		sha, err := writeEntry(tr, target, m.max)
		if err != nil {
			return err
		}

		if m.name == BundleJoinFile {
			b.JoinSHA256 = sha
		} else {
			b.BinarySHA256 = sha
		}
	}

	for _, m := range members {
		if !seen[m.name] {
			return fmt.Errorf("%w: it has no %s", ErrBundleMembers, m.name)
		}
	}

	return nil
}

// checkEntry holds the i-th tar entry to the layout and returns the member
// it is. The checks run from the most dangerous on: the name's safety, the
// entry's type, the name, a repeat, the order, the size, and the mode.
func checkEntry(hdr *tar.Header, i int, members []bundleMember, seen map[string]bool) (bundleMember, error) {
	name := hdr.Name

	if unsafeName(name) {
		return bundleMember{}, fmt.Errorf("%w: %q", ErrBundleUnsafePath, name)
	}

	switch hdr.Typeflag {
	case tar.TypeReg:
	case tar.TypeLink, tar.TypeSymlink:
		return bundleMember{}, fmt.Errorf("%w: %q", ErrBundleLink, name)
	case tar.TypeChar, tar.TypeBlock, tar.TypeFifo:
		return bundleMember{}, fmt.Errorf("%w: %q", ErrBundleDevice, name)
	default:
		return bundleMember{}, fmt.Errorf("%w: %q is not a regular file", ErrBundleMembers, name)
	}

	at := slices.IndexFunc(members, func(m bundleMember) bool { return m.name == name })
	if at < 0 {
		return bundleMember{}, fmt.Errorf("%w: it has a member %q", ErrBundleMembers, name)
	}

	m := members[at]

	switch {
	case seen[name]:
		return bundleMember{}, fmt.Errorf("%w: %s", ErrBundleDuplicate, name)
	case at != i:
		return bundleMember{}, fmt.Errorf("%w: %s is out of order", ErrBundleMembers, name)
	case hdr.Size < 1:
		return bundleMember{}, fmt.Errorf("%w: %s is empty", ErrBundleLayout, name)
	case hdr.Size > m.max:
		return bundleMember{}, fmt.Errorf("%w: %s holds %d bytes, past its bound of %d", ErrBundleTooLarge, name, hdr.Size, m.max)
	case hdr.Mode != m.mode:
		return bundleMember{}, fmt.Errorf("%w: %s has mode %#o, and the layout gives it %#o", ErrBundleLayout, name, hdr.Mode, m.mode)
	}

	return m, nil
}

// unsafeName reports whether a tar entry's name is empty or absolute,
// names a parent directory in any element, or holds a backslash, a drive
// letter, or a NUL.
func unsafeName(name string) bool {
	if name == "" || strings.HasPrefix(name, "/") || strings.ContainsAny(name, "\\\x00") {
		return true
	}

	if len(name) >= 2 && name[1] == ':' {
		return true
	}

	return slices.Contains(strings.Split(name, "/"), "..")
}

// drainZeros reads the stream past the tar's end and refuses any byte that
// is not zero, so the archive carries nothing a reader would skip.
func drainZeros(r io.Reader) error {
	buf := make([]byte, drainBuffer)

	for {
		n, err := r.Read(buf)
		if slices.ContainsFunc(buf[:n], func(c byte) bool { return c != 0 }) {
			return fmt.Errorf("%w: it carries data after its tar's end", ErrBundleLayout)
		}

		if errors.Is(err, io.EOF) {
			return nil
		}

		if err != nil {
			return fmt.Errorf("%w: it cannot be read to its end", ErrBundleLayout)
		}
	}
}

const drainBuffer = 32 << 10

// boundedReader reads at most left bytes from r and fails past them,
// marking over when r held more.
type boundedReader struct {
	r    io.Reader
	left int64
	over bool
}

var errBundleBound = errors.New("past the worker bundle's bound")

func (b *boundedReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	if b.left <= 0 {
		var one [1]byte

		n, err := b.r.Read(one[:])
		if n > 0 {
			b.over = true

			return 0, errBundleBound
		}

		return 0, err
	}

	if int64(len(p)) > b.left {
		p = p[:b.left]
	}

	n, err := b.r.Read(p)
	b.left -= int64(n)

	return n, err
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
		return "", fmt.Errorf("%w: %s is longer than %d bytes", ErrBundleTooLarge, filepath.Base(target), limit)
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
