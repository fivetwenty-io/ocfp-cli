package commands

import (
	"errors"
	"fmt"
	"io"

	"github.com/fivetwenty-io/ocfp-cli/internal/ownerrecord"
	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
	"github.com/spf13/cobra"
)

// maxOwnerInput is the most the owner-record commands read from stdin. A
// stored record is never larger, so neither is a valid input.
const maxOwnerInput = 1 << 20

// NewOwnerRecordCmd creates the owner-record command, which writes and
// releases the bastion's owner record.
func NewOwnerRecordCmd() *cobra.Command {
	return newOwnerRecordCmd(ownerrecord.NewDirStore, ownerrecord.DefaultDir)
}

// newOwnerRecordCmd creates the owner-record command with newStore to open
// the store and defaultDir as the default of --dir.
func newOwnerRecordCmd(newStore ownerStoreFunc, defaultDir string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "owner-record",
		Short: "Write or release the bastion's owner record",
		Long: `Write or release the bastion's owner record.

The owner record names the one instance whose signed job requests this
bastion runs, and the org and bloc this bastion serves. Each subcommand
reads a signed form as one JSON document on stdin. The signed format is
versioned and will gain fields.

A --dir that is not the default as written, but that leads to the default
directory or to a directory under it, is refused and exits 1. So is a --dir
that names a symbolic link to a directory that does not exist yet, and any
--dir but the default as written when a directory or link above the nearest
existing directory on the default directory's path fails the ownership and
mode check.

A refusal names its code on stderr and exits 2. Any other failure exits 1.`,
	}

	cmd.AddCommand(newOwnerRecordWriteCmd(newStore, defaultDir), newOwnerRecordReleaseCmd(newStore, defaultDir))

	return cmd
}

func newOwnerRecordWriteCmd(newStore ownerStoreFunc, defaultDir string) *cobra.Command {
	var (
		dir    string
		expect uint64
	)

	cmd := &cobra.Command{
		Use:   "write",
		Short: "Store a signed owner record from stdin",
		Long: `Read a signed owner record on stdin, check it, and store it.

Without --expect-generation the record is stored only when there is none
yet. With it, the stored record must be at that generation, the new record
must be at the next one, and both must name the same owner key, org, and
bloc. Moving the bastion to another org or bloc takes a release and then a
write without --expect-generation.`,
		Example: `  ocfp owner-record write < owner.json
  ocfp owner-record write --expect-generation 4 < owner.json`,
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			const what = "owner-record write"

			sf, err := readSignedForm(cmd, what, dir, defaultDir)
			if err != nil {
				return err
			}

			var exp *uint64
			if cmd.Flags().Changed("expect-generation") {
				exp = &expect
			}

			return ownerFailed(cmd, what, newStore(dir).Replace(cmd.Context(), exp, sf))
		},
	}

	cmd.Flags().StringVar(&dir, "dir", defaultDir, "absolute path of the directory that holds the owner record")
	cmd.Flags().Uint64Var(&expect, "expect-generation", 0, "generation of the stored record this one replaces")

	return cmd
}

func newOwnerRecordReleaseCmd(newStore ownerStoreFunc, defaultDir string) *cobra.Command {
	var (
		dir    string
		expect uint64
	)

	cmd := &cobra.Command{
		Use:   "release",
		Short: "Remove the owner record with its owner's signed release",
		Long: `Read the owner's signed release on stdin and remove the stored owner
record.

The release must be signed by the stored record's owner key, must name the
stored record, and --expect-generation must be the stored generation. A
release for any other record, or by any other key, leaves the record in
place.`,
		Example:       `  ocfp owner-record release --expect-generation 4 < release.json`,
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			const what = "owner-record release"

			sf, err := readSignedForm(cmd, what, dir, defaultDir)
			if err != nil {
				return err
			}

			return ownerFailed(cmd, what, newStore(dir).Release(cmd.Context(), expect, sf))
		},
	}

	cmd.Flags().StringVar(&dir, "dir", defaultDir, "absolute path of the directory that holds the owner record")
	cmd.Flags().Uint64Var(&expect, "expect-generation", 0, "generation of the stored record")
	_ = cmd.MarkFlagRequired("expect-generation")

	return cmd
}

// readSignedForm checks dir against defaultDir, the default of --dir, and
// reads and parses the signed form on stdin. Its error has already been
// written to stderr.
func readSignedForm(cmd *cobra.Command, what, dir, defaultDir string) (ownerrecord.Signed, error) {
	if err := absoluteFlag("--dir", dir); err != nil {
		return ownerrecord.Signed{}, failed(cmd, what, 1, err)
	}

	if err := refuseDefaultReach("--dir", dir, defaultDir); err != nil {
		return ownerrecord.Signed{}, failed(cmd, what, 1, err)
	}

	data, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), maxOwnerInput+1))
	if err != nil {
		return ownerrecord.Signed{}, failed(cmd, what, 1, fmt.Errorf("read stdin: %w", err))
	}

	if len(data) == 0 {
		return ownerrecord.Signed{}, failed(cmd, what, 1, errors.New("stdin is empty"))
	}

	if len(data) > maxOwnerInput {
		return ownerrecord.Signed{}, failed(cmd, what, 2, &progress.Refusal{
			Code:    progress.CodeJobRequestInvalid,
			Message: fmt.Sprintf("stdin holds more than %d bytes", maxOwnerInput),
		})
	}

	sf, err := ownerrecord.Parse(data)
	if err != nil {
		return ownerrecord.Signed{}, ownerFailed(cmd, what, err)
	}

	return sf, nil
}

// ownerFailed reports err from the owner record store on stderr with the
// refusal code it stands for, and returns an *ExitError: 2 for a refusal and
// 1 for anything else. It returns nil for a nil err.
func ownerFailed(cmd *cobra.Command, what string, err error) error {
	if err == nil {
		return nil
	}

	var code progress.Code

	switch {
	case errors.Is(err, ownerrecord.ErrStoredRecordInvalid):
		code = progress.CodeOwnerRecordInvalid
	case errors.Is(err, ownerrecord.ErrNoRecord):
		code = progress.CodeOwnerRecordMissing
	case errors.Is(err, ownerrecord.ErrRecordChanged):
		code = progress.CodeOwnerMismatch
	default:
		if _, ok := progress.AsRefusal(err); ok {
			return failed(cmd, what, 2, err)
		}

		return failed(cmd, what, 1, err)
	}

	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "%s: %s: %v\n", what, code, err)

	return &ExitError{Code: 2, Err: err}
}
