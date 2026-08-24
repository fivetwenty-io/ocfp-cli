// Package ui provides terminal user interface components for the OCFP CLI.
package ui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	yaml "github.com/goccy/go-yaml"
	"github.com/olekukonko/tablewriter"
	"github.com/olekukonko/tablewriter/tw"
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
		err := tableWriter.Append(row)
		if err != nil {
			return fmt.Errorf("failed to append table row: %w", err)
		}
	}

	err := tableWriter.Render()
	if err != nil {
		return fmt.Errorf("failed to render table: %w", err)
	}

	_, err = fmt.Fprint(os.Stdout, buf.String())
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
	tableWriter := tablewriter.NewTable(buf,
		tablewriter.WithRendition(tableRendition()),
		tablewriter.WithConfig(tableConfig(section)),
	)

	if len(section.Headers) > 0 {
		tableWriter.Header(section.Headers)
	}

	return tableWriter
}

// tableRendition describes the visual styling: full borders, a line under the
// header, and a separator between every row.
func tableRendition() tw.Rendition {
	style := tw.StyleLight
	if ASCII {
		style = tw.StyleASCII
	}

	return tw.Rendition{
		Borders: tw.Border{Left: tw.On, Right: tw.On, Top: tw.On, Bottom: tw.On, Overwrite: true},
		Symbols: tw.NewSymbols(style),
		Settings: tw.Settings{
			Separators: tw.Separators{
				ShowHeader:     tw.On,
				ShowFooter:     tw.On,
				BetweenRows:    tw.On,
				BetweenColumns: tw.On,
			},
			Lines: tw.Lines{
				ShowTop:        tw.On,
				ShowBottom:     tw.On,
				ShowHeaderLine: tw.On,
				ShowFooterLine: tw.On,
			},
			CompactMode: tw.Off,
		},
	}
}

// tableConfig disables text wrapping and header title-casing, keeping cells
// verbatim, and right-aligns purely numeric columns.
func tableConfig(section Section) tablewriter.Config {
	cellConfig := func(perColumn []tw.Align) tw.CellConfig {
		return tw.CellConfig{
			Formatting: tw.CellFormatting{AutoWrap: tw.WrapNone, AutoFormat: tw.Off},
			Alignment:  tw.CellAlignment{Global: tw.AlignLeft, PerColumn: perColumn},
		}
	}

	return tablewriter.Config{
		Header:   cellConfig(nil),
		Row:      cellConfig(columnAlignments(section)),
		Behavior: tw.Behavior{TrimSpace: tw.Off},
	}
}

// columnAlignments returns per-column alignment, right-aligning numeric columns.
func columnAlignments(section Section) []tw.Align {
	if len(section.Headers) == 0 || len(section.Rows) == 0 {
		return nil
	}

	colAlign := make([]tw.Align, len(section.Headers))
	for c := range section.Headers {
		if isNumericColumn(section.Rows, c) {
			colAlign[c] = tw.AlignRight
		} else {
			colAlign[c] = tw.AlignLeft
		}
	}

	return colAlign
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
