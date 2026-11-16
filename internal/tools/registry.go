package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Tool names must match the platform's published schemas.
const (
	toolReadFile  = "read_file"
	toolWriteFile = "write_file"
	toolEditFile  = "edit_file"
	toolShell     = "shell"
	toolTodoRead  = "todo_read"
	toolTodoWrite = "todo_write"
)

// IsSandboxTool reports whether the worker executes name locally. Keep this
// list in sync with the platform's self-hosted tool registry; divergence hangs
// calls until tool_timeout or hides child calls from the worker.
func IsSandboxTool(name string) bool {
	switch name {
	case toolReadFile, toolWriteFile, toolEditFile, toolShell, toolTodoRead, toolTodoWrite:
		return true
	}
	return false
}

// Defaults and limits match the platform's local tool behavior.
const (
	defaultReadMaxLine  = 1000
	defaultReadLineLen  = 2000
	defaultReadTotalLen = 100_000
	// Bound whole-file reads and replacements to limit memory use.
	defaultEditMaxBytes = 100 << 20
	defaultShellMaxLen  = 10_000
	defaultShellTimeout = 4 * time.Minute
)

type executor struct {
	// Non-positive values use defaultShellTimeout.
	shellTimeout time.Duration
	// todoMu protects the only mutable state shared across concurrent calls;
	// without it, writes could interleave and reads could observe partial files.
	todoMu sync.Mutex
}

// NewExecutor returns an Executor for the six local tools.
// Non-positive timeouts use defaultShellTimeout; unknown tool names make Execute
// return a Go error. The CLI reserves one-fifth of --tool-timeout so the shell
// fails first with a deterministic error.
func NewExecutor(shellTimeout time.Duration) Executor {
	if shellTimeout <= 0 {
		shellTimeout = defaultShellTimeout
	}
	return &executor{shellTimeout: shellTimeout}
}

func (e *executor) Execute(ctx context.Context, call Call) (Result, error) {
	switch call.Name {
	case toolReadFile:
		return e.readFile(ctx, call)
	case toolWriteFile:
		return e.writeFile(ctx, call)
	case toolEditFile:
		return e.editFile(ctx, call)
	case toolShell:
		return e.shell(ctx, call)
	case toolTodoRead:
		return e.todoRead(ctx, call)
	case toolTodoWrite:
		return e.todoWrite(ctx, call)
	default:
		return Result{}, fmt.Errorf("tools: unknown tool %q", call.Name)
	}
}

func errResult(format string, args ...any) Result {
	return Result{
		Content: []ContentBlock{{Type: ContentBlockTypeText, Text: fmt.Sprintf(format, args...)}},
		IsError: true,
	}
}

func textResult(text string) Result {
	return Result{
		Content: []ContentBlock{{Type: ContentBlockTypeText, Text: text}},
	}
}

// Windows requires a drive-rooted or UNC path; /foo, \foo, and C:foo depend on
// the current drive or directory and are rejected. The second return value
// reports whether a failure result was produced.
func requireAbsPath(filePath string) (Result, bool) {
	if !filepath.IsAbs(filePath) {
		return errResult("Error: the path %s is not absolute. Please provide a valid path.", filePath), true
	}
	return Result{}, false
}

// write_file cannot use statFile because its missing-file branch must create
// parent directories.
func statFile(filePath string) (os.FileInfo, Result, bool) {
	st, err := os.Stat(filePath)
	if err != nil || st.IsDir() {
		return nil, errResult("Error: the path %s is not a file. Please provide a valid path.", filePath), true
	}
	return st, Result{}, false
}

// Model arguments require presence checks because json.Unmarshal cannot
// distinguish omitted fields from zero values. Unknown fields remain ignored
// for platform compatibility; empty input is treated as "{}".
func parseArgs(input json.RawMessage, v any, required ...string) error {
	raw := input
	if len(strings.TrimSpace(string(raw))) == 0 {
		raw = json.RawMessage("{}")
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return err
	}
	if len(required) == 0 {
		return nil
	}
	var presence map[string]json.RawMessage
	if err := json.Unmarshal(raw, &presence); err != nil {
		return err
	}
	for _, field := range required {
		if _, ok := presence[field]; !ok {
			return fmt.Errorf("missing required argument: %s", field)
		}
	}
	return nil
}

func truncateRunes(s string, n int) (string, bool) {
	r := []rune(s)
	if len(r) <= n {
		return s, false
	}
	return string(r[:n]), true
}

// Size-guard failures remain distinguishable so callers can return
// model-correctable results when files grow after Stat.
type tooLargeError struct {
	path  string
	size  int64
	limit int64
}

func (e *tooLargeError) Error() string {
	return fmt.Sprintf("file %s is too large (%d bytes, limit %d bytes)", e.path, e.size, e.limit)
}
