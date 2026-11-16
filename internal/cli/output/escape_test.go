package output

import (
	"strings"
	"testing"
)

// TestStripEscapes locks the terminal-injection stripping shared by table
// cells and the event renderer: CSI, OSC (BEL- and ST-terminated),
// other ESC-initiated sequences, and C0/C1 controls never survive; plain
// text, CJK, and a lone newline pass through.
func TestStripEscapes(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"plain text 界面", "plain text 界面"},
		{"line\nbreak", "line\nbreak"},
		{"\x1b[31mred\x1b[0m", "red"},
		{"\x1b[2J\x1b[1;1Hhome", "home"},
		{"\x1b]52;c;aGVsbG8=\a", ""},                         // OSC 52 clipboard write, BEL-terminated
		{"pre\x1b]8;;https://evil.example\x1b\\x\a", "prex"}, // OSC 8 hyperlink, ST-terminated
		{"\x1b(Bascii", "ascii"},                             // charset designation: ESC + intermediate + final byte
		{"\x1b(0li\x1b(Bne", "line"},                         // line-drawing charset in, ASCII back out
		{"\x1b%Gutf\x1b%@8", "utf8"},                         // charset selects with a shared intermediate byte
		{"bell\abel", "bellbel"},
		{"cr\rlf", "crlf"},
		{"c1\u009b31mx", "c131mx"}, // C1 control dropped (the bytes after it are plain text)
		{"\x1b", ""},               // trailing lone ESC
	} {
		if got := StripEscapes(tc.in); got != tc.want {
			t.Errorf("StripEscapes(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestPrintTableStripsEscapes proves an agent-controlled cell (a Session
// title, a file name) cannot smuggle terminal control into the rendered
// table, and that the stripping does not disturb display-width alignment:
// everything removed is zero-width, so the visible cell keeps its padding.
func TestPrintTableStripsEscapes(t *testing.T) {
	var buf strings.Builder
	err := PrintTable(&buf, []string{"ID", "TITLE"}, [][]string{
		{"sess_1", "weekly\x1b]52;c;aGVsbG8=\a report"}, // OSC 52 inside the title
		{"sess_2", "\x1b[1;31mred\x1b[0m"},              // CSI color inside the title
		{"sess_3", "界面"},                                // CJK keeps its two-cell width
	})
	if err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if strings.ContainsRune(got, '\x1b') || strings.ContainsRune(got, '\a') {
		t.Fatalf("table output contains a control character:\n%q", got)
	}
	for _, want := range []string{"weekly report", "red", "界面"} {
		if !strings.Contains(got, want) {
			t.Errorf("table output missing visible cell %q:\n%s", want, got)
		}
	}
	// Alignment is computed on the stripped (visible) form: the "TITLE"
	// column starts at the same byte offset on every row.
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("table has %d lines, want 4:\n%s", len(lines), got)
	}
	col := strings.Index(lines[0], "TITLE")
	for i, l := range lines[1:] {
		if !strings.HasPrefix(l[col:], []string{"weekly", "red", "界面"}[i]) {
			t.Errorf("row %d: TITLE column misaligned at byte %d: %q", i, col, l)
		}
	}
}

// TestSanitizeCell documents the cell contract: tab/newline rendered as
// spaces, escape sequences and other controls stripped.
func TestSanitizeCell(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"a\tb", "a b"},
		{"a\nb", "a b"},
		{"a\x1b[2Jb", "ab"},
		{"clean", "clean"},
	} {
		if got := sanitizeCell(tc.in); got != tc.want {
			t.Errorf("sanitizeCell(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
