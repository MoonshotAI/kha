package output

import (
	"bytes"
	"strings"
	"testing"
)

func TestDisplayWidth(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
	}{
		{"", 0},
		{"abc", 3},
		{"agent_01x", 9},
		{"天眼查", 6},       // CJK unified ideographs
		{"金融投资分析助手", 16}, // longer CJK name
		{"カタカナ", 8},      // katakana
		{"한글", 4},        // hangul syllables
		{"价格：100！", 11},  // CJK + fullwidth colon/exclamation (2 each)
		{"（括弧）", 8},      // fullwidth parens
		{"rocket 🚀", 9},  // emoji width 2
		{"é", 1},        // e + combining acute (U+0301)
		{"a\u200db", 2},  // ZWJ is zero-width
		{"a\uFEFFb", 2},  // BOM is zero-width
		{"a\x00b", 2},    // control char is zero-width
		{"áb中", 4},
	} {
		if got := DisplayWidth(tc.in); got != tc.want {
			t.Errorf("DisplayWidth(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestPrintTableCJKAlignment renders a table mixing ASCII, CJK, fullwidth
// punctuation, and emoji, and asserts every column starts at the same
// display-width offset on every line (tabwriter fails this because it
// counts runes, not terminal cells).
func TestPrintTableCJKAlignment(t *testing.T) {
	headers := []string{"ID", "NAME", "VERSION", "CREATED"}
	rows := [][]string{
		{"agent_01", "demo", "v1", "2026-08-01 10:00:00"},
		{"agent_02", "天眼查", "v12", "2026-08-02 11:00:00"},
		{"agent_03", "金融投资分析助手", "v3", "2026-08-03 12:00:00"},
		{"agent_04", "价格：100！", "v4", "2026-08-04 13:00:00"},
		{"agent_05", "rocket 🚀 launch", "v5", "2026-08-05 14:00:00"},
	}
	var buf bytes.Buffer
	if err := PrintTable(&buf, headers, rows); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != len(rows)+1 {
		t.Fatalf("got %d lines, want %d:\n%s", len(lines), len(rows)+1, buf.String())
	}

	// Expected column offsets in display cells: widest cell per column + 2.
	widths := make([]int, len(headers))
	all := append([][]string{headers}, rows...)
	for _, r := range all {
		for i, c := range r {
			widths[i] = max(widths[i], DisplayWidth(c))
		}
	}
	wantOffsets := make([]int, len(headers))
	for i := 1; i < len(headers); i++ {
		wantOffsets[i] = wantOffsets[i-1] + widths[i-1] + 2
	}

	// Locate each cell in its line (sequential search; no cell is empty or
	// a prefix of padding) and compare display-width offsets across lines.
	for li, r := range all {
		line := lines[li]
		pos := 0
		for ci, cell := range r {
			idx := strings.Index(line[pos:], cell)
			if idx < 0 {
				t.Fatalf("line %d missing cell %q:\n%s", li, cell, buf.String())
			}
			start := pos + idx
			if got := DisplayWidth(line[:start]); got != wantOffsets[ci] {
				t.Errorf("line %d column %d starts at display offset %d, want %d:\n%s",
					li, ci, got, wantOffsets[ci], buf.String())
			}
			pos = start + len(cell)
		}
	}
}

func TestPrintTableSanitizesTabsAndNewlines(t *testing.T) {
	var buf bytes.Buffer
	if err := PrintTable(&buf, []string{"ID", "NAME"}, [][]string{
		{"agent_1", "a\tb\nc"},
	}); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if strings.ContainsAny(got, "\t") || strings.Count(got, "\n") != 2 {
		t.Fatalf("cell control characters leaked into output: %q", got)
	}
	if !strings.Contains(got, "a b c") {
		t.Fatalf("tab/newline not replaced with spaces: %q", got)
	}
}

func TestPrintTableShortRow(t *testing.T) {
	var buf bytes.Buffer
	if err := PrintTable(&buf, []string{"ID", "NAME", "CREATED"}, [][]string{
		{"agent_1", "demo"},
	}); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if !strings.Contains(got, "agent_1  demo\n") {
		t.Fatalf("short row not handled: %q", got)
	}
	if strings.HasSuffix(got, " \n") || strings.HasSuffix(got, " ") {
		t.Fatalf("trailing padding not trimmed: %q", got)
	}
}
