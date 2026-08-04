package commands

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/pve/stemcell"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stemcellsJSON builds the JSON that `bosh stemcells --json` returns.
// Each entry in rows is a map with "Name" and "Version" keys.
func stemcellsJSON(rows []map[string]string) []byte {
	type table struct {
		Rows []map[string]string `json:"Rows"`
	}

	type output struct {
		Tables []table `json:"Tables"`
	}

	v := output{Tables: []table{{Rows: rows}}}

	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}

	return b
}

// fakeStemcellBuilder returns a stemcellUploadBuilder that:
//   - RunBosh: calls boshFn each time, in call order, and — when capturedArgs
//     is non-nil — appends a copy of each call's args to it for assertion.
//   - SHA1Fetcher: calls fetchFn (may be nil; nil means test asserts it is not called).
func fakeStemcellBuilder(boshResponses []boshResponse, fetchFn stemcell.SHA1Fetcher, capturedArgs *[][]string) stemcellUploadBuilder {
	return func(_, sha1Override string) (stemcell.RunBosh, stemcell.SHA1Fetcher) {
		callIdx := 0

		runBosh := func(_ context.Context, args ...string) ([]byte, error) {
			if capturedArgs != nil {
				*capturedArgs = append(*capturedArgs, append([]string{}, args...))
			}

			if callIdx >= len(boshResponses) {
				panic("RunBosh called more times than expected")
			}

			resp := boshResponses[callIdx]
			callIdx++

			return resp.out, resp.err
		}

		// When the builder is given a non-empty sha1Override (mirroring production),
		// the fetcher ignores its arguments and returns the override. Tests that set
		// --sha1 must also mirror this by returning the override from their fetchFn.
		// For cleaner assertions, tests pass nil when they want to assert no call.
		if fetchFn == nil {
			fetchFn = func(_ context.Context, _, _ string) (string, error) {
				panic("SHA1Fetcher must not be called when --sha1 is provided")
			}
		}

		return runBosh, fetchFn
	}
}

// boshResponse pairs an output payload with an optional error for RunBosh fakes.
type boshResponse struct {
	out []byte
	err error
}

// resolveCall records one invocation of a fake stemcellResolveFunc.
type resolveCall struct {
	name, version, upstreamURL, expectedSHA1 string
	force                                    bool
}

// fakeStemcellResolverBuilder returns a stemcellResolverBuilder that always
// succeeds and hands back resolveFn, ignoring f (tests assert on f
// separately when needed).
func fakeStemcellResolverBuilder(resolveFn stemcellResolveFunc) stemcellResolverBuilder {
	return func(_ context.Context, _ *stemcellUploadFlags) (stemcellResolveFunc, error) {
		return resolveFn, nil
	}
}

// neverCalledResolverBuilder returns a stemcellResolverBuilder that panics if
// invoked — for tests asserting the already-uploaded path never touches the
// blobstore.
func neverCalledResolverBuilder() stemcellResolverBuilder {
	return func(_ context.Context, _ *stemcellUploadFlags) (stemcellResolveFunc, error) {
		panic("resolverBuilder must not be called")
	}
}

// TestPVEStemcellUpload_NoArgs_Usage verifies that running upload with fewer than
// three positional args returns an error (cobra.ExactArgs(3) enforcement).
func TestPVEStemcellUpload_NoArgs_Usage(t *testing.T) {
	builder := fakeStemcellBuilder(nil, func(_ context.Context, _, _ string) (string, error) {
		return "sha", nil
	}, nil)

	cmd := newPVEStemcellUploadCmdWithBuilder(builder, neverCalledResolverBuilder())
	cmd.SetArgs([]string{"only-one-arg", "v1"}) // missing url

	var sb strings.Builder
	cmd.SetOut(&sb)
	cmd.SetErr(&sb)

	err := cmd.Execute()
	require.Error(t, err, "upload with fewer than 3 args must return error")
}

// panicResolverBuilder returns a stemcellResolverBuilder that panics if invoked —
// used to assert that empty-arg validation fails fast before any blobstore
// resolution or bosh call is attempted.
func panicResolverBuilder() stemcellResolverBuilder {
	return func(_ context.Context, _ *stemcellUploadFlags) (stemcellResolveFunc, error) {
		panic("resolverBuilder must not be called for an empty-arg validation failure")
	}
}

