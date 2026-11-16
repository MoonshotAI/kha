package commands

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

const workItemJSON = `{"id":"work_1","session_id":"sess_1","environment_id":"env_1","status":"queued","generation":3,"worker_id":"worker-1","lease_ttl_ms":300000,"created_at":"2026-08-01T10:00:00Z"}`

const workPollJSON = `{"id":"work_1","session_id":"sess_1","environment_id":"env_1","status":"queued","generation":3,"worker_id":"worker-1","lease_ttl_ms":300000,"created_at":"2026-08-01T10:00:00Z","secret":"bundle-abc"}`

func writeSecretFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestWorkCommandTree(t *testing.T) {
	root := NewRootCommand()
	cases := []struct {
		path  string
		args  int
		flags []string
	}{
		{"environments work list", 1, []string{"status", "limit", "page-token", "all", "token-file"}},
		{"environments work get", 2, []string{"token-file"}},
		{"environments work stats", 1, []string{"token-file"}},
		{"environments work poll", 1, []string{"worker-id", "block", "lease-ttl", "reclaim-older-than", "token-file"}},
		{"environments work ack", 2, []string{"worker-id", "generation", "token-file", "token-stdin"}},
		{"environments work heartbeat", 2, []string{"worker-id", "generation", "token-file", "token-stdin"}},
		{"environments work stop", 2, []string{"worker-id", "generation", "token-file", "token-stdin", "final-status"}},
	}
	for _, c := range cases {
		cmd, _, err := root.Find(strings.Split(c.path, " "))
		if err != nil || cmd == nil {
			t.Errorf("command %q not found in the tree (err=%v)", c.path, err)
			continue
		}
		if cmd.Args == nil {
			t.Fatalf("%s has no Args validator", c.path)
		}
		if err := cmd.Args(cmd, make([]string, c.args)); err != nil {
			t.Errorf("%s rejected %d positional args: %v", c.path, c.args, err)
		}
		if err := cmd.Args(cmd, make([]string, c.args+1)); err == nil {
			t.Errorf("%s accepted %d positional args, want exactly %d", c.path, c.args+1, c.args)
		}
		for _, f := range c.flags {
			if cmd.Flags().Lookup(f) == nil {
				t.Errorf("%s missing flag --%s", c.path, f)
			}
		}
	}
}

func TestWorkListFiltersAndPaging(t *testing.T) {
	isolateEnv(t)
	var gotAuth, gotQuery string
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/environments/env_1/work" {
			http.NotFound(w, r)
			return
		}
		gotAuth, gotQuery = r.Header.Get("Authorization"), r.URL.RawQuery
		if r.URL.Query().Get("page_token") == "tok2" {
			_, _ = fmt.Fprint(w, `{"items":[{"id":"work_2","session_id":"sess_2","environment_id":"env_1","status":"active","generation":1,"worker_id":"worker-9","lease_ttl_ms":300000,"created_at":"2026-08-02T10:00:00Z"}]}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"items":[`+workItemJSON+`],"next_page_token":"tok2"}`)
	})

	stdout, _, err := runCmd(t, "", "environments", "work", "list", "env_1",
		"--status", "active", "--limit", "10", "--page-token", "tok2",
		"--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	q, err := url.ParseQuery(gotQuery)
	if err != nil {
		t.Fatalf("recorded query %q: %v", gotQuery, err)
	}
	for k, want := range map[string]string{"status": "active", "page_size": "10", "page_token": "tok2"} {
		if got := q.Get(k); got != want {
			t.Errorf("query %s = %q, want %q (raw: %s)", k, got, want, gotQuery)
		}
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("Authorization = %q, want the user API key", gotAuth)
	}
	if !strings.Contains(stdout, "work_2") || strings.Contains(stdout, "work_1") {
		t.Errorf("single-page table should hold only page 2:\n%s", stdout)
	}

	stdout, _, err = runCmd(t, "", "environments", "work", "list", "env_1", "--all",
		"--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "work_1") || !strings.Contains(stdout, "work_2") {
		t.Errorf("--all table missing rows from both pages:\n%s", stdout)
	}

	_, _, err = runCmd(t, "", "environments", "work", "list", "env_1", "--status", "bogus",
		"--base-url", srv.URL, "--api-key", "sk-test")
	if err == nil {
		t.Fatal("expected a usage error for --status bogus")
	}
	if code := exitCode(err); code != exitUsage {
		t.Errorf("exitCode = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(err.Error(), "--status") {
		t.Errorf("err = %v, want it to name --status", err)
	}
}

func TestWorkGetAndStats(t *testing.T) {
	isolateEnv(t)
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/environments/env_1/work/work_1":
			_, _ = fmt.Fprint(w, workItemJSON)
		case "/v1/environments/env_1/work/stats":
			_, _ = fmt.Fprint(w, `{"depth":2,"claimed":1,"workers":1,"oldest_queued_at":"2026-08-01T10:00:00Z"}`)
		default:
			http.NotFound(w, r)
		}
	})

	stdout, _, err := runCmd(t, "", "environments", "work", "get", "env_1", "work_1",
		"--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"work_1", "sess_1", "queued", "worker-1"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("get output missing %q:\n%s", want, stdout)
		}
	}

	stdout, _, err = runCmd(t, "", "environments", "work", "stats", "env_1",
		"--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"DEPTH", "CLAIMED", "WORKERS", "OLDEST QUEUED", "2", "1"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stats output missing %q:\n%s", want, stdout)
		}
	}
}

