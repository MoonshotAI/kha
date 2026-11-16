package output

import (
	"bytes"
	"testing"

	"encoding/json/jsontext"
)

var responseTestPayload = jsontext.Value(`{"items":[{"id":"agent_1","name":"demo","status":"active"},{"id":"agent_2","name":"archived","status":"archived"}],"next_page_token":"next","nullable":null}`)

func TestPrintResponseGJSON(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		format  string
		compact bool
		want    string
	}{
		{name: "nested field", path: "items.0.id", format: "json", want: "\"agent_1\"\n"},
		{name: "multipath", path: "{id:items.0.id,name:items.0.name}", format: "json", want: "{\n  \"id\": \"agent_1\",\n  \"name\": \"demo\"\n}\n"},
		{name: "array query", path: `items.#(status=="active")#.id`, format: "json", want: "[\n  \"agent_1\"\n]\n"},
		{name: "array length", path: "items.#", format: "json", want: "2\n"},
		{name: "yaml", path: "items.0", format: "yaml", want: "id: agent_1\nname: demo\nstatus: active\n"},
		{name: "compact json", path: "{id:items.0.id,name:items.0.name}", format: "json", compact: true, want: "{\"id\":\"agent_1\",\"name\":\"demo\"}\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got bytes.Buffer
			err := PrintResponse(&got, Response{Model: responseTestPayload}, ResponseOptions{
				Format:    tc.format,
				Transform: tc.path,
				stream:    tc.compact,
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

func TestPrintResponseUnmatchedPathAndNull(t *testing.T) {
	t.Run("unmatched path keeps original", func(t *testing.T) {
		var want bytes.Buffer
		if err := PrintJSON(&want, responseTestPayload); err != nil {
			t.Fatal(err)
		}
		var got bytes.Buffer
		if err := PrintResponse(&got, Response{Model: responseTestPayload}, ResponseOptions{Format: "json", Transform: "missing"}); err != nil {
			t.Fatal(err)
		}
		if got.String() != want.String() {
			t.Errorf("output = %q, want original %q", got.String(), want.String())
		}
	})
	t.Run("explicit null is a result", func(t *testing.T) {
		var got bytes.Buffer
		if err := PrintResponse(&got, Response{Model: responseTestPayload}, ResponseOptions{Format: "json", Transform: "nullable"}); err != nil {
			t.Fatal(err)
		}
		if got.String() != "null\n" {
			t.Errorf("output = %q, want null", got.String())
		}
	})
}

func TestPrintResponseRawOutput(t *testing.T) {
	tests := []struct {
		name     string
		payload  jsontext.Value
		path     string
		format   string
		document bool
		want     string
	}{
		{name: "string", payload: responseTestPayload, path: "items.0.id", format: "json", want: "agent_1\n"},
		{name: "terminal escapes", payload: jsontext.Value(`{"message":"pre\u001b]52;c;aGk=\u0007post"}`), path: "message", format: "json", want: "prepost\n"},
		{name: "number", payload: responseTestPayload, path: "items.#", format: "json", want: "2\n"},
		{name: "null", payload: responseTestPayload, path: "nullable", format: "json", want: "null\n"},
		{name: "object stays json", payload: responseTestPayload, path: "items.0", format: "json", want: "{\n  \"id\": \"agent_1\",\n  \"name\": \"demo\",\n  \"status\": \"active\"\n}\n"},
		{name: "object stays yaml document", payload: responseTestPayload, path: "items.0", format: "yaml", document: true, want: "---\nid: agent_1\nname: demo\nstatus: active\n"},
		{name: "raw string omits yaml document marker", payload: responseTestPayload, path: "items.0.id", format: "yaml", document: true, want: "agent_1\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got bytes.Buffer
			err := PrintResponse(&got, Response{Model: tc.payload}, ResponseOptions{
				Format:    tc.format,
				Transform: tc.path,
				RawOutput: true,
				stream:    tc.document,
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
