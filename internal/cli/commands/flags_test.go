package commands

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/MoonshotAI/kha/internal/cli/config"
)

// flagCompletion returns the flag completion function registered on the
// command at path (space-separated, e.g. "sessions list") for flag — the
// exact function RegisterFlagCompletionFunc stored, invoked directly.
func flagCompletion(t *testing.T, path, flag string) (*cobra.Command, cobra.CompletionFunc) {
	t.Helper()
	root := NewRootCommand()
	cmd, _, err := root.Find(strings.Split(path, " "))
	if err != nil {
		t.Fatal(err)
	}
	fn, ok := cmd.GetFlagCompletionFunc(flag)
	if !ok {
		t.Fatalf("%s --%s has no registered flag completion", path, flag)
	}
	cmd.SetContext(context.Background())
	return cmd, fn
}

// writeTestProfile stores a credential profile pointing at baseURL and
// selects it, so dynamic completions resolve the fake API through
// newClientQuiet.
func writeTestProfile(t *testing.T, baseURL string) {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.SetProfile("test", config.Profile{APIKey: "sk-test", BaseURL: baseURL})
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KHA_PROFILE", "test")
}

// TestStatusFlagRenamedFromStatuses covers the --statuses → --status rename:
// the new name drives the statuses query parameter and the old one is a
// usage error (exit 2) — the v0 CLI keeps no compatibility alias.
func TestStatusFlagRenamedFromStatuses(t *testing.T) {
	isolateEnv(t)
	var gotQuery string
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = fmt.Fprint(w, `{"items":[]}`)
	})
	if _, _, err := runCmd(t, "", "sessions", "list", "--status", "idle, running", "--base-url", srv.URL, "--api-key", "sk-test"); err != nil {
		t.Fatal(err)
	}
	q, err := url.ParseQuery(gotQuery)
	if err != nil {
		t.Fatalf("recorded query %q: %v", gotQuery, err)
	}
	if got := q.Get("statuses"); got != "idle,running" {
		t.Errorf("statuses = %q, want idle,running (raw: %s)", got, gotQuery)
	}

	_, _, err = runCmd(t, "", "sessions", "list", "--statuses", "idle")
	if err == nil {
		t.Fatal("expected a usage error for the removed --statuses name")
	}
	if code := exitCode(err); code != exitUsage {
		t.Errorf("exitCode = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(err.Error(), "unknown flag: --statuses") {
		t.Errorf("err = %v, want unknown flag: --statuses", err)
	}
}

