package output

import (
	"bytes"
	"testing"

	"encoding/json/jsontext"
)

func TestPrintResponseWithoutModifiers(t *testing.T) {
	tests := []struct {
		name     string
		response Response
		format   string
		stream   bool
		want     string
	}{
		{
			name:     "original table",
			response: Response{Headers: []string{"COL"}, Rows: [][]string{{"row"}}},
			format:   "table",
			want:     "COL\nrow\n",
		},
		{
			name:     "table footer",
			response: Response{Headers: []string{"COL"}, Rows: [][]string{{"row"}}, Footer: "CURSOR cur_1"},
			format:   "table",
			want:     "COL\nrow\nCURSOR cur_1\n",
		},
		{
			name:     "footer strips terminal escapes",
			response: Response{Headers: []string{"COL"}, Rows: [][]string{{"row"}}, Footer: "cur_\x1b]52;c;aGk=\a"},
			format:   "table",
			want:     "COL\nrow\ncur_\n",
		},
		{
			name:     "json",
			response: Response{Model: jsontext.Value(`{"a":1}`)},
			format:   "json",
			want:     "{\n  \"a\": 1\n}\n",
		},
		{
			name:     "compact json stream",
			response: Response{Model: jsontext.Value(`{"a":1}`)},
			format:   "json",
			stream:   true,
			want:     "{\"a\":1}\n",
		},
		{
			name:     "yaml",
			response: Response{Model: jsontext.Value(`{"a":1}`)},
			format:   "yaml",
			want:     "a: 1\n",
		},
		{
			name:     "yaml document stream",
			response: Response{Model: jsontext.Value(`{"a":1}`)},
			format:   "yaml",
			stream:   true,
			want:     "---\na: 1\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got bytes.Buffer
			err := PrintResponse(&got, tc.response, ResponseOptions{Format: tc.format, stream: tc.stream})
			if err != nil {
				t.Fatal(err)
			}
			if got.String() != tc.want {
				t.Errorf("output = %q, want %q", got.String(), tc.want)
			}
		})
	}
}
