package workerunit_test

import (
	"encoding/json"
	"encoding/pem"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fivetwenty-io/ocfp-cli/internal/workerunit"
)

// testCA is a PEM certificate block. The join contract checks that caPEM
// holds only certificate blocks, and does not parse what they hold.
var testCA = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("pretend certificate")}))

// contractJoinFile is a join file written to the join contract's exact
// shape, with its fields in the contract's order and caPEM present.
func contractJoinFile(t *testing.T) []byte {
	t.Helper()

	ca, err := json.Marshal(testCA)
	require.NoError(t, err)

	return fmt.Appendf(nil, `{
  "version": 1,
  "url": "wss://manager.example.com:443/nats",
  "caPEM": %s,
  "installation": %q,
  "org": "acme",
  "bloc": "ocfp-lab-drgao",
  "joinAccount": %q,
  "joinKey": %q,
  "binary": {
    "version": "v1.2.3",
    "sha256": {
      "amd64": %q,
      "arm64": %q
    }
  },
  "issuedAt": "2026-11-24T20:00:00.123456789Z"
}
`, ca, testInstallation, testAccount, testSeed, sum(testBinary), strings.Repeat("ab", 32))
}

func TestDecodeJoinFile_ReadsTheContractShape(t *testing.T) {
	t.Parallel()

	join, err := workerunit.DecodeJoinFile(contractJoinFile(t))
	require.NoError(t, err)

	assert.Equal(t, workerunit.JoinFile{
		Version:      1,
		URL:          "wss://manager.example.com:443/nats",
		CAPEM:        testCA,
		Installation: testInstallation,
		Org:          "acme",
		Bloc:         "ocfp-lab-drgao",
		JoinAccount:  testAccount,
		Binary: workerunit.Binary{
			Version: "v1.2.3",
			SHA256: map[string]string{
				"amd64": sum(testBinary),
				"arm64": strings.Repeat("ab", 32),
			},
		},
		IssuedAt: time.Date(2026, 11, 24, 20, 0, 0, 123456789, time.UTC),
	}, join)

	for _, out := range []string{fmt.Sprintf("%v", join), fmt.Sprintf("%+v", join), fmt.Sprintf("%#v", join)} {
		assert.NotContains(t, out, testSeed, "the decoded join file holds no copy of the join key")
	}
}

