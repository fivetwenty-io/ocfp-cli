package bastion

import (
	udiff "github.com/aymanbagabas/go-udiff"
)

// unifiedDiff renders a unified diff between current and desired, labelling the
// two sides fromLabel and toLabel. It returns an empty string when the two
// match, so callers can treat "no output" as "no change".
func unifiedDiff(fromLabel, toLabel, current, desired string) string {
	edits := udiff.Lines(current, desired)

	// ToUnified only fails on edits that do not apply to the source, which
	// cannot happen for edits Lines just derived from that same source.
	text, err := udiff.ToUnified(fromLabel, toLabel, current, edits, diffContextLines)
	if err != nil {
		return ""
	}

	return text
}
