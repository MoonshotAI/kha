package commands

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	json "encoding/json/v2"
)

// sessions_e2e_test.go drives the CLI's main streaming paths end to end
// against a scripted fake Session API (real httptest server, no synctest):
// `sessions send --wait` (send receipt -> SSE echo -> turn end -> TurnResult
// table), `sessions attach` (SSE frames rendered, terminal error frame
// reported with the received-event count), and `sessions events --watch`
// (history pages print first, then the row-per-event tail resumes from the
// last page's stream_cursor; Ctrl-C detaches with exit 130, a terminal error
// frame exits 1, and a dropped stream resumes via Last-Event-ID).

// sseTestFrame renders one SSE frame, mirroring the root package's fake.
func sseTestFrame(event, id, data string) string {
	return fmt.Sprintf("event: %s\nid: %s\ndata: %s\n\n", event, id, data)
}

func userMessageEventTestJSON(eventID, text string) string {
	return fmt.Sprintf(`{"id":%q,"type":"user.message","processed_at":"2026-01-01T00:00:01Z","created_at":"2026-01-01T00:00:01Z","data":{"content":[{"type":"text","text":%q}]}}`,
		eventID, text)
}

func agentMessageEventTestJSON(eventID, text string) string {
	return fmt.Sprintf(`{"id":%q,"type":"agent.message","processed_at":"2026-01-01T00:00:02Z","created_at":"2026-01-01T00:00:02Z","data":{"content":[{"type":"text","text":%q}]}}`,
		eventID, text)
}

func statusEventTestJSON(eventID, status, stopReason string) string {
	data := fmt.Sprintf(`{"status":%q`, status)
	if stopReason != "" {
		data += fmt.Sprintf(`,"stop_reason":{"type":%q}`, stopReason)
	}
	data += `}`
	return fmt.Sprintf(`{"id":%q,"type":"session.status","processed_at":"2026-01-01T00:00:03Z","created_at":"2026-01-01T00:00:03Z","data":%s}`,
		eventID, data)
}

const sessionTestJSON = `{"id":"sess_1","agent_id":"agent_1","environment_id":"env_1","status":"idle","created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-01T10:00:00Z"}`

// sessionStreamHandler scripts the session GET, an empty events history
// page (what events --watch reads before subscribing), the events POST
// (accepted with an event_id receipt), and one SSE stream carrying frames.
func sessionStreamHandler(frames string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/sess_1":
			_, _ = fmt.Fprint(w, sessionTestJSON)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/sess_1/events":
			_, _ = fmt.Fprint(w, `{"items":[],"stream_cursor":"cur_hist"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sessions/sess_1/events":
			w.WriteHeader(http.StatusAccepted)
			_, _ = fmt.Fprint(w, `{"event_id":"evt_q1"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/sess_1/events/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, frames)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `{"error":{"type":"not_found_error","message":"no such route","request_id":"req_test_1"}}`)
		}
	}
}

// TestSessionsSendWaitE2E: the send receipt's event_id arms the wait on the
// consumption echo; the following idle status with stop_reason end_turn ends
// it, and the TurnResult prints as the STOP REASON table.
func TestSessionsSendWaitE2E(t *testing.T) {
	isolateEnv(t)
	frames := strings.Join([]string{
		sseTestFrame("user.message", "100", userMessageEventTestJSON("evt_q1", "draft the report")),
		sseTestFrame("session.status", "101", statusEventTestJSON("se_run", "running", "")),
		sseTestFrame("agent.message", "102", agentMessageEventTestJSON("e2", "working on it")),
		sseTestFrame("session.status", "103", statusEventTestJSON("se_done", "idle", "end_turn")),
	}, "")
	var requests []string
	handler := sessionStreamHandler(frames)
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		handler(w, r)
	})

	stdout, stderr, err := runCmd(t, "", "sessions", "send", "sess_1", "draft the report", "--wait",
		"--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatalf("send --wait: %v (stderr: %s)", err, stderr)
	}
	for _, want := range []string{"STOP REASON", "end_turn", "se_done"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout)
		}
	}
	if !strings.Contains(stderr, "turn ended") {
		t.Errorf("stderr missing the completion note:\n%s", stderr)
	}
	if got := strings.Join(requests, ", "); got != "POST /v1/sessions/sess_1/events, GET /v1/sessions/sess_1/events/stream" {
		t.Errorf("requests = %s; want POST /v1/sessions/sess_1/events, GET /v1/sessions/sess_1/events/stream", got)
	}
}

