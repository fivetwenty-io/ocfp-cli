package commands

import (
	"fmt"
	"io"
	"os"

	"golang.org/x/term"

	"github.com/fivetwenty-io/ocfp-cli/internal/logger"
	"github.com/fivetwenty-io/ocfp-cli/internal/vault"
)

// stdoutIsTerminal reports whether this process's standard output is a
// terminal. The first-run code is shown only to a terminal, so it never
// lands in a log or a pipeline's output.
func stdoutIsTerminal() bool {
	return term.IsTerminal(int(os.Stdout.Fd())) // #nosec G115 -- a file descriptor fits in int
}

// runPopulateFirstRun is the first-run part of 'ocfp vault populate'. A
// dry run says what a real run would write, and a real run writes it.
func runPopulateFirstRun(w io.Writer, store vault.FirstRunVault, bloc string, dryRun bool) error {
	if dryRun {
		return previewFirstRunKey(w, store, bloc)
	}

	return populateFirstRunKey(w, store, bloc, stdoutIsTerminal())
}

// populateFirstRunKey gives the bloc a first-run key when it lacks one.
// When it makes a new code and terminal reports a terminal, it shows the
// code once with its sentence and marks it shown. Otherwise it prints
// nothing about the code, which keeps the code out of the bastion's
// populate log, and the bloc build's closing summary shows it instead.
func populateFirstRunKey(w io.Writer, store vault.FirstRunVault, bloc string, terminal bool) error {
	log := logger.Get()

	out, err := vault.EnsureFirstRunKey(store, bloc)
	if err != nil {
		return fmt.Errorf("writing the bloc's first-run key: %w", err)
	}

	switch {
	case out.CodeMissing:
		_, err = fmt.Fprintf(w, "The bloc's first-run public half has no code beside it, so no code was written. "+
			"Run 'ocfp first-run rotate --bloc %s' to make a new first-run key.\n", bloc)
	case out.Mismatch:
		_, err = fmt.Fprintf(w, "The bloc's first-run public half doesn't match its code, and neither was changed. "+
			"Run 'ocfp first-run rotate --bloc %s' to make a new first-run key.\n", bloc)
	}

	if err != nil {
		return fmt.Errorf("printing the first-run notice: %w", err)
	}

	if out.WrotePublic {
		log.Infow("Wrote the bloc's first-run public half", "bloc", bloc, "path", vault.FirstRunPublicPath(bloc))
	}

	if !out.WroteCode {
		return nil
	}

	log.Infow("Wrote a new first-run code", "bloc", bloc, "path", vault.FirstRunCodePath(bloc))

	if !terminal {
		return nil
	}

	err = vault.PrintFirstRunCode(w, out.Code)
	if err != nil {
		return err
	}

	err = vault.MarkFirstRunCodeShown(store, bloc)
	if err != nil {
		log.Warnw("The first-run code was shown but not marked as shown, so ocfp may show it once more",
			"bloc", bloc, "error", err)
	}

	return nil
}

// previewFirstRunKey says, for a dry run, which first-run leaves a real
// run would write. It never prints the code.
func previewFirstRunKey(w io.Writer, store vault.FirstRunVault, bloc string) error {
	state, err := vault.ReadFirstRunState(store, bloc)
	if err != nil {
		return fmt.Errorf("reading the bloc's first-run key: %w", err)
	}

	var line string

	switch {
	case state.Code == "" && state.Public == "":
		line = "DRY RUN: Would make the bloc's first-run key and write its code and public half"
	case state.Code == "":
		line = "DRY RUN: The bloc's first-run public half has no code beside it; " +
			"'ocfp first-run rotate' makes a new first-run key"
	case state.Public == "":
		line = "DRY RUN: Would write the first-run public half derived from the bloc's code"
	default:
		line = "DRY RUN: The bloc's first-run key is in place"
	}

	_, err = fmt.Fprintln(w, line)
	if err != nil {
		return fmt.Errorf("printing the first-run preview: %w", err)
	}

	return nil
}
