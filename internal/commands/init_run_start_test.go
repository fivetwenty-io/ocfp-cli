package commands

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A dry run's start error is the one the real run gets from starting the
// command, so it refuses with the real run's error wherever starting the
// command would fail, and passes wherever it would start.
func TestDryRunStartErrorMatchesStartingTheCommand(t *testing.T) {
	bin := t.TempDir()
	writeFakeExecutableFile(t, filepath.Join(bin, "tool"), "#!/bin/sh\nexit 0\n")
	t.Setenv("PATH", bin)

	base := t.TempDir()
	file := filepath.Join(base, "file")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o600))

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	cases := []struct {
		name, dir, tool string
		ctx             context.Context //nolint:containedctx // each case starts the command under its own context
		wantErr         bool
	}{
		{"starts in a directory", base, "tool", context.Background(), false},
		{"starts with no directory", "", "tool", context.Background(), false},
		{"tool not on PATH", base, "missing-tool", context.Background(), true},
		{"tool not on PATH and no directory", filepath.Join(base, "missing"), "missing-tool", context.Background(), true},
		{"directory missing", filepath.Join(base, "missing"), "tool", context.Background(), true},
		{"directory under a file", filepath.Join(file, "sub"), "tool", context.Background(), true},
		{"directory is a file", file, "tool", context.Background(), true},
		{"context cancelled", base, "tool", cancelled, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.CommandContext(tc.ctx, tc.tool, "arg") // #nosec G204 -- fixed test tool names
			cmd.Dir = tc.dir
			realErr := cmd.Run()

			dryErr := newInitRun(true).startError(tc.ctx, tc.dir, tc.tool, "arg")

			if !tc.wantErr {
				require.NoError(t, realErr)
				assert.NoError(t, dryErr)

				return
			}

			require.Error(t, realErr)
			require.Error(t, dryErr)
			assert.Equal(t, realErr.Error(), dryErr.Error())
		})
	}
}

// A directory that the dry run's earlier steps would have created counts as
// present, so the dry run doesn't refuse where the real run would have made
// the directory first.
func TestDryRunStartErrorCountsDirectoriesItWouldCreate(t *testing.T) {
	bin := t.TempDir()
	writeFakeExecutableFile(t, filepath.Join(bin, "tool"), "#!/bin/sh\nexit 0\n")
	t.Setenv("PATH", bin)

	dir := filepath.Join(t.TempDir(), "made-later")
	r := newInitRun(true)
	r.createsDir(dir)

	assert.NoError(t, r.startError(context.Background(), dir, "tool"))
}