// TestSessionsAttachE2E: attach renders the streamed agent message, then the
// terminal error frame stops the stream; the failure note reports how many
// events were received.
func TestSessionsAttachE2E(t *testing.T) {
	isolateEnv(t)
	frames := strings.Join([]string{
		sseTestFrame("agent.message", "100", agentMessageEventTestJSON("e1", "The report is ready.")),
		sseTestFrame("session.status", "101", statusEventTestJSON("se_run", "running", "")),
		sseTestFrame("error", "", `{"error":{"type":"server_error","message":"stream exploded","request_id":"req_test_1"}}`),
	}, "")
	var requests []string
	handler := sessionStreamHandler(frames)
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		handler(w, r)
	})

	stdout, stderr, err := runCmd(t, "", "sessions", "attach", "sess_1",
		"--base-url", srv.URL, "--api-key", "sk-test")
	if err == nil {
		t.Fatal("attach must fail on a terminal error frame")
	}
	if !strings.Contains(err.Error(), "stream exploded") {
		t.Errorf("error = %v, want the APIError from the error frame", err)
	}
	if !strings.Contains(stdout, "The report is ready.") {
		t.Errorf("stdout missing the rendered agent message:\n%s", stdout)
	}
	if !strings.Contains(stderr, "stream failed after 2 event(s)") {
		t.Errorf("stderr missing the received-event count:\n%s", stderr)
	}
	if got := strings.Join(requests, ", "); got != "GET /v1/sessions/sess_1/events/stream" {
		t.Errorf("requests = %s; want only the events stream GET", got)
	}
}

// watchTestFrames is the three-event SSE stream the events --watch tests
// replay: a user message, an agent message, and a terminal idle status.
var watchTestFrames = strings.Join([]string{
	sseTestFrame("user.message", "100", userMessageEventTestJSON("evt_w1", "draft the report")),
	sseTestFrame("agent.message", "101", agentMessageEventTestJSON("evt_w2", "working on it")),
	sseTestFrame("session.status", "102", statusEventTestJSON("evt_w3", "idle", "end_turn")),
}, "")

// cancelOnMarker wraps stdout and cancels the command's context once the
// marker text has printed — a deterministic Ctrl-C stand-in for watch
// tests: with the SDK's resume the stream reconnects after EOF and never
// ends on its own, so the test interrupts once the last expected event is
// out. Canceling inside Write guarantees the marker event is already on
// stdout, which keeps the received-event count deterministic.
type cancelOnMarker struct {
	buf    *bytes.Buffer
	marker string
	cancel context.CancelFunc
}

func (w *cancelOnMarker) Write(p []byte) (int, error) {
	n, err := w.buf.Write(p)
	if strings.Contains(w.buf.String(), w.marker) {
		w.cancel()
	}
	return n, err
}

// runCmdCtx is runCmd with an explicit context and stdout writer.
func runCmdCtx(t *testing.T, ctx context.Context, stdout io.Writer, args ...string) (stderr string, err error) {
	t.Helper()
	root := NewRootCommand()
	errBuf := new(bytes.Buffer)
	root.SetOut(stdout)
	root.SetErr(errBuf)
	root.SetIn(strings.NewReader(""))
	root.SetArgs(args)
	err = root.ExecuteContext(ctx)
	return errBuf.String(), err
}

