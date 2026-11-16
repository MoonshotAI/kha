package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MoonshotAI/kha/internal/creds"
	"github.com/MoonshotAI/kha/internal/tools"
)

func runTestConfig(t *testing.T, baseURL string, executor tools.Executor) RunConfig {
	t.Helper()
	client, err := creds.NewClient(baseURL, "swt-test")
	if err != nil {
		t.Fatal(err)
	}
	return RunConfig{
		Client:       client,
		Executor:     executor,
		PollInterval: 10 * time.Millisecond,
		ToolTimeout:  5 * time.Second,
		SendWindow:   2 * time.Second,
		MaxIdle:      120 * time.Millisecond,
	}
}

func testWorkRef() WorkRef {
	return WorkRef{
		EnvironmentID: "env_1",
		WorkID:        "work_1",
		SessionID:     "sess_1",
		WorkerID:      "wk-t",
		Generation:    3,
		LeaseTTL:      time.Minute,
	}
}

func indexOf(calls []string, call string) int {
	for i, c := range calls {
		if c == call {
			return i
		}
	}
	return -1
}

// An unknown initial lease TTL must converge from the first heartbeat so the second wait uses ttl/2.
func TestHeartbeatConvergesBeforeSecondWait(t *testing.T) {
	fake := newFakeWorkAPI(t)
	fake.onHeartbeat = func(w http.ResponseWriter, _ *http.Request, _ string) {
		_, _ = fmt.Fprint(w, heartbeatJSONWithTTL("active", 3, 1))
	}
	baseURL := fake.serve(t)
	client, err := creds.NewClient(baseURL, "swt-test")
	if err != nil {
		t.Fatal(err)
	}
	ref := testWorkRef()
	ref.LeaseTTL = 0
	hb := &heartbeat{client: client, ref: ref, log: slog.Default()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{})
	go hb.loop(ctx, cancel, ready, func(error) {})
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("activation heartbeat did not succeed")
	}
	deadline := time.After(3 * time.Second)
	for fake.count("heartbeat") < 2 {
		select {
		case <-deadline:
			t.Fatalf("second heartbeat did not fire within ttl/2; calls = %v", fake.snapshot())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestRunOneWorkExecutesAndSettles(t *testing.T) {
	fake := newFakeWorkAPI(t)
	fake.onEvents = func(w http.ResponseWriter, _ *http.Request) {
		page := toolUseEventJSON("evt_1", "tu_1", "shell", `{"command":"echo hi"}`) + "," + sessionStatusJSON("evt_0", "idle")
		if len(fake.bodies("send")) > 0 {
			page = toolResultEventJSON("evt_2", "tu_1") + "," + page
		}
		_, _ = fmt.Fprintf(w, `{"items":[%s]}`, page)
	}
	baseURL := fake.serve(t)
	executor := &fakeExecutor{}

	err := RunOneWork(context.Background(), runTestConfig(t, baseURL, executor), testWorkRef())
	if err != nil {
		t.Fatalf("RunOneWork = %v, want nil after idle settlement", err)
	}

	calls := fake.snapshot()
	if indexOf(calls, "heartbeat") < 0 || indexOf(calls, "events") < 0 {
		t.Fatalf("missing heartbeat or events call: %v", calls)
	}
	if indexOf(calls, "heartbeat") > indexOf(calls, "events") {
		t.Fatalf("events consumed before activation: %v", calls)
	}
	if executor.callCount() != 1 {
		t.Fatalf("executor calls = %d, want 1", executor.callCount())
	}
	if got := executor.calls[0].Name; got != "shell" {
		t.Fatalf("executor tool = %q, want shell", got)
	}

	sends := fake.bodies("send")
	if len(sends) != 1 {
		t.Fatalf("send bodies = %d, want exactly 1 tool result per execution", len(sends))
	}
	for _, want := range []string{`"type":"user.tool_result"`, `"tool_use_id":"tu_1"`, `"is_error":false`, `"text":"ok"`} {
		if !strings.Contains(sends[0], want) {
			t.Errorf("send body missing %s:\n%s", want, sends[0])
		}
	}
	if strings.Contains(sends[0], "swt-") {
		t.Errorf("send body leaks secret:\n%s", sends[0])
	}

	stops := fake.bodies("stop")
	if len(stops) != 1 {
		t.Fatalf("stop bodies = %d, want 1", len(stops))
	}
	for _, want := range []string{`"final_status":"completed"`, `"worker_id":"wk-t"`, `"generation":3`} {
		if !strings.Contains(stops[0], want) {
			t.Errorf("stop body missing %s:\n%s", want, stops[0])
		}
	}
}

// Descending pages require an answered-event pass before dispatch so answered calls cannot execute again.
func TestRunOneWorkDedupAnsweredCall(t *testing.T) {
	fake := newFakeWorkAPI(t)
	fake.onEvents = func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"items":[%s,%s,%s]}`,
			toolResultEventJSON("evt_2", "tu_1"),
			toolUseEventJSON("evt_1", "tu_1", "shell", `{"command":"echo hi"}`),
			sessionStatusJSON("evt_0", "idle"))
	}
	baseURL := fake.serve(t)
	executor := &fakeExecutor{}

	err := RunOneWork(context.Background(), runTestConfig(t, baseURL, executor), testWorkRef())
	if err != nil {
		t.Fatalf("RunOneWork = %v, want nil after idle settlement", err)
	}
	if executor.callCount() != 0 {
		t.Fatalf("executor calls = %d, want 0 for an already-answered call", executor.callCount())
	}
	if got := len(fake.bodies("send")); got != 0 {
		t.Fatalf("send bodies = %d, want 0", got)
	}
	if got := len(fake.bodies("stop")); got != 1 {
		t.Fatalf("stop bodies = %d, want 1", got)
	}
}

// A stopping heartbeat must cancel in-flight tools before their results are settled.
func TestRunOneWorkStoppingSettles(t *testing.T) {
	fake := newFakeWorkAPI(t)
	executor := &fakeExecutor{}
	fake.onHeartbeat = func(w http.ResponseWriter, _ *http.Request, _ string) {
		if executor.callCount() > 0 {
			_, _ = fmt.Fprint(w, heartbeatJSONWithTTL("stopping", 3, 1))
			return
		}
		_, _ = fmt.Fprint(w, heartbeatJSONWithTTL("active", 3, 1))
	}
	fake.onEvents = func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"items":[%s]}`, toolUseEventJSON("evt_1", "tu_1", "shell", `{"command":"sleep 60"}`))
	}
	baseURL := fake.serve(t)

	cancelled := make(chan struct{})
	executor.onExecute = func(ctx context.Context, _ tools.Call) (tools.Result, error) {
		<-ctx.Done()
		close(cancelled)
		return tools.Result{}, ctx.Err()
	}

	cfg := runTestConfig(t, baseURL, executor)
	// Disable idle exit so it cannot race the stopping transition.
	cfg.MaxIdle = 0
	ref := testWorkRef()
	ref.LeaseTTL = 100 * time.Millisecond

	err := RunOneWork(context.Background(), cfg, ref)
	if err != nil {
		t.Fatalf("RunOneWork = %v, want nil after stop settlement", err)
	}
	select {
	case <-cancelled:
	default:
		t.Fatal("in-flight tool was not cancelled on stopping")
	}
	if got := len(fake.bodies("send")); got != 1 {
		t.Fatalf("send bodies = %d, want 1 (result write completes during settlement)", got)
	}
	stops := fake.bodies("stop")
	if len(stops) != 1 {
		t.Fatalf("stop bodies = %d, want 1", len(stops))
	}
	if !strings.Contains(stops[0], `"final_status":"completed"`) {
		t.Fatalf("stop body missing completed:\n%s", stops[0])
	}
}

