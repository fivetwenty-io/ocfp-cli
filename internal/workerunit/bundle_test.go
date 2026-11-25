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
	// mode is the header's mode, 0644 when it is zero.
	mode int64
	// declared, when set, is a size the header claims with no body
	// written after it, and it ends the archive there.
	declared int64
	linkname string
}

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
			mode = 0o644
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

func encode(t *testing.T, doc map[string]any) []byte {
	t.Helper()

	b, err := json.Marshal(doc)
	require.NoError(t, err)

	return b
}

func goodArchive(t *testing.T) string {
	t.Helper()

	return writeArchive(t,
		entry{name: workerunit.BundleJoinFile, body: encode(t, joinDoc())},
		entry{name: workerunit.BundleBinary, body: testBinary},
	)
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

// tar -C dir -czf bundle.tar.gz . writes a ./ entry and ./ prefixes.
func TestExtractBundle_AcceptsDotSlashNames(t *testing.T) {
	t.Parallel()

	path := writeArchive(t,
		entry{name: "./", typeflag: tar.TypeDir},
		entry{name: "./" + workerunit.BundleBinary, body: testBinary},
		entry{name: "./" + workerunit.BundleJoinFile, body: encode(t, joinDoc())},
	)

	_, err := workerunit.ExtractBundle(path, t.TempDir(), "")
	require.NoError(t, err)
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
			{name: workerunit.BundleJoinFile, body: encode(t, doc)},
			{name: workerunit.BundleBinary, body: testBinary},
		}
	}

	cases := map[string]struct {
		entries []entry
		want    error
	}{
		"no join file": {
			entries: []entry{{name: workerunit.BundleBinary, body: testBinary}},
			want:    workerunit.ErrBundleLayout,
		},
		"no binary": {
			entries: []entry{{name: workerunit.BundleJoinFile, body: encode(t, joinDoc())}},
			want:    workerunit.ErrBundleLayout,
		},
		"an extra file": {
			entries: append(withJoin(func(map[string]any) {}), entry{name: "README", body: []byte("hi")}),
			want:    workerunit.ErrBundleLayout,
		},
		"a nested path": {
			entries: []entry{
				{name: "x/" + workerunit.BundleJoinFile, body: encode(t, joinDoc())},
				{name: workerunit.BundleBinary, body: testBinary},
			},
			want: workerunit.ErrBundleLayout,
		},
		"a path that climbs out": {
			entries: []entry{
				{name: "../" + workerunit.BundleJoinFile, body: encode(t, joinDoc())},
				{name: workerunit.BundleBinary, body: testBinary},
			},
			want: workerunit.ErrBundleLayout,
		},
		"a symlink": {
			entries: []entry{
				{name: workerunit.BundleJoinFile, body: encode(t, joinDoc())},
				{name: workerunit.BundleBinary, typeflag: tar.TypeSymlink},
			},
			want: workerunit.ErrBundleLayout,
		},
		"an absolute path": {
			entries: []entry{
				{name: "/" + workerunit.BundleJoinFile, body: encode(t, joinDoc())},
				{name: workerunit.BundleBinary, body: testBinary},
			},
			want: workerunit.ErrBundleLayout,
		},
		"a hard link": {
			entries: []entry{
				{name: workerunit.BundleJoinFile, body: encode(t, joinDoc())},
				{name: workerunit.BundleBinary, typeflag: tar.TypeLink, linkname: workerunit.BundleJoinFile},
			},
			want: workerunit.ErrBundleLayout,
		},
		"a character device": {
			entries: []entry{
				{name: workerunit.BundleJoinFile, body: encode(t, joinDoc())},
				{name: workerunit.BundleBinary, typeflag: tar.TypeChar},
			},
			want: workerunit.ErrBundleLayout,
		},
		"a block device": {
			entries: []entry{
				{name: workerunit.BundleJoinFile, body: encode(t, joinDoc())},
				{name: workerunit.BundleBinary, typeflag: tar.TypeBlock},
			},
			want: workerunit.ErrBundleLayout,
		},
		"a FIFO": {
			entries: []entry{
				{name: workerunit.BundleJoinFile, typeflag: tar.TypeFifo},
				{name: workerunit.BundleBinary, body: testBinary},
			},
			want: workerunit.ErrBundleLayout,
		},
		"a join file over 64 KiB": {
			entries: []entry{
				{name: workerunit.BundleJoinFile, body: bytes.Repeat([]byte(" "), 64<<10+1)},
				{name: workerunit.BundleBinary, body: testBinary},
			},
			want: workerunit.ErrBundleLayout,
		},
		"a join file whose header claims over 64 KiB": {
			entries: []entry{{name: workerunit.BundleJoinFile, declared: 64<<10 + 1}},
			want:    workerunit.ErrBundleLayout,
		},
		"a binary whose header claims over 512 MiB": {
			entries: []entry{
				{name: workerunit.BundleJoinFile, body: encode(t, joinDoc())},
				{name: workerunit.BundleBinary, declared: 512<<20 + 1},
			},
			want: workerunit.ErrBundleLayout,
		},
		"a repeated entry": {
			entries: append(withJoin(func(map[string]any) {}), entry{name: workerunit.BundleBinary, body: testBinary}),
			want:    workerunit.ErrBundleLayout,
		},
		"a join file that is not JSON": {
			entries: []entry{
				{name: workerunit.BundleJoinFile, body: []byte("{" + testSeed)},
				{name: workerunit.BundleBinary, body: testBinary},
			},
			want: workerunit.ErrJoinFile,
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

	for name, e := range map[string]entry{
		"join file": {name: workerunit.BundleJoinFile, declared: 64<<10 + 1},
		"binary":    {name: workerunit.BundleBinary, declared: 512<<20 + 1},
	} {
		dir := t.TempDir()

		_, err := workerunit.ExtractBundle(writeArchive(t, e), dir, "")
		require.ErrorIs(t, err, workerunit.ErrBundleLayout, name)
		assert.Contains(t, err.Error(), "longer than", name)

		left, readErr := os.ReadDir(dir)
		require.NoError(t, readErr)
		assert.Empty(t, left, "%s: nothing is written for an oversized member", name)
	}
}

// The archive's modes are never trusted. Whatever a header says, the
// extracted files are private to the operator, and the install scripts set
// the modes on the bastion themselves.
func TestExtractBundle_IgnoresTheArchivesModes(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := writeArchive(t,
		entry{name: workerunit.BundleJoinFile, body: encode(t, joinDoc()), mode: 0o6777},
		entry{name: workerunit.BundleBinary, body: testBinary, mode: 0o4755},
	)

	b, err := workerunit.ExtractBundle(path, dir, "")
	require.NoError(t, err)

	for _, p := range []string{b.JoinPath, b.BinaryPath} {
		info, err := os.Stat(p)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode()&(os.ModePerm|os.ModeSetuid|os.ModeSetgid|os.ModeSticky), p)
	}
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
			entry{name: workerunit.BundleJoinFile, body: encode(t, doc)},
			entry{name: workerunit.BundleBinary, body: testBinary},
		)
	}

	cases := map[string]struct {
		path string
		arch string
		want error
	}{
		"the listed architecture":                  {path: archive(nil), arch: workerunit.ArchAMD64},
		"any architecture in a dry run":            {path: archive(nil), arch: ""},
		"an architecture the join file omits":      {path: archive(amd64Only), arch: workerunit.ArchARM64, want: workerunit.ErrBundlePlatform},
		"a binary listed for another architecture": {path: archive(nil), arch: workerunit.ArchARM64, want: workerunit.ErrBundleArch},
		"an architecture that is not a key":        {path: archive(nil), arch: "x86_64", want: workerunit.ErrBundlePlatform},
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
