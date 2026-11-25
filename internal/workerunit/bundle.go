package workerunit

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
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

var (
	// blocForm is the form a bloc id takes.
	blocForm = regexp.MustCompile(`^ocfp-[a-z0-9]+(-[a-z0-9]+)*$`)

	// seedForm is the form of an nkey seed. The join key is the only
	// secret in the join file, so it is checked for shape and never read
	// further, logged, or repeated in an error.
	seedForm = regexp.MustCompile(`^S[A-Z2-7]{57}$`)

	// sumForm is a lowercase hex SHA256.
	sumForm = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// JoinFile holds the fields of the join file that the bastion reads. The
// join key, which is the only secret in the file, is deliberately absent,
// so no copy of it is held, formatted, or logged on this side. The file
// itself is installed on the bastion byte for byte.
type JoinFile struct {
	URL          string      `json:"url"`
	Installation string      `json:"installation"`
	Org          string      `json:"org"`
	Bloc         string      `json:"bloc"`
	JoinAccount  string      `json:"joinAccount"`
	Binary       BinarySums  `json:"binary"`
	IssuedAt     string      `json:"issuedAt"`
	CAPEM        string      `json:"caPEM,omitempty"`
	joinKeyShape joinKeyForm `json:"-"`
}

// BinarySums is the join file's binary field. SHA256 maps a platform, such
// as "linux/amd64", to the lowercase hex SHA256 of the worker binary built
// for it.
type BinarySums struct {
	Version string            `json:"version"`
	SHA256  map[string]string `json:"sha256"`
}

// joinKeyForm records whether the join key is present and shaped like a
// seed, and nothing else about it.
type joinKeyForm int

const (
	joinKeyMissing joinKeyForm = iota
	joinKeyMalformed
	joinKeySeed
)

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

// SumFor returns the SHA256 the join file lists for platform.
func (j JoinFile) SumFor(platform string) (string, error) {
	sha, ok := j.Binary.SHA256[platform]
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrBundlePlatform, platform)
	}

	return sha, nil
}

// Platform maps the machine name uname -m prints to the platform the join
// file lists its sums under.
func Platform(machine string) (string, error) {
	switch strings.TrimSpace(machine) {
	case "x86_64", "amd64":
		return "linux/amd64", nil
	case "aarch64", "arm64":
		return "linux/arm64", nil
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

	join, err := parseJoinFile(body)
	if err != nil {
		return err
	}

	b.Join = join

	return nil
}

// parseJoinFile decodes body and holds it to the join file's rules. The
// decoder's own text can quote the file, so a refusal names only the field
// at fault.
func parseJoinFile(body []byte) (JoinFile, error) {
	var join JoinFile

	err := json.Unmarshal(body, &join)
	if err != nil {
		return JoinFile{}, fmt.Errorf("%w: it is not a JSON join file", ErrJoinFile)
	}

	// The seed is decoded into a value that goes out of scope here, and
	// only its shape is kept.
	var secret struct {
		JoinKey *string `json:"joinKey"`
	}

	err = json.Unmarshal(body, &secret)
	if err != nil {
		return JoinFile{}, fmt.Errorf("%w: it is not a JSON join file", ErrJoinFile)
	}

	switch {
	case secret.JoinKey == nil || *secret.JoinKey == "":
		join.joinKeyShape = joinKeyMissing
	case seedForm.MatchString(*secret.JoinKey):
		join.joinKeyShape = joinKeySeed
	default:
		join.joinKeyShape = joinKeyMalformed
	}

	return join, join.validate()
}

// validate holds the join file to the fields the bastion needs.
func (j JoinFile) validate() error {
	refuse := func(field, rule string) error {
		return fmt.Errorf("%w: %s %s", ErrJoinFile, field, rule)
	}

	switch {
	case !blocForm.MatchString(j.Bloc):
		return refuse("bloc", "must be a bloc id such as ocfp-lab-1")
	case strings.TrimSpace(j.URL) == "":
		return refuse("url", "must name the manager URL")
	case strings.TrimSpace(j.Installation) == "":
		return refuse("installation", "must be set")
	case strings.TrimSpace(j.Org) == "":
		return refuse("org", "must be set")
	case strings.TrimSpace(j.JoinAccount) == "":
		return refuse("joinAccount", "must be set")
	case j.joinKeyShape == joinKeyMissing:
		return refuse("joinKey", "must be set")
	case j.joinKeyShape != joinKeySeed:
		return refuse("joinKey", "must be a signing key seed")
	case len(j.Binary.SHA256) == 0:
		return refuse("binary.sha256", "must list the worker binary's SHA256 for at least one platform")
	}

	for platform, sha := range j.Binary.SHA256 {
		if !sumForm.MatchString(sha) {
			return refuse("binary.sha256", "for "+platform+" must be a lowercase hex SHA256")
		}
	}

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
