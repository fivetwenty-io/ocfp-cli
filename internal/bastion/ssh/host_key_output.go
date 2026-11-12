package ssh

import (
	"fmt"
	"net"
	"regexp"
	"strings"
)

// The lines OpenSSH prints, even at LogLevel=ERROR, when it refuses a host
// key itself. Each names the host as known_hosts files it: "host" on port
// 22, otherwise "[host]:port", or the HostKeyAlias when one is set.
var (
	sshChangedKeyLine   = regexp.MustCompile(`^Host key for (\S+) has changed and you have requested strict checking\.$`)
	sshRevokedKeyLine   = regexp.MustCompile(`^\S+ host key for (\S+) was revoked and you have requested strict checking\.$`)
	sshOffendingKeyLine = regexp.MustCompile(`^Offending \S+ key in (.+):\d+$`)
)

// SystemSSHHostKeyError reports that system ssh refused the bastion because
// its host key differs from the one known_hosts records for it, or is
// marked as revoked. errors.Is matches it against ErrHostKeyMismatch, as
// the Go client's refusals do. It is terminal: a caller that sees it must
// not retry.
type SystemSSHHostKeyError struct {
	// Host is the name ssh printed for the host, which is the name
	// known_hosts files the entry under.
	Host string
	// KnownHostsPath is the file holding the refused entry, or empty when
	// neither ssh's output nor the home directory names it.
	KnownHostsPath string
	// Revoked is true when known_hosts marks the presented key as revoked
	// rather than recording a different key.
	Revoked bool
	// Err is the error ssh exited with, if any.
	Err error
}

// Error explains the refusal and how to clear the entry after a rebuild.
func (e *SystemSSHHostKeyError) Error() string {
	remove := "ssh-keygen -R " + shellWord(e.Host)
	if e.KnownHostsPath != "" {
		remove += " -f " + shellWord(e.KnownHostsPath)
	}

	presented := "a host key that differs from the one on record"
	if e.Revoked {
		presented = "a host key that known_hosts marks as revoked"
	}

	return fmt.Sprintf(
		"host key verification failed: %s presented %s, "+
			"so ssh refused to connect. Someone may be intercepting the connection. If the bastion was "+
			"rebuilt on purpose, remove the stale entry with `%s` and connect again",
		e.Host, presented, remove)
}

// Is reports whether target is ErrHostKeyMismatch.
func (e *SystemSSHHostKeyError) Is(target error) bool {
	return target == ErrHostKeyMismatch
}

// Unwrap returns the error ssh exited with.
func (e *SystemSSHHostKeyError) Unwrap() error {
	return e.Err
}

// SystemSSHHostKeyFailure returns a *SystemSSHHostKeyError when output, the
// captured stderr of a system ssh, scp, or rsync run, shows that the local
// ssh refused the bastion's host key. bastionHost names the bastion as ssh
// dialled it, and cause is the error the command exited with. It returns
// nil when the output does not prove such a refusal.
//
// The banner and "Host key verification failed." prove nothing on their
// own, because a command the bastion runs, such as an ssh to another host,
// prints them too and its exit status comes back as ssh's. The proof is
// ssh's line saying the key for a host has changed or was revoked, where
// that host is the bastion, or is a name rather than an address and the
// refused entry sits in our own known_hosts, as with a HostKeyAlias. The
// hint names the host the way that line spells it.
func SystemSSHHostKeyFailure(output, bastionHost string, cause error) error {
	if bastionHost == "" {
		return nil
	}

	ourKnownHosts, err := KnownHostsFile()
	if err != nil {
		ourKnownHosts = ""
	}

	var (
		host, offendingFile string
		revoked, inOurs     bool
	)

	for line := range strings.Lines(output) {
		line = strings.TrimRight(line, "\r\n")

		if match := sshOffendingKeyLine.FindStringSubmatch(line); match != nil {
			if offendingFile == "" {
				offendingFile = match[1]
			}

			inOurs = inOurs || (ourKnownHosts != "" && match[1] == ourKnownHosts)

			continue
		}

		if match := sshChangedKeyLine.FindStringSubmatch(line); match != nil && host == "" {
			host = match[1]
		} else if match := sshRevokedKeyLine.FindStringSubmatch(line); match != nil && host == "" {
			host, revoked = match[1], true
		}
	}

	if host == "" {
		return nil
	}

	isBastion := sameKnownHostsHost(host, bastionHost)
	isAlias := inOurs && net.ParseIP(knownHostsHostName(host)) == nil

	if !isBastion && !isAlias {
		return nil
	}

	knownHostsPath := ourKnownHosts
	if !inOurs && offendingFile != "" {
		knownHostsPath = offendingFile
	}

	return &SystemSSHHostKeyError{Host: host, KnownHostsPath: knownHostsPath, Revoked: revoked, Err: cause}
}

// knownHostsHostName returns the host in a known_hosts name, dropping the
// brackets and port of "[host]:port", in lower case.
func knownHostsHostName(name string) string {
	name = strings.TrimSpace(name)
	if strings.HasPrefix(name, "[") {
		if end := strings.Index(name, "]"); end > 0 {
			name = name[1:end]
		}
	}

	return strings.ToLower(strings.TrimSuffix(name, "."))
}

// sameKnownHostsHost reports whether a known_hosts name and a host as
// dialled name the same host, comparing addresses as IPs so that different
// spellings of one IPv6 address match.
func sameKnownHostsHost(name, dialled string) bool {
	a, b := knownHostsHostName(name), knownHostsHostName(dialled)

	ipA, ipB := net.ParseIP(a), net.ParseIP(b)
	if ipA != nil && ipB != nil {
		return ipA.Equal(ipB)
	}

	return a != "" && a == b
}
