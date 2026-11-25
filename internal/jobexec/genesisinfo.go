package jobexec

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
)

// verbGenesisInfo is the verb that reports what Genesis knows about the
// environments on this host. It only reads.
const verbGenesisInfo = "genesis.info"

// The genesis.info verb registers itself in the verb table.
var _ = registerVerb(verbs, &verbErrs, verb{name: verbGenesisInfo, cancelBudget: 10 * time.Second, run: runGenesisInfo})

// envGenesisEnv is the one job request env entry that the wrapper passes on
// to the genesis.info verb. It names the environment to report on. The
// wrapper passes on no other entry, so a request cannot change how the verb
// finds Genesis or what Genesis reads.
const envGenesisEnv = "OCFP_GENESIS_ENV"

// envDeploymentsDir names the directory that holds the deployment
// repositories, as the bastion's shell environment sets it.
const envDeploymentsDir = "DEPLOYMENTS_DIR"

// genesisEnvName is what a Genesis environment name looks like. It cannot
// start with a dash, so a name never reads as a flag.
var genesisEnvName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// maxGenesisLine bounds one line of Genesis output.
const maxGenesisLine = 1 << 20

// genesisTarget is one environment and the deployment repository that holds
// it.
type genesisTarget struct {
	name string
	dir  string
}

// genesisInfo is the genesis.info verb, with what it reads from its host
// made explicit.
type genesisInfo struct {
	// lookPath finds the genesis executable.
	lookPath func(string) (string, error)

	// deployments is the directory that holds the deployment repositories.
	deployments string

	// env is the environment to report on. It is empty to report on every
	// environment under deployments.
	env string
}

// runGenesisInfo runs the genesis.info verb against the host it runs on.
func runGenesisInfo(ctx context.Context, j Job, _ func() (string, error)) (int, error) {
	dir := os.Getenv(envDeploymentsDir)
	if dir == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			dir = filepath.Join(home, "ocfp", "deployments")
		}
	}

	g := genesisInfo{lookPath: exec.LookPath, deployments: dir, env: os.Getenv(envGenesisEnv)}

	return g.run(ctx, j)
}

// run runs `genesis <env> info` for the environment it was given, or for
// every environment under the deployments directory when it was given none,
// in the repository that holds the environment. Genesis's output goes through
// j as log lines on the stream Genesis wrote it to, and a line "== <env> =="
// on stdout opens each environment's output. It returns the first non-zero
// exit code that Genesis gave, or 1 when it could not run Genesis at all,
// and an error only when a record could not be written or a cancel arrived.
// A cancel stops the Genesis process that is running.
func (g genesisInfo) run(ctx context.Context, j Job) (int, error) {
	err := j.Emit(progress.Event{Kind: progress.EventStepStarted, Step: verbGenesisInfo})
	if err != nil {
		return 0, err
	}

	code, err := g.report(ctx, j)
	if err != nil {
		return 0, err
	}

	outcome := outcomeSucceeded
	if code != 0 {
		outcome = outcomeFailed
	}

	err = j.Emit(progress.Event{Kind: progress.EventStepFinished, Step: verbGenesisInfo, Outcome: outcome})
	if err != nil {
		return 0, err
	}

	return code, nil
}

// report does the work of run, between its two records.
func (g genesisInfo) report(ctx context.Context, j Job) (int, error) {
	refuse := func(format string, args ...any) (int, error) {
		return 1, j.Log("stderr", fmt.Sprintf(format, args...))
	}

	path, err := g.lookPath("genesis")
	if err != nil {
		return refuse("genesis is not on this host's PATH: %s", quote(err.Error()))
	}

	targets, err := g.targets()
	if err != nil {
		return refuse("read the deployments directory: %s", quote(err.Error()))
	}

	if g.env != "" {
		i := slices.IndexFunc(targets, func(t genesisTarget) bool { return t.name == g.env })
		if i < 0 {
			return refuse("no environment %s under the deployments directory", quote(g.env))
		}

		targets = targets[i : i+1]
	}

	if len(targets) == 0 {
		return refuse("the deployments directory holds no environments")
	}

	code := 0

	for _, t := range targets {
		err = j.Checkpoint()
		if err != nil {
			return 0, err
		}

		err = j.Log("stdout", "== "+t.name+" ==")
		if err != nil {
			return 0, err
		}

		c, err := runGenesis(ctx, j, path, t)
		if err != nil {
			return 0, err
		}

		if code == 0 {
			code = c
		}
	}

	return code, nil
}

