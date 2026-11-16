package output

import (
	"bytes"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestPrintYAML covers the JSON→YAML conversion: keys come out sorted
// alphabetically, scalar types survive (a number-like
// string stays quoted), large integers do not become exponents, nested
// values and sequences render, nulls are kept (the data matches -o json),
// and the output parses back.
func TestPrintYAML(t *testing.T) {
	model := struct {
		ID      string         `json:"id"`
		Version string         `json:"version"`
		Bytes   int            `json:"bytes"`
		Big     int64          `json:"big"`
		Enabled bool           `json:"enabled"`
		Ratio   float64        `json:"ratio"`
		Policy  map[string]any `json:"policy"`
		Tags    []string       `json:"tags"`
		Missing *string        `json:"missing"`
	}{
		ID: "agent_1", Version: "3", Bytes: 2048, Big: 94371840, Enabled: true, Ratio: 1.5,
		Policy: map[string]any{"mode": "fast"}, Tags: []string{"a", "b"},
	}
	var buf bytes.Buffer
	if err := PrintYAML(&buf, model); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	// Keys come out sorted alphabetically (yaml.v3 sorts plain maps).
	idx := func(sub string) int { return strings.Index(got, sub) }
	order := []string{"big:", "bytes:", "enabled:", "id:", "missing:", "policy:", "ratio:", "tags:", "version:"}
	for i := 1; i < len(order); i++ {
		if idx(order[i]) <= idx(order[i-1]) {
			t.Errorf("field %s out of order:\n%s", order[i], got)
		}
	}
	for _, want := range []string{"id: agent_1", `version: "3"`, "bytes: 2048", "big: 94371840", "enabled: true", "ratio: 1.5", "mode: fast", "- a", "missing: null"} {
		if !strings.Contains(got, want) {
			t.Errorf("yaml missing %q:\n%s", want, got)
		}
	}
	var back map[string]any
	if err := yaml.Unmarshal(buf.Bytes(), &back); err != nil {
		t.Fatalf("PrintYAML output does not parse: %v\n%s", err, got)
	}
	if back["id"] != "agent_1" || back["bytes"] != 2048 || back["enabled"] != true {
		t.Errorf("round-trip mismatch: %v", back)
	}
}

// TestSeqStreamMatchesPrintYAML locks the byte-identity contract: a
// SeqStream fed item by item produces exactly what PrintYAML prints for the
// collected slice — empty (the "[]" of an empty slice) and items with nested
// sequences and multi-line strings (the yamlNormalize path).
func TestSeqStreamMatchesPrintYAML(t *testing.T) {
	type item struct {
		ID   string   `json:"id"`
		Tags []string `json:"tags,omitempty"`
		Note string   `json:"note,omitempty"`
	}
	cases := map[string][]item{
		"empty": nil,
		"one":   {{ID: "file_1"}},
		"many": {
			{ID: "file_1", Tags: []string{"x", "y"}},
			{ID: "file_2", Note: "line one  \nline two\nline three"},
			{ID: "file_3"},
		},
	}
	for name, items := range cases {
		t.Run(name, func(t *testing.T) {
			var want bytes.Buffer
			if err := PrintYAML(&want, items); err != nil {
				t.Fatal(err)
			}
			var got bytes.Buffer
			stream := NewSeqStream(&got)
			for _, it := range items {
				if err := stream.Item(it); err != nil {
					t.Fatal(err)
				}
			}
			if err := stream.Close(); err != nil {
				t.Fatal(err)
			}
			if got.String() != want.String() {
				t.Errorf("streamed sequence differs from PrintYAML:\nstreamed:\n%s\nbuffered:\n%s", got.String(), want.String())
			}
		})
	}
}

// TestPrintYAMLMultilineStyles locks the multi-line string rendering:
// multi-line text always renders as a | literal block. A string with
// trailing-space lines loses those (invisible) tails — the normalization
// that makes yaml.v3 take the block; -o json stays byte-exact.
func TestPrintYAMLMultilineStyles(t *testing.T) {
	model := struct {
		Clean   string `json:"clean"`
		Trailer string `json:"trailer"`
	}{
		Clean:   "line one\nline two\nline three",
		Trailer: "line one  \nline two",
	}
	var buf bytes.Buffer
	if err := PrintYAML(&buf, model); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if !strings.Contains(got, "clean: |-") && !strings.Contains(got, "clean: |") {
		t.Errorf("clean multiline string not a literal block:\n%s", got)
	}
	if !strings.Contains(got, "trailer: |-") && !strings.Contains(got, "trailer: |") {
		t.Errorf("trailing-space multiline string not a literal block:\n%s", got)
	}
	var back map[string]any
	if err := yaml.Unmarshal(buf.Bytes(), &back); err != nil {
		t.Fatalf("output does not parse: %v\n%s", err, got)
	}
	if back["clean"] != model.Clean {
		t.Errorf("clean round-trip mismatch: %v", back)
	}
	if want := "line one\nline two"; back["trailer"] != want {
		t.Errorf("trailer = %q, want the line-tail-trimmed %q", back["trailer"], want)
	}
}

// TestPrintYAMLControlChars covers the control-character rule: strings with
// control characters are double-quoted with escapes, and everything parses
// back exactly.
func TestPrintYAMLControlChars(t *testing.T) {
	model := struct {
		Bell     string `json:"bell"`
		MultiCtl string `json:"multi_ctl"`
		Tab      string `json:"tab"`
	}{
		Bell:     "ding\x07dong",
		MultiCtl: "one\n two\x01three",
		Tab:      "a\tb",
	}
	var buf bytes.Buffer
	if err := PrintYAML(&buf, model); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	for _, notWant := range []string{"ding\x07dong", "two\x01three"} {
		if strings.Contains(got, notWant) {
			t.Errorf("control character emitted verbatim:\n%s", got)
		}
	}
	var back map[string]any
	if err := yaml.Unmarshal(buf.Bytes(), &back); err != nil {
		t.Fatalf("output does not parse: %v\n%s", err, got)
	}
	if back["bell"] != model.Bell || back["multi_ctl"] != model.MultiCtl || back["tab"] != model.Tab {
		t.Errorf("round-trip mismatch: %v", back)
	}
}
