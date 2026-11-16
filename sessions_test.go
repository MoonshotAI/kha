package kha

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

const (
	eventsUserMessageE1      = `{"id":"e1","type":"user.message","processed_at":"2026-10-22T01:00:00Z","data":{"content":[{"type":"text","text":"one"}]}}`
	eventsUserMessageE2      = `{"id":"e2","type":"user.message","processed_at":"2026-10-22T01:00:01Z","data":{"content":[{"type":"text","text":"two"}]}}`
	eventsUserMessageLive    = `{"id":"e-live","type":"user.message","processed_at":"2026-10-22T01:00:02Z","data":{"content":[{"type":"text","text":"live"}]}}`
	eventsUserMessageNew     = `{"id":"e-new","type":"user.message","processed_at":"2026-10-22T01:00:03Z","data":{"content":[{"type":"text","text":"new"}]}}`
	eventsUserMessageNoID    = `{"type":"user.message","processed_at":"2026-10-22T01:00:04Z","data":{"content":[]}}`
	eventsEchoMessage        = `{"id":"ev-echo","type":"user.message","processed_at":"2026-10-22T01:00:00Z","data":{"content":[{"type":"text","text":"hi"}]}}`
	eventsStatusIdleCanceled = `{"id":"e-oldidle","type":"session.status","processed_at":"2026-10-22T00:59:00Z","data":{"status":"idle","stop_reason":{"type":"canceled"}}}`
	eventsStatusIdleEndTurn  = `{"id":"e-idle","type":"session.status","processed_at":"2026-10-22T01:01:00Z","data":{"status":"idle","stop_reason":{"type":"end_turn"}}}`
	eventsSessionErrorBoom   = `{"id":"e-err","type":"session.error","processed_at":"2026-10-22T01:00:30Z","data":{"error_type":"model_error","message":"boom"}}`
	eventsRepositoryError    = `{"id":"e-repo","type":"session.error","processed_at":"2026-10-22T01:00:31Z","data":{"error_type":"repository_clone_error","message":"clone failed"}}`
)

// eventsFrame formats one SSE frame.
func eventsFrame(id, event, data string) string {
	return "id: " + id + "\nevent: " + event + "\ndata: " + data + "\n\n"
}

func TestRecoveryRetainsHistoryPositionWhenAnchorFails(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		historyTip string
	}{
		{"refused handshake", http.StatusServiceUnavailable, "history-tip"},
		{"closed before first frame", http.StatusOK, "history-tip"},
		{"missing history cursor replays", http.StatusServiceUnavailable, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var anchors, histories int
				server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if r.URL.Path == "/v1/sessions/s1/events" {
						histories++
						_, _ = fmt.Fprintf(w, `{"items":[%s],"stream_cursor":%q}`, eventsUserMessageE1, tc.historyTip)
						return
					}
					cursor := r.URL.Query().Get("cursor")
					if cursor == "stale" {
						w.WriteHeader(http.StatusBadRequest)
						_, _ = io.WriteString(w, `{"error":{"type":"invalid_parameter_error","message":"stream cursor expired","param":"cursor"}}`)
						return
					}
					if cursor == CursorNow {
						anchors++
						if anchors == 1 {
							if tc.status == http.StatusOK {
								w.Header().Set("Content-Type", "text/event-stream")
							} else {
								w.WriteHeader(tc.status)
								_, _ = io.WriteString(w, `{"error":{"type":"server_error","message":"retry later"}}`)
							}
							return
						}
						t.Error("recovery reopened cursor=now after backfill, skipping the gap")
					}
					w.Header().Set("Content-Type", "text/event-stream")
					if cursor == tc.historyTip {
						_, _ = io.WriteString(w, eventsFrame("1", "user.message", eventsUserMessageE1))
						_, _ = io.WriteString(w, eventsFrame("2", "user.message", eventsUserMessageE2))
					}
					_, _ = io.WriteString(w, "event: error\ndata: {\"error\":{\"type\":\"server_error\",\"message\":\"stop\"}}\n\n")
				}))
				client, err := New("test-key", WithHTTPClient(server.Client()), WithMaxRetries(0))
				if err != nil {
					t.Fatal(err)
				}
				var ids []string
				var terminal error
				for event, err := range client.Sessions.Events(t.Context(), "s1", WithCursor("stale")) {
					if err != nil {
						terminal = err
						continue
					}
					ids = append(ids, SessionEventID(event))
				}
				var streamErr *StreamError
				if fmt.Sprint(ids) != "[e1 e2]" || anchors != 1 || histories != 1 || !errors.As(terminal, &streamErr) {
					t.Fatalf("ids=%v anchors=%d histories=%d error=%v", ids, anchors, histories, terminal)
				}
			})
		})
	}
}

