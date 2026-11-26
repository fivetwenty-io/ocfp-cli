package commands

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/fivetwenty-io/ocfp-cli/firstrun"
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

// Refusal codes of the first-run commands.
const (
	codeFirstRunOutputNotTerminal = "FIRST_RUN_OUTPUT_NOT_TERMINAL"
	codeFirstRunNotConfirmed      = "FIRST_RUN_NOT_CONFIRMED"
	codeFirstRunCodeMissing       = "FIRST_RUN_CODE_MISSING"
	codeFirstRunNotDeployed       = "FIRST_RUN_NOT_DEPLOYED"
	codeFirstRunLinkInvalid       = "FIRST_RUN_LINK_INVALID"
	codeFirstRunOpenFailed        = "FIRST_RUN_OPEN_FAILED"
)

// firstRunDeps is what the first-run commands reach outside themselves:
// the bloc's vault, the terminal check, input, output, and the local open
// command. Tests swap each one for a fake.
type firstRunDeps struct {
	openVault func(bloc string) (vault.FirstRunVault, func(), error)
	terminal  func() bool
	in        io.Reader
	out       io.Writer
	browse    func(ctx context.Context, target string) error
}

// NewFirstRunCmd creates the first-run command, which shows, opens, and
// rotates the bloc's first-run code.
func NewFirstRunCmd() *cobra.Command {
	//nolint:exhaustruct // Using zero values for optional fields
	cmd := &cobra.Command{
		Use:   "first-run",
		Short: "Show, open, or rotate the bloc's first-run code",
		Long: `Show, open, or rotate the bloc's first-run code.

The first-run code claims the first sign-in of a managing system deployed
into the bloc. ocfp keeps it in the bloc's vault, readable only under the
admin policy, and shows it once when it makes it.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	cmd.AddCommand(newFirstRunShowCmd(), newFirstRunOpenCmd(), newFirstRunRotateCmd())

	return cmd
}

func newFirstRunShowCmd() *cobra.Command {
	//nolint:exhaustruct // Using zero values for optional fields
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Print the bloc's first-run code after confirmation",
		Long: `Print the bloc's first-run code after asking for confirmation.

The code is printed only to a terminal. When output isn't a terminal, the
command refuses with ` + codeFirstRunOutputNotTerminal + ` so the code never lands in a
file or a log.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runFirstRunShow(liveFirstRunDeps(cmd), firstRunBloc(cmd))
		},
	}

	addFirstRunBlocFlag(cmd)

	return cmd
}

func newFirstRunOpenCmd() *cobra.Command {
	//nolint:exhaustruct // Using zero values for optional fields
	cmd := &cobra.Command{
		Use:   "open",
		Short: "Open the managing system's first sign-in with the bloc's code",
		Long: `Open the managing system's first sign-in in the local browser.

The command reads the link the managing system's deploy recorded, appends
the bloc's first-run code, and hands the result to the local open command.
It prints only the managing system's address, never the code.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runFirstRunOpen(cmd.Context(), liveFirstRunDeps(cmd), firstRunBloc(cmd))
		},
	}

	addFirstRunBlocFlag(cmd)

	return cmd
}

func newFirstRunRotateCmd() *cobra.Command {
	//nolint:exhaustruct // Using zero values for optional fields
	cmd := &cobra.Command{
		Use:   "rotate",
		Short: "Replace the bloc's first-run code and show the new one once",
		Long: `Replace the bloc's first-run code and its public half.

The old code stops working once the managing system reads the new public
half. The new code is shown once on the terminal. With --quiet, nothing
secret is printed and the new code stays unshown, so 'ocfp first-run show'
or the next bloc build shows it.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			quiet, err := cmd.Flags().GetBool("quiet")
			if err != nil {
				return fmt.Errorf("reading --quiet: %w", err)
			}

			return runFirstRunRotate(liveFirstRunDeps(cmd), firstRunBloc(cmd), quiet)
		},
	}

	addFirstRunBlocFlag(cmd)
	cmd.Flags().Bool("quiet", false, "Print nothing secret and leave the new code unshown")

	return cmd
}

func addFirstRunBlocFlag(cmd *cobra.Command) {
	cmd.Flags().String("bloc", "", "Bloc name (or set OCFP_BLOC)")
}