func TestWorkPollWithUserKey(t *testing.T) {
	isolateEnv(t)
	var gotAuth, gotQuery string
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/environments/env_1/work/poll" {
			http.NotFound(w, r)
			return
		}
		gotAuth, gotQuery = r.Header.Get("Authorization"), r.URL.RawQuery
		_, _ = fmt.Fprint(w, workPollJSON)
	})

	stdout, stderr, err := runCmd(t, "", "environments", "work", "poll", "env_1",
		"--worker-id", "worker-1", "--block", "500ms", "--lease-ttl", "60s", "--reclaim-older-than", "10s",
		"--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	q, err := url.ParseQuery(gotQuery)
	if err != nil {
		t.Fatalf("recorded query %q: %v", gotQuery, err)
	}
	for k, want := range map[string]string{
		"worker_id": "worker-1", "block_ms": "500", "lease_ttl_ms": "60000", "reclaim_older_than_ms": "10000",
	} {
		if got := q.Get(k); got != want {
			t.Errorf("query %s = %q, want %q (raw: %s)", k, got, want, gotQuery)
		}
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("Authorization = %q, want the user API key", gotAuth)
	}
	if !strings.Contains(stdout, "work_1") || !strings.Contains(stdout, "bundle-abc") {
		t.Errorf("poll table must carry the item and its secret:\n%s", stdout)
	}
	if !strings.Contains(stderr, "claimed work item work_1") {
		t.Errorf("stderr = %q, want the claim note", stderr)
	}

	for _, extra := range [][]string{
		{},
		{"--worker-id", "worker-1", "--block", "1s"},
	} {
		_, _, err = runCmd(t, "", append([]string{"environments", "work", "poll", "env_1",
			"--base-url", srv.URL, "--api-key", "sk-test"}, extra...)...)
		if err == nil {
			t.Errorf("args %v: expected a usage error", extra)
			continue
		}
		if code := exitCode(err); code != exitUsage {
			t.Errorf("args %v: exitCode = %d, want %d", extra, code, exitUsage)
		}
	}
}

func TestWorkPollWithWorkKeyFile(t *testing.T) {
	isolateEnv(t)
	keyFile := writeSecretFile(t, "ewk-live\n")

	var gotAuth string
	empty := atomic.Bool{}
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/environments/env_1/work/poll" {
			http.NotFound(w, r)
			return
		}
		gotAuth = r.Header.Get("Authorization")
		if empty.Load() {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = fmt.Fprint(w, workPollJSON)
	})

	stdout, _, err := runCmd(t, "", "environments", "work", "poll", "env_1", "--worker-id", "worker-1",
		"--token-file", keyFile, "--base-url", srv.URL, "--api-key", "sk-ignored")
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer ewk-live" {
		t.Errorf("Authorization = %q, want Bearer ewk-live (work key replaces the user key)", gotAuth)
	}
	if !strings.Contains(stdout, "bundle-abc") {
		t.Errorf("poll table must carry the secret:\n%s", stdout)
	}

	empty.Store(true)
	stdout, stderr, err := runCmd(t, "", "environments", "work", "poll", "env_1", "--worker-id", "worker-1",
		"--token-file", keyFile, "--base-url", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on 204", stdout)
	}
	if !strings.Contains(stderr, "no queued work") {
		t.Errorf("stderr = %q, want the no-work note", stderr)
	}
}

func TestWorkPollNeverRetries(t *testing.T) {
	isolateEnv(t)
	keyFile := writeSecretFile(t, "ewk-live")

	var calls atomic.Int32
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, `{"error":{"type":"api_error","message":"boom","request_id":"req_1"}}`)
	})

	for _, extra := range [][]string{
		{"--api-key", "sk-test"},
		{"--token-file", keyFile},
	} {
		calls.Store(0)
		_, _, err := runCmd(t, "", append([]string{"environments", "work", "poll", "env_1",
			"--worker-id", "worker-1", "--base-url", srv.URL}, extra...)...)
		if err == nil {
			t.Errorf("args %v: expected the 500 to surface", extra)
			continue
		}
		if got := calls.Load(); got != 1 {
			t.Errorf("args %v: server saw %d requests, want exactly 1 (poll must not retry)", extra, got)
		}
	}
}

