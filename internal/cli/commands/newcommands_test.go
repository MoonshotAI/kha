package commands

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoonshotAI/kha"

	json "encoding/json/v2"

	"github.com/MoonshotAI/kha/internal/cli/output"
)

// serve runs one scripted handler for a command test, defaulting the
// response Content-Type to application/json (handlers for other media set
// their own afterwards).
func serve(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// lastBody records the most recent request body.
type bodyRecorder struct{ body string }

func (b *bodyRecorder) wrap(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			data, _ := io.ReadAll(r.Body)
			b.body = string(data)
		}
		h(w, r)
	}
}

func TestSessionsEventsTable(t *testing.T) {
	isolateEnv(t)
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/sess_1/events" {
			_, _ = fmt.Fprint(w, `{"items":[
				{"id":"sevt_1","processed_at":"2026-08-01T10:00:00Z","type":"user.message","data":{"content":[{"type":"text","text":"hello world"}]}},
				{"id":"sevt_2","processed_at":"2026-08-01T10:01:00Z","type":"session.status","data":{"status":"idle","stop_reason":{"type":"end_turn"}}}
			],"stream_cursor":"cur_1"}`)
			return
		}
		http.NotFound(w, r)
	})

	stdout, _, err := runCmd(t, "", "sessions", "events", "sess_1", "--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"PROCESSED_AT", "ID", "TYPE", "SUMMARY", "sevt_1", "user.message", "hello world", "sevt_2", "session.status", "idle"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout)
		}
	}
	if !strings.HasSuffix(stdout, "CURSOR cur_1\n") {
		t.Errorf("stdout missing the page cursor:\n%s", stdout)
	}
}

// TestSessionsEventsAllDrainsPages covers --all: the iterator follows
// next_page_token and the table lists both pages' events; -o json prints one
// aggregated array.
func TestSessionsEventsAllDrainsPages(t *testing.T) {
	isolateEnv(t)
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/sessions/sess_1/events" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("page_token") == "tok2" {
			_, _ = fmt.Fprint(w, `{"items":[{"id":"sevt_2","processed_at":"2026-08-01T10:01:00Z","type":"session.status","data":{"status":"running"}}],"stream_cursor":"cur_2"}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"items":[{"id":"sevt_1","processed_at":"2026-08-01T10:00:00Z","type":"user.message","data":{"content":[{"type":"text","text":"hi"}]}}],"next_page_token":"tok2","stream_cursor":"cur_1"}`)
	})

	stdout, _, err := runCmd(t, "", "sessions", "events", "sess_1", "--all", "--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "sevt_1") || !strings.Contains(stdout, "sevt_2") {
		t.Fatalf("--all table missing events from both pages:\n%s", stdout)
	}
	if strings.Contains(stdout, "CURSOR ") {
		t.Errorf("--all table must not include a cursor footer:\n%s", stdout)
	}

	stdout, _, err = runCmd(t, "", "sessions", "events", "sess_1", "--all", "-o", "json", "--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, `"sevt_1"`) || !strings.Contains(stdout, `"sevt_2"`) || strings.Contains(stdout, "next_page_token") || strings.Contains(stdout, "CURSOR") {
		t.Fatalf("--all -o json is not one aggregated array without a footer:\n%s", stdout)
	}
}

// markerWriter is a thread-safe stdout stand-in that closes seen once the
// marker text has been written — the streaming tests' proof that --all
// output left the process before the server answered the next page.
type markerWriter struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	marker string
	seen   chan struct{}
	once   sync.Once
}

func newMarkerWriter(marker string) *markerWriter {
	return &markerWriter{marker: marker, seen: make(chan struct{})}
}

func (w *markerWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buf.Write(p)
	if strings.Contains(w.buf.String(), w.marker) {
		w.once.Do(func() { close(w.seen) })
	}
	return n, err
}

func (w *markerWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// pagedFilesHandler scripts a two-page files listing whose second page
// blocks until release closes: the streaming tests can then observe page
// one's output before page two is even answered.
func pagedFilesHandler(release <-chan struct{}) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/files" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("page_token") == "tok2" {
			<-release
			_, _ = fmt.Fprint(w, fileTestPage2JSON)
			return
		}
		_, _ = fmt.Fprint(w, fileTestPage1JSON)
	}
}

const (
	fileTestPage1JSON = `{"items":[{"id":"file_1","filename":"a.csv","bytes":5,"created_at":"2026-08-01T10:00:00Z"}],"next_page_token":"tok2"}`
	fileTestPage2JSON = `{"items":[{"id":"file_2","filename":"b.csv","bytes":7,"created_at":"2026-08-02T10:00:00Z"}]}`
)

// mustDecodeFile decodes the one file a fake page carries, so byte-identity
// assertions compare against the same models the CLI streamed.
func mustDecodeFile(t *testing.T, data string) kha.FileMetadata {
	t.Helper()
	var page struct {
		Items []kha.FileMetadata `json:"items"`
	}
	if err := json.Unmarshal([]byte(data), &page); err != nil || len(page.Items) != 1 {
		t.Fatalf("decoding the fake page: %v", err)
	}
	return page.Items[0]
}

