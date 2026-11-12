package commands

import "strings"

// transferBastionHost returns bastionIP when one of an scp or rsync run's
// paths is on the bastion, and "" otherwise. scp and rsync check every
// remote end's host key strictly, so a refusal from a copy that does not
// touch the bastion is another host's and must not be reported as the
// bastion's.
func transferBastionHost(bastionIP string, paths ...string) string {
	bastion := normalizeSSHHost(bastionIP)
	if bastion == "" {
		return ""
	}

	for _, path := range paths {
		if sameSSHHost(normalizeSSHHost(transferRemoteHost(path)), bastion) {
			return bastionIP
		}
	}

	return ""
}

// transferRemoteHost returns the host of a remote scp or rsync path, such
// as "[user@]host:path", "user@[fd00::10]:path", "host::module", or
// "scp://user@host:port/path", and "" for a local path. As scp and rsync
// do, a path with a slash before its first colon is local.
func transferRemoteHost(path string) string {
	if scheme, rest, found := strings.Cut(path, "://"); found && scheme != "" && !strings.ContainsAny(scheme, "/:@") {
		authority, _, _ := strings.Cut(rest, "/")
		if at := strings.LastIndex(authority, "@"); at >= 0 {
			authority = authority[at+1:]
		}

		return hostBeforePort(authority)
	}

	colon := firstUnbracketedColon(path)
	if colon < 0 || strings.Contains(path[:colon], "/") {
		return ""
	}

	host := path[:colon]
	if at := strings.LastIndex(host, "@"); at >= 0 {
		host = host[at+1:]
	}

	return strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
}

// hostBeforePort returns the host of "host", "host:port", "[v6]", or
// "[v6]:port".
func hostBeforePort(authority string) string {
	if strings.HasPrefix(authority, "[") {
		if end := strings.Index(authority, "]"); end > 0 {
			return authority[1:end]
		}
	}

	host, _, _ := strings.Cut(authority, ":")

	return host
}

// firstUnbracketedColon returns the index of the first colon outside
// square brackets, or -1.
func firstUnbracketedColon(path string) int {
	depth := 0

	for i, r := range path {
		switch r {
		case '[':
			depth++
		case ']':
			depth--
		case ':':
			if depth <= 0 {
				return i
			}
		}
	}

	return -1
}
