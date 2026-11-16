package commands

import (
	"context"
	"fmt"
	"io"

	json "encoding/json/v2"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/MoonshotAI/kha/internal/tools"
)

func stubLocalExecutor(t *testing.T, executor tools.Executor) {
	t.Helper()
	old := newLocalExecutor
	newLocalExecutor = func(time.Duration) (tools.Executor, error) { return executor, nil }
	t.Cleanup(func() { newLocalExecutor = old })
}

type fakeToolExecutor struct {
	mu    sync.Mutex
	calls []tools.Call
}

func (f *fakeToolExecutor) Execute(_ context.Context, call tools.Call) (tools.Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.mu.Unlock()
	return tools.Result{Content: []tools.ContentBlock{{Type: tools.ContentBlockTypeText, Text: "ok"}}}, nil
}

func (f *fakeToolExecutor) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func findSubCmd(t *testing.T, root *cobra.Command, path ...string) *cobra.Command {
	t.Helper()
	cmd := root
	for _, name := range path {
		var next *cobra.Command
		for _, c := range cmd.Commands() {
			if c.Name() == name {
				next = c
				break
			}
		}
		if next == nil {
			t.Fatalf("command path %v missing %q", path, name)
		}
		cmd = next
	}
	return cmd
}

func flagDefaults(cmd *cobra.Command) map[string]string {
	defaults := map[string]string{}
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		defaults[f.Name] = f.DefValue
	})
	return defaults
}

func TestWorkerCommandShape(t *testing.T) {
	root := NewRootCommand()

	poll := findSubCmd(t, root, "worker", "poll")
	pollFlags := flagDefaults(poll)
	for _, name := range []string{
		"token-file", "token-stdin", "worker-id", "on-work",
		"block", "lease-ttl", "reclaim-older-than",
		"workdir", "max-idle", "session-max-idle", "poll-interval", "tool-timeout", "send-window",
	} {
		if _, ok := pollFlags[name]; !ok {
			t.Errorf("worker poll missing flag --%s", name)
		}
	}
	if got := pollFlags["block"]; got != "999ms" {
		t.Errorf("--block default = %q, want 999ms", got)
	}
	if got := pollFlags["max-idle"]; got != "0s" {
		t.Errorf("poll --max-idle default = %q, want 0s", got)
	}
	if got := pollFlags["session-max-idle"]; got != "1m0s" {
		t.Errorf("poll --session-max-idle default = %q, want 60s", got)
	}
	if got := pollFlags["worker-id"]; got == "" {
		t.Error("--worker-id default must be generated per process start")
	}

	run := findSubCmd(t, root, "worker", "run")
	runFlags := flagDefaults(run)
	for _, name := range []string{
		"environment-id", "work-id", "session-id", "worker-id", "generation", "lease-ttl", "token-file",
		"workdir", "max-idle", "poll-interval", "tool-timeout", "send-window",
	} {
		if _, ok := runFlags[name]; !ok {
			t.Errorf("worker run missing flag --%s", name)
		}
	}
	if got := runFlags["max-idle"]; got != "1m0s" {
		t.Errorf("run --max-idle default = %q, want 60s", got)
	}
	// A random default cannot match poll's identity, so run requires an explicit worker ID.
	if got := runFlags["worker-id"]; got != "" {
		t.Errorf("worker run --worker-id default = %q, want empty (required)", got)
	}

	// Generate a fresh poll worker ID for every command tree.
	again := findSubCmd(t, NewRootCommand(), "worker", "poll")
	if flagDefaults(again)["worker-id"] == pollFlags["worker-id"] {
		t.Error("--worker-id default must be unique per process start")
	}
}

type requestCounter struct {
	mu    sync.Mutex
	count int
}

func (c *requestCounter) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		c.mu.Lock()
		c.count++
		c.mu.Unlock()
		http.Error(w, "unexpected request", http.StatusBadRequest)
	}
}

func (c *requestCounter) total() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.count
}

func TestWorkerPollUsageErrors(t *testing.T) {
	keyFile := writeSecretFile(t, "ewk-test")
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"missing_environment", []string{"--token-file", keyFile}},
		{"missing_work_key_file", []string{"env_1"}},
		{"block_out_of_range", []string{"env_1", "--token-file", keyFile, "--block", "1s"}},
		{"launcher_missing", []string{"env_1", "--token-file", keyFile, "--on-work", filepath.Join(t.TempDir(), "missing.sh")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateEnv(t)
			counter := &requestCounter{}
			srv := serve(t, counter.handler())
			args := append([]string{"worker", "poll"}, tc.args...)
			args = append(args, "--base-url", srv.URL)
			_, _, err := runCmd(t, "", args...)
			if exitCode(err) != 2 {
				t.Fatalf("exit code = %d (err %v), want 2", exitCode(err), err)
			}
			if counter.total() != 0 {
				t.Fatalf("requests = %d, want 0 before validation passes", counter.total())
			}
		})
	}
}