// targets lists the environments under the deployments directory, by
// repository and then by name. A repository is a directory that holds a
// .genesis directory, and its environments are the .yml files at its top.
// When g.env names an environment, only that one is checked for.
func (g genesisInfo) targets() ([]genesisTarget, error) {
	repos, err := os.ReadDir(g.deployments)
	if err != nil {
		return nil, err //nolint:wrapcheck // the message quotes it whole
	}

	var out []genesisTarget

	for _, r := range repos {
		dir := filepath.Join(g.deployments, r.Name())

		info, err := os.Stat(filepath.Join(dir, ".genesis"))
		if err != nil || !info.IsDir() {
			continue
		}

		files, err := os.ReadDir(dir)
		if err != nil {
			continue
		}

		for _, f := range files {
			name, ok := strings.CutSuffix(f.Name(), ".yml")
			if !ok || !f.Type().IsRegular() || !genesisEnvName.MatchString(name) {
				continue
			}

			out = append(out, genesisTarget{name: name, dir: dir})
		}
	}

	return out, nil
}

// runGenesis runs `genesis <name> info` in t's repository and relays its
// output. A cancel kills the process, and the error then is ErrCanceled. The
// exit code is the process's, or 1 when it could not be started or a signal
// ended it.
func runGenesis(ctx context.Context, j Job, path string, t genesisTarget) (int, error) {
	cctx, stop := context.WithCancel(ctx)
	defer stop()

	go func() {
		select {
		case <-j.Done():
			stop()
		case <-cctx.Done():
		}
	}()

	cmd := exec.CommandContext(cctx, path, t.name, "info") //nolint:gosec // the host's genesis, and a name checked against genesisEnvName
	cmd.Dir = t.dir
	cmd.Cancel = func() error { return cmd.Process.Kill() }
	cmd.WaitDelay = time.Second

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 1, j.Log("stderr", "genesis "+quote(t.name)+" info: "+quote(err.Error()))
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return 1, j.Log("stderr", "genesis "+quote(t.name)+" info: "+quote(err.Error()))
	}

	err = cmd.Start()
	if err != nil {
		return 1, j.Log("stderr", "start genesis: "+quote(err.Error()))
	}

	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		logErr error
	)

	relay := func(stream string, r io.Reader) {
		defer wg.Done()

		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64*1024), maxGenesisLine)

		for sc.Scan() {
			mu.Lock()
			if logErr == nil {
				logErr = j.Log(stream, sc.Text())
			}
			mu.Unlock()
		}

		// A line too long to hold ends the relay, and the rest is drained
		// so that Genesis is never blocked on a full pipe.
		_, _ = io.Copy(io.Discard, r)
	}

	wg.Add(2)

	go relay("stdout", stdout)
	go relay("stderr", stderr)

	wg.Wait()

	waitErr := cmd.Wait()

	cerr := j.Checkpoint()
	if cerr != nil {
		return 0, cerr
	}

	if logErr != nil {
		return 0, logErr
	}

	var exitErr *exec.ExitError

	switch {
	case waitErr == nil:
		return 0, nil
	case errors.As(waitErr, &exitErr) && exitErr.ExitCode() > 0:
		return exitErr.ExitCode(), nil
	default:
		return 1, j.Log("stderr", "genesis "+quote(t.name)+" info: "+quote(waitErr.Error()))
	}
}