// panicStemcellBuilder returns a stemcellUploadBuilder that panics if RunBosh
// or SHA1Fetcher is invoked — used to assert that empty-arg validation fails
// fast before any bosh call is attempted.
func panicStemcellBuilder() stemcellUploadBuilder {
	return func(_, _ string) (stemcell.RunBosh, stemcell.SHA1Fetcher) {
		runBosh := func(_ context.Context, args ...string) ([]byte, error) {
			panic("RunBosh must not be called for an empty-arg validation failure")
		}
		fetchSHA1 := func(_ context.Context, _, _ string) (string, error) {
			panic("SHA1Fetcher must not be called for an empty-arg validation failure")
		}

		return runBosh, fetchSHA1
	}
}

// TestPVEStemcellUpload_EmptyName_Errors verifies that an empty name argument
// (cobra.ExactArgs(3) validates count only, not non-emptiness) fails fast
// with a clear error, before any bosh or blobstore call.
func TestPVEStemcellUpload_EmptyName_Errors(t *testing.T) {
	cmd := newPVEStemcellUploadCmdWithBuilder(panicStemcellBuilder(), panicResolverBuilder())
	cmd.SetArgs([]string{"", "1.584", "https://example.com/stemcell.tgz"})

	var sb strings.Builder
	cmd.SetOut(&sb)
	cmd.SetErr(&sb)

	err := cmd.Execute()
	require.Error(t, err, "empty name must return an error")
	assert.Contains(t, err.Error(), "name", "error must mention the offending argument")
}

// TestPVEStemcellUpload_EmptyVersion_Errors verifies that an empty version
// argument fails fast with a clear error, before any bosh or blobstore call.
func TestPVEStemcellUpload_EmptyVersion_Errors(t *testing.T) {
	cmd := newPVEStemcellUploadCmdWithBuilder(panicStemcellBuilder(), panicResolverBuilder())
	cmd.SetArgs([]string{"bosh-openstack-kvm-ubuntu-noble-go_agent", "", "https://example.com/stemcell.tgz"})

	var sb strings.Builder
	cmd.SetOut(&sb)
	cmd.SetErr(&sb)

	err := cmd.Execute()
	require.Error(t, err, "empty version must return an error")
	assert.Contains(t, err.Error(), "version", "error must mention the offending argument")
}

// TestPVEStemcellUpload_EmptyURL_Errors verifies that an empty url argument
// fails fast with a clear error, before any bosh or blobstore call.
func TestPVEStemcellUpload_EmptyURL_Errors(t *testing.T) {
	cmd := newPVEStemcellUploadCmdWithBuilder(panicStemcellBuilder(), panicResolverBuilder())
	cmd.SetArgs([]string{"bosh-openstack-kvm-ubuntu-noble-go_agent", "1.584", ""})

	var sb strings.Builder
	cmd.SetOut(&sb)
	cmd.SetErr(&sb)

	err := cmd.Execute()
	require.Error(t, err, "empty url must return an error")
	assert.Contains(t, err.Error(), "url", "error must mention the offending argument")
}

// TestPVEStemcellUpload_AllArgsEmpty_ReportsFirstOffender verifies that when
// all three args are empty, the error names the first offender (name) rather
// than a generic message.
func TestPVEStemcellUpload_AllArgsEmpty_ReportsFirstOffender(t *testing.T) {
	cmd := newPVEStemcellUploadCmdWithBuilder(panicStemcellBuilder(), panicResolverBuilder())
	cmd.SetArgs([]string{"", "", ""})

	var sb strings.Builder
	cmd.SetOut(&sb)
	cmd.SetErr(&sb)

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name")
}

