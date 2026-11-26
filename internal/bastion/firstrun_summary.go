package bastion

import (
	"errors"
	"fmt"
	"io"

	"github.com/fivetwenty-io/ocfp-cli/internal/logger"
	"github.com/fivetwenty-io/ocfp-cli/internal/vault"
)

// firstRunVaultOpener opens a bloc's vault for the first-run code and
// returns the function that closes it.
type firstRunVaultOpener func(bloc string) (vault.FirstRunVault, func(), error)

// openBlocFirstRunVault opens the bloc's own vault over the workstation's
// path to it, the same target 'ocfp vault populate --bloc' writes to.
func openBlocFirstRunVault(bloc string) (vault.FirstRunVault, func(), error) {
	client, err := vault.NewClientForBloc(bloc)
	if err != nil {
		return nil, nil, fmt.Errorf("opening the bloc's vault: %w", err)
	}

	return vault.NewSafe(client), func() { _ = client.Close() }, nil
}

// showFirstRunCodeAtClose reads the bloc's code leaf after a real run and,
// when the code hasn't been shown and w is a terminal, shows it once with
// its sentence and marks it shown. A dry run and output that isn't a
// terminal never open the vault. The code is a convenience at this point,
// so no failure here fails the build. When the vault is out of reach, the
// operator is told how to see the code later.
func showFirstRunCodeAtClose(w io.Writer, log logger.Logger, bloc string, dryRun, terminal bool, open firstRunVaultOpener) {
	if dryRun || !terminal || bloc == "" {
		return
	}

	store, closeVault, err := open(bloc)
	if err != nil {
		pointAtFirstRunShow(w, log, bloc, err)

		return
	}

	defer closeVault()

	_, err = vault.ShowFirstRunCodeOnce(w, store, bloc, terminal)

	switch {
	case errors.Is(err, vault.ErrFirstRunMarkShown):
		log.Warnw("The first-run code was shown but not marked as shown, so ocfp may show it once more",
			"bloc", bloc, "error", err)
	case err != nil:
		pointAtFirstRunShow(w, log, bloc, err)
	}
}

// pointAtFirstRunShow logs why the code couldn't be read and tells the
// operator which command shows it.
func pointAtFirstRunShow(w io.Writer, log logger.Logger, bloc string, err error) {
	log.Warnw("Could not read the bloc's first-run code", "bloc", bloc, "error", err)

	_, _ = fmt.Fprintf(w, "\nocfp couldn't read this bloc's first-run code from its vault. "+
		"Run 'ocfp first-run show --bloc %s' to see it.\n", bloc)
}