func TestWorkerRunUsageErrors(t *testing.T) {
	secretFile := writeSecretFile(t, "swt-test")
	base := []string{
		"--environment-id", "env_1",
		"--work-id", "work_1",
		"--session-id", "sess_1",
		"--worker-id", "wk-t",
		"--generation", "3",
		"--token-file", secretFile,
	}
	drop := func(flag string) []string {
		out := []string{}
		for i := 0; i < len(base); i += 2 {
			if base[i] != "--"+flag {
				out = append(out, base[i], base[i+1])
			}
		}
		return out
	}
	for _, flag := range []string{"environment-id", "work-id", "session-id", "worker-id", "generation", "token-file"} {
		t.Run("missing_"+strings.ReplaceAll(flag, "-", "_"), func(t *testing.T) {
			isolateEnv(t)
			counter := &requestCounter{}
			srv := serve(t, counter.handler())
			args := append([]string{"worker", "run"}, drop(flag)...)
			args = append(args, "--base-url", srv.URL)
			_, _, err := runCmd(t, "", args...)
			if exitCode(err) != 2 {
				t.Fatalf("exit code = %d (err %v), want 2", exitCode(err), err)
			}
			if counter.total() != 0 {
				t.Fatalf("requests = %d, want 0", counter.total())
			}
		})
	}
}

func TestWorkerRunLeaseTTLUsageError(t *testing.T) {
	secretFile := writeSecretFile(t, "swt-test")
	for _, raw := range []string{"abc", "-5", "99999999999999999999999"} {
		t.Run("value_"+raw, func(t *testing.T) {
			isolateEnv(t)
			t.Setenv("KHA_WORK_LEASE_TTL_MS", raw)
			counter := &requestCounter{}
			srv := serve(t, counter.handler())
			_, _, err := runCmd(t, "",
				"worker", "run",
				"--environment-id", "env_1",
				"--work-id", "work_1",
				"--session-id", "sess_1",
				"--worker-id", "wk-t",
				"--generation", "3",
				"--token-file", secretFile,
				"--base-url", srv.URL,
			)
			if exitCode(err) != 2 {
				t.Fatalf("exit code = %d (err %v), want 2", exitCode(err), err)
			}
			if counter.total() != 0 {
				t.Fatalf("requests = %d, want 0", counter.total())
			}
		})
	}
}

// Zero must tolerate non-conforming servers that omit lease details.
func TestWorkerRunLeaseTTLZeroFallsBack(t *testing.T) {
	isolateEnv(t)
	t.Setenv("KHA_WORK_LEASE_TTL_MS", "0")
	executor := &fakeToolExecutor{}
	stubLocalExecutor(t, executor)
	fake := newWorkerRunFake()
	srv := serve(t, fake.handler())
	secretFile := writeSecretFile(t, "swt-run")

	_, stderr, err := runCmd(t, "",
		"worker", "run",
		"--environment-id", "env_1",
		"--work-id", "work_1",
		"--session-id", "sess_1",
		"--worker-id", "wk-t",
		"--generation", "3",
		"--token-file", secretFile,
		"--base-url", srv.URL,
		"--max-idle", "100ms",
		"--poll-interval", "10ms",
	)
	if err != nil {
		t.Fatalf("worker run with KHA_WORK_LEASE_TTL_MS=0 failed: %v\n%s", err, stderr)
	}
	if executor.count() != 1 {
		t.Fatalf("executor calls = %d, want 1", executor.count())
	}
}

