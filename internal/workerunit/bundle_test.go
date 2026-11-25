package workerunit_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fivetwenty-io/ocfp-cli/internal/workerunit"
)

// testSeed is a well-formed account seed with a valid checksum, made from
// fixed bytes for these tests only. No test output may ever repeat it.
const testSeed = "SAAAOCAJBIFQYDIOB4IBCEQTCQKRMFYYDENBWHA5DYPSAIJCEMSCKJWJX4"

// The other keys the tests use. testAccount is an account public key, and
// testUser and testUserSeed are a user's public key and seed, which a join
// file may never carry in place of the account's.
const (
	testAccount      = "AAAQEAYEAUDAOCAJBIFQYDIOB4IBCEQTCQKRMFYYDENBWHA5DYPSAXJQ"
	testUser         = "UAAQEAYEAUDAOCAJBIFQYDIOB4IBCEQTCQKRMFYYDENBWHA5DYPSBT2P"
	testUserSeed     = "SUAAOCAJBIFQYDIOB4IBCEQTCQKRMFYYDENBWHA5DYPSAIJCEMSCKJR2OM"
	testInstallation = "inst-0123456789abcdef"
)

var testBinary = []byte("\x7fELF pretend worker binary")

func sum(b []byte) string {
	s := sha256.Sum256(b)

	return hex.EncodeToString(s[:])
}

// joinDoc returns a join file as the workers panel issues it, which the
// caller can change before it is encoded.
func joinDoc() map[string]any {
	return map[string]any{
		"version":      1,
		"url":          "wss://manager.example.com:443",
		"installation": testInstallation,
		"org":          "acme",
		"bloc":         "ocfp-lab-drgao",
		"joinAccount":  testAccount,
		"joinKey":      testSeed,
		"binary": map[string]any{
			"version": "1.2.3",
			"sha256": map[string]string{
				"amd64": sum(testBinary),
				"arm64": strings.Repeat("ab", 32),
			},
		},
		"issuedAt": "2026-11-24T20:00:00Z",
	}
}

type entry struct {
	name     string
	body     []byte
	typeflag byte
	// mode is the header's mode. When it is zero, a member the layout
	// names gets the layout's mode, and any other entry gets 0644.
	mode int64
	// declared, when set, is a size the header claims with no body
	// written after it, and it ends the archive there.
	declared int64
	linkname string
}

// layoutMode is the mode the join contract gives a member.
func layoutMode(name string) int64 {
	switch name {
	case workerunit.BundleBinary:
		return 0o755
	case workerunit.BundleJoinFile:
		return 0o600
	default:
		return 0o644
	}
}

// writeArchive writes the entries, in order, as one gzip member.
func writeArchive(t *testing.T, entries ...entry) string {
	t.Helper()

	var buf bytes.Buffer

	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	for _, e := range entries {
		flag := e.typeflag
		if flag == 0 {
			flag = tar.TypeReg
		}

		mode := e.mode
		if mode == 0 {
			mode = layoutMode(e.name)
		}

		hdr := &tar.Header{Name: e.name, Mode: mode, Size: int64(len(e.body)), Typeflag: flag, Linkname: e.linkname}
		if flag != tar.TypeReg {
			hdr.Size = 0
		}

		if flag == tar.TypeSymlink && hdr.Linkname == "" {
			hdr.Linkname = "/etc/passwd"
		}

		if e.declared > 0 {
			hdr.Size = e.declared
			require.NoError(t, tw.WriteHeader(hdr))
			require.NoError(t, gz.Close())

			return writeBundleFile(t, buf.Bytes())
		}

		require.NoError(t, tw.WriteHeader(hdr))

		if flag == tar.TypeReg {
			_, err := tw.Write(e.body)
			require.NoError(t, err)
		}
	}

	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())

	return writeBundleFile(t, buf.Bytes())
}

func writeBundleFile(t *testing.T, body []byte) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "bloc-worker-bundle.tar.gz")
	require.NoError(t, os.WriteFile(path, body, 0o600))

	return path
}

