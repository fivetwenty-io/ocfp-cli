package commands

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/firstrun"
	"github.com/fivetwenty-io/ocfp-cli/internal/vault"
)

const (
	firstRunCmdLink = "https://manage.example.test/#/setup?code="
	firstRunCmdURL  = "https://manage.example.test"
)

// firstRunTestDeps wires the first-run commands to a fake vault, a fake
// terminal check, scripted input, and a browser that records its target.
type firstRunTestDeps struct {
	store    *fakeFirstRunVault
	out      bytes.Buffer
	opened   []string
	opens    int
	closes   int
	openErr  error
	terminal bool
	input    string
}

func (d *firstRunTestDeps) deps() firstRunDeps {
	return firstRunDeps{
		openVault: func(bloc string) (vault.FirstRunVault, func(), error) {
			d.opens++

			if bloc != firstRunCmdBloc {
				return nil, nil, errors.New("opened the wrong bloc's vault")
			}

			if d.openErr != nil {
				return nil, nil, d.openErr
			}

			return d.store, func() { d.closes++ }, nil
		},
		terminal: func() bool { return d.terminal },
		in:       strings.NewReader(d.input),
		out:      &d.out,
		browse: func(_ context.Context, target string) error {
			d.opened = append(d.opened, target)

			return nil
		},
	}
}

func newFirstRunTestDeps(terminal bool, input string) *firstRunTestDeps {
	store := newFakeFirstRunVault()
	store.put(vault.FirstRunCodePath(firstRunCmdBloc), vault.FirstRunCodeKey, firstRunCmdCode)
	store.put(vault.FirstRunCodePath(firstRunCmdBloc), vault.FirstRunShownKey, "false")
	store.put(vault.FirstRunPublicPath(firstRunCmdBloc), vault.FirstRunPublicKey, firstRunCmdPublic)

	return &firstRunTestDeps{store: store, terminal: terminal, input: input}
}

func TestFirstRunShowPrintsTheCodeAfterConfirmation(t *testing.T) {
	t.Parallel()

	d := newFirstRunTestDeps(true, "y\n")

	err := runFirstRunShow(d.deps(), firstRunCmdBloc)
	if err != nil {
		t.Fatal(err)
	}

	if strings.Count(d.out.String(), firstRunCmdCode) != 1 {
		t.Fatalf("printed %q, want the code once", d.out.String())
	}

	if !strings.Contains(d.out.String(), vault.FirstRunCodeSentence) {
		t.Fatal("the code was printed without its sentence")
	}

	if d.store.get(vault.FirstRunCodePath(firstRunCmdBloc), vault.FirstRunShownKey) != "true" {
		t.Fatal("show didn't mark the code shown")
	}

	if d.closes != d.opens {
		t.Fatal("the vault wasn't closed")
	}
}

func TestFirstRunShowRefusesWithoutConfirmation(t *testing.T) {
	t.Parallel()

	for name, input := range map[string]string{"no": "n\n", "empty": "\n", "end of input": "", "other": "maybe\n"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			d := newFirstRunTestDeps(true, input)

			err := runFirstRunShow(d.deps(), firstRunCmdBloc)
			if firstrun.CodeOf(err) != codeFirstRunNotConfirmed {
				t.Fatalf("got %v, want %s", err, codeFirstRunNotConfirmed)
			}

			if strings.Contains(d.out.String(), firstRunCmdCode) {
				t.Fatal("the code was printed without confirmation")
			}
		})
	}
}

// TestFirstRunShowRefusesOutputThatIsntATerminal checks the coded refusal
// and that it comes before the vault is opened or a prompt is printed.
func TestFirstRunShowRefusesOutputThatIsntATerminal(t *testing.T) {
	t.Parallel()

	d := newFirstRunTestDeps(false, "y\n")

	err := runFirstRunShow(d.deps(), firstRunCmdBloc)
	if firstrun.CodeOf(err) != codeFirstRunOutputNotTerminal {
		t.Fatalf("got %v, want %s", err, codeFirstRunOutputNotTerminal)
	}

	if d.opens != 0 || d.out.Len() != 0 {
		t.Fatalf("opened the vault %d times and printed %q", d.opens, d.out.String())
	}
}

