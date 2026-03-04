// Package main is the entry point for the OCFP CLI.
package main

import (
	"github.com/ocfp/ocfp-cli-go/internal/cli"
)

func main() {
	// Execute the CLI (constructs and runs root command)
	cli.Execute()
}
