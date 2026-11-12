package ssh

import (
	"os"
	"strings"
)

// BastionHostKeyOptions returns the ssh -o options for a connection to the
// bastion itself, whether it is the destination or the jump host. They check
// its host key the way the Go client does: accept-new records a host seen for
// the first time and refuses one whose key differs from the entry on record,
// and naming the file keeps system ssh on the same known_hosts the Go client
// reads and writes, whatever the user's ssh config says. When the home
// directory cannot be found the file is left to ssh's default, and the check
// stays strict.
//
// ssh keeps the first value it sees for an option, so callers put these
// before any options the operator supplies.
func BastionHostKeyOptions() []string {
	options := []string{"-o", "StrictHostKeyChecking=accept-new"}

	knownHostsPath, err := KnownHostsFile()
	if err == nil {
		options = append(options, "-o", "UserKnownHostsFile="+sshConfigPath(knownHostsPath))
	}

	return options
}

// InternalHostKeyOptions returns the ssh -o options for a host behind the
// bastion, such as a BOSH VM or the artifacts VM. Those hosts are rebuilt
// often and their keys churn, so they are reached without a host key check
// and nothing is recorded for them. Only the bastion hop is checked.
func InternalHostKeyOptions() []string {
	return []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=" + os.DevNull,
	}
}

// ShellJoin joins args into one command line for a context that splits
// words the way a POSIX shell does, such as an ssh ProxyCommand or the
// command rsync is given with -e. An argument holding anything a shell
// would split or expand is single-quoted.
func ShellJoin(args []string) string {
	quoted := make([]string, 0, len(args))

	for _, arg := range args {
		quoted = append(quoted, shellQuote(arg))
	}

	return strings.Join(quoted, " ")
}

// shellQuote single-quotes s unless every character in it is one a shell
// passes through as it is.
func shellQuote(s string) string {
	if s != "" && strings.Trim(s, shellSafeCharacters) == "" {
		return s
	}

	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shellSafeCharacters are the characters a POSIX shell neither splits on
// nor expands anywhere in a word.
const shellSafeCharacters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_./:=@%+,"
