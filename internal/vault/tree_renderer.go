package vault

import (
	"fmt"
	"os"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/output"
)

// TreeRenderer handles hierarchical display during vault migration.
type TreeRenderer struct {
	useUnicode     bool                // Unicode support detected
	useColor       bool                // Color output enabled
	indentStack    []bool              // Track which levels need vertical bars
	failures       []ValidationFailure // Collected validation failures
	totalValidated int                 // Count of validated secrets
}

// ValidationFailure tracks a single validation error.
type ValidationFailure struct {
	FullPath           string
	Key                string
	InceptionChecksum  string
	ProductionChecksum string
	ErrorMessage       string
}

// NewTreeRenderer creates renderer with terminal capability detection.
func NewTreeRenderer(mode output.Mode) *TreeRenderer {
	return &TreeRenderer{
		useUnicode:     detectUnicodeSupport(),
		useColor:       mode == output.ModeInteractive,
		indentStack:    []bool{},
		failures:       []ValidationFailure{},
		totalValidated: 0,
	}
}

// StartDirectory begins rendering a directory node.
func (tr *TreeRenderer) StartDirectory(name string, isLast bool) {
	prefix, connector := tr.getTreeChars(isLast)

	// Print directory with tree characters
	if tr.useColor {
		_, _ = fmt.Fprintf(os.Stdout, "%s%s\033[34m%s/\033[0m\n", prefix, connector, name)
	} else {
		_, _ = fmt.Fprintf(os.Stdout, "%s%s%s/\n", prefix, connector, name)
	}

	// Update indent stack for children
	tr.indentStack = append(tr.indentStack, !isLast)
}

// EndDirectory completes a directory node (pops indent stack).
func (tr *TreeRenderer) EndDirectory() {
	if len(tr.indentStack) > 0 {
		tr.indentStack = tr.indentStack[:len(tr.indentStack)-1]
	}
}

// RenderKeyValidation displays a key with validation checksums.
func (tr *TreeRenderer) RenderKeyValidation(
	key string,
	inceptionHash string,
	productionHash string,
	err error,
	isLast bool,
) {
	prefix, connector := tr.getTreeChars(isLast)

	// Format key name with colon prefix
	keyDisplay := ":" + key

	if err != nil { //nolint:nestif // error handling tree with fallback logic
		// Validation failed
		if tr.useColor {
			_, _ = fmt.Fprintf(os.Stdout, "%s%s%s %s → %s \033[31m✗\033[0m (%v)\n",
				prefix, connector, keyDisplay,
				truncateHash(inceptionHash), truncateHash(productionHash), err)
		} else {
			_, _ = fmt.Fprintf(os.Stdout, "%s%s%s %s -> %s X (%v)\n",
				prefix, connector, keyDisplay,
				truncateHash(inceptionHash), truncateHash(productionHash), err)
		}

		// Collect failure
		tr.failures = append(tr.failures, ValidationFailure{
			Key:                key,
			InceptionChecksum:  inceptionHash,
			ProductionChecksum: productionHash,
			ErrorMessage:       err.Error(),
		})
	} else {
		// Validation success
		if tr.useColor {
			_, _ = fmt.Fprintf(os.Stdout, "%s%s%s %s → %s \033[32m✓\033[0m\n",
				prefix, connector, keyDisplay,
				truncateHash(inceptionHash), truncateHash(productionHash))
		} else {
			_, _ = fmt.Fprintf(os.Stdout, "%s%s%s %s -> %s ok\n",
				prefix, connector, keyDisplay,
				truncateHash(inceptionHash), truncateHash(productionHash))
		}

		tr.totalValidated++
	}
}

// RenderFailureSummary displays collected validation failures.
func (tr *TreeRenderer) RenderFailureSummary() error {
	if len(tr.failures) == 0 {
		return nil
	}

	_, _ = fmt.Fprintln(os.Stdout)

	if tr.useColor {
		_, _ = fmt.Fprintln(os.Stdout, "\033[31m=== Validation Failures ===\033[0m")
	} else {
		_, _ = fmt.Fprintln(os.Stdout, "=== Validation Failures ===")
	}

	_, _ = fmt.Fprintln(os.Stdout)

	for i, failure := range tr.failures {
		_, _ = fmt.Fprintf(os.Stdout, "%d. Key: %s\n", i+1, failure.Key)
		_, _ = fmt.Fprintf(os.Stdout, "   Inception:  %s\n", failure.InceptionChecksum)
		_, _ = fmt.Fprintf(os.Stdout, "   Production: %s\n", failure.ProductionChecksum)
		_, _ = fmt.Fprintf(os.Stdout, "   Error: %s\n", failure.ErrorMessage)
		_, _ = fmt.Fprintln(os.Stdout)
	}

	return nil
}

// detectUnicodeSupport checks terminal capabilities.
func detectUnicodeSupport() bool {
	// Check LANG and LC_* environment variables for UTF-8 support
	lang := os.Getenv("LANG")
	lcAll := os.Getenv("LC_ALL")

	if strings.Contains(lang, "UTF-8") || strings.Contains(lang, "utf8") {
		return true
	}

	if strings.Contains(lcAll, "UTF-8") || strings.Contains(lcAll, "utf8") {
		return true
	}

	// Check TERM for unicode support indicators
	term := os.Getenv("TERM")
	if strings.Contains(term, "256color") || strings.Contains(term, "xterm") {
		return true
	}

	// Default to ASCII for safety
	return false
}

// getTreeChars returns appropriate tree characters based on position.
func (tr *TreeRenderer) getTreeChars(isLast bool) (string, string) {
	// Build prefix from indent stack
	var parts []string

	for _, needsBar := range tr.indentStack {
		if needsBar {
			if tr.useUnicode {
				parts = append(parts, "│  ")
			} else {
				parts = append(parts, "|  ")
			}
		} else {
			parts = append(parts, "   ")
		}
	}

	prefix := strings.Join(parts, "")

	// Connector for current node
	var connector string

	if isLast { //nolint:nestif // tree rendering with conditional formatting
		if tr.useUnicode {
			connector = "└─ "
		} else {
			connector = "\\- "
		}
	} else {
		if tr.useUnicode {
			connector = "├─ "
		} else {
			connector = "|- "
		}
	}

	return prefix, connector
}

// truncateHash returns the first 8 characters of a hash for display.
func truncateHash(hash string) string {
	if len(hash) > 8 { //nolint:mnd
		return hash[:8]
	}

	return hash
}
