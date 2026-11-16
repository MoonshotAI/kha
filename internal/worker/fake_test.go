package worker

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/MoonshotAI/kha/internal/tools"
)

type fakeWorkAPI struct {
	t *testing.T

	mu          sync.Mutex
	calls       []string // "poll" / "ack" / "heartbeat" / "stop" / "events" / "send"
	auths       []string
	ackBodies   []string
	stopBodies  []string
	sendBodies  []string
	pollQueries []string

	onPoll      func(w http.ResponseWriter, r *http.Request)
	onAck       func(w http.ResponseWriter, r *http.Request, body string)
	onHeartbeat func(w http.ResponseWriter, r *http.Request, body string)
	onStop      func(w http.ResponseWriter, r *http.Request, body string)
	onEvents    func(w http.ResponseWriter, r *http.Request)
	onSend      func(w http.ResponseWriter, r *http.Request, body string)
}

func newFakeWorkAPI(t *testing.T) *fakeWorkAPI {
	t.Helper()
	return &fakeWorkAPI{t: t}
}

func (f *fakeWorkAPI) serve(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(f.route))
	t.Cleanup(srv.Close)
	return srv.URL
}

func (f *fakeWorkAPI) record(call, auth string) {
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.auths = append(f.auths, auth)
	f.mu.Unlock()
}

func (f *fakeWorkAPI) snapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeWorkAPI) count(call string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == call {
			n++
		}
	}
	return n
}

func (f *fakeWorkAPI) bodies(kind string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var src []string
	switch kind {
	case "ack":
		src = f.ackBodies
	case "stop":
		src = f.stopBodies
	case "send":
		src = f.sendBodies
	}
	return append([]string(nil), src...)
}

func readBody(t *testing.T, r *http.Request) string {
	t.Helper()
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		t.Errorf("read request body: %v", err)
	}
	return string(raw)
}

func (f *fakeWorkAPI) route(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/work/poll"):
		f.record("poll", r.Header.Get("Authorization"))
		f.mu.Lock()
		f.pollQueries = append(f.pollQueries, r.URL.RawQuery)
		f.mu.Unlock()
		if f.onPoll != nil {
			f.onPoll(w, r)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/ack"):
		body := readBody(f.t, r)
		f.record("ack", r.Header.Get("Authorization"))
		f.mu.Lock()
		f.ackBodies = append(f.ackBodies, body)
		f.mu.Unlock()
		if f.onAck != nil {
			f.onAck(w, r, body)
			return
		}
		_, _ = fmt.Fprint(w, workItemJSON("work_1", "sess_1", "starting", 3, ""))
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/heartbeat"):
		f.record("heartbeat", r.Header.Get("Authorization"))
		if f.onHeartbeat != nil {
			f.onHeartbeat(w, r, readBody(f.t, r))
			return
		}
		_, _ = fmt.Fprint(w, heartbeatJSON("active", 3))
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/stop"):
		body := readBody(f.t, r)
		f.record("stop", r.Header.Get("Authorization"))
		f.mu.Lock()
		f.stopBodies = append(f.stopBodies, body)
		f.mu.Unlock()
		if f.onStop != nil {
			f.onStop(w, r, body)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/events"):
		f.record("events", r.Header.Get("Authorization"))
		if f.onEvents != nil {
			f.onEvents(w, r)
			return
		}
		_, _ = fmt.Fprint(w, `{"items":[]}`)
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/events"):
		body := readBody(f.t, r)
		f.record("send", r.Header.Get("Authorization"))
		f.mu.Lock()
		f.sendBodies = append(f.sendBodies, body)
		f.mu.Unlock()
		if f.onSend != nil {
			f.onSend(w, r, body)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{}`)
	default:
		f.t.Errorf("unexpected request: %s %s", r.Method, path)
		http.Error(w, "unhandled", http.StatusNotFound)
	}
}

func testSecretBundle(token string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"sessions_token":%q}`, token)))
}

func workItemJSON(id, sessionID, status string, generation int, secret string) string {
	s := fmt.Sprintf(`{"id":%q,"session_id":%q,"environment_id":"env_1","status":%q,"generation":%d,"lease_ttl_ms":60000,"created_at":"2026-01-01T00:00:00Z"`,
		id, sessionID, status, generation)
	if secret != "" {
		s += fmt.Sprintf(`,"secret":%q`, secret)
	}
	return s + `}`
}

