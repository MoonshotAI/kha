// Package output renders CLI results. PrintResponse, PrintAll, and
// StreamPrinter handle response formats and transformations; the JSON, YAML,
// table, and stream writers are their building blocks. Tables align by
// terminal display width, using width.go instead of text/tabwriter because
// tabwriter counts runes and misaligns CJK/fullwidth cells.
//
// Data goes to the writer handed in (command stdout); progress, warnings,
// and errors never pass through here — they belong to stderr.
package output

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"encoding/json/jsontext"
	json "encoding/json/v2"
)

// PrintJSON writes v as indented JSON followed by a newline.
func PrintJSON(w io.Writer, v any) error {
	data, err := json.Marshal(v, jsontext.Multiline(true), jsontext.WithIndent("  "))
	if err != nil {
		return fmt.Errorf("encoding result as JSON: %w", err)
	}
	_, err = fmt.Fprintf(w, "%s\n", data)
	return err
}

// ArrayStream writes one JSON array whose items arrive one at a time (a
// draining --all listing): the first Item opens "[", each item is marshalled
// with the same multiline indent as PrintJSON and prefixed two spaces per
// line, ",\n" separates items, and Close finishes with "\n]". The bytes are
// identical to PrintJSON on the collected slice; with no items the output is
// the "[]" PrintJSON prints for an empty slice. A stream abandoned before
// Close (an error stopped the feed) stays unterminated — the partial array
// a failed feed leaves behind.
type ArrayStream struct {
	w       io.Writer
	started bool
}

// NewArrayStream returns an ArrayStream writing to w. Nothing is written
// until the first Item (or Close on an empty stream).
func NewArrayStream(w io.Writer) *ArrayStream {
	return &ArrayStream{w: w}
}

// Item appends v as the next array element.
func (s *ArrayStream) Item(v any) error {
	data, err := json.Marshal(v, jsontext.Multiline(true), jsontext.WithIndent("  "))
	if err != nil {
		return fmt.Errorf("encoding result as JSON: %w", err)
	}
	if !s.started {
		s.started = true
		if _, err := io.WriteString(s.w, "[\n"); err != nil {
			return err
		}
	} else if _, err := io.WriteString(s.w, ",\n"); err != nil {
		return err
	}
	for i, line := range strings.Split(string(data), "\n") {
		if i > 0 {
			if _, err := io.WriteString(s.w, "\n"); err != nil {
				return err
			}
		}
		if _, err := io.WriteString(s.w, "  "+line); err != nil {
			return err
		}
	}
	return nil
}

// Close terminates the array. With no Item calls it writes the empty array
// PrintJSON would print for an empty slice.
func (s *ArrayStream) Close() error {
	if !s.started {
		_, err := io.WriteString(s.w, "[]\n")
		return err
	}
	_, err := io.WriteString(s.w, "\n]\n")
	return err
}

