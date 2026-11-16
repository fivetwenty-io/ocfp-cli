package jobexec

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
	"github.com/fivetwenty-io/ocfp-cli/internal/safedir"
	"github.com/fivetwenty-io/ocfp-cli/internal/strictjson"
)

// The files in a spool directory. A run writes every record of its stream to
// out, writes status.json when it starts and again when it ends, and writes
// exit.json once out holds the whole stream. A cancel writes cancel.json, and
// only a cancel does.
const (
	outFile    = "out"
	statusFile = "status.json"
	exitFile   = "exit.json"
	cancelFile = "cancel.json"
)

// ErrSpoolNotFound is what OpenSpool returns for a job that has no spool.
var ErrSpoolNotFound = errors.New("spool: no such spool")

// The states a status names.
const (
	StateRunning = "running"
	StateExited  = "exited"
)

// maxSpoolJSON bounds how much of status.json or exit.json is read.
const maxSpoolJSON = 64 << 10

// Spool is the directory that holds one job's stream and state. Its name is
// the job id, under a spool root that only this host's user can write.
type Spool struct {
	Dir string
}

// Status is the content of status.json. ExitCode is null while the job runs.
// WireContract is the contract of the run that wrote it, and a run of a
// contract before 3 left it out, so a cancel can tell a job that would never
// read a cancel marker.
type Status struct {
	State        string    `json:"state"`
	PID          int       `json:"pid"`
	StartedAt    time.Time `json:"startedAt"`
	ExitCode     *int      `json:"exitCode"`
	WireContract int       `json:"wireContract,omitempty"`
}

// Exit is the content of exit.json, which a run writes once its whole stream
// is in out. Cancel is set only when a cancel ended the job, and then to
// stopped or killed, never to already_finished, which a requester derives and
// a run never stores. CancelSeenAt is when the run first saw the marker.
type Exit struct {
	ExitCode     int                    `json:"exitCode"`
	FinishedAt   time.Time              `json:"finishedAt"`
	Cancel       progress.CancelOutcome `json:"cancel,omitempty"`
	CancelSeenAt *time.Time             `json:"cancelSeenAt,omitempty"`
}

// Cancel is the content of cancel.json. RequestedAt is the cancel process's
// clock reading, for people to read. The run's cancel budget starts from its
// own reading when it first sees the marker.
type Cancel struct {
	RequestedAt time.Time `json:"requestedAt"`
}

// CreateSpool makes the spool directory root/jobID with os.Mkdir, so that of
// any number of runs for one job exactly one sees created true. Every later
// run gets the same Spool with created false, and attaches to it.
//
// The job id must match ^[a-z0-9][a-z0-9-]{0,63}$, so that it names one entry
// directly under root. The root must be a directory, not a symbolic link,
// owned by this user, and not writable by group or other, because whoever
// can write it can plant a job's spool. Every directory above the root, and
// every symbolic link on the way to it, must be owned by this user or by
// root, and a directory above it that group or other can write must be
// sticky, because whoever can rename an ancestor can swap in their own
// root. CreateSpool cleans root first, so a ".." in it climbs the names
// as written and not through a link. It then resolves the symbolic links
// above root, and the Spool it returns names the resolved directory, so a
// link that changes later cannot move where the spool reads and writes. It
// makes a missing root with mode 0700, but never its parent. An existing
// spool path must pass the same checks, so a file, a symbolic link, or a
// directory that others can write is never taken for a job's spool.
func CreateSpool(root, jobID string) (s Spool, created bool, err error) {
	if !jobIDPattern.MatchString(jobID) {
		return Spool{}, false, fmt.Errorf("spool: job id %s is not of the form %s", quote(jobID), jobIDPattern)
	}

	if root == "" {
		return Spool{}, false, errors.New("spool: no spool root")
	}

	root, err = prepareRoot(filepath.Clean(root))
	if err != nil {
		return Spool{}, false, err
	}

	s = Spool{Dir: filepath.Join(root, jobID)}

	err = os.Mkdir(s.Dir, 0o700)
	if err == nil {
		return s, true, nil
	}

	if !errors.Is(err, fs.ErrExist) {
		return Spool{}, false, fmt.Errorf("spool: %w", err)
	}

	err = checkPrivateDir(s.Dir, "spool directory")
	if err != nil {
		return Spool{}, false, err
	}

	return s, false, nil
}

