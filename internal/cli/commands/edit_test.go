package commands

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	json "encoding/json/v2"
)

// fakeEditor points EDITOR at a shell script in a temp dir; the script gets
// the manifest path as $1. KHA_EDITOR is cleared so EDITOR always wins,
// and both are set so the developer's real editor can never leak into a test.
func fakeEditor(t *testing.T, script string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "editor.sh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KHA_EDITOR", "")
	t.Setenv("EDITOR", "sh "+path)
}

// editRecorder counts update (PATCH) calls and keeps their bodies in order.
type editRecorder struct {
	updates int
	bodies  []string
}

func (r *editRecorder) wrap(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPatch {
			r.updates++
			data, _ := io.ReadAll(req.Body)
			r.bodies = append(r.bodies, string(data))
		}
		h(w, req)
	}
}

func (r *editRecorder) body(t *testing.T, i int) map[string]any {
	t.Helper()
	if i >= len(r.bodies) {
		t.Fatalf("update body %d missing (have %d)", i, len(r.bodies))
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(r.bodies[i]), &m); err != nil {
		t.Fatalf("update body %d is not JSON: %v", i, err)
	}
	return m
}

// demoAgent is the stock Agent the edit tests fetch and modify.
const demoAgent = `{"id":"agent_1","name":"demo","model":"kimi-k3.1-pro","version":"3","created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-02T10:00:00Z","scope":"project"}`

const renamedAgent = `{"id":"agent_1","name":"renamed","model":"kimi-k3.1-pro","version":"4","created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-03T10:00:00Z","scope":"project"}`

// serveAgentEdit scripts GET + PATCH /v1/agents/agent_1.
func serveAgentEdit(t *testing.T, rec *editRecorder, patch func(w http.ResponseWriter, updates int)) *httptest.Server {
	return serve(t, rec.wrap(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/agents/agent_1":
			_, _ = fmt.Fprint(w, demoAgent)
		case r.Method == http.MethodPatch && r.URL.Path == "/v1/agents/agent_1":
			patch(w, rec.updates)
		default:
			http.NotFound(w, r)
		}
	}))
}

// TestAgentsEditAppliesChanges: the editor's modified manifest goes out as
// the update — the fetched version rides along untouched for the
// optimistic-concurrency check — and the updated Agent prints.
func TestAgentsEditAppliesChanges(t *testing.T) {
	isolateEnv(t)
	fakeEditor(t, `printf 'model: kimi-k3.1-pro\nname: renamed\nversion: "3"\n' > "$1"`)
	rec := &editRecorder{}
	srv := serveAgentEdit(t, rec, func(w http.ResponseWriter, _ int) {
		_, _ = fmt.Fprint(w, renamedAgent)
	})

	stdout, stderr, err := runCmd(t, "", "agents", "edit", "agent_1", "--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if rec.updates != 1 {
		t.Fatalf("updates = %d, want 1", rec.updates)
	}
	body := rec.body(t, 0)
	if body["name"] != "renamed" || body["version"] != "3" || body["model"] != "kimi-k3.1-pro" {
		t.Errorf("update body = %v, want the renamed name with the fetched model and version", body)
	}
	if len(body) != 3 {
		t.Errorf("update body = %v, want only the editable fields", body)
	}
	if !strings.Contains(stdout, "agent_1") || !strings.Contains(stdout, "renamed") {
		t.Errorf("stdout = %q, want the updated Agent row", stdout)
	}
	if !strings.Contains(stderr, "agent agent_1 updated (new version 4)") {
		t.Errorf("stderr = %q, want the update note", stderr)
	}
}

// TestEditUnchangedCancels: saving without changes (EDITOR=true) cancels and
// never touches the update endpoint.
func TestEditUnchangedCancels(t *testing.T) {
	isolateEnv(t)
	t.Setenv("KHA_EDITOR", "")
	t.Setenv("EDITOR", "true")
	rec := &editRecorder{}
	srv := serveAgentEdit(t, rec, func(w http.ResponseWriter, _ int) {
		_, _ = fmt.Fprint(w, renamedAgent)
	})

	_, stderr, err := runCmd(t, "", "agents", "edit", "agent_1", "--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "Edit cancelled, no changes made.") {
		t.Errorf("stderr = %q, want the cancel note", stderr)
	}
	if rec.updates != 0 {
		t.Errorf("updates = %d, want none on a cancelled edit", rec.updates)
	}
}

