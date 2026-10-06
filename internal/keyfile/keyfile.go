// Package keyfile reads, writes, and protects the key files of an inception
// vault: the root token in root.key, the unseal key, and the copy of the root
// token that safe holds for the bloc's own target. It sits below both the
// vault commands and the workstation teardown, so each stop path keeps the
// token the same way.
package keyfile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/goccy/go-yaml"
	"go.uber.org/zap"
)

const (
	// FileMode is the permission mode of a key file.
	FileMode = 0o600

	// DirMode keeps the directory that holds a bloc's vault data and keys
	// private to the user.
	DirMode = 0o700

	// TimestampFormat stamps the names of what is kept or moved aside.
	TimestampFormat = "20060102-150405"

	// TokenCopyInfix names the copy of safe's root token that is kept beside
	// root.key when root.key holds something else: root.key.saferc-<timestamp>.
	TokenCopyInfix = ".saferc-"

	// KeepNameAttempts bounds the -N suffixes tried for a free name.
	KeepNameAttempts = 100

	// rootTokenMinLength and rootTokenMaxLength bound a root token. Vault and
	// OpenBao tokens run from about two dozen characters to a little under a
	// hundred, so these bounds reject only what cannot be a token.
	rootTokenMinLength = 8
	rootTokenMaxLength = 512

	// tokenChars are the characters a Vault or OpenBao token is made of.
	tokenChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-"
)

var (
	// ErrUnreadable reports a key file that exists but cannot be read, such
	// as one left owned by root. It says nothing about whether the key is
	// good, so nothing may be stopped, moved, or replaced because of it.
	ErrUnreadable = errors.New("an inception vault key file cannot be read")

	// ErrRootTokenMalformed reports a root token holding characters no
	// engine issues, or of a length no token has.
	ErrRootTokenMalformed = errors.New("the inception vault root token is malformed")

	// ErrExists reports a path that is already taken.
	ErrExists = errors.New("vault archive already exists")

	// ErrNoFreeKeepName reports that every name tried for keeping a copy of a
	// root token, or an older vault log, was already taken.
	ErrNoFreeKeepName = errors.New("no free name left to keep an inception vault file")
)

// ReadBytes returns exactly what a key file holds. A file that does not
// exist, or that is empty, holds nothing, and an empty file counts as empty
// even when its mode would keep it from being read. A file that exists with
// content but cannot be read is ErrUnreadable, because it may hold the only
// copy of its key: treating it as missing would archive the vault or write a
// recovered key over it.
func ReadBytes(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, UnreadableError(path, err)
	}

	if info.Mode().IsRegular() && info.Size() == 0 {
		return nil, nil
	}

	data, err := os.ReadFile(path) // #nosec G304 -- path is the bloc's own key file from getVaultInceptionPaths()
	if err != nil {
		return nil, UnreadableError(path, err)
	}

	return data, nil
}

// Read returns what a key file holds with surrounding whitespace trimmed, or
// "" when it is missing or blank.
func Read(path string) (string, error) {
	data, err := ReadBytes(path)
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(data)), nil
}

// UnreadableError names the key file and why it could not be read, never
// what it holds.
func UnreadableError(path string, err error) error {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		err = pathErr.Err
	}

	return fmt.Errorf("%w: %s: %w; make it readable by this user and run this again",
		ErrUnreadable, path, err)
}

// CheckRootToken reports why token cannot be a root token. The error
// describes the token's shape and never its value.
func CheckRootToken(token string) error {
	if len(token) < rootTokenMinLength || len(token) > rootTokenMaxLength {
		return fmt.Errorf("%w: it has %d characters, outside %d to %d",
			ErrRootTokenMalformed, len(token), rootTokenMinLength, rootTokenMaxLength)
	}

	if strings.IndexFunc(token, notTokenChar) != -1 {
		return fmt.Errorf("%w: it holds characters other than letters, digits, '.', '_', and '-'", ErrRootTokenMalformed)
	}

	return nil
}