func heartbeatJSON(state string, generation int) string {
	return heartbeatJSONWithTTL(state, generation, 300)
}

func heartbeatJSONWithTTL(state string, generation, ttlSeconds int) string {
	return fmt.Sprintf(`{"state":%q,"generation":%d,"ttl_seconds":%d,"lease_extended":true,"last_heartbeat":"2026-01-01T00:00:00Z"}`, state, generation, ttlSeconds)
}

func errorEnvelope(errType, message string) string {
	return fmt.Sprintf(`{"error":{"type":%q,"message":%q,"request_id":"req_fake"}}`, errType, message)
}

func toolUseEventJSON(eventID, toolUseID, name, input string) string {
	return fmt.Sprintf(`{"id":%q,"type":"agent.tool_use","processed_at":"2026-01-01T00:00:01Z","data":{"tool_use":{"id":%q,"name":%q,"input":%s},"evaluated_permission":"always_allow"}}`,
		eventID, toolUseID, name, input)
}

func toolResultEventJSON(eventID, toolUseID string) string {
	return fmt.Sprintf(`{"id":%q,"type":"agent.tool_result","processed_at":"2026-01-01T00:00:02Z","data":{"tool_result":{"tool_use_id":%q,"structured_content":[],"content":[],"is_error":false}}}`,
		eventID, toolUseID)
}

func childToolUseEventJSON(eventID, threadID, toolUseID, name, input string) string {
	return fmt.Sprintf(`{"id":%q,"type":"agent.tool_use","session_thread_id":%q,"processed_at":"2026-01-01T00:00:01Z","data":{"tool_use":{"id":%q,"name":%q,"input":%s},"evaluated_permission":"always_allow"}}`,
		eventID, threadID, toolUseID, name, input)
}

func childToolResultEventJSON(eventID, threadID, toolUseID string) string {
	return fmt.Sprintf(`{"id":%q,"type":"agent.tool_result","session_thread_id":%q,"processed_at":"2026-01-01T00:00:02Z","data":{"tool_result":{"tool_use_id":%q,"structured_content":[],"content":[],"is_error":false}}}`,
		eventID, threadID, toolUseID)
}

func sessionStatusJSON(eventID, status string) string {
	return fmt.Sprintf(`{"id":%q,"type":"session.status","processed_at":"2026-01-01T00:00:03Z","data":{"status":%q}}`, eventID, status)
}

func toolUseAskEventJSON(eventID, toolUseID, name, input string) string {
	return fmt.Sprintf(`{"id":%q,"type":"agent.tool_use","processed_at":"2026-01-01T00:00:01Z","data":{"tool_use":{"id":%q,"name":%q,"input":%s},"evaluated_permission":"always_ask"}}`,
		eventID, toolUseID, name, input)
}

func childToolUseAskEventJSON(eventID, threadID, toolUseID, name, input string) string {
	return fmt.Sprintf(`{"id":%q,"type":"agent.tool_use","session_thread_id":%q,"processed_at":"2026-01-01T00:00:01Z","data":{"tool_use":{"id":%q,"name":%q,"input":%s},"evaluated_permission":"always_ask"}}`,
		eventID, threadID, toolUseID, name, input)
}

func toolConfirmationEventJSON(eventID, targetEventID, result string) string {
	return fmt.Sprintf(`{"id":%q,"type":"user.tool_confirmation","processed_at":"2026-01-01T00:00:02Z","data":{"event_id":%q,"result":%q}}`,
		eventID, targetEventID, result)
}

type fakeExecutor struct {
	mu        sync.Mutex
	calls     []tools.Call
	onExecute func(ctx context.Context, call tools.Call) (tools.Result, error)
}

func (f *fakeExecutor) Execute(ctx context.Context, call tools.Call) (tools.Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, call)
	hook := f.onExecute
	f.mu.Unlock()
	if hook != nil {
		return hook(ctx, call)
	}
	return tools.Result{Content: []tools.ContentBlock{{Type: tools.ContentBlockTypeText, Text: "ok"}}}, nil
}

func (f *fakeExecutor) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}