// TestSessionsEventsWatchInterruptE2E: Ctrl-C on --watch detaches like
// attach — stderr reports the received-event count and the command exits
// 130. The stream carries the SDK's resume semantics, so the test cancels
// once the second (and last) event has printed.
func TestSessionsEventsWatchInterruptE2E(t *testing.T) {
	isolateEnv(t)
	frames := strings.Join([]string{
		sseTestFrame("user.message", "100", userMessageEventTestJSON("evt_w1", "draft the report")),
		sseTestFrame("agent.message", "101", agentMessageEventTestJSON("evt_w2", "working on it")),
	}, "")
	var requests []string
	handler := sessionStreamHandler(frames)
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		handler(w, r)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &cancelOnMarker{buf: new(bytes.Buffer), marker: "evt_w2", cancel: cancel}

	stderr, err := runCmdCtx(t, ctx, out, "sessions", "events", "sess_1", "--watch",
		"--base-url", srv.URL, "--api-key", "sk-test")
	if !errors.Is(err, errInterrupted) {
		t.Fatalf("events --watch: err = %v, want errInterrupted (stderr: %s)", err, stderr)
	}
	if code := exitCode(err); code != exitInterrupted {
		t.Errorf("exitCode = %d, want %d", code, exitInterrupted)
	}
	stdout := out.buf.String()
	if n := strings.Count(stdout, "PROCESSED_AT"); n != 1 {
		t.Errorf("table header printed %d times, want once:\n%s", n, stdout)
	}
	for _, want := range []string{"evt_w1", "user.message", "draft the report", "evt_w2", "agent.message"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout)
		}
	}
	if !strings.Contains(stderr, "interrupted: 2 event(s) received") {
		t.Errorf("stderr missing the received-event count:\n%s", stderr)
	}
	if got := strings.Join(requests, ", "); got != "GET /v1/sessions/sess_1/events, GET /v1/sessions/sess_1/events/stream" {
		t.Errorf("requests = %s; want GET /v1/sessions/sess_1/events, GET /v1/sessions/sess_1/events/stream", got)
	}
}

// TestSessionsEventsWatchJSONE2E: --watch -o json emits NDJSON — every line
// is one compact event object that unmarshals on its own, with no table
// header.
func TestSessionsEventsWatchJSONE2E(t *testing.T) {
	isolateEnv(t)
	srv := serve(t, sessionStreamHandler(watchTestFrames))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &cancelOnMarker{buf: new(bytes.Buffer), marker: "evt_w3", cancel: cancel}

	stderr, err := runCmdCtx(t, ctx, out, "sessions", "events", "sess_1", "--watch", "-o", "json",
		"--base-url", srv.URL, "--api-key", "sk-test")
	if !errors.Is(err, errInterrupted) {
		t.Fatalf("events --watch -o json: err = %v, want errInterrupted (stderr: %s)", err, stderr)
	}
	stdout := out.buf.String()
	if strings.Contains(stdout, "PROCESSED_AT") {
		t.Errorf("json output must not carry the table header:\n%s", stdout)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 3 {
		t.Fatalf("stdout has %d lines, want 3 (one per event):\n%s", len(lines), stdout)
	}
	wantIDs := []string{"evt_w1", "evt_w2", "evt_w3"}
	wantTypes := []string{"user.message", "agent.message", "session.status"}
	for i, line := range lines {
		var env struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(line), &env); err != nil {
			t.Fatalf("line %d is not one compact JSON object: %v\n%s", i, err, line)
		}
		if env.ID != wantIDs[i] || env.Type != wantTypes[i] {
			t.Errorf("line %d = {id: %q, type: %q}, want {%q, %q}", i, env.ID, env.Type, wantIDs[i], wantTypes[i])
		}
	}
}

// TestSessionsEventsWatchYAMLE2E: --watch -o yaml emits one YAML document per
// event, each introduced by a `---` separator line.
func TestSessionsEventsWatchYAMLE2E(t *testing.T) {
	isolateEnv(t)
	srv := serve(t, sessionStreamHandler(watchTestFrames))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &cancelOnMarker{buf: new(bytes.Buffer), marker: "evt_w3", cancel: cancel}

	stderr, err := runCmdCtx(t, ctx, out, "sessions", "events", "sess_1", "--watch", "-o", "yaml",
		"--base-url", srv.URL, "--api-key", "sk-test")
	if !errors.Is(err, errInterrupted) {
		t.Fatalf("events --watch -o yaml: err = %v, want errInterrupted (stderr: %s)", err, stderr)
	}
	stdout := out.buf.String()
	if n := strings.Count(stdout, "---\n"); n != 3 {
		t.Errorf("yaml stream has %d `---` separators, want 3 (one per event):\n%s", n, stdout)
	}
	for _, want := range []string{"evt_w1", "evt_w2", "evt_w3"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout)
		}
	}
}

