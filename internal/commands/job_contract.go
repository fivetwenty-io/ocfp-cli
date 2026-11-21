package commands

import (
	"fmt"

	"github.com/fivetwenty-io/ocfp-cli/internal/jobexec"
	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
	"github.com/spf13/cobra"
)

func newJobContractCmd(newStore ownerStoreFunc, ownerDefault string) *cobra.Command {
	var ownerDir string

	cmd := &cobra.Command{
		Use:   "contract",
		Short: "Answer one signed wire contract query from stdin",
		Long: `Read one signed wire contract query as a line of JSON on stdin, check it
against the owner record, and write the wire contract this host reads to
stdout as one line of JSON. It runs nothing and changes nothing.

The query is signed the same way as a job exec request, and its action is
wire-contract. It names no verb, env, or interactive. It passes the same
owner checks as a job exec request, in the same order, with one difference:
a query of the wire contract this host reads, or of any later one, is
answered, and only an older one is refused with WIRE_CONTRACT_UNSUPPORTED.
A query whose action is run or cancel is refused with JOB_REQUEST_INVALID,
and job exec refuses a request whose action is wire-contract the same way.

Job contract reads the owner record only as owner.json. A directory that
holds the record only under the name an earlier release gave it holds no
record for job contract, which refuses the query with OWNER_RECORD_MISSING.

A query that the owner signed for the org and the bloc this host serves
gets the answer, and the command exits 0:

  {"format":1,"wireContract":3,"jobId":"<id>"}

A query that is not signed by the owner, that names an org or a bloc other
than the ones the owner record names, or that the owner record cannot vouch
for, is refused. Stdout carries one line that names the refusal code,
stderr names the code as well, and the command exits 2:

  {"format":1,"jobId":"<id>","error":{"code":"<CODE>","message":"<text>"}}

The jobId is there only once the query's signature has verified and its
payload has decoded to a job id. A line that is not a signed request is
refused with a fixed message, so nothing from a query that the owner did
not sign is ever written back. The codes are REQUEST_UNSIGNED,
OWNER_RECORD_MISSING, OWNER_RECORD_INVALID, OWNER_HANDOVER, OWNER_MISMATCH,
WIRE_CONTRACT_UNSUPPORTED, ORG_BLOC_MISMATCH, and JOB_REQUEST_INVALID.

Any other failure, such as stdin that cannot be read or holds nothing, a
lock on the owner record that cannot be taken, or a flag that is refused,
writes nothing to stdout, and the command exits 1. An --owner-dir that is
not absolute, or that is not the default as written but leads to the
default directory or to a directory under it, is refused the same way as
for job exec.`,
		Example:       `  ocfp job contract < contract-query.json`,
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := absoluteFlag("--owner-dir", ownerDir); err != nil {
				return failed(cmd, "job contract", 1, err)
			}

			if err := refuseDefaultReach("--owner-dir", ownerDir, ownerDefault); err != nil {
				return failed(cmd, "job contract", 1, err)
			}

			err := jobexec.AnswerWireContract(cmd.Context(), jobexec.ContractOptions{
				Owner:  newStore(ownerDir),
				Stdin:  cmd.InOrStdin(),
				Stdout: cmd.OutOrStdout(),
			})
			if err == nil {
				return nil
			}

			// A refusal is named on stderr by the same code and message as
			// the line on stdout.
			if ref, ok := progress.AsRefusal(err); ok {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "job contract: %s: %s\n", ref.Code, ref.Message)

				return &ExitError{Code: 2, Err: err}
			}

			return failed(cmd, "job contract", 1, err)
		},
	}

	cmd.Flags().StringVar(&ownerDir, "owner-dir", ownerDefault, "absolute path of the directory that holds the owner record")

	return cmd
}
