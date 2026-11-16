package commands

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	json "encoding/json/v2"

	"github.com/spf13/cobra"

	"github.com/MoonshotAI/kha/internal/cli/config"
)

// isolateEnv cuts every environment input the CLI reads off from the real
// one: config root, credentials, profile selection, colors.
func isolateEnv(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("KIMI_API_KEY", "")
	t.Setenv("KHA_PROFILE", "")
	t.Setenv("KHA_BASE_URL", "")
	t.Setenv("NO_COLOR", "1")
	for _, key := range []string{"KHA_ENVIRONMENT_ID", "KHA_WORK_ID", "KHA_SESSION_ID", "KHA_WORKER_ID", "KHA_WORK_GENERATION", "KHA_WORK_SECRET_FILE", "KHA_WORK_LEASE_TTL_MS"} {
		t.Setenv(key, "")
	}
	stubKeychain(t)
}

func runCmd(t *testing.T, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := NewRootCommand()
	outBuf, errBuf := new(bytes.Buffer), new(bytes.Buffer)
	root.SetOut(outBuf)
	root.SetErr(errBuf)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(args)
	execErr := root.Execute()
	return outBuf.String(), errBuf.String(), execErr
}

// fakeAPI scripts the endpoints the tests hit and records requests.
type fakeAPI struct {
	t        *testing.T
	lastAuth string
	lastVer  string
	lastBody []byte
	requests []string
}

func (f *fakeAPI) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		f.requests = append(f.requests, r.Method+" "+r.URL.Path)
		f.lastAuth = r.Header.Get("Authorization")
		f.lastVer = r.Header.Get("kimi-api-version")
		if r.Body != nil {
			f.lastBody, _ = io.ReadAll(r.Body)
		}
		w.Header().Set("Msh-Request-Id", "req_test_1")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/agents":
			_, _ = fmt.Fprint(w, `{"items":[{"id":"agent_1","name":"demo","model":"kimi-k2","version":"3","created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-02T10:00:00Z","scope":"project"}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/agents":
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, `{"id":"agent_9","name":"created","model":"kimi-k2","version":"1","created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-01T10:00:00Z","scope":"project"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/sess_1":
			_, _ = fmt.Fprint(w, `{"id":"sess_1","agent_id":"agent_1","environment_id":"env_1","status":"idle","created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-01T10:00:00Z"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sessions/sess_1/events":
			w.WriteHeader(http.StatusAccepted)
			_, _ = fmt.Fprint(w, `{"event_id":"evt_1"}`)
		case r.URL.Path == "/v1/agents/boom":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = fmt.Fprint(w, `{"error":{"type":"authentication_error","message":"bad key","request_id":"req_test_1"}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `{"error":{"type":"not_found_error","message":"no such route","request_id":"req_test_1"}}`)
		}
	})
	return mux
}

func TestArgumentValidationExplainsHowToFixMissingID(t *testing.T) {
	isolateEnv(t)
	root := NewRootCommand()
	var stderr bytes.Buffer
	root.SetOut(new(bytes.Buffer))
	root.SetErr(&stderr)
	root.SetArgs([]string{"sessions", "get"})

	err := root.Execute()
	if err == nil {
		t.Fatal("expected a usage error")
	}
	if code := exitCode(err); code != exitUsage {
		t.Fatalf("exitCode = %d, want %d", code, exitUsage)
	}
	printed := captureStderr(t, func() { exitCode(err) })
	want := "error: accepts 1 arg(s), received 0\nhint: run 'kha sessions get --help' for more information\n"
	if printed != want {
		t.Errorf("stderr = %q, want %q", printed, want)
	}
	for _, unwanted := range []string{"Usage:", "Get one Session", "Arguments:", "is the Session id"} {
		if strings.Contains(printed, unwanted) {
			t.Errorf("stderr = %q, must not contain %q", printed, unwanted)
		}
	}
	if stderr.Len() != 0 {
		t.Errorf("cobra writer received usage error output: %q", stderr.String())
	}
}

func TestAgentsListTable(t *testing.T) {
	isolateEnv(t)
	f := &fakeAPI{t: t}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	stdout, _, err := runCmd(t, "", "agents", "list", "--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ID", "NAME", "VERSION", "MODEL", "agent_1", "demo", "kimi-k2"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout)
		}
	}
	if f.lastAuth != "Bearer sk-test" {
		t.Errorf("Authorization = %q, want Bearer sk-test", f.lastAuth)
	}
	if f.lastVer == "" {
		t.Error("kimi-api-version header not sent")
	}
}