// runStreaming starts a command against srv in a goroutine and returns once
// page one's marker has reached stdout (proving the output is streamed, not
// buffered) or the wait times out. release must unblock the server's second
// page; the returned finish waits for the command and yields its outcome.
func runStreaming(t *testing.T, out *markerWriter, release func(), args ...string) (finish func() (string, error)) {
	t.Helper()
	done := make(chan struct{})
	var stderr string
	var runErr error
	go func() {
		defer close(done)
		stderr, runErr = runCmdCtx(t, context.Background(), out, args...)
	}()
	select {
	case <-out.seen:
	case <-time.After(10 * time.Second):
		release()
		t.Fatalf("%v: page one's output was not printed before page two answered", args)
	}
	release()
	return func() (string, error) {
		<-done
		return stderr, runErr
	}
}

// TestFilesListAllStreamsRows proves printAll streams the table: with page
// two blocked server-side, page one's row is already on stdout (the old
// buffered printAll printed nothing until every page had arrived). The
// finished output holds both pages under one header.
func TestFilesListAllStreamsRows(t *testing.T) {
	isolateEnv(t)
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	srv := serve(t, pagedFilesHandler(release))
	out := newMarkerWriter("file_1")

	finish := runStreaming(t, out,
		func() { releaseOnce.Do(func() { close(release) }) },
		"files", "list", "--all", "--base-url", srv.URL, "--api-key", "sk-test")
	stderr, runErr := finish()
	if runErr != nil {
		t.Fatalf("files list --all: %v (stderr: %s)", runErr, stderr)
	}
	stdout := out.String()
	if lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n"); len(lines) != 3 {
		t.Errorf("table has %d lines, want 3 (one header + 2 rows):\n%s", len(lines), stdout)
	}
	if !strings.Contains(stdout, "file_1") || !strings.Contains(stdout, "file_2") {
		t.Errorf("--all table missing files from both pages:\n%s", stdout)
	}
}

// TestFilesListAllStreamsJSON proves -o json streams too (page one's array
// element prints before page two answers) and that the finished bytes equal
// the buffered PrintJSON of the whole slice exactly.
func TestFilesListAllStreamsJSON(t *testing.T) {
	isolateEnv(t)
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	srv := serve(t, pagedFilesHandler(release))
	out := newMarkerWriter(`"file_1"`)

	finish := runStreaming(t, out,
		func() { releaseOnce.Do(func() { close(release) }) },
		"files", "list", "--all", "-o", "json", "--base-url", srv.URL, "--api-key", "sk-test")
	stderr, runErr := finish()
	if runErr != nil {
		t.Fatalf("files list --all -o json: %v (stderr: %s)", runErr, stderr)
	}
	var want bytes.Buffer
	if err := output.PrintJSON(&want, []kha.FileMetadata{mustDecodeFile(t, fileTestPage1JSON), mustDecodeFile(t, fileTestPage2JSON)}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != want.String() {
		t.Errorf("streamed -o json differs from the buffered PrintJSON:\nstreamed:\n%s\nbuffered:\n%s", got, want.String())
	}
}

// TestFilesListAllStreamsYAML proves -o yaml streams (page one's sequence
// entry prints before page two answers) and stays byte-identical to the
// buffered PrintYAML of the whole slice.
func TestFilesListAllStreamsYAML(t *testing.T) {
	isolateEnv(t)
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	srv := serve(t, pagedFilesHandler(release))
	out := newMarkerWriter("file_1")

	finish := runStreaming(t, out,
		func() { releaseOnce.Do(func() { close(release) }) },
		"files", "list", "--all", "-o", "yaml", "--base-url", srv.URL, "--api-key", "sk-test")
	stderr, runErr := finish()
	if runErr != nil {
		t.Fatalf("files list --all -o yaml: %v (stderr: %s)", runErr, stderr)
	}
	var want bytes.Buffer
	if err := output.PrintYAML(&want, []kha.FileMetadata{mustDecodeFile(t, fileTestPage1JSON), mustDecodeFile(t, fileTestPage2JSON)}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != want.String() {
		t.Errorf("streamed -o yaml differs from the buffered PrintYAML:\nstreamed:\n%s\nbuffered:\n%s", got, want.String())
	}
}

// TestFilesListAllPageError: when a later page fails, the rows already
// streamed stay on stdout and the command exits 1 — partial output on
// error. (The fake 500 costs the transport's
// full retry budget, so this exercises the table format only; the JSON/YAML
// streams share the same mid-loop return.)
func TestFilesListAllPageError(t *testing.T) {
	isolateEnv(t)
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/files" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("page_token") == "tok2" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprint(w, `{"error":{"type":"server_error","message":"page two exploded","request_id":"req_test_1"}}`)
			return
		}
		_, _ = fmt.Fprint(w, fileTestPage1JSON)
	})

	stdout, _, err := runCmd(t, "", "files", "list", "--all", "--base-url", srv.URL, "--api-key", "sk-test")
	if err == nil {
		t.Fatal("expected an error")
	}
	if code := exitCode(err); code != exitError {
		t.Errorf("exitCode = %d, want %d", code, exitError)
	}
	if !strings.Contains(apiErrorMessage(err), "page two exploded") {
		t.Errorf("err = %v, want the page-two APIError", err)
	}
	if !strings.Contains(stdout, "file_1") {
		t.Errorf("the streamed page-one row must survive the page-two error:\n%s", stdout)
	}
	if strings.Contains(stdout, "file_2") {
		t.Errorf("page two must not appear:\n%s", stdout)
	}
}

