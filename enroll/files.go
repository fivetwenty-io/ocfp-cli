package enroll

import (
	"errors"
	"strings"
)

// MaxSmallFile bounds every read under the file rules except the
// worker binary's, which the manifest's size bounds.
const MaxSmallFile = 64 << 10

// ErrFileRule is the error every refusal under the file rules wraps.
// Each refusal names the path, so the log line that reports it does.
var ErrFileRule = errors.New("enroll: the file breaks the file rules")

// The file rules guard the root steps against a directory that an
// unprivileged user can write. A root step opens every path under such
// a directory without following a link at any component, accepts only
// a regular file, and bounds each read. It never renames, chowns, or
// chmods a file it found there. Where it places a file, it creates a
// fresh one with O_CREAT|O_EXCL in a directory it owns, writes the
// bytes it decided on, and sets the owner and the mode on its own
// descriptor. A file that breaks a rule is left in place.

// splitRelative splits a relative path into its components, refusing an
// absolute path, an empty component, and a dot or dot-dot.
func splitRelative(name string) ([]string, bool) {
	if name == "" || strings.HasPrefix(name, "/") {
		return nil, false
	}

	parts := strings.Split(name, "/")
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return nil, false
		}
	}

	return parts, true
}