func TestAgentsListJSON(t *testing.T) {
	isolateEnv(t)
	f := &fakeAPI{t: t}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	stdout, _, err := runCmd(t, "", "agents", "list", "-o", "json", "--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, `"items"`) || !strings.Contains(stdout, `"agent_1"`) {
		t.Fatalf("json output malformed:\n%s", stdout)
	}
	if strings.Contains(stdout, "NAME") {
		t.Fatalf("json output contains table headers:\n%s", stdout)
	}
}

func TestAgentsCreateWithFlags(t *testing.T) {
	isolateEnv(t)
	f := &fakeAPI{t: t}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	_, _, err := runCmd(t, "", "agents", "create", "--name", "created", "--model", "kimi-k2",
		"--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	body := string(f.lastBody)
	if !strings.Contains(body, `"name":"created"`) || !strings.Contains(body, `"model":"kimi-k2"`) {
		t.Fatalf("create body = %s", body)
	}
}

func TestAgentsCreateFromStdinFile(t *testing.T) {
	isolateEnv(t)
	f := &fakeAPI{t: t}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	stdin := `{"name":"from-file","model":"kimi-k2","description":"d"}`
	_, _, err := runCmd(t, stdin, "agents", "create", "--file", "-", "--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if body := string(f.lastBody); !strings.Contains(body, `"name":"from-file"`) {
		t.Fatalf("create body = %s", body)
	}
}

// TestFilesListPaginates covers files list paging: a plain list forwards
// page_size/page_token, and --all drains every page through Files.All.
func TestFilesListPaginates(t *testing.T) {
	isolateEnv(t)
	var sawSize, sawToken string
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/files" {
			http.NotFound(w, r)
			return
		}
		sawSize, sawToken = r.URL.Query().Get("page_size"), r.URL.Query().Get("page_token")
		if sawToken == "tok2" {
			_, _ = fmt.Fprint(w, `{"items":[{"id":"file_2","filename":"b.csv","bytes":7,"created_at":"2026-08-02T10:00:00Z"}]}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"items":[{"id":"file_1","filename":"a.csv","bytes":5,"created_at":"2026-08-01T10:00:00Z"}],"next_page_token":"tok2"}`)
	})

	stdout, _, err := runCmd(t, "", "files", "list", "--all", "--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "file_1") || !strings.Contains(stdout, "file_2") {
		t.Fatalf("--all table missing files from both pages:\n%s", stdout)
	}

	stdout, _, err = runCmd(t, "", "files", "list", "--limit", "10", "--page-token", "tok2", "--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if sawSize != "10" || sawToken != "tok2" {
		t.Errorf("server saw page_size=%q page_token=%q, want 10/tok2", sawSize, sawToken)
	}
	if !strings.Contains(stdout, "file_2") || strings.Contains(stdout, "file_1") {
		t.Errorf("single-page table should hold only page 2:\n%s", stdout)
	}
}

// TestTriggersCreateEnvironmentRequired: environment_id is required on
// CreateTriggerRequest, so the flag-mode create demands --environment and
// sends it.
func TestTriggersCreateEnvironmentRequired(t *testing.T) {
	isolateEnv(t)
	_, _, err := runCmd(t, "", "triggers", "create", "--name", "nightly", "--agent", "agent_1", "--cron", "0 2 * * *")
	if err == nil {
		t.Fatal("expected a usage error without --environment")
	}
	if code := exitCode(err); code != exitUsage {
		t.Errorf("exitCode = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(err.Error(), "--environment") {
		t.Errorf("err = %v, want it to name --environment", err)
	}

	rec := &bodyRecorder{}
	srv := serve(t, rec.wrap(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/triggers" {
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, `{"id":"trig_1","name":"nightly","status":"active","agent_id":"agent_1","environment_id":"env_1","session_mode":"new_session","policy":{"type":"cron","expression":"0 2 * * *","timezone":"UTC"},"created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-01T10:00:00Z"}`)
			return
		}
		http.NotFound(w, r)
	}))
	_, _, err = runCmd(t, "", "triggers", "create", "--name", "nightly", "--agent", "agent_1", "--environment", "env_1",
		"--cron", "0 2 * * *", "--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if body := rec.body; !strings.Contains(body, `"environment_id":"env_1"`) {
		t.Fatalf("create body = %s, want environment_id", body)
	}
}

