// Package tools executes local tools for self-hosted workers.
// The platform owns the model-facing tool schemas.
package tools

import (
	"context"
	"encoding/json"
)

// Call is a model tool invocation.
type Call struct {
	// Name is one of read_file, write_file, edit_file, shell, todo_read, or todo_write.
	Name string
	// Input fields must match the platform schema for the named tool.
	Input json.RawMessage
	// Workdir is the shell cwd and roots session-isolated todo files. File tools
	// require absolute paths, matching their platform schemas.
	Workdir string
	// SessionID isolates todo storage between Sessions sharing a worker.
	// Empty selects the default file in tests.
	SessionID string
}

const (
	// ContentBlockTypeText identifies a text content block.
	ContentBlockTypeText = "text"
	// ContentBlockTypeError identifies an error content block.
	ContentBlockTypeError = "error"
)

// ContentBlock is one content block in a tool result.
type ContentBlock struct {
	Type string
	// Text contains the text content when Type is text or error.
	Text string
}

// Result is the outcome of a tool execution.
type Result struct {
	Content []ContentBlock
	// IsError marks failures the model can correct itself, such as missing files,
	// invalid arguments, and command timeouts. The reason is in the Content text.
	// Execute returns a Go error only for framework-level failures such as ctx
	// cancellation.
	IsError bool
}

// Executor executes a call by tool name. Implementations must support
// cooperative cancellation through ctx, terminating in-flight file operations
// and child processes when canceled.
type Executor interface {
	Execute(ctx context.Context, call Call) (Result, error)
}