// A lease conflict on activation must abandon the run without consuming events or submitting Stop.
func TestRunOneWorkAbandonsOn412(t *testing.T) {
	fake := newFakeWorkAPI(t)
	fake.onHeartbeat = func(w http.ResponseWriter, _ *http.Request, _ string) {
		w.WriteHeader(http.StatusPreconditionFailed)
		_, _ = fmt.Fprint(w, errorEnvelope("failed_precondition_error", "stale generation"))
	}
	baseURL := fake.serve(t)
	executor := &fakeExecutor{}

	err := RunOneWork(context.Background(), runTestConfig(t, baseURL, executor), testWorkRef())
	if !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("RunOneWork = %v, want ErrLeaseLost", err)
	}
	if got := fake.count("events"); got != 0 {
		t.Fatalf("events calls = %d, want 0 before activation", got)
	}
	if executor.callCount() != 0 {
		t.Fatalf("executor calls = %d, want 0", executor.callCount())
	}
	if got := fake.count("stop"); got != 0 {
		t.Fatalf("stop calls = %d, want 0 (abandon must not settle)", got)
	}
}

// After losing the lease, the worker must not stop or overwrite the new owner's session.
func TestRunOneWorkAbandonsOnMidflight412(t *testing.T) {
	fake := newFakeWorkAPI(t)
	heartbeats := 0
	fake.onHeartbeat = func(w http.ResponseWriter, _ *http.Request, _ string) {
		heartbeats++
		if heartbeats > 1 {
			w.WriteHeader(http.StatusPreconditionFailed)
			_, _ = fmt.Fprint(w, errorEnvelope("failed_precondition_error", "stale generation"))
			return
		}
		_, _ = fmt.Fprint(w, heartbeatJSONWithTTL("active", 3, 1))
	}
	baseURL := fake.serve(t)
	executor := &fakeExecutor{}

	cfg := runTestConfig(t, baseURL, executor)
	cfg.MaxIdle = 0
	ref := testWorkRef()
	ref.LeaseTTL = 100 * time.Millisecond

	err := RunOneWork(context.Background(), cfg, ref)
	if !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("RunOneWork = %v, want ErrLeaseLost", err)
	}
	if got := fake.count("stop"); got != 0 {
		t.Fatalf("stop calls = %d, want 0 (abandon must not overwrite the new owner)", got)
	}
}

