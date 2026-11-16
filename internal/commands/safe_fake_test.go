package commands

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
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

// fakeSafeState is one run of the fake safe.
type fakeSafeState struct {
	rc       *fakeSafeRC
	pinned   string
	keepFlag bool
	stdin    io.Reader
	stdout   io.Writer
	stderr   io.Writer
}

// runFakeSafeHelper emulates the parts of safe that ocfp's inception vault
// code uses, with safe's own rules for which commands select a target:
// `target <name>`, `target <name> <url>` without --keep-current, and `local`
// without --keep-current make the target current, and deleting the current
// target leaves none current.
func runFakeSafeHelper() int {
	args := os.Args[1:]
	logFakeSafeCall(args)

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
	if code == 0 {
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
		fmt.Fprintln(s.stdout, "safe v1.25.0\n  commit 0000000")

		return 0
	case "help":
		return s.help(rest)
	case "target":
		return s.target(rest)
	case "targets":
		return s.targets()
	case "local":
		return s.local(rest)
	case "auth":
		return s.auth()
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
		for _, name := range positional[1:] {
			delete(s.rc.Vaults, name)

			if s.rc.Current == name {
				s.rc.Current = ""
			}
		}

		return 0
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

		return 0
	default:
		name, url := positional[0], positional[1]
		vault := &fakeSafeVault{URL: url}

		if existing, ok := s.rc.Vaults[name]; ok && existing.URL == url {
			vault.Token = existing.Token
		}

		s.rc.Vaults[name] = vault

		if keep {
			fmt.Fprintf(s.stderr, "Added target %s at %s\n", name, url)

			return 0
		}

		s.rc.Current = name
		fmt.Fprintf(s.stderr, "Currently targeting %s at %s\n", name, url)

		return 0
	}
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
	var name, port string

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
		case "--raft", "--file", "--cluster-port", "--engine", "--root-token-file":
			i++
		}
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
	s.rc.Vaults[name] = &fakeSafeVault{URL: url, Token: fakeSafeRootToken}

	if !keep {
		s.rc.Current = name
	}

	err := writeFakeSafeRCFile(s.rc)
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