// eventsFlush pushes buffered stream bytes to the client.
func eventsFlush(w http.ResponseWriter) {
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// TestStreamEventsTerminalErrorFrame streams one declared event and a
// terminal error frame: the event is delivered and the frame surfaces as an
// *StreamError that ends the iteration.
func TestStreamEventsTerminalErrorFrame(t *testing.T) {
	srv := httptest.NewServer(authWrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/sessions/s1/events/stream" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, eventsFrame("1", "user.message", eventsUserMessageE1))
		_, _ = io.WriteString(w, "event: error\ndata: {\"error\":{\"type\":\"session_unavailable_error\",\"message\":\"session gone\"}}\n\n")
	})))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	sess := &Session{ID: "s1"}
	var ids []string
	var gotErr error
	for ev, err := range c.Sessions.Events(context.Background(), sess.ID) {
		if err != nil {
			gotErr = err
			continue
		}
		ids = append(ids, ev.UserMessageEvent.ID)
	}
	if len(ids) != 1 || ids[0] != "e1" {
		t.Fatalf("ids = %v, want [e1]", ids)
	}
	var ae *StreamError
	if !errors.As(gotErr, &ae) || ae.Details.Type != "session_unavailable_error" {
		t.Fatalf("stream error = %v, want a terminal StreamError", gotErr)
	}
}

// TestStreamEventsDedupe drops frames whose payload id was already
// delivered, always delivers events without a payload id, and — with
// WithResume(false) — ends with io.EOF verbatim on a clean close.
func TestStreamEventsDedupe(t *testing.T) {
	srv := httptest.NewServer(authWrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, eventsFrame("1", "user.message", eventsUserMessageE1))
		_, _ = io.WriteString(w, eventsFrame("2", "user.message", eventsUserMessageE1)) // re-delivery: dropped
		_, _ = io.WriteString(w, eventsFrame("3", "user.message", eventsUserMessageE2))
		_, _ = io.WriteString(w, eventsFrame("4", "user.message", eventsUserMessageNoID))
		_, _ = io.WriteString(w, eventsFrame("5", "user.message", eventsUserMessageNoID)) // unidentifiable: delivered
	})))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	sess := &Session{ID: "s1"}
	var ids []string
	var gotErr error
	for ev, err := range c.Sessions.Events(context.Background(), sess.ID, WithResume(false)) {
		if err != nil {
			gotErr = err
			continue
		}
		ids = append(ids, ev.UserMessageEvent.ID)
	}
	if want := []string{"e1", "e2", "", ""}; fmt.Sprint(ids) != fmt.Sprint(want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	if !errors.Is(gotErr, io.EOF) {
		t.Fatalf("stream end = %v, want io.EOF verbatim with WithResume(false)", gotErr)
	}
}

// TestStreamEventsFlashCloseEscalatesBackoff hammers the stream with a
// server that accepts every handshake and closes immediately: the reconnect
// backoff escalates 1s→2s instead of hammering at a constant 1s. Jitter is
// disabled for this test so the assertion does not depend on wall-clock
// luck; production still jitters each retry by ±25%.
func TestStreamEventsFlashCloseEscalatesBackoff(t *testing.T) {
	originalJitter := jitterWait
	jitterWait = func(d time.Duration) time.Duration { return d }
	t.Cleanup(func() { jitterWait = originalJitter })

	start := time.Now()
	var mu sync.Mutex
	var stamps []time.Duration
	srv := httptest.NewServer(authWrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/sessions/s1/events/stream" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		mu.Lock()
		stamps = append(stamps, time.Since(start))
		n := len(stamps)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if n <= 2 {
			return // accept, then close immediately with an empty body
		}
		_, _ = io.WriteString(w, "event: error\ndata: {\"error\":{\"type\":\"session_unavailable_error\",\"message\":\"stop\"}}\n\n")
	})))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	sess := &Session{ID: "s1"}
	var gotErr error
	for _, err := range c.Sessions.Events(context.Background(), sess.ID) {
		if err != nil {
			gotErr = err
		}
	}
	if _, isAPIErr := errors.AsType[*StreamError](gotErr); !isAPIErr {
		t.Fatalf("stream error = %v, want the terminal error frame", gotErr)
	}
	if len(stamps) != 3 {
		t.Fatalf("hits = %d, want 3 (2 flash closes + terminal)", len(stamps))
	}
	if gap := (stamps[1] - stamps[0]).Seconds(); gap < 0.8 || gap > 1.5 {
		t.Fatalf("gap 1 = %.2fs, want about 1s (stamps %v)", gap, stamps)
	}
	if gap := (stamps[2] - stamps[1]).Seconds(); gap < 1.8 || gap > 2.5 {
		t.Fatalf("gap 2 = %.2fs, want about 2s of escalated backoff (stamps %v)", gap, stamps)
	}
}