func TestFirstRunShowRefusesABlocWithNoCode(t *testing.T) {
	t.Parallel()

	d := newFirstRunTestDeps(true, "y\n")
	d.store = newFakeFirstRunVault()

	err := runFirstRunShow(d.deps(), firstRunCmdBloc)
	if firstrun.CodeOf(err) != codeFirstRunCodeMissing {
		t.Fatalf("got %v, want %s", err, codeFirstRunCodeMissing)
	}

	if !strings.Contains(err.Error(), "ocfp first-run rotate --bloc lab") {
		t.Fatalf("the refusal %q doesn't name the rotation", err)
	}
}

func TestFirstRunCommandsRefuseNoBloc(t *testing.T) {
	t.Parallel()

	d := newFirstRunTestDeps(true, "y\n")

	for name, run := range map[string]func() error{
		"show":   func() error { return runFirstRunShow(d.deps(), "") },
		"open":   func() error { return runFirstRunOpen(context.Background(), d.deps(), "") },
		"rotate": func() error { return runFirstRunRotate(d.deps(), "", false) },
	} {
		if err := run(); !errors.Is(err, ErrBlocFlagOrEnvVarRequired) {
			t.Fatalf("%s gave %v", name, err)
		}
	}

	if d.opens != 0 {
		t.Fatal("a command opened a vault with no bloc")
	}
}

// TestFirstRunOpenHandsTheLinkToTheBrowser checks that the browser gets
// the recorded link with the code appended, and that only the address is
// printed.
func TestFirstRunOpenHandsTheLinkToTheBrowser(t *testing.T) {
	t.Parallel()

	for name, terminal := range map[string]bool{"terminal": true, "not a terminal": false} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			d := newFirstRunTestDeps(terminal, "")
			d.store.put(vault.FirstRunPublicPath(firstRunCmdBloc), vault.FirstRunLinkKey, firstRunCmdLink)
			d.store.put(vault.FirstRunPublicPath(firstRunCmdBloc), vault.FirstRunURLKey, firstRunCmdURL)

			err := runFirstRunOpen(context.Background(), d.deps(), firstRunCmdBloc)
			if err != nil {
				t.Fatal(err)
			}

			norm, _ := firstrun.Normalize(firstRunCmdCode)
			if len(d.opened) != 1 || d.opened[0] != firstRunCmdLink+norm {
				t.Fatalf("the browser got %v", d.opened)
			}

			if strings.Contains(d.out.String(), norm) || strings.Contains(d.out.String(), firstRunCmdCode) {
				t.Fatal("open printed the code")
			}

			if !strings.Contains(d.out.String(), firstRunCmdURL) {
				t.Fatalf("printed %q, want the address", d.out.String())
			}

			if d.store.get(vault.FirstRunCodePath(firstRunCmdBloc), vault.FirstRunShownKey) != "false" {
				t.Fatal("open changed shown")
			}
		})
	}
}

// TestFirstRunOpenPrintsTheLinksAddressWithNoURL checks that the address
// comes from the link when no url is recorded.
func TestFirstRunOpenPrintsTheLinksAddressWithNoURL(t *testing.T) {
	t.Parallel()

	d := newFirstRunTestDeps(true, "")
	d.store.put(vault.FirstRunPublicPath(firstRunCmdBloc), vault.FirstRunLinkKey, firstRunCmdLink)

	err := runFirstRunOpen(context.Background(), d.deps(), firstRunCmdBloc)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(d.out.String(), firstRunCmdURL) || strings.Contains(d.out.String(), "#/setup") {
		t.Fatalf("printed %q, want only the address", d.out.String())
	}
}

func TestFirstRunOpenSaysNothingHasDeployed(t *testing.T) {
	t.Parallel()

	d := newFirstRunTestDeps(true, "")

	err := runFirstRunOpen(context.Background(), d.deps(), firstRunCmdBloc)
	if firstrun.CodeOf(err) != codeFirstRunNotDeployed {
		t.Fatalf("got %v, want %s", err, codeFirstRunNotDeployed)
	}

	if !strings.Contains(err.Error(), "no managing system in this bloc has finished deploying") {
		t.Fatalf("the refusal %q doesn't say why", err)
	}

	if len(d.opened) != 0 {
		t.Fatal("the browser was opened")
	}
}

