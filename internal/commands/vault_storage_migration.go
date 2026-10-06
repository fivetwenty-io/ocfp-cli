package commands

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// safeLocalRaftNodeID is the raft node ID safe gives the single node of a
// `safe local --raft` vault, raftNodeID in safe's internal/cli/local_config.go.
// A store that `operator migrate` writes records the node ID it was given,
// and safe starts the node under its own constant, so the two must be equal
// or the restarted node will not find itself in its own configuration.
const safeLocalRaftNodeID = "safe-local"

// ErrMigrationPathUnsafe reports a path that cannot be written into the
// migrate config without changing its meaning.
var ErrMigrationPathUnsafe = errors.New("path cannot be written safely into the migrate config")

// renderRaftMigrationConfig renders the config `operator migrate` reads to
// copy the file storage at source into a new raft store at dest. The cluster
// address uses the bloc's cluster port, so the raft configuration records the
// address the restarted vault will listen on.
func renderRaftMigrationConfig(source, dest, clusterPort string) (string, error) {
	for _, path := range []string{source, dest} {
		err := checkMigrationPath(path)
		if err != nil {
			return "", err
		}
	}

	var b strings.Builder

	fmt.Fprintf(&b, "storage_source \"file\" {\n  path = %s\n}\n\n", strconv.Quote(source))
	fmt.Fprintf(&b, "storage_destination \"raft\" {\n  path    = %s\n  node_id = %s\n}\n\n",
		strconv.Quote(dest), strconv.Quote(safeLocalRaftNodeID))
	fmt.Fprintf(&b, "cluster_addr = %s\n", strconv.Quote("https://127.0.0.1:"+clusterPort))

	return b.String(), nil
}

// checkMigrationPath refuses a path the engines' HCL parsers could read as
// something else. HCL treats ${ and %{ inside a string as the start of a
// template, and a control character or invalid UTF-8 has no spelling every
// parser reads back the same way. A quote or backslash is fine, because
// strconv.Quote escapes both.
func checkMigrationPath(path string) error {
	if strings.Contains(path, "${") || strings.Contains(path, "%{") {
		return fmt.Errorf("%w: %q holds a template sequence", ErrMigrationPathUnsafe, path)
	}

	if !utf8.ValidString(path) || strings.ContainsFunc(path, unicode.IsControl) {
		return fmt.Errorf("%w: %q holds a control character or invalid UTF-8", ErrMigrationPathUnsafe, path)
	}

	return nil
}