// TestStreamEventsExpiredCursorRecovery drives the spec's recovery flow: a
// 400 invalid_parameter_error with param=cursor and an "expired" message
// re-anchors on cursor=now (established before any history request),
// backfills the gap from event history with dedupe, fires onRecovery once,
// and resumes live from the backfill tip's stream cursor.
func TestStreamEventsExpiredCursorRecovery(t *testing.T) {
	pumpHit := make(chan struct{})
	var mu sync.Mutex
	var order []string
	record := func(s string) {
		mu.Lock()
		order = append(order, s)
		mu.Unlock()
	}

	srv := httptest.NewServer(authWrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/sessions/s1/events/stream" {
			switch r.URL.Query().Get("cursor") {
			case "stale-cursor":
				record("stream:stale")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":{"type":"invalid_parameter_error","message":"the stream cursor expired","param":"cursor"}}`)
			case "now":
				record("stream:now")
				close(pumpHit)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, eventsFrame("c0", "control", `{"cursor":"c0","reason":"tail"}`))
				eventsFlush(w)
				_, _ = io.WriteString(w, eventsFrame("8", "user.message", eventsUserMessageLive)) // resume overlap: deduped
				_, _ = io.WriteString(w, eventsFrame("9", "user.message", eventsUserMessageNew))
				eventsFlush(w)
				<-r.Context().Done()
			default:
				t.Errorf("unexpected stream cursor %q", r.URL.Query().Get("cursor"))
				w.WriteHeader(http.StatusBadRequest)
			}
			return
		}
		if r.URL.Path == "/v1/sessions/s1/events" {
			select {
			case <-pumpHit:
			case <-time.After(5 * time.Second):
				t.Error("history requested before the cursor=now subscription was established")
			}
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Query().Get("page_token") == "t2" {
				record("history:2")
				_, _ = io.WriteString(w, `{"items":[`+eventsUserMessageE2+`,`+eventsUserMessageLive+`],"stream_cursor":"c-tip"}`)
				return
			}
			record("history:1")
			_, _ = io.WriteString(w, `{"items":[`+eventsUserMessageE1+`],"next_page_token":"t2","stream_cursor":"c-mid"}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	sess := &Session{ID: "s1"}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var recovered atomic.Int32
	var ids []string
	var gotErr error
	for ev, err := range c.Sessions.Events(ctx, sess.ID,
		WithCursor("stale-cursor"),
		WithOnRecovery(func() { recovered.Add(1) }),
	) {
		if err != nil {
			gotErr = err
			continue
		}
		ids = append(ids, ev.UserMessageEvent.ID)
		if ev.UserMessageEvent.ID == "e-new" {
			if got := recovered.Load(); got != 1 {
				t.Errorf("onRecovery count at live resume = %d, want 1", got)
			}
			cancel()
		}
	}
	if gotErr != nil && !errors.Is(gotErr, context.Canceled) {
		t.Fatalf("unexpected stream error: %v", gotErr)
	}
	if want := []string{"e1", "e2", "e-live", "e-new"}; fmt.Sprint(ids) != fmt.Sprint(want) {
		t.Fatalf("ids = %v, want %v (backfill order, overlap deduped)", ids, want)
	}
	if got := recovered.Load(); got != 1 {
		t.Fatalf("onRecovery calls = %d, want exactly 1", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"stream:stale", "stream:now", "history:1", "history:2"}; fmt.Sprint(order) != fmt.Sprint(want) {
		t.Fatalf("request order = %v, want %v", order, want)
	}
}

// TestAllEventsUnknownTypeTolerated pins the open-vocabulary contract of
// event history: a page carrying an event type this SDK predates still
// decodes — the unknown event arrives in page order with a zero Type and its
// payload kept in Raw, and it is never deduplicated away.
func TestAllEventsUnknownTypeTolerated(t *testing.T) {
	srv := httptest.NewServer(authWrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/sessions/s1/events" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"items":[`+eventsUserMessageE1+`,`+
			`{"id":"e-future","type":"agent.future_thing","processed_at":"2026-10-22T01:00:05Z","data":{"x":1}}`+
			`]}`)
	})))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	sess := &Session{ID: "s1"}
	var got []*SessionEvent
	for ev, err := range c.Sessions.AllEvents(context.Background(), ListSessionEventsRequest{ID: sess.ID}) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, ev)
	}
	if len(got) != 2 {
		t.Fatalf("delivered %d events, want 2 (the unknown type rides along)", len(got))
	}
	if got[0].Type != SessionEventTypeUserMessageEvent || got[0].UserMessageEvent.ID != "e1" {
		t.Fatalf("known event: %+v", got[0])
	}
	if got[1].Type != 0 || len(got[1].Raw) == 0 {
		t.Fatalf("unknown event: Type = %v, Raw = %s, want a zero Type with the payload kept in Raw", got[1].Type, got[1].Raw)
	}
	if id := SessionEventID(got[1]); id != "" {
		t.Fatalf("unknown event id = %q, want empty (never deduplicated)", id)
	}
}

