package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func todoCall(name, input, workdir, sessionID string) Call {
	return Call{Name: name, Input: []byte(input), Workdir: workdir, SessionID: sessionID}
}

func allText(t *testing.T, blocks []ContentBlock) string {
	t.Helper()
	var sb strings.Builder
	for _, b := range blocks {
		if b.Type != ContentBlockTypeText {
			t.Fatalf("expect text block, got type %q", b.Type)
		}
		sb.WriteString(b.Text)
	}
	return sb.String()
}

func TestTodoReadEmpty(t *testing.T) {
	dir := t.TempDir()
	res, err := NewExecutor(0).Execute(context.Background(), todoCall(toolTodoRead, ``, dir, "sesn_1"))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("expect success, got: %+v", res)
	}
	if got := onlyText(t, res.Content); got != "No todos found." {
		t.Fatalf("unexpected text: %q", got)
	}
}

func TestTodoWriteThenReadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	ex := NewExecutor(0)
	write := todoCall(toolTodoWrite,
		`{"todos":[{"id":"t1","content":"write tests","status":"in_progress","priority":"high"},`+
			`{"id":"t2","content":"run tests","status":"pending","priority":"low"}]}`, dir, "sesn_1")
	res, err := ex.Execute(context.Background(), write)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("write failed: %+v", res)
	}
	if got := onlyText(t, res.Content); got != "Todos have been written successfully." {
		t.Fatalf("unexpected text: %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, todoDirName, "sesn_1.jsonl")); err != nil {
		t.Fatalf("todo file not where expected: %v", err)
	}

	res, err = ex.Execute(context.Background(), todoCall(toolTodoRead, ``, dir, "sesn_1"))
	if err != nil {
		t.Fatal(err)
	}
	got := allText(t, res.Content)
	for _, want := range []string{"# Current todos", "t1", "write tests", "in_progress", "high", "t2", "run tests", "pending", "low"} {
		if !strings.Contains(got, want) {
			t.Fatalf("read output missing %q: %q", want, got)
		}
	}
}

func TestTodoWriteUpsertByIDKeepsFirstPosition(t *testing.T) {
	dir := t.TempDir()
	ex := NewExecutor(0)
	call := todoCall(toolTodoWrite,
		`{"todos":[{"id":"t1","content":"first","status":"pending","priority":"low"},`+
			`{"id":"t2","content":"second","status":"pending","priority":"low"},`+
			`{"id":"t1","content":"first-updated","status":"completed","priority":"high"}]}`, dir, "sesn_1")
	if _, err := ex.Execute(context.Background(), call); err != nil {
		t.Fatal(err)
	}
	todos, err := loadTodos(context.Background(), todoFilePath(todoCall(toolTodoRead, ``, dir, "sesn_1")))
	if err != nil {
		t.Fatal(err)
	}
	if len(todos) != 2 {
		t.Fatalf("expect 2 todos after upsert, got %d: %+v", len(todos), todos)
	}
	if todos[0].ID != "t1" || todos[0].Content != "first-updated" || todos[0].Status != "completed" {
		t.Fatalf("t1 should be updated in first position: %+v", todos[0])
	}
	if todos[1].ID != "t2" {
		t.Fatalf("t2 should keep second position: %+v", todos[1])
	}
}

func TestTodoWriteValidation(t *testing.T) {
	dir := t.TempDir()
	ex := NewExecutor(0)
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"missing todos", `{}`, "missing required argument"},
		{"missing field", `{"todos":[{"id":"t1","content":"x","status":"pending"}]}`, `invalid todos[0]: missing required field "priority"`},
		{"bad status", `{"todos":[{"id":"t1","content":"x","status":"doing","priority":"low"}]}`, `invalid todos[0]: invalid status "doing"`},
		{"bad priority", `{"todos":[{"id":"t1","content":"x","status":"pending","priority":"p0"}]}`, `invalid todos[0]: invalid priority "p0"`},
		{"bad json", `{"todos":`, "invalid arguments"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := ex.Execute(context.Background(), todoCall(toolTodoWrite, tc.input, dir, "sesn_1"))
			if err != nil {
				t.Fatal(err)
			}
			if !res.IsError {
				t.Fatalf("expect IsError, got: %+v", res)
			}
			if got := res.Content[0].Text; !strings.Contains(got, tc.want) {
				t.Fatalf("error %q should contain %q", got, tc.want)
			}
		})
	}
}

