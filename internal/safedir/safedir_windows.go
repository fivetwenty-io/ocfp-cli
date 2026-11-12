package safedir

import "fmt"

// Ancestors refuses every path on Windows, which has no owner and mode bits
// to check. Until it can check access lists, a trusted path is Unix only.
func Ancestors(path string) error {
	return AncestorsAs(path, -1)
}

// AncestorsAs refuses every path on Windows. See Ancestors.
func AncestorsAs(path string, _ int) error {
	return fmt.Errorf("cannot check who may replace the parents of %s, because this check runs only on Unix", path)
}

// Resolve refuses every path on Windows. See Ancestors.
func Resolve(path string) (string, error) {
	return ResolveAs(path, -1)
}

// ResolveAs refuses every path on Windows. See Ancestors.
func ResolveAs(path string, euid int) (string, error) {
	return "", AncestorsAs(path, euid)
}

// Entry refuses every path on Windows. See Ancestors.
func Entry(path string, dir bool) error {
	return EntryAs(path, dir, -1)
}

// EntryAs refuses every path on Windows. See Ancestors.
func EntryAs(path string, _ bool, _ int) error {
	return fmt.Errorf("cannot check who may write %s, because this check runs only on Unix", path)
}
