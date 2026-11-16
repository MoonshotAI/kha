package tools

// Todo files are session-scoped under Workdir: shared /tmp would mix sessions
// and is not an absolute path on Windows.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const todoDirName = ".todo"

const todoDefaultOwner = "default"

// Todo is a session task. All fields are required, and declaration order
// preserves the platform's JSON field order.
type Todo struct {
	ID       string `json:"id"`
	Content  string `json:"content"`
	Status   string `json:"status"`
	Priority string `json:"priority"`
}

var (
	todoStatuses   = []string{"pending", "in_progress", "completed"}
	todoPriorities = []string{"high", "medium", "low"}
)

// Session IDs are sanitized to prevent path injection and invalid filename characters.
func todoFilePath(call Call) string {
	base := call.Workdir
	if base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, todoDirName, sanitizeOwner(call.SessionID)+".jsonl")
}

func sanitizeOwner(s string) string {
	if s == "" {
		return todoDefaultOwner
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, s)
}

// Field-specific errors let todo_read skip malformed lines and todo_write
// return correctable feedback to the model.
func parseTodo(data []byte) (Todo, error) {
	// Pointer fields distinguish missing fields (an error) from empty strings
	// (valid).
	var raw struct {
		ID       *string `json:"id"`
		Content  *string `json:"content"`
		Status   *string `json:"status"`
		Priority *string `json:"priority"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return Todo{}, err
	}
	for field, v := range map[string]*string{
		"id": raw.ID, "content": raw.Content, "status": raw.Status, "priority": raw.Priority,
	} {
		if v == nil {
			return Todo{}, fmt.Errorf("missing required field %q", field)
		}
	}
	if !slices.Contains(todoStatuses, *raw.Status) {
		return Todo{}, fmt.Errorf("invalid status %q: must be one of %s",
			*raw.Status, strings.Join(todoStatuses, ", "))
	}
	if !slices.Contains(todoPriorities, *raw.Priority) {
		return Todo{}, fmt.Errorf("invalid priority %q: must be one of %s",
			*raw.Priority, strings.Join(todoPriorities, ", "))
	}
	return Todo{ID: *raw.ID, Content: *raw.Content, Status: *raw.Status, Priority: *raw.Priority}, nil
}

func upsertTodo(todos []Todo, index map[string]int, t Todo) []Todo {
	if pos, ok := index[t.ID]; ok {
		todos[pos] = t
		return todos
	}
	index[t.ID] = len(todos)
	return append(todos, t)
}

// Duplicate IDs are last-wins while retaining their first position; malformed
// lines are skipped so one bad entry does not hide the remaining todos.
func loadTodos(ctx context.Context, filePath string) ([]Todo, error) {
	data, err := os.ReadFile(filePath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var todos []Todo
	index := map[string]int{}
	for i, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		t, err := parseTodo([]byte(line))
		if err != nil {
			slog.WarnContext(ctx, "kha/tools: skip invalid todo line",
				"file", filePath, "line", i+1, "err", err)
			continue
		}
		todos = upsertTodo(todos, index, t)
	}
	return todos, nil
}

// Atomic replacement prevents readers outside todoMu from observing partial
// writes. Directories use 0700 and files 0600 because session todos must remain
// private on shared hosts.
func saveTodos(filePath string, todos []Todo) error {
	lines := make([]string, 0, len(todos))
	for _, t := range todos {
		line, err := marshalNoEscape(t)
		if err != nil {
			return err
		}
		lines = append(lines, line)
	}
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".todo-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // Clean up fragments on failure; no-op after rename.
	if _, err := tmp.WriteString(strings.Join(lines, "\n")); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, filePath)
}

func marshalNoEscape(v any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}