func notTokenChar(r rune) bool {
	return !strings.ContainsRune(tokenChars, r)
}

// syncFile flushes a written file's data and mode to disk, and syncDir
// flushes a directory's entries. Tests replace them to check the order and
// how a failure is handled.
var (
	syncFile = (*os.File).Sync //nolint:gochecknoglobals // tests replace the sync to observe it
	syncDir  = syncDirectory   //nolint:gochecknoglobals // tests replace the sync to observe it
)

// WriteRecovered writes one key file with mode 0600, through a temporary
// file and a rename, so neither a crash nor a power loss leaves a partial or
// empty key behind.
func WriteRecovered(path, value string) error {
	return writeFile(path, value, true)
}

// WriteNew writes a key file that must not exist yet, the same way
// WriteRecovered does, and fails with ErrExists rather than replace anything
// already at path.
func WriteNew(path, value string) error {
	return writeFile(path, value, false)
}

// writeFile writes value and a newline to path with mode 0600, through a
// temporary file in the same directory. With replace it renames the
// temporary file over path. Without it, it hard-links the temporary file to
// path, which fails when path exists, so nothing there is ever replaced.
//
// A rename can reach the disk before the data it points at, so a power loss
// soon after it could leave the only copy of a key empty. The temporary file
// is therefore synced before it takes its name, and the directory is synced
// after the name changes. A failure before the rename or link leaves path as
// it was and removes the temporary file. A directory sync that fails after
// it leaves the new key at path, and the error says so.
func writeFile(path, value string, replace bool) error {
	dir := filepath.Dir(path)

	err := os.MkdirAll(dir, DirMode)
	if err != nil {
		return fmt.Errorf("failed to create %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("failed to write %s: %w", path, err)
	}

	tmpName := tmp.Name()

	_, err = tmp.WriteString(value + "\n")
	if err == nil {
		err = tmp.Chmod(FileMode)
	}

	if err == nil {
		err = syncFile(tmp)
	}

	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}

	if err == nil {
		err = placeFile(tmpName, path, replace)
	}

	if err != nil {
		_ = os.Remove(tmpName) // the temporary file holds only this write's copy of the key

		return fmt.Errorf("failed to write %s: %w", path, err)
	}

	if !replace {
		err = os.Remove(tmpName)
		if err != nil {
			return fmt.Errorf("wrote %s, but could not remove its temporary copy %s: %w", path, tmpName, err)
		}
	}

	err = syncDir(dir)
	if err != nil {
		return fmt.Errorf("wrote %s, but could not flush its directory %s to disk: %w", path, dir, err)
	}

	return nil
}

// placeFile gives the synced temporary file the key file's name, by a
// rename that replaces path, or by a hard link that refuses an existing path
// and leaves the temporary name for the caller to remove.
func placeFile(tmpName, path string, replace bool) error {
	if replace {
		return os.Rename(tmpName, path)
	}

	err := os.Link(tmpName, path)
	if errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("%w: %s", ErrExists, path)
	}

	return err
}

// syncDirectory flushes dir's entries to disk, so a rename or link in it
// survives a power loss. A filesystem that cannot sync a directory at all
// reports EINVAL or ENOTSUP, and then there is nothing more to do.
func syncDirectory(dir string) error {
	d, err := os.Open(dir) // #nosec G304 -- the directory of the bloc's own key file
	if err != nil {
		return err
	}

	err = d.Sync()
	closeErr := d.Close()

	if err != nil && !dirSyncUnsupported(err) {
		return err
	}

	return closeErr
}

// dirSyncUnsupported reports a directory sync that the filesystem does not
// support, as opposed to one that failed.
func dirSyncUnsupported(err error) bool {
	return errors.Is(err, syscall.EINVAL) || errors.Is(err, errors.ErrUnsupported)
}

