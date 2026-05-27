// Package ui provides terminal user interface components for the OCFP CLI.
package ui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	yaml "github.com/goccy/go-yaml"
	"github.com/olekukonko/tablewriter"
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
var ASCII bool //nolint:gochecknoglobals // global rendering toggle for UI tables

// SetASCII sets ASCII-only rendering for tables.
func SetASCII(on bool) { ASCII = on }

// Render prints the table in the requested format: "table", "json", or "yaml".
// Stdout is reserved for UX output; logs should be file-only via the logger package.
func Render(table *Table, format string) error {
	switch format {
	case "json":
		return renderJSON(table)
	case "yaml":
		return renderYAML(table)
	default:
		return renderTable(table)
	}
}

func renderJSON(table *Table) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")

	err := enc.Encode(table)
	if err != nil {
		return fmt.Errorf("failed to encode JSON: %w", err)
	}

	return nil
}

func renderYAML(table *Table) error {
	data, err := yaml.Marshal(table)
	if err != nil {
		return fmt.Errorf("failed to marshal YAML: %w", err)
	}

	_, err = os.Stdout.Write(data)
	if err != nil {
		return fmt.Errorf("failed to write YAML output: %w", err)
	}

	return nil
}

func renderTable(table *Table) error {
	err := printTableHeader(table)
	if err != nil {
		return fmt.Errorf("failed to print table header: %w", err)
	}

	for _, section := range table.Sections {
		err := renderSection(section)
		if err != nil {
			return fmt.Errorf("failed to render section: %w", err)
		}
	}

	return nil
}

func printTableHeader(table *Table) error {
	if table.Title != "" {
		_, err := fmt.Fprintf(os.Stdout, "%s\n\n", table.Title)
		if err != nil {
			return fmt.Errorf("failed to write title: %w", err)
		}
	}

	if table.Summary != "" {
		_, err := fmt.Fprintf(os.Stdout, "%s\n\n", table.Summary)
		if err != nil {
			return fmt.Errorf("failed to write summary: %w", err)
		}
	}

	return nil
}

func renderSection(section Section) error {
	if section.Title != "" {
		_, err := fmt.Fprint(os.Stdout, section.Title+"\n")
		if err != nil {
			return fmt.Errorf("failed to write section title: %w", err)
		}
	}

	// If there are no rows, just print the title and skip table rendering
	if len(section.Rows) == 0 {
		_, err := fmt.Fprint(os.Stdout, "\n")
		if err != nil {
			return fmt.Errorf("failed to write newline: %w", err)
		}

		return nil
	}

	var buf bytes.Buffer

	tableWriter := setupTableWriter(&buf, section)

	for _, row := range section.Rows {
		tableWriter.Append(row)
	}

	tableWriter.Render()

	out := buf.String()
	if !ASCII {
		out = boxifyUnicode(out)
	}

	_, err := fmt.Fprint(os.Stdout, out)
	if err != nil {
		return fmt.Errorf("failed to write table output: %w", err)
	}

	_, err = fmt.Fprint(os.Stdout, "\n")
	if err != nil {
		return fmt.Errorf("failed to write newline: %w", err)
	}

	return nil
}

func setupTableWriter(buf *bytes.Buffer, section Section) *tablewriter.Table {
	tableWriter := tablewriter.NewWriter(buf)

	if len(section.Headers) > 0 {
		tableWriter.SetHeader(section.Headers)
	}

	configureTableStyle(tableWriter)
	configureColumnAlignment(tableWriter, section)

	return tableWriter
}

func configureTableStyle(tableWriter *tablewriter.Table) {
	tableWriter.SetAutoWrapText(false)
	tableWriter.SetAutoFormatHeaders(false)
	tableWriter.SetHeaderAlignment(tablewriter.ALIGN_LEFT)
	tableWriter.SetAlignment(tablewriter.ALIGN_LEFT)
	tableWriter.SetBorder(true)
	tableWriter.SetRowLine(true)
	tableWriter.SetHeaderLine(true)
	tableWriter.SetBorders(tablewriter.Border{Left: true, Top: true, Right: true, Bottom: true})

	if ASCII {
		tableWriter.SetCenterSeparator("+")
		tableWriter.SetColumnSeparator("|")
		tableWriter.SetRowSeparator("-")
	} else {
		tableWriter.SetCenterSeparator("┼")
		tableWriter.SetColumnSeparator("│")
		tableWriter.SetRowSeparator("─")
	}
}

func configureColumnAlignment(tableWriter *tablewriter.Table, section Section) {
	if len(section.Headers) == 0 || len(section.Rows) == 0 {
		return
	}

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

// isNumericColumn returns true if all non-empty cells in column c parse as numbers.
func isNumericColumn(rows [][]string, columnIndex int) bool {
	for _, r := range rows {
		if columnIndex >= len(r) { // uneven row lengths are treated as non-numeric
			return false
		}

		v := r[columnIndex]
		if v == "" || v == "-" { // placeholders are ignored
			continue
		}

		_, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return false
		}
	}

	return true
}