func TestWorkAckCredentialRules(t *testing.T) {
	isolateEnv(t)
	secretFile := writeSecretFile(t, "swt-xyz\n")
	keyFile := writeSecretFile(t, "ewk-abc\n")

	var gotAuth, gotBody string
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/environments/env_1/work/work_1/ack" {
			gotAuth = r.Header.Get("Authorization")
			buf, _ := io.ReadAll(r.Body)
			gotBody = string(buf)
			_, _ = fmt.Fprint(w, `{"id":"work_1","session_id":"sess_1","environment_id":"env_1","status":"starting","generation":3,"worker_id":"worker-1","lease_ttl_ms":300000,"created_at":"2026-08-01T10:00:00Z","acknowledged_at":"2026-08-01T10:00:04Z"}`)
			return
		}
		http.NotFound(w, r)
	})

	_, _, err := runCmd(t, "", "environments", "work", "ack", "env_1", "work_1",
		"--worker-id", "worker-1", "--generation", "3", "--base-url", srv.URL, "--api-key", "sk-test")
	if err == nil {
		t.Fatal("expected a usage error without an explicit credential")
	}
	if code := exitCode(err); code != exitUsage {
		t.Errorf("exitCode = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(err.Error(), "--token-file") || !strings.Contains(err.Error(), "--token-stdin") {
		t.Errorf("err = %v, want it to name both credential flags", err)
	}

	_, _, err = runCmd(t, "", "environments", "work", "ack", "env_1", "work_1",
		"--worker-id", "worker-1", "--generation", "3",
		"--token-file", secretFile, "--token-stdin", "--base-url", srv.URL)
	if err == nil {
		t.Fatal("expected a usage error with both credential flags")
	}
	if code := exitCode(err); code != exitUsage {
		t.Errorf("exitCode = %d, want %d", code, exitUsage)
	}

	stdout, stderr, err := runCmd(t, "", "environments", "work", "ack", "env_1", "work_1",
		"--worker-id", "worker-1", "--generation", "3",
		"--token-file", secretFile, "--base-url", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer swt-xyz" {
		t.Errorf("Authorization = %q, want Bearer swt-xyz", gotAuth)
	}
	if !strings.Contains(gotBody, `"worker_id":"worker-1"`) || !strings.Contains(gotBody, `"generation":3`) {
		t.Errorf("ack body = %s, want worker_id and generation", gotBody)
	}
	if !strings.Contains(stdout, "starting") {
		t.Errorf("ack table should show the new status:\n%s", stdout)
	}
	if !strings.Contains(stderr, "acknowledged") {
		t.Errorf("stderr = %q, want the ack note", stderr)
	}

	if _, _, err := runCmd(t, "", "environments", "work", "ack", "env_1", "work_1",
		"--worker-id", "worker-1", "--generation", "3",
		"--token-file", keyFile, "--base-url", srv.URL); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer ewk-abc" {
		t.Errorf("Authorization = %q, want Bearer ewk-abc", gotAuth)
	}

	_, _, err = runCmd(t, "", "environments", "work", "ack", "env_1", "work_1",
		"--worker-id", "worker-1", "--token-file", secretFile, "--base-url", srv.URL)
	if err == nil {
		t.Fatal("expected a usage error without --generation")
	}
	if code := exitCode(err); code != exitUsage {
		t.Errorf("exitCode = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(err.Error(), "--generation") {
		t.Errorf("err = %v, want it to name --generation", err)
	}
}

func TestWorkHeartbeat(t *testing.T) {
	isolateEnv(t)
	secretFile := writeSecretFile(t, "swt-xyz")

	var gotBody string
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/environments/env_1/work/work_1/heartbeat" {
			buf, _ := io.ReadAll(r.Body)
			gotBody = string(buf)
			_, _ = fmt.Fprint(w, `{"last_heartbeat":"2026-08-01T10:05:00Z","lease_extended":true,"state":"active","ttl_seconds":300,"generation":3}`)
			return
		}
		http.NotFound(w, r)
	})

	stdout, _, err := runCmd(t, "", "environments", "work", "heartbeat", "env_1", "work_1",
		"--worker-id", "worker-1", "--generation", "3",
		"--token-file", secretFile, "--base-url", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotBody, `"worker_id":"worker-1"`) || !strings.Contains(gotBody, `"generation":3`) {
		t.Errorf("heartbeat body = %s, want worker_id and generation", gotBody)
	}
	for _, want := range []string{"STATE", "active", "300"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("heartbeat output missing %q:\n%s", want, stdout)
		}
	}
}