// TestSendMessageAndWaitArmedEcho pins the echo-armed wait: a session.status
// transition from an earlier turn must not end the wait; the turn ends when
// the status returns to idle after the echo of the sent message.
func TestSendMessageAndWaitArmedEcho(t *testing.T) {
	srv := httptest.NewServer(authWrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sessions/s1/events":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"event_id":"ev-echo"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/s1/events/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, eventsFrame("1", "session.status", eventsStatusIdleCanceled))
			_, _ = io.WriteString(w, eventsFrame("2", "user.message", eventsEchoMessage))
			_, _ = io.WriteString(w, eventsFrame("4", "session.status", eventsStatusIdleEndTurn))
			eventsFlush(w)
			<-r.Context().Done()
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	sess := &Session{ID: "s1"}
	res, err := c.Sessions.SendMessageAndWait(context.Background(), sess.ID, "hi", WithTimeout(30*time.Second))
	if err != nil {
		t.Fatalf("SendMessageAndWait: %v", err)
	}
	if res.StopReason != StopReasonEndTurn {
		t.Fatalf("StopReason = %q, want end_turn (the pre-echo idle must not end the wait)", res.StopReason)
	}
	if res.LastEvent == nil || res.LastEvent.SessionStatusEvent == nil || res.LastEvent.SessionStatusEvent.ID != "e-idle" {
		t.Fatalf("LastEvent = %+v, want the e-idle session.status event", res.LastEvent)
	}
}

// TestSendMessageAndWaitSessionError ends the wait with a *SessionError
// carrying the session.error event's type, message, and event id.
func TestSendMessageAndWaitSessionError(t *testing.T) {
	srv := httptest.NewServer(authWrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sessions/s1/events":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"event_id":"ev-echo"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/s1/events/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, eventsFrame("1", "user.message", eventsEchoMessage))
			_, _ = io.WriteString(w, eventsFrame("2", "session.error", eventsSessionErrorBoom))
			eventsFlush(w)
			<-r.Context().Done()
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	sess := &Session{ID: "s1"}
	res, err := c.Sessions.SendMessageAndWait(context.Background(), sess.ID, "hi", WithTimeout(30*time.Second))
	if res != nil {
		t.Fatalf("result = %+v, want nil", res)
	}
	var se *SessionError
	if !errors.As(err, &se) {
		t.Fatalf("error = %v, want a *SessionError", err)
	}
	if se.Type != SessionErrorTypeModelError || se.Message != "boom" || se.EventID != "e-err" {
		t.Fatalf("SessionError = %+v", se)
	}
}

func TestSendMessageAndWaitRepositoryErrorDoesNotEndTurn(t *testing.T) {
	for _, errorBeforeEcho := range []bool{false, true} {
		t.Run(map[bool]string{false: "after echo", true: "before echo"}[errorBeforeEcho], func(t *testing.T) {
			srv := httptest.NewServer(authWrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/v1/sessions/s1/events":
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusAccepted)
					_, _ = io.WriteString(w, `{"event_id":"ev-echo"}`)
				case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/s1/events/stream":
					w.Header().Set("Content-Type", "text/event-stream")
					if errorBeforeEcho {
						_, _ = io.WriteString(w, eventsFrame("1", "session.error", eventsRepositoryError))
						_, _ = io.WriteString(w, eventsFrame("2", "user.message", eventsEchoMessage))
					} else {
						_, _ = io.WriteString(w, eventsFrame("1", "user.message", eventsEchoMessage))
						_, _ = io.WriteString(w, eventsFrame("2", "session.error", eventsRepositoryError))
					}
					_, _ = io.WriteString(w, eventsFrame("3", "session.status", eventsStatusIdleEndTurn))
					eventsFlush(w)
					<-r.Context().Done()
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			})))
			t.Cleanup(srv.Close)

			c := newTestClient(t, srv)
			res, err := c.Sessions.SendMessageAndWait(context.Background(), "s1", "hi", WithTimeout(30*time.Second))
			if err != nil {
				t.Fatalf("SendMessageAndWait: %v", err)
			}
			if res.StopReason != StopReasonEndTurn {
				t.Fatalf("StopReason = %q, want end_turn", res.StopReason)
			}
		})
	}
}