func TestTodoReadSkipsInvalidLines(t *testing.T) {
	dir := t.TempDir()
	path := todoFilePath(todoCall(toolTodoRead, ``, dir, "sesn_1"))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	content := `{"id":"t1","content":"good","status":"pending","priority":"low"}` + "\n" +
		`{"id":"bad"}` + "\n" +
		`not json` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := NewExecutor(0).Execute(context.Background(), todoCall(toolTodoRead, ``, dir, "sesn_1"))
	if err != nil {
		t.Fatal(err)
	}
	got := allText(t, res.Content)
	if !strings.Contains(got, "t1") || strings.Contains(got, `"bad"`) {
		t.Fatalf("bad lines should be skipped: %q", got)
	}
}

func TestTodoSessionIsolation(t *testing.T) {
	dir := t.TempDir()
	ex := NewExecutor(0)
	write := todoCall(toolTodoWrite,
		`{"todos":[{"id":"t1","content":"from session A","status":"pending","priority":"low"}]}`,
		dir, "sesn_A")
	if _, err := ex.Execute(context.Background(), write); err != nil {
		t.Fatal(err)
	}
	res, err := ex.Execute(context.Background(), todoCall(toolTodoRead, ``, dir, "sesn_B"))
	if err != nil {
		t.Fatal(err)
	}
	if got := onlyText(t, res.Content); got != "No todos found." {
		t.Fatalf("session B must not see session A todos: %q", got)
	}
	// Sanitization must keep path-like session IDs inside the todo directory.
	evil := todoFilePath(todoCall(toolTodoRead, ``, dir, "../sesn_evil"))
	if !strings.HasPrefix(evil, filepath.Join(dir, todoDirName)+string(filepath.Separator)) {
		t.Fatalf("sanitized owner escaped todo dir: %q", evil)
	}
	res, err = ex.Execute(context.Background(), todoCall(toolTodoRead, ``, dir, "../sesn_evil"))
	if err != nil {
		t.Fatal(err)
	}
	if got := onlyText(t, res.Content); got != "No todos found." {
		t.Fatalf("evil session should read its own empty file: %q", got)
	}
}

func TestTodoDefaultOwnerWithoutSession(t *testing.T) {
	dir := t.TempDir()
	ex := NewExecutor(0)
	write := todoCall(toolTodoWrite,
		`{"todos":[{"id":"t1","content":"x","status":"pending","priority":"low"}]}`, dir, "")
	if _, err := ex.Execute(context.Background(), write); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, todoDirName, "default.jsonl")); err != nil {
		t.Fatalf("default owner file missing: %v", err)
	}
}

func TestTodoEmptyWorkdirFallsBackTempDir(t *testing.T) {
	ex := NewExecutor(0)
	call := Call{Name: toolTodoRead, Input: []byte(``), SessionID: "sesn_tmp_fallback"}
	res, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("expect success reading empty todo: %+v", res)
	}
}

func TestTodoConcurrentWritesNeverTorn(t *testing.T) {
	dir := t.TempDir()
	ex := NewExecutor(0)
	const writers, rounds = 16, 8
	var wg sync.WaitGroup
	for w := range writers {
		wg.Go(func() {
			id := fmt.Sprintf("w%02d", w)
			for range rounds {
				input := fmt.Sprintf(
					`{"todos":[{"id":%q,"content":"task from %s","status":"pending","priority":"low"}]}`, id, id)
				res, err := ex.Execute(context.Background(), todoCall(toolTodoWrite, input, dir, "sesn_race"))
				if err != nil {
					t.Errorf("write: %v", err)
					return
				}
				if res.IsError {
					t.Errorf("write IsError: %+v", res)
					return
				}
				// Reads must tolerate concurrent replacement without returning a Go error.
				if _, err := ex.Execute(context.Background(), todoCall(toolTodoRead, ``, dir, "sesn_race")); err != nil {
					t.Errorf("read: %v", err)
					return
				}
			}
		})
	}
	wg.Wait()
	todos, err := loadTodos(context.Background(), todoFilePath(todoCall(toolTodoRead, ``, dir, "sesn_race")))
	if err != nil {
		t.Fatalf("final load: %v", err)
	}
	if len(todos) != 1 || !strings.HasPrefix(todos[0].ID, "w") || len(todos[0].ID) != 3 {
		t.Fatalf("torn write: final todos = %+v", todos)
	}
}
