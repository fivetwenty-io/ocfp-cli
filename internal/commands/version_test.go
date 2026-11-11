package commands

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
	"github.com/fivetwenty-io/ocfp-cli/internal/version"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runVersion(t *testing.T, args ...string) string {
	t.Helper()

	cmd := NewVersionCmd()

	var out bytes.Buffer

	cmd.SetOut(&out)
	cmd.SetArgs(args)
	require.NoError(t, cmd.Execute())

	return out.String()
}

func TestVersionOutputJSON(t *testing.T) {
	out := runVersion(t, "--output", "json")

	require.Equal(t, 1, strings.Count(out, "\n"), "json output is one line: %q", out)

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &got))

	for _, k := range []string{"version", "buildTime", "gitCommit", "goVersion", "os", "arch", "wireContract"} {
		assert.Contains(t, got, k)
	}

	assert.Len(t, got, 7)
	assert.EqualValues(t, progress.WireContract, got["wireContract"])
	assert.Equal(t, version.Get().Version, got["version"])
}

func TestVersionOutputTextUnchanged(t *testing.T) {
	assert.Equal(t, version.Get().String()+"\n", runVersion(t))
	assert.Equal(t, version.Get().String()+"\n", runVersion(t, "--output", "text"))
}

func TestVersionOutputRejectsUnknownFormat(t *testing.T) {
	cmd := NewVersionCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--output", "yaml"})
	assert.Error(t, cmd.Execute())
}
