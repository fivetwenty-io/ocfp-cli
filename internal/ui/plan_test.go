package ui

// Golden-file tests for Render (table/json/yaml) and Table helper methods.
//
// Each test captures stdout to a buffer and compares it byte-for-byte against
// a fixture in testdata/. To regenerate fixtures after an intentional change:
//  1. Delete stale fixture files under internal/ui/testdata/.
//  2. Run: go test ./internal/ui/... -update
//  3. Review the diff and commit new fixtures alongside the code change.

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

var updateGolden = flag.Bool("update", false, "update golden fixture files instead of comparing")

const goldenDir = "testdata"

// captureStdout redirects os.Stdout to a pipe, calls fn, then returns all
// bytes written. It restores os.Stdout before returning even if fn panics.
func captureStdout(t *testing.T, fn func()) []byte {
	t.Helper()

	origStdout := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err, "os.Pipe")

	os.Stdout = w

	defer func() {
		os.Stdout = origStdout
	}()

	fn()
	w.Close()

	var buf bytes.Buffer
	_, err = io.Copy(&buf, r)
	require.NoError(t, err, "read pipe")

	return buf.Bytes()
}

func assertGolden(t *testing.T, name string, got []byte) {
	t.Helper()

	path := filepath.Join(goldenDir, name)

	if *updateGolden {
		require.NoError(t, os.MkdirAll(goldenDir, 0755), "create testdata dir")
		require.NoError(t, os.WriteFile(path, got, 0600), "write fixture %s", path)
		t.Logf("updated fixture %s", path)

		return
	}

	want, err := os.ReadFile(path)
	require.NoErrorf(t, err, "read fixture %s — regenerate with -update", path)

	if !bytes.Equal(want, got) {
		t.Errorf("golden mismatch for %s\n--- want ---\n%s\n--- got ---\n%s",
			name, want, got)
	}
}

// emptyTable returns a Table with no sections, title, or summary.
func emptyTable() *Table {
	return &Table{}
}

// oneActionTable returns a Table with a single section and one data row.
func oneActionTable() *Table {
	t := NewTable("Deployment Plan")
	t.Summary = "1 action pending"
	t.SetHeaders([]string{"Action", "Target", "Status"})
	t.AddRow([]string{"create", "bosh-director", "pending"})

	return t
}

// multiActionTable returns a Table with multiple sections and rows.
func multiActionTable() *Table {
	t := NewTable("Full Deployment Plan")
	t.Summary = "3 actions across 2 sections"

	t.SetHeaders([]string{"Action", "Target", "Status"})
	t.AddRow([]string{"create", "bosh-director", "pending"})
	t.AddRow([]string{"update", "cf", "pending"})

	t.AddSection("Cleanup")
	t.Sections[1].Headers = []string{"Resource", "Reason"}
	t.Sections[1].Rows = [][]string{
		{"old-stemcell", "superseded"},
	}

	return t
}

// numericColTable returns a Table whose second column contains numeric values,
// triggering right-alignment via isNumericColumn.
func numericColTable() *Table {
	t := NewTable("Resource Counts")
	t.SetHeaders([]string{"Component", "Count"})
	t.AddRow([]string{"VMs", "3"})
	t.AddRow([]string{"Disks", "6"})

	return t
}

// noRowsSectionTable returns a Table that has a section title but zero rows.
func noRowsSectionTable() *Table {
	t := NewTable("Empty Section Plan")
	t.AddSection("Nothing to do")

	return t
}

// TestRenderTable_Golden covers ASCII table rendering for all fixture inputs.
func TestRenderTable_Golden(t *testing.T) {
	// Force ASCII mode so golden files are deterministic across terminals.
	origASCII := ASCII
	SetASCII(true)

	defer func() { SetASCII(origASCII) }()

	cases := []struct {
		name  string
		table *Table
	}{
		{"table_empty.golden", emptyTable()},
		{"table_one_action.golden", oneActionTable()},
		{"table_multi_action.golden", multiActionTable()},
		{"table_numeric_col.golden", numericColTable()},
		{"table_no_rows_section.golden", noRowsSectionTable()},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := captureStdout(t, func() {
				require.NoError(t, Render(tc.table, "table"))
			})
			assertGolden(t, tc.name, got)
		})
	}
}

// TestRenderJSON_Golden covers JSON rendering.
func TestRenderJSON_Golden(t *testing.T) {
	cases := []struct {
		name  string
		table *Table
	}{
		{"json_empty.golden", emptyTable()},
		{"json_one_action.golden", oneActionTable()},
		{"json_multi_action.golden", multiActionTable()},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := captureStdout(t, func() {
				require.NoError(t, Render(tc.table, "json"))
			})
			assertGolden(t, tc.name, got)
		})
	}
}

// TestRenderYAML_Golden covers YAML rendering.
func TestRenderYAML_Golden(t *testing.T) {
	cases := []struct {
		name  string
		table *Table
	}{
		{"yaml_empty.golden", emptyTable()},
		{"yaml_one_action.golden", oneActionTable()},
		{"yaml_multi_action.golden", multiActionTable()},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := captureStdout(t, func() {
				require.NoError(t, Render(tc.table, "yaml"))
			})
			assertGolden(t, tc.name, got)
		})
	}
}

// TestRenderJSON_ValidJSON asserts JSON output is parseable regardless of golden files.
func TestRenderJSON_ValidJSON(t *testing.T) {
	tbl := oneActionTable()
	got := captureStdout(t, func() {
		require.NoError(t, Render(tbl, "json"))
	})

	var out map[string]interface{}
	require.NoError(t, json.Unmarshal(got, &out), "JSON output must be valid")
	require.Equal(t, "Deployment Plan", out["title"])
}