func TestListAllRejectsPagingFlags(t *testing.T) {
	isolateEnv(t)
	for _, args := range [][]string{
		{"agents", "list", "--all", "--limit", "5"},
		{"agents", "list", "--all", "--page-token", "tok"},
	} {
		_, _, err := runCmd(t, "", args...)
		if err == nil {
			t.Fatalf("%v: expected an error", args)
		}
		if code := exitCode(err); code != exitUsage {
			t.Errorf("%v: exitCode = %d, want %d", args, code, exitUsage)
		}
		if !strings.Contains(err.Error(), "--all cannot be combined") {
			t.Errorf("%v: err = %v", args, err)
		}
	}
}

func TestSessionsResourcesListAndAdd(t *testing.T) {
	isolateEnv(t)
	rec := &bodyRecorder{}
	srv := serve(t, rec.wrap(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/sess_1/resources":
			_, _ = fmt.Fprint(w, `{"items":[{"type":"file","id":"sres_1","file_id":"file_1","path":"upload/a.csv"}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sessions/sess_1/resources":
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, `{"type":"file","id":"sres_2","file_id":"file_2","path":"upload/b.csv"}`)
		default:
			http.NotFound(w, r)
		}
	}))

	stdout, _, err := runCmd(t, "", "sessions", "resources", "list", "sess_1", "--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"sres_1", "file", "file_1", "upload/a.csv"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("resources list missing %q:\n%s", want, stdout)
		}
	}

	stdout, _, err = runCmd(t, "", "sessions", "resources", "add", "sess_1", "--file-id", "file_2", "--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rec.body, `"file_id":"file_2"`) || !strings.Contains(rec.body, `"type":"file"`) {
		t.Fatalf("add body = %s", rec.body)
	}
	if !strings.Contains(stdout, "sres_2") {
		t.Errorf("add output missing the new binding:\n%s", stdout)
	}
}

func TestSessionsThreadsList(t *testing.T) {
	isolateEnv(t)
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/sess_1/threads" {
			_, _ = fmt.Fprint(w, `{"items":[
				{"id":"sthr_1","session_id":"sess_1","status":"idle","agent":{"id":"agent_1","version":"3","name":"coordinator"},"created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-01T10:00:00Z"},
				{"id":"sthr_2","session_id":"sess_1","parent_thread_id":"sthr_1","status":"running","created_at":"2026-08-01T10:02:00Z","updated_at":"2026-08-01T10:02:00Z"}
			]}`)
			return
		}
		http.NotFound(w, r)
	})

	stdout, _, err := runCmd(t, "", "sessions", "threads", "list", "sess_1", "--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ID", "STATUS", "PARENT", "sthr_1", "coordinator@3", "sthr_2"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("threads list missing %q:\n%s", want, stdout)
		}
	}
}

// TestVaultsCredentialsCreateValueStdin covers the stdin secret path: the
// secret travels only in the request body, never in argv.
func TestVaultsCredentialsCreateValueStdin(t *testing.T) {
	isolateEnv(t)
	rec := &bodyRecorder{}
	srv := serve(t, rec.wrap(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/vaults/vault_1/credentials" {
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, `{"id":"vcrd_1","vault_id":"vault_1","display_name":"gh","auth":{"type":"environment_variable","secret_name":"GITHUB_TOKEN"},"created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-01T10:00:00Z"}`)
			return
		}
		http.NotFound(w, r)
	}))

	stdout, _, err := runCmd(t, "s3cr3t\n", "vaults", "credentials", "create", "vault_1",
		"--type", "environment_variable", "--name", "GITHUB_TOKEN", "--display-name", "gh", "--value-stdin",
		"--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"secret_name":"GITHUB_TOKEN"`, `"secret_value":"s3cr3t"`, `"display_name":"gh"`} {
		if !strings.Contains(rec.body, want) {
			t.Errorf("create body missing %s: %s", want, rec.body)
		}
	}
	if !strings.Contains(stdout, "vcrd_1") || !strings.Contains(stdout, "GITHUB_TOKEN") {
		t.Errorf("create output:\n%s", stdout)
	}
	if strings.Contains(stdout, "s3cr3t") {
		t.Errorf("the secret leaked into the output:\n%s", stdout)
	}
}

func TestVaultsCredentialsBatchFromStdin(t *testing.T) {
	isolateEnv(t)
	rec := &bodyRecorder{}
	srv := serve(t, rec.wrap(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/vaults/vault_1/credentials/batch" {
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, `{"items":[
				{"id":"vcrd_1","vault_id":"vault_1","display_name":"a","auth":{"type":"environment_variable","secret_name":"A"},"created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-01T10:00:00Z"},
				{"id":"vcrd_2","vault_id":"vault_1","display_name":"b","auth":{"type":"static_bearer","mcp_server_url":"https://mcp.example"},"created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-01T10:00:00Z"}
			]}`)
			return
		}
		http.NotFound(w, r)
	}))

	stdin := `{"items":[{"display_name":"a","auth":{"type":"environment_variable","secret_name":"A","secret_value":"1"}},{"display_name":"b","auth":{"type":"static_bearer","mcp_server_url":"https://mcp.example","token":"t"}}]}`
	stdout, _, err := runCmd(t, stdin, "vaults", "credentials", "batch", "vault_1", "--file", "-", "--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rec.body, `"secret_value":"1"`) {
		t.Fatalf("batch body = %s", rec.body)
	}
	if !strings.Contains(stdout, "vcrd_1") || !strings.Contains(stdout, "vcrd_2") {
		t.Errorf("batch output missing created credentials:\n%s", stdout)
	}
}

