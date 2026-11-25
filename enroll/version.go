package enroll

import (
	"regexp"
	"strconv"
	"strings"
)

// versionForm is a semantic version, MAJOR.MINOR.PATCH with an optional
// pre-release, and no leading v or build metadata.
var versionForm = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)

// maxVersion bounds a version's length.
const maxVersion = 64

// ValidVersion reports whether v is a semantic version the manifest can
// carry.
func ValidVersion(v string) bool { return len(v) <= maxVersion && versionForm.MatchString(v) }

// CompareVersions orders two valid versions by semantic version
// precedence, and returns -1, 0, or 1. A pre-release sorts below its
// release. It orders an invalid version below every valid one, so an
// invalid version never raises a floor.
func CompareVersions(a, b string) int {
	va, vb := ValidVersion(a), ValidVersion(b)

	switch {
	case !va && !vb:
		return 0
	case !va:
		return -1
	case !vb:
		return 1
	}

	ca, pa, _ := strings.Cut(a, "-")
	cb, pb, _ := strings.Cut(b, "-")

	if c := compareDotted(ca, cb, true); c != 0 {
		return c
	}

	switch {
	case pa == pb:
		return 0
	case pa == "":
		return 1
	case pb == "":
		return -1
	}

	return compareDotted(pa, pb, false)
}

// compareDotted compares dot-separated identifiers. Numeric identifiers
// compare as numbers and sort below alphanumeric ones, and a shorter
// list that is a prefix of a longer one sorts first.
func compareDotted(a, b string, numeric bool) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")

	for i := 0; i < len(as) && i < len(bs); i++ {
		if c := compareIdent(as[i], bs[i], numeric); c != 0 {
			return c
		}
	}

	switch {
	case len(as) < len(bs):
		return -1
	case len(as) > len(bs):
		return 1
	}

	return 0
}

func compareIdent(a, b string, numeric bool) int {
	na, ea := strconv.ParseUint(a, 10, 64)
	nb, eb := strconv.ParseUint(b, 10, 64)

	switch {
	case ea == nil && eb == nil:
		switch {
		case na < nb:
			return -1
		case na > nb:
			return 1
		}

		return 0
	case numeric:
		// A version core too large for uint64 compares by length, then
		// by text, which orders digit strings with no leading zeros.
		if len(a) != len(b) {
			if len(a) < len(b) {
				return -1
			}

			return 1
		}
	case ea == nil:
		return -1
	case eb == nil:
		return 1
	}

	return strings.Compare(a, b)
}

// MaxVersion returns the highest of the valid versions given, or the
// empty string when none is valid.
func MaxVersion(vs ...string) string {
	best := ""

	for _, v := range vs {
		if ValidVersion(v) && (best == "" || CompareVersions(v, best) > 0) {
			best = v
		}
	}

	return best
}