// TestEditEmptyFileCancels: truncating the manifest cancels the edit.
func TestEditEmptyFileCancels(t *testing.T) {
	isolateEnv(t)
	fakeEditor(t, `: > "$1"`)
	rec := &editRecorder{}
	srv := serveAgentEdit(t, rec, func(w http.ResponseWriter, _ int) {
		_, _ = fmt.Fprint(w, renamedAgent)
	})

	_, stderr, err := runCmd(t, "", "agents", "edit", "agent_1", "--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "Edit cancelled, no changes made.") {
		t.Errorf("stderr = %q, want the cancel note", stderr)
	}
	if rec.updates != 0 {
		t.Errorf("updates = %d, want none on a cancelled edit", rec.updates)
	}
}

// countEditorScript returns an editor script that counts its invocations in
// counterFile and runs the first or later block accordingly.
func countEditorScript(counterFile, first, later string) string {
	return fmt.Sprintf(`n=0; [ -f %[1]q ] && n=$(cat %[1]q); n=$((n+1)); echo $n > %[1]q
if [ "$n" -eq 1 ]; then
%[2]s
else
%[3]s
fi`, counterFile, first, later)
}

func readCounter(t *testing.T, counterFile string) string {
	t.Helper()
	data, err := os.ReadFile(counterFile)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(data))
}

// TestAgentsEditUnknownFieldReopens: a manifest with an unknown field is
// rejected by the strict decode; the file reopens with the error as comments
// and the corrected second save goes through.
func TestAgentsEditUnknownFieldReopens(t *testing.T) {
	isolateEnv(t)
	counter := filepath.Join(t.TempDir(), "count")
	fakeEditor(t, countEditorScript(counter,
		`  printf 'name: renamed\nbogus: 1\n' > "$1"`,
		`  grep -q 'Edit failed' "$1" || exit 1
  grep -q 'bogus' "$1" || exit 1
  printf 'name: renamed\nversion: "3"\n' > "$1"`))
	rec := &editRecorder{}
	srv := serveAgentEdit(t, rec, func(w http.ResponseWriter, _ int) {
		_, _ = fmt.Fprint(w, renamedAgent)
	})

	_, _, err := runCmd(t, "", "agents", "edit", "agent_1", "--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if got := readCounter(t, counter); got != "2" {
		t.Errorf("editor invocations = %s, want 2 (the bad save must reopen)", got)
	}
	if rec.updates != 1 {
		t.Fatalf("updates = %d, want 1 (only the corrected save)", rec.updates)
	}
	if body := rec.body(t, 0); body["name"] != "renamed" || body["version"] != "3" {
		t.Errorf("update body = %v, want the corrected manifest", body)
	}
}

// TestAgentsEditServerRejectionReopens: an API 4xx reopens the file with the
// error; the retry succeeds.
func TestAgentsEditServerRejectionReopens(t *testing.T) {
	isolateEnv(t)
	counter := filepath.Join(t.TempDir(), "count")
	fakeEditor(t, countEditorScript(counter,
		`  printf 'name: renamed\nversion: "3"\n' > "$1"`,
		`  grep -q 'agent changed since version 3' "$1" || exit 1
  grep -q 'param: version' "$1" || exit 1
  grep -q 'request id: req_c' "$1" || exit 1
  printf 'name: renamed\nversion: "3"\n' > "$1"`))
	rec := &editRecorder{}
	srv := serveAgentEdit(t, rec, func(w http.ResponseWriter, updates int) {
		if updates == 1 {
			w.WriteHeader(http.StatusConflict)
			_, _ = fmt.Fprint(w, `{"error":{"type":"conflict_error","message":"agent changed since version 3","param":"version","request_id":"req_c"}}`)
			return
		}
		_, _ = fmt.Fprint(w, renamedAgent)
	})

	_, _, err := runCmd(t, "", "agents", "edit", "agent_1", "--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if got := readCounter(t, counter); got != "2" {
		t.Errorf("editor invocations = %s, want 2 (the 409 must reopen)", got)
	}
	if rec.updates != 2 {
		t.Errorf("updates = %d, want 2 (rejected once, then accepted)", rec.updates)
	}
}

// TestResolveEditorPrecedence: KHA_EDITOR wins over EDITOR, and the
// platform default is vi (notepad on Windows).
func TestResolveEditorPrecedence(t *testing.T) {
	t.Setenv("KHA_EDITOR", "kha-ed")
	t.Setenv("EDITOR", "plain-ed")
	if got := resolveEditor(); got != "kha-ed" {
		t.Errorf("resolveEditor() = %q, want KHA_EDITOR", got)
	}
	t.Setenv("KHA_EDITOR", "")
	if got := resolveEditor(); got != "plain-ed" {
		t.Errorf("resolveEditor() = %q, want EDITOR", got)
	}
	t.Setenv("EDITOR", "")
	want := "vi"
	if runtime.GOOS == "windows" {
		want = "notepad"
	}
	if got := resolveEditor(); got != want {
		t.Errorf("resolveEditor() = %q, want the platform default %q", got, want)
	}
}

