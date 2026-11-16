package tools

import (
	"context"
	"encoding/json"
)

// Entries remain RawMessage values so parseTodo can report entry-specific
// errors with todos[i] indexes. JSON tags match the platform schema.
type todoWriteArgs struct {
	Todos []json.RawMessage `json:"todos"`
}

// Preserve the platform's success response for compatibility.
func (e *executor) todoWrite(_ context.Context, call Call) (Result, error) {
	var args todoWriteArgs
	if err := parseArgs(call.Input, &args, "todos"); err != nil {
		return errResult("invalid arguments: %v. Please fix the JSON arguments and retry.", err), nil
	}
	todos := make([]Todo, 0, len(args.Todos))
	for i, raw := range args.Todos {
		t, err := parseTodo(raw)
		if err != nil {
			return errResult("invalid todos[%d]: %v", i, err), nil
		}
		todos = append(todos, t)
	}

	merged := make([]Todo, 0, len(todos))
	index := map[string]int{}
	for _, t := range todos {
		merged = upsertTodo(merged, index, t)
	}
	e.todoMu.Lock()
	err := saveTodos(todoFilePath(call), merged)
	e.todoMu.Unlock()
	if err != nil {
		return errResult("failed to write todos: %v. Check that the todo file location is writable, then retry.", err), nil
	}
	return textResult("Todos have been written successfully."), nil
}
