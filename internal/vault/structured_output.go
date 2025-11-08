package vault

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/output"
	"gopkg.in/yaml.v3"
)

// ValidationEntry represents a single validation event.
type ValidationEntry struct {
	Timestamp          time.Time `json:"timestamp" yaml:"timestamp"`
	Path               string    `json:"path" yaml:"path"`
	Key                string    `json:"key" yaml:"key"`
	FullPath           string    `json:"full_path" yaml:"full_path"`
	Depth              int       `json:"depth" yaml:"depth"`
	ParentPath         string    `json:"parent_path" yaml:"parent_path"`
	IsLastSibling      bool      `json:"is_last_sibling" yaml:"is_last_sibling"`
	InceptionChecksum  string    `json:"inception_checksum" yaml:"inception_checksum"`
	ProductionChecksum string    `json:"production_checksum" yaml:"production_checksum"`
	Status             string    `json:"status" yaml:"status"` // "ok", "mismatch", "error"
	ErrorMessage       string    `json:"error_message,omitempty" yaml:"error_message,omitempty"`
}

// StructuredOutputWriter handles JSON/YAML output.
type StructuredOutputWriter struct {
	mode output.Mode
}

// NewStructuredOutputWriter creates writer for JSON or YAML mode.
func NewStructuredOutputWriter(mode output.Mode) *StructuredOutputWriter {
	return &StructuredOutputWriter{
		mode: mode,
	}
}

// WriteValidation outputs a single validation entry.
func (w *StructuredOutputWriter) WriteValidation(entry ValidationEntry) error {
	entry.Timestamp = time.Now()

	if w.mode == output.ModeJSON {
		encoder := json.NewEncoder(os.Stdout)
		return encoder.Encode(entry)
	} else if w.mode == output.ModeYAML {
		encoder := yaml.NewEncoder(os.Stdout)
		return encoder.Encode(entry)
	}

	return nil
}

// validateWithStructuredOutput validates with JSON/YAML logging.
func (m *Manager) validateWithStructuredOutput(
	tree *VaultTree,
	inceptionSafe, productionSafe *Safe,
	mode output.Mode,
) (int, error) {
	writer := NewStructuredOutputWriter(mode)

	validatedCount := 0
	var validationErrors []ValidationEntry

	// Traverse tree and output structured entries
	err := m.traverseTreeForStructuredOutput(
		tree.Root,
		inceptionSafe,
		productionSafe,
		writer,
		&validatedCount,
		&validationErrors,
		0,  // depth
		"", // parent path
	)

	if len(validationErrors) > 0 {
		return validatedCount, fmt.Errorf("validation failed for %d secret(s)", len(validationErrors))
	}

	return validatedCount, err
}

// traverseTreeForStructuredOutput performs DFS with structured logging.
func (m *Manager) traverseTreeForStructuredOutput(
	node *TreeNode,
	inceptionSafe, productionSafe *Safe,
	writer *StructuredOutputWriter,
	validatedCount *int,
	validationErrors *[]ValidationEntry,
	depth int,
	parentPath string,
) error {
	if node == nil {
		return nil
	}

	// Sort children and keys
	childNames := make([]string, 0, len(node.Children))
	for name := range node.Children {
		childNames = append(childNames, name)
	}
	sort.Strings(childNames)
	sort.Strings(node.Keys)

	totalItems := len(childNames) + len(node.Keys)
	currentItem := 0

	// Process children
	for _, childName := range childNames {
		child := node.Children[childName]
		currentItem++

		if err := m.traverseTreeForStructuredOutput(
			child,
			inceptionSafe,
			productionSafe,
			writer,
			validatedCount,
			validationErrors,
			depth+1,
			node.FullPath,
		); err != nil {
			return err
		}
	}

	// Process keys
	for _, key := range node.Keys {
		currentItem++
		isLast := currentItem == totalItems

		pathWithKey := strings.TrimPrefix(node.FullPath, "secret/") + ":" + key
		inceptionHash, productionHash, err := m.validateSinglePath(
			inceptionSafe,
			productionSafe,
			"secret/",
			pathWithKey,
		)

		entry := ValidationEntry{
			Path:               node.FullPath,
			Key:                key,
			FullPath:           node.FullPath + ":" + key,
			Depth:              depth,
			ParentPath:         parentPath,
			IsLastSibling:      isLast,
			InceptionChecksum:  inceptionHash,
			ProductionChecksum: productionHash,
		}

		if err != nil {
			entry.Status = "error"
			entry.ErrorMessage = err.Error()
			if strings.Contains(err.Error(), "mismatch") {
				entry.Status = "mismatch"
			}
			*validationErrors = append(*validationErrors, entry)
		} else {
			entry.Status = "ok"
			*validatedCount++
		}

		if err := writer.WriteValidation(entry); err != nil {
			return fmt.Errorf("failed to write validation entry: %w", err)
		}
	}

	return nil
}