// TestEditScratchFileIsPrivate: the scratch manifest lands on disk mode 0600
// (it can hold system prompts and metadata).
func TestEditScratchFileIsPrivate(t *testing.T) {
	isolateEnv(t)
	modeFile := filepath.Join(t.TempDir(), "mode")
	// GNU stat first (Linux), BSD stat as the fallback (macOS): the reverse
	// order never triggers the fallback because GNU stat treats -f as
	// --file-system and exits 0 with the wrong dump.
	fakeEditor(t, fmt.Sprintf(`(stat -c '%%a' "$1" 2>/dev/null || stat -f '%%Lp' "$1") > %q`, modeFile))
	rec := &editRecorder{}
	srv := serveAgentEdit(t, rec, func(w http.ResponseWriter, _ int) {
		_, _ = fmt.Fprint(w, renamedAgent)
	})

	if _, _, err := runCmd(t, "", "agents", "edit", "agent_1", "--base-url", srv.URL, "--api-key", "sk-test"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(modeFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(data)); got != "600" {
		t.Errorf("scratch file mode = %s, want 600", got)
	}
}

// TestVaultsCredentialsEdit covers a nested resource end to end: the fetched
// credential's auth rides along while the display name changes.
func TestVaultsCredentialsEdit(t *testing.T) {
	isolateEnv(t)
	fakeEditor(t, `cat > "$1" <<'EOF'
auth:
  secret_name: GITHUB_TOKEN
  type: environment_variable
display_name: renamed-cred
EOF
`)
	rec := &editRecorder{}
	srv := serve(t, rec.wrap(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/vaults/vlt_1/credentials/vcrd_1":
			if r.Method == http.MethodGet {
				_, _ = fmt.Fprint(w, `{"id":"vcrd_1","vault_id":"vlt_1","display_name":"github","auth":{"type":"environment_variable","secret_name":"GITHUB_TOKEN"},"created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-01T10:00:00Z"}`)
				return
			}
			_, _ = fmt.Fprint(w, `{"id":"vcrd_1","vault_id":"vlt_1","display_name":"renamed-cred","auth":{"type":"environment_variable","secret_name":"GITHUB_TOKEN"},"created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-02T10:00:00Z"}`)
		default:
			http.NotFound(w, r)
		}
	}))

	stdout, _, err := runCmd(t, "", "vaults", "credentials", "edit", "vlt_1", "vcrd_1", "--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	body := rec.body(t, 0)
	if body["display_name"] != "renamed-cred" {
		t.Errorf("update body = %v, want the renamed display_name", body)
	}
	auth, _ := body["auth"].(map[string]any)
	if auth["type"] != "environment_variable" || auth["secret_name"] != "GITHUB_TOKEN" {
		t.Errorf("update body auth = %v, want the fetched auth riding along", auth)
	}
	if !strings.Contains(stdout, "renamed-cred") {
		t.Errorf("stdout = %q, want the updated credential row", stdout)
	}
}

// TestMemoriesEditPlaintextContent: the manifest shows the memory's content
// as plain text (base64 in the API), and the edited text goes back out
// base64-encoded with content_sha256 echoed for the concurrency check.
func TestMemoriesEditPlaintextContent(t *testing.T) {
	isolateEnv(t)
	fakeEditor(t, `grep -q 'content: buy milk' "$1" || exit 1
cat > "$1" <<'EOF'
content: buy bread
content_sha256: 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08
path: notes/todo.md
EOF
`)
	rec := &editRecorder{}
	srv := serve(t, rec.wrap(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/memory-stores/mstr_1/memories/mem_1":
			if r.Method == http.MethodGet {
				_, _ = fmt.Fprint(w, `{"id":"mem_1","memory_store_id":"mstr_1","path":"notes/todo.md","content":"YnV5IG1pbGs=","content_sha256":"9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08","content_size_bytes":8,"created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-01T10:00:00Z"}`)
				return
			}
			_, _ = fmt.Fprint(w, `{"id":"mem_1","memory_store_id":"mstr_1","path":"notes/todo.md","content":"YnV5IGJyZWFk","content_sha256":"3d5470c8b24ea0e2f1c9d5c8d3d4b0b6d4d00f05a02e1e2c5b6a5d4c3b2a1908","content_size_bytes":9,"created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-02T10:00:00Z"}`)
		default:
			http.NotFound(w, r)
		}
	}))

	stdout, _, err := runCmd(t, "", "memory-stores", "memories", "edit", "mstr_1", "mem_1", "--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	body := rec.body(t, 0)
	if body["content"] != "YnV5IGJyZWFk" {
		t.Errorf("update body content = %v, want base64 of the edited plain text", body["content"])
	}
	if body["content_sha256"] != "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08" {
		t.Errorf("update body content_sha256 = %v, want the fetched hash echoed", body["content_sha256"])
	}
	if body["path"] != "notes/todo.md" {
		t.Errorf("update body path = %v, want the fetched path", body["path"])
	}
	if !strings.Contains(stdout, "notes/todo.md") {
		t.Errorf("stdout = %q, want the updated memory row", stdout)
	}
}

