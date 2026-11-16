package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	kha "github.com/MoonshotAI/kha"
)

func newTestPoller(t *testing.T, baseURL string, cfg PollConfig) *Poller {
	t.Helper()
	if cfg.PollClient == nil {
		client, err := kha.NewPollClient(baseURL, "ewk-test")
		if err != nil {
			t.Fatal(err)
		}
		cfg.PollClient = client
	}
	cfg.BaseURL = baseURL
	if cfg.EnvironmentID == "" {
		cfg.EnvironmentID = "env_1"
	}
	if cfg.WorkerID == "" {
		cfg.WorkerID = "wk-p"
	}
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 10 * time.Millisecond
	}
	if cfg.SendWindow == 0 {
		cfg.SendWindow = 2 * time.Second
	}
	if cfg.ToolTimeout == 0 {
		cfg.ToolTimeout = 5 * time.Second
	}
	poller, err := NewPoller(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return poller
}

// Ack must precede dispatch so heartbeats and settlement use the delivered session token and generation.
func TestPollerClaimAckDispatchOrder(t *testing.T) {
	fake := newFakeWorkAPI(t)
	polls := 0
	fake.onPoll = func(w http.ResponseWriter, _ *http.Request) {
		polls++
		if polls > 1 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = fmt.Fprint(w, workItemJSON("work_1", "sess_1", "queued", 3, testSecretBundle("swt-delivery")))
	}
	fake.onEvents = func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"items":[%s]}`, sessionStatusJSON("evt_0", "idle"))
	}
	baseURL := fake.serve(t)

	poller := newTestPoller(t, baseURL, PollConfig{
		Executor:       &fakeExecutor{},
		QueueMaxIdle:   120 * time.Millisecond,
		SessionMaxIdle: 20 * time.Millisecond,
	})
	if err := poller.Run(context.Background()); err != nil {
		t.Fatalf("poller.Run = %v", err)
	}

	calls := fake.snapshot()
	if got := indexOf(calls, "poll"); got != 0 {
		t.Fatalf("first call = %d, want poll first: %v", got, calls)
	}
	if indexOf(calls, "ack") < indexOf(calls, "poll") || indexOf(calls, "heartbeat") < indexOf(calls, "ack") {
		t.Fatalf("want poll < ack < heartbeat: %v", calls)
	}
	if got := fake.count("poll"); got < 2 {
		t.Fatalf("poll calls = %d, want another poll after idle settlement", got)
	}

	acks := fake.bodies("ack")
	if len(acks) != 1 {
		t.Fatalf("ack bodies = %d, want 1", len(acks))
	}
	for _, want := range []string{`"worker_id":"wk-p"`, `"generation":3`} {
		if !strings.Contains(acks[0], want) {
			t.Errorf("ack body missing %s:\n%s", want, acks[0])
		}
	}
	// Poll uses the environment work key; lifecycle endpoints must use the delivered session token.
	fake.mu.Lock()
	authByCall := map[string]string{}
	for i, c := range fake.calls {
		authByCall[c] = fake.auths[i]
	}
	fake.mu.Unlock()
	if got := authByCall["poll"]; got != "Bearer ewk-test" {
		t.Errorf("poll auth = %q, want ewk", got)
	}
	for _, call := range []string{"ack", "heartbeat", "stop"} {
		if got := authByCall[call]; got != "Bearer swt-delivery" {
			t.Errorf("%s auth = %q, want delivery swt", call, got)
		}
	}
	if got := len(fake.bodies("stop")); got != 1 {
		t.Fatalf("stop bodies = %d, want 1", got)
	}
}

// Queue idle is shorter than Session idle: claimed work still settles before queue timing resumes.
func TestPollerContinuesAfterSessionIdle(t *testing.T) {
	fake := newFakeWorkAPI(t)
	polls := 0
	fake.onPoll = func(w http.ResponseWriter, _ *http.Request) {
		polls++
		switch polls {
		case 1:
			_, _ = fmt.Fprint(w, workItemJSON("work_1", "sess_1", "queued", 3, testSecretBundle("swt-1")))
		case 2:
			_, _ = fmt.Fprint(w, workItemJSON("work_2", "sess_2", "queued", 4, testSecretBundle("swt-2")))
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}
	fake.onEvents = func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"items":[%s]}`, sessionStatusJSON("evt_0", "idle"))
	}
	baseURL := fake.serve(t)

	poller := newTestPoller(t, baseURL, PollConfig{
		Executor:       &fakeExecutor{},
		QueueMaxIdle:   40 * time.Millisecond,
		SessionMaxIdle: 60 * time.Millisecond,
		backoffInitial: 5 * time.Millisecond,
		backoffMax:     20 * time.Millisecond,
	})
	if err := poller.Run(context.Background()); err != nil {
		t.Fatalf("poller.Run = %v", err)
	}
	if got := fake.count("ack"); got != 2 {
		t.Fatalf("ack count = %d, want 2 sequential work items", got)
	}
	if got := fake.count("stop"); got != 2 {
		t.Fatalf("stop count = %d, want 2 idle settlements", got)
	}
	if got := fake.count("poll"); got < 3 {
		t.Fatalf("poll count = %d, want an empty poll after both work items", got)
	}
}