// firstRunBloc resolves the bloc from the command's --bloc, then the
// configuration's bloc, then OCFP_BLOC.
func firstRunBloc(cmd *cobra.Command) string {
	if bloc, err := cmd.Flags().GetString("bloc"); err == nil && bloc != "" {
		return bloc
	}

	return vaultBlocName()
}

func liveFirstRunDeps(cmd *cobra.Command) firstRunDeps {
	return firstRunDeps{
		openVault: vault.OpenBlocFirstRunVault,
		terminal:  stdoutIsTerminal,
		in:        cmd.InOrStdin(),
		out:       cmd.OutOrStdout(),
		browse:    openInBrowser,
	}
}

// readFirstRunCode opens the bloc's vault and reads both first-run
// leaves, refusing a bloc that holds no code.
func readFirstRunCode(deps firstRunDeps, bloc string) (vault.FirstRunState, error) {
	store, closeVault, err := deps.openVault(bloc)
	if err != nil {
		return vault.FirstRunState{}, err
	}

	defer closeVault()

	state, err := vault.ReadFirstRunState(store, bloc)
	if err != nil {
		return vault.FirstRunState{}, fmt.Errorf("reading the bloc's first-run key: %w", err)
	}

	if state.Code == "" {
		return vault.FirstRunState{}, &firstrun.Error{
			Code: codeFirstRunCodeMissing,
			Detail: fmt.Sprintf("the bloc holds no first-run code; "+
				"run 'ocfp first-run rotate --bloc %s' to make one", bloc),
		}
	}

	return state, nil
}

// runFirstRunShow prints the bloc's code to a terminal after the operator
// confirms, and marks it shown.
func runFirstRunShow(deps firstRunDeps, bloc string) error {
	if bloc == "" {
		return ErrBlocFlagOrEnvVarRequired
	}

	if !deps.terminal() {
		return &firstrun.Error{
			Code:   codeFirstRunOutputNotTerminal,
			Detail: "the first-run code is printed only to a terminal; run the command without redirecting its output",
		}
	}

	state, err := readFirstRunCode(deps, bloc)
	if err != nil {
		return err
	}

	confirmed, err := confirmFirstRunShow(deps.in, deps.out)
	if err != nil {
		return err
	}

	if !confirmed {
		return &firstrun.Error{Code: codeFirstRunNotConfirmed, Detail: "the first-run code was not shown"}
	}

	err = vault.PrintFirstRunCode(deps.out, state.Code)
	if err != nil {
		return err
	}

	return markFirstRunShown(deps, bloc)
}

// confirmFirstRunShow asks before the code is printed. Only y or yes,
// in any case, confirms. End of input declines.
func confirmFirstRunShow(in io.Reader, out io.Writer) (bool, error) {
	_, err := fmt.Fprint(out, "This prints the bloc's first-run code, which claims the first sign-in of its managing system. Show it? [y/N]: ")
	if err != nil {
		return false, fmt.Errorf("printing the confirmation: %w", err)
	}

	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("reading the confirmation: %w", err)
	}

	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

// markFirstRunShown records that the code was shown. The code is already
// on screen, so a failure is a warning rather than an error.
func markFirstRunShown(deps firstRunDeps, bloc string) error {
	store, closeVault, err := deps.openVault(bloc)
	if err == nil {
		defer closeVault()

		err = vault.MarkFirstRunCodeShown(store, bloc)
	}

	if err != nil {
		logger.Get().Warnw("The first-run code was shown but not marked as shown, so ocfp may show it once more",
			"bloc", bloc, "error", err)
	}

	return nil
}

// runFirstRunOpen hands the recorded link with the code appended to the
// local open command, and prints only the managing system's address.
func runFirstRunOpen(ctx context.Context, deps firstRunDeps, bloc string) error {
	if bloc == "" {
		return ErrBlocFlagOrEnvVarRequired
	}

	state, err := readFirstRunCode(deps, bloc)
	if err != nil {
		return err
	}

	if state.Link == "" {
		return &firstrun.Error{
			Code:   codeFirstRunNotDeployed,
			Detail: "no managing system in this bloc has finished deploying, so there is no sign-in to open",
		}
	}

	linkAddress, err := firstRunAddress(state.Link)
	if err != nil {
		return &firstrun.Error{
			Code:   codeFirstRunLinkInvalid,
			Detail: "the recorded first-run link is not an https address; redeploy the managing system to record it again",
		}
	}

	address := linkAddress
	if urlAddress, urlErr := firstRunAddress(state.URL); urlErr == nil {
		address = urlAddress
	}

	normalized, err := firstrun.Normalize(state.Code)
	if err != nil {
		return err
	}

	err = deps.browse(ctx, state.Link+normalized)
	if err != nil {
		detail := "the local open command failed"
		if msg := err.Error(); !strings.Contains(msg, normalized) && !strings.Contains(msg, state.Code) {
			detail += ": " + msg
		}

		return &firstrun.Error{
			Code:   codeFirstRunOpenFailed,
			Detail: fmt.Sprintf("%s; run 'ocfp first-run show --bloc %s' to see the code", detail, bloc),
		}
	}

	_, err = fmt.Fprintf(deps.out, "Opened the managing system's first sign-in at %s\n", address)
	if err != nil {
		return fmt.Errorf("printing the address: %w", err)
	}

	return nil
}