// TestPVEStemcellUpload_AlreadyUploaded_NoOp verifies that when the stemcell is already
// present in the director, no upload-stemcell call and no blobstore resolve happens,
// and the command reports "already uploaded; skipping".
func TestPVEStemcellUpload_AlreadyUploaded_NoOp(t *testing.T) {
	rows := []map[string]string{
		{"Name": "bosh-openstack-kvm-ubuntu-noble-go_agent", "Version": "1.584"},
	}

	boshResps := []boshResponse{
		{out: stemcellsJSON(rows), err: nil}, // stemcells --json (IsStemcellUploaded check)
		// No second call expected — upload must not happen.
	}

	fetchCalled := false
	fetchFn := func(_ context.Context, _, _ string) (string, error) {
		fetchCalled = true
		return "sha1abc", nil
	}

	builder := fakeStemcellBuilder(boshResps, fetchFn, nil)

	cmd := newPVEStemcellUploadCmdWithBuilder(builder, neverCalledResolverBuilder())
	cmd.SetArgs([]string{
		"bosh-openstack-kvm-ubuntu-noble-go_agent",
		"1.584",
		"https://example.com/stemcell.tgz",
		"--env", "pve",
	})

	var stdout, stderr strings.Builder
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	require.NoError(t, err, "already-present stemcell must not return error")

	outStr := stdout.String()
	assert.Contains(t, outStr, "already uploaded; skipping", "stdout must report skip")
	assert.Contains(t, outStr, "bosh-openstack-kvm-ubuntu-noble-go_agent", "stdout must name the stemcell")
	assert.Contains(t, outStr, "1.584", "stdout must include the version")
	assert.Empty(t, stderr.String(), "stderr must be empty on skip")
	assert.False(t, fetchCalled, "SHA1Fetcher must not be called when stemcell is already uploaded")
}

// TestPVEStemcellUpload_AbsentAndUploaded verifies that when the stemcell is absent,
// the command fetches the SHA1, resolves the tarball through the blobstore cache
// (passing the raw arg URL as upstreamURL and the fetched SHA1 as expectedSHA1),
// and calls `bosh upload-stemcell --sha1 <sha1> <blobstore-url>` — the blobstore
// URL returned by the resolver, never the raw arg URL.
func TestPVEStemcellUpload_AbsentAndUploaded(t *testing.T) {
	const (
		rawURL       = "https://example.com/stemcell.tgz"
		blobstoreURL = "https://rustfs.internal/testbloc-ocf-bosh/stemcells/bosh-openstack-kvm-ubuntu-noble-go_agent-1.584.tgz"
	)

	// First bosh call: stemcells --json → empty (absent).
	// Second bosh call: upload-stemcell → success.
	boshResps := []boshResponse{
		{out: stemcellsJSON(nil), err: nil},  // IsStemcellUploaded pre-check
		{out: []byte("upload ok"), err: nil}, // upload-stemcell
	}

	fetchCalled := false
	fetchFn := func(_ context.Context, name, version string) (string, error) {
		fetchCalled = true
		assert.Equal(t, "bosh-openstack-kvm-ubuntu-noble-go_agent", name)
		assert.Equal(t, "1.584", version)
		return "deadbeef", nil
	}

	var boshCalls [][]string
	builder := fakeStemcellBuilder(boshResps, fetchFn, &boshCalls)

	var resolveCalls []resolveCall
	resolverBuilder := fakeStemcellResolverBuilder(func(_ context.Context, name, version, upstreamURL, expectedSHA1 string, force bool) (string, string, error) {
		resolveCalls = append(resolveCalls, resolveCall{name, version, upstreamURL, expectedSHA1, force})
		return blobstoreURL, "cachedsha256", nil
	})

	cmd := newPVEStemcellUploadCmdWithBuilder(builder, resolverBuilder)
	cmd.SetArgs([]string{
		"bosh-openstack-kvm-ubuntu-noble-go_agent",
		"1.584",
		rawURL,
		"--env", "pve",
	})

	var stdout, stderr strings.Builder
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	require.NoError(t, err, "successful upload must return nil error")

	outStr := stdout.String()
	assert.Contains(t, outStr, "upload complete", "stdout must report completion")
	assert.Contains(t, outStr, "1.584", "stdout must include version")
	assert.Empty(t, stderr.String(), "stderr must be empty on success")
	assert.True(t, fetchCalled, "SHA1Fetcher must be called when stemcell is absent")

	require.Len(t, resolveCalls, 1, "the resolver must be called exactly once on a cache miss")
	rc := resolveCalls[0]
	assert.Equal(t, rawURL, rc.upstreamURL, "resolver must receive the raw arg URL as upstreamURL")
	assert.Equal(t, "deadbeef", rc.expectedSHA1, "resolver must receive the fetched sha1 as expectedSHA1")
	assert.False(t, rc.force, "force must default to false")

	require.Len(t, boshCalls, 2, "expected exactly two bosh calls: precheck + upload")
	upload := boshCalls[1]
	require.Contains(t, upload, "upload-stemcell")
	assert.Contains(t, upload, blobstoreURL, "bosh upload-stemcell must be called with the blobstore URL")
	assert.NotContains(t, upload, rawURL, "bosh upload-stemcell must NOT be called with the raw arg URL")

	require.Contains(t, upload, "--sha1")
	for i, a := range upload {
		if a == "--sha1" {
			require.Greater(t, len(upload), i+1)
			assert.Equal(t, "deadbeef", upload[i+1], "bosh upload-stemcell --sha1 must use the fetched sha1")
		}
	}
}

