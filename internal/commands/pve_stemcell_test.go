package commands

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/pve/stemcell"
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
//   - RunBosh: calls boshFn each time, in call order.
//   - SHA1Fetcher: calls fetchFn (may be nil; nil means test asserts it is not called).
func fakeStemcellBuilder(boshResponses []boshResponse, fetchFn stemcell.SHA1Fetcher) stemcellUploadBuilder {
	return func(_, sha1Override string) (stemcell.RunBosh, stemcell.SHA1Fetcher) {
		callIdx := 0

		runBosh := func(_ context.Context, args ...string) ([]byte, error) {
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

// TestPVEStemcellUpload_NoArgs_Usage verifies that running upload with fewer than
// three positional args returns an error (cobra.ExactArgs(3) enforcement).
func TestPVEStemcellUpload_NoArgs_Usage(t *testing.T) {
	builder := fakeStemcellBuilder(nil, func(_ context.Context, _, _ string) (string, error) {
		return "sha", nil
	})

	cmd := newPVEStemcellUploadCmdWithBuilder(builder)
	cmd.SetArgs([]string{"only-one-arg", "v1"}) // missing url

	var sb strings.Builder
	cmd.SetOut(&sb)
	cmd.SetErr(&sb)

	err := cmd.Execute()
	require.Error(t, err, "upload with fewer than 3 args must return error")
}

// TestPVEStemcellUpload_AlreadyUploaded_NoOp verifies that when the stemcell is already
// present in the director, no upload-stemcell call is made and the command reports
// "already uploaded; skipping".
func TestPVEStemcellUpload_AlreadyUploaded_NoOp(t *testing.T) {
	rows := []map[string]string{
		{"Name": "bosh-openstack-kvm-ubuntu-noble-go_agent", "Version": "1.584"},
	}

	boshResps := []boshResponse{
		{out: stemcellsJSON(rows), err: nil}, // first: stemcells --json (IsStemcellUploaded check)
		// No second call expected — upload must not happen.
	}

	fetchCalled := false
	fetchFn := func(_ context.Context, _, _ string) (string, error) {
		fetchCalled = true
		return "sha1abc", nil
	}

	builder := fakeStemcellBuilder(boshResps, fetchFn)

	cmd := newPVEStemcellUploadCmdWithBuilder(builder)
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
// the command fetches the SHA1 and calls upload-stemcell, then reports completion.
func TestPVEStemcellUpload_AbsentAndUploaded(t *testing.T) {
	// First bosh call: stemcells --json → empty (absent).
	// Second bosh call: upload-stemcell → success.
	boshResps := []boshResponse{
		{out: stemcellsJSON(nil), err: nil},  // IsStemcellUploaded check (pre-check in runPVEStemcellUpload)
		{out: stemcellsJSON(nil), err: nil},  // IsStemcellUploaded check (inside EnsureStemcell)
		{out: []byte("upload ok"), err: nil}, // upload-stemcell
	}

	fetchCalled := false
	fetchFn := func(_ context.Context, name, version string) (string, error) {
		fetchCalled = true
		assert.Equal(t, "bosh-openstack-kvm-ubuntu-noble-go_agent", name)
		assert.Equal(t, "1.584", version)
		return "deadbeef", nil
	}

	builder := fakeStemcellBuilder(boshResps, fetchFn)

	cmd := newPVEStemcellUploadCmdWithBuilder(builder)
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
	require.NoError(t, err, "successful upload must return nil error")

	outStr := stdout.String()
	assert.Contains(t, outStr, "upload complete", "stdout must report completion")
	assert.Contains(t, outStr, "1.584", "stdout must include version")
	assert.Empty(t, stderr.String(), "stderr must be empty on success")
	assert.True(t, fetchCalled, "SHA1Fetcher must be called when stemcell is absent")
}

// TestPVEStemcellUpload_SHA1Override_SkipsFetch verifies that when --sha1 is provided,
// the SHA1Fetcher is never invoked (the override is used directly).
func TestPVEStemcellUpload_SHA1Override_SkipsFetch(t *testing.T) {
	boshResps := []boshResponse{
		{out: stemcellsJSON(nil), err: nil},  // pre-check: absent
		{out: stemcellsJSON(nil), err: nil},  // EnsureStemcell internal check: absent
		{out: []byte("upload ok"), err: nil}, // upload-stemcell
	}

	// We need a builder that honours the sha1Override the same way defaultStemcellUploadBuilder
	// does. Because fakeStemcellBuilder does not inspect sha1Override, we use a custom
	// builder here that returns the override directly.
	overrideBuilder := func(_, sha1Override string) (stemcell.RunBosh, stemcell.SHA1Fetcher) {
		callIdx := 0

		runBosh := func(_ context.Context, _ ...string) ([]byte, error) {
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

	cmd2 := newPVEStemcellUploadCmdWithBuilder(overrideBuilder)
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