// TestDreamPolicyEdit: the policy update is a full replacement, so the edited
// manifest carries both conditions back, with only the threshold changed.
func TestDreamPolicyEdit(t *testing.T) {
	isolateEnv(t)
	fakeEditor(t, `cat > "$1" <<'EOF'
activity:
  enabled: true
  min_changed_sessions: 5
agent_id: agent_1
environment_id: env_1
schedule:
  enabled: false
EOF
`)
	rec := &editRecorder{}
	srv := serve(t, rec.wrap(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/memory-stores/mstr_1/dream-policy":
			if r.Method == http.MethodGet {
				_, _ = fmt.Fprint(w, `{"memory_store_id":"mstr_1","agent_id":"agent_1","activity":{"enabled":true,"min_changed_sessions":3},"schedule":{"enabled":false},"environment_id":"env_1","trigger_id":"trig_1","updated_at":"2026-08-01T10:00:00Z"}`)
				return
			}
			_, _ = fmt.Fprint(w, `{"memory_store_id":"mstr_1","agent_id":"agent_1","activity":{"enabled":true,"min_changed_sessions":5},"schedule":{"enabled":false},"environment_id":"env_1","trigger_id":"trig_1","updated_at":"2026-08-02T10:00:00Z"}`)
		default:
			http.NotFound(w, r)
		}
	}))

	stdout, _, err := runCmd(t, "", "memory-stores", "dream-policy", "edit", "mstr_1", "--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	body := rec.body(t, 0)
	activity, _ := body["activity"].(map[string]any)
	if activity["enabled"] != true || activity["min_changed_sessions"] != float64(5) {
		t.Errorf("update body activity = %v, want enabled with the edited threshold", activity)
	}
	schedule, _ := body["schedule"].(map[string]any)
	if schedule["enabled"] != false {
		t.Errorf("update body schedule = %v, want the untouched condition riding along", schedule)
	}
	if body["agent_id"] != "agent_1" || body["environment_id"] != "env_1" {
		t.Errorf("update body = %v, want agent_id and environment_id riding along", body)
	}
	if _, ok := body["trigger_id"]; ok {
		t.Errorf("update body = %v, trigger_id is server-owned and must not be sent", body)
	}
	if !strings.Contains(stdout, "mstr_1") {
		t.Errorf("stdout = %q, want the updated policy row", stdout)
	}
}

// TestBodyFileYAMLMapsSnakeCase: a YAML --file body maps snake_case keys onto
// the request structs. Decoding YAML straight into the structs (the old
// behavior) matched yaml.v3's lowercased Go field names and silently dropped
// multi-word keys like display_name and agent_id.
func TestBodyFileYAMLMapsSnakeCase(t *testing.T) {
	isolateEnv(t)
	rec := &editRecorder{}
	srv := serve(t, rec.wrap(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/vaults/vlt_1":
			_, _ = fmt.Fprint(w, `{"id":"vlt_1","display_name":"renamed","created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-02T10:00:00Z"}`)
		case "/v1/triggers/trig_1":
			_, _ = fmt.Fprint(w, `{"id":"trig_1","name":"nightly","status":"active","agent_id":"agent_9","environment_id":"env_9","session_mode":"new_session","policy":{"type":"cron","expression":"0 2 * * *","timezone":"UTC"},"scope":"project","created_by":"user_1","created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-02T10:00:00Z"}`)
		default:
			http.NotFound(w, r)
		}
	}))

	_, _, err := runCmd(t, "display_name: renamed\nmetadata:\n  team: infra\n",
		"vaults", "update", "vlt_1", "--file", "-", "--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	vault := rec.body(t, 0)
	if vault["display_name"] != "renamed" {
		t.Errorf("vault update body = %v, want display_name mapped from YAML", vault)
	}
	if md, _ := vault["metadata"].(map[string]any); md["team"] != "infra" {
		t.Errorf("vault update body = %v, want the nested metadata mapped", vault)
	}

	_, _, err = runCmd(t, "name: nightly\nagent_id: agent_9\nenvironment_id: env_9\n",
		"triggers", "update", "trig_1", "--file", "-", "--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	trigger := rec.body(t, 1)
	if trigger["agent_id"] != "agent_9" || trigger["environment_id"] != "env_9" {
		t.Errorf("trigger update body = %v, want agent_id and environment_id mapped from YAML", trigger)
	}
}
