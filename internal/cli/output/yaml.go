package output

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"strings"

	json "encoding/json/v2"

	"gopkg.in/yaml.v3"
)

// PrintYAML writes v as YAML: the same data as PrintJSON, with mapping keys
// sorted alphabetically (yaml.v3 sorts plain maps that way). The JSON
// encoding defines the values; yaml.v3 picks
// scalar styles, and multi-line strings come out as | literal blocks.
//
// Two normalizations happen here, so -o yaml is the human view while
// -o json stays byte-exact: integral JSON numbers print as integers (never
// 9.437184e+07), and multi-line strings lose line-tail whitespace (yaml.v3
// refuses block style for such lines, producing an unreadable double-quoted
// megastring for agent prompts; the tails are invisible either way).
//
// ponytail: an ordered jsontext→yaml.Node converter existed and was deleted
// as over-engineered; alphabetical keys won. Restore it if spec-order keys
// ever matter.
func PrintYAML(w io.Writer, v any) error {
	return encodeYAML(w, v)
}

// encodeYAML is PrintYAML's body, shared with SeqStream's per-item encoding.
func encodeYAML(w io.Writer, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encoding result as JSON: %w", err)
	}
	var parsed any
	if err := json.Unmarshal(data, &parsed); err != nil {
		return fmt.Errorf("converting result to YAML: %w", err)
	}
	enc := yaml.NewEncoder(w)
	enc.SetIndent(2)
	if err := enc.Encode(yamlNormalize(parsed)); err != nil {
		return fmt.Errorf("encoding result as YAML: %w", err)
	}
	return enc.Close()
}

// SeqStream writes one YAML block sequence whose items arrive one at a time
// (a draining --all listing): each item is encoded through the same path as
// PrintYAML, its first line prefixed with "- " and the rest with two spaces.
// The bytes are identical to PrintYAML on the collected slice; with no items
// the output is the "[]" PrintYAML prints for an empty slice. A stream
// abandoned before Close (an error stopped the feed) keeps the items already
// written — the partial sequence a failed feed leaves behind.
type SeqStream struct {
	w       io.Writer
	started bool
}

// NewSeqStream returns a SeqStream writing to w.
func NewSeqStream(w io.Writer) *SeqStream {
	return &SeqStream{w: w}
}

// Item appends v as the next sequence entry.
func (s *SeqStream) Item(v any) error {
	var buf bytes.Buffer
	if err := encodeYAML(&buf, v); err != nil {
		return err
	}
	s.started = true
	for i, line := range strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n") {
		prefix := "  "
		if i == 0 {
			prefix = "- "
		}
		if _, err := io.WriteString(s.w, prefix+line+"\n"); err != nil {
			return err
		}
	}
	return nil
}

// Close finishes the sequence. With no Item calls it writes the empty
// sequence PrintYAML would print for an empty slice.
func (s *SeqStream) Close() error {
	if !s.started {
		_, err := io.WriteString(s.w, "[]\n")
		return err
	}
	return nil
}

// yamlNormalize prepares the decoded JSON tree for yaml.v3: integral
// float64 values (JSON numbers all decode as float64) become int64 so large
// integers print as 94371840 instead of 9.437184e+07 (1e15 is far past any
// value this API carries), and multi-line strings are trimmed of line-tail
// whitespace so they always take the | literal block.
func yamlNormalize(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			t[k] = yamlNormalize(x)
		}
		return t
	case []any:
		for i, x := range t {
			t[i] = yamlNormalize(x)
		}
		return t
	case float64:
		if t == math.Trunc(t) && math.Abs(t) < 1e15 {
			return int64(t)
		}
		return t
	case string:
		if strings.Contains(t, "\n") {
			lines := strings.Split(t, "\n")
			for i, line := range lines {
				lines[i] = strings.TrimRight(line, " \t")
			}
			return strings.Join(lines, "\n")
		}
		return t
	default:
		return v
	}
}