// TestShorthandFlags covers the short options end to end: each one reaches
// the same request shape as its long form.
func TestShorthandFlags(t *testing.T) {
	isolateEnv(t)

	// -a drains every page like --all.
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/files" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("page_token") == "tok2" {
			_, _ = fmt.Fprint(w, `{"items":[{"id":"file_2","filename":"b.csv","bytes":7,"created_at":"2026-08-02T10:00:00Z"}]}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"items":[{"id":"file_1","filename":"a.csv","bytes":5,"created_at":"2026-08-01T10:00:00Z"}],"next_page_token":"tok2"}`)
	})
	stdout, _, err := runCmd(t, "", "files", "list", "-a", "--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "file_1") || !strings.Contains(stdout, "file_2") {
		t.Errorf("files list -a did not drain both pages:\n%s", stdout)
	}

	// -f reads the create body (here from stdin) like --file.
	rec := &bodyRecorder{}
	srv = serve(t, rec.wrap(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/agents" {
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, `{"id":"agent_9","name":"from-file","model":"kimi-k2","version":"1","created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-01T10:00:00Z","scope":"project"}`)
			return
		}
		http.NotFound(w, r)
	}))
	if _, _, err := runCmd(t, `{"name":"from-file","model":"kimi-k2"}`, "agents", "create", "-f", "-", "--base-url", srv.URL, "--api-key", "sk-test"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rec.body, `"name":"from-file"`) {
		t.Errorf("agents create -f - body = %s", rec.body)
	}

	// -n/-m/-d set the Agent fields like --name/--model/--description.
	if _, _, err := runCmd(t, "", "agents", "create", "-n", "created", "-m", "kimi-k2", "-d", "desc",
		"--base-url", srv.URL, "--api-key", "sk-test"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"name":"created"`, `"model":"kimi-k2"`, `"description":"desc"`} {
		if !strings.Contains(rec.body, want) {
			t.Errorf("agents create shorthand body missing %s: %s", want, rec.body)
		}
	}

	// -s filters sessions list like --status.
	var gotQuery string
	srv = serve(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = fmt.Fprint(w, `{"items":[]}`)
	})
	if _, _, err := runCmd(t, "", "sessions", "list", "-s", "idle", "--base-url", srv.URL, "--api-key", "sk-test"); err != nil {
		t.Fatal(err)
	}
	if q, err := url.ParseQuery(gotQuery); err != nil || q.Get("statuses") != "idle" {
		t.Errorf("sessions list -s idle: query = %q (parse err %v)", gotQuery, err)
	}

	// -w/-t wait like --wait/--timeout on triggers run.
	srv = serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/triggers/trig_1/run":
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, `{"id":"trun_1","trigger_id":"trig_1","status":"running","created_at":"2026-08-01T10:00:00Z"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/triggers/trig_1/runs/trun_1":
			_, _ = fmt.Fprint(w, `{"id":"trun_1","trigger_id":"trig_1","status":"completed","created_at":"2026-08-01T10:00:00Z","finished_at":"2026-08-01T10:03:00Z"}`)
		default:
			http.NotFound(w, r)
		}
	})
	stdout, _, err = runCmd(t, "", "triggers", "run", "trig_1", "-w", "-t", "5s", "--interval", "1ms",
		"--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "completed") {
		t.Errorf("triggers run -w -t 5s output:\n%s", stdout)
	}

	// -p selects the upload purpose like --purpose.
	var gotBody string
	srv = serve(t, func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		gotBody = string(data)
		// The upload is 201 per the spec; the SDK decodes the File only on
		// the declared status.
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, `{"id":"file_1","filename":"up.txt","bytes":2,"purpose":"image","created_at":"2026-08-01T10:00:00Z"}`)
	})
	path := filepath.Join(t.TempDir(), "up.txt")
	if err := os.WriteFile(path, []byte("hi"), 0o666); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCmd(t, "", "files", "upload", "-f", path, "-p", "image", "--base-url", srv.URL, "--api-key", "sk-test"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotBody, `name="purpose"`) || !strings.Contains(gotBody, "image") {
		t.Errorf("files upload -p image multipart missing the purpose:\n%s", gotBody)
	}

	// -q filters plugins list like --query.
	srv = serve(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = fmt.Fprint(w, `{"items":[]}`)
	})
	if _, _, err := runCmd(t, "", "plugins", "list", "-q", "research", "--base-url", srv.URL, "--api-key", "sk-test"); err != nil {
		t.Fatal(err)
	}
	if q, err := url.ParseQuery(gotQuery); err != nil || q.Get("query") != "research" {
		t.Errorf("plugins list -q research: query = %q (parse err %v)", gotQuery, err)
	}
}