func TestPollerEmptyPollBackoff(t *testing.T) {
	fake := newFakeWorkAPI(t)
	baseURL := fake.serve(t)

	poller := newTestPoller(t, baseURL, PollConfig{
		Executor:       &fakeExecutor{},
		QueueMaxIdle:   300 * time.Millisecond,
		backoffInitial: 40 * time.Millisecond,
		backoffMax:     160 * time.Millisecond,
	})
	start := time.Now()
	if err := poller.Run(context.Background()); err != nil {
		t.Fatalf("poller.Run = %v", err)
	}
	if elapsed := time.Since(start); elapsed < 300*time.Millisecond {
		t.Fatalf("poller exited after %s, want ≥ max-idle 300ms", elapsed)
	}
	if got := fake.count("poll"); got < 3 {
		t.Fatalf("poll calls = %d, want ≥ 3 empty polls before idle exit", got)
	}
	fake.mu.Lock()
	queries := append([]string(nil), fake.pollQueries...)
	fake.mu.Unlock()
	if len(queries) != fake.count("poll") {
		t.Fatalf("recorded %d queries for %d polls", len(queries), fake.count("poll"))
	}
}

func TestPollerOnWorkScript(t *testing.T) {
	fake := newFakeWorkAPI(t)
	dir := t.TempDir()
	seqPath := filepath.Join(dir, "seq")
	polls := 0
	fake.onPoll = func(w http.ResponseWriter, _ *http.Request) {
		polls++
		if polls > 1 {
			if polls == 2 {
				// Polling before script exit would break synchronous on-work dispatch.
				f, err := os.OpenFile(seqPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
				if err == nil {
					_, _ = fmt.Fprintln(f, "poll2")
					_ = f.Close()
				}
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = fmt.Fprint(w, workItemJSON("work_1", "sess_1", "queued", 3, testSecretBundle("swt-ondemand")))
	}
	baseURL := fake.serve(t)

	script := filepath.Join(dir, "on-work.sh")
	scriptBody := `#!/bin/sh
cat > "$SEQDIR/stdin.json"
env | grep '^KHA_' > "$SEQDIR/env.txt"
cat "$KHA_WORK_SECRET_FILE" > "$SEQDIR/secret.txt"
(stat -c %a "$KHA_WORK_SECRET_FILE" 2>/dev/null || stat -f %Lp "$KHA_WORK_SECRET_FILE") > "$SEQDIR/mode.txt"
echo started >> "$SEQDIR/seq"
sleep 0.15
echo finished >> "$SEQDIR/seq"
`
	if err := os.WriteFile(script, []byte(scriptBody), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SEQDIR", dir)

	var stdout, stderr bytes.Buffer
	poller := newTestPoller(t, baseURL, PollConfig{
		OnWork:         script,
		QueueMaxIdle:   400 * time.Millisecond,
		backoffInitial: 30 * time.Millisecond,
		backoffMax:     120 * time.Millisecond,
		Stdout:         &stdout,
		Stderr:         &stderr,
	})
	if err := poller.Run(context.Background()); err != nil {
		t.Fatalf("poller.Run = %v", err)
	}

	seq, err := os.ReadFile(seqPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Fields(string(seq)), []string{"started", "finished", "poll2"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("sequence = %v, want %v (poller waits for the script synchronously)", got, want)
	}

	stdin, err := os.ReadFile(filepath.Join(dir, "stdin.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stdin), `"id":"work_1"`) {
		t.Errorf("stdin work JSON missing work id:\n%s", stdin)
	}
	if strings.Contains(string(stdin), "swt-") || strings.Contains(string(stdin), "secret") {
		t.Errorf("stdin work JSON must be sanitized (no secret):\n%s", stdin)
	}

	env, err := os.ReadFile(filepath.Join(dir, "env.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"KHA_BASE_URL=" + baseURL,
		"KHA_WORK_ID=work_1",
		"KHA_ENVIRONMENT_ID=env_1",
		"KHA_SESSION_ID=sess_1",
		"KHA_WORK_GENERATION=3",
		"KHA_WORK_LEASE_TTL_MS=60000",
		"KHA_WORKER_ID=wk-p",
		"KHA_WORK_SECRET_FILE=",
	} {
		if !strings.Contains(string(env), want) {
			t.Errorf("script env missing %s:\n%s", want, env)
		}
	}
	if strings.Contains(string(env), "swt-ondemand") {
		t.Errorf("secret must not appear in the environment:\n%s", env)
	}

	secret, err := os.ReadFile(filepath.Join(dir, "secret.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(secret) != "swt-ondemand" {
		t.Errorf("secret file content = %q, want swt-ondemand", secret)
	}
	if mode, err := os.ReadFile(filepath.Join(dir, "mode.txt")); err != nil || strings.TrimSpace(string(mode)) != "600" {
		t.Errorf("secret file mode = %q, %v; want 600", mode, err)
	}

	// The secret file must be removed when the child exits.
	secretPath := ""
	for line := range strings.SplitSeq(string(env), "\n") {
		if p, ok := strings.CutPrefix(line, "KHA_WORK_SECRET_FILE="); ok {
			secretPath = p
		}
	}
	if secretPath == "" {
		t.Fatal("KHA_WORK_SECRET_FILE not captured")
	}
	if _, err := os.Stat(secretPath); !os.IsNotExist(err) {
		t.Errorf("secret file should be removed after the child exits, stat err = %v", err)
	}
	if got := fake.count("ack"); got != 1 {
		t.Fatalf("ack calls = %d, want 1", got)
	}
}

// A spawn failure must leave acknowledged work for lease recovery rather than submitting Stop.
func TestPollerOnWorkSpawnFailure(t *testing.T) {
	fake := newFakeWorkAPI(t)
	fake.onPoll = func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, workItemJSON("work_1", "sess_1", "queued", 3, testSecretBundle("swt-delivery")))
	}
	baseURL := fake.serve(t)

	// The executable bit passes launcher validation; the missing interpreter fails only at spawn.
	dir := t.TempDir()
	script := filepath.Join(dir, "bad.sh")
	if err := os.WriteFile(script, []byte("#!/nonexistent/interpreter\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	poller := newTestPoller(t, baseURL, PollConfig{OnWork: script, QueueMaxIdle: time.Second})
	err := poller.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "start on-work script") {
		t.Fatalf("poller.Run = %v, want a launcher config error", err)
	}
	if got := fake.count("ack"); got != 1 {
		t.Fatalf("ack calls = %d, want 1 (the acked work is left for lease reclaim)", got)
	}
	if got := fake.count("stop"); got != 0 {
		t.Fatalf("stop calls = %d, want 0", got)
	}
}

func TestPollerOnWorkNonZeroContinues(t *testing.T) {
	fake := newFakeWorkAPI(t)
	polls := 0
	fake.onPoll = func(w http.ResponseWriter, _ *http.Request) {
		polls++
		if polls > 1 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = fmt.Fprint(w, workItemJSON("work_1", "sess_1", "queued", 3, testSecretBundle("swt-delivery")))
	}
	baseURL := fake.serve(t)

	dir := t.TempDir()
	script := filepath.Join(dir, "fail.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	poller := newTestPoller(t, baseURL, PollConfig{
		OnWork:         script,
		QueueMaxIdle:   200 * time.Millisecond,
		backoffInitial: 30 * time.Millisecond,
		backoffMax:     120 * time.Millisecond,
	})
	if err := poller.Run(context.Background()); err != nil {
		t.Fatalf("poller.Run = %v, want nil (non-zero exit continues the loop)", err)
	}
	if got := fake.count("poll"); got < 2 {
		t.Fatalf("poll calls = %d, want ≥ 2 after a failed script", got)
	}
	if got := fake.count("ack"); got != 1 {
		t.Fatalf("ack calls = %d, want 1", got)
	}
}

func TestNewPollerValidatesLauncher(t *testing.T) {
	fake := newFakeWorkAPI(t)
	baseURL := fake.serve(t)
	client, err := kha.NewPollClient(baseURL, "ewk-test")
	if err != nil {
		t.Fatal(err)
	}
	base := PollConfig{PollClient: client, EnvironmentID: "env_1", WorkerID: "wk-p"}

	cfg := base
	cfg.OnWork = filepath.Join(t.TempDir(), "missing.sh")
	if _, err := NewPoller(cfg); err == nil {
		t.Fatal("NewPoller with a missing on-work script should fail")
	}

	dir := t.TempDir()
	notExec := filepath.Join(dir, "noexec.sh")
	if err := os.WriteFile(notExec, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg.OnWork = notExec
	if _, err := NewPoller(cfg); err == nil {
		t.Fatal("NewPoller with a non-executable on-work script should fail")
	}

	cfg = base
	cfg.OnWork = dir
	if _, err := NewPoller(cfg); err == nil {
		t.Fatal("NewPoller with a directory on-work script should fail")
	}

	cfg = base
	cfg.Executor = &fakeExecutor{}
	cfg.BlockMs = 1000
	if _, err := NewPoller(cfg); err == nil {
		t.Fatal("NewPoller with block-ms 1000 should fail (range 0..999)")
	}

	cfg = base
	if _, err := NewPoller(cfg); err == nil {
		t.Fatal("NewPoller in-process mode without executor should fail")
	}
}

// QueueMaxIdle measures claim inactivity only; long-running work must not trigger an idle exit.
func TestPollerMaxIdleExcludesServeTime(t *testing.T) {
	fake := newFakeWorkAPI(t)
	polls := 0
	fake.onPoll = func(w http.ResponseWriter, _ *http.Request) {
		polls++
		if polls > 1 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = fmt.Fprint(w, workItemJSON("work_1", "sess_1", "queued", 3, testSecretBundle("swt-ondemand")))
	}
	baseURL := fake.serve(t)

	dir := t.TempDir()
	script := filepath.Join(dir, "on-work.sh")
	scriptBody := "#!/bin/sh\nsleep 0.25\n"
	if err := os.WriteFile(script, []byte(scriptBody), 0o755); err != nil {
		t.Fatal(err)
	}

	poller := newTestPoller(t, baseURL, PollConfig{
		OnWork:         script,
		QueueMaxIdle:   100 * time.Millisecond,
		backoffInitial: 10 * time.Millisecond,
		backoffMax:     40 * time.Millisecond,
	})
	if err := poller.Run(context.Background()); err != nil {
		t.Fatalf("poller.Run = %v", err)
	}
	if got := fake.count("poll"); got < 2 {
		t.Fatalf("poll count = %d, want >= 2: script execution time counted toward max-idle", got)
	}
}

func TestPollerRejectsPermanentFailure(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusBadRequest, http.StatusUnauthorized, http.StatusPreconditionFailed} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					calls++
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(status)
					if status == http.StatusOK {
						_, _ = fmt.Fprint(w, `"not a work object"`)
					}
				}))
				client, err := kha.New("ewk-test", kha.WithHTTPClient(server.Client()), kha.WithBaseURL(server.URL), kha.WithMaxRetries(0))
				if err != nil {
					t.Fatal(err)
				}
				poller := newTestPoller(t, server.URL, PollConfig{PollClient: client, Executor: &fakeExecutor{}})
				ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
				defer cancel()
				err = poller.Run(ctx)
				if err == nil || errors.Is(err, context.DeadlineExceeded) || calls != 1 {
					t.Fatalf("calls=%d error=%v", calls, err)
				}
				if status == http.StatusUnauthorized || status == http.StatusPreconditionFailed {
					if !errors.Is(err, ErrLeaseLost) {
						t.Fatalf("error=%v, want lease loss", err)
					}
				}
			})
		})
	}
}
