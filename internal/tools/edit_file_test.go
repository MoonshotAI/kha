package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func editOKFixture(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestEditFileOK(t *testing.T) {
	p := editOKFixture(t, "hello world")
	res, err := NewExecutor(0).Execute(context.Background(), mustCall(toolEditFile,
		fmt.Sprintf(`{"file_path":%q,"old_string":"world","new_string":"gopher"}`, p)))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %+v", res)
	}
	wantContent := fmt.Sprintf("File %s has been edited successfully.", p)
	if onlyText(t, res.Content) != wantContent {
		t.Fatalf("unexpected content: %+v", res)
	}
	if data, _ := os.ReadFile(p); string(data) != "hello gopher" {
		t.Fatalf("file not edited, got %q", data)
	}
}

func TestEditFileReplaceAll(t *testing.T) {
	p := editOKFixture(t, "foo bar foo")
	res, err := NewExecutor(0).Execute(context.Background(), mustCall(toolEditFile,
		fmt.Sprintf(`{"file_path":%q,"old_string":"foo","new_string":"baz","replace_all":true}`, p)))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %+v", res)
	}
	if data, _ := os.ReadFile(p); string(data) != "baz bar baz" {
		t.Fatalf("expect replace_all, got %q", data)
	}
}

func TestEditFileErrors(t *testing.T) {
	dir := t.TempDir()
	existing := editOKFixture(t, "foo bar foo")
	binary := filepath.Join(t.TempDir(), "b.bin")
	if err := os.WriteFile(binary, []byte{0xff, 0xfe}, 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"invalid json", `{`, "invalid arguments"},
		{"missing old_string", fmt.Sprintf(`{"file_path":%q,"new_string":"y"}`, existing), "invalid arguments"},
		{"relative path", `{"file_path":"a.txt","old_string":"x","new_string":"y"}`, "not absolute"},
		{"old equals new", fmt.Sprintf(`{"file_path":%q,"old_string":"foo","new_string":"foo"}`, existing),
			"must be different"},
		{"not exist", fmt.Sprintf(`{"file_path":%q,"old_string":"x","new_string":"y"}`,
			filepath.Join(dir, "nope.txt")), "not a file"},
		{"directory", fmt.Sprintf(`{"file_path":%q,"old_string":"x","new_string":"y"}`, dir), "not a file"},
		{"binary file", fmt.Sprintf(`{"file_path":%q,"old_string":"x","new_string":"y"}`, binary),
			"not a text file"},
		{"not found", fmt.Sprintf(`{"file_path":%q,"old_string":"zzz","new_string":"y"}`, existing),
			"is not found"},
		{"multiple without replace_all",
			fmt.Sprintf(`{"file_path":%q,"old_string":"foo","new_string":"y"}`, existing),
			"is found 2 times"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := NewExecutor(0).Execute(context.Background(), mustCall(toolEditFile, tc.input))
			if err != nil {
				t.Fatalf("model-correctable failures must be Result errors, got Go error: %v", err)
			}
			if !res.IsError {
				t.Fatalf("expect IsError, got %+v", res)
			}
			if text := onlyText(t, res.Content); !strings.Contains(text, tc.want) {
				t.Fatalf("expect %q in %q", tc.want, text)
			}
		})
	}
}

func TestEditFileCtxCancel(t *testing.T) {
	p := editOKFixture(t, "hello")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewExecutor(0).Execute(ctx, mustCall(toolEditFile,
		fmt.Sprintf(`{"file_path":%q,"old_string":"hello","new_string":"bye"}`, p)))
	if err == nil {
		t.Fatal("ctx cancel is a framework error, expect Go error")
	}
}
