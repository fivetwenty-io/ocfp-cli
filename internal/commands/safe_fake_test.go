package commands

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSafeHelperEnv makes this test binary act as safe. A test puts a
// wrapper named safe on PATH that runs the binary again with it set.
const fakeSafeHelperEnv = "OCFP_TEST_FAKE_SAFE"

// The fake safe reads these from its environment.
const (
	// fakeSafeLogEnv names the file the fake appends one JSON line to per
	// call, holding its arguments and SAFE_TARGET.
	fakeSafeLogEnv = "OCFP_TEST_FAKE_SAFE_LOG"
	// fakeSafeNoKeepCurrentEnv, set to 1, makes the fake an older safe whose
	// help never mentions --keep-current and which rejects the flag.
	fakeSafeNoKeepCurrentEnv = "OCFP_TEST_FAKE_SAFE_NO_KEEP_CURRENT"
	// fakeSafeLocalStartedEnv names a file the fake's `safe local` creates
	// once it has registered its target.
	fakeSafeLocalStartedEnv = "OCFP_TEST_FAKE_SAFE_LOCAL_STARTED"
)

// fakeSafeLocalExit is the fake's own command, never one of safe's, that ends
// every `safe local` the fake has running. A fake tmux or pkill runs it, as
// the hangup or the signal they send makes the real safe local shut down.
const fakeSafeLocalExit = "__local-exit"

// fakeSafeRootToken is the root token the fake's `safe local` stores.
const fakeSafeRootToken = "s.FAKE-SAFE-LOCAL-ROOT-TOKEN"

// fakeSafeRC is the part of ~/.saferc the fake reads and writes.
type fakeSafeRC struct {
	Version int                       `yaml:"version"`
	Current string                    `yaml:"current"`
	Vaults  map[string]*fakeSafeVault `yaml:"vaults"`
}

type fakeSafeVault struct {
	URL   string `yaml:"url"`
	Token string `yaml:"token"`
}

// fakeSafeCall is one logged call of the fake safe.
type fakeSafeCall struct {
	Args       []string `json:"args"`
	SafeTarget string   `json:"safe_target"`
}

func fakeSafeRCPath() string {
	home, _ := os.UserHomeDir()

	return filepath.Join(home, ".saferc")
}

func readFakeSafeRC() (*fakeSafeRC, error) {
	rc := &fakeSafeRC{Version: 1, Vaults: map[string]*fakeSafeVault{}}

	data, err := os.ReadFile(fakeSafeRCPath())
	if os.IsNotExist(err) {
		return rc, nil
	}

	if err != nil {
		return nil, err
	}

	err = yaml.Unmarshal(data, rc)
	if err != nil {
		return nil, err
	}

	if rc.Vaults == nil {
		rc.Vaults = map[string]*fakeSafeVault{}
	}

	return rc, nil
}

func writeFakeSafeRCFile(rc *fakeSafeRC) error {
	data, err := yaml.Marshal(rc)
	if err != nil {
		return err
	}

	return os.WriteFile(fakeSafeRCPath(), data, 0o600)
}

// fakeSafeLocalRun is one `safe local` the fake has running: the target it
// registered, the URL it registered it at, and the target that was current
// when it started, which real safe puts back on shutdown.
type fakeSafeLocalRun struct {
	Name     string `json:"name"`
	URL      string `json:"url"`
	Previous string `json:"previous"`
}

func fakeSafeLocalRunsPath() string {
	home, _ := os.UserHomeDir()

	return filepath.Join(home, ".fake-safe-local-runs.json")
}

func readFakeSafeLocalRuns() []fakeSafeLocalRun {
	var runs []fakeSafeLocalRun

	data, err := os.ReadFile(fakeSafeLocalRunsPath())
	if err == nil {
		_ = json.Unmarshal(data, &runs)
	}

	return runs
}

func writeFakeSafeLocalRuns(runs []fakeSafeLocalRun) error {
	data, err := json.Marshal(runs)
	if err != nil {
		return err
	}

	return os.WriteFile(fakeSafeLocalRunsPath(), data, 0o600)
}

// fakeSafeState is one run of the fake safe.
type fakeSafeState struct {
	rc       *fakeSafeRC
	pinned   string
	keepFlag bool
	// dirty is set by every command that changes ~/.saferc. Only those
	// write it back, so a read-only call leaves the file untouched.
	dirty  bool
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
}

