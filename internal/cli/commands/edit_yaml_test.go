package commands

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/MoonshotAI/kha"

	"gopkg.in/yaml.v3"
)

func TestEditManifestMultilinePreservesContent(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{name: "trailing spaces and newline", content: "line one  \nline two\n"},
		{name: "no final newline", content: "line one  \nline two"},
		{name: "multiple final newlines", content: "line one\nline two\n\n"},
		{name: "carriage returns", content: "line one\r\nline two\r\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			current := &kha.Agent{Model: *kha.NewModelRefString("kimi-k3.1-pro"), System: new(tt.content)}
			data, err := editManifest(current, &editSpec[kha.Agent, kha.UpdateAgentRequest]{})
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]any
			if err := yaml.Unmarshal(data, &fields); err != nil {
				t.Fatalf("manifest does not parse: %v\n%s", err, data)
			}
			if got := fields["system"]; got != tt.content {
				t.Errorf("system = %q, want original content %q\nmanifest:\n%s", got, tt.content, data)
			}
		})
	}
}

func TestAgentsEditUnrelatedFieldPreservesMultilineSystem(t *testing.T) {
	isolateEnv(t)
	fakeEditor(t, `sed 's/^name: demo$/name: renamed/' "$1" > "$1.tmp" && mv "$1.tmp" "$1"`)
	rec := &editRecorder{}
	const system = "line one  \nline two\n"
	srv := serve(t, rec.wrap(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/agents/agent_1" {
			_, _ = fmt.Fprint(w, `{"id":"agent_1","name":"demo","model":"kimi-k3.1-pro","version":"3","system":"line one  \nline two\n","created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-02T10:00:00Z","scope":"project"}`)
			return
		}
		_, _ = fmt.Fprint(w, renamedAgent)
	}))

	_, _, err := runCmd(t, "", "agents", "edit", "agent_1", "--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if rec.updates != 1 {
		t.Fatalf("updates = %d, want 1", rec.updates)
	}
	body := rec.body(t, 0)
	if body["name"] != "renamed" {
		t.Errorf("update name = %v, want renamed", body["name"])
	}
	if got := body["system"]; got != system {
		t.Errorf("update system = %q, want original content %q", got, system)
	}
}

func TestMemoriesEditUnrelatedFieldPreservesMultilineContent(t *testing.T) {
	isolateEnv(t)
	fakeEditor(t, `sed 's|^path: notes/todo.md$|path: notes/renamed.md|' "$1" > "$1.tmp" && mv "$1.tmp" "$1"`)
	rec := &editRecorder{}
	const content = "bGluZSBvbmUgIApsaW5lIHR3bwo="
	srv := serve(t, rec.wrap(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/memory-stores/mstr_1/memories/mem_1" {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodGet {
			_, _ = fmt.Fprint(w, `{"id":"mem_1","memory_store_id":"mstr_1","path":"notes/todo.md","content":"bGluZSBvbmUgIApsaW5lIHR3bwo=","content_sha256":"hash","content_size_bytes":20,"created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-01T10:00:00Z"}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"id":"mem_1","memory_store_id":"mstr_1","path":"notes/renamed.md","content":"bGluZSBvbmUgIApsaW5lIHR3bwo=","content_sha256":"hash","content_size_bytes":20,"created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-02T10:00:00Z"}`)
	}))

	_, _, err := runCmd(t, "", "memory-stores", "memories", "edit", "mstr_1", "mem_1", "--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if rec.updates != 1 {
		t.Fatalf("updates = %d, want 1", rec.updates)
	}
	body := rec.body(t, 0)
	if body["path"] != "notes/renamed.md" {
		t.Errorf("update path = %v, want notes/renamed.md", body["path"])
	}
	if got := body["content"]; got != content {
		t.Errorf("update content = %q, want original base64 content %q", got, content)
	}
}

func TestEditMultilineUnchangedCancels(t *testing.T) {
	isolateEnv(t)
	fakeEditor(t, "true")
	rec := &editRecorder{}
	srv := serve(t, rec.wrap(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/agents/agent_1" {
			_, _ = fmt.Fprint(w, `{"id":"agent_1","name":"demo","model":"kimi-k3.1-pro","version":"3","system":"line one  \nline two\n","created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-02T10:00:00Z","scope":"project"}`)
			return
		}
		_, _ = fmt.Fprint(w, renamedAgent)
	}))

	_, stderr, err := runCmd(t, "", "agents", "edit", "agent_1", "--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if rec.updates != 0 {
		t.Errorf("updates = %d, want none for an unchanged multiline manifest", rec.updates)
	}
	if !strings.Contains(stderr, "Edit cancelled, no changes made.") {
		t.Errorf("stderr = %q, want the cancel note", stderr)
	}
}
