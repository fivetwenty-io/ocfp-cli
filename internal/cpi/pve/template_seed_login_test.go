package pve

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"testing"
	"time"
)

// consoleStep is one scripted ExpectRegex result: the console output that
// arrives, or the error the wait ends with.
type consoleStep struct {
	out string
	err error
}

// scriptedConsole plays back console output for seedLogin and records every
// line seedLogin sends. Once the script runs out, every wait times out.
type scriptedConsole struct {
	t      *testing.T
	script []consoleStep
	waits  int
	sent   []string
}

func (c *scriptedConsole) SendLine(line string) error {
	c.sent = append(c.sent, line)

	return nil
}

func (c *scriptedConsole) ExpectRegex(re *regexp.Regexp, _ time.Duration) (string, error) {
	c.waits++

	if len(c.script) == 0 {
		return "", fmt.Errorf("%w waiting for %s", errTermproxyTimeout, re)
	}

	step := c.script[0]
	c.script = c.script[1:]

	if step.err != nil {
		return step.out, step.err
	}

	if !re.MatchString(step.out) {
		c.t.Fatalf("seedLogin waited for %s, which does not match scripted output %q", re, step.out)
	}

	return step.out, nil
}

func (c *scriptedConsole) Drain(time.Duration) string { return "" }

// typed returns the lines seedLogin sent after its three wake-up CRs.
func (c *scriptedConsole) typed() []string {
	if len(c.sent) < 3 {
		return nil
	}

	return c.sent[3:]
}

func timeoutStep() consoleStep {
	return consoleStep{err: fmt.Errorf("%w waiting for prompt", errTermproxyTimeout)}
}

const testSeedPassword = "OcfpSeed.test"

func TestSeedLogin_CleanLogin(t *testing.T) {
	t.Parallel()

	console := &scriptedConsole{t: t, script: []consoleStep{
		{out: "ubuntu-resolute login: "},
		{out: "Password: "},
		{out: "ubuntu@ubuntu-resolute:~$ "},
	}}

	err := seedLogin(console, testSeedPassword)
	if err != nil {
		t.Fatalf("seedLogin() error = %v", err)
	}

	want := []string{templateSeedCIUser, testSeedPassword}
	if got := console.typed(); len(got) != 3 || !slices.Equal(got[:2], want) {
		t.Fatalf("typed = %q, want %q followed by the stty line", got, want)
	}
}

// TestSeedLogin_RetriesWhenPasswordPromptIsLost covers the pipes rebuild
// failure: cloud-init was still writing to ttyS0 after getty printed
// login:, and the Password: prompt never arrived inside the wait.
func TestSeedLogin_RetriesWhenPasswordPromptIsLost(t *testing.T) {
	t.Parallel()

	console := &scriptedConsole{t: t, script: []consoleStep{
		{out: "ubuntu-resolute login: "},
		timeoutStep(),
		{out: "cloud-init[971]: |oB * o. . .|\r\n\r\nLogin incorrect\r\nubuntu-resolute login: "},
		{out: "Password: "},
		{out: "ubuntu@ubuntu-resolute:~$ "},
	}}

	err := seedLogin(console, testSeedPassword)
	if err != nil {
		t.Fatalf("seedLogin() error = %v", err)
	}

	want := []string{templateSeedCIUser, "", templateSeedCIUser, testSeedPassword}
	if got := console.typed(); len(got) != 5 || !slices.Equal(got[:4], want) {
		t.Fatalf("typed = %q, want %q followed by the stty line", got, want)
	}
}

func TestSeedLogin_RecoversWhenShellPromptIsLost(t *testing.T) {
	t.Parallel()

	console := &scriptedConsole{t: t, script: []consoleStep{
		{out: "ubuntu-resolute login: "},
		{out: "Password: "},
		timeoutStep(),
		{out: "ubuntu@ubuntu-resolute:~$ "},
	}}

	err := seedLogin(console, testSeedPassword)
	if err != nil {
		t.Fatalf("seedLogin() error = %v", err)
	}

	want := []string{templateSeedCIUser, testSeedPassword, ""}
	if got := console.typed(); len(got) != 4 || !slices.Equal(got[:3], want) {
		t.Fatalf("typed = %q, want %q followed by the stty line", got, want)
	}
}

func TestSeedLogin_IgnoresPasswordPromptBeforeUserIsSent(t *testing.T) {
	t.Parallel()

	console := &scriptedConsole{t: t, script: []consoleStep{
		{out: "Password: "},
		{out: "ubuntu-resolute login: "},
		{out: "Password: "},
		{out: "ubuntu@ubuntu-resolute:~$ "},
	}}

	err := seedLogin(console, testSeedPassword)
	if err != nil {
		t.Fatalf("seedLogin() error = %v", err)
	}

	if got := console.typed(); len(got) == 0 || got[0] != templateSeedCIUser {
		t.Fatalf("typed = %q, want the user name sent first", got)
	}
}

func TestSeedLogin_DoesNotRetryAClosedConnection(t *testing.T) {
	t.Parallel()

	closed := errors.New("termproxy: connection closed")
	console := &scriptedConsole{t: t, script: []consoleStep{
		{out: "ubuntu-resolute login: "},
		{err: closed},
	}}

	err := seedLogin(console, testSeedPassword)
	if !errors.Is(err, closed) {
		t.Fatalf("seedLogin() error = %v, want %v", err, closed)
	}

	if console.waits != 2 {
		t.Fatalf("waits = %d, want 2 (no retry after the connection closed)", console.waits)
	}
}

func TestSeedLogin_GivesUpAfterBoundedRetries(t *testing.T) {
	t.Parallel()

	console := &scriptedConsole{t: t}

	err := seedLogin(console, testSeedPassword)
	if !errors.Is(err, errTermproxyTimeout) {
		t.Fatalf("seedLogin() error = %v, want a termproxy timeout", err)
	}

	if console.waits != templateSeedLoginAttempts+1 {
		t.Fatalf("waits = %d, want %d", console.waits, templateSeedLoginAttempts+1)
	}
}
