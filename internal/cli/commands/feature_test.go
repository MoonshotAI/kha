package commands

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/MoonshotAI/kha"

	"github.com/spf13/cobra"

	"github.com/MoonshotAI/kha/internal/cli/config"
)

func completionServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/agents":
			_, _ = io.WriteString(w, `{"items":[{"id":"agent_00000000000000000000000000","name":"demo","model":"kimi-k2","version":"1","created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-01T10:00:00Z","scope":"project"}]}`)
		case "/v1/agents/agent_1/versions":
			_, _ = io.WriteString(w, `{"items":[{"id":"agent_1","name":"demo","model":"kimi-k2","version":"2","created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-02T10:00:00Z","scope":"project"}]}`)
		case "/v1/sessions":
			_, _ = io.WriteString(w, `{"items":[{"id":"sess_00000000000000000000000000","agent_id":"agent_1","environment_id":"env_1","status":"idle","created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-01T10:00:00Z"}]}`)
		case "/v1/sessions/sess_1/resources":
			_, _ = io.WriteString(w, `{"items":[{"type":"file","id":"sres_1","file_id":"file_1","path":"upload/a.csv"},{"type":"vault","id":"sres_2","vault_id":"vlt_1"},{"type":"memory_store","id":"sres_3","memory_store_id":"mstr_1","access":"read_only"},{"type":"github_repository","id":"sres_4","url":"https://github.com/acme/widgets","mount_path":"/workspace/widgets","created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-01T10:00:00Z"}]}`)
		case "/v1/sessions/sess_1/threads":
			_, _ = io.WriteString(w, `{"items":[{"id":"sthr_1","session_id":"sess_1","status":"idle","agent":{"id":"agent_1","version":"3","name":"coordinator"},"created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-01T10:00:00Z"},{"id":"sthr_2","session_id":"sess_1","parent_thread_id":"sthr_1","status":"running","created_at":"2026-08-01T10:02:00Z","updated_at":"2026-08-01T10:02:00Z"}]}`)
		case "/v1/sessions/sess_1/events/pending":
			_, _ = io.WriteString(w, `{"items":[{"event_id":"sevt_9","type":"steer","events":[{"type":"user.message","data":{"content":[{"type":"text","text":"hurry up"}]}}],"queued_at":"2026-08-01T10:05:00Z"}]}`)
		case "/v1/sessions/sess_1/filesystem":
			if r.URL.Query().Get("prefix") == "upload" {
				_, _ = io.WriteString(w, `{"items":[{"name":"a.csv","size_bytes":12,"is_dir":false,"path":"upload/a.csv","last_modified":"2026-08-01T10:00:00Z"}]}`)
				return
			}
			_, _ = io.WriteString(w, `{"items":[{"name":"upload","size_bytes":0,"is_dir":true,"path":"upload"},{"name":"notes.txt","size_bytes":2048,"is_dir":false,"path":"notes.txt","last_modified":"2026-08-01T10:00:00Z"}]}`)
		case "/v1/vaults":
			_, _ = io.WriteString(w, `{"items":[{"id":"vlt_1","display_name":"prod vault","created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-01T10:00:00Z"}]}`)
		case "/v1/vaults/vlt_1/credentials":
			_, _ = io.WriteString(w, `{"items":[{"id":"vcrd_1","vault_id":"vlt_1","display_name":"github token","auth":{"type":"environment_variable","secret_name":"GITHUB_TOKEN"},"created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-01T10:00:00Z"}]}`)
		case "/v1/triggers":
			_, _ = io.WriteString(w, `{"items":[{"id":"trig_1","name":"nightly","status":"active","agent_id":"agent_1","session_mode":"new","policy":{"type":"cron","expression":"0 2 * * *","timezone":"UTC"},"scope":"project","created_by":"user_1","created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-01T10:00:00Z"}]}`)
		case "/v1/triggers/trig_1/runs":
			_, _ = io.WriteString(w, `{"items":[{"id":"trun_1","trigger_id":"trig_1","status":"completed","created_at":"2026-08-01T10:00:00Z","finished_at":"2026-08-01T10:03:00Z"}]}`)
		case "/v1/environments":
			_, _ = io.WriteString(w, `{"items":[{"id":"env_1","name":"builder","build_status":"ready","created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-01T10:00:00Z"}]}`)
		case "/v1/plugins":
			_, _ = io.WriteString(w, `{"items":[{"id":"plug_1","name":"web-search","display_name":"Web Search","description":"Search the web","version":"1.2.0","created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-01T10:00:00Z"}]}`)
		case "/v1/memory-stores":
			_, _ = io.WriteString(w, `{"items":[{"id":"mstr_1","name":"notes","created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-01T10:00:00Z"}]}`)
		case "/v1/memory-stores/mstr_1/memories":
			_, _ = io.WriteString(w, `{"items":[{"id":"mem_1","memory_store_id":"mstr_1","path":"notes/todo.md","content_sha256":"abc","content_size_bytes":8,"created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-01T10:00:00Z"}]}`)
		case "/v1/memory-stores/mstr_1/memories/mem_1/versions":
			_, _ = io.WriteString(w, `{"items":[{"id":"mver_1","memory_store_id":"mstr_1","memory_id":"mem_1","path":"notes/todo.md","operation":"created","source":"api","created_at":"2026-08-01T10:00:00Z"}]}`)
		case "/v1/skills":
			_, _ = io.WriteString(w, `{"items":[{"id":"skl_1","name":"web-research","description":"Search the web","latest_version":"3","status":"active","created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-01T10:00:00Z","scope":"project"}]}`)
		case "/v1/skills/skl_1/versions":
			_, _ = io.WriteString(w, `{"items":[{"skill_id":"skl_1","version":"3","name":"web-research","description":"Search the web","created_at":"2026-08-01T10:00:00Z"}]}`)
		case "/v1/artifacts":
			_, _ = io.WriteString(w, `{"items":[{"id":"arti_1","session_id":"sess_1","path":"/tmp/report.pdf","name":"report.pdf","mime_type":"application/pdf","size_bytes":10,"version":"3","delivered_at":"2026-08-01T10:00:00Z"}]}`)
		case "/v1/artifacts/arti_1/versions":
			_, _ = io.WriteString(w, `{"items":[{"id":"arti_1","session_id":"sess_1","path":"/tmp/report.pdf","name":"report.pdf","mime_type":"application/pdf","size_bytes":10,"version":"3","delivered_at":"2026-08-01T10:00:00Z"}]}`)
		case "/v1/files":
			_, _ = io.WriteString(w, `{"items":[{"id":"file_1","filename":"notes.txt","mime_type":"text/plain","size_bytes":12,"created_at":"2026-08-01T10:00:00Z"}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestStructuredFileInputs(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, tc := range []struct{ name, source, want string }{
		{"stdin", "-", "stdin-agent"},
		{"file", "request.yaml", "file-agent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.source != "-" {
				if err := os.WriteFile(tc.source, []byte("name: "+tc.want+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			b := &bodyFlags{file: tc.source}
			cmd := &cobra.Command{}
			cmd.SetIn(strings.NewReader("name: stdin-agent\n"))
			var got struct {
				Name string `json:"name"`
			}
			if err := b.decode(cmd, &got); err != nil {
				t.Fatal(err)
			}
			if got.Name != tc.want {
				t.Fatalf("decoded name = %q, want %q", got.Name, tc.want)
			}
		})
	}
}

func TestCompletionCommandGeneratesAllShells(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		root := NewRootCommand()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(io.Discard)
		root.SetArgs([]string{"completion", shell})
		if err := root.Execute(); err != nil {
			t.Fatalf("%s: %v", shell, err)
		}
		if out.Len() == 0 {
			t.Fatalf("%s completion is empty", shell)
		}
	}
}

func TestDynamicCompletionCandidates(t *testing.T) {
	isolateEnv(t)
	ts := completionServer(t)
	defer ts.Close()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.SetProfile("test", config.Profile{APIKey: "sk-test", BaseURL: ts.URL})
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KHA_PROFILE", "test")
	root := NewRootCommand()
	root.SetContext(context.Background())
	root.SetArgs([]string{"agents", "get"})
	root.SetIn(strings.NewReader(""))
	agents, _, err := root.Find([]string{"agents", "get"})
	agents.SetContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	client, _, e := newClientQuiet(agents)
	if e != nil {
		t.Fatal(e)
	}
	itemsResult, e := client.Agents.List(agents.Context(), kha.ListAgentsRequest{})
	if e != nil {
		t.Fatal(e)
	}

	items := itemsResult.Body.Items

	if len(items) != 1 {
		t.Fatalf("page items = %d", len(items))
	}
	got, _ := agents.ValidArgsFunction(agents, nil, "")
	if len(got) != 1 || got[0] != "agent_00000000000000000000000000\tdemo v1" {
		t.Fatalf("agents candidates = %v", got)
	}
	sessions, _, err := root.Find([]string{"sessions", "get"})
	sessions.SetContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got, _ = sessions.ValidArgsFunction(sessions, nil, "")
	if len(got) != 1 || got[0] != "sess_00000000000000000000000000\tidle" {
		t.Fatalf("sessions candidates = %v", got)
	}
}

// TestAgentSessionCompletionCandidates exercises every agents- and
// sessions-group ValidArgsFunction against the fake API: the Agent ID
// completes as "id\tname vN" and the Session ID as "id\tstatus" at the first
// positional; the nested resources/threads/pending/fs commands complete the
// Session ID first, then the nested ID (or file path) at the second.
func TestAgentSessionCompletionCandidates(t *testing.T) {
	isolateEnv(t)
	ts := completionServer(t)
	defer ts.Close()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.SetProfile("test", config.Profile{APIKey: "sk-test", BaseURL: ts.URL})
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KHA_PROFILE", "test")

	complete := func(path string, args ...string) ([]string, cobra.ShellCompDirective) {
		t.Helper()
		root := NewRootCommand()
		cmd, _, err := root.Find(strings.Split(path, " "))
		if err != nil {
			t.Fatal(err)
		}
		if cmd.ValidArgsFunction == nil {
			t.Fatalf("%s has no ValidArgsFunction", path)
		}
		cmd.SetContext(context.Background())
		return cmd.ValidArgsFunction(cmd, args, "")
	}

	for _, path := range []string{
		"agents get", "agents update", "agents archive", "agents versions",
	} {
		got, directive := complete(path)
		if directive != cobra.ShellCompDirectiveNoFileComp {
			t.Errorf("%s: directive = %v, want NoFileComp", path, directive)
		}
		if len(got) != 1 || got[0] != "agent_00000000000000000000000000\tdemo v1" {
			t.Errorf("%s: agent candidates = %v", path, got)
		}
	}

	for _, path := range []string{
		"sessions get", "sessions update", "sessions archive", "sessions delete",
		"sessions send", "sessions interrupt", "sessions attach", "sessions events",
		"sessions resources list", "sessions resources add",
		"sessions resources get", "sessions resources delete",
		"sessions threads list", "sessions threads get", "sessions threads events", "sessions threads archive",
		"sessions pending list", "sessions pending retract",
		"sessions fs list", "sessions fs download",
	} {
		got, directive := complete(path)
		if directive != cobra.ShellCompDirectiveNoFileComp {
			t.Errorf("%s: directive = %v, want NoFileComp", path, directive)
		}
		if len(got) != 1 || got[0] != "sess_00000000000000000000000000\tidle" {
			t.Errorf("%s: session candidates = %v", path, got)
		}
	}

	// The resource ID position lists the bindings of the Session in args[0],
	// one candidate per union variant.
	for _, path := range []string{"sessions resources get", "sessions resources delete"} {
		got, directive := complete(path, "sess_1")
		if directive != cobra.ShellCompDirectiveNoFileComp {
			t.Errorf("%s <resource-id>: directive = %v, want NoFileComp", path, directive)
		}
		want := []string{"sres_1\tfile file_1 -> upload/a.csv", "sres_2\tvault vlt_1", "sres_3\tmemory-store mstr_1 (read_only)", "sres_4\trepo https://github.com/acme/widgets -> /workspace/widgets"}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("%s <resource-id>: candidates = %v", path, got)
		}
	}

	// The Thread ID position lists the Threads of the Session in args[0].
	for _, path := range []string{"sessions threads get", "sessions threads events", "sessions threads archive"} {
		got, directive := complete(path, "sess_1")
		if directive != cobra.ShellCompDirectiveNoFileComp {
			t.Errorf("%s <thread-id>: directive = %v, want NoFileComp", path, directive)
		}
		want := []string{"sthr_1\tcoordinator, coordinator@3, idle", "sthr_2\trunning"}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("%s <thread-id>: candidates = %v", path, got)
		}
	}

	// The event ID position lists the queued inputs of the Session in args[0].
	got, directive := complete("sessions pending retract", "sess_1")
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("pending retract <event-id>: directive = %v, want NoFileComp", directive)
	}
	if len(got) != 1 || got[0] != "sevt_9\tsteer: > hurry up" {
		t.Errorf("pending retract <event-id>: candidates = %v", got)
	}

	// The path position of fs download lists files only (a directory is not
	// downloadable); typing into a directory lists that directory.
	got, directive = complete("sessions fs download", "sess_1")
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("fs download <path>: directive = %v, want NoFileComp", directive)
	}
	if len(got) != 1 || got[0] != "notes.txt\t2.0 KB" {
		t.Errorf("fs download <path>: candidates = %v", got)
	}

	// Typing into a directory lists that directory (the prefix query selects
	// it), so nested file paths complete too.
	fsCmd, _, err := NewRootCommand().Find([]string{"sessions", "fs", "download"})
	if err != nil {
		t.Fatal(err)
	}
	fsCmd.SetContext(context.Background())
	got, directive = fsCmd.ValidArgsFunction(fsCmd, []string{"sess_1"}, "upload/")
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("fs download upload/…: directive = %v, want NoFileComp", directive)
	}
	if len(got) != 1 || got[0] != "upload/a.csv\t12 B" {
		t.Errorf("fs download upload/…: candidates = %v", got)
	}

	// Failures stay silent: an unknown Session's nested listing, a position
	// past the command's arity, and a dead API all yield no candidates.
	if got, directive := complete("sessions resources get", "sess_missing"); len(got) != 0 || directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("unknown session: candidates = %v, directive = %v", got, directive)
	}
	if got, directive := complete("sessions send", "sess_1"); len(got) != 0 || directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("send <text>: candidates = %v, directive = %v", got, directive)
	}
	if got, directive := complete("sessions threads get", "sess_1", "sthr_1"); len(got) != 0 || directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("third positional of threads get: candidates = %v, directive = %v", got, directive)
	}
	cfg.SetProfile("test", config.Profile{APIKey: "sk-test", BaseURL: "http://127.0.0.1:1"})
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"agents get", "sessions get", "sessions pending retract"} {
		if got, directive := complete(path); len(got) != 0 || directive != cobra.ShellCompDirectiveNoFileComp {
			t.Errorf("%s (dead API): candidates = %v, directive = %v", path, got, directive)
		}
	}
}

// TestVaultCompletionCandidates exercises every vaults-group ValidArgsFunction
// against the fake API: the Vault ID completes as "id\tdisplay name" at the
// first positional, the credential ID at the second, and the OAuth session ID
// (no list endpoint exists) silently yields nothing.
func TestVaultCompletionCandidates(t *testing.T) {
	isolateEnv(t)
	ts := completionServer(t)
	defer ts.Close()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.SetProfile("test", config.Profile{APIKey: "sk-test", BaseURL: ts.URL})
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KHA_PROFILE", "test")

	complete := func(path string, args ...string) ([]string, cobra.ShellCompDirective) {
		t.Helper()
		root := NewRootCommand()
		cmd, _, err := root.Find(strings.Split(path, " "))
		if err != nil {
			t.Fatal(err)
		}
		if cmd.ValidArgsFunction == nil {
			t.Fatalf("%s has no ValidArgsFunction", path)
		}
		cmd.SetContext(context.Background())
		return cmd.ValidArgsFunction(cmd, args, "")
	}

	for _, path := range []string{
		"vaults get", "vaults update", "vaults delete", "vaults archive",
		"vaults credentials list", "vaults credentials create", "vaults credentials batch",
		"vaults credentials get", "vaults credentials update", "vaults credentials delete", "vaults credentials archive",
		"vaults oauth create", "vaults oauth wait", "vaults oauth get",
	} {
		got, directive := complete(path)
		if directive != cobra.ShellCompDirectiveNoFileComp {
			t.Errorf("%s: directive = %v, want NoFileComp", path, directive)
		}
		if len(got) != 1 || got[0] != "vlt_1\tprod vault" {
			t.Errorf("%s: vault candidates = %v", path, got)
		}
	}

	// The credential ID position lists the credentials of the Vault in args[0].
	for _, path := range []string{
		"vaults credentials get", "vaults credentials update",
		"vaults credentials delete", "vaults credentials archive",
	} {
		got, directive := complete(path, "vlt_1")
		if directive != cobra.ShellCompDirectiveNoFileComp {
			t.Errorf("%s: directive = %v, want NoFileComp", path, directive)
		}
		if len(got) != 1 || got[0] != "vcrd_1\tgithub token" {
			t.Errorf("%s: credential candidates = %v", path, got)
		}
	}

	// Failures stay silent: an unknown Vault's credentials and the OAuth
	// session ID position (no list endpoint) both yield no candidates.
	if got, directive := complete("vaults credentials get", "vlt_missing"); len(got) != 0 || directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("unknown vault: candidates = %v, directive = %v", got, directive)
	}
	for _, path := range []string{"vaults oauth wait", "vaults oauth get"} {
		if got, directive := complete(path, "vlt_1"); len(got) != 0 || directive != cobra.ShellCompDirectiveNoFileComp {
			t.Errorf("%s <oauth-session-id>: candidates = %v, directive = %v", path, got, directive)
		}
	}
}

// TestTriggerCompletionCandidates exercises every triggers-group
// ValidArgsFunction against the fake API: the Trigger ID completes as
// "id\tname (status)" at the first positional, and `runs wait` lists the
// runs of the Trigger in args[0] at the second.
func TestTriggerCompletionCandidates(t *testing.T) {
	isolateEnv(t)
	ts := completionServer(t)
	defer ts.Close()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.SetProfile("test", config.Profile{APIKey: "sk-test", BaseURL: ts.URL})
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KHA_PROFILE", "test")

	complete := func(path string, args ...string) ([]string, cobra.ShellCompDirective) {
		t.Helper()
		root := NewRootCommand()
		cmd, _, err := root.Find(strings.Split(path, " "))
		if err != nil {
			t.Fatal(err)
		}
		if cmd.ValidArgsFunction == nil {
			t.Fatalf("%s has no ValidArgsFunction", path)
		}
		cmd.SetContext(context.Background())
		return cmd.ValidArgsFunction(cmd, args, "")
	}

	for _, path := range []string{
		"triggers get", "triggers update", "triggers archive",
		"triggers run", "triggers pause", "triggers unpause",
		"triggers runs", "triggers runs wait",
	} {
		got, directive := complete(path)
		if directive != cobra.ShellCompDirectiveNoFileComp {
			t.Errorf("%s: directive = %v, want NoFileComp", path, directive)
		}
		if len(got) != 1 || got[0] != "trig_1\tnightly (active)" {
			t.Errorf("%s: trigger candidates = %v", path, got)
		}
	}

	// The run ID position lists the runs of the Trigger in args[0].
	got, directive := complete("triggers runs wait", "trig_1")
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("runs wait <run-id>: directive = %v, want NoFileComp", directive)
	}
	if len(got) != 1 || !strings.HasPrefix(got[0], "trun_1\tcompleted ") {
		t.Errorf("runs wait <run-id>: candidates = %v", got)
	}

	// Failures stay silent: an unknown Trigger's runs yield no candidates,
	// and a third positional completes nothing.
	if got, directive := complete("triggers runs wait", "trig_missing"); len(got) != 0 || directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("unknown trigger: candidates = %v, directive = %v", got, directive)
	}
	if got, directive := complete("triggers runs wait", "trig_1", "trun_1"); len(got) != 0 || directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("third positional: candidates = %v, directive = %v", got, directive)
	}
}

// TestEnvironmentPluginCompletionCandidates exercises every environments- and
// plugins-group ValidArgsFunction against the fake API: the Environment ID
// completes as "id\tname", the Plugin ID as "id\tdisplay name", and a dead
// API yields nothing without diagnostics.
func TestEnvironmentPluginCompletionCandidates(t *testing.T) {
	isolateEnv(t)
	ts := completionServer(t)
	defer ts.Close()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.SetProfile("test", config.Profile{APIKey: "sk-test", BaseURL: ts.URL})
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KHA_PROFILE", "test")

	complete := func(path string, args ...string) ([]string, cobra.ShellCompDirective) {
		t.Helper()
		root := NewRootCommand()
		cmd, _, err := root.Find(strings.Split(path, " "))
		if err != nil {
			t.Fatal(err)
		}
		if cmd.ValidArgsFunction == nil {
			t.Fatalf("%s has no ValidArgsFunction", path)
		}
		cmd.SetContext(context.Background())
		return cmd.ValidArgsFunction(cmd, args, "")
	}

	for _, path := range []string{
		"environments get", "environments update", "environments delete",
		"environments archive", "environments wait-ready",
	} {
		got, directive := complete(path)
		if directive != cobra.ShellCompDirectiveNoFileComp {
			t.Errorf("%s: directive = %v, want NoFileComp", path, directive)
		}
		if len(got) != 1 || got[0] != "env_1\tbuilder" {
			t.Errorf("%s: environment candidates = %v", path, got)
		}
	}

	got, directive := complete("plugins get")
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("plugins get: directive = %v, want NoFileComp", directive)
	}
	if len(got) != 1 || got[0] != "plug_1\tWeb Search" {
		t.Errorf("plugins get: plugin candidates = %v", got)
	}

	// A second positional completes nothing, and a dead API stays silent.
	if got, directive := complete("environments get", "env_1"); len(got) != 0 || directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("second positional: candidates = %v, directive = %v", got, directive)
	}
	cfg.SetProfile("test", config.Profile{APIKey: "sk-test", BaseURL: "http://127.0.0.1:1"})
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"environments get", "plugins get"} {
		if got, directive := complete(path); len(got) != 0 || directive != cobra.ShellCompDirectiveNoFileComp {
			t.Errorf("%s (dead API): candidates = %v, directive = %v", path, got, directive)
		}
	}
}

// TestMemoryCompletionCandidates exercises every memory-stores-group
// ValidArgsFunction against the fake API: the Memory Store ID completes as
// "id\tname" at the first positional, the Memory ID as "id\tpath" at the
// second, and the version ID as "id\toperation path" at the third.
func TestMemoryCompletionCandidates(t *testing.T) {
	isolateEnv(t)
	ts := completionServer(t)
	defer ts.Close()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.SetProfile("test", config.Profile{APIKey: "sk-test", BaseURL: ts.URL})
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KHA_PROFILE", "test")

	complete := func(path string, args ...string) ([]string, cobra.ShellCompDirective) {
		t.Helper()
		root := NewRootCommand()
		cmd, _, err := root.Find(strings.Split(path, " "))
		if err != nil {
			t.Fatal(err)
		}
		if cmd.ValidArgsFunction == nil {
			t.Fatalf("%s has no ValidArgsFunction", path)
		}
		cmd.SetContext(context.Background())
		return cmd.ValidArgsFunction(cmd, args, "")
	}

	for _, path := range []string{
		"memory-stores get", "memory-stores update", "memory-stores delete",
		"memory-stores archive", "memory-stores dream-policy",
		"memory-stores dream-policy run", "memory-stores dream-policy runs",
		"memory-stores memories list", "memory-stores memories create",
		"memory-stores memories get", "memory-stores memories update", "memory-stores memories delete",
		"memory-stores memories versions list", "memory-stores memories versions get", "memory-stores memories versions redact",
	} {
		got, directive := complete(path)
		if directive != cobra.ShellCompDirectiveNoFileComp {
			t.Errorf("%s: directive = %v, want NoFileComp", path, directive)
		}
		if len(got) != 1 || got[0] != "mstr_1\tnotes" {
			t.Errorf("%s: store candidates = %v", path, got)
		}
	}

	// The memory ID position lists the memories of the store in args[0].
	for _, path := range []string{
		"memory-stores memories get", "memory-stores memories update", "memory-stores memories delete",
		"memory-stores memories versions list", "memory-stores memories versions get", "memory-stores memories versions redact",
	} {
		got, directive := complete(path, "mstr_1")
		if directive != cobra.ShellCompDirectiveNoFileComp {
			t.Errorf("%s <memory-id>: directive = %v, want NoFileComp", path, directive)
		}
		if len(got) != 1 || got[0] != "mem_1\tnotes/todo.md" {
			t.Errorf("%s <memory-id>: memory candidates = %v", path, got)
		}
	}

	// The version ID position lists the versions of the Memory in args[1].
	for _, path := range []string{
		"memory-stores memories versions get", "memory-stores memories versions redact",
	} {
		got, directive := complete(path, "mstr_1", "mem_1")
		if directive != cobra.ShellCompDirectiveNoFileComp {
			t.Errorf("%s <version-id>: directive = %v, want NoFileComp", path, directive)
		}
		if len(got) != 1 || got[0] != "mver_1\tcreated notes/todo.md" {
			t.Errorf("%s <version-id>: version candidates = %v", path, got)
		}
	}

	// Failures stay silent: an unknown store's memories yield no candidates,
	// and positions past the command's arity complete nothing.
	if got, directive := complete("memory-stores memories get", "mstr_missing"); len(got) != 0 || directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("unknown store: candidates = %v, directive = %v", got, directive)
	}
	if got, directive := complete("memory-stores memories get", "mstr_1", "mem_1"); len(got) != 0 || directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("third positional of memories get: candidates = %v, directive = %v", got, directive)
	}
	if got, directive := complete("memory-stores memories versions get", "mstr_1", "mem_1", "mver_1"); len(got) != 0 || directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("fourth positional of versions get: candidates = %v, directive = %v", got, directive)
	}
}

// TestSkillArtifactFileCompletionCandidates exercises every skills-, artifacts-,
// and files-group ValidArgsFunction against the fake API: the resource ID
// completes as "id\thuman-readable description" at the first positional, and
// `skills content` lists the versions of the Skill in args[0] at the second.
func TestSkillArtifactFileCompletionCandidates(t *testing.T) {
	isolateEnv(t)
	ts := completionServer(t)
	defer ts.Close()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.SetProfile("test", config.Profile{APIKey: "sk-test", BaseURL: ts.URL})
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KHA_PROFILE", "test")

	complete := func(path string, args ...string) ([]string, cobra.ShellCompDirective) {
		t.Helper()
		root := NewRootCommand()
		cmd, _, err := root.Find(strings.Split(path, " "))
		if err != nil {
			t.Fatal(err)
		}
		if cmd.ValidArgsFunction == nil {
			t.Fatalf("%s has no ValidArgsFunction", path)
		}
		cmd.SetContext(context.Background())
		return cmd.ValidArgsFunction(cmd, args, "")
	}

	for _, path := range []string{
		"skills get", "skills delete", "skills archive", "skills versions", "skills content",
	} {
		got, directive := complete(path)
		if directive != cobra.ShellCompDirectiveNoFileComp {
			t.Errorf("%s: directive = %v, want NoFileComp", path, directive)
		}
		if len(got) != 1 || got[0] != "skl_1\tweb-research" {
			t.Errorf("%s: skill candidates = %v", path, got)
		}
	}

	// The version position of skills content lists the versions of the Skill
	// in args[0].
	got, directive := complete("skills content", "skl_1")
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("skills content <version>: directive = %v, want NoFileComp", directive)
	}
	if len(got) != 1 || got[0] != "3\tweb-research" {
		t.Errorf("skills content <version>: version candidates = %v", got)
	}

	for _, path := range []string{
		"artifacts get", "artifacts delete", "artifacts download", "artifacts versions",
	} {
		got, directive := complete(path)
		if directive != cobra.ShellCompDirectiveNoFileComp {
			t.Errorf("%s: directive = %v, want NoFileComp", path, directive)
		}
		if len(got) != 1 || got[0] != "arti_1\treport.pdf (v3)" {
			t.Errorf("%s: artifact candidates = %v", path, got)
		}
	}

	for _, path := range []string{
		"files get", "files delete", "files download",
	} {
		got, directive := complete(path)
		if directive != cobra.ShellCompDirectiveNoFileComp {
			t.Errorf("%s: directive = %v, want NoFileComp", path, directive)
		}
		if len(got) != 1 || got[0] != "file_1\tnotes.txt" {
			t.Errorf("%s: file candidates = %v", path, got)
		}
	}

	// Failures stay silent: an unknown Skill's versions, a position past the
	// command's arity, and a dead API all yield no candidates.
	if got, directive := complete("skills content", "skl_missing"); len(got) != 0 || directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("unknown skill: candidates = %v, directive = %v", got, directive)
	}
	if got, directive := complete("skills content", "skl_1", "3"); len(got) != 0 || directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("third positional of skills content: candidates = %v, directive = %v", got, directive)
	}
	if got, directive := complete("files get", "file_1"); len(got) != 0 || directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("second positional of files get: candidates = %v, directive = %v", got, directive)
	}
	cfg.SetProfile("test", config.Profile{APIKey: "sk-test", BaseURL: "http://127.0.0.1:1"})
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"skills get", "artifacts get", "files get"} {
		if got, directive := complete(path); len(got) != 0 || directive != cobra.ShellCompDirectiveNoFileComp {
			t.Errorf("%s (dead API): candidates = %v, directive = %v", path, got, directive)
		}
	}
}

func TestErrorOutputValidationAndRuntimeFormat(t *testing.T) {
	isolateEnv(t)
	_, _, err := runCmd(t, "", "agents", "get", "agent_1", "--error-output", "xml")
	if err == nil || !strings.Contains(err.Error(), "invalid --error-output") {
		t.Fatalf("invalid error-output error = %v", err)
	}
	_, _, err = runCmd(t, "", "agents", "get", "boom", "--base-url", "http://127.0.0.1:1", "--error-output", "json")
	var runtime *runtimeError
	if !errors.As(err, &runtime) || runtime.errorOutput != "json" {
		t.Fatalf("runtime error format = %v", err)
	}
}

func TestStructuredErrorOutput(t *testing.T) {
	for _, format := range []string{"json", "yaml"} {
		var buf bytes.Buffer
		renderError(&buf, os.ErrNotExist, format)
		if !strings.Contains(buf.String(), "error") {
			t.Fatalf("%s error = %q", format, buf.String())
		}
	}
}

func TestMalformedYAMLReportsParserLocation(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetIn(strings.NewReader("name: test\nitems: [\n"))
	var out map[string]any
	err := (&bodyFlags{file: "-"}).decode(cmd, &out)
	if err == nil || !strings.Contains(err.Error(), "YAML:") || !strings.Contains(err.Error(), "line") || !strings.Contains(err.Error(), "JSON:") {
		t.Fatalf("missing parser diagnostics: %v", err)
	}
}
