package kha

import (
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
)

const (
	runnerCustomAnswered = `{"id":"c1","type":"agent.custom_tool_use","processed_at":"2026-10-22T01:00:00Z","data":{"name":"weather","input":{"city":"old"}}}`
	runnerResultAnswered = `{"id":"r1","type":"user.custom_tool_result","processed_at":"2026-10-22T01:00:01Z","data":{"event_id":"c1","content":[]}}`
	runnerCustomPending  = `{"id":"c2","type":"agent.custom_tool_use","processed_at":"2026-10-22T01:00:02Z","data":{"name":"weather","input":{"city":"Beijing"}}}`
	runnerToolUseAsk     = `{"id":"t1","type":"agent.tool_use","processed_at":"2026-10-22T01:00:03Z","data":{"tool_use":{"id":"tu1","name":"bash","input":{"command":"rm -rf build"}},"evaluated_permission":"always_ask"}}`
	runnerRequiresAction = `{"id":"st1","type":"session.status","processed_at":"2026-10-22T01:00:04Z","data":{"status":"idle","stop_reason":{"type":"requires_action","event_ids":["c2","t1"]}}}`
	runnerCustomUnknown  = `{"id":"c3","type":"agent.custom_tool_use","processed_at":"2026-10-22T01:00:05Z","data":{"name":"missing","input":{}}}`
)

type runnerAnswer struct {
	key   string
	input map[string]any
}

func TestRunToolsAnswersPendingAndLiveCalls(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		var mu sync.Mutex
		var answers []runnerAnswer
		var streamCursors []string
		server := httptest.NewTestServer(t, authWrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/s1/events":
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"items":[`+strings.Join([]string{runnerCustomAnswered, runnerResultAnswered, runnerCustomPending, runnerToolUseAsk, runnerRequiresAction}, ",")+`],"stream_cursor":"tip-1"}`)
			case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/s1/events/stream":
				mu.Lock()
				streamCursors = append(streamCursors, r.URL.Query().Get("cursor"))
				mu.Unlock()
				w.Header().Set("Content-Type", "text/event-stream")
				// The duplicate c2 must not run the handler a second time.
				_, _ = io.WriteString(w, eventsFrame("5", "agent.custom_tool_use", runnerCustomPending))
				_, _ = io.WriteString(w, eventsFrame("6", "agent.custom_tool_use", runnerCustomUnknown))
				eventsFlush(w)
				<-r.Context().Done()
			case r.Method == http.MethodPost && r.URL.Path == "/v1/sessions/s1/events":
				var body struct {
					Events []map[string]any `json:"events"`
				}
				data, _ := io.ReadAll(r.Body)
				if err := json.Unmarshal(data, &body); err != nil || len(body.Events) != 1 {
					t.Errorf("answer body = %s, err = %v", data, err)
				}
				mu.Lock()
				answers = append(answers, runnerAnswer{key: r.Header.Get("Idempotency-Key"), input: body.Events[0]})
				if len(answers) == 3 {
					cancel()
				}
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusAccepted)
				_, _ = io.WriteString(w, `{}`)
			default:
				t.Errorf("unexpected %s %s", r.Method, r.URL)
				w.WriteHeader(http.StatusNotFound)
			}
		})))
		client := newTestClient(t, server)

		var weatherCalls []string
		err := client.Sessions.RunTools(ctx, "s1",
			WithCustomTool("weather", func(_ context.Context, call *AgentCustomToolUseEvent) ([]*ToolResultContent, error) {
				weatherCalls = append(weatherCalls, call.ID)
				return []*ToolResultContent{NewToolResultContentTextContentBlock(&TextContentBlock{Type: TextContentBlockTypeText, Text: "sunny"})}, nil
			}),
			WithToolConfirmation(func(_ context.Context, call *AgentToolUseEvent) (ToolConfirmation, error) {
				if call.Data.ToolUse.Name != "bash" {
					t.Errorf("confirmation call = %+v", call)
				}
				return ToolConfirmation{DenyMessage: "destructive command"}, nil
			}),
		)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("RunTools error = %v; want context.Canceled", err)
		}
		if len(weatherCalls) != 1 || weatherCalls[0] != "c2" {
			t.Fatalf("weather handler calls = %v; want only the unanswered c2", weatherCalls)
		}
		if len(streamCursors) == 0 || streamCursors[0] != "tip-1" {
			t.Fatalf("stream cursors = %v; want the history tip first", streamCursors)
		}
		want := []struct {
			key, typ, eventID string
			isError           bool
		}{
			{"kha-custom-tool-result-c2", "user.custom_tool_result", "c2", false},
			{"kha-tool-confirmation-t1", "user.tool_confirmation", "t1", false},
			{"kha-custom-tool-result-c3", "user.custom_tool_result", "c3", true},
		}
		if len(answers) != len(want) {
			t.Fatalf("answers = %+v", answers)
		}
		for i, w := range want {
			got := answers[i]
			data, _ := got.input["data"].(map[string]any)
			if got.key != w.key || got.input["type"] != w.typ || data["event_id"] != w.eventID {
				t.Fatalf("answer %d = %+v; want %+v", i, got, w)
			}
			if isErr, _ := data["is_error"].(bool); isErr != w.isError {
				t.Fatalf("answer %d is_error = %v; want %v", i, data["is_error"], w.isError)
			}
		}
		if data := answers[1].input["data"].(map[string]any); data["result"] != "deny" || data["deny_message"] != "destructive command" {
			t.Fatalf("confirmation = %v", data)
		}
	})
}

func TestRunToolsRequiresSessionID(t *testing.T) {
	client, err := New("key", WithBaseURL("http://127.0.0.1:1"))
	if err != nil {
		t.Fatal(err)
	}
	var invalid *InvalidCallError
	if err := client.Sessions.RunTools(t.Context(), ""); !errors.As(err, &invalid) {
		t.Fatalf("error = %v; want *InvalidCallError", err)
	}
}
