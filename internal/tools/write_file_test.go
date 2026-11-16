package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteFileOK(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "dir", "a.txt")
	res, err := NewExecutor(0).Execute(context.Background(), mustCall(toolWriteFile,
		fmt.Sprintf(`{"file_path":%q,"content":"hello"}`, p)))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %+v", res)
	}
	want := fmt.Sprintf("File %s has been written successfully.", p)
	if onlyText(t, res.Content) != want {
		t.Fatalf("unexpected success text: %+v", res)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("parent dirs should be created automatically: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("expect %q, got %q", "hello", data)
	}
}

func TestWriteFileOverwriteAndAppend(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.txt")
	ex := NewExecutor(0)
	if _, err := ex.Execute(context.Background(), mustCall(toolWriteFile,
		fmt.Sprintf(`{"file_path":%q,"content":"one"}`, p))); err != nil {
		t.Fatal(err)
	}
	if _, err := ex.Execute(context.Background(), mustCall(toolWriteFile,
		fmt.Sprintf(`{"file_path":%q,"content":"two"}`, p))); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(p); string(data) != "two" {
		t.Fatalf("expect overwrite, got %q", data)
	}
	res, err := ex.Execute(context.Background(), mustCall(toolWriteFile,
		fmt.Sprintf(`{"file_path":%q,"content":"-three","append":true}`, p)))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %+v", res)
	}
	if data, _ := os.ReadFile(p); string(data) != "two-three" {
		t.Fatalf("expect append, got %q", data)
	}
}

func TestWriteFileErrors(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"invalid json", `{`, "invalid arguments"},
		{"missing content", fmt.Sprintf(`{"file_path":%q}`, filepath.Join(dir, "a.txt")), "invalid arguments"},
		{"relative path", `{"file_path":"a.txt","content":"x"}`, "not absolute"},
		{"directory", fmt.Sprintf(`{"file_path":%q,"content":"x"}`, dir), "is a directory"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := NewExecutor(0).Execute(context.Background(), mustCall(toolWriteFile, tc.input))
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

func TestWriteFileCtxCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewExecutor(0).Execute(ctx, mustCall(toolWriteFile,
		fmt.Sprintf(`{"file_path":%q,"content":"x"}`, filepath.Join(t.TempDir(), "a.txt"))))
	if err == nil {
		t.Fatal("ctx cancel is a framework error, expect Go error")
	}
}
