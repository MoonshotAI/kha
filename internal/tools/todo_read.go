package tools

import (
	"context"
	"fmt"
	"strings"
)

// todoRead matches the platform's text-block output. Display is platform-only:
// user.tool_result carries text blocks, so no JSON array is emitted.
func (e *executor) todoRead(ctx context.Context, call Call) (Result, error) {
	// The schema asks for empty input, but tolerate whitespace and ignore unknown
	// JSON fields; malformed JSON remains a model-correctable error.
	if strings.TrimSpace(string(call.Input)) != "" {
		var args struct{}
		if err := parseArgs(call.Input, &args); err != nil {
			return errResult("invalid arguments: %v. Please fix the JSON arguments and retry.", err), nil
		}
	}
	filePath := todoFilePath(call)
	e.todoMu.Lock()
	todos, err := loadTodos(ctx, filePath)
	e.todoMu.Unlock()
	if err != nil {
		return errResult("failed to read todos: %v. Check that the todo file is readable, then retry.", err), nil
	}
	if len(todos) == 0 {
		return textResult("No todos found."), nil
	}
	blocks := []ContentBlock{{Type: ContentBlockTypeText, Text: "# Current todos\n"}}
	for _, t := range todos {
		blocks = append(blocks, ContentBlock{
			Type: ContentBlockTypeText,
			Text: fmt.Sprintf("- %s\n  - status: %s\n  - priority: %s\n  - content: %s\n",
				t.ID, t.Status, t.Priority, t.Content),
		})
	}
	return Result{Content: blocks}, nil
}
