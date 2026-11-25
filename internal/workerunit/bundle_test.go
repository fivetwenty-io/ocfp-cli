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

		hdr := &tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.body)), Typeflag: flag}
		if flag != tar.TypeReg {
			hdr.Size = 0
		}

		if flag == tar.TypeSymlink {
			hdr.Linkname = "/etc/passwd"
		}

		require.NoError(t, tw.WriteHeader(hdr))

		if flag == tar.TypeReg {
			_, err := tw.Write(e.body)
			require.NoError(t, err)
		}
	}

	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())

	path := filepath.Join(t.TempDir(), "bloc-worker-bundle.tar.gz")
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o600))

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

	b, err := workerunit.ExtractBundle(goodArchive(t), dir)
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

	_, err := workerunit.ExtractBundle(path, t.TempDir())
	require.NoError(t, err)
}

func TestExtractBundle_SumFor(t *testing.T) {
	t.Parallel()

	b, err := workerunit.ExtractBundle(goodArchive(t), t.TempDir())
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

			_, err := workerunit.ExtractBundle(writeArchive(t, tc.entries...), dir)
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

	_, err := workerunit.ExtractBundle(path, t.TempDir())
	require.ErrorIs(t, err, workerunit.ErrBundleLayout)
	assert.NotContains(t, err.Error(), testSeed)

	_, err = workerunit.ExtractBundle(filepath.Join(t.TempDir(), "missing.tar.gz"), t.TempDir())
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