// contractPart is one gzip member of a bundle as the join contract has the
// panel write it: one USTAR entry with the layout's mode and a time, and,
// for the last part, the tar's end. An earlier part is padded to the tar's
// block size and left open for the next part's entry.
func contractPart(t *testing.T, name string, body []byte, last bool) []byte {
	t.Helper()

	var buf bytes.Buffer

	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	require.NoError(t, tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg, Name: name, Mode: layoutMode(name), Size: int64(len(body)),
		ModTime: time.Date(2026, 11, 24, 20, 0, 0, 0, time.UTC), Format: tar.FormatUSTAR,
	}))

	_, err := tw.Write(body)
	require.NoError(t, err)

	if last {
		require.NoError(t, tw.Close())
	} else {
		require.NoError(t, tw.Flush())
	}

	require.NoError(t, gz.Close())

	return buf.Bytes()
}

// contractBundle is a bundle in the join contract's exact layout: two gzip
// members, the binary's entry first and then the join file's entry and
// the tar's end.
func contractBundle(t *testing.T, binary, join []byte) string {
	t.Helper()

	head := contractPart(t, workerunit.BundleBinary, binary, false)
	tail := contractPart(t, workerunit.BundleJoinFile, join, true)

	return writeBundleFile(t, append(head, tail...))
}

func encode(t *testing.T, doc map[string]any) []byte {
	t.Helper()

	b, err := json.Marshal(doc)
	require.NoError(t, err)

	return b
}

func goodArchive(t *testing.T) string {
	t.Helper()

	return writeArchive(t,
		entry{name: workerunit.BundleBinary, body: testBinary},
		entry{name: workerunit.BundleJoinFile, body: encode(t, joinDoc())},
	)
}

// A bundle built in the join contract's exact layout reads back, with both
// members intact.
func TestExtractBundle_ReadsTheContractLayout(t *testing.T) {
	t.Parallel()

	join := contractJoinFile(t)
	dir := t.TempDir()

	b, err := workerunit.ExtractBundle(contractBundle(t, testBinary, join), dir, workerunit.ArchAMD64)
	require.NoError(t, err)

	assert.Equal(t, "ocfp-lab-drgao", b.Join.Bloc)
	assert.Equal(t, 1, b.Join.Version)
	assert.Equal(t, sum(testBinary), b.BinarySHA256)
	assert.Equal(t, sum(join), b.JoinSHA256)

	got, err := os.ReadFile(b.JoinPath)
	require.NoError(t, err)
	assert.Equal(t, join, got, "the join file is installed byte for byte")

	bin, err := os.ReadFile(b.BinaryPath)
	require.NoError(t, err)
	assert.Equal(t, testBinary, bin)

	for _, p := range []string{b.JoinPath, b.BinaryPath} {
		info, err := os.Stat(p)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "%s is private to the operator", p)
	}
}

func TestExtractBundle_ReadsTheJoinFileAndBinary(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	b, err := workerunit.ExtractBundle(goodArchive(t), dir, "")
	require.NoError(t, err)

	assert.Equal(t, "ocfp-lab-drgao", b.Join.Bloc)
	assert.Equal(t, "acme", b.Join.Org)
	assert.Equal(t, testInstallation, b.Join.Installation)
	assert.Equal(t, "1.2.3", b.Join.Binary.Version)
	assert.Equal(t, sum(testBinary), b.BinarySHA256)

	joinBody, err := os.ReadFile(b.JoinPath)
	require.NoError(t, err)
	assert.Equal(t, encode(t, joinDoc()), joinBody, "the join file is installed byte for byte")
	assert.Equal(t, sum(joinBody), b.JoinSHA256)

	binBody, err := os.ReadFile(b.BinaryPath)
	require.NoError(t, err)
	assert.Equal(t, testBinary, binBody)

	for _, p := range []string{b.JoinPath, b.BinaryPath} {
		info, err := os.Stat(p)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "%s is private to the operator", p)
		assert.Equal(t, dir, filepath.Dir(p))
	}
}

func TestExtractBundle_SumFor(t *testing.T) {
	t.Parallel()

	b, err := workerunit.ExtractBundle(goodArchive(t), t.TempDir(), "")
	require.NoError(t, err)

	got, err := b.Join.SumFor("amd64")
	require.NoError(t, err)
	assert.Equal(t, sum(testBinary), got)

	_, err = b.Join.SumFor("riscv64")
	require.ErrorIs(t, err, workerunit.ErrBundlePlatform)
}

