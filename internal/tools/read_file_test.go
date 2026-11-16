package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeLines(t *testing.T, name string, lines []string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadFileOK(t *testing.T) {
	p := writeLines(t, "a.txt", []string{"alpha", "beta", "gamma", "delta", "epsilon"})
	res, err := NewExecutor(0).Execute(context.Background(), mustCall(toolReadFile,
		fmt.Sprintf(`{"file_path":%q}`, p)))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %+v", res)
	}
	content := onlyText(t, res.Content)
	if !strings.HasPrefix(content, "Here's the result of running `cat -n` on "+p+":\n") {
		t.Fatalf("content missing cat -n prefix:\n%s", content)
	}
	if !strings.Contains(content, "     1\talpha") || !strings.Contains(content, "     5\tepsilon") {
		t.Fatalf("content missing numbered lines:\n%s", content)
	}
}

func TestReadFileOffsetLimit(t *testing.T) {
	p := writeLines(t, "a.txt", []string{"l1", "l2", "l3", "l4", "l5"})
	res, err := NewExecutor(0).Execute(context.Background(), mustCall(toolReadFile,
		fmt.Sprintf(`{"file_path":%q,"offset":2,"limit":2}`, p)))
	if err != nil {
		t.Fatal(err)
	}
	content := onlyText(t, res.Content)
	want := "     2\tl2\n     3\tl3"
	if !strings.Contains(content, want) {
		t.Fatalf("expect %q in %q", want, content)
	}
}

func TestReadFileDefaultLimit(t *testing.T) {
	lines := make([]string, 1500)
	for i := range lines {
		lines[i] = fmt.Sprintf("line%d", i+1)
	}
	p := writeLines(t, "big.txt", lines)
	res, err := NewExecutor(0).Execute(context.Background(), mustCall(toolReadFile,
		fmt.Sprintf(`{"file_path":%q}`, p)))
	if err != nil {
		t.Fatal(err)
	}
	content := onlyText(t, res.Content)
	if got := strings.Count(content, "\tline"); got != defaultReadMaxLine {
		t.Fatalf("expect %d numbered lines, got %d", defaultReadMaxLine, got)
	}
	if !strings.Contains(content, "  1000\tline1000") {
		t.Fatalf("last line should be line1000:\n%.200s...", content)
	}
}

func TestReadFileEmpty(t *testing.T) {
	p := writeLines(t, "empty.txt", nil)
	res, err := NewExecutor(0).Execute(context.Background(), mustCall(toolReadFile,
		fmt.Sprintf(`{"file_path":%q}`, p)))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("empty file is not an error: %+v", res)
	}
	if onlyText(t, res.Content) != readFileEmptyText {
		t.Fatalf("expect %q, got %+v", readFileEmptyText, res)
	}
}

func TestReadFileErrors(t *testing.T) {
	dir := t.TempDir()
	existing := writeLines(t, "a.txt", []string{"x"})
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"invalid json", `{`, "invalid arguments"},
		{"missing file_path", `{}`, "invalid arguments"},
		{"relative path", `{"file_path":"a.txt"}`, "not absolute"},
		{"not exist", fmt.Sprintf(`{"file_path":%q}`, filepath.Join(dir, "nope.txt")), "not a file"},
		{"directory", fmt.Sprintf(`{"file_path":%q}`, dir), "not a file"},
		{"offset zero", fmt.Sprintf(`{"file_path":%q,"offset":0}`, existing), "offset 0 is not within the range"},
		{"offset beyond", fmt.Sprintf(`{"file_path":%q,"offset":2}`, existing), "offset 2 is not within the range"},
		{"limit zero", fmt.Sprintf(`{"file_path":%q,"limit":0}`, existing), "limit 0 is not positive"},
		{"limit negative", fmt.Sprintf(`{"file_path":%q,"limit":-1}`, existing), "limit -1 is not positive"},
		{"limit too large", fmt.Sprintf(`{"file_path":%q,"limit":1001}`, existing), "greater than the max line"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := NewExecutor(0).Execute(context.Background(), mustCall(toolReadFile, tc.input))
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

func TestReadFileLineTruncation(t *testing.T) {
	longLine := strings.Repeat("a", defaultReadLineLen+500)
	p := writeLines(t, "long.txt", []string{longLine})
	res, err := NewExecutor(0).Execute(context.Background(), mustCall(toolReadFile,
		fmt.Sprintf(`{"file_path":%q}`, p)))
	if err != nil {
		t.Fatal(err)
	}
	content := onlyText(t, res.Content)
	if !strings.Contains(content, "<response clipped><NOTE>Lines longer than") {
		t.Fatalf("expect line truncation NOTE:\n%.120s...", content)
	}
	if strings.Contains(content, longLine) {
		t.Fatal("line should be truncated")
	}
}

func TestReadFileTotalTruncation(t *testing.T) {
	lines := make([]string, 60)
	for i := range lines {
		lines[i] = strings.Repeat("b", defaultReadLineLen)
	}
	p := writeLines(t, "total.txt", lines)
	res, err := NewExecutor(0).Execute(context.Background(), mustCall(toolReadFile,
		fmt.Sprintf(`{"file_path":%q}`, p)))
	if err != nil {
		t.Fatal(err)
	}
	content := onlyText(t, res.Content)
	if !strings.Contains(content, "Total length longer than") {
		t.Fatalf("expect total truncation NOTE:\n%.120s...", content)
	}
}

func TestReadFileMediaDegraded(t *testing.T) {
	p := filepath.Join(t.TempDir(), "pic.png")
	if err := os.WriteFile(p, []byte("fake-png"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := NewExecutor(0).Execute(context.Background(), mustCall(toolReadFile,
		fmt.Sprintf(`{"file_path":%q}`, p)))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatalf("expect IsError for media file, got %+v", res)
	}
	if text := onlyText(t, res.Content); !strings.Contains(text, "image files is not supported") {
		t.Fatalf("expect media degradation message, got %q", text)
	}
}

func TestReadFileBinaryDegraded(t *testing.T) {
	p := filepath.Join(t.TempDir(), "data.bin")
	if err := os.WriteFile(p, []byte{0xff, 0xfe, 0x00, 0x01}, 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := NewExecutor(0).Execute(context.Background(), mustCall(toolReadFile,
		fmt.Sprintf(`{"file_path":%q}`, p)))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatalf("expect IsError for binary file, got %+v", res)
	}
	if text := onlyText(t, res.Content); !strings.Contains(text, "not a text file") {
		t.Fatalf("expect binary degradation message, got %q", text)
	}
}

func TestReadFileCtxCancel(t *testing.T) {
	p := writeLines(t, "a.txt", []string{"x"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewExecutor(0).Execute(ctx, mustCall(toolReadFile,
		fmt.Sprintf(`{"file_path":%q}`, p)))
	if err == nil {
		t.Fatal("ctx cancel is a framework error, expect Go error")
	}
}
