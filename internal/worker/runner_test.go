package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	kha "github.com/MoonshotAI/kha"
	"github.com/MoonshotAI/kha/internal/tools"
)

type sendTransport func(*http.Request) (*http.Response, error)

func (f sendTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestPollOnceConfirmationBeforeRequest(t *testing.T) {
	request := childToolUseAskEventJSON("evt_a", "sthr_b", "tu_a", "shell", `{"command":"echo hi"}`)
	allow := toolConfirmationEventJSON("evt_c", "evt_a", "allow")
	deny := toolConfirmationEventJSON("evt_c", "evt_a", "deny")
	result := childToolResultEventJSON("evt_r", "sthr_b", "tu_a")
	for _, tt := range []struct {
		name      string
		pages     []string
		wantCalls int
	}{
		{name: "allow same page", pages: []string{request + "," + allow}, wantCalls: 1},
		{name: "deny same page", pages: []string{request + "," + deny}},
		{name: "allow across polls", pages: []string{allow, request}, wantCalls: 1},
		{name: "deny across polls", pages: []string{deny, request}},
		{name: "first deny wins", pages: []string{deny, toolConfirmationEventJSON("evt_c2", "evt_a", "allow"), request}},
		{name: "first allow wins", pages: []string{allow, toolConfirmationEventJSON("evt_c2", "evt_a", "deny"), request}, wantCalls: 1},
		{name: "result before request", pages: []string{allow, request + "," + result}},
		{name: "result while awaiting confirmation", pages: []string{request, result, allow}},
		{name: "platform call", pages: []string{allow, toolUseAskEventJSON("evt_a", "tu_a", "spawn_subagent", `{}`)}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				fake := newFakeWorkAPI(t)
				fake.onEvents = func(w http.ResponseWriter, _ *http.Request) {
					page := tt.pages[min(fake.count("events")-1, len(tt.pages)-1)]
					_, _ = fmt.Fprintf(w, `{"items":[%s]}`, page)
				}
				executor := &fakeExecutor{}
				cfg := runTestConfig(t, fake.serve(t), executor)
				r := newRunner(cfg.Client, "sess_1", executor, "", nil,
					cfg.PollInterval, cfg.ToolTimeout, 0, cfg.SendWindow, nil)
				// The extra final-page read guards against re-executing matched calls.
				for i := 0; i <= len(tt.pages); i++ {
					if err := r.pollOnce(t.Context()); err != nil {
						t.Fatal(err)
					}
					r.wg.Wait()
					if i < len(tt.pages)-1 && executor.callCount() != 0 {
						t.Fatal("tool executed before request and confirmation were paired")
					}
				}
				if got := executor.callCount(); got != tt.wantCalls {
					t.Fatalf("executor calls = %d, want %d", got, tt.wantCalls)
				}
				if len(r.feed.pending) != 0 {
					t.Fatalf("pending calls after processing = %d, want 0", len(r.feed.pending))
				}
				sends := fake.bodies("send")
				if len(sends) != tt.wantCalls {
					t.Fatalf("send bodies = %d, want %d", len(sends), tt.wantCalls)
				}
				if tt.wantCalls > 0 {
					if got := string(executor.calls[0].Input); got != `{"command":"echo hi"}` {
						t.Errorf("executor input = %s, want original shell arguments", got)
					}
					for _, want := range []string{`"tool_use_id":"tu_a"`, `"session_thread_id":"sthr_b"`} {
						if !strings.Contains(sends[0], want) {
							t.Errorf("send body missing %s: %s", want, sends[0])
						}
					}
				}
			})
		})
	}
}

func TestPollOnceConfirmationAcrossPages(t *testing.T) {
	allow := toolConfirmationEventJSON("evt_c", "evt_a", "allow")
	page := strings.Replace(fullDescPage("evt", "older"), sessionStatusJSON("evt_200", "idle"), allow, 1)
	request := childToolUseAskEventJSON("evt_a", "sthr_b", "tu_a", "shell", `{"command":"echo hi"}`)
	tr := &eventsTransport{pages: map[string]string{
		"":      page,
		"older": `{"items":[` + request + `]}`,
	}}
	executor := &fakeExecutor{}
	r := newRunner(newTransportClient(t, tr), "sess_1", executor, "", nil, 0, 0, 0, 0, nil)
	if err := r.pollOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	r.wg.Wait()
	if got := tr.requestCount(); got != 2 {
		t.Fatalf("ListEvents requests = %d, want 2", got)
	}
	if got := executor.callCount(); got != 1 {
		t.Fatalf("executor calls = %d, want 1 (approved request in older page)", got)
	}
	if len(r.feed.pending) != 0 {
		t.Fatalf("pending calls = %d, want 0", len(r.feed.pending))
	}
}