// TestSessionsEventsWatchRejectsOrderDesc: --watch tails oldest-first, so a
// newest-first history cannot flow into it — --order desc is a usage error
// (exit 2), while the paging flags now steer the watch's history phase.
func TestSessionsEventsWatchRejectsOrderDesc(t *testing.T) {
	isolateEnv(t)
	_, _, err := runCmd(t, "", "sessions", "events", "sess_1", "--watch", "--order", "desc")
	if err == nil {
		t.Fatal("expected an error")
	}
	if code := exitCode(err); code != exitUsage {
		t.Errorf("exitCode = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(err.Error(), "--watch cannot be combined") {
		t.Errorf("err = %v", err)
	}
}

// TestSessionsEventsSinceRequiresWatch: --since on its own (no --watch) is a
// usage error (exit 2), not silently ignored.
func TestSessionsEventsSinceRequiresWatch(t *testing.T) {
	isolateEnv(t)
	_, _, err := runCmd(t, "", "sessions", "events", "sess_1", "--since", "evt_x")
	if err == nil {
		t.Fatal("expected an error")
	}
	if code := exitCode(err); code != exitUsage {
		t.Errorf("exitCode = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(err.Error(), "--since only applies with --watch") {
		t.Errorf("err = %v", err)
	}
}

// TestSessionsEventsWatchSinceCursor: --watch --since skips the history
// phase entirely — no history GET reaches the server, and the first stream
// request goes out with cursor=evt_x.
func TestSessionsEventsWatchSinceCursor(t *testing.T) {
	isolateEnv(t)
	var cursors []string
	var historyGets int
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/sess_1":
			_, _ = fmt.Fprint(w, sessionTestJSON)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/sess_1/events":
			historyGets++
			_, _ = fmt.Fprint(w, `{"items":[],"stream_cursor":"cur_hist"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/sess_1/events/stream":
			cursors = append(cursors, r.URL.Query().Get("cursor"))
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, sseTestFrame("agent.message", "100", agentMessageEventTestJSON("evt_w1", "hi")))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `{"error":{"type":"not_found_error","message":"no such route","request_id":"req_test_1"}}`)
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &cancelOnMarker{buf: new(bytes.Buffer), marker: "evt_w1", cancel: cancel}

	stderr, err := runCmdCtx(t, ctx, out, "sessions", "events", "sess_1", "--watch", "--since", "evt_x",
		"--base-url", srv.URL, "--api-key", "sk-test")
	if !errors.Is(err, errInterrupted) {
		t.Fatalf("events --watch --since: err = %v, want errInterrupted (stderr: %s)", err, stderr)
	}
	if historyGets != 0 {
		t.Errorf("--since must skip the history phase, server saw %d history GET(s)", historyGets)
	}
	if len(cursors) == 0 || cursors[0] != "evt_x" {
		t.Errorf("first stream request cursor = %v, want %q", cursors, "evt_x")
	}
}

// TestSessionsEventsWatchShortFlag: -w is the short form of --watch.
func TestSessionsEventsWatchShortFlag(t *testing.T) {
	isolateEnv(t)
	srv := serve(t, sessionStreamHandler(watchTestFrames))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &cancelOnMarker{buf: new(bytes.Buffer), marker: "evt_w3", cancel: cancel}

	stderr, err := runCmdCtx(t, ctx, out, "sessions", "events", "sess_1", "-w",
		"--base-url", srv.URL, "--api-key", "sk-test")
	if !errors.Is(err, errInterrupted) {
		t.Fatalf("events -w: err = %v, want errInterrupted (stderr: %s)", err, stderr)
	}
	if !strings.Contains(out.buf.String(), "evt_w1") {
		t.Errorf("stdout missing the streamed event:\n%s", out.buf.String())
	}
}

// TestSessionsEventsWatchListsHistoryFirstE2E: without --since, --watch
// first pages the history with the plain list defaults (no order/page_size
// query), prints its rows in the watch format, then subscribes from the
// page's stream_cursor — the history rows precede the live rows under one
// table header, and the received-event count spans both phases.
func TestSessionsEventsWatchListsHistoryFirstE2E(t *testing.T) {
	isolateEnv(t)
	var requests []string
	var historyQuery string
	var cursors []string
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/sess_1":
			_, _ = fmt.Fprint(w, sessionTestJSON)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/sess_1/events":
			requests = append(requests, "history")
			historyQuery = r.URL.RawQuery
			_, _ = fmt.Fprintf(w, `{"items":[%s],"stream_cursor":"cur_hist"}`,
				userMessageEventTestJSON("sevt_h1", "draft the report"))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/sess_1/events/stream":
			requests = append(requests, "stream")
			cursors = append(cursors, r.URL.Query().Get("cursor"))
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, strings.Join([]string{
				sseTestFrame("agent.message", "100", agentMessageEventTestJSON("evt_w1", "working on it")),
				sseTestFrame("session.status", "101", statusEventTestJSON("evt_w2", "idle", "end_turn")),
			}, ""))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `{"error":{"type":"not_found_error","message":"no such route","request_id":"req_test_1"}}`)
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &cancelOnMarker{buf: new(bytes.Buffer), marker: "evt_w2", cancel: cancel}

	stderr, err := runCmdCtx(t, ctx, out, "sessions", "events", "sess_1", "--watch",
		"--base-url", srv.URL, "--api-key", "sk-test")
	if !errors.Is(err, errInterrupted) {
		t.Fatalf("events --watch: err = %v, want errInterrupted (stderr: %s)", err, stderr)
	}
	if len(requests) < 2 || requests[0] != "history" || requests[1] != "stream" {
		t.Errorf("request order = %v, want the history GET before the stream", requests)
	}
	if historyQuery != "" {
		t.Errorf("history GET query = %q, want the plain list defaults (no order/page_size)", historyQuery)
	}
	if len(cursors) == 0 || cursors[0] != "cur_hist" {
		t.Errorf("first stream request cursor = %v, want the history page's stream_cursor %q", cursors, "cur_hist")
	}
	stdout := out.buf.String()
	if n := strings.Count(stdout, "PROCESSED_AT"); n != 1 {
		t.Errorf("table header printed %d times, want once across both phases:\n%s", n, stdout)
	}
	hist, live := strings.Index(stdout, "sevt_h1"), strings.Index(stdout, "evt_w1")
	if hist < 0 || live < 0 || hist > live {
		t.Errorf("the history row must precede the live rows (sevt_h1 at %d, evt_w1 at %d):\n%s", hist, live, stdout)
	}
	if !strings.Contains(stderr, "interrupted: 3 event(s) received") {
		t.Errorf("stderr missing the received-event count (1 history + 2 live):\n%s", stderr)
	}
}

// TestSessionsEventsWatchAllE2E: --watch --all drains every history page by
// hand (following next_page_token, so the last page's stream_cursor is on
// hand for the tail) before subscribing — page one's rows print before page
// two is fetched, and the live rows continue under the same header.
func TestSessionsEventsWatchAllE2E(t *testing.T) {
	isolateEnv(t)
	var pageTokens []string
	var cursors []string
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/sess_1":
			_, _ = fmt.Fprint(w, sessionTestJSON)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/sess_1/events":
			token := r.URL.Query().Get("page_token")
			pageTokens = append(pageTokens, token)
			if token == "tok2" {
				// Page 2 carries the stream_cursor; page 1 deliberately does
				// not, so only the LAST page's cursor can anchor the tail.
				_, _ = fmt.Fprintf(w, `{"items":[%s],"stream_cursor":"cur_2"}`,
					statusEventTestJSON("sevt_h2", "idle", "end_turn"))
				return
			}
			_, _ = fmt.Fprintf(w, `{"items":[%s],"next_page_token":"tok2"}`,
				userMessageEventTestJSON("sevt_h1", "draft the report"))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/sess_1/events/stream":
			cursors = append(cursors, r.URL.Query().Get("cursor"))
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, sseTestFrame("agent.message", "100", agentMessageEventTestJSON("evt_w1", "working on it")))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `{"error":{"type":"not_found_error","message":"no such route","request_id":"req_test_1"}}`)
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &cancelOnMarker{buf: new(bytes.Buffer), marker: "evt_w1", cancel: cancel}

	stderr, err := runCmdCtx(t, ctx, out, "sessions", "events", "sess_1", "--watch", "--all",
		"--base-url", srv.URL, "--api-key", "sk-test")
	if !errors.Is(err, errInterrupted) {
		t.Fatalf("events --watch --all: err = %v, want errInterrupted (stderr: %s)", err, stderr)
	}
	if len(pageTokens) != 2 || pageTokens[0] != "" || pageTokens[1] != "tok2" {
		t.Errorf("history page tokens = %v, want [%q %q] (drained before subscribing)", pageTokens, "", "tok2")
	}
	if len(cursors) == 0 || cursors[0] != "cur_2" {
		t.Errorf("first stream request cursor = %v, want the last page's stream_cursor %q", cursors, "cur_2")
	}
	stdout := out.buf.String()
	if n := strings.Count(stdout, "PROCESSED_AT"); n != 1 {
		t.Errorf("table header printed %d times, want once across both phases:\n%s", n, stdout)
	}
	h1, h2, live := strings.Index(stdout, "sevt_h1"), strings.Index(stdout, "sevt_h2"), strings.Index(stdout, "evt_w1")
	if h1 < 0 || h2 < 0 || live < 0 || h1 > h2 || h2 > live {
		t.Errorf("rows out of order (sevt_h1 at %d, sevt_h2 at %d, evt_w1 at %d):\n%s", h1, h2, live, stdout)
	}
	if !strings.Contains(stderr, "interrupted: 3 event(s) received") {
		t.Errorf("stderr missing the received-event count (2 history + 1 live):\n%s", stderr)
	}
}

// TestSessionsEventsWatchDedupesHistoryTailE2E: the stream may re-deliver a
// few events from the resumed page's tail — the watch drops those repeats
// by event id (sevt_e3 prints once and is not counted), while an event with
// an empty id is never deduplicated (the SDK's rule) and prints from both
// phases.
func TestSessionsEventsWatchDedupesHistoryTailE2E(t *testing.T) {
	isolateEnv(t)
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/sess_1":
			_, _ = fmt.Fprint(w, sessionTestJSON)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/sess_1/events":
			_, _ = fmt.Fprintf(w, `{"items":[%s,%s,%s,%s],"stream_cursor":"cur_seam"}`,
				userMessageEventTestJSON("sevt_e1", "first"),
				agentMessageEventTestJSON("sevt_e2", "second"),
				agentMessageEventTestJSON("sevt_e3", "third"),
				agentMessageEventTestJSON("", "no id"))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/sess_1/events/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, strings.Join([]string{
				// The tail's re-delivery of sevt_e3 (a duplicate), the
				// empty-id event again (never deduplicated), then a new one.
				sseTestFrame("agent.message", "100", agentMessageEventTestJSON("sevt_e3", "third")),
				sseTestFrame("agent.message", "101", agentMessageEventTestJSON("", "no id")),
				sseTestFrame("agent.message", "102", agentMessageEventTestJSON("evt_e4", "fourth")),
			}, ""))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `{"error":{"type":"not_found_error","message":"no such route","request_id":"req_test_1"}}`)
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &cancelOnMarker{buf: new(bytes.Buffer), marker: "evt_e4", cancel: cancel}

	stderr, err := runCmdCtx(t, ctx, out, "sessions", "events", "sess_1", "--watch",
		"--base-url", srv.URL, "--api-key", "sk-test")
	if !errors.Is(err, errInterrupted) {
		t.Fatalf("events --watch: err = %v, want errInterrupted (stderr: %s)", err, stderr)
	}
	stdout := out.buf.String()
	if n := strings.Count(stdout, "sevt_e3"); n != 1 {
		t.Errorf("sevt_e3 printed %d times, want once (the stream's re-delivery is a duplicate):\n%s", n, stdout)
	}
	if n := strings.Count(stdout, "no id"); n != 2 {
		t.Errorf("the empty-id event printed %d times, want 2 (empty ids are never deduplicated):\n%s", n, stdout)
	}
	for _, want := range []string{"sevt_e1", "sevt_e2", "evt_e4"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout)
		}
	}
	if !strings.Contains(stderr, "interrupted: 6 event(s) received") {
		t.Errorf("stderr missing the received-event count (4 history + 2 live, the duplicate not counted):\n%s", stderr)
	}
}

// TestSessionsEventsWatchHistoryFlagsE2E: --order asc, --limit, and
// --page-token combine with --watch — they steer the history phase (order,
// page size, start token) before the tail subscribes from the page's
// stream_cursor.
func TestSessionsEventsWatchHistoryFlagsE2E(t *testing.T) {
	isolateEnv(t)
	var historyOrder, historySize, historyToken string
	var cursors []string
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/sess_1":
			_, _ = fmt.Fprint(w, sessionTestJSON)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/sess_1/events":
			historyOrder = r.URL.Query().Get("order")
			historySize = r.URL.Query().Get("page_size")
			historyToken = r.URL.Query().Get("page_token")
			_, _ = fmt.Fprint(w, `{"items":[],"stream_cursor":"cur_tok"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/sess_1/events/stream":
			cursors = append(cursors, r.URL.Query().Get("cursor"))
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, sseTestFrame("agent.message", "100", agentMessageEventTestJSON("evt_w1", "working on it")))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `{"error":{"type":"not_found_error","message":"no such route","request_id":"req_test_1"}}`)
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &cancelOnMarker{buf: new(bytes.Buffer), marker: "evt_w1", cancel: cancel}

	stderr, err := runCmdCtx(t, ctx, out, "sessions", "events", "sess_1", "--watch",
		"--order", "asc", "--limit", "5", "--page-token", "tok0",
		"--base-url", srv.URL, "--api-key", "sk-test")
	if !errors.Is(err, errInterrupted) {
		t.Fatalf("events --watch --order asc --limit 5 --page-token tok0: err = %v, want errInterrupted (stderr: %s)", err, stderr)
	}
	if historyOrder != "asc" || historySize != "5" || historyToken != "tok0" {
		t.Errorf("history GET saw order=%q page_size=%q page_token=%q, want asc/5/tok0", historyOrder, historySize, historyToken)
	}
	if len(cursors) == 0 || cursors[0] != "cur_tok" {
		t.Errorf("first stream request cursor = %v, want %q", cursors, "cur_tok")
	}
}

// TestSessionsEventsWatchStreamErrorE2E: a terminal error frame is
// non-retryable — the watch reports the received-event count and exits 1.
func TestSessionsEventsWatchStreamErrorE2E(t *testing.T) {
	isolateEnv(t)
	frames := strings.Join([]string{
		sseTestFrame("agent.message", "100", agentMessageEventTestJSON("evt_w1", "partial")),
		sseTestFrame("error", "", `{"error":{"type":"server_error","message":"stream exploded","request_id":"req_test_1"}}`),
	}, "")
	srv := serve(t, sessionStreamHandler(frames))

	stdout, stderr, err := runCmd(t, "", "sessions", "events", "sess_1", "--watch",
		"--base-url", srv.URL, "--api-key", "sk-test")
	if err == nil {
		t.Fatal("events --watch must fail on a terminal error frame")
	}
	if code := exitCode(err); code != exitError {
		t.Errorf("exitCode = %d, want %d", code, exitError)
	}
	if !strings.Contains(err.Error(), "stream exploded") {
		t.Errorf("error = %v, want the APIError from the error frame", err)
	}
	if !strings.Contains(stdout, "evt_w1") {
		t.Errorf("stdout missing the event received before the failure:\n%s", stdout)
	}
	if !strings.Contains(stderr, "stream failed after 1 event(s)") {
		t.Errorf("stderr missing the received-event count:\n%s", stderr)
	}
}

// TestSessionsEventsWatchReconnectE2E: when the SSE stream drops mid-tail,
// the SDK resumes precisely with Last-Event-ID (the event machine prefers the
// server's own resume header over a history re-sync once a frame id was
// observed), and the watch keeps receiving the following events. The fake
// server ends the first connection after one event; the second connection
// must carry Last-Event-ID and deliver the next event.
func TestSessionsEventsWatchReconnectE2E(t *testing.T) {
	isolateEnv(t)
	var cursors, lastIDs []string
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/sess_1":
			_, _ = fmt.Fprint(w, sessionTestJSON)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/sess_1/events":
			_, _ = fmt.Fprint(w, `{"items":[],"stream_cursor":"cur_hist"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/sess_1/events/stream":
			cursors = append(cursors, r.URL.Query().Get("cursor"))
			lastIDs = append(lastIDs, r.Header.Get("Last-Event-ID"))
			w.Header().Set("Content-Type", "text/event-stream")
			if len(cursors) == 1 {
				// First connection: one event, then the stream drops.
				_, _ = fmt.Fprint(w, sseTestFrame("user.message", "100", userMessageEventTestJSON("evt_w1", "draft the report")))
				return
			}
			_, _ = fmt.Fprint(w, sseTestFrame("agent.message", "101", agentMessageEventTestJSON("evt_w2", "working on it")))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `{"error":{"type":"not_found_error","message":"no such route","request_id":"req_test_1"}}`)
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &cancelOnMarker{buf: new(bytes.Buffer), marker: "evt_w2", cancel: cancel}

	stderr, err := runCmdCtx(t, ctx, out, "sessions", "events", "sess_1", "--watch",
		"--base-url", srv.URL, "--api-key", "sk-test")
	if !errors.Is(err, errInterrupted) {
		t.Fatalf("events --watch: err = %v, want errInterrupted (stderr: %s)", err, stderr)
	}
	if len(cursors) != 2 || cursors[0] != "cur_hist" || cursors[1] != "" {
		t.Errorf("stream cursors = %v, want [cur_hist \"\"] (initial from the history tip, then a precise Last-Event-ID resume)", cursors)
	}
	if len(lastIDs) != 2 || lastIDs[0] != "" || lastIDs[1] != "100" {
		t.Errorf("Last-Event-ID headers = %v, want [\"\" \"100\"] (resume carries the last observed frame id)", lastIDs)
	}
	stdout := out.buf.String()
	for _, want := range []string{"evt_w1", "evt_w2"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout)
		}
	}
	if !strings.Contains(stderr, "interrupted: 2 event(s) received") {
		t.Errorf("stderr missing the received-event count:\n%s", stderr)
	}
}

// TestSessionsAttachInputE2E: --input sends each stdin line as a
// user.message, and after stdin closes attach detaches itself once the last
// message's echo and the following idle status arrive — the user does not
// have to Ctrl-C a pipeline.
func TestSessionsAttachInputE2E(t *testing.T) {
	isolateEnv(t)
	frames := strings.Join([]string{
		sseTestFrame("user.message", "100", userMessageEventTestJSON("evt_q1", "first line")),
		sseTestFrame("session.status", "101", statusEventTestJSON("se_run", "running", "")),
		sseTestFrame("user.message", "102", userMessageEventTestJSON("evt_q2", "second line")),
		sseTestFrame("session.status", "103", statusEventTestJSON("se_done", "idle", "end_turn")),
	}, "")
	var posts []string
	handler := sessionStreamHandler(frames)
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/sessions/sess_1/events" {
			body, _ := io.ReadAll(r.Body)
			posts = append(posts, string(body))
			// Two receipts: one per line.
			if len(posts) == 1 {
				w.WriteHeader(http.StatusAccepted)
				_, _ = fmt.Fprint(w, `{"event_id":"evt_q1"}`)
				return
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = fmt.Fprint(w, `{"event_id":"evt_q2"}`)
			return
		}
		handler(w, r)
	})

	_, stderr, err := runCmd(t, "first line\n\nsecond line\n",
		"sessions", "attach", "sess_1", "--input",
		"--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatalf("attach --input: %v (stderr: %s)", err, stderr)
	}
	if len(posts) != 2 {
		t.Fatalf("posts = %d; want 2 (one per nonempty line)", len(posts))
	}
	if !strings.Contains(posts[0], "first line") || !strings.Contains(posts[1], "second line") {
		t.Fatalf("posts = %v", posts)
	}
	if !strings.Contains(stderr, "stream ended") {
		t.Fatalf("attach did not end on its own:\n%s", stderr)
	}
}

// TestSessionsAttachInputSendFailure: a rejected stdin message (archived
// Session, 409) ends attach with the failure instead of hanging on the
// stream.
func TestSessionsAttachInputSendFailure(t *testing.T) {
	isolateEnv(t)
	frames := sseTestFrame("session.status", "100", statusEventTestJSON("se_1", "idle", ""))
	handler := sessionStreamHandler(frames)
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/sessions/sess_1/events" {
			w.WriteHeader(http.StatusConflict)
			_, _ = fmt.Fprint(w, `{"error":{"type":"conflict_error","message":"session is archived","request_id":"req_1"}}`)
			return
		}
		handler(w, r)
	})
	_, stderr, err := runCmd(t, "hello\n",
		"sessions", "attach", "sess_1", "--input",
		"--base-url", srv.URL, "--api-key", "sk-test")
	if err == nil {
		t.Fatalf("attach --input on archived session should fail (stderr: %s)", stderr)
	}
}