// OpenSpool returns the spool of a job that has one, and never makes a root or
// a spool. It makes the checks CreateSpool makes on the job id, the root and
// its ancestors, and the spool directory. A job with no spool, or a root that
// does not exist, gives an error that wraps ErrSpoolNotFound.
func OpenSpool(root, jobID string) (Spool, error) {
	if !jobIDPattern.MatchString(jobID) {
		return Spool{}, fmt.Errorf("spool: job id %s is not of the form %s", quote(jobID), jobIDPattern)
	}

	if root == "" {
		return Spool{}, errors.New("spool: no spool root")
	}

	root, err := checkRoot(filepath.Clean(root), false)
	if err != nil {
		return Spool{}, err
	}

	s := Spool{Dir: filepath.Join(root, jobID)}

	_, err = os.Lstat(s.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return Spool{}, fmt.Errorf("%w: job %s", ErrSpoolNotFound, jobID)
	}

	if err != nil {
		return Spool{}, fmt.Errorf("spool: %w", err)
	}

	err = checkPrivateDir(s.Dir, "spool directory")
	if err != nil {
		return Spool{}, err
	}

	return s, nil
}

// prepareRoot checks the root as checkRoot does and makes it when it is
// missing.
func prepareRoot(root string) (string, error) {
	return checkRoot(root, true)
}

// checkRoot checks that no other user can replace root by way of one of
// its ancestors, makes root when it is missing and create is set, and checks
// that it is a private directory. It returns root with the symbolic links
// above it resolved, which is the root it checked and the one the caller must
// use. A missing root with create unset wraps ErrSpoolNotFound.
func checkRoot(root string, create bool) (string, error) {
	resolved, err := safedir.Resolve(root)
	if err != nil {
		return "", fmt.Errorf("spool: %w", err)
	}

	if create {
		err = os.Mkdir(resolved, 0o700)
		if err != nil && !errors.Is(err, fs.ErrExist) {
			return "", fmt.Errorf("spool: make root: %w", err)
		}
	} else if _, serr := os.Lstat(resolved); errors.Is(serr, fs.ErrNotExist) {
		return "", fmt.Errorf("%w: no spool root %s", ErrSpoolNotFound, resolved)
	}

	err = checkPrivateDir(resolved, "spool root")
	if err != nil {
		return "", err
	}

	return resolved, nil
}

// checkPrivateDir checks that path is a directory, not a symbolic link, owned
// by this user, and not writable by group or other.
func checkPrivateDir(path, what string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("spool: %w", err)
	}

	if info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("spool: %s %s is a symbolic link", what, path)
	}

	if !info.IsDir() {
		return fmt.Errorf("spool: %s %s is not a directory", what, path)
	}

	return checkOwnedPrivate(info, what, path)
}

// WriteStatus replaces status.json with st. It writes a temporary file, syncs
// it, and renames it into place, so a reader sees the old status or the new
// one and never part of either.
func (s Spool) WriteStatus(st Status) error {
	return s.writeJSON(statusFile, st)
}

// WriteCancel publishes the cancel marker. It writes a temporary file, syncs
// it, and links it to cancel.json, which fails when a marker is already there.
// It reports true when this call made the marker, and false when one was
// already there, which is not an error, so two cancels leave one marker and
// both succeed. The marker never replaces another, so a reader sees a whole
// marker or none.
func (s Spool) WriteCancel(c Cancel) (created bool, err error) {
	data, err := json.Marshal(c)
	if err != nil {
		return false, fmt.Errorf("spool: encode %s: %w", cancelFile, err)
	}

	tmp, err := os.CreateTemp(s.Dir, "."+cancelFile+".tmp-*")
	if err != nil {
		return false, fmt.Errorf("spool: write %s: %w", cancelFile, err)
	}

	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()

	_, err = tmp.Write(append(data, '\n'))
	if err == nil {
		err = tmp.Sync()
	}

	if err != nil {
		return false, fmt.Errorf("spool: write %s: %w", cancelFile, err)
	}

	err = os.Link(tmp.Name(), filepath.Join(s.Dir, cancelFile))
	if errors.Is(err, fs.ErrExist) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("spool: write %s: %w", cancelFile, err)
	}

	err = syncDir(s.Dir)
	if err != nil {
		return true, fmt.Errorf("spool: sync %s: %w", s.Dir, err)
	}

	return true, nil
}

// WriteExit writes exit.json the same way. A run calls it only once out holds
// the whole stream, so an attach that sees exit.json reads a complete out.
func (s Spool) WriteExit(e Exit) error {
	return s.writeJSON(exitFile, e)
}