// TestStaticFlagCompletionCandidates covers the fixed-value flag
// completions: each returns its value set verbatim with NoFileComp, and the
// comma-separated sessions list --status keeps the typed prefix before the
// last comma ("idle,run" completes to "idle,running").
func TestStaticFlagCompletionCandidates(t *testing.T) {
	isolateEnv(t)
	cases := []struct {
		path, flag string
		want       []string
	}{
		{"docs", "output", []string{"markdown", "json"}},
		{"sessions list", "status", []string{"idle", "running", "terminated"}},
		{"sessions list", "order", []string{"created_at", "created_at_desc", "updated_at", "updated_at_desc"}},
		{"agents list", "order", []string{"created_at", "created_at_desc"}},
		{"sessions events", "order", []string{"asc", "desc"}},
		{"sessions threads events", "order", []string{"asc", "desc"}},
		{"memory-stores memories versions list", "order", []string{"asc", "desc"}},
		{"files upload", "purpose", []string{"file-extract", "image", "video", "batch"}},
		{"triggers list", "status", []string{"active", "paused", "archived"}},
		{"triggers list", "policy-type", []string{"cron"}},
		{"vaults credentials create", "type", []string{"static_bearer", "environment_variable"}},
		{"sessions attach", "since", []string{"now"}},
	}
	for _, c := range cases {
		cmd, fn := flagCompletion(t, c.path, c.flag)
		got, directive := fn(cmd, nil, "")
		if directive != cobra.ShellCompDirectiveNoFileComp {
			t.Errorf("%s --%s: directive = %v, want NoFileComp", c.path, c.flag, directive)
		}
		if strings.Join(got, "\n") != strings.Join(c.want, "\n") {
			t.Errorf("%s --%s: candidates = %v, want %v", c.path, c.flag, got, c.want)
		}
	}

	// The root's format flags complete too: --output takes jsonl, the
	// error path (never streamed) does not.
	root := NewRootCommand()
	for flag, want := range map[string]string{
		"output":       "table,json,yaml,jsonl",
		"error-output": "table,json,yaml",
	} {
		fn, ok := root.GetFlagCompletionFunc(flag)
		if !ok {
			t.Fatalf("root --%s has no registered flag completion", flag)
		}
		got, directive := fn(root, nil, "")
		if directive != cobra.ShellCompDirectiveNoFileComp || strings.Join(got, ",") != want {
			t.Errorf("root --%s: candidates = %v, directive = %v", flag, got, directive)
		}
	}

	// Comma-separated continuation: everything before the last comma stays.
	cmd, fn := flagCompletion(t, "sessions list", "status")
	got, directive := fn(cmd, nil, "idle,run")
	want := []string{"idle,idle", "idle,running", "idle,terminated"}
	if directive != cobra.ShellCompDirectiveNoFileComp || strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("status CSV continuation: candidates = %v, directive = %v", got, directive)
	}
}

// TestProfileFlagCompletion covers --profile: the candidates are the local
// config's profile names, sorted — the one dynamic completion that never
// touches the network.
func TestProfileFlagCompletion(t *testing.T) {
	isolateEnv(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.SetProfile("staging", config.Profile{APIKey: "sk-s"})
	cfg.SetProfile("prod", config.Profile{APIKey: "sk-p"})
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	root := NewRootCommand()
	fn, ok := root.GetFlagCompletionFunc("profile")
	if !ok {
		t.Fatal("root --profile has no registered flag completion")
	}
	got, directive := fn(root, nil, "")
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("directive = %v, want NoFileComp", directive)
	}
	if strings.Join(got, ",") != "prod,staging" {
		t.Errorf("profile candidates = %v, want sorted [prod staging]", got)
	}
}

