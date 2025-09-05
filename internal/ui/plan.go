package ui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/olekukonko/tablewriter"
	yaml "gopkg.in/yaml.v3"
)

// Table represents a CLI-friendly plan with titled sections.
type Table struct {
	Title    string    `json:"title"    yaml:"title"`
	Summary  string    `json:"summary"  yaml:"summary"`
	Sections []Section `json:"sections" yaml:"sections"`
}

// Section is a titled table with headers and rows.
type Section struct {
	Title   string     `json:"title"   yaml:"title"`
	Headers []string   `json:"headers" yaml:"headers"`
	Rows    [][]string `json:"rows"    yaml:"rows"`
}

// ASCII controls whether Render prints ASCII-only tables.
// Defaults to false (use Unicode box-drawing where supported).
var ASCII bool

// SetASCII sets ASCII-only rendering for tables.
func SetASCII(on bool) { ASCII = on }

// Render prints the table in the requested format: "table", "json", or "yaml".
// Stdout is reserved for UX output; logs should be file-only via the logger package.
func Render(table *Table, format string) error {
	switch format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")

		return enc.Encode(table)
	case "yaml":
		data, err := yaml.Marshal(table)
		if err != nil {
			return err
		}

		_, err = os.Stdout.Write(data)

		return err
	default:
		// Pretty/table output
		if table.Title != "" {
			fmt.Println(table.Title)
			fmt.Println()
		}

		if table.Summary != "" {
			fmt.Println(table.Summary)
			fmt.Println()
		}

		for _, section := range table.Sections {
			if section.Title != "" {
				fmt.Println(section.Title)
			}
			// Render to buffer so we can post-process corners to pretty box-drawing
			var buf bytes.Buffer

			tableWriter := tablewriter.NewWriter(&buf)
			if len(section.Headers) > 0 {
				tableWriter.SetHeader(section.Headers)
			}
			// Tablewriter UX tweaks for clearer output
			tableWriter.SetAutoWrapText(false)
			tableWriter.SetAutoFormatHeaders(false)
			tableWriter.SetHeaderAlignment(tablewriter.ALIGN_LEFT)
			tableWriter.SetAlignment(tablewriter.ALIGN_LEFT)
			tableWriter.SetBorder(true)
			tableWriter.SetRowLine(true)
			tableWriter.SetHeaderLine(true)
			tableWriter.SetBorders(tablewriter.Border{Left: true, Top: true, Right: true, Bottom: true})

			if ASCII {
				// ASCII separators
				tableWriter.SetCenterSeparator("+")
				tableWriter.SetColumnSeparator("|")
				tableWriter.SetRowSeparator("-")
			} else {
				// Unicode box-drawing separators
				tableWriter.SetCenterSeparator("┼")
				tableWriter.SetColumnSeparator("│")
				tableWriter.SetRowSeparator("─")
			}

			// Align numeric columns to the right when possible
			if len(section.Headers) > 0 && len(section.Rows) > 0 {
				colAlign := make([]int, len(section.Headers))
				for c := range section.Headers {
					if isNumericColumn(section.Rows, c) {
						colAlign[c] = tablewriter.ALIGN_RIGHT
					} else {
						colAlign[c] = tablewriter.ALIGN_LEFT
					}
				}

				tableWriter.SetColumnAlignment(colAlign)
			}

			for _, row := range section.Rows {
				tableWriter.Append(row)
			}

			tableWriter.Render()

			out := buf.String()
			if !ASCII {
				// Fix corners and intersections for a cleaner Unicode table
				out = boxifyUnicode(out)
			}

			fmt.Print(out)
			fmt.Println()
		}

		return nil
	}
}

// isNumericColumn returns true if all non-empty cells in column c parse as numbers.
func isNumericColumn(rows [][]string, c int) bool {
	for _, r := range rows {
		if c >= len(r) { // uneven row lengths are treated as non-numeric
			return false
		}

		v := r[c]
		if v == "" || v == "-" { // placeholders are ignored
			continue
		}

		if _, err := strconv.ParseFloat(v, 64); err != nil {
			return false
		}
	}

	return true
}

// boxifyUnicode transforms tablewriter's uniform intersection characters into
// proper box-drawing corners and T-junctions for a cleaner look.
func boxifyUnicode(s string) string {
	lines := strings.Split(s, "\n")
	// Identify border-only lines (made of row and center separators)
	borderIdx := []int{}

	for i, ln := range lines {
		l := strings.TrimSpace(ln)
		if l == "" {
			continue
		}
		// Border lines should have no column separators and contain row+center glyphs
		if !strings.Contains(ln, "│") && (strings.Contains(ln, "─") || strings.Contains(ln, "-")) && (strings.Contains(ln, "┼") || strings.Contains(ln, "+")) {
			borderIdx = append(borderIdx, i)
		}
	}

	if len(borderIdx) == 0 {
		return s
	}

	// Helper to replace first/last intersections and the internal ones
	replaceLine := func(ln string, start, middle, end rune) string {
		rs := []rune(ln)
		// replace first intersection
		for i := 0; i < len(rs); i++ {
			if rs[i] == '┼' || rs[i] == '+' {
				rs[i] = start

				break
			}
		}
		// replace last intersection
		for i := len(rs) - 1; i >= 0; i-- {
			if rs[i] == '┼' || rs[i] == '+' {
				rs[i] = end

				break
			}
		}
		// replace internal intersections
		// skip first and last positions touched above
		// do a second pass to swap remaining intersections
		for i := 0; i < len(rs); i++ {
			if rs[i] == '┼' || rs[i] == '+' {
				rs[i] = middle
			}
		}

		return string(rs)
	}

	// Top border
	lines[borderIdx[0]] = replaceLine(lines[borderIdx[0]], '┌', '┬', '┐')
	// Bottom border
	lines[borderIdx[len(borderIdx)-1]] = replaceLine(lines[borderIdx[len(borderIdx)-1]], '└', '┴', '┘')
	// Middle borders (header separator and row separators)
	for _, i := range borderIdx[1 : len(borderIdx)-1] {
		lines[i] = replaceLine(lines[i], '├', '┼', '┤')
	}

	return strings.Join(lines, "\n")
}