// TestSendMessageAndWaitLiveOnly covers the deployment without an input
// queue: the send response carries no event_id and the wait subscribes
// live-only with cursor=now.
func TestSendMessageAndWaitLiveOnly(t *testing.T) {
	var gotCursor atomic.Value
	srv := httptest.NewServer(authWrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sessions/s1/events":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/s1/events/stream":
			gotCursor.Store(r.URL.Query().Get("cursor"))
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, eventsFrame("c0", "control", `{"cursor":"c0","reason":"tail"}`))
			_, _ = io.WriteString(w, eventsFrame("1", "session.status", eventsStatusIdleEndTurn))
			eventsFlush(w)
			<-r.Context().Done()
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	sess := &Session{ID: "s1"}
	res, err := c.Sessions.SendMessageAndWait(context.Background(), sess.ID, "hi", WithTimeout(30*time.Second))
	if err != nil {
		t.Fatalf("SendMessageAndWait: %v", err)
	}
	if res.StopReason != StopReasonEndTurn {
		t.Fatalf("StopReason = %q, want end_turn", res.StopReason)
	}
	if cursor, _ := gotCursor.Load().(string); cursor != CursorNow {
		t.Fatalf("stream cursor = %q, want %q (live-only tail without an echo id)", cursor, CursorNow)
	}
}

func TestSendMessageAndWaitLiveOnlyExpiredCursor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var streams, histories atomic.Int32
		server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch {
			case r.Method == http.MethodPost && r.URL.Path == "/v1/sessions/s1/events":
				w.WriteHeader(http.StatusAccepted)
				_, _ = io.WriteString(w, `{}`)
			case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/s1/events/stream":
				attempt := streams.Add(1)
				if attempt == 2 {
					if r.URL.Query().Get("cursor") != "" || r.Header.Get("Last-Event-ID") != "c0" {
						t.Errorf("resume cursor=%q Last-Event-ID=%q", r.URL.Query().Get("cursor"), r.Header.Get("Last-Event-ID"))
					}
					w.Header().Set("Msh-Request-Id", "expired-resume")
					w.WriteHeader(http.StatusBadRequest)
					_, _ = io.WriteString(w, `{"error":{"type":"invalid_parameter_error","message":"stream cursor expired","param":"cursor"}}`)
					return
				}
				if r.URL.Query().Get("cursor") != CursorNow {
					t.Errorf("initial cursor=%q, want now", r.URL.Query().Get("cursor"))
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, eventsFrame("c0", "control", `{"cursor":"c0","reason":"tail"}`))
				// End after the anchor, forcing an explicit cursor resume.
			case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/s1/events":
				histories.Add(1)
				_, _ = fmt.Fprintf(w, `{"items":[%s],"stream_cursor":"history-tip"}`, eventsStatusIdleCanceled)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		result, err := newTestClient(t, server).Sessions.SendMessageAndWait(t.Context(), "s1", "hi", WithTimeout(30*time.Second))
		if result != nil {
			t.Fatalf("result=%+v: historical idle must not finish the current turn", result)
		}
		response, ok := errors.AsType[*Error](err)
		if !ok || !isExpiredCursorError(err) || response.Header().Get("Msh-Request-Id") != "expired-resume" {
			t.Fatalf("error=%v, want the original expired-cursor response", err)
		}
		if streams.Load() != 2 || histories.Load() != 0 {
			t.Fatalf("streams=%d histories=%d, want one subscription, one resume, and no backfill", streams.Load(), histories.Load())
		}
	})
}