// TestDynamicFlagCompletionCandidates exercises the API-backed flag
// completions against the fake server: IDs complete with descriptions,
// --agent-version reads its Agent from the sibling --agent flag,
// --thread/--prefix/--version read their parent resource from the first
// positional, and missing context or a dead API stay silent.
func TestDynamicFlagCompletionCandidates(t *testing.T) {
	isolateEnv(t)
	ts := completionServer(t)
	defer ts.Close()
	writeTestProfile(t, ts.URL)

	check := func(path, flag string, args []string, toComplete string, want []string) {
		t.Helper()
		cmd, fn := flagCompletion(t, path, flag)
		got, directive := fn(cmd, args, toComplete)
		if directive != cobra.ShellCompDirectiveNoFileComp {
			t.Errorf("%s --%s: directive = %v, want NoFileComp", path, flag, directive)
		}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("%s --%s: candidates = %v, want %v", path, flag, got, want)
		}
	}

	check("sessions list", "agent", nil, "", []string{"agent_00000000000000000000000000\tdemo v1"})
	check("sessions create", "agent", nil, "", []string{"agent_00000000000000000000000000\tdemo v1"})
	check("sessions create", "environment", nil, "", []string{"env_1\tbuilder"})
	check("triggers create", "agent", nil, "", []string{"agent_00000000000000000000000000\tdemo v1"})
	check("triggers create", "environment", nil, "", []string{"env_1\tbuilder"})
	check("sessions attach", "thread", []string{"sess_1"}, "", []string{"sthr_1\tcoordinator, coordinator@3, idle", "sthr_2\trunning"})
	check("sessions resources add", "file-id", nil, "", []string{"file_1\tnotes.txt"})
	check("artifacts list", "session", nil, "", []string{"sess_00000000000000000000000000\tidle"})
	check("artifacts get", "version", []string{"arti_1"}, "", []string{"3\treport.pdf"})
	check("artifacts download", "version", []string{"arti_1"}, "", []string{"3\treport.pdf"})
	check("vaults credentials check", "vault", nil, "", []string{"vlt_1\tprod vault"})

	// --prefix lists directories only (the fake root holds the "upload"
	// directory and the "notes.txt" file), each with a trailing slash; a
	// typed directory descends into it (upload/ holds only a file).
	check("sessions fs list", "prefix", []string{"sess_1"}, "", []string{"upload/\tdirectory"})
	check("sessions fs list", "prefix", []string{"sess_1"}, "upload/", []string{})

	// --agent-version reads the Agent from the sibling --agent flag (same
	// registration on sessions list/create and triggers create).
	for _, path := range []string{"sessions list", "sessions create", "triggers create"} {
		cmd, fn := flagCompletion(t, path, "agent-version")
		if err := cmd.Flags().Set("agent", "agent_1"); err != nil {
			t.Fatal(err)
		}
		got, directive := fn(cmd, nil, "")
		if directive != cobra.ShellCompDirectiveNoFileComp || strings.Join(got, "\n") != `2	demo (kimi-k2)` {
			t.Errorf("%s --agent-version: candidates = %v, directive = %v", path, got, directive)
		}
	}

	// Missing parent context completes nothing, silently.
	for _, c := range []struct {
		path, flag string
		args       []string
	}{
		{"artifacts get", "version", nil},
		{"sessions attach", "thread", nil},
		{"sessions fs list", "prefix", nil},
	} {
		cmd, fn := flagCompletion(t, c.path, c.flag)
		if got, directive := fn(cmd, c.args, ""); len(got) != 0 || directive != cobra.ShellCompDirectiveNoFileComp {
			t.Errorf("%s --%s without the parent arg: candidates = %v, directive = %v", c.path, c.flag, got, directive)
		}
	}
	cmd, fn := flagCompletion(t, "sessions list", "agent-version")
	if got, directive := fn(cmd, nil, ""); len(got) != 0 || directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("--agent-version without --agent: candidates = %v, directive = %v", got, directive)
	}

	// A dead API completes nothing and never falls back to file completion.
	writeTestProfile(t, "http://127.0.0.1:1")
	for _, c := range []struct {
		path, flag string
		args       []string
	}{
		{"sessions list", "agent", nil},
		{"artifacts get", "version", []string{"arti_1"}},
	} {
		cmd, fn := flagCompletion(t, c.path, c.flag)
		if got, directive := fn(cmd, c.args, ""); len(got) != 0 || directive != cobra.ShellCompDirectiveNoFileComp {
			t.Errorf("%s --%s (dead API): candidates = %v, directive = %v", c.path, c.flag, got, directive)
		}
	}
}
