package tools

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// Samples mirror the platform tool schemas; every top-level key must survive JSON decoding.

func assertKeysCovered(t *testing.T, sample string, v any) {
	t.Helper()
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(sample), &obj); err != nil {
		t.Fatalf("sample is not valid JSON: %v", err)
	}
	tags := map[string]bool{}
	rt := reflect.TypeOf(v).Elem()
	for f := range rt.Fields() {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name != "" {
			tags[name] = true
		}
	}
	for k := range obj {
		if !tags[k] {
			t.Errorf("sample key %q has no matching json tag in %T", k, v)
		}
	}
}

func TestCompatReadFile(t *testing.T) {
	sample := `{"file_path":"/tmp/a.txt","offset":51,"limit":50}`
	var args readFileArgs
	assertKeysCovered(t, sample, &args)
	if err := json.Unmarshal([]byte(sample), &args); err != nil {
		t.Fatal(err)
	}
	if args.FilePath != "/tmp/a.txt" || args.Offset == nil || *args.Offset != 51 ||
		args.Limit == nil || *args.Limit != 50 {
		t.Fatalf("field mismatch: %+v", args)
	}
	// Omitted offset and limit must remain nil to preserve presence semantics.
	minimal := `{"file_path":"/tmp/a.txt"}`
	var args2 readFileArgs
	if err := json.Unmarshal([]byte(minimal), &args2); err != nil {
		t.Fatal(err)
	}
	if args2.Offset != nil || args2.Limit != nil {
		t.Fatalf("offset/limit should stay nil when absent: %+v", args2)
	}
}

func TestCompatWriteFile(t *testing.T) {
	sample := `{"file_path":"/tmp/a.txt","content":"hello","append":true}`
	var args writeFileArgs
	assertKeysCovered(t, sample, &args)
	if err := json.Unmarshal([]byte(sample), &args); err != nil {
		t.Fatal(err)
	}
	if args.FilePath != "/tmp/a.txt" || args.Content != "hello" || !args.Append {
		t.Fatalf("field mismatch: %+v", args)
	}
	var args2 writeFileArgs
	if err := json.Unmarshal([]byte(`{"file_path":"/tmp/a.txt","content":"x"}`), &args2); err != nil {
		t.Fatal(err)
	}
	if args2.Append {
		t.Fatalf("append should default to false: %+v", args2)
	}
}

func TestCompatEditFile(t *testing.T) {
	sample := `{"file_path":"/tmp/a.txt","old_string":"foo","new_string":"bar","replace_all":true}`
	var args editFileArgs
	assertKeysCovered(t, sample, &args)
	if err := json.Unmarshal([]byte(sample), &args); err != nil {
		t.Fatal(err)
	}
	if args.FilePath != "/tmp/a.txt" || args.OldString != "foo" || args.NewString != "bar" ||
		!args.ReplaceAll {
		t.Fatalf("field mismatch: %+v", args)
	}
	var args2 editFileArgs
	if err := json.Unmarshal([]byte(
		`{"file_path":"/tmp/a.txt","old_string":"a","new_string":"b"}`), &args2); err != nil {
		t.Fatal(err)
	}
	if args2.ReplaceAll {
		t.Fatalf("replace_all should default to false: %+v", args2)
	}
}

func TestCompatShell(t *testing.T) {
	sample := `{"command":"ls -la","description":"Lists files in current directory"}`
	var args shellArgs
	assertKeysCovered(t, sample, &args)
	if err := json.Unmarshal([]byte(sample), &args); err != nil {
		t.Fatal(err)
	}
	if args.Command != "ls -la" || args.Description != "Lists files in current directory" {
		t.Fatalf("field mismatch: %+v", args)
	}
}

func TestCompatTodoWrite(t *testing.T) {
	sample := `{"todos":[{"id":"t1","content":"write tests","status":"in_progress","priority":"high"}]}`
	var args todoWriteArgs
	assertKeysCovered(t, sample, &args)
	if err := json.Unmarshal([]byte(sample), &args); err != nil {
		t.Fatal(err)
	}
	if len(args.Todos) != 1 {
		t.Fatalf("expect 1 todo, got %d", len(args.Todos))
	}
	todo, err := parseTodo(args.Todos[0])
	if err != nil {
		t.Fatal(err)
	}
	if todo.ID != "t1" || todo.Content != "write tests" || todo.Status != "in_progress" || todo.Priority != "high" {
		t.Fatalf("field mismatch: %+v", todo)
	}
}
