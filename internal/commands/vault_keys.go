package commands

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/keyfile"
)

// sealKeyHexLength is the length of the unseal key safe local prints. safe
// initializes its vault with a single key share, which is the 32-byte key
// itself, and prints it hex-encoded.
const sealKeyHexLength = 64

var (
	// ErrSealKeyMalformed reports an unseal key that is not the 64
	// hexadecimal digits safe prints, such as one cut short at a tmux
	// pane's edge.
	ErrSealKeyMalformed = errors.New("the inception vault unseal key is malformed")
	// ErrRootTokenMalformed reports a root token holding characters no
	// engine issues, or of a length no token has.
	ErrRootTokenMalformed = keyfile.ErrRootTokenMalformed
	// ErrInceptionKeysNotSaved reports a running inception vault, new or
	// not, whose root token and unseal key are not both saved in a valid
	// shape, so it could not be reopened once it stopped.
	ErrInceptionKeysNotSaved = errors.New("the inception vault's keys were not saved")
	// ErrUnsealKeyFileMalformed reports an unseal key file that holds
	// something other than a whole key, whose whole key could not be found
	// in what safe printed.
	ErrUnsealKeyFileMalformed = errors.New("the inception vault unseal key file does not hold a whole key")
	// ErrInceptionKeyFileUnreadable reports a key file that exists but
	// cannot be read, such as one left owned by root. It says nothing about
	// whether the key is good, so nothing may be stopped, moved, or
	// replaced because of it.
	ErrInceptionKeyFileUnreadable = keyfile.ErrUnreadable
)

// checkSealKey reports why key cannot be the unseal key safe printed. The
// error describes the key's shape and never its value.
func checkSealKey(key string) error {
	if len(key) != sealKeyHexLength {
		return fmt.Errorf("%w: it has %d characters, not %d", ErrSealKeyMalformed, len(key), sealKeyHexLength)
	}

	if strings.IndexFunc(key, notHexDigit) != -1 {
		return fmt.Errorf("%w: it holds characters that are not hexadecimal digits", ErrSealKeyMalformed)
	}

	return nil
}

func notHexDigit(r rune) bool {
	return !strings.ContainsRune("0123456789abcdefABCDEF", r)
}