func TestVaultsCredentialsCheck(t *testing.T) {
	isolateEnv(t)
	rec := &bodyRecorder{}
	srv := serve(t, rec.wrap(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/vaults/credentials/check" {
			_, _ = fmt.Fprint(w, `{"coverage":[{"mcp_server_url":"https://mcp.example","matching_vault_ids":["vault_1"]}]}`)
			return
		}
		http.NotFound(w, r)
	}))

	stdout, _, err := runCmd(t, `{"mcp_server_urls":["https://mcp.example"]}`,
		"vaults", "credentials", "check", "--vault", "vault_1", "--file", "-", "--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rec.body, `"vault_ids":["vault_1"]`) {
		t.Fatalf("check body = %s, want the --vault restriction", rec.body)
	}
	if !strings.Contains(stdout, "https://mcp.example") || !strings.Contains(stdout, "vault_1") {
		t.Errorf("check output:\n%s", stdout)
	}
}

// TestVaultsOAuthCreateAndWait covers the flow: create prints the authorize
// URL; wait polls (short --interval) until authorized and prints the
// credential id.
func TestVaultsOAuthCreateAndWait(t *testing.T) {
	isolateEnv(t)
	gets := 0
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/vaults/vault_1/oauth-sessions":
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, `{"oauth_session_id":"voss_1","authorize_url":"https://auth.example/flow","mcp_server_url":"https://mcp.example"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/vaults/vault_1/oauth-sessions/voss_1":
			gets++
			if gets == 1 {
				_, _ = fmt.Fprint(w, `{"status":"pending"}`)
			} else {
				_, _ = fmt.Fprint(w, `{"status":"authorized","credential_id":"vcrd_1"}`)
			}
		default:
			http.NotFound(w, r)
		}
	})

	stdout, stderr, err := runCmd(t, "", "vaults", "oauth", "create", "vault_1", "--mcp-url", "https://mcp.example",
		"--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "https://auth.example/flow") || !strings.Contains(stdout, "voss_1") {
		t.Errorf("oauth create output:\n%s", stdout)
	}
	if !strings.Contains(stderr, "vaults oauth wait vault_1 voss_1") {
		t.Errorf("oauth create stderr = %q, want the wait hint", stderr)
	}

	stdout, stderr, err = runCmd(t, "", "vaults", "oauth", "wait", "vault_1", "voss_1", "--interval", "1ms",
		"--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "authorized") || !strings.Contains(stdout, "vcrd_1") {
		t.Errorf("oauth wait output:\n%s", stdout)
	}
	if !strings.Contains(stderr, "credential vcrd_1 created") {
		t.Errorf("oauth wait stderr = %q", stderr)
	}
}

func TestMemoriesCreateAndList(t *testing.T) {
	isolateEnv(t)
	rec := &bodyRecorder{}
	srv := serve(t, rec.wrap(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/memory-stores/mstr_1/memories":
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, `{"id":"mem_1","memory_store_id":"mstr_1","path":"notes/todo.md","content":"aGVsbG8=","content_sha256":"abc","content_size_bytes":5,"created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-01T10:00:00Z"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/memory-stores/mstr_1/memories":
			_, _ = fmt.Fprint(w, `{"items":[{"id":"mem_1","memory_store_id":"mstr_1","path":"notes/todo.md","content_sha256":"abc","content_size_bytes":5,"created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-01T10:00:00Z"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))

	stdout, _, err := runCmd(t, "", "memory-stores", "memories", "create", "mstr_1", "--path", "notes/todo.md", "--content", "hello",
		"--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	// The CLI base64-encodes --content into the request body.
	if !strings.Contains(rec.body, `"content":"aGVsbG8="`) || !strings.Contains(rec.body, `"path":"notes/todo.md"`) {
		t.Fatalf("create body = %s", rec.body)
	}
	if !strings.Contains(stdout, "mem_1") {
		t.Errorf("create output:\n%s", stdout)
	}

	stdout, _, err = runCmd(t, "", "memory-stores", "memories", "list", "mstr_1", "--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"PATH", "SIZE", "notes/todo.md", "5 B"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("memories list missing %q:\n%s", want, stdout)
		}
	}
}

func TestMemoryVersionsRedact(t *testing.T) {
	isolateEnv(t)
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/memory-stores/mstr_1/memories/mem_1/versions/mver_1/redact" {
			_, _ = fmt.Fprint(w, `{"id":"mver_1","memory_store_id":"mstr_1","memory_id":"mem_1","path":"notes/todo.md","operation":"modified","source":"api","created_at":"2026-08-01T10:00:00Z","redacted_at":"2026-08-02T10:00:00Z","revision":2}`)
			return
		}
		http.NotFound(w, r)
	})

	stdout, _, err := runCmd(t, "", "memory-stores", "memories", "versions", "redact", "mstr_1", "mem_1", "mver_1",
		"--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "mver_1") || !strings.Contains(stdout, "modified") {
		t.Errorf("redact output:\n%s", stdout)
	}
}

func TestSkillsVersionsAndContent(t *testing.T) {
	isolateEnv(t)
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/skills/skill_1/versions":
			_, _ = fmt.Fprint(w, `{"items":[{"skill_id":"skill_1","version":"3","name":"demo","description":"third","created_at":"2026-08-01T10:00:00Z"}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/skills/skill_1/versions/3/content":
			// The contract now serves the raw SKILL.md (text/markdown).
			w.Header().Set("Content-Type", "text/markdown")
			_, _ = fmt.Fprint(w, "BUNDLEBYTES")
		default:
			http.NotFound(w, r)
		}
	})

	stdout, _, err := runCmd(t, "", "skills", "versions", "skill_1", "--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"VERSION", "NAME", "3", "demo", "third"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("versions list missing %q:\n%s", want, stdout)
		}
	}

	stdout, _, err = runCmd(t, "", "skills", "content", "skill_1", "3", "--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "BUNDLEBYTES" {
		t.Errorf("content stdout = %q, want the raw bundle bytes", stdout)
	}

	out := filepath.Join(t.TempDir(), "bundle.zip")
	_, stderr, err := runCmd(t, "", "skills", "content", "skill_1", "3", "--out", out, "--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil || string(data) != "BUNDLEBYTES" {
		t.Fatalf("--out file = %q, %v", data, err)
	}
	if !strings.Contains(stderr, "downloaded to") {
		t.Errorf("stderr = %q, want the download note", stderr)
	}
}