// TestFirstRunOpenRefusesALinkThatIsntHTTPS checks that only an https
// address with a host reaches the local open command, so a tampered link
// can't run a local handler with the code.
func TestFirstRunOpenRefusesALinkThatIsntHTTPS(t *testing.T) {
	t.Parallel()

	for _, link := range []string{
		"http://manage.example.test/#/setup?code=",
		"file:///etc/passwd#",
		"javascript:alert(1)//",
		"https:///#/setup?code=",
		"https://manage.example.test/#/setup?code= --new-window",
		"-a Terminal https://manage.example.test/",
		"https://user@manage.example.test/#/setup?code=",
		"https://manage.example.test/\n#/setup?code=",
	} {
		d := newFirstRunTestDeps(true, "")
		d.store.put(vault.FirstRunPublicPath(firstRunCmdBloc), vault.FirstRunLinkKey, link)

		err := runFirstRunOpen(context.Background(), d.deps(), firstRunCmdBloc)
		if firstrun.CodeOf(err) != codeFirstRunLinkInvalid {
			t.Fatalf("link %q gave %v, want %s", link, err, codeFirstRunLinkInvalid)
		}

		if len(d.opened) != 0 {
			t.Fatalf("link %q reached the browser", link)
		}

		if strings.Contains(err.Error(), firstRunCmdCode) {
			t.Fatal("the refusal holds the code")
		}
	}
}

func TestFirstRunOpenReportsABrowserFailureWithoutTheCode(t *testing.T) {
	t.Parallel()

	d := newFirstRunTestDeps(true, "")
	d.store.put(vault.FirstRunPublicPath(firstRunCmdBloc), vault.FirstRunLinkKey, firstRunCmdLink)

	deps := d.deps()
	deps.browse = func(_ context.Context, target string) error {
		return errors.New("exec: no browser for " + target)
	}

	err := runFirstRunOpen(context.Background(), deps, firstRunCmdBloc)
	if err == nil {
		t.Fatal("a failed browser was reported as success")
	}

	norm, _ := firstrun.Normalize(firstRunCmdCode)
	if strings.Contains(err.Error(), norm) || strings.Contains(err.Error(), firstRunCmdCode) {
		t.Fatalf("the error %q holds the code", err)
	}
}

// TestFirstRunRotateShowsTheNewCodeOnce checks the interactive rotation:
// both leaves change, the new code is shown once, and it is marked shown.
func TestFirstRunRotateShowsTheNewCodeOnce(t *testing.T) {
	t.Parallel()

	d := newFirstRunTestDeps(true, "")

	err := runFirstRunRotate(d.deps(), firstRunCmdBloc, false)
	if err != nil {
		t.Fatal(err)
	}

	code, _ := d.store.get(vault.FirstRunCodePath(firstRunCmdBloc), vault.FirstRunCodeKey).(string)
	if code == firstRunCmdCode || code == "" {
		t.Fatal("the code didn't change")
	}

	if strings.Count(d.out.String(), code) != 1 || !strings.Contains(d.out.String(), vault.FirstRunCodeSentence) {
		t.Fatalf("printed %q, want the new code once with its sentence", d.out.String())
	}

	want, _ := firstrun.PublicFor(code)
	if d.store.get(vault.FirstRunPublicPath(firstRunCmdBloc), vault.FirstRunPublicKey) != want {
		t.Fatal("the public half isn't the new code's")
	}

	if d.store.get(vault.FirstRunCodePath(firstRunCmdBloc), vault.FirstRunShownKey) != "true" {
		t.Fatal("the shown code isn't marked shown")
	}
}