// TestSessionsCreateEnvironmentRequired: the platform provides no default
// Environment, so the flag-mode create demands --environment (aligned with
// triggers create) and sends it.
func TestSessionsCreateEnvironmentRequired(t *testing.T) {
	isolateEnv(t)
	_, _, err := runCmd(t, "", "sessions", "create", "--agent", "agent_1")
	if err == nil {
		t.Fatal("expected a usage error without --environment")
	}
	if code := exitCode(err); code != exitUsage {
		t.Errorf("exitCode = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(err.Error(), "--environment") {
		t.Errorf("err = %v, want it to name --environment", err)
	}

	rec := &bodyRecorder{}
	srv := serve(t, rec.wrap(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/sessions" {
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, `{"id":"sess_1","agent_id":"agent_1","environment_id":"env_1","status":"idle","created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-01T10:00:00Z"}`)
			return
		}
		http.NotFound(w, r)
	}))
	_, _, err = runCmd(t, "", "sessions", "create", "--agent", "agent_1", "--environment", "env_1",
		"--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if body := rec.body; !strings.Contains(body, `"environment_id":"env_1"`) {
		t.Fatalf("create body = %s, want environment_id", body)
	}
}

func TestAPIErrorSurfaces(t *testing.T) {
	isolateEnv(t)
	f := &fakeAPI{t: t}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	_, _, err := runCmd(t, "", "agents", "get", "boom", "--base-url", srv.URL, "--api-key", "sk-test")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(apiErrorMessage(err), "bad key") {
		t.Fatalf("error = %v, want the API message", err)
	}
	if code := exitCode(err); code != exitError {
		t.Fatalf("exitCode = %d, want %d", code, exitError)
	}
}

// TestNoCredentialsStillSendsRequest: without any credential the command
// sends the request without an Authorization header instead of refusing to
// run, and stays quiet about it — anonymous mode is normal operation against
// test deployments.
func TestNoCredentialsStillSendsRequest(t *testing.T) {
	isolateEnv(t)
	f := &fakeAPI{t: t}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	stdout, stderr, err := runCmd(t, "", "agents", "list", "--base-url", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "agent_1") {
		t.Errorf("stdout missing the listed agent:\n%s", stdout)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want silent anonymous mode", stderr)
	}
	if f.lastAuth != "" {
		t.Errorf("Authorization = %q, want no header without credentials", f.lastAuth)
	}
}

// TestAnonymousGets401: an unauthenticated request against an
// authenticating endpoint surfaces the API's 401 as a plain runtime error
// (exit 1).
func TestAnonymousGets401(t *testing.T) {
	isolateEnv(t)
	f := &fakeAPI{t: t}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	_, stderr, err := runCmd(t, "", "agents", "get", "boom", "--base-url", srv.URL)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(apiErrorMessage(err), "bad key") {
		t.Fatalf("error = %v, want the API message", err)
	}
	if code := exitCode(err); code != exitError {
		t.Fatalf("exitCode = %d, want %d", code, exitError)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want silent anonymous mode", stderr)
	}
}

func TestVersionCommand(t *testing.T) {
	isolateEnv(t)
	stdout, _, err := runCmd(t, "", "version")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, version) {
		t.Fatalf("version output = %q, want %q", stdout, version)
	}
}

func TestLoginWhoamiLogoutFlow(t *testing.T) {
	isolateEnv(t)
	f := &fakeAPI{t: t}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	// login writes a 0600 config for profile demo.
	if _, _, err := runCmd(t, "", "login", "--api-key", "sk-demo", "--profile", "demo", "--base-url", srv.URL); err != nil {
		t.Fatal(err)
	}
	dir, _ := os.ReadDir(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "kha"))
	if len(dir) != 1 {
		t.Fatalf("config dir = %v", dir)
	}
	p := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "kha", "config.yaml")
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Fatalf("config mode = %04o, want 0600", perm)
	}

	// whoami resolves everything from the stored profile (no flags).
	stdout, _, err := runCmd(t, "", "whoami")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"demo", srv.URL, "req_test_1", "2026-09-01-beta"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("whoami output missing %q:\n%s", want, stdout)
		}
	}
	if f.lastAuth != "Bearer sk-demo" {
		t.Errorf("whoami used Authorization = %q, want the stored key", f.lastAuth)
	}

	// logout removes the profile; whoami then runs anonymously — it still
	// probes successfully, just without an Authorization header and without
	// any note about it.
	if _, _, err := runCmd(t, "", "logout", "--profile", "demo"); err != nil {
		t.Fatal(err)
	}
	_, stderr, err := runCmd(t, "", "whoami", "--base-url", srv.URL)
	if err != nil {
		t.Fatalf("whoami after logout: %v", err)
	}
	if stderr != "" {
		t.Errorf("whoami after logout: stderr = %q, want silent anonymous mode", stderr)
	}
	if f.lastAuth != "" {
		t.Errorf("whoami after logout used Authorization = %q, want no header", f.lastAuth)
	}
}

