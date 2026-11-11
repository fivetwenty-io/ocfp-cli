package commands

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
	"github.com/fivetwenty-io/ocfp-cli/internal/version"
	"github.com/spf13/cobra"
)

// versionJSON is the machine-readable form of the version command. It carries
// the wire contract of the progress stream so that a caller can tell whether
// it understands the stream this binary writes.
type versionJSON struct {
	Version      string `json:"version"`
	BuildTime    string `json:"buildTime"`
	GitCommit    string `json:"gitCommit"`
	GoVersion    string `json:"goVersion"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	WireContract int    `json:"wireContract"`
}

// NewVersionCmd creates the version command.
func NewVersionCmd() *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:          "version",
		Short:        "Print OCFP CLI version information",
		SilenceUsage: true,
		Long: `Print the version of this OCFP CLI binary, along with the git commit it was
built from, its build time, the Go toolchain that produced it, and the
platform it targets.

A binary built with a bare "go build" reports "dev" and "unknown" for the
stamped fields; "make build" and "make install-local" embed the real values.

This prints the same information as the --version flag.`,
		Example: `  ocfp version
  ocfp version --output json`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			info := version.Get()

			switch output {
			case "text":
				_, err := fmt.Fprintln(cmd.OutOrStdout(), info.String())

				return err
			case "json":
				b, err := json.Marshal(versionJSON{
					Version:      info.Version,
					BuildTime:    info.BuildTime,
					GitCommit:    info.GitCommit,
					GoVersion:    info.GoVersion,
					OS:           info.OS,
					Arch:         info.Arch,
					WireContract: progress.WireContract,
				})
				if err != nil {
					return fmt.Errorf("encode version: %w", err)
				}

				_, err = fmt.Fprintln(cmd.OutOrStdout(), string(b))

				return err
			default:
				return fmt.Errorf("%w: %q (want text or json)", errVersionOutput, output)
			}
		},
	}

	cmd.Flags().StringVar(&output, "output", "text", "output format: text|json")

	return cmd
}

var errVersionOutput = errors.New("unsupported output format")
