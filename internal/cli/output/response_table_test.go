package output

import (
	"bytes"
	"testing"

	"encoding/json/jsontext"
)

func TestPrintResponseTransformedTable(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    string
	}{
		{name: "string", payload: `"agent_1"`, want: "VALUE\nagent_1\n"},
		{name: "null", payload: `null`, want: "VALUE\nnull\n"},
		{name: "large number", payload: `9007199254740993`, want: "VALUE\n9007199254740993\n"},
		{name: "object", payload: `{"id":"agent_1","name":"demo"}`, want: "id       name\nagent_1  demo\n"},
		{name: "object key case", payload: `{"id":"a","ID":"b"}`, want: "id  ID\na   b\n"},
		{name: "object array union", payload: `[{"id":"a"},{"name":"b","id":"c"}]`, want: "id  name\na\nc   b\n"},
		{name: "scalar array", payload: `["a","b"]`, want: "VALUE\na\nb\n"},
		{name: "mixed array", payload: `[{"id":"a"},"b"]`, want: "VALUE\n{\"id\":\"a\"}\nb\n"},
		{name: "nested array", payload: `[[1,2],[3]]`, want: "VALUE\n[1,2]\n[3]\n"},
		{name: "empty object", payload: `{}`, want: "VALUE\n{}\n"},
		{name: "empty array", payload: `[]`, want: "VALUE\n[]\n"},
		{name: "empty objects", payload: `[{},{}]`, want: "VALUE\n{}\n{}\n"},
		{name: "nested compact json", payload: `{"meta":{"tags":["a","b"]}}`, want: "meta\n{\"tags\":[\"a\",\"b\"]}\n"},
		{name: "dotted key", payload: `{"a.b":"x","a":{"b":"y"}}`, want: "a.b  a\nx    {\"b\":\"y\"}\n"},
		{name: "object null", payload: `{"value":null}`, want: "value\nnull\n"},
		{name: "terminal escapes", payload: `{"name":"red\u001b[31m"}`, want: "name\nred\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got bytes.Buffer
			err := PrintResponse(&got, Response{Model: jsontext.Value(tc.payload)}, ResponseOptions{
				Format:    "table",
				Transform: "@this",
			})
			if err != nil {
				t.Fatal(err)
			}
			if got.String() != tc.want {
				t.Errorf("output = %q, want %q", got.String(), tc.want)
			}
		})
	}
}