// TestRenderUnknownFormat falls back to table rendering without error.
func TestRenderUnknownFormat(t *testing.T) {
	origASCII := ASCII
	SetASCII(true)

	defer func() { SetASCII(origASCII) }()

	tbl := oneActionTable()
	got := captureStdout(t, func() {
		require.NoError(t, Render(tbl, "unknown-format"))
	})

	require.NotEmpty(t, got, "fallback table render must produce output")
}

// TestSetASCII verifies toggle persists on global.
func TestSetASCII(t *testing.T) {
	orig := ASCII
	defer func() { ASCII = orig }()

	SetASCII(true)
	require.True(t, ASCII)
	SetASCII(false)
	require.False(t, ASCII)
}

// TestIsNumericColumn verifies numeric detection logic.
func TestIsNumericColumn(t *testing.T) {
	cases := []struct {
		name    string
		rows    [][]string
		col     int
		numeric bool
	}{
		{"all numeric", [][]string{{"a", "1"}, {"b", "2"}}, 1, true},
		{"mixed", [][]string{{"a", "1"}, {"b", "x"}}, 1, false},
		{"placeholder dash", [][]string{{"a", "-"}, {"b", "3"}}, 1, true},
		{"empty cell", [][]string{{"a", ""}, {"b", "5"}}, 1, true},
		{"short row", [][]string{{"a"}}, 1, false},
		{"float values", [][]string{{"x", "1.5"}, {"y", "2.0"}}, 1, true},
		{"all empty", [][]string{{"a", ""}, {"b", ""}}, 1, true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			result := isNumericColumn(tc.rows, tc.col)
			require.Equal(t, tc.numeric, result,
				fmt.Sprintf("isNumericColumn(%v, %d)", tc.rows, tc.col))
		})
	}
}

// TestNewTable verifies constructor sets title and empty collections.
func TestNewTable(t *testing.T) {
	tbl := NewTable("my title")
	require.Equal(t, "my title", tbl.Title)
	require.Empty(t, tbl.Summary)
	require.Empty(t, tbl.Sections)
}

// TestTableHelpers covers SetHeaders, AddRow, AddSection, AddSeparator.
func TestTableHelpers(t *testing.T) {
	t.Run("SetHeaders creates first section", func(t *testing.T) {
		tbl := NewTable("")
		tbl.SetHeaders([]string{"A", "B"})
		require.Len(t, tbl.Sections, 1)
		require.Equal(t, []string{"A", "B"}, tbl.Sections[0].Headers)
	})

	t.Run("SetHeaders updates existing first section", func(t *testing.T) {
		tbl := NewTable("")
		tbl.Sections = []Section{{Title: "existing"}}
		tbl.SetHeaders([]string{"X"})
		require.Equal(t, []string{"X"}, tbl.Sections[0].Headers)
	})

	t.Run("AddRow creates section if none", func(t *testing.T) {
		tbl := NewTable("")
		tbl.AddRow([]string{"r1c1", "r1c2"})
		require.Len(t, tbl.Sections, 1)
		require.Len(t, tbl.Sections[0].Rows, 1)
	})

	t.Run("AddRow appends to first section", func(t *testing.T) {
		tbl := NewTable("")
		tbl.SetHeaders([]string{"H"})
		tbl.AddRow([]string{"v1"})
		tbl.AddRow([]string{"v2"})
		require.Len(t, tbl.Sections[0].Rows, 2)
	})

	t.Run("AddSection appends named section", func(t *testing.T) {
		tbl := NewTable("")
		tbl.AddSection("sec1")
		tbl.AddSection("sec2")
		require.Len(t, tbl.Sections, 2)
		require.Equal(t, "sec1", tbl.Sections[0].Title)
	})

	t.Run("AddSeparator creates section if none", func(t *testing.T) {
		tbl := NewTable("")
		tbl.AddSeparator()
		require.Len(t, tbl.Sections, 1)
		require.Len(t, tbl.Sections[0].Rows, 1)
	})

	t.Run("AddSeparator appends empty row to last section", func(t *testing.T) {
		tbl := NewTable("")
		tbl.AddSection("s")
		tbl.AddSeparator()
		require.Len(t, tbl.Sections[0].Rows, 1)
		require.Empty(t, tbl.Sections[0].Rows[0])
	})
}

// TestTableRenderMethod verifies the (t *Table).Render() convenience wrapper.
func TestTableRenderMethod(t *testing.T) {
	origASCII := ASCII
	SetASCII(true)

	defer func() { SetASCII(origASCII) }()

	tbl := oneActionTable()
	got := captureStdout(t, func() {
		require.NoError(t, tbl.Render())
	})

	require.NotEmpty(t, got)
}

// TestBoxifyUnicode_NoBorderLines confirms input without borders is returned unchanged.
func TestBoxifyUnicode_NoBorderLines(t *testing.T) {
	input := "plain text\nno borders here\n"
	got := boxifyUnicode(input)
	require.Equal(t, input, got)
}

// TestIsBorderLine covers the border-detection predicate.
func TestIsBorderLine(t *testing.T) {
	cases := []struct {
		line   string
		expect bool
	}{
		{"┼───┼───┼", true},
		{"+---+---+", true},
		{"│ foo │ bar │", false},
		{"", false},
		{"   ", false},
		{"─────────", false}, // no intersection
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.line, func(t *testing.T) {
			require.Equal(t, tc.expect, isBorderLine(tc.line))
		})
	}
}

// TestIsIntersection covers the rune predicate.
func TestIsIntersection(t *testing.T) {
	require.True(t, isIntersection('┼'))
	require.True(t, isIntersection('+'))
	require.False(t, isIntersection('─'))
	require.False(t, isIntersection('│'))
	require.False(t, isIntersection('x'))
}
