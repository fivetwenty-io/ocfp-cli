package commands

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/fivetwenty-io/ocfp-cli/internal/config"
	"github.com/fivetwenty-io/ocfp-cli/internal/logger"
	"go.uber.org/zap"
)

// initLogger returns the logger the init components write to. Tests
// replace it to read what a run logged.
var initLogger = logger.Get //nolint:gochecknoglobals // intentional seam for testing

// initRun is one run of an init component other than bastion. Each
// component runs the same steps in both forms, so a dry run makes every
// check the real run makes, in the same order, and refuses wherever the
// real run refuses. Only the changes differ. A real run makes each one,
// and a dry run logs it instead.
type initRun struct {
	dryRun bool
	log    *zap.SugaredLogger

	// pendingDirs holds the directories that a dry run's earlier steps
	// would have created, so a later check counts them as present.
	pendingDirs map[string]bool
}

// newInitRun returns a run that logs to the init logger.
func newInitRun(dryRun bool) *initRun {
	return &initRun{dryRun: dryRun, log: initLogger(), pendingDirs: map[string]bool{}}
}

// say logs a progress line of the real run. A dry run does none of the
// work these lines announce, so it doesn't log them.
func (r *initRun) say(msg string, keysAndValues ...any) {
	if !r.dryRun {
		r.log.Infow(msg, keysAndValues...)
	}
}

// warn logs a warning of the real run, such as a step that failed without
// stopping it.
func (r *initRun) warn(msg string, keysAndValues ...any) {
	r.log.Warnw(msg, keysAndValues...)
}

// change makes a change in a real run. A dry run logs what it would have
// done and makes nothing.
func (r *initRun) change(would string, apply func() error, keysAndValues ...any) error {
	if r.dryRun {
		r.log.Infow("DRY RUN: Would "+would, keysAndValues...)

		return nil
	}

	return apply()
}

// createsDir records that a dry run would have created dir, so the checks
// that follow treat it as present.
func (r *initRun) createsDir(dir string) {
	r.pendingDirs[filepath.Clean(dir)] = true
}

// finish logs the closing line of a dry run. A real run's components log
// their own closing lines.
func (r *initRun) finish(component string) {
	if r.dryRun {
		r.log.Infow("Dry run of init "+component+" finished without changing anything", "component", component)
	}
}

// startError is the error the real run gets when it starts name with args
// in dir, before the command itself runs. A dry run starts no command, so
// it makes the checks that starting one makes, in the same order. The
// command must be on PATH, the context must still be live, and dir must be
// a directory, which an empty dir always is. A directory that the dry
// run's own earlier steps would have created counts as present.
func (r *initRun) startError(ctx context.Context, dir, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204 -- never started; callers validate the args
	if cmd.Err != nil {
		return cmd.Err
	}

	err := ctx.Err()
	if err != nil {
		return err //nolint:wrapcheck // starting the command returns the context's error as it is
	}

	if dir == "" || r.pendingDirs[filepath.Clean(dir)] {
		return nil
	}

	info, err := os.Stat(dir)
	if err != nil {
		// Starting a command checks its directory with the same stat, and
		// reports the failure as a chdir.
		var pathErr *fs.PathError
		if errors.As(err, &pathErr) {
			return &fs.PathError{Op: "chdir", Path: pathErr.Path, Err: pathErr.Err}
		}

		return err //nolint:wrapcheck // os.Stat returns a *fs.PathError, so this is only a guard
	}

	if !info.IsDir() {
		// The stat passes, and the child then fails to change into it.
		return &fs.PathError{Op: "fork/exec", Path: cmd.Path, Err: syscall.ENOTDIR}
	}

	return nil
}

// writeDirError is the error the real run gets when it writes path and the
// directory that holds it is missing. A dry run writes nothing, so it
// checks the directory itself.
func (r *initRun) writeDirError(path string) error {
	dir := filepath.Dir(path)
	if r.pendingDirs[filepath.Clean(dir)] {
		return nil
	}

	return dirError("open", path, dir)
}

// dirError returns the *fs.PathError that op on path gets when dir is
// missing or isn't a directory, which is what the operating system reports
// to the real run. It returns nil when dir is a directory, or when it can't
// be read for a reason the real run would only meet when it tried, which
// it leaves to the real run.
func dirError(op, path, dir string) error {
	info, err := os.Stat(dir)

	switch {
	case errors.Is(err, fs.ErrNotExist):
		return &fs.PathError{Op: op, Path: path, Err: syscall.ENOENT}
	case err != nil:
		return nil //nolint:nilerr // the real run meets this error only when it tries, so the check leaves it to it
	case !info.IsDir():
		return &fs.PathError{Op: op, Path: path, Err: syscall.ENOTDIR}
	default:
		return nil
	}
}

// checkCurrentBlocState makes the read-only check of persistBlocToState,
// which loads the CLI state file before it sets the current bloc, and
// returns the error that persistBlocToState would.
func checkCurrentBlocState() error {
	_, err := config.LoadState()
	if err != nil {
		return fmt.Errorf("failed to persist bloc to state: failed to load state: %w", err)
	}

	return nil
}

// persistCurrentBloc checks the CLI state and then makes bloc the current
// bloc.
func (r *initRun) persistCurrentBloc(bloc string) error {
	err := checkCurrentBlocState()
	if err != nil {
		return err
	}

	return r.change("make "+bloc+" the current bloc", func() error { return persistBlocToState(bloc) }, "bloc", bloc)
}

// blocDeploymentFile resolves the path of a file in one of the bloc's
// deployment slots, under the bloc's resolved directory. It fails when the
// OCFP home can't be found or the bloc's directory can't be settled, such
// as when both data directories hold a vault for the bloc.
func blocDeploymentFile(bloc, slot, name string) (string, error) {
	if config.DataHome() == "" {
		return "", fmt.Errorf("cannot determine OCFP home directory: %w", config.ErrOcfpHomeNotFound)
	}

	blocDir, err := config.OcfpBlocDir(bloc)
	if err != nil {
		return "", fmt.Errorf("failed to resolve the directory for bloc %s: %w", bloc, err)
	}

	return filepath.Join(blocDir, "deployments", slot, name), nil
}

// writeBlocEnvFile resolves an env file's path, which checks the bloc's
// directory, and then writes the file with write.
func (r *initRun) writeBlocEnvFile(bloc, slot, envName, what string, write func() error) error {
	path, err := blocDeploymentFile(bloc, slot, bloc+"-"+envName+".yml")
	if err != nil {
		return err
	}

	return r.change("write the "+what, write, "path", path)
}
