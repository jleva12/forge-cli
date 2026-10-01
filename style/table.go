package style

import (
	"fmt"
	"io"
	"strings"
)

// Table renders aligned columns. Unlike text/tabwriter it measures cells
// without color codes, so styled cells line up.
type Table struct {
	p       Painter
	headers []string
	rows    [][]string
}

// NewTable creates a table whose styling follows w.
func NewTable(w io.Writer, headers ...string) *Table {
	return &Table{p: For(w), headers: headers}
}

// Row adds a row; missing cells are blank.
func (t *Table) Row(cells ...string) { t.rows = append(t.rows, cells) }

// Len is the number of rows.
func (t *Table) Len() int { return len(t.rows) }

// Render writes the table. The last column isn't padded.
func (t *Table) Render(w io.Writer) error {
	cols := len(t.headers)
	for _, r := range t.rows {
		cols = max(cols, len(r))
	}
	widths := make([]int, cols)
	for i, h := range t.headers {
		widths[i] = Width(h)
	}
	for _, r := range t.rows {
		for i, c := range r {
			widths[i] = max(widths[i], Width(c))
		}
	}
	line := func(cells []string, style func(string) string) string {
		var b strings.Builder
		for i := 0; i < cols; i++ {
			cell := ""
			if i < len(cells) {
				cell = cells[i]
			}
			if style != nil {
				cell = style(cell)
			}
			if i < cols-1 {
				b.WriteString(Pad(cell, widths[i]) + "  ")
			} else {
				b.WriteString(cell)
			}
		}
		return strings.TrimRight(b.String(), " ")
	}
	if len(t.headers) > 0 {
		if _, err := fmt.Fprintln(w, line(t.headers, t.p.Heading)); err != nil {
			return err
		}
	}
	for _, r := range t.rows {
		if _, err := fmt.Fprintln(w, line(r, nil)); err != nil {
			return err
		}
	}
	return nil
}
