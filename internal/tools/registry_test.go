package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

var _ Executor = (*executor)(nil)

func mustCall(name, input string) Call {
	return Call{Name: name, Input: json.RawMessage(input)}
}

func onlyText(t *testing.T, blocks []ContentBlock) string {
	t.Helper()
	if len(blocks) != 1 {
		t.Fatalf("expect 1 content block, got %d: %+v", len(blocks), blocks)
	}
	if blocks[0].Type != ContentBlockTypeText {
		t.Fatalf("expect text block, got type %q", blocks[0].Type)
	}
	return blocks[0].Text
}

func TestExecuteUnknownTool(t *testing.T) {
	_, err := NewExecutor(0).Execute(context.Background(), mustCall("no_such_tool", `{}`))
	if err == nil {
		t.Fatal("expect Go error for unknown tool")
	}
	if !strings.Contains(err.Error(), "no_such_tool") {
		t.Fatalf("error should name the tool, got: %v", err)
	}
}

func TestParseArgsRequiredAndUnknown(t *testing.T) {
	var args shellArgs
	if err := parseArgs(json.RawMessage(`{"command":"ls","extra_field":1}`), &args, "command"); err != nil {
		t.Fatalf("unknown field should be ignored, got: %v", err)
	}
	if args.Command != "ls" {
		t.Fatalf("command not parsed: %+v", args)
	}
	if err := parseArgs(json.RawMessage(`{}`), &args, "command"); err == nil {
		t.Fatal("expect missing required argument error")
	}
	if err := parseArgs(nil, &args, "command"); err == nil {
		t.Fatal("empty input should be treated as {} and fail required check")
	}
}