// TestPVEStemcellUpload_CacheHit_StillUsesResolverURL verifies that a resolver
// reporting a cache hit (present already, no upstream download performed by
// the resolver — that hit/miss decision has its own dedicated unit tests in
// internal/precompile) is still the exclusive source of the URL handed to
// bosh; runPVEStemcellUpload performs no separate upstream fetch itself.
func TestPVEStemcellUpload_CacheHit_StillUsesResolverURL(t *testing.T) {
	const (
		rawURL       = "https://example.com/stemcell.tgz"
		blobstoreURL = "https://rustfs.internal/testbloc-ocf-bosh/stemcells/bosh-openstack-kvm-ubuntu-noble-go_agent-1.584.tgz"
	)

	boshResps := []boshResponse{
		{out: stemcellsJSON(nil), err: nil},  // IsStemcellUploaded pre-check: absent on director
		{out: []byte("upload ok"), err: nil}, // upload-stemcell
	}

	fetchFn := func(_ context.Context, _, _ string) (string, error) {
		return "cachedsha1", nil
	}

	var boshCalls [][]string
	builder := fakeStemcellBuilder(boshResps, fetchFn, &boshCalls)

	resolveCalled := 0
	resolverBuilder := fakeStemcellResolverBuilder(func(_ context.Context, _, _, _, _ string, _ bool) (string, string, error) {
		resolveCalled++
		// Cache hit: resolver returns the existing cached URL without this
		// test simulating any upstream HTTP interaction at all.
		return blobstoreURL, "cachedsha256", nil
	})

	cmd := newPVEStemcellUploadCmdWithBuilder(builder, resolverBuilder)
	cmd.SetArgs([]string{
		"bosh-openstack-kvm-ubuntu-noble-go_agent",
		"1.584",
		rawURL,
		"--env", "pve",
	})

	var stdout, stderr strings.Builder
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	require.NoError(t, err)
	assert.Equal(t, 1, resolveCalled, "resolver called once regardless of hit/miss — the miss/hit distinction is internal to it")

	upload := boshCalls[1]
	assert.Contains(t, upload, blobstoreURL, "bosh upload-stemcell must use the resolver's URL on a cache hit too")
	assert.NotContains(t, upload, rawURL, "the raw upstream URL must never reach bosh directly")
}

// TestPVEStemcellUpload_Force_PassedThrough verifies --force flows into the
// resolver's force argument.
func TestPVEStemcellUpload_Force_PassedThrough(t *testing.T) {
	boshResps := []boshResponse{
		{out: stemcellsJSON(nil), err: nil},
		{out: []byte("upload ok"), err: nil},
	}

	fetchFn := func(_ context.Context, _, _ string) (string, error) {
		return "sha1", nil
	}

	builder := fakeStemcellBuilder(boshResps, fetchFn, nil)

	var gotForce bool
	resolverBuilder := fakeStemcellResolverBuilder(func(_ context.Context, _, _, _, _ string, force bool) (string, string, error) {
		gotForce = force
		return "https://rustfs.internal/x.tgz", "sha256", nil
	})

	cmd := newPVEStemcellUploadCmdWithBuilder(builder, resolverBuilder)
	cmd.SetArgs([]string{
		"bosh-openstack-kvm-ubuntu-noble-go_agent",
		"1.584",
		"https://example.com/stemcell.tgz",
		"--force",
	})

	var stdout, stderr strings.Builder
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	require.NoError(t, cmd.Execute())
	assert.True(t, gotForce, "--force must be passed through to the resolver")
}