func TestDecodeJoinFile_HoldsToTheContract(t *testing.T) {
	t.Parallel()

	change := func(edit func(map[string]any)) []byte {
		var doc map[string]any
		require.NoError(t, json.Unmarshal(contractJoinFile(t), &doc))
		edit(doc)

		return encode(t, doc)
	}

	cases := map[string]struct {
		body []byte
		want error
	}{
		"an unknown field": {
			body: change(func(d map[string]any) { d["extra"] = "x" }),
			want: workerunit.ErrJoinFile,
		},
		"trailing data": {
			body: append(contractJoinFile(t), []byte(`{"joinKey":"`+testSeed+`"}`)...),
			want: workerunit.ErrJoinFile,
		},
		"a url that is not wss": {
			body: change(func(d map[string]any) { d["url"] = "https://manager.example.com" }),
			want: workerunit.ErrJoinFile,
		},
		"a url with no host": {
			body: change(func(d map[string]any) { d["url"] = "wss:///nats" }),
			want: workerunit.ErrJoinFile,
		},
		"a url with user info": {
			body: change(func(d map[string]any) { d["url"] = "wss://user:pass@manager.example.com" }),
			want: workerunit.ErrJoinFile,
		},
		"a url with a query": {
			body: change(func(d map[string]any) { d["url"] = "wss://manager.example.com/?x=1" }),
			want: workerunit.ErrJoinFile,
		},
		"a url with a fragment": {
			body: change(func(d map[string]any) { d["url"] = "wss://manager.example.com/#x" }),
			want: workerunit.ErrJoinFile,
		},
		"a CA that holds a key": {
			body: change(func(d map[string]any) {
				d["caPEM"] = testCA + string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("k")}))
			}),
			want: workerunit.ErrJoinFile,
		},
		"a CA that is not PEM": {
			body: change(func(d map[string]any) { d["caPEM"] = "not pem" }),
			want: workerunit.ErrJoinFile,
		},
		"an installation out of form": {
			body: change(func(d map[string]any) { d["installation"] = "inst-1" }),
			want: workerunit.ErrJoinFile,
		},
		"an org out of form": {
			body: change(func(d map[string]any) { d["org"] = "Acme" }),
			want: workerunit.ErrJoinFile,
		},
		"a short org": {
			body: change(func(d map[string]any) { d["org"] = "ac" }),
			want: workerunit.ErrJoinFile,
		},
		"a bloc longer than 40": {
			body: change(func(d map[string]any) { d["bloc"] = "ocfp-" + strings.Repeat("a", 36) }),
			want: workerunit.ErrJoinFile,
		},
		"a user key as the join account": {
			body: change(func(d map[string]any) { d["joinAccount"] = testUser }),
			want: workerunit.ErrJoinFile,
		},
		"a join account with a bad checksum": {
			body: change(func(d map[string]any) { d["joinAccount"] = testAccount[:55] + "A" }),
			want: workerunit.ErrJoinFile,
		},
		"a user seed as the join key": {
			body: change(func(d map[string]any) { d["joinKey"] = testUserSeed }),
			want: workerunit.ErrJoinFile,
		},
		"a join key with a bad checksum": {
			body: change(func(d map[string]any) { d["joinKey"] = testSeed[:57] + "A" }),
			want: workerunit.ErrJoinFile,
		},
		"a join key that is not a string": {
			body: change(func(d map[string]any) { d["joinKey"] = 7 }),
			want: workerunit.ErrJoinFile,
		},
		"no binary version": {
			body: change(func(d map[string]any) { d["binary"].(map[string]any)["version"] = "" }),
			want: workerunit.ErrJoinFile,
		},
		"a binary version out of form": {
			body: change(func(d map[string]any) { d["binary"].(map[string]any)["version"] = "1 2" }),
			want: workerunit.ErrJoinFile,
		},
		"an unknown binary field": {
			body: change(func(d map[string]any) { d["binary"].(map[string]any)["url"] = "x" }),
			want: workerunit.ErrJoinFile,
		},
		"no issue time": {
			body: change(func(d map[string]any) { delete(d, "issuedAt") }),
			want: workerunit.ErrJoinFile,
		},
		"an issue time out of form": {
			body: change(func(d map[string]any) { d["issuedAt"] = "yesterday" }),
			want: workerunit.ErrJoinFile,
		},
		"a file longer than 64 KiB": {
			body: change(func(d map[string]any) { d["caPEM"] = strings.Repeat(testCA, 64<<10/len(testCA)+1) }),
			want: workerunit.ErrJoinFile,
		},
		"no version": {
			body: change(func(d map[string]any) { delete(d, "version") }),
			want: workerunit.ErrJoinFile,
		},
		"version zero": {
			body: change(func(d map[string]any) { d["version"] = 0 }),
			want: workerunit.ErrJoinFile,
		},
		"a version that is not a number": {
			body: change(func(d map[string]any) { d["version"] = "1" }),
			want: workerunit.ErrJoinFile,
		},
		"a newer version": {
			body: change(func(d map[string]any) { d["version"] = 2 }),
			want: workerunit.ErrJoinFileTooNew,
		},
		"a newer version with fields this side does not know": {
			body: change(func(d map[string]any) {
				d["version"] = 2
				d["relay"] = "wss://relay.example.com"
			}),
			want: workerunit.ErrJoinFileTooNew,
		},
		"an architecture the contract does not name": {
			body: change(func(d map[string]any) {
				d["binary"].(map[string]any)["sha256"] = map[string]string{"linux/amd64": sum(testBinary)}
			}),
			want: workerunit.ErrBundleLayout,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := workerunit.DecodeJoinFile(tc.body)
			require.ErrorIs(t, err, tc.want)
			assert.NotContains(t, err.Error(), testSeed, "a refusal never repeats the join key")
			assert.NotContains(t, err.Error(), testSeed[:12], "a refusal never repeats part of the join key")
		})
	}
}

func TestDecodeJoinFile_AcceptsNoCA(t *testing.T) {
	t.Parallel()

	var doc map[string]any
	require.NoError(t, json.Unmarshal(contractJoinFile(t), &doc))
	delete(doc, "caPEM")

	join, err := workerunit.DecodeJoinFile(encode(t, doc))
	require.NoError(t, err)
	assert.Empty(t, join.CAPEM)
}

// A join file newer than this ocfp reads is refused with a message that
// says this ocfp is too old, and the refusal is still a join file refusal.
func TestDecodeJoinFile_ANewerVersionSaysUpgrade(t *testing.T) {
	t.Parallel()

	var doc map[string]any
	require.NoError(t, json.Unmarshal(contractJoinFile(t), &doc))
	doc["version"] = 3

	_, err := workerunit.DecodeJoinFile(encode(t, doc))
	require.ErrorIs(t, err, workerunit.ErrJoinFileTooNew)
	require.ErrorIs(t, err, workerunit.ErrJoinFile)
	assert.Contains(t, err.Error(), "version 3")
	assert.Contains(t, err.Error(), "this ocfp is too old")
	assert.NotContains(t, err.Error(), testSeed)
}
