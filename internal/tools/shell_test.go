package tools

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func skipOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test uses sh-specific syntax")
	}
}

func TestShellOK(t *testing.T) {
	res, err := NewExecutor(0).Execute(context.Background(), mustCall(toolShell,
		`{"command":"echo hello","description":"Prints greeting"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %+v", res)
	}
	text := onlyText(t, res.Content)
	if strings.TrimSpace(text) != "hello" {
		t.Fatalf("expect %q, got %q", "hello", text)
	}
}

func TestShellExitCode(t *testing.T) {
	skipOnWindows(t)
	res, err := NewExecutor(0).Execute(context.Background(), mustCall(toolShell,
		`{"command":"echo out; echo err 1>&2; exit 3"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("non-zero exit is data, not IsError: %+v", res)
	}
	text := onlyText(t, res.Content)
	if !strings.HasPrefix(text, "**Exit Code: 3**") {
		t.Fatalf("expect exit code header, got %q", text)
	}
	if !strings.Contains(text, "out") || !strings.Contains(text, "err") {
		t.Fatalf("expect stdout and stderr in result, got %q", text)
	}
}

func TestShellEmptyOutput(t *testing.T) {
	skipOnWindows(t)
	res, err := NewExecutor(0).Execute(context.Background(), mustCall(toolShell, `{"command":"true"}`))
	if err != nil {
		t.Fatal(err)
	}
	if onlyText(t, res.Content) != "Command executed with empty output" {
		t.Fatalf("unexpected empty-output text: %+v", res)
	}
}

func TestShellWorkdir(t *testing.T) {
	skipOnWindows(t)
	dir := t.TempDir()
	// /tmp may be a symlink; resolve it before comparing paths.
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	res, err := NewExecutor(0).Execute(context.Background(),
		Call{Name: toolShell, Input: []byte(`{"command":"pwd"}`), Workdir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if text := onlyText(t, res.Content); strings.TrimSpace(text) != realDir {
		t.Fatalf("expect pwd=%q, got %q", realDir, text)
	}
}

func TestShellTimeout(t *testing.T) {
	skipOnWindows(t)
	ex := &executor{shellTimeout: 100 * time.Millisecond}
	res, err := ex.Execute(context.Background(), mustCall(toolShell, `{"command":"sleep 5"}`))
	if err != nil {
		t.Fatalf("timeout is model-correctable, expect Result error, got Go error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expect IsError on timeout, got %+v", res)
	}
	if text := onlyText(t, res.Content); !strings.Contains(text, "timed out after 0 seconds") {
		t.Fatalf("expect timeout message, got %q", text)
	}
}

func TestNewExecutorShellTimeout(t *testing.T) {
	skipOnWindows(t)
	res, err := NewExecutor(100*time.Millisecond).Execute(context.Background(), mustCall(toolShell, `{"command":"sleep 5"}`))
	if err != nil {
		t.Fatalf("timeout is model-correctable, expect Result error, got Go error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expect IsError on timeout, got %+v", res)
	}
	if text := onlyText(t, res.Content); !strings.Contains(text, "timed out after 0 seconds") {
		t.Fatalf("expect timeout message, got %q", text)
	}
}

func TestShellCtxCancel(t *testing.T) {
	skipOnWindows(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewExecutor(0).Execute(ctx, mustCall(toolShell, `{"command":"sleep 5"}`))
	if err == nil {
		t.Fatal("ctx cancel is a framework error, expect Go error")
	}
}

func TestShellEnvScrubbed(t *testing.T) {
	skipOnWindows(t)
	t.Setenv("LOOPY_WORK_SECRET", "topsecret-value")
	t.Setenv("KHA_SESSION_TOKEN", "session-value")
	t.Setenv("KHA_TOOLS_TEST_VISIBLE", "visible-value")
	res, err := NewExecutor(0).Execute(context.Background(), mustCall(toolShell,
		`{"command":"echo \"secret=[$LOOPY_WORK_SECRET] token=[$KHA_SESSION_TOKEN] visible=[$KHA_TOOLS_TEST_VISIBLE]\""}`))
	if err != nil {
		t.Fatal(err)
	}
	text := onlyText(t, res.Content)
	if strings.Contains(text, "topsecret-value") || strings.Contains(text, "session-value") {
		t.Fatalf("secret env leaked into subprocess: %q", text)
	}
	if !strings.Contains(text, "visible=[visible-value]") {
		t.Fatalf("normal env should be inherited, got %q", text)
	}
}

func TestShellOutputTruncation(t *testing.T) {
	skipOnWindows(t)
	res, err := NewExecutor(0).Execute(context.Background(), mustCall(toolShell,
		`{"command":"printf '%.0s-x' $(seq 10000)"}`))
	if err != nil {
		t.Fatal(err)
	}
	text := onlyText(t, res.Content)
	if !strings.Contains(text, "<response clipped><NOTE>Result is longer than") {
		t.Fatalf("expect truncation NOTE, got %.120q...", text)
	}
}

func TestShellErrors(t *testing.T) {
	res, err := NewExecutor(0).Execute(context.Background(), mustCall(toolShell, `{}`))
	if err != nil {
		t.Fatalf("missing command is model-correctable, got Go error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expect IsError, got %+v", res)
	}
	if text := onlyText(t, res.Content); !strings.Contains(text, "invalid arguments") {
		t.Fatalf("expect invalid arguments message, got %q", text)
	}
}