func TestRecoveryHistoryRejectsTokenCycle(t *testing.T) {
	var tokens []string
	var terminal error
	stream := &eventStream{
		seen: newIDSet(dedupeWindow),
		yield: func(_ *SessionEvent, err error) bool {
			terminal = err
			return true
		},
		src: eventSource{history: func(_ context.Context, token *string) ([]*SessionEvent, *string, *string, error) {
			value := ""
			if token != nil {
				value = *token
			}
			tokens = append(tokens, value)
			if len(tokens) > 3 {
				t.Fatal("history fetched a previously visited token")
			}
			if value == "a" {
				return nil, new("b"), nil, nil
			}
			return nil, new("a"), nil, nil
		}},
	}
	_, outcome := stream.backfill(t.Context())
	if outcome != outTerminal || terminal == nil || strings.Join(tokens, ",") != ",a,b" {
		t.Fatalf("outcome=%v error=%v tokens=%v, want terminal pagination failure after three pages", outcome, terminal, tokens)
	}
}

// Generated event pages retain their items; product iterators skip null elements.
func TestEventPagesRetainNullItemsAndIteratorsSkipThem(t *testing.T) {
	srv := httptest.NewServer(authWrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"items":[`+eventsUserMessageE1+`,null,`+eventsUserMessageE2+`,null],"stream_cursor":"c1"}`)
	})))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	check := func(t *testing.T, items []*SessionEvent) {
		t.Helper()
		if len(items) != 4 || items[0] == nil || items[1] != nil || items[2] == nil || items[3] != nil {
			t.Fatalf("items = %v, want [event nil event nil]", items)
		}
		if got := SessionEventID(items[0]); got != "e1" {
			t.Fatalf("first id = %q, want e1", got)
		}
		if got := SessionEventID(items[2]); got != "e2" {
			t.Fatalf("second id = %q, want e2", got)
		}
	}
	pageResult, err := c.Sessions.ListEvents(context.Background(), ListSessionEventsRequest{ID: "s1"})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	page := pageResult.Body

	check(t, page.Items)

	th := &SessionThread{ID: "t1", SessionID: "s1"}
	threadPageResult, err := c.Sessions.ListThreadEvents(context.Background(), ListThreadEventsRequest{ID: th.SessionID, ThreadID: th.ID})
	if err != nil {
		t.Fatalf("Thread.ListEvents: %v", err)
	}
	threadPage := threadPageResult.Body

	check(t, threadPage.Items)

	sess := &Session{ID: "s1"}
	var ids []string
	for ev, err := range c.Sessions.AllEvents(context.Background(), ListSessionEventsRequest{ID: sess.ID}) {
		if err != nil {
			t.Fatalf("AllEvents: %v", err)
		}
		ids = append(ids, SessionEventID(ev))
	}
	if len(ids) != 2 || ids[0] != "e1" || ids[1] != "e2" {
		t.Fatalf("iterator ids = %v, want [e1 e2]", ids)
	}
	ids = nil
	for ev, err := range c.Sessions.AllThreadEvents(t.Context(), ListThreadEventsRequest{ID: "s1", ThreadID: "t1"}) {
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, SessionEventID(ev))
	}
	if len(ids) != 2 || ids[0] != "e1" || ids[1] != "e2" {
		t.Fatalf("thread iterator ids = %v, want [e1 e2]", ids)
	}
}

// TestSessionEventIDCoversEveryVariant reconciles the handwritten switch in
// SessionEventID with the generated SessionEvent union. Adding an event
// variant to the public contract regenerates the union with a new As* method, a new
// SessionEventType constant and a new union field, but the extraction branch
// in SessionEventID is added by hand — a missed branch silently opts the new
// variant out of duplicate suppression. The test reflects over every As
// method on *SessionEvent and asserts SessionEventID returns a sentinel id
// for that variant.
func TestSessionEventIDCoversEveryVariant(t *testing.T) {
	const sentinel = "sentinel-id"
	eventType := reflect.TypeFor[*SessionEvent]()
	total, covered := 0, 0
	for method := range eventType.Methods() {
		variant, ok := strings.CutPrefix(method.Name, "As")
		if !ok {
			continue
		}
		total++
		t.Run(variant, func(t *testing.T) {
			field, ok := eventType.Elem().FieldByName(variant)
			if !ok {
				t.Fatalf("union field %s backing %s not found", variant, method.Name)
			}
			payload := reflect.New(field.Type.Elem())
			id := payload.Elem().FieldByName("ID")
			if !id.IsValid() || id.Kind() != reflect.String {
				t.Skipf("variant %s payload %s has no string ID field", variant, field.Type)
			}
			id.SetString(sentinel)
			ev := reflect.New(eventType.Elem())
			ev.Elem().FieldByName(variant).Set(payload)
			// Constants are compile-time only, so the discriminator value is
			// discovered through the As method itself: it selects the payload
			// exactly when Type holds this variant's SessionEventType value.
			typField := ev.Elem().FieldByName("Type")
			matched := false
			for c := int64(1); c <= 4096 && !matched; c++ {
				typField.SetInt(c)
				matched = !method.Func.Call([]reflect.Value{ev})[0].IsNil()
			}
			if !matched {
				t.Fatalf("no SessionEventType%s discriminator value selects union field %s", variant, variant)
			}
			if got := SessionEventID(ev.Interface().(*SessionEvent)); got != sentinel {
				t.Fatalf("SessionEventID = %q, want %q: switch misses the %s branch", got, sentinel, variant)
			}
			covered++
		})
	}
	if total == 0 {
		t.Fatal("reflected no As* methods on *SessionEvent; the union shape changed")
	}
	t.Logf("SessionEventID extraction verified for %d/%d variants", covered, total)
}

// TestThreadEventsTerminalErrorFrame streams one declared event and a
// terminal error frame through Threads.Events, verifying that cursor and
// include_deltas reach the server without a preliminary resource lookup.
func TestThreadEventsTerminalErrorFrame(t *testing.T) {
	var gotCursor, gotDeltas string
	srv := httptest.NewServer(authWrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCursor = r.URL.Query().Get("cursor")
		gotDeltas = r.URL.Query().Get("include_deltas")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "id: 7\nevent: user.message\ndata: {\"id\":\"e1\",\"type\":\"user.message\",\"processed_at\":\"2026-10-22T01:00:00Z\",\"data\":{\"content\":[{\"type\":\"text\",\"text\":\"one\"}]}}\n\n")
		_, _ = io.WriteString(w, "event: error\ndata: {\"error\":{\"type\":\"session_unavailable_error\",\"message\":\"thread gone\"}}\n\n")
	})))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	thread := &SessionThread{SessionID: "s1", ID: "thr_1"}
	var events int
	var gotErr error
	for ev, err := range c.Sessions.ThreadEvents(context.Background(), thread.SessionID, thread.ID, WithCursor(CursorNow), WithDeltas(true)) {
		if err != nil {
			gotErr = err
			continue
		}
		if ev == nil {
			t.Error("nil event")
		}
		events++
	}
	if events != 1 {
		t.Fatalf("events = %d, want 1", events)
	}
	if gotCursor != "now" || gotDeltas != "true" {
		t.Fatalf("query = cursor %q include_deltas %q, want now/true", gotCursor, gotDeltas)
	}
	var ae *StreamError
	if !errors.As(gotErr, &ae) || ae.Details.Type != "session_unavailable_error" {
		t.Fatalf("stream error = %v, want the terminal APIError", gotErr)
	}
}