// boxifyUnicode transforms tablewriter's uniform intersection characters into
// proper box-drawing corners and T-junctions for a cleaner look.
func boxifyUnicode(input string) string {
	lines := strings.Split(input, "\n")
	borderIdx := findBorderLines(lines)

	if len(borderIdx) == 0 {
		return input
	}

	applyBorderStyles(lines, borderIdx)

	return strings.Join(lines, "\n")
}

// findBorderLines identifies lines that contain border characters.
func findBorderLines(lines []string) []int {
	borderIdx := []int{}

	for index, line := range lines {
		if isBorderLine(line) {
			borderIdx = append(borderIdx, index)
		}
	}

	return borderIdx
}

// isBorderLine checks if a line is a border line.
func isBorderLine(line string) bool {
	l := strings.TrimSpace(line)
	if l == "" {
		return false
	}

	hasNoColomnSeparators := !strings.Contains(line, "│")
	hasHorizontalChars := strings.Contains(line, "─") || strings.Contains(line, "-")
	hasIntersections := strings.Contains(line, "┼") || strings.Contains(line, "+")

	return hasNoColomnSeparators && hasHorizontalChars && hasIntersections
}

// applyBorderStyles applies different border styles to top, middle, and bottom borders.
func applyBorderStyles(lines []string, borderIdx []int) {
	if len(borderIdx) == 0 {
		return
	}

	// Top border
	lines[borderIdx[0]] = replaceLine(lines[borderIdx[0]], '┌', '┬', '┐')

	// Bottom border
	if len(borderIdx) > 1 {
		lastIdx := len(borderIdx) - 1
		lines[borderIdx[lastIdx]] = replaceLine(lines[borderIdx[lastIdx]], '└', '┴', '┘')
	}

	// Middle borders (header separator and row separators)
	for _, i := range borderIdx[1 : len(borderIdx)-1] {
		lines[i] = replaceLine(lines[i], '├', '┼', '┤')
	}
}

// replaceLine replaces intersection characters with appropriate border characters.
func replaceLine(ln string, start, middle, end rune) string {
	runes := []rune(ln)

	replaceFirstIntersection(runes, start)
	replaceLastIntersection(runes, end)
	replaceMiddleIntersections(runes, middle)

	return string(runes)
}

// replaceFirstIntersection replaces the first intersection character.
func replaceFirstIntersection(runes []rune, start rune) {
	for runeIndex := range runes {
		if isIntersection(runes[runeIndex]) {
			runes[runeIndex] = start

			break
		}
	}
}

// replaceLastIntersection replaces the last intersection character.
func replaceLastIntersection(runes []rune, end rune) {
	for lastIndex := len(runes) - 1; lastIndex >= 0; lastIndex-- {
		if isIntersection(runes[lastIndex]) {
			runes[lastIndex] = end

			break
		}
	}
}

// replaceMiddleIntersections replaces all remaining intersection characters.
func replaceMiddleIntersections(runes []rune, middle rune) {
	for midIndex := range runes {
		if isIntersection(runes[midIndex]) {
			runes[midIndex] = middle
		}
	}
}

// isIntersection checks if a character is an intersection character.
func isIntersection(r rune) bool {
	return r == '┼' || r == '+'
}

// NewTable creates a new Table with the given title.
func NewTable(title string) *Table {
	return &Table{
		Title:    title,
		Summary:  "",
		Sections: []Section{},
	}
}

// SetHeaders sets the headers for the table (creates a single section).
func (t *Table) SetHeaders(headers []string) {
	if len(t.Sections) == 0 {
		t.Sections = append(t.Sections, Section{
			Title:   "",
			Headers: headers,
			Rows:    [][]string{},
		})
	} else {
		t.Sections[0].Headers = headers
	}
}

// AddRow adds a row to the table (to the first section).
func (t *Table) AddRow(row []string) {
	if len(t.Sections) == 0 {
		t.Sections = append(t.Sections, Section{
			Title:   "",
			Headers: []string{},
			Rows:    [][]string{},
		})
	}

	t.Sections[0].Rows = append(t.Sections[0].Rows, row)
}

// AddSection adds a new section to the table.
func (t *Table) AddSection(title string) {
	t.Sections = append(t.Sections, Section{
		Title:   title,
		Headers: []string{},
		Rows:    [][]string{},
	})
}

// AddSeparator adds a separator row to the current section.
func (t *Table) AddSeparator() {
	if len(t.Sections) == 0 {
		t.AddSection("")
	}
	// Add an empty row as separator
	t.Sections[len(t.Sections)-1].Rows = append(t.Sections[len(t.Sections)-1].Rows, []string{})
}

// Render renders the table to stdout using the default format.
func (t *Table) Render() error {
	return Render(t, "table")
}