// TestExplicitProfileMustExist: a profile named via --profile or
// KHA_PROFILE that is not in the config is a usage error (exit 2) pointing
// at login, never a silent anonymous downgrade. With no profile named,
// anonymous mode still runs (covered by TestNoCredentialsStillSendsRequest).
func TestExplicitProfileMustExist(t *testing.T) {
	isolateEnv(t)

	_, _, err := runCmd(t, "", "agents", "list", "--profile", "ghost")
	if err == nil {
		t.Fatal("expected a usage error for a missing --profile")
	}
	if code := exitCode(err); code != exitUsage {
		t.Errorf("--profile ghost: exitCode = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(err.Error(), `"ghost"`) || !strings.Contains(err.Error(), "login") {
		t.Errorf("--profile ghost: err = %v, want it to name the profile and login", err)
	}

	t.Setenv("KHA_PROFILE", "ghost")
	_, _, err = runCmd(t, "", "agents", "list")
	if err == nil {
		t.Fatal("expected a usage error for a missing KHA_PROFILE")
	}
	if code := exitCode(err); code != exitUsage {
		t.Errorf("KHA_PROFILE ghost: exitCode = %d, want %d", code, exitUsage)
	}
}

// TestLoginAPIKeyStdin: the API key can come from stdin instead of argv;
// the stored profile holds exactly the piped key (line terminator
// stripped), and neither stdout nor stderr echoes it.
func TestLoginAPIKeyStdin(t *testing.T) {
	isolateEnv(t)

	stdout, stderr, err := runCmd(t, "sk-stdin-secret\n", "login", "--api-key-stdin", "--profile", "demo")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout+stderr, "sk-stdin-secret") {
		t.Errorf("the key was echoed: stdout = %q, stderr = %q", stdout, stderr)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	prof := cfg.GetProfile("demo")
	if prof == nil || prof.APIKey != "" || prof.APIKeyRef == "" {
		t.Fatalf("profile must hold only a keychain reference: %+v", prof)
	}
	if key, err := savedAPIKey(prof.APIKeyRef); err != nil || key != "sk-stdin-secret" {
		t.Fatal("piped key was not stored in keychain")
	}

	// --api-key and --api-key-stdin reject each other (usage error).
	_, _, err = runCmd(t, "", "login", "--api-key", "sk-x", "--api-key-stdin")
	if err == nil {
		t.Fatal("expected a usage error combining --api-key and --api-key-stdin")
	}
	if code := exitCode(err); code != exitUsage {
		t.Errorf("exitCode = %d, want %d", code, exitUsage)
	}
}

func TestSessionsSend(t *testing.T) {
	isolateEnv(t)
	f := &fakeAPI{t: t}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	_, stderr, err := runCmd(t, "", "sessions", "send", "sess_1", "hello there",
		"--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "message accepted") {
		t.Fatalf("stderr = %q", stderr)
	}
	if body := string(f.lastBody); !strings.Contains(body, `"user.message"`) || !strings.Contains(body, "hello there") {
		t.Fatalf("send body = %s", body)
	}
	if got := strings.Join(f.requests, ", "); got != "POST /v1/sessions/sess_1/events" {
		t.Errorf("requests = %s; want only the events POST", got)
	}
}

// TestEveryCommandHelpRenders walks the whole tree rendering --help: it
// exercises cobra's flag-merge path on every command, which is where local
// shorthands colliding with the persistent -o would panic.
func TestEveryCommandHelpRenders(t *testing.T) {
	var walk func(cmd *cobra.Command)
	walk = func(cmd *cobra.Command) {
		for _, sub := range cmd.Commands() {
			walk(sub)
		}
		if cmd.RunE == nil && !cmd.HasSubCommands() {
			t.Errorf("%s has no runner", cmd.CommandPath())
			return
		}
		if err := cmd.Help(); err != nil {
			t.Errorf("%s: Help: %v", cmd.CommandPath(), err)
		}
	}
	root := NewRootCommand()
	root.SetOut(io.Discard)
	walk(root)
}

// TestFilesUploadStreamsFile covers the upload wiring end to end: the
// command opens the local file and streams its handle through the SDK, so
// the server receives a multipart form carrying the basename, the purpose
// field, and the exact file bytes.
func TestFilesUploadStreamsFile(t *testing.T) {
	isolateEnv(t)
	var gotBody []byte
	var gotCT string
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		// The upload is 201 per the spec; the SDK decodes the File only on
		// the declared status.
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, `{"id":"file_1","filename":"up.txt","bytes":11,"purpose":"image","created_at":"2026-08-01T10:00:00Z"}`)
	})
	dir := t.TempDir()
	path := filepath.Join(dir, "up.txt")
	if err := os.WriteFile(path, []byte("hello world"), 0o666); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := runCmd(t, "", "files", "upload", "--file", path, "--purpose", "image",
		"--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "file_1") {
		t.Errorf("stdout = %q, want the uploaded file id", stdout)
	}
	if !strings.HasPrefix(gotCT, "multipart/form-data; boundary=") {
		t.Errorf("Content-Type = %q", gotCT)
	}
	body := string(gotBody)
	for _, want := range []string{`name="purpose"`, "image", `name="file"; filename="up.txt"`, "hello world"} {
		if !strings.Contains(body, want) {
			t.Errorf("multipart body missing %q:\n%s", want, body)
		}
	}
}