func TestMemoryStoresDreamPolicyGetAndUpdate(t *testing.T) {
	isolateEnv(t)
	rec := &bodyRecorder{}
	srv := serve(t, rec.wrap(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/memory-stores/mstr_1/dream-policy":
			_, _ = fmt.Fprint(w, `{"memory_store_id":"mstr_1","activity":{"enabled":false},"schedule":{"enabled":false},"updated_at":"2026-08-01T10:00:00Z"}`)
		case r.Method == http.MethodPatch && r.URL.Path == "/v1/memory-stores/mstr_1/dream-policy":
			_, _ = fmt.Fprint(w, `{"memory_store_id":"mstr_1","activity":{"enabled":true,"min_changed_sessions":3},"schedule":{"enabled":false},"trigger_id":"trig_1","environment_id":"env_1","updated_at":"2026-08-02T10:00:00Z"}`)
		default:
			http.NotFound(w, r)
		}
	}))

	stdout, _, err := runCmd(t, "", "memory-stores", "dream-policy", "mstr_1", "--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"MEMORY STORE", "mstr_1", "ACTIVITY", "SCHEDULE", "off"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("policy get missing %q:\n%s", want, stdout)
		}
	}

	stdin := `{"activity":{"enabled":true,"min_changed_sessions":3},"schedule":{"enabled":false},"environment_id":"env_1"}`
	stdout, _, err = runCmd(t, stdin, "memory-stores", "dream-policy", "mstr_1", "--file", "-", "--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rec.body, `"min_changed_sessions":3`) {
		t.Fatalf("update body = %s", rec.body)
	}
	if !strings.Contains(stdout, "on (>=3 sessions)") || !strings.Contains(stdout, "trig_1") {
		t.Errorf("policy update output:\n%s", stdout)
	}
}

