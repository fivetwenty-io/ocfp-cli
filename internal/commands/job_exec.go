package commands

import (
	"fmt"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/fivetwenty-io/ocfp-cli/internal/jobexec"
	"github.com/fivetwenty-io/ocfp-cli/internal/ownerrecord"
	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
	"github.com/spf13/cobra"
)

// ownerStoreFunc opens the owner record store in dir.
type ownerStoreFunc func(dir string) *ownerrecord.DirStore

// NewJobCmd creates the job command, which runs signed jobs on a bastion.
func NewJobCmd() *cobra.Command {
	return newJobCmd(ownerrecord.NewDirStore, ownerrecord.DefaultDir)
}

// newJobCmd creates the job command with newStore to open the owner record
// store and ownerDefault as the default of --owner-dir.
func newJobCmd(newStore ownerStoreFunc, ownerDefault string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "job",
		Short: "Run signed jobs on a bastion",
		Long: `Run signed jobs on a bastion.

A job request is signed by the instance that the bastion's owner record
names. The job command checks every request against that record before it
runs anything.`,
	}

	cmd.AddCommand(newJobExecCmd(newStore, ownerDefault))

	return cmd
}

func newJobExecCmd(newStore ownerStoreFunc, ownerDefault string) *cobra.Command {
	var spoolRoot, ownerDir string

	cmd := &cobra.Command{
		Use:   "exec",
		Short: "Run one signed job request from stdin",
		Long: `Read one signed job request as a line of JSON on stdin, check it against
the owner record, run it, and write its progress stream to stdout.

The job keeps its own copy of the stream in a spool under --spool-root, so
a job whose caller goes away still runs to the end. Sending the same request
again attaches to that job and replays its stream from the start.

A request that is not signed by the owner, that names an org or a bloc
other than the ones the owner record names, or that the owner record cannot
vouch for, is refused before anything runs: the stream carries the refusal
code, stderr names it, and the command exits 2. The command exits 0 when the
job succeeds and 1 for any other failure.

An --owner-dir that is not the default as written, but that leads to the
default directory or to a directory under it, is refused before the request
is read, and the command exits 1. So is a --spool-root that leads to the
owner directory in use or to a directory under it, that holds the owner
directory in use, or that lies inside the default directory. A flag that
names a symbolic link to a directory that does not exist yet is refused the
same way, and so is any flag when the directory it is compared with fails
its own check.`,
		Example:       `  ocfp job exec --spool-root /data/ocfp-jobs < request.json`,
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// A caller that goes away closes the read end of stdout, and a
			// lost terminal or ssh session sends SIGHUP. The job must then
			// get an error from its next write and finish into the spool,
			// rather than be killed by either signal.
			signal.Ignore(syscall.SIGPIPE, syscall.SIGHUP)

			if err := absoluteFlag("--spool-root", spoolRoot); err != nil {
				return failed(cmd, "job exec", 1, err)
			}

			if err := absoluteFlag("--owner-dir", ownerDir); err != nil {
				return failed(cmd, "job exec", 1, err)
			}

			if err := refuseDefaultReach("--owner-dir", ownerDir, ownerDefault); err != nil {
				return failed(cmd, "job exec", 1, err)
			}

			if err := refuseSpoolOverlap(spoolRoot, ownerDir, ownerDefault); err != nil {
				return failed(cmd, "job exec", 1, err)
			}

			err := jobexec.Run(cmd.Context(), jobexec.Options{
				SpoolRoot: spoolRoot,
				Owner:     newStore(ownerDir),
				Stdin:     cmd.InOrStdin(),
				Stdout:    cmd.OutOrStdout(),
			})
			if err == nil {
				return nil
			}

			code := 1
			if _, ok := progress.AsRefusal(err); ok {
				code = 2
			}

			return failed(cmd, "job exec", code, err)
		},
	}

	cmd.Flags().StringVar(&spoolRoot, "spool-root", "", "absolute path of the directory that holds one spool per job")
	cmd.Flags().StringVar(&ownerDir, "owner-dir", ownerDefault, "absolute path of the directory that holds the owner record")
	_ = cmd.MarkFlagRequired("spool-root")

	return cmd
}

// absoluteFlag refuses a flag whose path is not absolute, because a job runs
// from whatever directory its caller left it in.
func absoluteFlag(flag, path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("%s must be an absolute path, not %q", flag, path)
	}

	return nil
}

// failed writes what failed to stderr and returns an *ExitError with code.
func failed(cmd *cobra.Command, what string, code int, err error) error {
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "%s: %v\n", what, err)

	return &ExitError{Code: code, Err: err}
}
