package config

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	gossh "golang.org/x/crypto/ssh"
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
// are accepted, but not as the first character. Nothing that could leave
// the user's path, such as a slash, a query, a fragment, a percent escape,
// or whitespace, matches; a run of dots is refused separately.
var bastionKeyUserPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)

// bastionKeyTypeDSA is the OpenSSH DSA key type, which current OpenSSH
// releases no longer accept.
const bastionKeyTypeDSA = "ssh-dss"

// bastionKeyErrPreviewLen caps how much of a bad entry an error repeats.
const bastionKeyErrPreviewLen = 40

// ParseBastionKeySpec reads one bastion.keys value. It accepts github/<user>
// and gitlab/<user>, which keep the username exactly as written, the host
// forms github.com/<user> and gitlab.com/<user> with an optional https:// in
// front and an optional trailing .keys, and a literal OpenSSH public key of
// any type ssh.ParseAuthorizedKey reads, security keys included. Anything
// else is an error wrapping ErrBastionKeySpecInvalid, so no entry is ever
// dropped quietly.
func ParseBastionKeySpec(spec string) (BastionKeySpec, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return BastionKeySpec{}, fmt.Errorf("empty entry: %w", ErrBastionKeySpecInvalid)
	}

	// No username or host form holds whitespace, so an entry that does is
	// a literal key or nothing.
	if strings.ContainsAny(spec, " \t\r\n") {
		err := checkLiteralBastionKey(spec)
		if err != nil {
			return BastionKeySpec{}, err
		}

		return BastionKeySpec{Source: BastionKeyLiteral, Value: spec}, nil
	}

	rest, hasScheme := strings.CutPrefix(spec, "https://")

	forms := []struct {
		prefix string
		source BastionKeySource
		short  bool
	}{
		{"github.com/", BastionKeyGitHub, false},
		{"gitlab.com/", BastionKeyGitLab, false},
		{"github/", BastionKeyGitHub, true},
		{"gitlab/", BastionKeyGitLab, true},
	}

	for _, form := range forms {
		user, found := strings.CutPrefix(rest, form.prefix)
		if !found || (form.short && hasScheme) {
			continue
		}

		// The short forms predate the host forms and have always named
		// the account verbatim, so gitlab/alice.keys is the account
		// alice.keys. Only the host forms read .keys as part of the URL.
		trimmed := user
		if !form.short {
			trimmed = strings.TrimSuffix(user, ".keys")
		}

		if strings.Contains(user, "..") || !bastionKeyUserPattern.MatchString(trimmed) {
			return BastionKeySpec{}, fmt.Errorf("bad %s username %q: %w", form.source, user, ErrBastionKeySpecInvalid)
		}

		return BastionKeySpec{Source: form.source, Value: trimmed}, nil
	}

	if !strings.Contains(spec, "/") {
		return BastionKeySpec{}, fmt.Errorf("names no key source: %w", ErrBastionKeySpecInvalid)
	}

	return BastionKeySpec{}, ErrBastionKeySpecInvalid
}

// checkLiteralBastionKey accepts one authorized_keys line holding a single
// OpenSSH public key with no options in front, such as a from= or command=
// restriction the entry was never meant to carry. DSA keys, which OpenSSH
// no longer accepts, and certificates, which authorized_keys does not take
// as keys, are refused too.
func checkLiteralBastionKey(spec string) error {
	errNotAKey := fmt.Errorf("not a valid OpenSSH public key: %w", ErrBastionKeySpecInvalid)

	if strings.ContainsAny(spec, "\r\n") {
		return errNotAKey
	}

	pub, _, options, rest, err := gossh.ParseAuthorizedKey([]byte(spec))
	if err != nil || len(options) > 0 || len(strings.TrimSpace(string(rest))) > 0 {
		return errNotAKey
	}

	if _, isCert := pub.(*gossh.Certificate); isCert || pub.Type() == bastionKeyTypeDSA {
		return fmt.Errorf("%s keys are not accepted: %w", pub.Type(), ErrBastionKeySpecInvalid)
	}

	return nil
}

// KeyWarnings returns one warning line for each keys entry that
// ParseBastionKeySpec cannot read, in label order, naming the bloc, the
// entry's label, and the reason. An unreadable entry does not stop the bloc
// from loading, so commands that only read the bloc keep working, but
// ResolveKeySpecs still refuses it before ocfp init bastion touches the
// bastion.
func (b *Bastion) KeyWarnings(bloc string) []string {
	labels := make([]string, 0, len(b.Keys))
	for label := range b.Keys {
		labels = append(labels, label)
	}

	sort.Strings(labels)

	var warnings []string

	for _, label := range labels {
		_, err := ParseBastionKeySpec(b.Keys[label])
		if err != nil {
			warnings = append(warnings, fmt.Sprintf(
				"WARNING: bloc %s: bastion.keys.%s %q cannot be read (%v); ocfp init bastion will refuse to install keys until it is fixed",
				bloc, label, bastionKeyPreview(b.Keys[label]), err))
		}
	}

	return warnings
}

// BastionKeyEntryError names a bastion.keys entry that failed to parse. It
// repeats only the first line of the value, cut short, so a private key
// pasted there by mistake is never echoed.
func BastionKeyEntryError(label, value string, err error) error {
	return fmt.Errorf("bastion config: keys.%s %q: %w", label, bastionKeyPreview(value), err)
}

// bastionKeyPreview returns the first line of a bastion.keys value, cut
// short, so a private key pasted there by mistake is never echoed.
func bastionKeyPreview(value string) string {
	preview, _, _ := strings.Cut(strings.TrimSpace(value), "\n")
	if len(preview) > bastionKeyErrPreviewLen {
		preview = preview[:bastionKeyErrPreviewLen] + "..."
	}

	return preview
}