// TargetToken returns the token safe holds for the named target, or "" unless
// that target exists and points at the bloc's port.
func TargetToken(vaultName, port string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	data, err := os.ReadFile(filepath.Join(home, ".saferc")) // #nosec G304 -- safe's own config under the user's home
	if err != nil {
		return ""
	}

	var safeRC struct {
		Vaults map[string]struct {
			URL   string `yaml:"url"`
			Token string `yaml:"token"`
		} `yaml:"vaults"`
	}

	if yaml.Unmarshal(data, &safeRC) != nil {
		return ""
	}

	target, ok := safeRC.Vaults[vaultName]
	if !ok || strings.TrimRight(target.URL, "/") != "http://127.0.0.1:"+port {
		return ""
	}

	return strings.TrimSpace(target.Token)
}

// PreserveTargetToken makes sure the root token safe holds for the bloc's
// target is on disk before anything stops the vault. Stopping deletes that
// target, and with it what may be the only copy of the token.
//
// When root.key is missing or blank and the token has the shape of a token,
// the token is written to root.key. When root.key already holds the token,
// nothing changes. Otherwise root.key holds a different value, or the token
// is not token-shaped, and both are kept: the token goes to a copy beside
// root.key, root.key.saferc-<timestamp>, which an archive of <bloc>/vault
// carries along. An identical copy kept by an earlier run is reused rather
// than kept twice.
//
// It returns the file that now holds the token, or "" when safe has none.
// A root.key that cannot be read is an error, and nothing is written then.
func PreserveTargetToken(rootKeyFile, vaultName, token string, now time.Time, log *zap.SugaredLogger) (string, error) {
	if token == "" {
		return "", nil
	}

	held, err := Read(rootKeyFile)
	if err != nil {
		return "", err
	}

	if held == token {
		return rootKeyFile, nil
	}

	if held == "" && CheckRootToken(token) == nil {
		err = WriteRecovered(rootKeyFile, token)
		if err != nil {
			return "", err
		}

		log.Infow("Saved the root token from safe's target before stopping the vault", "path", rootKeyFile,
			"target", vaultName)

		return rootKeyFile, nil
	}

	return keepTokenCopy(rootKeyFile, vaultName, token, now, log)
}

// keepTokenCopy keeps the target's token beside root.key, reusing a copy an
// earlier run kept when it holds the same token.
func keepTokenCopy(rootKeyFile, vaultName, token string, now time.Time, log *zap.SugaredLogger) (string, error) {
	existing, err := keptTokenCopies(rootKeyFile)
	if err != nil {
		return "", err
	}

	for _, kept := range existing {
		// A copy that cannot be read cannot be compared, so a new copy is
		// kept beside it rather than trusting it.
		value, readErr := Read(kept)
		if readErr == nil && value == token {
			return kept, nil
		}
	}

	kept, err := WriteUnderFreeName(rootKeyFile+TokenCopyInfix+now.Format(TimestampFormat), token)
	if err != nil {
		return "", err
	}

	log.Warnw("Kept the root token from safe's target beside root.key, which holds a different value",
		"kept", kept, "root_key", rootKeyFile, "target", vaultName)

	return kept, nil
}

// keptTokenCopies lists the copies of safe's token kept beside root.key.
func keptTokenCopies(rootKeyFile string) ([]string, error) {
	dir := filepath.Dir(rootKeyFile)

	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("failed to list %s: %w", dir, err)
	}

	prefix := filepath.Base(rootKeyFile) + TokenCopyInfix

	var kept []string

	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.HasPrefix(entry.Name(), prefix) {
			kept = append(kept, filepath.Join(dir, entry.Name()))
		}
	}

	return kept, nil
}

// WriteUnderFreeName writes value to base, or to base with the first free -N
// appended, and never replaces an existing file.
func WriteUnderFreeName(base, value string) (string, error) {
	candidate := base

	for n := 2; n <= KeepNameAttempts; n++ {
		err := WriteNew(candidate, value)
		if err == nil {
			return candidate, nil
		}

		if !errors.Is(err, ErrExists) {
			return "", err
		}

		candidate = base + "-" + strconv.Itoa(n)
	}

	return "", fmt.Errorf("%w: %s", ErrNoFreeKeepName, base)
}