func TestMemoryStoresDreamRunAndRuns(t *testing.T) {
	isolateEnv(t)
	var gotRunsQuery string
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/memory-stores/mstr_1/dream-policy":
			_, _ = fmt.Fprint(w, `{"memory_store_id":"mstr_1","activity":{"enabled":true,"min_changed_sessions":3},"schedule":{"enabled":false},"trigger_id":"trig_d","updated_at":"2026-08-01T10:00:00Z"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/memory-stores/mstr_new/dream-policy":
			_, _ = fmt.Fprint(w, `{"memory_store_id":"mstr_new","activity":{"enabled":false},"schedule":{"enabled":false}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/triggers/trig_d/run":
			if data, _ := io.ReadAll(r.Body); len(data) != 0 {
				t.Errorf("dream run body = %s, want none", data)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, `{"id":"trun_1","trigger_id":"trig_d","status":"running","session_id":"sesn_1","created_at":"2026-08-01T10:00:00Z"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/triggers/trig_d/runs/trun_1":
			_, _ = fmt.Fprint(w, `{"id":"trun_1","trigger_id":"trig_d","status":"completed","session_id":"sesn_1","created_at":"2026-08-01T10:00:00Z","finished_at":"2026-08-01T10:03:00Z"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/triggers/trig_d/runs":
			gotRunsQuery = r.URL.RawQuery
			_, _ = fmt.Fprint(w, `{"items":[{"id":"trun_1","trigger_id":"trig_d","status":"failed","session_id":"sesn_1","created_at":"2026-08-01T10:00:00Z"}]}`)
		default:
			http.NotFound(w, r)
		}
	})

	stdout, stderr, err := runCmd(t, "", "memory-stores", "dream-policy", "run", "mstr_1", "--wait", "--interval", "1ms",
		"--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "trun_1") || !strings.Contains(stdout, "completed") {
		t.Errorf("dream run --wait output:\n%s", stdout)
	}
	if !strings.Contains(stderr, "dream started for mstr_1 (trigger trig_d, run trun_1)") {
		t.Errorf("dream run stderr = %q", stderr)
	}

	stdout, _, err = runCmd(t, "", "memory-stores", "dream-policy", "runs", "mstr_1", "--has-error",
		"--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "trun_1") || !strings.Contains(stdout, "failed") || !strings.Contains(gotRunsQuery, "has_error=true") {
		t.Errorf("dream runs output:\n%s\nquery = %q", stdout, gotRunsQuery)
	}

	_, _, err = runCmd(t, "", "memory-stores", "dream-policy", "run", "mstr_new", "--base-url", srv.URL, "--api-key", "sk-test")
	if err == nil || !strings.Contains(err.Error(), "has no Dream policy yet") {
		t.Fatalf("dream run without a policy: err = %v", err)
	}
}

// TestEnvironmentsCreateWait covers create --wait: the create returns a
// building Environment, the waiter polls (short --interval) until ready, and
// the final row shows the ready build.
func TestEnvironmentsCreateWait(t *testing.T) {
	isolateEnv(t)
	gets := 0
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/environments":
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, `{"id":"env_1","name":"demo","build_status":"building","created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-01T10:00:00Z"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/environments/env_1":
			gets++
			_, _ = fmt.Fprint(w, `{"id":"env_1","name":"demo","build_status":"ready","created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-01T10:00:00Z"}`)
		default:
			http.NotFound(w, r)
		}
	})

	stdout, stderr, err := runCmd(t, "", "environments", "create", "--name", "demo", "--wait", "--interval", "1ms",
		"--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "ready") {
		t.Errorf("create --wait output:\n%s", stdout)
	}
	if !strings.Contains(stderr, "environment env_1 is ready") {
		t.Errorf("stderr = %q", stderr)
	}
	if gets != 1 {
		t.Errorf("polls = %d, want 1", gets)
	}
}

// TestEnvironmentsWaitReadyFailed covers the terminal-failure path: the
// build fails and the WaitFailedError lands on stderr with exit 1.
func TestEnvironmentsWaitReadyFailed(t *testing.T) {
	isolateEnv(t)
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/environments/env_1" {
			_, _ = fmt.Fprint(w, `{"id":"env_1","name":"demo","build_status":"failed","created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-01T10:00:00Z"}`)
			return
		}
		http.NotFound(w, r)
	})

	_, _, err := runCmd(t, "", "environments", "wait-ready", "env_1", "--interval", "1ms", "--base-url", srv.URL, "--api-key", "sk-test")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), `"failed"`) {
		t.Fatalf("err = %v, want the WaitFailedError", err)
	}
	if code := exitCode(err); code != exitError {
		t.Fatalf("exitCode = %d, want %d", code, exitError)
	}
}

// TestTriggersRunsWait covers runs wait: the run is polled (short
// --interval) until it completes, and the terminal row plus note appear.
func TestTriggersRunsWait(t *testing.T) {
	isolateEnv(t)
	gets := 0
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/triggers/trig_1/runs/trun_1" {
			gets++
			if gets == 1 {
				_, _ = fmt.Fprint(w, `{"id":"trun_1","trigger_id":"trig_1","status":"running","created_at":"2026-08-01T10:00:00Z"}`)
			} else {
				_, _ = fmt.Fprint(w, `{"id":"trun_1","trigger_id":"trig_1","status":"completed","session_id":"sess_9","created_at":"2026-08-01T10:00:00Z","finished_at":"2026-08-01T10:03:00Z"}`)
			}
			return
		}
		http.NotFound(w, r)
	})

	stdout, stderr, err := runCmd(t, "", "triggers", "runs", "wait", "trig_1", "trun_1", "--interval", "1ms",
		"--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "completed") || !strings.Contains(stdout, "sess_9") {
		t.Errorf("runs wait output:\n%s", stdout)
	}
	if !strings.Contains(stderr, "run trun_1 reached completed") {
		t.Errorf("stderr = %q", stderr)
	}
}

// TestTriggersRunWaitFlag covers run --wait: the fired run is waited on
// through the same WaitForTerminal path.
func TestTriggersRunWaitFlag(t *testing.T) {
	isolateEnv(t)
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
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

	stdout, _, err := runCmd(t, "", "triggers", "run", "trig_1", "--wait", "--interval", "1ms",
		"--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "completed") {
		t.Errorf("run --wait output:\n%s", stdout)
	}
}

func TestAttachHelpMentionsThread(t *testing.T) {
	isolateEnv(t)
	stdout, _, err := runCmd(t, "", "sessions", "attach", "--help")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "--thread") {
		t.Errorf("attach help missing --thread:\n%s", stdout)
	}
}

// TestSessionsFsList covers both the root listing and a --prefix
// subdirectory listing, asserting the request path/query and the table
// shape (PATH/SIZE/TYPE, directories carry no size).
func TestSessionsFsList(t *testing.T) {
	isolateEnv(t)
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/sess_1/filesystem" {
			if r.URL.Query().Get("prefix") == "upload" {
				_, _ = fmt.Fprint(w, `{"items":[{"name":"a.csv","size_bytes":12,"is_dir":false,"path":"upload/a.csv","last_modified":"2026-08-01T10:00:00Z"}]}`)
				return
			}
			_, _ = fmt.Fprint(w, `{"items":[
				{"name":"upload","size_bytes":0,"is_dir":true,"path":"upload"},
				{"name":"notes.txt","size_bytes":2048,"is_dir":false,"path":"notes.txt","last_modified":"2026-08-01T10:00:00Z"}
			]}`)
			return
		}
		http.NotFound(w, r)
	})

	stdout, _, err := runCmd(t, "", "sessions", "fs", "list", "sess_1", "--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"PATH", "SIZE", "TYPE", "upload", "dir", "notes.txt", "2.0 KB", "file"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("fs list missing %q:\n%s", want, stdout)
		}
	}

	stdout, _, err = runCmd(t, "", "sessions", "fs", "list", "sess_1", "--prefix", "upload", "--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "upload/a.csv") || strings.Contains(stdout, "notes.txt") {
		t.Errorf("--prefix listing did not show the subdirectory contents:\n%s", stdout)
	}
}

// TestSessionsFsDownload covers the raw download endpoint: bytes stream to
// stdout verbatim by default; --out writes them to a file and confirms on
// stderr.
func TestSessionsFsDownload(t *testing.T) {
	isolateEnv(t)
	const payload = "line one\nline two\n"
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/sess_1/filesystem/raw" && r.URL.Query().Get("path") == "out/result.txt" {
			_, _ = fmt.Fprint(w, payload)
			return
		}
		http.NotFound(w, r)
	})

	stdout, _, err := runCmd(t, "", "sessions", "fs", "download", "sess_1", "out/result.txt", "--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if stdout != payload {
		t.Fatalf("streamed bytes = %q, want %q", stdout, payload)
	}

	dest := filepath.Join(t.TempDir(), "result.txt")
	stdout, stderr, err := runCmd(t, "", "sessions", "fs", "download", "sess_1", "out/result.txt", "--out", dest, "--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "" {
		t.Errorf("--out must keep stdout clean, got %q", stdout)
	}
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != payload {
		t.Errorf("file bytes = %q, want %q", data, payload)
	}
	if !strings.Contains(stderr, "downloaded to "+dest) {
		t.Errorf("stderr missing the confirmation:\n%s", stderr)
	}
}

// TestSessionsPendingListAndRetract covers the pending queue: listing renders
// one row per queued input (user.message summarized through render.Summary's
// variant knowledge, a streamed-only variant by its discriminator, an unknown
// one without crashing), and retract issues the DELETE and confirms on stderr.
func TestSessionsPendingListAndRetract(t *testing.T) {
	isolateEnv(t)
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/sess_1/events/pending":
			_, _ = fmt.Fprint(w, `{"items":[
				{"event_id":"sevt_9","type":"steer","events":[{"type":"user.message","data":{"content":[{"type":"text","text":"hurry up"}]}}],"queued_at":"2026-08-01T10:05:00Z"},
				{"event_id":"sevt_10","type":"next_turn","events":[{"type":"user.interrupt"}],"queued_at":"2026-08-01T10:06:00Z"},
				{"event_id":"sevt_11","type":"steer","events":[{"type":"user.brand_new"}],"queued_at":"2026-08-01T10:07:00Z"}
			]}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/sessions/sess_1/events/pending/sevt_9":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})

	stdout, _, err := runCmd(t, "", "sessions", "pending", "list", "sess_1", "--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"QUEUED_AT", "ID", "TYPE", "SUMMARY", "sevt_9", "steer", "hurry up", "sevt_10", "[user.interrupt]", "sevt_11", "[unknown event user.brand_new]"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("pending list missing %q:\n%s", want, stdout)
		}
	}

	stdout, stderr, err := runCmd(t, "", "sessions", "pending", "retract", "sess_1", "sevt_9", "--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "" {
		t.Errorf("retract must keep stdout clean, got %q", stdout)
	}
	if !strings.Contains(stderr, "event sevt_9 retracted from session sess_1") {
		t.Errorf("stderr missing the retract confirmation:\n%s", stderr)
	}
}

