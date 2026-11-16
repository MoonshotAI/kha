package output

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestPrintTable(t *testing.T) {
	var buf bytes.Buffer
	err := PrintTable(&buf, []string{"ID", "NAME", "CREATED"}, [][]string{
		{"agent_1", "demo", "2026-08-01 10:00:00"},
		{"agent_2", "-", "-"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	for _, want := range []string{"ID", "NAME", "CREATED", "agent_1", "demo", "agent_2"} {
		if !strings.Contains(got, want) {
			t.Errorf("table output missing %q:\n%s", want, got)
		}
	}
	if lines := strings.Split(strings.TrimRight(got, "\n"), "\n"); len(lines) != 3 {
		t.Fatalf("table has %d lines, want 3 (header + 2 rows):\n%s", len(lines), got)
	}
}

func TestPrintTableEmptyStillPrintsHeader(t *testing.T) {
	var buf bytes.Buffer
	if err := PrintTable(&buf, []string{"ID", "NAME"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); !strings.Contains(got, "ID") || !strings.Contains(got, "NAME") {
		t.Fatalf("empty table lost its header: %q", got)
	}
}

func TestPrintJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := PrintJSON(&buf, map[string]string{"id": "agent_1"}); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if !strings.Contains(got, "\n  \"id\": \"agent_1\"\n") {
		t.Fatalf("PrintJSON not indented with two spaces: %q", got)
	}
}

// TestArrayStreamMatchesPrintJSON locks the byte-identity contract: an
// ArrayStream fed item by item produces exactly what PrintJSON prints for
// the collected slice — empty (the "[]" of an empty slice), one item, and
// several items with nested arrays and objects. The nested object is a
// struct, not a map: encoding/json/v2 emits map keys in randomized
// iteration order, which would make the byte comparison a coin flip.
func TestArrayStreamMatchesPrintJSON(t *testing.T) {
	type meta struct {
		Bytes  int   `json:"bytes"`
		Nested []int `json:"nested"`
	}
	type item struct {
		ID   string   `json:"id"`
		Tags []string `json:"tags,omitempty"`
		Meta any      `json:"meta,omitempty"`
	}
	cases := map[string][]item{
		"empty": nil,
		"one":   {{ID: "file_1"}},
		"many": {
			{ID: "file_1", Tags: []string{"x", "y"}},
			{ID: "file_2", Meta: meta{Bytes: 7, Nested: []int{1, 2}}},
			{ID: "file_3"},
		},
	}
	for name, items := range cases {
		t.Run(name, func(t *testing.T) {
			var want bytes.Buffer
			if err := PrintJSON(&want, items); err != nil {
				t.Fatal(err)
			}
			var got bytes.Buffer
			stream := NewArrayStream(&got)
			for _, it := range items {
				if err := stream.Item(it); err != nil {
					t.Fatal(err)
				}
			}
			if err := stream.Close(); err != nil {
				t.Fatal(err)
			}
			if got.String() != want.String() {
				t.Errorf("streamed array differs from PrintJSON:\nstreamed:\n%s\nbuffered:\n%s", got.String(), want.String())
			}
		})
	}
}

func TestCellHelpers(t *testing.T) {
	if Dash("") != "-" || Dash("x") != "x" {
		t.Fatal("Dash broken")
	}
	if Time(time.Time{}) != "-" {
		t.Fatal("Time(zero) != -")
	}
	ts := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	if got := Time(ts); !strings.HasPrefix(got, "2026-") {
		t.Fatalf("Time() = %q", got)
	}
	for n, want := range map[int64]string{512: "512 B", 2048: "2.0 KB", 5 << 20: "5.0 MB"} {
		if got := Bytes(n); got != want {
			t.Errorf("Bytes(%d) = %q, want %q", n, got, want)
		}
	}
}

// TestPrintTableTruncatesToFit covers the width cap: a table whose natural
// width exceeds maxTableWidth has its widest columns shrunk so every printed
// line fits, with over-long cells cut by an ellipsis (agent-generated titles
// reach thousands of characters); narrow cells and tables are untouched.
func TestPrintTableTruncatesToFit(t *testing.T) {
	long := strings.Repeat("长标题", 100) // 600 display cells
	var buf bytes.Buffer
	err := PrintTable(&buf, []string{"ID", "TITLE", "CREATED"}, [][]string{
		{"sesn_1", long, "2026-08-01 10:00:00"},
		{"sesn_2", "short", "2026-08-02 10:00:00"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	for line := range strings.Lines(got) {
		line = strings.TrimRight(line, "\n")
		if w := DisplayWidth(line); w > maxTableWidth {
			t.Errorf("line is %d cells wide (> %d): %q", w, maxTableWidth, line)
		}
	}
	if !strings.Contains(got, "…") {
		t.Errorf("long cell was not ellipsized:\n%s", got)
	}
	if !strings.Contains(got, "short") || !strings.Contains(got, "sesn_1") {
		t.Errorf("short cells must not be truncated:\n%s", got)
	}
}

func TestTruncateToWidth(t *testing.T) {
	if got := truncateToWidth("hello", 10); got != "hello" {
		t.Errorf("no-op case = %q", got)
	}
	if got := truncateToWidth("hello world", 8); got != "hello w…" {
		t.Errorf("cut case = %q", got)
	}
	// CJK: two cells per rune; limit 7 → 3 runes (6 cells) + ellipsis.
	if got := truncateToWidth("你好世界啊", 7); got != "你好世…" {
		t.Errorf("CJK cut = %q", got)
	}
	if w := DisplayWidth(truncateToWidth("你好世界啊", 7)); w != 7 {
		t.Errorf("CJK truncated width = %d, want 7", w)
	}
	if got := truncateToWidth("abc", 1); got != "…" {
		t.Errorf("limit 1 = %q", got)
	}
}

// TestPrintTableKeepsBoundedColumnsIntact covers the natural-width priority:
// the unbounded text column bears the truncation while bounded columns (an
// ID the user copies for follow-up commands) keep every character.
func TestPrintTableKeepsBoundedColumnsIntact(t *testing.T) {
	id := "sesn_01m1jrvyjzf7sa7ckq0v80yhap"
	agent := "agent_01m1jrrctbfhc8gtjybefxvn6c"
	long := strings.Repeat("长标题", 100)
	var buf bytes.Buffer
	err := PrintTable(&buf, []string{"ID", "AGENT", "TITLE", "CREATED"}, [][]string{
		{id, agent, long, "2026-08-01 10:00:00"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	for line := range strings.Lines(got) {
		line = strings.TrimRight(line, "\n")
		if w := DisplayWidth(line); w > maxTableWidth {
			t.Errorf("line is %d cells wide (> %d): %q", w, maxTableWidth, line)
		}
	}
	if !strings.Contains(got, id) {
		t.Errorf("the ID column lost characters:\n%s", got)
	}
	if !strings.Contains(got, agent) {
		t.Errorf("the AGENT column lost characters:\n%s", got)
	}
	if !strings.Contains(got, "…") {
		t.Errorf("the long text cell was not ellipsized:\n%s", got)
	}
}