// PrintTable writes one header row plus the given rows, column-aligned by
// terminal display width: every column is as wide as its widest cell plus
// two spaces of separation (the last column is not padded). An empty row
// set still prints the header, so scripts can rely on the
// column set. Rows with fewer cells than headers are treated as empty in
// the missing columns; extra cells are dropped.
//
// The table's total width is capped at maxTableWidth display cells: when the
// natural widths exceed it, the widest columns are shrunk (never below
// minColumnWidth) and over-long cells are truncated with an ellipsis.
// Unbounded server-side values — an agent-generated Session title can run to
// thousands of characters — would otherwise stretch every row far past any
// terminal. Full values stay available via -o json.
func PrintTable(w io.Writer, headers []string, rows [][]string) error {
	cols := len(headers)
	widths := make([]int, cols)
	for i, h := range headers {
		widths[i] = DisplayWidth(sanitizeCell(h))
	}
	for _, r := range rows {
		for i := 0; i < cols && i < len(r); i++ {
			widths[i] = max(widths[i], DisplayWidth(sanitizeCell(r[i])))
		}
	}
	fitWidths(widths)
	var b strings.Builder
	writeRow := func(cells []string) {
		var line strings.Builder
		for i := range cols {
			c := ""
			if i < len(cells) {
				c = truncateToWidth(sanitizeCell(cells[i]), widths[i])
			}
			line.WriteString(c)
			if i < cols-1 {
				line.WriteString(strings.Repeat(" ", widths[i]-DisplayWidth(c)+2))
			}
		}
		// Trailing padding (short rows) carries no information; trim it.
		b.WriteString(strings.TrimRight(line.String(), " "))
		b.WriteByte('\n')
	}
	writeRow(headers)
	for _, r := range rows {
		writeRow(r)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// StreamTable writes a table whose rows arrive one at a time (a live event
// stream): NewStreamTable prints the header once, then Row appends one line
// per item. Column widths are fixed up front — widths floors each column's
// padding (pass nil to pad to the header widths) — so rows are never
// buffered and items print as they arrive. A cell longer than its column
// keeps its natural width and overflows past the two-space gap instead of
// being truncated; there is no cross-row alignment like PrintTable's.
type StreamTable struct {
	w      io.Writer
	widths []int
}

// NewStreamTable prints the header row and returns the table to stream rows
// into with Row.
func NewStreamTable(w io.Writer, headers []string, widths []int) (*StreamTable, error) {
	t := &StreamTable{w: w, widths: make([]int, len(headers))}
	for i, h := range headers {
		t.widths[i] = DisplayWidth(sanitizeCell(h))
		if i < len(widths) {
			t.widths[i] = max(t.widths[i], widths[i])
		}
	}
	if err := t.Row(headers); err != nil {
		return nil, err
	}
	return t, nil
}

// Row writes one streamed row, sanitizing cells like PrintTable. Rows with
// fewer cells than columns leave the missing columns empty; extra cells are
// dropped.
func (t *StreamTable) Row(cells []string) error {
	var line strings.Builder
	for i := range t.widths {
		c := ""
		if i < len(cells) {
			c = sanitizeCell(cells[i])
		}
		line.WriteString(c)
		if i < len(t.widths)-1 {
			line.WriteString(strings.Repeat(" ", max(t.widths[i]-DisplayWidth(c), 0)+2))
		}
	}
	_, err := fmt.Fprintln(t.w, strings.TrimRight(line.String(), " "))
	return err
}

// maxTableWidth caps a rendered table's total display width, separators
// included. minColumnWidth is the floor below which shrinking a column stops
// helping; a table that still does not fit is accepted as-is.
const (
	maxTableWidth  = 120
	minColumnWidth = 8
)

// fitWidths shrinks columns until the table (columns plus the two-space
// separators) fits maxTableWidth, never below minColumnWidth. The column
// whose natural width is largest bears the truncation first: the overflow
// comes from unbounded text cells (an agent-generated title can run to
// thousands of characters), so the text column gives way before bounded
// ones — an ID column the user copies for follow-up commands stays intact
// unless everything else is at the floor already.
func fitWidths(widths []int) {
	natural := slices.Clone(widths)
	total := func() int {
		t := 2 * (len(widths) - 1)
		for _, w := range widths {
			t += w
		}
		return t
	}
	for total() > maxTableWidth {
		widest := -1
		for i := range widths {
			if widths[i] <= minColumnWidth {
				continue
			}
			if widest < 0 || natural[i] > natural[widest] || (natural[i] == natural[widest] && widths[i] > widths[widest]) {
				widest = i
			}
		}
		if widest < 0 {
			return
		}
		widths[widest]--
	}
}

// truncateToWidth renders s in at most limit display cells, cutting it with
// an ellipsis when it is longer. limit includes the ellipsis cell.
func truncateToWidth(s string, limit int) string {
	if DisplayWidth(s) <= limit {
		return s
	}
	if limit <= 1 {
		return "…"
	}
	var b strings.Builder
	w := 0
	for _, r := range s {
		rw := runeWidth(r)
		if w+rw > limit-1 {
			break
		}
		b.WriteRune(r)
		w += rw
	}
	b.WriteRune('…')
	return b.String()
}

// Dash replaces an empty string with "-" for table cells.
func Dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// Time renders t in local time for table cells, "-" when zero.
func Time(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04:05")
}

// Bytes renders a byte count in compact human form for table cells.
func Bytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit && exp < 4; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