func TestWorkStop(t *testing.T) {
	isolateEnv(t)
	secretFile := writeSecretFile(t, "swt-xyz")

	var gotBody string
	var calls atomic.Int32
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/environments/env_1/work/work_1/stop" {
			calls.Add(1)
			buf, _ := io.ReadAll(r.Body)
			gotBody = string(buf)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	})

	_, stderr, err := runCmd(t, "", "environments", "work", "stop", "env_1", "work_1",
		"--worker-id", "worker-1", "--generation", "3",
		"--token-file", secretFile, "--base-url", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(gotBody, "final_status") {
		t.Errorf("graceful stop body = %s, must omit final_status", gotBody)
	}
	if !strings.Contains(stderr, "stop requested") {
		t.Errorf("stderr = %q, want the stop-requested note", stderr)
	}

	_, stderr, err = runCmd(t, "", "environments", "work", "stop", "env_1", "work_1",
		"--worker-id", "worker-1", "--generation", "3", "--final-status", "completed",
		"--token-file", secretFile, "--base-url", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotBody, `"final_status":"completed"`) {
		t.Errorf("completion body = %s, want final_status completed", gotBody)
	}
	if !strings.Contains(stderr, "completed") {
		t.Errorf("stderr = %q, want the completion note", stderr)
	}

	calls.Store(0)
	_, _, err = runCmd(t, "", "environments", "work", "stop", "env_1", "work_1",
		"--worker-id", "worker-1", "--generation", "3", "--final-status", "failed",
		"--token-file", secretFile, "--base-url", srv.URL)
	if err == nil {
		t.Fatal("expected a usage error for --final-status failed")
	}
	if code := exitCode(err); code != exitUsage {
		t.Errorf("exitCode = %d, want %d", code, exitUsage)
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("server saw %d requests for an invalid --final-status, want 0", got)
	}
}

func TestWorkReadsAcceptWorkKeyFile(t *testing.T) {
	isolateEnv(t)
	keyFile := writeSecretFile(t, "ewk-read")

	var gotAuth string
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/v1/environments/env_1/work":
			_, _ = fmt.Fprint(w, `{"items":[`+workItemJSON+`]}`)
		case "/v1/environments/env_1/work/work_1":
			_, _ = fmt.Fprint(w, workItemJSON)
		case "/v1/environments/env_1/work/stats":
			_, _ = fmt.Fprint(w, `{"depth":0,"claimed":0,"workers":0}`)
		default:
			http.NotFound(w, r)
		}
	})

	for _, args := range [][]string{
		{"list", "env_1"},
		{"get", "env_1", "work_1"},
		{"stats", "env_1"},
	} {
		full := append([]string{"environments", "work"}, args...)
		full = append(full, "--token-file", keyFile, "--base-url", srv.URL)
		if _, _, err := runCmd(t, "", full...); err != nil {
			t.Errorf("%v: %v", args, err)
			continue
		}
		if gotAuth != "Bearer ewk-read" {
			t.Errorf("%v: Authorization = %q, want Bearer ewk-read", args, gotAuth)
		}
	}
}

func TestWorkPollAckHint(t *testing.T) {
	isolateEnv(t)
	key := writeSecretFile(t, "ewk-hint")
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, workPollJSON) })
	for _, mode := range []string{"file", "stdin", "user_key"} {
		t.Run(mode, func(t *testing.T) {
			args := []string{"environments", "work", "poll", "env_1", "--worker-id", "w'$(touch nope)", "--base-url", srv.URL, "-o", "json"}
			input := ""
			switch mode {
			case "file":
				args = append(args, "--token-file", key)
			case "stdin":
				args = append(args, "--token-stdin")
				input = "ewk-hint"
			case "user_key":
				args = append(args, "--api-key", "sk-test")
			}
			out, stderr, err := runCmd(t, input, args...)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(out, "next:") || !strings.Contains(out, `"secret": "bundle-abc"`) {
				t.Fatal("poll output corrupted", out)
			}
			if !strings.Contains(stderr, "--worker-id 'w'\"'\"'$(touch nope)'") || !strings.Contains(stderr, "--generation 3") || !strings.Contains(stderr, srv.URL) {
				t.Fatal("hint missing quoted identity/endpoint", stderr)
			}
			if strings.Contains(stderr, "ewk-hint") || strings.Contains(stderr, "bundle-abc") {
				t.Fatal("hint leaked secret")
			}
			if mode == "file" && !strings.Contains(stderr, shellWord(key)) {
				t.Fatal("hint missing key file")
			}
			if mode != "file" && !strings.Contains(stderr, "TOKEN_FILE") {
				t.Fatal("hint did not request credential file")
			}
		})
	}
}