// TestSkillsCreateStreamsFile covers the same file-handle wiring on the
// skills path: the bundle streams from disk into the multipart file part.
func TestSkillsCreateStreamsFile(t *testing.T) {
	isolateEnv(t)
	var gotBody []byte
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		// Skill creation is 201 per the spec; the SDK decodes the result
		// only on the declared status.
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, `{"skill":{"id":"skill_1","name":"demo","created_at":"2026-08-01T10:00:00Z"},"version":{"id":"skv_1","version":"1"}}`)
	})
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.skill")
	if err := os.WriteFile(path, []byte("zip-bytes"), 0o666); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := runCmd(t, "", "skills", "create", path, "--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "skill_1") {
		t.Errorf("stdout = %q, want the created skill id", stdout)
	}
	body := string(gotBody)
	for _, want := range []string{`name="file"; filename="demo.skill"`, "zip-bytes"} {
		if !strings.Contains(body, want) {
			t.Errorf("multipart body missing %q:\n%s", want, body)
		}
	}
}

// TestFilesDownloadSanitizesServerName: the default download destination is
// the server-provided name stripped to its basename (a "../../etc/passwd"
// shape cannot escape the working directory), and an existing destination
// is never silently overwritten without --force.
func TestFilesDownloadSanitizesServerName(t *testing.T) {
	isolateEnv(t)
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/files/file_1":
			_, _ = fmt.Fprint(w, `{"id":"file_1","filename":"../../etc/passwd","bytes":12,"created_at":"2026-08-01T10:00:00Z"}`)
		case "/v1/files/file_1/content":
			_, _ = fmt.Fprint(w, "file-content")
		default:
			http.NotFound(w, r)
		}
	})
	dir := t.TempDir()
	t.Chdir(dir)

	_, stderr, err := runCmd(t, "", "files", "download", "file_1", "--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "passwd"))
	if err != nil {
		t.Fatalf("download did not land at the basename: %v (stderr: %s)", err, stderr)
	}
	if string(data) != "file-content" {
		t.Errorf("downloaded content = %q", data)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("working dir holds %d entries, want exactly the one basename file", len(entries))
	}

	// Without --force the existing destination is refused (exit 1).
	_, _, err = runCmd(t, "", "files", "download", "file_1", "--base-url", srv.URL, "--api-key", "sk-test")
	if err == nil {
		t.Fatal("expected an error overwriting without --force")
	}
	if code := exitCode(err); code != exitError {
		t.Errorf("exitCode = %d, want %d", code, exitError)
	}
	if !strings.Contains(err.Error(), "already exists") || !strings.Contains(err.Error(), "--force") {
		t.Errorf("err = %v, want the already-exists hint naming --force", err)
	}

	// --force overwrites.
	if _, _, err := runCmd(t, "", "files", "download", "file_1", "--force", "--base-url", srv.URL, "--api-key", "sk-test"); err != nil {
		t.Fatalf("--force download: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "passwd")); err != nil || string(data) != "file-content" {
		t.Errorf("after --force: content = %q, err = %v", data, err)
	}
}

// TestArtifactsDownloadSanitizesServerName covers the same guard on the
// Artifact path (server-chosen name stripped to its basename).
func TestArtifactsDownloadSanitizesServerName(t *testing.T) {
	isolateEnv(t)
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/artifacts/art_1":
			_, _ = fmt.Fprint(w, `{"id":"art_1","name":"../../evil.sh","version":"1","size_bytes":10,"delivered_at":"2026-08-01T10:00:00Z"}`)
		case "/v1/artifacts/art_1/content":
			_, _ = fmt.Fprint(w, "artifact!!")
		default:
			http.NotFound(w, r)
		}
	})
	dir := t.TempDir()
	t.Chdir(dir)

	if _, _, err := runCmd(t, "", "artifacts", "download", "art_1", "--base-url", srv.URL, "--api-key", "sk-test"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "evil.sh"))
	if err != nil {
		t.Fatalf("download did not land at the basename: %v", err)
	}
	if string(data) != "artifact!!" {
		t.Errorf("downloaded content = %q", data)
	}
	if _, _, err := runCmd(t, "", "artifacts", "download", "art_1", "--base-url", srv.URL, "--api-key", "sk-test"); err == nil ||
		!strings.Contains(err.Error(), "already exists") {
		t.Errorf("second download without --force: err = %v, want already-exists", err)
	}
}

func TestExitCodeMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, 0},
		{"runtime", &runtimeError{err: errors.New("boom")}, exitError},
		{"usage inside run", &runtimeError{err: errUsage("bad flag combo")}, exitUsage},
		{"interrupted", errInterrupted, exitInterrupted},
	}
	for _, c := range cases {
		if got := exitCode(c.err); got != c.want {
			t.Errorf("%s: exitCode = %d, want %d", c.name, got, c.want)
		}
	}
	// A cobra-level failure (unknown command) is a usage error.
	_, _, err := runCmd(t, "", "nosuchresource")
	if code := exitCode(err); code != exitUsage {
		t.Errorf("unknown command: exitCode = %d, want %d", code, exitUsage)
	}
}

// TestArtifactsDownloadVersionNamesFileAfterThatVersion: with --version, the
// default destination name comes from THAT version's metadata (the Get and
// the content request both carry the version).
func TestArtifactsDownloadVersionNamesFileAfterThatVersion(t *testing.T) {
	isolateEnv(t)
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/artifacts/art_1":
			if r.URL.Query().Get("version") == "2" {
				_, _ = fmt.Fprint(w, `{"id":"art_1","name":"report-v2.pdf","version":"2","size_bytes":7,"delivered_at":"2026-08-02T10:00:00Z"}`)
			} else {
				_, _ = fmt.Fprint(w, `{"id":"art_1","name":"report-v3.pdf","version":"3","size_bytes":7,"delivered_at":"2026-08-03T10:00:00Z"}`)
			}
		case "/v1/artifacts/art_1/content":
			if got := r.URL.Query().Get("version"); got != "2" {
				t.Errorf("content request version = %q, want 2", got)
			}
			_, _ = fmt.Fprint(w, "v2bytes")
		default:
			http.NotFound(w, r)
		}
	})
	dir := t.TempDir()
	t.Chdir(dir)

	if _, _, err := runCmd(t, "", "artifacts", "download", "art_1", "--version", "2", "--base-url", srv.URL, "--api-key", "sk-test"); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "report-v2.pdf")); err != nil {
		t.Fatalf("download did not land at the requested version's name: %v", err)
	} else if string(data) != "v2bytes" {
		t.Errorf("content = %q", data)
	}
	if _, err := os.Stat(filepath.Join(dir, "report-v3.pdf")); !os.IsNotExist(err) {
		t.Errorf("the latest version's name must not be used with --version")
	}
}

// TestWriteToFileRemovesPartialFileOnError locks the download-side cleanup:
// a body that fails mid-stream leaves no file behind.
func TestWriteToFileRemovesPartialFileOnError(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "partial.bin")
	src := io.MultiReader(strings.NewReader("first half"), errReader{})
	if _, err := writeToFile(dest, src, false); err == nil {
		t.Fatal("expected a write error")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("partial file survived the failed write: stat err = %v", err)
	}
}

// errReader fails after yielding nothing.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("source exploded") }

