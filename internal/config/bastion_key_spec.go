package config

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// BastionKeySource says where a bastion.keys entry's public keys come from.
type BastionKeySource string

const (
	// BastionKeyGitHub fetches the user's keys from github.com/<user>.keys.
	BastionKeyGitHub BastionKeySource = "github"
	// BastionKeyGitLab fetches the user's keys from gitlab.com/<user>.keys.
	BastionKeyGitLab BastionKeySource = "gitlab"
	// BastionKeyLiteral is an OpenSSH public key written into the config.
	BastionKeyLiteral BastionKeySource = "key"
)

// BastionKeySpec is a parsed bastion.keys entry. Value is the username for
// GitHub and GitLab, and the whole public key for a literal key.
type BastionKeySpec struct {
	Source BastionKeySource
	Value  string
}

// ErrBastionKeySpecInvalid marks a bastion.keys entry that names no
// recognised key source.
var ErrBastionKeySpecInvalid = errors.New(
	"want github/<user>, gitlab/<user>, github.com/<user>, gitlab.com/<user>, or an OpenSSH public key")

// bastionKeyUserPattern bounds the username part of a GitHub or GitLab
// entry, which ends up in a URL. GitLab allows dots and underscores, so both
// are accepted, but not as the first character.
var bastionKeyUserPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)

// bastionKeyErrPreviewLen caps how much of a bad entry an error repeats.
const bastionKeyErrPreviewLen = 40

// ValidSSHPublicKeyPrefixes returns the OpenSSH public key types a literal
// bastion.keys entry may start with.
func ValidSSHPublicKeyPrefixes() []string {
	return []string{
		"ssh-rsa",
		"ssh-ed25519",
		"ecdsa-sha2-nistp256",
		"ecdsa-sha2-nistp384",
		"ecdsa-sha2-nistp521",
	}
}

// ParseBastionKeySpec reads one bastion.keys value. It accepts github/<user>
// and gitlab/<user>, the host forms github.com/<user> and gitlab.com/<user>
// with an optional https:// in front, an optional trailing .keys on any of
// them, and a literal OpenSSH public key. Anything else is an error wrapping
// ErrBastionKeySpecInvalid, so no entry is ever dropped quietly.
func ParseBastionKeySpec(spec string) (BastionKeySpec, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return BastionKeySpec{}, fmt.Errorf("empty entry: %w", ErrBastionKeySpecInvalid)
	}

	for _, prefix := range ValidSSHPublicKeyPrefixes() {
		if strings.HasPrefix(spec, prefix+" ") {
			return BastionKeySpec{Source: BastionKeyLiteral, Value: spec}, nil
		}
	}

	rest, hasScheme := strings.CutPrefix(spec, "https://")

	forms := []struct {
		prefix string
		source BastionKeySource
		bare   bool
	}{
		{"github.com/", BastionKeyGitHub, false},
		{"gitlab.com/", BastionKeyGitLab, false},
		{"github/", BastionKeyGitHub, true},
		{"gitlab/", BastionKeyGitLab, true},
	}

	for _, form := range forms {
		user, found := strings.CutPrefix(rest, form.prefix)
		if !found || (form.bare && hasScheme) {
			continue
		}

		user = strings.TrimSuffix(user, ".keys")
		if !bastionKeyUserPattern.MatchString(user) {
			return BastionKeySpec{}, fmt.Errorf("bad %s username %q: %w", form.source, user, ErrBastionKeySpecInvalid)
		}

		return BastionKeySpec{Source: form.source, Value: user}, nil
	}

	return BastionKeySpec{}, ErrBastionKeySpecInvalid
}

// validateBastionKeys parses every bastion.keys entry, in label order, and
// fails on the first one it cannot read.
func validateBastionKeys(keys map[string]string) error {
	labels := make([]string, 0, len(keys))
	for label := range keys {
		labels = append(labels, label)
	}

	sort.Strings(labels)

	for _, label := range labels {
		_, err := ParseBastionKeySpec(keys[label])
		if err != nil {
			return BastionKeyEntryError(label, keys[label], err)
		}
	}

	return nil
}

// BastionKeyEntryError names a bastion.keys entry that failed to parse. It
// repeats only the first line of the value, cut short, so a private key
// pasted there by mistake is never echoed.
func BastionKeyEntryError(label, value string, err error) error {
	preview, _, _ := strings.Cut(strings.TrimSpace(value), "\n")
	if len(preview) > bastionKeyErrPreviewLen {
		preview = preview[:bastionKeyErrPreviewLen] + "..."
	}

	return fmt.Errorf("bastion config: keys.%s %q: %w", label, preview, err)
}