// TestAgentsListExcludeMultiagent covers the --exclude-multiagent filter: the
// query parameter goes out verbatim (subagent-picker mode).
func TestAgentsListExcludeMultiagent(t *testing.T) {
	isolateEnv(t)
	var got string
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/agents" {
			http.NotFound(w, r)
			return
		}
		got = r.URL.Query().Get("exclude_multiagent")
		_, _ = fmt.Fprint(w, `{"items":[]}`)
	})

	if _, _, err := runCmd(t, "", "agents", "list", "--exclude-multiagent", "--base-url", srv.URL, "--api-key", "sk-test"); err != nil {
		t.Fatal(err)
	}
	if got != "true" {
		t.Errorf("exclude_multiagent = %q, want true", got)
	}
}

// TestTriggersListPolicyType covers the --policy-type filter.
func TestTriggersListPolicyType(t *testing.T) {
	isolateEnv(t)
	var got string
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/triggers" {
			http.NotFound(w, r)
			return
		}
		got = r.URL.Query().Get("policy_type")
		_, _ = fmt.Fprint(w, `{"items":[]}`)
	})

	if _, _, err := runCmd(t, "", "triggers", "list", "--policy-type", "cron", "--base-url", srv.URL, "--api-key", "sk-test"); err != nil {
		t.Fatal(err)
	}
	if got != "cron" {
		t.Errorf("policy_type = %q, want cron", got)
	}
}