// runFakeSafeHelper emulates the parts of safe that ocfp's inception vault
// code uses, with safe's own rules for which commands select a target:
// `target <name>`, `target <name> <url>` without --keep-current, and `local`
// without --keep-current make the target current, and deleting the current
// target leaves none current. Deleting a target safe does not know fails, and
// a `safe local` that shuts down removes its own target and puts back the
// target that was current when it started, as safe's restoreTarget does.
func runFakeSafeHelper() int {
	args := os.Args[1:]
	if len(args) == 0 || args[0] != fakeSafeLocalExit {
		logFakeSafeCall(args)
	}

	rc, err := readFakeSafeRC()
	if err != nil {
		fmt.Fprintf(os.Stderr, "!! %s\n", err)

		return 1
	}

	state := &fakeSafeState{
		rc:       rc,
		keepFlag: os.Getenv(fakeSafeNoKeepCurrentEnv) != "1",
		stdin:    os.Stdin,
		stdout:   os.Stdout,
		stderr:   os.Stderr,
	}

	for len(args) > 1 && (args[0] == "-T" || args[0] == "--target") {
		state.pinned = args[1]
		args = args[2:]
	}

	if len(args) == 0 {
		return 0
	}

	code := state.dispatch(args[0], args[1:])
	if code == 0 && state.dirty {
		err = writeFakeSafeRCFile(state.rc)
		if err != nil {
			fmt.Fprintf(os.Stderr, "!! %s\n", err)

			return 1
		}
	}

	return code
}

func logFakeSafeCall(args []string) {
	logFile := os.Getenv(fakeSafeLogEnv)
	if logFile == "" {
		return
	}

	line, _ := json.Marshal(fakeSafeCall{Args: args, SafeTarget: os.Getenv("SAFE_TARGET")})

	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) // #nosec G304 -- test-owned log
	if err != nil {
		return
	}

	defer func() { _ = f.Close() }()

	_, _ = f.Write(append(line, '\n'))
}

func (s *fakeSafeState) dispatch(command string, rest []string) int {
	switch command {
	case "--version":
		fmt.Fprintln(s.stdout, "safe v1.27.0\n  commit 0000000")

		return 0
	case "help":
		return s.help(rest)
	case "target":
		return s.target(rest)
	case "targets":
		if !s.safeTargetKnown() {
			return 1
		}

		return s.targets()
	case "local":
		return s.local(rest)
	case "auth":
		return s.auth()
	case fakeSafeLocalExit:
		return s.localExit()
	default:
		return 0
	}
}

func (s *fakeSafeState) help(rest []string) int {
	keep := ""
	if s.keepFlag {
		keep = " [--keep-current]"
	}

	if len(rest) > 0 && rest[0] == "target" {
		fmt.Fprintln(s.stdout, "safe target [-i] [name [url]]"+keep)
	} else {
		fmt.Fprintln(s.stdout, fakeSafeLocalHelpBase+keep)
	}

	return 0
}

// splitFlags separates the --flags in args from the positional arguments.
func splitFlags(args []string) ([]string, []string) {
	var flags, positional []string

	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			flags = append(flags, arg)
		} else {
			positional = append(positional, arg)
		}
	}

	return flags, positional
}

func (s *fakeSafeState) target(rest []string) int {
	flags, positional := splitFlags(rest)
	keep := slices.Contains(flags, "--keep-current")

	if keep && !s.keepFlag {
		fmt.Fprintln(s.stderr, "!! unrecognized flag `--keep-current`")

		return 1
	}

	if len(positional) > 0 && positional[0] == "delete" {
		if !s.safeTargetKnown() {
			return 1
		}

		return s.deleteTarget(positional[1:])
	}

	switch len(positional) {
	case 0:
		return s.showCurrent(slices.Contains(flags, "--json"))
	case 1:
		if keep {
			fmt.Fprintln(s.stderr, "!! --keep-current only applies to `safe target <name> <url>'")

			return 1
		}

		if _, ok := s.rc.Vaults[positional[0]]; !ok {
			fmt.Fprintf(s.stderr, "!! Unknown target '%s'\n", positional[0])

			return 1
		}

		s.rc.Current = positional[0]
		s.dirty = true

		return 0
	default:
		name, url := positional[0], positional[1]
		vault := &fakeSafeVault{URL: url}

		if existing, ok := s.rc.Vaults[name]; ok && existing.URL == url {
			vault.Token = existing.Token
		}

		s.rc.Vaults[name] = vault
		s.dirty = true

		if keep {
			fmt.Fprintf(s.stderr, "Added target %s at %s\n", name, url)

			return 0
		}

		s.rc.Current = name
		fmt.Fprintf(s.stderr, "Currently targeting %s at %s\n", name, url)

		return 0
	}
}