// TestFirstRunRotateQuietPrintsNothingSecret checks the deploy's form: it
// rotates with no terminal, prints no code, and leaves shown false.
func TestFirstRunRotateQuietPrintsNothingSecret(t *testing.T) {
	t.Parallel()

	d := newFirstRunTestDeps(false, "")

	err := runFirstRunRotate(d.deps(), firstRunCmdBloc, true)
	if err != nil {
		t.Fatal(err)
	}

	code, _ := d.store.get(vault.FirstRunCodePath(firstRunCmdBloc), vault.FirstRunCodeKey).(string)
	if code == firstRunCmdCode {
		t.Fatal("the code didn't change")
	}

	norm, _ := firstrun.Normalize(code)
	if strings.Contains(d.out.String(), code) || strings.Contains(d.out.String(), norm) {
		t.Fatal("the quiet rotation printed the code")
	}

	if d.store.get(vault.FirstRunCodePath(firstRunCmdBloc), vault.FirstRunShownKey) != "false" {
		t.Fatal("the quiet rotation marked the code shown")
	}
}

// TestFirstRunRotateRefusesToShowToAFile checks that the showing form
// refuses output that isn't a terminal before it changes anything.
func TestFirstRunRotateRefusesToShowToAFile(t *testing.T) {
	t.Parallel()

	d := newFirstRunTestDeps(false, "")

	err := runFirstRunRotate(d.deps(), firstRunCmdBloc, false)
	if firstrun.CodeOf(err) != codeFirstRunOutputNotTerminal {
		t.Fatalf("got %v, want %s", err, codeFirstRunOutputNotTerminal)
	}

	if d.store.get(vault.FirstRunCodePath(firstRunCmdBloc), vault.FirstRunCodeKey) != firstRunCmdCode {
		t.Fatal("the refused rotation changed the code")
	}

	if !strings.Contains(err.Error(), "--quiet") {
		t.Fatalf("the refusal %q doesn't name --quiet", err)
	}
}

func TestFirstRunCommandsReportAnUnreachableVault(t *testing.T) {
	t.Parallel()

	d := newFirstRunTestDeps(true, "y\n")
	d.openErr = errors.New("no target for the bloc")

	if err := runFirstRunShow(d.deps(), firstRunCmdBloc); err == nil || !strings.Contains(err.Error(), "no target") {
		t.Fatalf("show gave %v", err)
	}

	if err := runFirstRunOpen(context.Background(), d.deps(), firstRunCmdBloc); err == nil {
		t.Fatal("open succeeded with no vault")
	}

	if err := runFirstRunRotate(d.deps(), firstRunCmdBloc, true); err == nil {
		t.Fatal("rotate succeeded with no vault")
	}
}

// TestFirstRunCommandTree checks the command's shape: three subcommands,
// --quiet on rotate only, and --bloc reachable on each.
func TestFirstRunCommandTree(t *testing.T) {
	t.Parallel()

	cmd := NewFirstRunCmd()
	if cmd.Use != "first-run" {
		t.Fatalf("Use = %q", cmd.Use)
	}

	names := map[string]bool{}
	for _, sub := range cmd.Commands() {
		names[sub.Name()] = true

		if sub.Flags().Lookup("bloc") == nil {
			t.Errorf("%s has no --bloc", sub.Name())
		}

		if (sub.Flags().Lookup("quiet") != nil) != (sub.Name() == "rotate") {
			t.Errorf("%s has the wrong --quiet", sub.Name())
		}
	}

	for _, want := range []string{"show", "open", "rotate"} {
		if !names[want] {
			t.Errorf("no %s subcommand", want)
		}
	}
}

// TestBrowserCommandPassesTheTargetAsOneArgument checks the local open
// command for each platform: the target is its own argument, never part
// of a shell string.
func TestBrowserCommandPassesTheTargetAsOneArgument(t *testing.T) {
	t.Parallel()

	target := firstRunCmdLink + "AAAQEAYEAUDAOCAJBIFQYDIO"

	for _, goos := range []string{"darwin", "linux", "freebsd", "windows"} {
		name, args, err := browserCommand(goos, target)
		if err != nil {
			t.Fatalf("%s: %v", goos, err)
		}

		if name == "" || len(args) == 0 || args[len(args)-1] != target {
			t.Fatalf("%s: %s %v doesn't end with the target", goos, name, args)
		}

		for _, a := range args[:len(args)-1] {
			if strings.Contains(a, target) {
				t.Fatalf("%s: the target is folded into another argument", goos)
			}
		}
	}

	if _, _, err := browserCommand("plan9", target); err == nil {
		t.Fatal("an unsupported system was given a command")
	}
}
