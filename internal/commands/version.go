package commands

import (
	"fmt"

	"github.com/ocfp/ocfp-cli-go/internal/version"
	"github.com/spf13/cobra"
)

// NewVersionCmd creates the version command.
func NewVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "version",
		Short:        "Print OCFP CLI version information",
		SilenceUsage: true,
		Long: `Print the version of this OCFP CLI binary, along with the git commit it was
built from, its build time, the Go toolchain that produced it, and the
platform it targets.

A binary built with a bare "go build" reports "dev" and "unknown" for the
stamped fields; "make build" and "make install-local" embed the real values.

This prints the same information as the --version flag.`,
		Example: `  ocfp version`,
		RunE: func(_ *cobra.Command, _ []string) error {
			fmt.Println(version.Get().String())

			return nil
		},
	}
}