// safeTargetKnown reports whether SAFE_TARGET is unset or names a target
// safe knows. `safe targets` and `safe target delete` look it up before
// they do anything, as real safe does, and fail when it names none.
func (s *fakeSafeState) safeTargetKnown() bool {
	name := os.Getenv("SAFE_TARGET")
	if name == "" {
		return true
	}

	if _, ok := s.alias(name); ok {
		return true
	}

	fmt.Fprintf(s.stderr, "!! Target '%s' (from $SAFE_TARGET) not found in ~/.saferc\n", name)

	return false
}

// deleteTarget removes one target, found by its name or by its URL, as safe's
// `target delete` does. A name safe does not know is an error. The current
// target is cleared only when it no longer names a target that exists.
func (s *fakeSafeState) deleteTarget(names []string) int {
	if len(names) != 1 {
		fmt.Fprintln(s.stderr, "!! usage: safe target delete <name>")

		return 1
	}

	name, ok := s.alias(names[0])
	if !ok {
		fmt.Fprintf(s.stderr, "!! Unknown target '%s'\n", names[0])

		return 1
	}

	delete(s.rc.Vaults, name)

	if _, ok := s.alias(s.rc.Current); !ok {
		s.rc.Current = ""
	}

	s.dirty = true

	return 0
}

// alias resolves a target by its name, or by its URL when exactly one target
// has that URL, as safe does.
func (s *fakeSafeState) alias(name string) (string, bool) {
	if _, ok := s.rc.Vaults[name]; ok {
		return name, true
	}

	found := ""
	n := 0

	for alias, vault := range s.rc.Vaults {
		if strings.TrimSuffix(vault.URL, "/") == strings.TrimSuffix(name, "/") {
			found = alias
			n++
		}
	}

	return found, n == 1
}

func (s *fakeSafeState) showCurrent(asJSON bool) int {
	url := ""
	if vault, ok := s.rc.Vaults[s.rc.Current]; ok {
		url = vault.URL
	}

	if asJSON {
		out, _ := json.Marshal(map[string]string{"name": s.rc.Current, "url": url})
		fmt.Fprintln(s.stdout, string(out))

		return 0
	}

	if s.rc.Current == "" {
		fmt.Fprintln(s.stderr, "No Vault currently targeted")

		return 0
	}

	fmt.Fprintf(s.stderr, "Currently targeting %s at %s\n", s.rc.Current, url)

	return 0
}

func (s *fakeSafeState) targets() int {
	names := make([]string, 0, len(s.rc.Vaults))
	for name := range s.rc.Vaults {
		names = append(names, name)
	}

	slices.Sort(names)

	list := make([]map[string]string, 0, len(names))
	for _, name := range names {
		list = append(list, map[string]string{"name": name, "url": s.rc.Vaults[name].URL})
	}

	out, _ := json.Marshal(list)
	fmt.Fprintln(s.stdout, string(out))

	return 0
}

