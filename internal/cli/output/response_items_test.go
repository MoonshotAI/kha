package output

import (
	"bytes"
	"testing"

	"encoding/json/jsontext"
)

func TestPrintResponseListTransforms(t *testing.T) {
	tests := []struct {
		name      string
		payload   string
		itemsPath string
		path      string
		raw       bool
		want      string
	}{
		{
			name:      "field path falls back to items",
			payload:   `{"items":[{"id":"a"},{"id":"b"}]}`,
			itemsPath: "items",
			path:      "id",
			want:      "[\n  \"a\",\n  \"b\"\n]\n",
		},
		{
			name:      "multipath applies to each item",
			payload:   `{"items":[{"id":"a","name":"A"},{"id":"b","name":"B"}]}`,
			itemsPath: "items",
			path:      "{id,name}",
			want:      "[\n  {\n    \"id\": \"a\",\n    \"name\": \"A\"\n  },\n  {\n    \"id\": \"b\",\n    \"name\": \"B\"\n  }\n]\n",
		},
		{
			name:      "collection path still applies to the page",
			payload:   `{"items":[{"id":"a"},{"id":"b"}]}`,
			itemsPath: "items",
			path:      "items.#.id",
			want:      "[\n  \"a\",\n  \"b\"\n]\n",
		},
		{
			name:      "aggregate field path falls back to items",
			payload:   `[{"id":"a"},{"id":"b"}]`,
			itemsPath: "@this",
			path:      "id",
			want:      "[\n  \"a\",\n  \"b\"\n]\n",
		},
		{
			name:      "coverage items path",
			payload:   `{"coverage":[{"mcp_server_url":"https://a"},{"mcp_server_url":"https://b"}]}`,
			itemsPath: "coverage",
			path:      "mcp_server_url",
			want:      "[\n  \"https://a\",\n  \"https://b\"\n]\n",
		},
		{
			name:      "nested issues items path",
			payload:   `{"report":{"issues":[{"code":"a"},{"code":"b"}]}}`,
			itemsPath: "report.issues",
			path:      "code",
			want:      "[\n  \"a\",\n  \"b\"\n]\n",
		},
		{
			name:      "raw strings from different items",
			payload:   `{"items":[{"id":"a"},{"id":"b"}]}`,
			itemsPath: "items",
			path:      "id",
			raw:       true,
			want:      "a\nb\n",
		},
		{
			name:      "raw strings from collection query",
			payload:   `[{"type":"agent.message","data":{"content":[{"type":"text","text":"hello"}]}},{"type":"user.message","data":{"content":[{"type":"text","text":"user"}]}}]`,
			itemsPath: "@this",
			path:      `#(type=="agent.message")#.data.content.#(type=="text")#.text|@flatten`,
			raw:       true,
			want:      "hello\n",
		},
		{
			name:      "mixed array stays structured",
			payload:   `{"items":[{"id":"a"},1]}`,
			itemsPath: "items",
			path:      "id",
			raw:       true,
			want:      "[\n  \"a\",\n  1\n]\n",
		},
		{
			name:      "empty raw array prints nothing",
			payload:   `{"items":[]}`,
			itemsPath: "items",
			path:      "id",
			raw:       true,
			want:      "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got bytes.Buffer
			err := PrintResponse(&got, Response{
				Model:     jsontext.Value(tc.payload),
				ItemsPath: tc.itemsPath,
			}, ResponseOptions{
				Format:    "json",
				Transform: tc.path,
				RawOutput: tc.raw,
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
