package ssh

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh/knownhosts"
)

// systemSSHHostKeyMarkers are the lines OpenSSH prints when it refuses a
// host key: the banner for a key that differs from the one on record, and
// the fatal line that follows any refusal, a revoked key included.
var systemSSHHostKeyMarkers = []string{ //nolint:gochecknoglobals // fixed list of OpenSSH messages
	"REMOTE HOST IDENTIFICATION HAS CHANGED",
	"Host key verification failed",
}

// SystemSSHHostKeyError reports that system ssh refused the bastion because
// its host key differs from the one known_hosts records for it. errors.Is
// matches it against ErrHostKeyMismatch, as the Go client's refusals do. It
// is terminal: a caller that sees it must not retry.
type SystemSSHHostKeyError struct {
	// Host is the name known_hosts files the entry under: "host" on port
	// 22, otherwise "[host]:port".
	Host string
	// KnownHostsPath is the file ssh checked, or empty when the home
	// directory could not be found.
	KnownHostsPath string
	// Err is the error ssh exited with, if any.
	Err error
}

// Error explains the refusal and how to clear the entry after a rebuild.
func (e *SystemSSHHostKeyError) Error() string {
	remove := "ssh-keygen -R " + shellWord(e.Host)
	if e.KnownHostsPath != "" {
		remove += " -f " + shellWord(e.KnownHostsPath)
	}

	return fmt.Sprintf(
		"host key verification failed: %s presented a host key that differs from the one on record, "+
			"so ssh refused to connect. Someone may be intercepting the connection. If the bastion was "+
			"rebuilt on purpose, remove the stale entry with `%s` and connect again",
		e.Host, remove)
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
// captured stderr of a system ssh, scp, or rsync run, shows that ssh refused
// the bastion's host key. host and port name the bastion as ssh dialled it,
// and cause is the error the command exited with. It returns nil when the
// output shows no host key refusal.
//
// Only the bastion is checked strictly. Hosts behind it are reached with
// InternalHostKeyOptions, which never refuse a key, so a refusal in the
// output is always the bastion's.
func SystemSSHHostKeyFailure(output, host string, port int, cause error) error {
	refused := false

	for _, marker := range systemSSHHostKeyMarkers {
		if strings.Contains(output, marker) {
			refused = true

			break
		}
	}

	if !refused {
		return nil
	}

	knownHostsPath, err := KnownHostsFile()
	if err != nil {
		knownHostsPath = ""
	}

	return &SystemSSHHostKeyError{
		Host:           knownhosts.Normalize(net.JoinHostPort(host, strconv.Itoa(port))),
		KnownHostsPath: knownHostsPath,
		Err:            cause,
	}
}
