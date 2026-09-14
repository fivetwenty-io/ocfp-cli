package commands

import (
	"io"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newRSyncTestRoot wires the rsync command under a root that carries the same
// --bloc persistent flag the real root command carries, so that a test can
// parse a whole command line the way the CLI parses it.
func newRSyncTestRoot(t *testing.T) (*cobra.Command, *cobra.Command) {
	t.Helper()

	viper.Reset()
	t.Cleanup(viper.Reset)

	root := &cobra.Command{Use: "ocfp"} //nolint:exhaustruct // Only the fields a test needs are set.
	root.PersistentFlags().String("bloc", "", "bloc name")

	rsyncCmd := NewRSyncCmd()
	root.AddCommand(rsyncCmd)

	return root, rsyncCmd
}

// parseRSyncLine parses one command line and returns the argv that ocfp would
// hand to rsync, with a fixed bastion IP and key path so the result is stable.
func parseRSyncLine(t *testing.T, args []string) []string {
	t.Helper()

	_, rsyncCmd := newRSyncTestRoot(t)

	err := rsyncCmd.ParseFlags(args)
	require.NoError(t, err)

	viper.Set("bloc", "ocfp-cf1-lab")

	config, err := parseRSyncConfig(rsyncCmd, rsyncCmd.Flags().Args())
	require.NoError(t, err)

	source := processRSyncPath(config.source, "10.61.148.10", config.user)
	destination := processRSyncPath(config.destination, "10.61.148.10", config.user)

	return buildRSyncCommand(config, source, destination, "/tmp/id_ed25519")
}

// indexOf reports where value first appears in argv, or -1 when it is absent.
func indexOf(argv []string, value string) int {
	for i, arg := range argv {
		if arg == value {
			return i
		}
	}

	return -1
}

// excludePatterns pulls the pattern that follows every --exclude in argv, in
// the order the patterns appear.
func excludePatterns(argv []string) []string {
	patterns := []string{}

	for i, arg := range argv {
		if arg == "--exclude" && i+1 < len(argv) {
			patterns = append(patterns, argv[i+1])
		}
	}

	return patterns
}

// TestRSyncPassesUnmodelledFlagThrough proves that a flag ocfp does not model
// reaches the rsync argv when it is written after the -- separator.
func TestRSyncPassesUnmodelledFlagThrough(t *testing.T) {
	argv := parseRSyncLine(t, []string{
		"--dry-run",
		"/local/dir/", "bastion:/remote/dir/",
		"--", "--itemize-changes", "--info=progress2", "--copy-links",
	})

	assert.Contains(t, argv, "--itemize-changes")
	assert.Contains(t, argv, "--info=progress2")
	assert.Contains(t, argv, "--copy-links")

	itemize := indexOf(argv, "--itemize-changes")
	info := indexOf(argv, "--info=progress2")
	copyLinks := indexOf(argv, "--copy-links")

	assert.Less(t, itemize, info, "passthrough flags keep the order they were written in")
	assert.Less(t, info, copyLinks, "passthrough flags keep the order they were written in")

	require.GreaterOrEqual(t, len(argv), 2)
	assert.Equal(t, []string{"/local/dir/", "ubuntu@10.61.148.10:/remote/dir/"}, argv[len(argv)-2:],
		"the source and the destination stay last")
	assert.Less(t, copyLinks, len(argv)-2, "passthrough flags come before the source and the destination")
}

// TestRSyncPassesUnknownFlagThroughWithItsValue proves that a passthrough flag
// taking a separate value keeps that value, since nothing after the separator
// is interpreted.
func TestRSyncPassesUnknownFlagThroughWithItsValue(t *testing.T) {
	argv := parseRSyncLine(t, []string{
		"/local/dir/", "bastion:/remote/dir/",
		"--", "--max-size", "10m", "--chmod=D2775,F664",
	})

	maxSize := indexOf(argv, "--max-size")
	require.NotEqual(t, -1, maxSize)
	assert.Equal(t, "10m", argv[maxSize+1])
	assert.Contains(t, argv, "--chmod=D2775,F664")
}

// TestRSyncItemizeChangesIsModelled proves --itemize-changes is accepted as an
// ocfp flag in its own right, without a separator, and reaches the rsync argv.
func TestRSyncItemizeChangesIsModelled(t *testing.T) {
	argv := parseRSyncLine(t, []string{
		"--dry-run", "--itemize-changes",
		"/local/dir/", "bastion:/remote/dir/",
	})

	assert.Contains(t, argv, "--itemize-changes")
	assert.Contains(t, argv, "--dry-run")
}

// TestRSyncRepeatedExcludeKeepsOrder proves repeated --exclude patterns reach
// rsync in the order they were written.
func TestRSyncRepeatedExcludeKeepsOrder(t *testing.T) {
	argv := parseRSyncLine(t, []string{
		"--exclude", ".genesis", "--exclude=dev", "--exclude", ".git",
		"/local/dir/", "bastion:/remote/dir/",
	})

	assert.Equal(t, []string{".genesis", "dev", ".git"}, excludePatterns(argv))
}

// TestRSyncDoesNotForwardBlocFlag proves ocfp's own flags stay with ocfp and
// never reach rsync.
func TestRSyncDoesNotForwardBlocFlag(t *testing.T) {
	argv := parseRSyncLine(t, []string{
		"--bloc", "ocfp-cf1-lab", "--user", "vcap", "--dry-run",
		"/local/dir/", "bastion:/remote/dir/",
	})

	assert.NotContains(t, argv, "--bloc")
	assert.NotContains(t, argv, "ocfp-cf1-lab")
	assert.NotContains(t, argv, "--user")
	assert.NotContains(t, argv, "--key")
	assert.NotContains(t, argv, "--rsync-options")
	assert.Contains(t, argv, "--dry-run")
	assert.Equal(t, "vcap@10.61.148.10:/remote/dir/", argv[len(argv)-1])
}

// TestRSyncTheFieldCommandLine runs the command line that first failed in the
// field, with the separator added, and checks every part of it lands.
func TestRSyncTheFieldCommandLine(t *testing.T) {
	argv := parseRSyncLine(t, []string{
		"--bloc", "ocfp-cf1-lab", "--dry-run",
		"--exclude", ".genesis", "--exclude", "dev", "--exclude", ".git",
		"/home/user/w/ocfp/deployments/inf-ocfp-deployments/", "bastion:ocfp/deployments/",
		"--", "--itemize-changes",
	})

	assert.Equal(t, "rsync", argv[0])
	assert.Contains(t, argv, "--dry-run")
	assert.Contains(t, argv, "--itemize-changes")
	assert.Equal(t, []string{".genesis", "dev", ".git"}, excludePatterns(argv))
	assert.Equal(t, "ubuntu@10.61.148.10:ocfp/deployments/", argv[len(argv)-1])
}

// TestRSyncKeepsSSHOptions proves the ssh command rsync is told to use is
// unchanged by the passthrough.
func TestRSyncKeepsSSHOptions(t *testing.T) {
	argv := parseRSyncLine(t, []string{
		"/local/dir/", "bastion:/remote/dir/", "--", "--itemize-changes",
	})

	dashE := indexOf(argv, "-e")
	require.NotEqual(t, -1, dashE)
	assert.Equal(t,
		"ssh -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no -o LogLevel=ERROR -i /tmp/id_ed25519",
		argv[dashE+1])
}

// TestRSyncArgsRequireSourceAndDestination proves the argument check counts
// only what sits in front of the separator.
func TestRSyncArgsRequireSourceAndDestination(t *testing.T) {
	_, rsyncCmd := newRSyncTestRoot(t)

	require.Error(t, rsyncCmd.Args(rsyncCmd, []string{}))
	require.Error(t, rsyncCmd.Args(rsyncCmd, []string{"source"}))
	require.NoError(t, rsyncCmd.Args(rsyncCmd, []string{"source", "dest"}))
}

// TestRSyncArgsCountOnlyBeforeTheSeparator proves passthrough arguments are
// not mistaken for a third and fourth positional argument.
func TestRSyncArgsCountOnlyBeforeTheSeparator(t *testing.T) {
	_, rsyncCmd := newRSyncTestRoot(t)

	args := []string{"/local/dir/", "bastion:/remote/dir/", "--", "--itemize-changes", "--copy-links"}

	err := rsyncCmd.ParseFlags(args)
	require.NoError(t, err)

	parsed := rsyncCmd.Flags().Args()
	assert.Equal(t, 2, rsyncCmd.ArgsLenAtDash())
	assert.Equal(t, []string{"/local/dir/", "bastion:/remote/dir/"}, rsyncPositionalArgs(rsyncCmd, parsed))
	assert.Equal(t, []string{"--itemize-changes", "--copy-links"}, rsyncPassthroughArgs(rsyncCmd, parsed))
	require.NoError(t, rsyncCmd.Args(rsyncCmd, parsed))
}

// TestRSyncFlagErrorMentionsTheSeparator proves an unknown flag written
// without the separator tells the caller how to pass it.
func TestRSyncFlagErrorMentionsTheSeparator(t *testing.T) {
	root, _ := newRSyncTestRoot(t)

	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SilenceUsage = true
	root.SilenceErrors = true
	root.SetArgs([]string{"rsync", "--no-such-rsync-flag", "/local/", "bastion:/remote/"})

	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown flag")
	assert.Contains(t, err.Error(), "-- separator")
}

// TestRSyncLongDescriptionDocumentsTheSeparator keeps the help text honest
// about the passthrough.
func TestRSyncLongDescriptionDocumentsTheSeparator(t *testing.T) {
	_, rsyncCmd := newRSyncTestRoot(t)

	assert.Contains(t, rsyncCmd.Long, "-- separator")
	assert.Contains(t, rsyncCmd.Use, "--")
}
