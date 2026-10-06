package commands

import (
	"errors"
	"fmt"
	"strings"
)

// sealKeyHexLength is the length of the unseal key safe local prints. safe
// initializes its vault with a single key share, which is the 32-byte key
// itself, and prints it hex-encoded.
const sealKeyHexLength = 64

// rootTokenMinLength and rootTokenMaxLength bound a root token. Vault and
// OpenBao tokens run from about two dozen characters to a little under a
// hundred, so these bounds reject only what cannot be a token.
const (
	rootTokenMinLength = 8
	rootTokenMaxLength = 512
)

var (
	// ErrSealKeyMalformed reports an unseal key that is not the 64
	// hexadecimal digits safe prints, such as one cut short at a tmux
	// pane's edge.
	ErrSealKeyMalformed = errors.New("the inception vault unseal key is malformed")
	// ErrRootTokenMalformed reports a root token holding characters no
	// engine issues, or of a length no token has.
	ErrRootTokenMalformed = errors.New("the inception vault root token is malformed")
	// ErrInceptionKeysNotSaved reports a new inception vault whose root
	// token and unseal key were not both saved in a valid shape.
	ErrInceptionKeysNotSaved = errors.New("the new inception vault's keys were not saved")
	// ErrUnsealKeyFileMalformed reports an unseal key file that holds
	// something other than a whole key, whose whole key could not be found
	// in what safe printed.
	ErrUnsealKeyFileMalformed = errors.New("the inception vault unseal key file does not hold a whole key")
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

// checkRootToken reports why token cannot be a root token. The error
// describes the token's shape and never its value.
func checkRootToken(token string) error {
	if len(token) < rootTokenMinLength || len(token) > rootTokenMaxLength {
		return fmt.Errorf("%w: it has %d characters, outside %d to %d",
			ErrRootTokenMalformed, len(token), rootTokenMinLength, rootTokenMaxLength)
	}

	if strings.IndexFunc(token, notTokenChar) != -1 {
		return fmt.Errorf("%w: it holds characters other than letters, digits, '.', '_', and '-'", ErrRootTokenMalformed)
	}

	return nil
}

// tokenChars are the characters a Vault or OpenBao token is made of.
const tokenChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-"

func notTokenChar(r rune) bool {
	return !strings.ContainsRune(tokenChars, r)
}