func TestWorkerRunLeaseTTLFromEnv(t *testing.T) {
	isolateEnv(t)
	t.Setenv("KHA_WORK_LEASE_TTL_MS", "100")
	executor := &fakeToolExecutor{}
	stubLocalExecutor(t, executor)
	fake := newWorkerRunFake()
	fake.heartbeatTTLOmit = true
	srv := serve(t, fake.handler())
	secretFile := writeSecretFile(t, "swt-run")

	_, stderr, err := runCmd(t, "",
		"worker", "run",
		"--environment-id", "env_1",
		"--work-id", "work_1",
		"--session-id", "sess_1",
		"--worker-id", "wk-t",
		"--generation", "3",
		"--token-file", secretFile,
		"--base-url", srv.URL,
		"--max-idle", "400ms",
		"--poll-interval", "10ms",
	)
	if err != nil {
		t.Fatalf("worker run failed: %v\n%s", err, stderr)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.heartbeatCalls < 2 {
		t.Fatalf("heartbeat calls = %d, want >= 2 (env lease TTL drives the first renewal gap)", fake.heartbeatCalls)
	}
}

type workerRunFake struct {
	mu             sync.Mutex
	auths          map[string]string
	sendBodies     []string
	stopBodies     []string
	heartbeatCalls int
	// Omitting ttl_seconds keeps the heartbeat interval at its initial estimate.
	heartbeatTTLOmit bool
}

func newWorkerRunFake() *workerRunFake {
	return &workerRunFake{auths: map[string]string{}}
}

func (f *workerRunFake) handler() http.HandlerFunc {
	toolUse := `{"id":"evt_1","type":"agent.tool_use","processed_at":"2026-01-01T00:00:01Z","data":{"tool_use":{"id":"tu_1","name":"shell","input":{"command":"echo hi"}},"evaluated_permission":"always_allow"}}`
	idle := `{"id":"evt_0","type":"session.status","processed_at":"2026-01-01T00:00:03Z","data":{"status":"idle"}}`
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.auths[r.URL.Path] = r.Header.Get("Authorization")
		f.mu.Unlock()
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/heartbeat"):
			f.mu.Lock()
			f.heartbeatCalls++
			omitTTL := f.heartbeatTTLOmit
			f.mu.Unlock()
			if omitTTL {
				_, _ = fmt.Fprint(w, `{"state":"active","generation":3,"lease_extended":true,"last_heartbeat":"2026-01-01T00:00:00Z"}`)
			} else {
				_, _ = fmt.Fprint(w, `{"state":"active","generation":3,"ttl_seconds":300,"lease_extended":true,"last_heartbeat":"2026-01-01T00:00:00Z"}`)
			}
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/threads"):
			_, _ = fmt.Fprint(w, `{"items":[{"id":"thr_coord","session_id":"sess_1","status":"idle"}]}`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/events"):
			_, _ = fmt.Fprintf(w, `{"items":[%s,%s]}`, toolUse, idle)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/events"):
			raw, _ := io.ReadAll(r.Body)
			f.mu.Lock()
			f.sendBodies = append(f.sendBodies, string(raw))
			f.mu.Unlock()
			w.WriteHeader(http.StatusAccepted)
			_, _ = fmt.Fprint(w, `{}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/stop"):
			raw, _ := io.ReadAll(r.Body)
			f.mu.Lock()
			f.stopBodies = append(f.stopBodies, string(raw))
			f.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unhandled", http.StatusNotFound)
		}
	}
}

func TestWorkerRunHappyPath(t *testing.T) {
	isolateEnv(t)
	executor := &fakeToolExecutor{}
	stubLocalExecutor(t, executor)
	fake := newWorkerRunFake()
	srv := serve(t, fake.handler())
	secretFile := writeSecretFile(t, "swt-run")

	_, stderr, err := runCmd(t, "",
		"worker", "run",
		"--environment-id", "env_1",
		"--work-id", "work_1",
		"--session-id", "sess_1",
		"--worker-id", "wk-t",
		"--generation", "3",
		"--token-file", secretFile,
		"--base-url", srv.URL,
		"--max-idle", "100ms",
		"--poll-interval", "10ms",
	)
	if err != nil {
		t.Fatalf("worker run failed: %v\n%s", err, stderr)
	}
	if executor.count() != 1 {
		t.Fatalf("executor calls = %d, want 1", executor.count())
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.sendBodies) != 1 {
		t.Fatalf("send bodies = %d, want 1", len(fake.sendBodies))
	}
	for _, want := range []string{`"type":"user.tool_result"`, `"tool_use_id":"tu_1"`, `"text":"ok"`} {
		if !strings.Contains(fake.sendBodies[0], want) {
			t.Errorf("send body missing %s:\n%s", want, fake.sendBodies[0])
		}
	}
	if len(fake.stopBodies) != 1 {
		t.Fatalf("stop bodies = %d, want 1", len(fake.stopBodies))
	}
	for _, want := range []string{`"final_status":"completed"`, `"worker_id":"wk-t"`, `"generation":3`} {
		if !strings.Contains(fake.stopBodies[0], want) {
			t.Errorf("stop body missing %s:\n%s", want, fake.stopBodies[0])
		}
	}
	if got := fake.auths["/v1/environments/env_1/work/work_1/heartbeat"]; got != "Bearer swt-run" {
		t.Errorf("heartbeat auth = %q, want Bearer swt-run", got)
	}
	if fake.heartbeatCalls < 1 {
		t.Error("want at least the activation heartbeat")
	}
}

func TestWorkerPollEmptyQueue(t *testing.T) {
	isolateEnv(t)
	stubLocalExecutor(t, &fakeToolExecutor{})
	var mu sync.Mutex
	polls := 0
	var auth string
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/work/poll") {
			http.Error(w, "unhandled", http.StatusNotFound)
			return
		}
		mu.Lock()
		polls++
		auth = r.Header.Get("Authorization")
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	keyFile := writeSecretFile(t, "ewk-test")

	_, stderr, err := runCmd(t, "",
		"worker", "poll", "env_1",
		"--token-file", keyFile,
		"--base-url", srv.URL,
		"--max-idle", "100ms",
	)
	if err != nil {
		t.Fatalf("worker poll failed: %v\n%s", err, stderr)
	}
	mu.Lock()
	defer mu.Unlock()
	if polls < 1 {
		t.Fatal("want at least one poll")
	}
	if auth != "Bearer ewk-test" {
		t.Errorf("poll auth = %q, want Bearer ewk-test", auth)
	}
}

// Delete only poller-owned secret files after reading them.
func TestWorkerRunRemovesPollerSecretFile(t *testing.T) {
	isolateEnv(t)
	stubLocalExecutor(t, &fakeToolExecutor{})
	fake := newWorkerRunFake()
	srv := serve(t, fake.handler())
	secretFile := writeSecretFile(t, "swt-polled")
	t.Setenv("KHA_WORK_SECRET_FILE", secretFile)

	_, stderr, err := runCmd(t, "",
		"worker", "run",
		"--environment-id", "env_1",
		"--work-id", "work_1",
		"--session-id", "sess_1",
		"--worker-id", "wk-t",
		"--generation", "3",
		"--token-file", secretFile,
		"--base-url", srv.URL,
		"--max-idle", "100ms",
		"--poll-interval", "10ms",
	)
	if err != nil {
		t.Fatalf("worker run failed: %v\n%s", err, stderr)
	}
	if _, err := os.Stat(secretFile); !os.IsNotExist(err) {
		t.Fatalf("poller-provided secret file should be removed after load, stat err = %v", err)
	}
}

func TestWorkerRunKeepsUserSecretFile(t *testing.T) {
	isolateEnv(t)
	stubLocalExecutor(t, &fakeToolExecutor{})
	fake := newWorkerRunFake()
	srv := serve(t, fake.handler())
	secretFile := writeSecretFile(t, "swt-user")

	_, stderr, err := runCmd(t, "",
		"worker", "run",
		"--environment-id", "env_1",
		"--work-id", "work_1",
		"--session-id", "sess_1",
		"--worker-id", "wk-t",
		"--generation", "3",
		"--token-file", secretFile,
		"--base-url", srv.URL,
		"--max-idle", "100ms",
		"--poll-interval", "10ms",
	)
	if err != nil {
		t.Fatalf("worker run failed: %v\n%s", err, stderr)
	}
	if _, err := os.Stat(secretFile); err != nil {
		t.Fatalf("user-provided secret file must be kept, stat err = %v", err)
	}
}

func TestShellTimeoutForKeepsMargin(t *testing.T) {
	if got := shellTimeoutFor(5 * time.Minute); got != 4*time.Minute {
		t.Fatalf("shellTimeoutFor(5m) = %v, want 4m", got)
	}
	if got := shellTimeoutFor(time.Second); got != 800*time.Millisecond {
		t.Fatalf("shellTimeoutFor(1s) = %v, want 800ms", got)
	}
	if got := shellTimeoutFor(0); got != time.Second {
		t.Fatalf("shellTimeoutFor(0) = %v, want floor 1s", got)
	}
}

// TestWorkerPollLogFormatJSON: json mode emits structured records on stderr
// carrying the queue identity, and the text default keeps human output.
func TestWorkerPollLogFormatJSON(t *testing.T) {
	isolateEnv(t)
	stubLocalExecutor(t, &fakeToolExecutor{})
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/work/poll") {
			http.Error(w, "unhandled", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	keyFile := writeSecretFile(t, "ewk-test")

	_, stderr, err := runCmd(t, "",
		"worker", "poll", "env_1",
		"--token-file", keyFile,
		"--base-url", srv.URL,
		"--max-idle", "100ms",
		"--log-format", "json",
	)
	if err != nil {
		t.Fatalf("worker poll: %v\n%s", err, stderr)
	}
	var found bool
	for line := range strings.Lines(stderr) {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line not JSON: %q", line)
		}
		if rec["environment_id"] == "env_1" && rec["worker_id"] != "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no JSON log record with environment_id/worker_id:\n%s", stderr)
	}

	// Invalid format is a usage error before any request.
	if _, _, err := runCmd(t, "",
		"worker", "poll", "env_1",
		"--token-file", keyFile,
		"--base-url", srv.URL,
		"--log-format", "xml",
	); err == nil {
		t.Fatal("--log-format xml should fail")
	}
}