func TestExtractBundle_Refusals(t *testing.T) {
	t.Parallel()

	withJoin := func(change func(map[string]any)) []entry {
		doc := joinDoc()
		change(doc)

		return []entry{
			{name: workerunit.BundleBinary, body: testBinary},
			{name: workerunit.BundleJoinFile, body: encode(t, doc)},
		}
	}

	bin := entry{name: workerunit.BundleBinary, body: testBinary}
	join := entry{name: workerunit.BundleJoinFile, body: encode(t, joinDoc())}
	asJoin := func(e entry) []entry { return []entry{bin, e} }
	asBinary := func(e entry) []entry { return []entry{e, join} }

	cases := map[string]struct {
		entries []entry
		want    error
	}{
		"no join file": {
			entries: []entry{bin},
			want:    workerunit.ErrBundleMembers,
		},
		"no binary": {
			entries: []entry{join},
			want:    workerunit.ErrBundleMembers,
		},
		"nothing at all": {
			entries: nil,
			want:    workerunit.ErrBundleMembers,
		},
		"the members out of order": {
			entries: []entry{join, bin},
			want:    workerunit.ErrBundleMembers,
		},
		"an extra file": {
			entries: []entry{bin, join, {name: "README", body: []byte("hi")}},
			want:    workerunit.ErrBundleMembers,
		},
		"a directory": {
			entries: []entry{{name: "./", typeflag: tar.TypeDir}, bin, join},
			want:    workerunit.ErrBundleMembers,
		},
		"a dot-slash name": {
			entries: asJoin(entry{name: "./" + workerunit.BundleJoinFile, body: join.body}),
			want:    workerunit.ErrBundleMembers,
		},
		"a nested path": {
			entries: asJoin(entry{name: "x/" + workerunit.BundleJoinFile, body: join.body}),
			want:    workerunit.ErrBundleMembers,
		},
		"a path that climbs out": {
			entries: asJoin(entry{name: "../" + workerunit.BundleJoinFile, body: join.body}),
			want:    workerunit.ErrBundleUnsafePath,
		},
		"an absolute path": {
			entries: asJoin(entry{name: "/" + workerunit.BundleJoinFile, body: join.body}),
			want:    workerunit.ErrBundleUnsafePath,
		},
		"a backslash": {
			entries: asJoin(entry{name: `..\` + workerunit.BundleJoinFile, body: join.body}),
			want:    workerunit.ErrBundleUnsafePath,
		},
		"a drive letter": {
			entries: asJoin(entry{name: "C:" + workerunit.BundleJoinFile, body: join.body}),
			want:    workerunit.ErrBundleUnsafePath,
		},
		"a symlink": {
			entries: asBinary(entry{name: workerunit.BundleBinary, typeflag: tar.TypeSymlink}),
			want:    workerunit.ErrBundleLink,
		},
		"a hard link": {
			entries: asJoin(entry{name: workerunit.BundleJoinFile, typeflag: tar.TypeLink, linkname: workerunit.BundleBinary}),
			want:    workerunit.ErrBundleLink,
		},
		"a character device": {
			entries: asBinary(entry{name: workerunit.BundleBinary, typeflag: tar.TypeChar}),
			want:    workerunit.ErrBundleDevice,
		},
		"a block device": {
			entries: asBinary(entry{name: workerunit.BundleBinary, typeflag: tar.TypeBlock}),
			want:    workerunit.ErrBundleDevice,
		},
		"a FIFO": {
			entries: asJoin(entry{name: workerunit.BundleJoinFile, typeflag: tar.TypeFifo}),
			want:    workerunit.ErrBundleDevice,
		},
		"a repeated binary": {
			entries: []entry{bin, bin, join},
			want:    workerunit.ErrBundleDuplicate,
		},
		"a repeated join file": {
			entries: []entry{bin, join, join},
			want:    workerunit.ErrBundleDuplicate,
		},
		"an empty binary": {
			entries: asBinary(entry{name: workerunit.BundleBinary, body: []byte{}}),
			want:    workerunit.ErrBundleLayout,
		},
		"an empty join file": {
			entries: asJoin(entry{name: workerunit.BundleJoinFile, body: []byte{}}),
			want:    workerunit.ErrBundleLayout,
		},
		"a binary that is not mode 0755": {
			entries: asBinary(entry{name: workerunit.BundleBinary, body: testBinary, mode: 0o4755}),
			want:    workerunit.ErrBundleLayout,
		},
		"a join file that is not mode 0600": {
			entries: asJoin(entry{name: workerunit.BundleJoinFile, body: join.body, mode: 0o644}),
			want:    workerunit.ErrBundleLayout,
		},
		"a join file over 64 KiB": {
			entries: asJoin(entry{name: workerunit.BundleJoinFile, body: bytes.Repeat([]byte(" "), 64<<10+1)}),
			want:    workerunit.ErrBundleTooLarge,
		},
		"a join file whose header claims over 64 KiB": {
			entries: asJoin(entry{name: workerunit.BundleJoinFile, declared: 64<<10 + 1}),
			want:    workerunit.ErrBundleTooLarge,
		},
		"a binary whose header claims over 128 MiB": {
			entries: []entry{{name: workerunit.BundleBinary, declared: 128<<20 + 1}},
			want:    workerunit.ErrBundleTooLarge,
		},
		"a join file that is not JSON": {
			entries: asJoin(entry{name: workerunit.BundleJoinFile, body: []byte("{" + testSeed)}),
			want:    workerunit.ErrJoinFile,
		},
		"no join key": {
			entries: withJoin(func(d map[string]any) { delete(d, "joinKey") }),
			want:    workerunit.ErrJoinFile,
		},
		"a join key that is not a seed": {
			entries: withJoin(func(d map[string]any) { d["joinKey"] = "not-a-seed" }),
			want:    workerunit.ErrJoinFile,
		},
		"a bad bloc": {
			entries: withJoin(func(d map[string]any) { d["bloc"] = "Bad/Bloc" }),
			want:    workerunit.ErrJoinFile,
		},
		"no url": {
			entries: withJoin(func(d map[string]any) { d["url"] = "" }),
			want:    workerunit.ErrJoinFile,
		},
		"no org": {
			entries: withJoin(func(d map[string]any) { d["org"] = "" }),
			want:    workerunit.ErrJoinFile,
		},
		"no installation": {
			entries: withJoin(func(d map[string]any) { d["installation"] = "" }),
			want:    workerunit.ErrJoinFile,
		},
		"no join account": {
			entries: withJoin(func(d map[string]any) { d["joinAccount"] = "" }),
			want:    workerunit.ErrJoinFile,
		},
		"no sums": {
			entries: withJoin(func(d map[string]any) { d["binary"] = map[string]any{"version": "1"} }),
			want:    workerunit.ErrJoinFile,
		},
		"a sum that is not hex": {
			entries: withJoin(func(d map[string]any) {
				d["binary"] = map[string]any{"version": "1.2.3", "sha256": map[string]string{"amd64": "zz"}}
			}),
			want: workerunit.ErrJoinFile,
		},
		"a platform-style key": {
			entries: withJoin(func(d map[string]any) {
				d["binary"] = map[string]any{"version": "1.2.3", "sha256": map[string]string{"linux/amd64": sum(testBinary)}}
			}),
			want: workerunit.ErrBundleLayout,
		},
		"an unknown architecture key": {
			entries: withJoin(func(d map[string]any) {
				d["binary"] = map[string]any{"version": "1.2.3", "sha256": map[string]string{
					"amd64": sum(testBinary), "riscv64": strings.Repeat("ab", 32),
				}}
			}),
			want: workerunit.ErrBundleLayout,
		},
		"a binary no sum names": {
			entries: withJoin(func(d map[string]any) {
				d["binary"] = map[string]any{"version": "1.2.3", "sha256": map[string]string{"amd64": strings.Repeat("cd", 32)}}
			}),
			want: workerunit.ErrBundleSum,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()

			_, err := workerunit.ExtractBundle(writeArchive(t, tc.entries...), dir, "")
			require.ErrorIs(t, err, tc.want)

			left, readErr := os.ReadDir(dir)
			require.NoError(t, readErr)
			assert.Empty(t, left, "a refused bundle leaves nothing behind")
			assert.NotContains(t, err.Error(), testSeed, "a refusal never repeats the join key")
			assert.NotContains(t, err.Error(), testSeed[:12], "a refusal never repeats part of the join key")
		})
	}
}

func TestExtractBundle_RefusesWhatIsNotAnArchive(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "bundle.tar.gz")
	require.NoError(t, os.WriteFile(path, []byte(testSeed), 0o600))

	_, err := workerunit.ExtractBundle(path, t.TempDir(), "")
	require.ErrorIs(t, err, workerunit.ErrBundleLayout)
	assert.NotContains(t, err.Error(), testSeed)

	_, err = workerunit.ExtractBundle(filepath.Join(t.TempDir(), "missing.tar.gz"), t.TempDir(), "")
	require.Error(t, err)
}

func TestPlatform(t *testing.T) {
	t.Parallel()

	for machine, want := range map[string]string{
		"x86_64":  "amd64",
		"amd64":   "amd64",
		"aarch64": "arm64",
		"arm64":   "arm64",
	} {
		got, err := workerunit.Platform(machine)
		require.NoError(t, err, machine)
		assert.Equal(t, want, got, machine)
	}

	_, err := workerunit.Platform("riscv64")
	require.ErrorIs(t, err, workerunit.ErrBundlePlatform)
}

// A size cap is refused from the header alone, before a byte of the
// member is read or written.
func TestExtractBundle_RefusesAnOversizedMemberFromItsHeader(t *testing.T) {
	t.Parallel()

	bin := entry{name: workerunit.BundleBinary, body: testBinary}

	for name, entries := range map[string][]entry{
		"join file": {bin, {name: workerunit.BundleJoinFile, declared: 64<<10 + 1}},
		"binary":    {{name: workerunit.BundleBinary, declared: 128<<20 + 1}},
	} {
		dir := t.TempDir()

		_, err := workerunit.ExtractBundle(writeArchive(t, entries...), dir, "")
		require.ErrorIs(t, err, workerunit.ErrBundleTooLarge, name)
		assert.Contains(t, err.Error(), "past its bound", name)

		left, readErr := os.ReadDir(dir)
		require.NoError(t, readErr)
		assert.Empty(t, left, "%s: nothing is left for an oversized member", name)
	}
}

// The archive's modes are checked against the layout but never applied.
// The extracted files are private to the operator, and the install
// scripts set the modes on the bastion themselves.
func TestExtractBundle_NeverAppliesTheArchivesModes(t *testing.T) {
	t.Parallel()

	b, err := workerunit.ExtractBundle(goodArchive(t), t.TempDir(), "")
	require.NoError(t, err)

	info, err := os.Stat(b.BinaryPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode(), "the binary's 0755 in the archive is not applied")
}

// The stream may hold only zero bytes after the tar's end.
func TestExtractBundle_RefusesDataAfterTheTarsEnd(t *testing.T) {
	t.Parallel()

	good, err := os.ReadFile(contractBundle(t, testBinary, contractJoinFile(t)))
	require.NoError(t, err)

	var extra bytes.Buffer

	gz := gzip.NewWriter(&extra)
	_, err = gz.Write([]byte("after the end"))
	require.NoError(t, err)
	require.NoError(t, gz.Close())

	dir := t.TempDir()

	_, err = workerunit.ExtractBundle(writeBundleFile(t, append(good, extra.Bytes()...)), dir, "")
	require.ErrorIs(t, err, workerunit.ErrBundleLayout)

	left, readErr := os.ReadDir(dir)
	require.NoError(t, readErr)
	assert.Empty(t, left, "a refused bundle leaves nothing behind")

	var zeros bytes.Buffer

	gz = gzip.NewWriter(&zeros)
	_, err = gz.Write(make([]byte, 4096))
	require.NoError(t, err)
	require.NoError(t, gz.Close())

	_, err = workerunit.ExtractBundle(writeBundleFile(t, append(good, zeros.Bytes()...)), t.TempDir(), "")
	require.NoError(t, err, "zero bytes after the tar's end are allowed")
}

// Given the bastion's architecture, the reader refuses a binary that is
// not the one the join file lists for it, before anything reaches the
// bastion. With no architecture, as in a dry run that never asks the
// bastion, the binary need only be listed under some architecture.
func TestExtractBundle_ChecksTheBastionsArchitecture(t *testing.T) {
	t.Parallel()

	amd64Only := func(d map[string]any) {
		d["binary"] = map[string]any{"version": "1.2.3", "sha256": map[string]string{"amd64": sum(testBinary)}}
	}

	archive := func(change func(map[string]any)) string {
		doc := joinDoc()
		if change != nil {
			change(doc)
		}

		return writeArchive(t,
			entry{name: workerunit.BundleBinary, body: testBinary},
			entry{name: workerunit.BundleJoinFile, body: encode(t, doc)},
		)
	}

	cases := map[string]struct {
		path string
		arch string
		want error
	}{
		"the listed architecture":                  {path: archive(nil), arch: workerunit.ArchAMD64},
		"any architecture in a dry run":            {path: archive(nil), arch: ""},
		"an architecture the join file omits":      {path: archive(amd64Only), arch: workerunit.ArchARM64, want: workerunit.ErrBundleSum},
		"a binary listed for another architecture": {path: archive(nil), arch: workerunit.ArchARM64, want: workerunit.ErrBundleArch},
		"an architecture that is not a key":        {path: archive(nil), arch: "x86_64", want: workerunit.ErrBundleLayout},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := workerunit.ExtractBundle(tc.path, t.TempDir(), tc.arch)
			if tc.want == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tc.want)
			assert.NotContains(t, err.Error(), testSeed)
		})
	}
}

// The bounds are the join contract's: 128 MiB for the binary, 64 KiB for
// the join file, and the two summed plus one MiB for the archive.
func TestExtractBundle_HoldsTheContractsBounds(t *testing.T) {
	t.Parallel()

	assert.Equal(t, [3]int64{134217728, 65536, 135331840}, workerunit.ContractBounds)
}

// The archive's bound holds the compressed bytes read from disk.
func TestExtractBundle_RefusesACompressedArchivePastItsBound(t *testing.T) {
	t.Parallel()

	path := goodArchive(t)

	info, err := os.Stat(path)
	require.NoError(t, err)

	dir := t.TempDir()

	_, err = workerunit.ExtractBundleWithin(path, dir, "", 1<<20, 1<<20, info.Size()-1)
	require.ErrorIs(t, err, workerunit.ErrBundleTooLarge)

	left, readErr := os.ReadDir(dir)
	require.NoError(t, readErr)
	assert.Empty(t, left, "a refused bundle leaves nothing behind")

	_, err = workerunit.ExtractBundleWithin(goodArchive(t), t.TempDir(), "", 1<<20, 1<<20, 1<<20)
	require.NoError(t, err, "an archive within its bound reads")
}

// The archive's bound also holds the tar stream it decompresses to, so a
// small archive that inflates past it is refused.
func TestExtractBundle_RefusesATarStreamPastItsBound(t *testing.T) {
	t.Parallel()

	good, err := os.ReadFile(goodArchive(t))
	require.NoError(t, err)

	var zeros bytes.Buffer

	gz := gzip.NewWriter(&zeros)
	_, err = gz.Write(make([]byte, 1<<20))
	require.NoError(t, err)
	require.NoError(t, gz.Close())

	inflates := append(good, zeros.Bytes()...)
	require.Less(t, len(inflates), 64<<10, "the archive itself is small")

	dir := t.TempDir()

	_, err = workerunit.ExtractBundleWithin(writeBundleFile(t, inflates), dir, "", 1<<20, 1<<20, 512<<10)
	require.ErrorIs(t, err, workerunit.ErrBundleTooLarge)

	left, readErr := os.ReadDir(dir)
	require.NoError(t, readErr)
	assert.Empty(t, left, "a refused bundle leaves nothing behind")
}

// A tar writer cannot encode some of the names the reader refuses, so the
// name check is pinned on its own.
func TestExtractBundle_RefusesUnsafeNames(t *testing.T) {
	t.Parallel()

	for _, name := range []string{
		"", "/join.json", "../join.json", "a/../join.json", "a/..", `a\join.json`,
		"C:join.json", "c:/join.json", "join.json\x00", "join\x00.json",
	} {
		assert.True(t, workerunit.UnsafeName(name), "%q", name)
	}

	for _, name := range []string{"join.json", "bloc-worker", "./join.json", "a/b", "..join.json"} {
		assert.False(t, workerunit.UnsafeName(name), "%q", name)
	}
}
