// Package testenv gives a test binary a home of its own.
//
// ocfp finds its config, state, data, keys, and logs from HOME, the XDG
// base directory variables, and OCFP_HOME. A package whose tests reach any
// of those calls Main or Run from its TestMain. Before any test runs, they
// point all of those variables at one temporary tree, so no test can find
// the developer's real directories through them. They also mark the
// process as a test process for the write guard, which panics if a write
// still heads for a real directory.
//
// Moving HOME would move the Go build and module caches as well, for any
// test that runs the go command, so the helper pins them where they were.
package testenv

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/fivetwenty-io/ocfp-cli/internal/writeguard"
)

// Runner is what a TestMain receives. *testing.M satisfies it.
type Runner interface {
	Run() int
}

// homeVars names the variables the tree replaces, each with the directory
// inside the tree it gets.
var homeVars = [...]struct{ name, dir string }{ //nolint:gochecknoglobals // a fixed table
	{"HOME", "home"},
	{"XDG_CONFIG_HOME", "config"},
	{"XDG_STATE_HOME", "state"},
	{"XDG_DATA_HOME", "data"},
	{"XDG_CACHE_HOME", "cache"},
	{"OCFP_HOME", "ocfp"},
}

// goVars are the go command's settings that depend on HOME and that the
// tree must not move.
var goVars = [...]string{"GOENV", "GOCACHE", "GOMODCACHE", "GOPATH"} //nolint:gochecknoglobals // a fixed list

// Main runs m's tests in a temporary home and exits with their code.
func Main(m Runner) {
	os.Exit(Run(m.Run))
}

// Run runs run in a temporary home, removes the home, and returns run's
// code. It returns 1 without running anything when it can't set the home
// up, after saying why on stderr.
func Run(run func() int) int {
	tree, err := os.MkdirTemp("", "ocfp-test-home-*")
	if err != nil {
		return failed(os.Stderr, fmt.Errorf("make the temporary home: %w", err))
	}

	defer func() { _ = os.RemoveAll(tree) }()

	err = Isolate(tree, goEnv)
	if err != nil {
		return failed(os.Stderr, err)
	}

	return run()
}

// Isolate points HOME, the XDG variables, and OCFP_HOME at directories it
// makes inside tree, and marks the process as a test process. Before it
// moves HOME it pins the go command's caches to where goSettings says they
// are, and it records the ocfp directories that the variables named, so
// the write guard keeps guarding them.
func Isolate(tree string, goSettings func() (map[string]string, error)) error {
	pinned, err := goSettings()
	if err != nil {
		return err
	}

	for _, name := range goVars {
		if value := pinned[name]; value != "" {
			err = os.Setenv(name, value)
			if err != nil {
				return fmt.Errorf("pin %s: %w", name, err)
			}
		}
	}

	// A test binary that another test process started keeps the roots it
	// was given, since its HOME and XDG variables already name a tree.
	if os.Getenv(writeguard.EnvVar) == "" {
		err = os.Setenv(writeguard.RootsEnvVar, strings.Join(namedRoots(), string(os.PathListSeparator)))
		if err != nil {
			return fmt.Errorf("record the real ocfp directories: %w", err)
		}
	}

	for _, v := range homeVars {
		dir := filepath.Join(tree, v.dir)

		err = os.MkdirAll(dir, 0o700)
		if err != nil {
			return fmt.Errorf("make the temporary %s: %w", v.name, err)
		}

		err = os.Setenv(v.name, dir)
		if err != nil {
			return fmt.Errorf("set %s: %w", v.name, err)
		}
	}

	err = os.Setenv(writeguard.EnvVar, "1")
	if err != nil {
		return fmt.Errorf("mark the process as a test process: %w", err)
	}

	return nil
}

// OcfpHome returns the directory OCFP_HOME names, for a test that builds a
// path inside it. It panics when OCFP_HOME is empty or not absolute, since
// a path built from either would sit in the package directory, inside the
// source tree, where the write guard doesn't look.
func OcfpHome() string {
	home := os.Getenv("OCFP_HOME")
	if home == "" || !filepath.IsAbs(home) {
		panic(fmt.Sprintf("testenv: OCFP_HOME is %q, but this test needs an absolute directory; call testenv.Main or testenv.Run from TestMain", home))
	}

	return home
}

// namedRoots returns the ocfp directories that HOME, the XDG variables,
// and OCFP_HOME name now, before they move. They are the real ones on a
// developer's machine, whatever the variables are set to.
func namedRoots() []string {
	var roots []string

	if v := os.Getenv("OCFP_HOME"); v != "" {
		roots = append(roots, v)
	}

	if home := os.Getenv("HOME"); home != "" {
		roots = append(roots,
			filepath.Join(home, ".ocfp"),
			filepath.Join(home, ".config", "ocfp"),
			filepath.Join(home, ".local", "state", "ocfp"),
			filepath.Join(home, ".local", "share", "ocfp"),
			filepath.Join(home, ".cache", "ocfp"),
		)
	}

	for _, name := range []string{"XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME"} {
		if v := os.Getenv(name); v != "" {
			roots = append(roots, filepath.Join(v, "ocfp"))
		}
	}

	return roots
}

// goEnv asks the go command where its caches are. Without a go command on
// PATH no test can run one, so there is nothing to pin.
func goEnv() (map[string]string, error) {
	gocmd, err := exec.LookPath("go")
	if errors.Is(err, exec.ErrNotFound) {
		return map[string]string{}, nil
	}

	if err != nil {
		return nil, fmt.Errorf("find the go command: %w", err)
	}

	var stdout, stderr bytes.Buffer

	// A TestMain has no context of its own to pass on.
	cmd := exec.CommandContext(context.Background(), gocmd, append([]string{"env", "-json"}, goVars[:]...)...) // #nosec G204 -- the go command with fixed arguments
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	if err != nil {
		return nil, fmt.Errorf("go env: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	settings := map[string]string{}

	err = json.Unmarshal(stdout.Bytes(), &settings)
	if err != nil {
		return nil, fmt.Errorf("read go env's output: %w", err)
	}

	return settings, nil
}

// failed reports err on w and returns the code a failed run exits with.
func failed(w io.Writer, err error) int {
	_, _ = fmt.Fprintf(w, "testenv: cannot give the tests a home of their own: %v\n", err)

	return 1
}