func (s Spool) writeJSON(name string, v any) (err error) {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("spool: encode %s: %w", name, err)
	}

	tmp, err := os.CreateTemp(s.Dir, "."+name+".tmp-*")
	if err != nil {
		return fmt.Errorf("spool: write %s: %w", name, err)
	}

	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()

	_, err = tmp.Write(append(data, '\n'))
	if err == nil {
		err = tmp.Sync()
	}

	if err != nil {
		return fmt.Errorf("spool: write %s: %w", name, err)
	}

	err = tmp.Close()
	if err != nil {
		return fmt.Errorf("spool: write %s: %w", name, err)
	}

	err = os.Rename(tmp.Name(), filepath.Join(s.Dir, name))
	if err != nil {
		return fmt.Errorf("spool: write %s: %w", name, err)
	}

	err = syncDir(s.Dir)
	if err != nil {
		return fmt.Errorf("spool: sync %s: %w", s.Dir, err)
	}

	return nil
}

// createOut makes out, which must not exist yet, for appending.
func (s Spool) createOut() (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(s.Dir, outFile), os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("spool: create out: %w", err)
	}

	return f, nil
}

// openOut opens out for reading. It reports false when out does not exist.
func (s Spool) openOut() (*os.File, bool, error) {
	f, err := os.Open(filepath.Join(s.Dir, outFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}

	if err != nil {
		return nil, false, fmt.Errorf("spool: open out: %w", err)
	}

	return f, true, nil
}

// readStatus reads status.json. It reports false when the file does not
// exist.
func (s Spool) readStatus() (Status, bool, error) {
	var st Status

	ok, err := s.readJSON(statusFile, &st)
	if err != nil || !ok {
		return Status{}, ok, err
	}

	if st.State != StateRunning && st.State != StateExited {
		return Status{}, false, fmt.Errorf("spool: %s names the state %s", statusFile, quote(st.State))
	}

	return st, true, nil
}

// readExit reads exit.json. It reports false when the file does not exist.
func (s Spool) readExit() (Exit, bool, error) {
	var e Exit

	ok, err := s.readJSON(exitFile, &e)
	if err != nil || !ok {
		return Exit{}, ok, err
	}

	switch e.Cancel {
	case "", progress.CancelStopped, progress.CancelKilled:
	case progress.CancelAlreadyFinished:
		return Exit{}, false, fmt.Errorf("spool: %s names the cancel outcome %s, which a run never stores", exitFile, e.Cancel)
	default:
		return Exit{}, false, fmt.Errorf("spool: %s names the cancel outcome %s", exitFile, quote(string(e.Cancel)))
	}

	return e, true, nil
}

// readCancel reads cancel.json. It reports false when the file does not
// exist.
func (s Spool) readCancel() (Cancel, bool, error) {
	var c Cancel

	ok, err := s.readJSON(cancelFile, &c)
	if err != nil || !ok {
		return Cancel{}, ok, err
	}

	return c, true, nil
}

// cancelled reports whether the spool holds a cancel marker. A marker that
// opens and reads but does not decode, or is too large, still counts,
// because only a cancel writes one. A marker that cannot be opened or read
// is not seen yet, since that can happen with no marker there at all, as
// when the process is out of descriptors, and the run looks again at its
// next poll.
func (s Spool) cancelled() bool {
	_, ok, err := s.readCancel()

	var bad *malformedError

	return ok || errors.As(err, &bad)
}

// malformedError is a spool file that opened and read in full but does not
// hold what it should.
type malformedError struct {
	err error
}

func (e *malformedError) Error() string { return e.err.Error() }

func (e *malformedError) Unwrap() error { return e.err }

// readJSON decodes name strictly into v. It reports false when name does not
// exist. A file that opened and read but is too large or does not decode
// gives a *malformedError.
func (s Spool) readJSON(name string, v any) (bool, error) {
	f, err := os.Open(filepath.Join(s.Dir, name)) //nolint:gosec // name is one of the spool's own file names
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("spool: %w", err)
	}

	defer func() { _ = f.Close() }()

	data, err := io.ReadAll(io.LimitReader(f, maxSpoolJSON+1))
	if err != nil {
		return false, fmt.Errorf("spool: read %s: %w", name, err)
	}

	if len(data) > maxSpoolJSON {
		return false, &malformedError{err: fmt.Errorf("spool: %s is larger than %d bytes", name, maxSpoolJSON)}
	}

	err = strictjson.Decode(data, v)
	if err != nil {
		return false, &malformedError{err: fmt.Errorf("spool: %s: %w", name, err)}
	}

	return true, nil
}