// A busy-session conflict must preserve the lease and deduplication state for the next idle settlement.
func TestRunOneWorkIdleConflictKeepsServing(t *testing.T) {
	fake := newFakeWorkAPI(t)
	stops := 0
	fake.onStop = func(w http.ResponseWriter, _ *http.Request, _ string) {
		stops++
		if stops == 1 {
			w.WriteHeader(http.StatusConflict)
			_, _ = fmt.Fprint(w, errorEnvelope("failed_precondition_error", "session not dormant"))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
	fake.onEvents = func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"items":[%s]}`, sessionStatusJSON("evt_0", "idle"))
	}
	baseURL := fake.serve(t)
	executor := &fakeExecutor{}

	err := RunOneWork(context.Background(), runTestConfig(t, baseURL, executor), testWorkRef())
	if err != nil {
		t.Fatalf("RunOneWork = %v, want nil after idle settlement", err)
	}
	if got := fake.count("stop"); got != 2 {
		t.Fatalf("stop calls = %d, want 2 (409 keeps the lease and serves on)", got)
	}
}

// The deduplication key must include threadID because different threads may reuse a tool_use_id.
func TestRunOneWorkFanInChildCall(t *testing.T) {
	fake := newFakeWorkAPI(t)
	fake.onEvents = func(w http.ResponseWriter, _ *http.Request) {
		page := childToolUseEventJSON("evt_c1", "sthr_b", "tu_1", "read_file", `{"path":"/tmp/a"}`) +
			"," + childToolUseEventJSON("evt_c2", "sthr_c", "tu_1", "read_file", `{"path":"/tmp/b"}`) +
			"," + sessionStatusJSON("evt_0", "idle")
		if len(fake.bodies("send")) >= 2 {
			page = childToolResultEventJSON("evt_r1", "sthr_b", "tu_1") + "," +
				childToolResultEventJSON("evt_r2", "sthr_c", "tu_1") + "," + page
		}
		_, _ = fmt.Fprintf(w, `{"items":[%s]}`, page)
	}
	baseURL := fake.serve(t)
	executor := &fakeExecutor{}

	err := RunOneWork(context.Background(), runTestConfig(t, baseURL, executor), testWorkRef())
	if err != nil {
		t.Fatalf("RunOneWork = %v, want nil after idle settlement", err)
	}
	if executor.callCount() != 2 {
		t.Fatalf("executor calls = %d, want 2 (same tool_use_id on two threads)", executor.callCount())
	}

	sends := fake.bodies("send")
	if len(sends) != 2 {
		t.Fatalf("send bodies = %d, want 2", len(sends))
	}
	echoed := map[string]bool{}
	for _, body := range sends {
		for _, want := range []string{`"type":"user.tool_result"`, `"tool_use_id":"tu_1"`} {
			if !strings.Contains(body, want) {
				t.Errorf("send body missing %s:\n%s", want, body)
			}
		}
		for _, thread := range []string{"sthr_b", "sthr_c"} {
			if strings.Contains(body, `"session_thread_id":"`+thread+`"`) {
				echoed[thread] = true
			}
		}
	}
	if !echoed["sthr_b"] || !echoed["sthr_c"] {
		t.Errorf("results not echoed to both threads: %v\n%v", echoed, sends)
	}
}

// Writing results for platform-owned calls would race the platform loop's own results.
func TestRunOneWorkSkipsPlatformToolCall(t *testing.T) {
	fake := newFakeWorkAPI(t)
	fake.onEvents = func(w http.ResponseWriter, _ *http.Request) {
		page := toolUseEventJSON("evt_p", "spawn_subagent:1", "spawn_subagent", `{"agent_type":"x","task":"t"}`) +
			"," + toolUseEventJSON("evt_1", "tu_1", "shell", `{"command":"echo hi"}`) +
			"," + sessionStatusJSON("evt_0", "idle")
		if len(fake.bodies("send")) > 0 {
			page = toolResultEventJSON("evt_2", "tu_1") + "," + page
		}
		_, _ = fmt.Fprintf(w, `{"items":[%s]}`, page)
	}
	baseURL := fake.serve(t)
	executor := &fakeExecutor{}

	err := RunOneWork(context.Background(), runTestConfig(t, baseURL, executor), testWorkRef())
	if err != nil {
		t.Fatalf("RunOneWork = %v, want nil after idle settlement", err)
	}
	if executor.callCount() != 1 {
		t.Fatalf("executor calls = %d, want 1 (platform call skipped)", executor.callCount())
	}
	if got := executor.calls[0].Name; got != "shell" {
		t.Fatalf("executor tool = %q, want shell", got)
	}
	sends := fake.bodies("send")
	if len(sends) != 1 {
		t.Fatalf("send bodies = %d, want 1 (no result for the platform call)", len(sends))
	}
	if !strings.Contains(sends[0], `"tool_use_id":"tu_1"`) {
		t.Errorf("send body should reference only the sandbox call:\n%s", sends[0])
	}
}

func TestRunOneWorkAwaitsToolConfirmation(t *testing.T) {
	fake := newFakeWorkAPI(t)
	fake.onEvents = func(w http.ResponseWriter, _ *http.Request) {
		page := toolUseAskEventJSON("evt_a", "tu_a", "shell", `{"command":"echo hi"}`) +
			"," + sessionStatusJSON("evt_0", "idle")
		_, _ = fmt.Fprintf(w, `{"items":[%s]}`, page)
	}
	baseURL := fake.serve(t)
	executor := &fakeExecutor{}

	err := RunOneWork(context.Background(), runTestConfig(t, baseURL, executor), testWorkRef())
	if err != nil {
		t.Fatalf("RunOneWork = %v, want nil after idle settlement", err)
	}
	if executor.callCount() != 0 {
		t.Fatalf("executor calls = %d, want 0 (unapproved call must not execute)", executor.callCount())
	}
	if sends := fake.bodies("send"); len(sends) != 0 {
		t.Fatalf("send bodies = %d, want 0 (no result without execution)", len(sends))
	}
}

// Descending pages require pending calls to be collected before confirmations are applied in chronological order.
func TestRunOneWorkConfirmationAllowExecutes(t *testing.T) {
	fake := newFakeWorkAPI(t)
	fake.onEvents = func(w http.ResponseWriter, _ *http.Request) {
		page := toolConfirmationEventJSON("evt_c", "evt_a", "allow") +
			"," + toolUseAskEventJSON("evt_a", "tu_a", "shell", `{"command":"echo hi"}`) +
			"," + sessionStatusJSON("evt_0", "idle")
		_, _ = fmt.Fprintf(w, `{"items":[%s]}`, page)
	}
	baseURL := fake.serve(t)
	executor := &fakeExecutor{}

	err := RunOneWork(context.Background(), runTestConfig(t, baseURL, executor), testWorkRef())
	if err != nil {
		t.Fatalf("RunOneWork = %v, want nil after idle settlement", err)
	}
	if executor.callCount() != 1 {
		t.Fatalf("executor calls = %d, want 1 (approved call executes)", executor.callCount())
	}
	sends := fake.bodies("send")
	if len(sends) != 1 {
		t.Fatalf("send bodies = %d, want 1", len(sends))
	}
	if !strings.Contains(sends[0], `"tool_use_id":"tu_a"`) {
		t.Errorf("send body should reference the approved call:\n%s", sends[0])
	}
}

// The platform loop owns denial results; worker writes would race them.
func TestRunOneWorkConfirmationDenySkips(t *testing.T) {
	fake := newFakeWorkAPI(t)
	fake.onEvents = func(w http.ResponseWriter, _ *http.Request) {
		page := toolConfirmationEventJSON("evt_c", "evt_a", "deny") +
			"," + toolUseAskEventJSON("evt_a", "tu_a", "shell", `{"command":"echo hi"}`) +
			"," + sessionStatusJSON("evt_0", "idle")
		_, _ = fmt.Fprintf(w, `{"items":[%s]}`, page)
	}
	baseURL := fake.serve(t)
	executor := &fakeExecutor{}

	err := RunOneWork(context.Background(), runTestConfig(t, baseURL, executor), testWorkRef())
	if err != nil {
		t.Fatalf("RunOneWork = %v, want nil after idle settlement", err)
	}
	if executor.callCount() != 0 {
		t.Fatalf("executor calls = %d, want 0 (denied call must not execute)", executor.callCount())
	}
	if sends := fake.bodies("send"); len(sends) != 0 {
		t.Fatalf("send bodies = %d, want 0 (denied call writes nothing)", len(sends))
	}
}

func TestRunOneWorkConfirmationAcrossPolls(t *testing.T) {
	fake := newFakeWorkAPI(t)
	fake.onEvents = func(w http.ResponseWriter, _ *http.Request) {
		page := toolUseAskEventJSON("evt_a", "tu_a", "shell", `{"command":"echo hi"}`) +
			"," + sessionStatusJSON("evt_0", "idle")
		if fake.count("events") >= 3 {
			page = toolConfirmationEventJSON("evt_c", "evt_a", "allow") + "," + page
		}
		_, _ = fmt.Fprintf(w, `{"items":[%s]}`, page)
	}
	baseURL := fake.serve(t)
	executor := &fakeExecutor{}

	err := RunOneWork(context.Background(), runTestConfig(t, baseURL, executor), testWorkRef())
	if err != nil {
		t.Fatalf("RunOneWork = %v, want nil after idle settlement", err)
	}
	if executor.callCount() != 1 {
		t.Fatalf("executor calls = %d, want 1 (late confirmation still executes)", executor.callCount())
	}
}

// Pending calls must retain session_thread_id so confirmed child results return to their originating thread.
func TestRunOneWorkConfirmationChildEcho(t *testing.T) {
	fake := newFakeWorkAPI(t)
	fake.onEvents = func(w http.ResponseWriter, _ *http.Request) {
		page := toolConfirmationEventJSON("evt_c", "evt_k", "allow") +
			"," + childToolUseAskEventJSON("evt_k", "sthr_b", "tu_1", "read_file", `{"path":"/tmp/a"}`) +
			"," + sessionStatusJSON("evt_0", "idle")
		_, _ = fmt.Fprintf(w, `{"items":[%s]}`, page)
	}
	baseURL := fake.serve(t)
	executor := &fakeExecutor{}

	err := RunOneWork(context.Background(), runTestConfig(t, baseURL, executor), testWorkRef())
	if err != nil {
		t.Fatalf("RunOneWork = %v, want nil after idle settlement", err)
	}
	if executor.callCount() != 1 {
		t.Fatalf("executor calls = %d, want 1", executor.callCount())
	}
	sends := fake.bodies("send")
	if len(sends) != 1 {
		t.Fatalf("send bodies = %d, want 1", len(sends))
	}
	for _, want := range []string{`"tool_use_id":"tu_1"`, `"session_thread_id":"sthr_b"`} {
		if !strings.Contains(sends[0], want) {
			t.Errorf("send body missing %s:\n%s", want, sends[0])
		}
	}
}