// TestPVEStemcellUpload_SHA1Override_SkipsFetch verifies that when --sha1 is provided,
// the SHA1Fetcher is never invoked (the override is used directly) and the override
// value flows into both the resolver's expectedSHA1 and the final bosh --sha1 arg.
func TestPVEStemcellUpload_SHA1Override_SkipsFetch(t *testing.T) {
	const blobstoreURL = "https://rustfs.internal/testbloc-ocf-bosh/stemcells/bosh-openstack-kvm-ubuntu-noble-go_agent-1.584.tgz"

	boshResps := []boshResponse{
		{out: stemcellsJSON(nil), err: nil},  // pre-check: absent
		{out: []byte("upload ok"), err: nil}, // upload-stemcell
	}

	// We need a builder that honours the sha1Override the same way defaultStemcellUploadBuilder
	// does. Because fakeStemcellBuilder does not inspect sha1Override, we use a custom
	// builder here that returns the override directly.
	var boshCalls [][]string

	overrideBuilder := func(_, sha1Override string) (stemcell.RunBosh, stemcell.SHA1Fetcher) {
		callIdx := 0

		runBosh := func(_ context.Context, args ...string) ([]byte, error) {
			boshCalls = append(boshCalls, append([]string{}, args...))

			resp := boshResps[callIdx]
			callIdx++

			return resp.out, resp.err
		}

		fetchFn := func(_ context.Context, _, _ string) (string, error) {
			// sha1Override comes from --sha1 flag, bound in f.sha1.
			// The builder receives it and returns it directly.
			return sha1Override, nil
		}

		return runBosh, fetchFn
	}

	var resolveCalls []resolveCall
	resolverBuilder := fakeStemcellResolverBuilder(func(_ context.Context, name, version, upstreamURL, expectedSHA1 string, force bool) (string, string, error) {
		resolveCalls = append(resolveCalls, resolveCall{name, version, upstreamURL, expectedSHA1, force})
		return blobstoreURL, "sha256", nil
	})

	cmd2 := newPVEStemcellUploadCmdWithBuilder(overrideBuilder, resolverBuilder)
	cmd2.SetArgs([]string{
		"bosh-openstack-kvm-ubuntu-noble-go_agent",
		"1.584",
		"https://example.com/stemcell.tgz",
		"--sha1", "override123",
		"--env", "pve",
	})

	var stdout, stderr strings.Builder
	cmd2.SetOut(&stdout)
	cmd2.SetErr(&stderr)

	err := cmd2.Execute()
	require.NoError(t, err, "upload with --sha1 must succeed")

	assert.Contains(t, stdout.String(), "upload complete")
	assert.Empty(t, stderr.String())

	require.Len(t, resolveCalls, 1)
	assert.Equal(t, "override123", resolveCalls[0].expectedSHA1, "resolver must receive the sha1 override as expectedSHA1")

	require.Len(t, boshCalls, 2)
	upload := boshCalls[1]
	assert.Contains(t, upload, blobstoreURL)
	assert.Contains(t, upload, "override123")
}

// TestNewPVEStemcellCmd_RegisteredUnderPVE verifies that NewPVECmd() includes "stemcell"
// as a registered subcommand.
func TestNewPVEStemcellCmd_RegisteredUnderPVE(t *testing.T) {
	pve := NewPVECmd()
	require.NotNil(t, pve)

	var found bool

	for _, sub := range pve.Commands() {
		if strings.HasPrefix(sub.Use, "stemcell") {
			found = true
			break
		}
	}

	assert.True(t, found, "pve command must have 'stemcell' subcommand registered via AddCommand")
}

// TestDefaultStemcellResolverBuilder_BlocRequired verifies that resolving the
// blobstore without a --bloc (flag empty, persistent --bloc/OCFP_BLOC unset)
// fails fast with ErrStemcellBlocRequired before any blobstore lookup.
func TestDefaultStemcellResolverBuilder_BlocRequired(t *testing.T) {
	prev := viper.GetString("bloc")
	viper.Set("bloc", "")

	t.Cleanup(func() { viper.Set("bloc", prev) })

	f := &stemcellUploadFlags{}

	_, err := defaultStemcellResolverBuilder(context.Background(), f)
	require.ErrorIs(t, err, ErrStemcellBlocRequired)
}