// TestSessionsSendWaitNeverClaimsAcceptanceBeforeSend locks the note
// ordering: a rejected send must not print "message accepted".
func TestSessionsSendWaitNeverClaimsAcceptanceBeforeSend(t *testing.T) {
	isolateEnv(t)
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/sessions/sess_1":
			_, _ = fmt.Fprint(w, `{"id":"sess_1","agent_id":"agent_1","environment_id":"env_1","status":"terminated","created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-01T10:00:00Z"}`)
		case "/v1/sessions/sess_1/events":
			w.WriteHeader(http.StatusConflict)
			_, _ = fmt.Fprint(w, `{"error":{"type":"state_conflict_error","message":"session is archived","request_id":"req_1"}}`)
		default:
			http.NotFound(w, r)
		}
	})
	_, stderr, err := runCmd(t, "", "sessions", "send", "sess_1", "hello", "--wait", "--base-url", srv.URL, "--api-key", "sk-test")
	if err == nil {
		t.Fatal("expected the rejected send to fail")
	}
	if strings.Contains(stderr, "message accepted") {
		t.Errorf("stderr claims acceptance although the send was rejected: %q", stderr)
	}
}

// TestSessionsListFilterFlags covers the list filters beyond paging:
// --order, --agent-version, and --status map onto ListSessionsParams.
func TestSessionsListFilterFlags(t *testing.T) {
	isolateEnv(t)
	var gotQuery string
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = fmt.Fprint(w, `{"items":[]}`)
	})
	if _, _, err := runCmd(t, "", "sessions", "list",
		"--order", "created_at", "--agent", "agent_1", "--agent-version", "2",
		"--status", "idle, running", "--include-archived",
		"--base-url", srv.URL, "--api-key", "sk-test"); err != nil {
		t.Fatal(err)
	}
	q, err := url.ParseQuery(gotQuery)
	if err != nil {
		t.Fatalf("recorded query %q: %v", gotQuery, err)
	}
	for k, want := range map[string]string{
		"order":            "created_at",
		"agent_id":         "agent_1",
		"agent_version":    "2",
		"statuses":         "idle,running",
		"include_archived": "true",
	} {
		if got := q.Get(k); got != want {
			t.Errorf("query %s = %q, want %q (raw: %s)", k, got, want, gotQuery)
		}
	}
}

// captureStderr redirects os.Stderr for the duration of fn and returns
// everything written — the exitCode error path prints to the real os.Stderr,
// not the cobra writer. Tests using it must not run in parallel.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = old }()
	fn()
	_ = w.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// TestNoteStripsTerminalEscapes: a server-provided file name carrying an
// OSC 52 (clipboard write) sequence is interpolated into the download
// confirmation note — it must reach stderr stripped, while the readable
// text survives.
func TestNoteStripsTerminalEscapes(t *testing.T) {
	isolateEnv(t)
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/files/file_1":
			// The filename carries an OSC 52 (clipboard write) sequence; json.Marshal escapes it into valid JSON.
			meta, _ := json.Marshal(map[string]any{"id": "file_1", "filename": "evil\x1b]52;c;aGk=\a.txt", "bytes": 7, "purpose": "image", "created_at": "2026-08-01T10:00:00Z"})
			_, _ = w.Write(meta)
		case "/v1/files/file_1/content":
			_, _ = fmt.Fprint(w, "payload")
		default:
			http.NotFound(w, r)
		}
	})
	t.Chdir(t.TempDir())

	_, stderr, err := runCmd(t, "", "files", "download", "file_1", "--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stderr, "\x1b") || strings.Contains(stderr, "\a") {
		t.Errorf("escape sequence leaked into the note: %q", stderr)
	}
	if !strings.Contains(stderr, "downloaded to evil.txt") {
		t.Errorf("stripped destination missing from the note: %q", stderr)
	}
}

// TestExitCodePrintStripsTerminalEscapes covers the same hardening on the
// top-level error path: an APIError message carrying OSC 52 prints through
// exitCode stripped.
func TestExitCodePrintStripsTerminalEscapes(t *testing.T) {
	isolateEnv(t)
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		env, _ := json.Marshal(map[string]any{"error": map[string]any{"type": "invalid_request_error", "message": "bad \x1b]52;c;aGk=\arequest", "request_id": "req_x"}})
		_, _ = w.Write(env)
	})
	_, _, err := runCmd(t, "", "agents", "get", "agent_1", "--base-url", srv.URL, "--api-key", "sk-test")
	if err == nil {
		t.Fatal("want the API error")
	}
	printed := captureStderr(t, func() {
		if code := exitCode(err); code != exitError {
			t.Errorf("exitCode = %d, want %d", code, exitError)
		}
	})
	if strings.Contains(printed, "\x1b") || strings.Contains(printed, "\a") {
		t.Errorf("escape sequence leaked into the error print: %q", printed)
	}
	if !strings.Contains(printed, "error:") || !strings.Contains(printed, "bad request") {
		t.Errorf("stripped message missing from the error print: %q", printed)
	}
}