func (s *fakeSafeState) local(rest []string) int {
	var name, port, rootTokenFile string

	keep := false

	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case "--keep-current":
			keep = true
		case "--as":
			i++
			name = rest[i]
		case "--port":
			i++
			port = rest[i]
		case "--root-token-file":
			i++
			rootTokenFile = rest[i]
		case "--raft", "--file", "--cluster-port", "--engine":
			i++
		}
	}

	// A restart opens the vault with the saved root token, and safe stores
	// that token on the target.
	token := fakeSafeRootToken
	if rootTokenFile != "" {
		saved, err := os.ReadFile(rootTokenFile) // #nosec G304 -- the test's own key file
		if err != nil {
			fmt.Fprintf(s.stderr, "!! %s\n", err)

			return 1
		}

		token = strings.TrimSpace(string(saved))
	}

	if keep && !s.keepFlag {
		fmt.Fprintln(s.stderr, "!! unrecognized flag `--keep-current`")

		return 1
	}

	if _, ok := s.rc.Vaults[name]; ok {
		fmt.Fprintf(s.stderr, "!! You already have '%s' as a Vault target\n", name)

		return 1
	}

	url := "http://127.0.0.1:" + port
	previous := s.rc.Current
	s.rc.Vaults[name] = &fakeSafeVault{URL: url, Token: token}

	if !keep {
		s.rc.Current = name
	}

	err := writeFakeSafeRCFile(s.rc)
	if err != nil {
		fmt.Fprintf(s.stderr, "!! %s\n", err)

		return 1
	}

	err = writeFakeSafeLocalRuns(append(readFakeSafeLocalRuns(), fakeSafeLocalRun{Name: name, URL: url, Previous: previous}))
	if err != nil {
		fmt.Fprintf(s.stderr, "!! %s\n", err)

		return 1
	}

	if started := os.Getenv(fakeSafeLocalStartedEnv); started != "" {
		_ = os.WriteFile(started, nil, 0o600)
	}

	fmt.Fprintf(s.stdout, "Your OpenBao Seal Key is %s\n", testSealKey)

	if keep {
		fmt.Fprintf(s.stdout, "Registered the temporary target %s at %s; it is not selected, and your current target is unchanged\n",
			name, url)
	} else {
		fmt.Fprintf(s.stdout, "Now targeting (temporary) %s at %s\n", name, url)
	}

	return 0
}

// localExit shuts down every `safe local` the fake has running, the way
// safe's restoreTarget does: a target whose URL has changed since it was
// registered is left alone, and otherwise it is removed, and when it was
// current the target that was current before it is put back if it still
// exists.
func (s *fakeSafeState) localExit() int {
	for _, run := range readFakeSafeLocalRuns() {
		if existing, ok := s.rc.Vaults[run.Name]; ok && existing.URL != run.URL {
			continue
		}

		if s.rc.Current == run.Name {
			s.rc.Current = ""
			if _, ok := s.alias(run.Previous); ok {
				s.rc.Current = run.Previous
			}
		}

		delete(s.rc.Vaults, run.Name)

		s.dirty = true
	}

	err := writeFakeSafeLocalRuns(nil)
	if err != nil {
		fmt.Fprintf(s.stderr, "!! %s\n", err)

		return 1
	}

	return 0
}

func (s *fakeSafeState) auth() int {
	name := s.pinned
	if name == "" {
		name = os.Getenv("SAFE_TARGET")
	}

	if name == "" {
		name = s.rc.Current
	}

	vault, ok := s.rc.Vaults[name]
	if !ok {
		fmt.Fprintf(s.stderr, "!! Unknown target '%s'\n", name)

		return 1
	}

	token, _ := bufio.NewReader(s.stdin).ReadString('\n')
	vault.Token = strings.TrimSpace(token)
	s.dirty = true

	return 0
}

// installFakeSafe writes a safe wrapper into bin that runs this test binary
// as the fake safe, logging every call to the returned log file.
func installFakeSafe(t *testing.T, bin string) (string, string) {
	t.Helper()

	exe, err := os.Executable()
	require.NoError(t, err)

	logFile := filepath.Join(t.TempDir(), "safe-calls.jsonl")
	t.Setenv(fakeSafeLogEnv, logFile)

	safe := writeFakeExecutable(t, bin, "safe", fakeSafeHelperEnv+"=1 exec '"+exe+"' \"$@\"\n")

	return safe, logFile
}

// readFakeSafeCalls returns every call the fake safe logged.
func readFakeSafeCalls(t *testing.T, logFile string) []fakeSafeCall {
	t.Helper()

	data, err := os.ReadFile(logFile) // #nosec G304 -- test-owned log
	if os.IsNotExist(err) {
		return nil
	}

	require.NoError(t, err)

	var calls []fakeSafeCall

	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}

		var call fakeSafeCall
		require.NoError(t, json.Unmarshal([]byte(line), &call))

		calls = append(calls, call)
	}

	return calls
}