// TestSessionsThreadsEvents covers the Thread-level history listing: the
// endpoint carries both ids, --order maps to the order query, and the table
// renders one row per event.
func TestSessionsThreadsEvents(t *testing.T) {
	isolateEnv(t)
	var gotOrder string
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/sess_1/threads/sthr_2/events" {
			gotOrder = r.URL.Query().Get("order")
			_, _ = fmt.Fprint(w, `{"items":[
				{"id":"sevt_8","processed_at":"2026-08-01T10:03:00Z","type":"user.message","data":{"content":[{"type":"text","text":"subtask"}]}},
				{"id":"sevt_9","processed_at":"2026-08-01T10:04:00Z","type":"agent.message","data":{"content":[{"type":"text","text":"done"}]}}
			],"stream_cursor":"cur_9"}`)
			return
		}
		http.NotFound(w, r)
	})

	stdout, _, err := runCmd(t, "", "sessions", "threads", "events", "sess_1", "sthr_2", "--order", "desc", "--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if gotOrder != "desc" {
		t.Errorf("order = %q, want desc", gotOrder)
	}
	for _, want := range []string{"sevt_8", "user.message", "subtask", "sevt_9", "agent.message"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("threads events missing %q:\n%s", want, stdout)
		}
	}
	if !strings.HasSuffix(stdout, "CURSOR cur_9\n") {
		t.Errorf("threads events missing the page cursor:\n%s", stdout)
	}
}

// TestSessionsListJSONFieldNames checks the generated SessionList page's
// JSON form: -o json must print the API's JSON field names, never Go's
// default field names.
func TestSessionsListJSONFieldNames(t *testing.T) {
	isolateEnv(t)
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/sessions" {
			_, _ = fmt.Fprint(w, `{"items":[{"id":"sesn_1","agent_id":"agent_1","environment_id":"env_1","status":"idle","created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-02T10:00:00Z"}]}`)
			return
		}
		http.NotFound(w, r)
	})

	stdout, _, err := runCmd(t, "", "sessions", "list", "-o", "json", "--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, `"items"`) {
		t.Errorf("sessions list -o json missing the JSON field name:\n%s", stdout)
	}
	if strings.Contains(stdout, `"Items"`) || strings.Contains(stdout, `"NextPageToken"`) {
		t.Errorf("sessions list -o json prints Go field names:\n%s", stdout)
	}
}