// firstRunAddress checks that raw is an https address with a host, no
// user information, and no space or control character, and returns its
// scheme and host. Only such a link reaches the local open command, so a
// changed link can't hand the code to another local handler.
func firstRunAddress(raw string) (string, error) {
	for i := range len(raw) {
		if raw[i] <= ' ' || raw[i] == 0x7f {
			return "", errFirstRunLinkShape
		}
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return "", errFirstRunLinkShape
	}

	if parsed.Scheme != "https" || parsed.Host == "" || parsed.Hostname() == "" || parsed.User != nil || parsed.Opaque != "" {
		return "", errFirstRunLinkShape
	}

	return parsed.Scheme + "://" + parsed.Host, nil
}

var errFirstRunLinkShape = errors.New("not an https address with a host")

// runFirstRunRotate replaces both first-run leaves. Without quiet it shows
// the new code once on the terminal and marks it shown. With quiet it
// prints nothing secret and leaves the code unshown.
func runFirstRunRotate(deps firstRunDeps, bloc string, quiet bool) error {
	if bloc == "" {
		return ErrBlocFlagOrEnvVarRequired
	}

	if !quiet && !deps.terminal() {
		return &firstrun.Error{
			Code:   codeFirstRunOutputNotTerminal,
			Detail: "the new first-run code is shown only on a terminal; add --quiet to rotate without showing it",
		}
	}

	store, closeVault, err := deps.openVault(bloc)
	if err != nil {
		return err
	}

	defer closeVault()

	code, err := vault.RotateFirstRunKey(store, bloc, false)
	if err != nil {
		return fmt.Errorf("rotating the bloc's first-run key: %w", err)
	}

	logger.Get().Infow("Rotated the bloc's first-run key", "bloc", bloc,
		"code_path", vault.FirstRunCodePath(bloc), "public_path", vault.FirstRunPublicPath(bloc))

	if quiet {
		_, err = fmt.Fprintf(deps.out, "Rotated the bloc's first-run key. "+
			"Run 'ocfp first-run show --bloc %s' on a terminal to see the new code.\n", bloc)
		if err != nil {
			return fmt.Errorf("printing the rotation notice: %w", err)
		}

		return nil
	}

	err = vault.PrintFirstRunCode(deps.out, code)
	if err != nil {
		return err
	}

	err = vault.MarkFirstRunCodeShown(store, bloc)
	if err != nil {
		logger.Get().Warnw("The first-run code was shown but not marked as shown, so ocfp may show it once more",
			"bloc", bloc, "error", err)
	}

	return nil
}

// browserCommand names the local open command for goos and its
// arguments, with target as the last argument on its own.
func browserCommand(goos, target string) (string, []string, error) {
	switch goos {
	case "darwin":
		return "open", []string{target}, nil
	case "linux", "freebsd", "openbsd", "netbsd", "dragonfly", "illumos", "solaris":
		return "xdg-open", []string{target}, nil
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", target}, nil
	default:
		return "", nil, fmt.Errorf("%w: %s", errNoLocalOpenCommand, goos)
	}
}

var errNoLocalOpenCommand = errors.New("no local open command is known for this operating system")

// openInBrowser runs the local open command with target as one argument.
// No shell is involved. The error never repeats the target, which holds
// the code.
func openInBrowser(ctx context.Context, target string) error {
	name, args, err := browserCommand(runtime.GOOS, target)
	if err != nil {
		return err
	}

	// #nosec G204 -- the command is fixed per platform and the target is one argument.
	err = exec.CommandContext(ctx, name, args...).Run()
	if err != nil {
		return fmt.Errorf("running %s: %w", name, err)
	}

	return nil
}