// fakeSafeHome is a temp home with the fake safe on PATH and an rc holding
// the given targets, with current selected.
func fakeSafeHome(t *testing.T, current string, targets map[string]string) string {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SAFE_TARGET", "")
	require.NoError(t, os.Unsetenv("SAFE_TARGET"))

	safe, _ := installFakeSafe(t, home)

	rc := &fakeSafeRC{Version: 1, Current: current, Vaults: map[string]*fakeSafeVault{}}
	for name, url := range targets {
		rc.Vaults[name] = &fakeSafeVault{URL: url, Token: "s." + name}
	}

	require.NoError(t, writeFakeSafeRCFile(rc))

	return safe
}

// runFakeSafe runs the fake safe with args and reports its exit status.
func runFakeSafe(t *testing.T, safe string, env []string, args ...string) int {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), safe, args...) // #nosec G204 -- the test's own fake
	cmd.Env = append(os.Environ(), env...)

	err := cmd.Run()

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}

	require.NoError(t, err)

	return 0
}

// The fake behaves as safe does where the regression tests lean on it. A
// read-only call leaves ~/.saferc untouched, deleting a target safe does not
// know fails, an unknown SAFE_TARGET fails the calls that look it up, and a
// `safe local` that shuts down removes its own target and puts back the
// target that was current when it started.
func TestFakeSafe_BehavesAsSafeDoes(t *testing.T) {
	t.Run("read-only calls leave the rc untouched", func(t *testing.T) {
		safe := fakeSafeHome(t, "ops", map[string]string{"ops": "https://ops.example:8200"})
		require.NoError(t, os.WriteFile(fakeSafeRCPath(), []byte("current: ops\nversion: 1\nvaults:\n  ops: {url: \"https://ops.example:8200\"}\n"), 0o600))

		before, err := os.ReadFile(fakeSafeRCPath())
		require.NoError(t, err)

		for _, args := range [][]string{{"--version"}, {"help", "local"}, {"targets", "--json"}, {"target"}} {
			require.Equal(t, 0, runFakeSafe(t, safe, nil, args...), args)
		}

		after, err := os.ReadFile(fakeSafeRCPath())
		require.NoError(t, err)
		assert.Equal(t, string(before), string(after))
	})

	t.Run("deleting an unknown target fails", func(t *testing.T) {
		safe := fakeSafeHome(t, "ops", map[string]string{"ops": "https://ops.example:8200"})
		assert.Equal(t, 1, runFakeSafe(t, safe, nil, "target", "delete", "ghost"))
	})

	t.Run("an unknown SAFE_TARGET fails the lookups", func(t *testing.T) {
		safe := fakeSafeHome(t, "ops", map[string]string{"ops": "https://ops.example:8200"})
		assert.Equal(t, 1, runFakeSafe(t, safe, []string{"SAFE_TARGET=ghost"}, "targets", "--json"))
		assert.Equal(t, 1, runFakeSafe(t, safe, []string{"SAFE_TARGET=ghost"}, "target", "delete", "ops"))
		assert.Equal(t, 0, runFakeSafe(t, safe, []string{"SAFE_TARGET=ops"}, "targets", "--json"))
	})

	t.Run("safe local cleans up after itself on shutdown", func(t *testing.T) {
		safe := fakeSafeHome(t, "ops", map[string]string{"ops": "https://ops.example:8200"})
		require.Equal(t, 0, runFakeSafe(t, safe, nil, "local", "--as", "tmp", "--port", "8201"))

		rc, err := readFakeSafeRC()
		require.NoError(t, err)
		require.Equal(t, "tmp", rc.Current)

		require.Equal(t, 0, runFakeSafe(t, safe, nil, fakeSafeLocalExit))

		rc, err = readFakeSafeRC()
		require.NoError(t, err)
		assert.Equal(t, "ops", rc.Current)
		assert.NotContains(t, rc.Vaults, "tmp")
	})

	t.Run("safe local leaves a target whose URL changed", func(t *testing.T) {
		safe := fakeSafeHome(t, "", map[string]string{})
		require.Equal(t, 0, runFakeSafe(t, safe, nil, "local", "--as", "tmp", "--port", "8201", "--keep-current"))
		require.Equal(t, 0, runFakeSafe(t, safe, nil, "target", "tmp", "http://127.0.0.1:9999", "--keep-current"))
		require.Equal(t, 0, runFakeSafe(t, safe, nil, fakeSafeLocalExit))

		rc, err := readFakeSafeRC()
		require.NoError(t, err)
		assert.Contains(t, rc.Vaults, "tmp")
	})
}