func TestSendToolResultRetryOutcome(t *testing.T) {
	for _, tt := range []struct {
		name       string
		statuses   []int // 0 blocks until request cancellation; last status repeats.
		parent     string
		wantWindow bool
		wantStatus int
		wantCalls  int // 0 means multiple attempts.
	}{
		{name: "success", statuses: []int{202}, wantCalls: 1},
		{name: "retry succeeds", statuses: []int{503, 202}, wantCalls: 2},
		{name: "lease lost", statuses: []int{412}, wantStatus: 412, wantCalls: 1},
		{name: "last failure retained", statuses: []int{503}, wantWindow: true, wantStatus: 503},
		{name: "inflight request cancelled", statuses: []int{0}, wantWindow: true, wantCalls: 1},
		{name: "failure before hung request retained", statuses: []int{503, 0}, wantWindow: true, wantStatus: 503, wantCalls: 2},
		{name: "caller cancellation", statuses: []int{503}, parent: "cancel", wantCalls: 1},
		{name: "caller deadline", statuses: []int{0}, parent: "deadline", wantCalls: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx := t.Context()
				cancel := func() {}
				switch tt.parent {
				case "cancel":
					ctx, cancel = context.WithCancel(ctx)
				case "deadline":
					ctx, cancel = context.WithTimeout(ctx, time.Second)
				}
				defer cancel()
				calls := 0
				client, err := kha.New("swt-test", kha.WithMaxRetries(0),
					kha.WithHTTPClient(&http.Client{Transport: sendTransport(func(req *http.Request) (*http.Response, error) {
						status := tt.statuses[min(calls, len(tt.statuses)-1)]
						calls++
						if tt.parent == "cancel" {
							cancel()
						}
						if status == 0 {
							<-req.Context().Done()
							return nil, req.Context().Err()
						}
						body := `{}`
						if status >= 400 {
							body = errorEnvelope("server_error", "send backend unavailable")
						}
						return &http.Response{
							StatusCode: status,
							Header:     http.Header{"Content-Type": {"application/json"}},
							Body:       io.NopCloser(strings.NewReader(body)),
						}, nil
					})}))
				if err != nil {
					t.Fatal(err)
				}
				r := runner{client: client, sessionID: "sess_1", sendWindow: 5 * time.Minute}
				started := time.Now()
				err = r.sendToolResult(ctx, "", "tu_1", tools.Result{})
				if tt.wantCalls == 0 && calls < 2 || tt.wantCalls != 0 && calls != tt.wantCalls {
					t.Fatalf("calls = %d, want %d (0 means multiple)", calls, tt.wantCalls)
				}
				if tt.parent != "" {
					if !errors.Is(err, ctx.Err()) || strings.Contains(err.Error(), "window") {
						t.Fatalf("caller cancellation/deadline changed: %v", err)
					}
					return
				}
				if tt.wantWindow {
					if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "send window (5m0s) exhausted") {
						t.Fatalf("missing window context: %v", err)
					}
					if elapsed := time.Since(started); elapsed != r.sendWindow {
						t.Fatalf("send took %s, want %s", elapsed, r.sendWindow)
					}
				}
				if tt.wantStatus != 0 {
					apiErr, ok := errors.AsType[kha.HTTPError](err)
					detail, hasDetail := kha.ErrorDetails(err)
					if !ok || apiErr.StatusCode() != tt.wantStatus || !hasDetail || detail.Message != "send backend unavailable" {
						t.Fatalf("lost API failure (%d): %v", tt.wantStatus, err)
					}
				} else if !tt.wantWindow && err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}

type eventsTransport struct {
	mu       sync.Mutex
	tokens   []string
	pages    map[string]string
	fallback string
	sendCode int
}

func (tr *eventsTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	switch {
	case req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/events"):
		token := req.URL.Query().Get("page_token")
		tr.mu.Lock()
		tr.tokens = append(tr.tokens, token)
		page, ok := tr.pages[token]
		tr.mu.Unlock()
		if !ok {
			page = tr.fallback
		}
		if page == "" {
			page = `{"items":[]}`
		}
		return jsonResponse(http.StatusOK, page), nil
	case req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/events"):
		code := tr.sendCode
		if code == 0 {
			code = http.StatusAccepted
		}
		body := `{}`
		if code >= 400 {
			body = errorEnvelope("failed_precondition_error", "stale generation")
		}
		return jsonResponse(code, body), nil
	}
	return nil, fmt.Errorf("unexpected request: %s %s", req.Method, req.URL)
}

func (tr *eventsTransport) requestCount() int {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return len(tr.tokens)
}

func jsonResponse(code int, body string) *http.Response {
	return &http.Response{
		StatusCode: code,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func newTransportClient(t *testing.T, rt http.RoundTripper) *kha.Client {
	t.Helper()
	client, err := kha.New("swt-test", kha.WithMaxRetries(0),
		kha.WithHTTPClient(&http.Client{Transport: rt}))
	if err != nil {
		t.Fatal(err)
	}
	return client
}

type logCapture struct {
	mu   sync.Mutex
	msgs []string
}

func (h *logCapture) Enabled(context.Context, slog.Level) bool { return true }
func (h *logCapture) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	h.msgs = append(h.msgs, r.Message)
	h.mu.Unlock()
	return nil
}
func (h *logCapture) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *logCapture) WithGroup(string) slog.Handler      { return h }

func (h *logCapture) contains(sub string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, m := range h.msgs {
		if strings.Contains(m, sub) {
			return true
		}
	}
	return false
}

func fullDescPage(prefix, nextToken string) string {
	items := make([]string, 0, eventPageSize)
	for i := eventPageSize; i >= 1; i-- {
		items = append(items, sessionStatusJSON(fmt.Sprintf("%s_%d", prefix, i), "idle"))
	}
	return fmt.Sprintf(`{"items":[%s],"next_page_token":%q}`, strings.Join(items, ","), nextToken)
}

// A 412 on result writeback must cancel the run before a call waiting for an execution slot dispatches.
func TestRunnerLeaseLostCancelsDispatch(t *testing.T) {
	page := `{"items":[` +
		toolUseEventJSON("evt_2", "tu_2", "shell", `{"command":"echo b"}`) + `,` +
		toolUseEventJSON("evt_1", "tu_1", "shell", `{"command":"echo a"}`) + `]}`
	tr := &eventsTransport{pages: map[string]string{"": page}, sendCode: http.StatusPreconditionFailed}
	executor := &fakeExecutor{}
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	executor.onExecute = func(context.Context, tools.Call) (tools.Result, error) {
		once.Do(func() { close(started) })
		<-release
		return tools.Result{Content: []tools.ContentBlock{{Type: tools.ContentBlockTypeText, Text: "ok"}}}, nil
	}
	runCtx, stopRunner := context.WithCancelCause(context.Background())
	defer stopRunner(nil)
	r := newRunner(newTransportClient(t, tr), "sess_1", executor, "", nil,
		10*time.Millisecond, time.Minute, 0, time.Minute, stopRunner)
	r.sem = make(chan struct{}, 1)

	done := make(chan error, 1)
	go func() { done <- r.run(runCtx) }()
	<-started
	close(release)
	select {
	case err := <-done:
		if !errors.Is(err, ErrLeaseLost) {
			t.Fatalf("run = %v, want ErrLeaseLost", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not exit after lease loss")
	}
	if got := executor.callCount(); got != 1 {
		t.Fatalf("executor calls = %d, want 1 (second claim aborted on fatal)", got)
	}
	if cause := context.Cause(runCtx); !errors.Is(cause, ErrLeaseLost) {
		t.Fatalf("runCtx cause = %v, want ErrLeaseLost", cause)
	}
}

// A ready fatal error must win over a simultaneously ready execution slot to prevent post-lease-loss dispatch.
func TestHandleToolUseSkipsClaimOnFatal(t *testing.T) {
	executor := &fakeExecutor{}
	r := newRunner(nil, "sess_1", executor, "", nil, 0, 0, 0, 0, nil)
	r.fatal <- ErrLeaseLost
	evt := &kha.AgentToolUseEvent{
		Data: kha.AgentToolUsePayload{
			ToolUse: kha.ToolUseBlock{ID: "tu_1", Name: "shell", Input: map[string]any{"command": "true"}},
		},
	}
	err := r.handleToolUse(context.Background(), r.feed, evt)
	if !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("handleToolUse = %v, want ErrLeaseLost", err)
	}
	r.wg.Wait()
	if got := executor.callCount(); got != 0 {
		t.Fatalf("executor calls = %d, want 0 (no dispatch after fatal)", got)
	}
}

// SessionID and Workdir must reach stateful tools to prevent cross-session state leakage.
func TestExecutePassesSessionIDToToolCall(t *testing.T) {
	executor := &fakeExecutor{}
	r := newRunner(nil, "sess_42", executor, "/tmp/wd", nil, 0, time.Second, 0, 0, nil)
	res := r.execute(context.Background(), "todo_read", nil)
	if res.IsError {
		t.Fatalf("execute failed: %+v", res)
	}
	if len(executor.calls) != 1 {
		t.Fatalf("executor calls = %d, want 1", len(executor.calls))
	}
	call := executor.calls[0]
	if call.SessionID != "sess_42" {
		t.Fatalf("Call.SessionID = %q, want sess_42", call.SessionID)
	}
	if call.Workdir != "/tmp/wd" {
		t.Fatalf("Call.Workdir = %q, want /tmp/wd", call.Workdir)
	}
}

// A full newest page with unseen oldest events requires backtracking so older unanswered calls are not skipped.
func TestPollOnceBacktracksOlderPages(t *testing.T) {
	page1 := fullDescPage("evt", "tok1")
	page2 := `{"items":[` +
		toolUseEventJSON("evt_old_call", "tu_old", "shell", `{"command":"echo old"}`) + `,` +
		sessionStatusJSON("evt_seen", "idle") + `]}`
	tr := &eventsTransport{pages: map[string]string{"": page1, "tok1": page2}}
	executor := &fakeExecutor{}
	r := newRunner(newTransportClient(t, tr), "sess_1", executor, "", nil, 0, 0, 0, 0, nil)
	r.feed.seen["evt_seen"] = true

	if err := r.pollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.wg.Wait()
	if got := tr.requestCount(); got != 2 {
		t.Fatalf("ListEvents requests = %d, want 2 (one backtrack page)", got)
	}
	if got := executor.callCount(); got != 1 {
		t.Fatalf("executor calls = %d, want 1 (older page's unanswered call claimed)", got)
	}
	if got := executor.calls[0].Name; got != "shell" {
		t.Fatalf("executor tool = %q, want shell", got)
	}
}

func TestPollOnceBacktrackCapped(t *testing.T) {
	full := fullDescPage("evt", "tok_next")
	tr := &eventsTransport{pages: map[string]string{"": full}, fallback: full}
	capture := &logCapture{}
	r := newRunner(newTransportClient(t, tr), "sess_1", &fakeExecutor{}, "",
		slog.New(capture), 0, 0, 0, 0, nil)

	if err := r.pollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := tr.requestCount(); got != maxEventBacktrackPages {
		t.Fatalf("ListEvents requests = %d, want %d (backtrack capped)", got, maxEventBacktrackPages)
	}
	if !capture.contains("page cap") {
		t.Fatalf("missing backtrack-cap warning, got %v", capture.msgs)
	}
}

func TestPollOnceSinglePageNoBacktrack(t *testing.T) {
	page := `{"items":[` + sessionStatusJSON("evt_0", "idle") + `]}`
	tr := &eventsTransport{pages: map[string]string{"": page}}
	r := newRunner(newTransportClient(t, tr), "sess_1", &fakeExecutor{}, "", nil, 0, 0, 0, 0, nil)

	if err := r.pollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := tr.requestCount(); got != 1 {
		t.Fatalf("ListEvents requests = %d, want 1 (steady state, no backtrack)", got)
	}
}

// A deterministic protocol decode error must terminate the run instead of being retried forever.
func TestRunExitsOnProtocolError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		fake := newFakeWorkAPI(t)
		fake.onEvents = func(w http.ResponseWriter, _ *http.Request) {
			if fake.count("events") > 1 {
				cancel()
			}
			_, _ = fmt.Fprint(w, `{"items":"not-an-array"}`)
		}
		executor := &fakeExecutor{}
		cfg := runTestConfig(t, fake.serve(t), executor)
		r := newRunner(cfg.Client, "sess_1", executor, "", nil,
			cfg.PollInterval, cfg.ToolTimeout, 0, cfg.SendWindow, nil)

		err := r.run(ctx)
		if err == nil || !kha.IsProtocolError(err) {
			t.Fatalf("run = %v, want the deterministic protocol error", err)
		}
		if got := fake.count("events"); got != 1 {
			t.Fatalf("ListEvents calls = %d, want 1 (fail fast, no backoff retry)", got)
		}
	})
}