// TestSessionsInterrupt sends user.interrupt directly to the events endpoint;
// Ctrl-C on attach only detaches the local stream.
func TestSessionsInterrupt(t *testing.T) {
	isolateEnv(t)
	f := &fakeAPI{t: t}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	_, stderr, err := runCmd(t, "", "sessions", "interrupt", "sess_1", "--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if body := string(f.lastBody); !strings.Contains(body, `"type":"user.interrupt"`) {
		t.Fatalf("interrupt body = %s", body)
	}
	if !strings.Contains(stderr, "session sess_1 interrupted") {
		t.Errorf("stderr = %q, want the confirmation note", stderr)
	}
	if got := strings.Join(f.requests, ", "); got != "POST /v1/sessions/sess_1/events" {
		t.Errorf("requests = %s; want only the events POST", got)
	}
}

// TestAPIVersionOverride covers --api-version and the profile's api_version:
// the flag wins, the profile stored at login is the fallback, and whoami
// reports the effective version.
func TestAPIVersionOverride(t *testing.T) {
	isolateEnv(t)
	f := &fakeAPI{t: t}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	if _, _, err := runCmd(t, "", "agents", "list", "--api-version", "2099-01-01-test", "--base-url", srv.URL, "--api-key", "sk-test"); err != nil {
		t.Fatal(err)
	}
	if f.lastVer != "2099-01-01-test" {
		t.Errorf("kimi-api-version = %q, want the --api-version flag value", f.lastVer)
	}

	// The profile stores the version at login and applies it without the flag.
	if _, _, err := runCmd(t, "", "login", "--api-key", "sk-demo", "--profile", "demo", "--base-url", srv.URL, "--api-version", "2099-02-02-pro"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCmd(t, "", "agents", "list"); err != nil {
		t.Fatal(err)
	}
	if f.lastVer != "2099-02-02-pro" {
		t.Errorf("kimi-api-version = %q, want the profile's api_version", f.lastVer)
	}
	stdout, _, err := runCmd(t, "", "whoami")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "2099-02-02-pro") {
		t.Errorf("whoami missing the effective api version:\n%s", stdout)
	}
}

// TestAgentsListYAML covers -o yaml on a list page: the same data as
// -o json rendered as YAML — the page object with its items, not a table.
func TestAgentsListYAML(t *testing.T) {
	isolateEnv(t)
	f := &fakeAPI{t: t}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	stdout, _, err := runCmd(t, "", "agents", "list", "-o", "yaml", "--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"items:", "id: agent_1", "name: demo", "model: kimi-k2"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("yaml output missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "NAME") {
		t.Errorf("yaml output contains table headers:\n%s", stdout)
	}
}

// TestSessionsGetYAML covers -o yaml on a single object.
func TestSessionsGetYAML(t *testing.T) {
	isolateEnv(t)
	f := &fakeAPI{t: t}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	stdout, _, err := runCmd(t, "", "sessions", "get", "sess_1", "-o", "yaml", "--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"id: sess_1", "agent_id: agent_1", "status: idle"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("yaml output missing %q:\n%s", want, stdout)
		}
	}
}

// TestInvalidOutputFormat covers the -o validation: an unknown format is a
// usage error naming the three accepted ones.
func TestInvalidOutputFormat(t *testing.T) {
	isolateEnv(t)
	_, _, err := runCmd(t, "", "agents", "list", "-o", "xml")
	if err == nil {
		t.Fatal("expected an error for -o xml")
	}
	if code := exitCode(err); code != exitUsage {
		t.Errorf("exitCode = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(err.Error(), "want table, json, yaml, or jsonl") {
		t.Errorf("err = %v, want it to name the accepted formats", err)
	}
}

func TestWriteToFileForcePreservesDestinationOnFailure(t *testing.T) {
	for _, existing := range []bool{false, true} {
		dir := t.TempDir()
		dest := filepath.Join(dir, "download.bin")
		if existing {
			if err := os.WriteFile(dest, []byte("original"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		src := io.MultiReader(strings.NewReader("partial"), errReader{})
		if _, err := writeToFile(dest, src, true); err == nil {
			t.Fatal("expected copy failure")
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if existing {
			data, err := os.ReadFile(dest)
			if err != nil || string(data) != "original" || len(entries) != 1 {
				t.Fatalf("original changed: %q, %v; entries %v", data, err, entries)
			}
		} else if len(entries) != 0 {
			t.Fatalf("partial files: %v", entries)
		}
	}
}
