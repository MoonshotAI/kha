package kha

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestPaginationLifecycle(t *testing.T) {
	t.Run("empty pages and null items", func(t *testing.T) {
		var tokens []string
		all := paginate(t.Context(), new("start"), func(_ context.Context, token *string) ([]*int, *string, error) {
			tokens = append(tokens, *token)
			if *token == "start" {
				return nil, new("next"), nil
			}
			return []*int{nil, new(7), nil}, new(""), nil
		})
		var values []int
		for value, err := range all {
			if err != nil {
				t.Fatal(err)
			}
			values = append(values, *value)
		}
		if !slices.Equal(tokens, []string{"start", "next"}) || !slices.Equal(values, []int{7}) {
			t.Fatalf("tokens=%v values=%v", tokens, values)
		}
	})
	t.Run("stop and reuse", func(t *testing.T) {
		calls := 0
		all := paginate(t.Context(), new("first"), func(_ context.Context, token *string) ([]*int, *string, error) {
			calls++
			if *token != "first" {
				t.Fatalf("unexpected page fetched: %s", *token)
			}
			return []*int{new(1), new(2)}, new("second"), nil
		})
		for range 2 {
			for value, err := range all {
				if err != nil || *value != 1 {
					t.Fatalf("value=%v error=%v", value, err)
				}
				break
			}
		}
		if calls != 2 {
			t.Fatalf("calls=%d, want one initial fetch per traversal", calls)
		}
	})
	t.Run("nonadvancing token", func(t *testing.T) {
		calls := 0
		all := paginate(t.Context(), new("stuck"), func(context.Context, *string) ([]*int, *string, error) { calls++; return nil, new("stuck"), nil })
		var terminal error
		for _, err := range all {
			terminal = err
		}
		if !IsProtocolError(terminal) || calls != 1 {
			t.Fatalf("calls=%d error=%v", calls, terminal)
		}
	})
	t.Run("cancel before fetching", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		all := paginate(ctx, nil, func(context.Context, *string) ([]*int, *string, error) {
			t.Fatal("fetch called after cancellation")
			return nil, nil, nil
		})
		var terminal error
		for _, err := range all {
			terminal = err
		}
		if !errors.Is(terminal, context.Canceled) {
			t.Fatalf("error=%v", terminal)
		}
	})
	t.Run("fetch error", func(t *testing.T) {
		fault := errors.New("fetch failed")
		all := paginate(t.Context(), nil, func(context.Context, *string) ([]*int, *string, error) { return nil, nil, fault })
		var terminal error
		for _, err := range all {
			terminal = err
		}
		if !errors.Is(terminal, fault) {
			t.Fatalf("error=%v", terminal)
		}
	})
}

func TestPaginationRejectsTokenCycles(t *testing.T) {
	for _, initial := range []*string{nil, new("a")} {
		name := "first page"
		if initial != nil {
			name = "initial token"
		}
		t.Run(name, func(t *testing.T) {
			var tokens []string
			all := paginate(t.Context(), initial, func(_ context.Context, token *string) ([]*int, *string, error) {
				value := ""
				if token != nil {
					value = *token
				}
				tokens = append(tokens, value)
				if len(tokens) > 3 {
					t.Fatal("pagination fetched a previously visited token")
				}
				if value == "a" {
					return []*int{new(1)}, new("b"), nil
				}
				return []*int{new(2)}, new("a"), nil
			})
			var terminal error
			for _, err := range all {
				terminal = err
			}
			want := []string{"", "a", "b"}
			if initial != nil {
				want = []string{"a", "b"}
			}
			if !IsProtocolError(terminal) || !slices.Equal(tokens, want) {
				t.Fatalf("tokens=%v error=%v, want %v and a protocol error", tokens, terminal, want)
			}
		})
	}
}

func TestListResponsesRetainItemsAndMetadata(t *testing.T) {
	cases := []struct {
		name, path string
		run        func(*testing.T, *Client) ([]byte, error)
	}{
		{"agents", "/v1/agents", func(t *testing.T, c *Client) ([]byte, error) {
			return listResponseJSON(c.Agents.List(t.Context(), ListAgentsRequest{}))
		}},
		{"agent versions", "/v1/agents/a/versions", func(t *testing.T, c *Client) ([]byte, error) {
			return listResponseJSON(c.Agents.ListVersions(t.Context(), ListAgentVersionsRequest{ID: "a"}))
		}},
		{"artifacts", "/v1/artifacts", func(t *testing.T, c *Client) ([]byte, error) {
			return listResponseJSON(c.Artifacts.List(t.Context(), ListArtifactsRequest{}))
		}},
		{"artifact versions", "/v1/artifacts/a/versions", func(t *testing.T, c *Client) ([]byte, error) {
			return listResponseJSON(c.Artifacts.ListVersions(t.Context(), ListArtifactVersionsRequest{ID: "a"}))
		}},
		{"environments", "/v1/environments", func(t *testing.T, c *Client) ([]byte, error) {
			return listResponseJSON(c.Environments.List(t.Context(), ListEnvironmentsRequest{}))
		}},
		{"files", "/v1/files", func(t *testing.T, c *Client) ([]byte, error) {
			return listResponseJSON(c.Files.List(t.Context(), ListFilesRequest{}))
		}},
		{"memory stores", "/v1/memory-stores", func(t *testing.T, c *Client) ([]byte, error) {
			return listResponseJSON(c.MemoryStores.List(t.Context(), ListMemoryStoresRequest{}))
		}},
		{"memories", "/v1/memory-stores/m/memories", func(t *testing.T, c *Client) ([]byte, error) {
			return listResponseJSON(c.MemoryStores.ListMemories(t.Context(), ListMemoriesRequest{ID: "m"}))
		}},
		{"memory versions", "/v1/memory-stores/m/memories/n/versions", func(t *testing.T, c *Client) ([]byte, error) {
			return listResponseJSON(c.MemoryStores.ListMemoryVersions(t.Context(), ListMemoryVersionsRequest{ID: "m", MemoryID: "n"}))
		}},
		{"plugins", "/v1/plugins", func(t *testing.T, c *Client) ([]byte, error) {
			return listResponseJSON(c.Plugins.List(t.Context(), ListPluginsRequest{}))
		}},
		{"plugin versions", "/v1/plugins/p/versions", func(t *testing.T, c *Client) ([]byte, error) {
			return listResponseJSON(c.Plugins.ListVersions(t.Context(), ListPluginVersionsRequest{ID: "p"}))
		}},
		{"sessions", "/v1/sessions", func(t *testing.T, c *Client) ([]byte, error) {
			return listResponseJSON(c.Sessions.List(t.Context(), ListSessionsRequest{}))
		}},
		{"session events", "/v1/sessions/s/events", func(t *testing.T, c *Client) ([]byte, error) {
			return listResponseJSON(c.Sessions.ListEvents(t.Context(), ListSessionEventsRequest{ID: "s"}))
		}},
		{"threads", "/v1/sessions/s/threads", func(t *testing.T, c *Client) ([]byte, error) {
			return listResponseJSON(c.Sessions.ListThreads(t.Context(), ListSessionThreadsRequest{ID: "s"}))
		}},
		{"thread events", "/v1/sessions/s/threads/t/events", func(t *testing.T, c *Client) ([]byte, error) {
			return listResponseJSON(c.Sessions.ListThreadEvents(t.Context(), ListThreadEventsRequest{ID: "s", ThreadID: "t"}))
		}},
		{"skills", "/v1/skills", func(t *testing.T, c *Client) ([]byte, error) {
			return listResponseJSON(c.Skills.List(t.Context(), ListSkillsRequest{}))
		}},
		{"skill versions", "/v1/skills/k/versions", func(t *testing.T, c *Client) ([]byte, error) {
			return listResponseJSON(c.Skills.ListVersions(t.Context(), ListSkillVersionsRequest{ID: "k"}))
		}},
		{"triggers", "/v1/triggers", func(t *testing.T, c *Client) ([]byte, error) {
			return listResponseJSON(c.Triggers.List(t.Context(), ListTriggersRequest{}))
		}},
		{"trigger runs", "/v1/triggers/r/runs", func(t *testing.T, c *Client) ([]byte, error) {
			return listResponseJSON(c.Triggers.ListRuns(t.Context(), ListTriggerRunsRequest{ID: "r"}))
		}},
		{"vaults", "/v1/vaults", func(t *testing.T, c *Client) ([]byte, error) {
			return listResponseJSON(c.Vaults.List(t.Context(), ListVaultsRequest{}))
		}},
		{"vault credentials", "/v1/vaults/v/credentials", func(t *testing.T, c *Client) ([]byte, error) {
			return listResponseJSON(c.Vaults.ListCredentials(t.Context(), ListVaultCredentialsRequest{ID: "v"}))
		}},
		{"work items", "/v1/environments/e/work", func(t *testing.T, c *Client) ([]byte, error) {
			return listResponseJSON(c.WorkItems.List(t.Context(), ListWorkRequest{EnvironmentID: "e"}))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != tc.path {
					t.Errorf("request=%s %s, want GET %s", r.Method, r.URL.Path, tc.path)
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Msh-Request-Id", "req-list")
				item := `{"id":"row","model":"kimi-k3.1-pro","config":{"type":"cloud"},"policy":{"type":"cron"},"auth":{"type":"static_bearer","mcp_server_url":"https://example.com"}}`
				if strings.Contains(tc.name, "events") {
					item = eventsUserMessageE1
				}
				_, _ = io.WriteString(w, `{"items":[null,`+item+`,null],"next_page_token":"next","stream_cursor":"cursor"}`)
			}))
			raw, err := tc.run(t, newTestClient(t, server))
			if err != nil {
				t.Fatal(err)
			}
			var page struct {
				Items         []jsontext.Value `json:"items"`
				NextPageToken string           `json:"next_page_token"`
				StreamCursor  string           `json:"stream_cursor"`
			}
			if err := json.Unmarshal(raw, &page); err != nil {
				t.Fatal(err)
			}
			if len(page.Items) != 3 || page.Items[0].Kind() != jsontext.KindNull || page.Items[1].Kind() != jsontext.KindBeginObject || page.Items[2].Kind() != jsontext.KindNull || page.NextPageToken != "next" {
				t.Fatalf("page lost an item or token: %s", raw)
			}
			if strings.Contains(tc.name, "events") && page.StreamCursor != "cursor" {
				t.Fatalf("event page lost its stream cursor: %s", raw)
			}
		})
	}
}

func listResponseJSON[T any](result HTTPResult[*T], err error) ([]byte, error) {
	if err != nil {
		return nil, err
	}
	if result.StatusCode != http.StatusOK || result.Header.Get("Msh-Request-Id") != "req-list" {
		return nil, fmt.Errorf("list response lost HTTP metadata: status=%d header=%v", result.StatusCode, result.Header)
	}
	return json.Marshal(result.Body)
}

// Every row covers handwritten wiring: the group operation, nested path IDs,
// response branch, and token propagation. Encoding itself belongs to the generated codec.
func TestPaginationRoutes(t *testing.T) {
	cases := []struct {
		name, path string
		run        func(*testing.T, *Client) int
	}{
		{"agents", "/v1/agents", func(t *testing.T, c *Client) int {
			return countItems(t, c.Agents.All(t.Context(), ListAgentsRequest{}))
		}},
		{"agent versions", "/v1/agents/a/versions", func(t *testing.T, c *Client) int {
			return countItems(t, c.Agents.AllVersions(t.Context(), ListAgentVersionsRequest{ID: "a"}))
		}},
		{"artifacts", "/v1/artifacts", func(t *testing.T, c *Client) int {
			return countItems(t, c.Artifacts.All(t.Context(), ListArtifactsRequest{}))
		}},
		{"artifact versions", "/v1/artifacts/a/versions", func(t *testing.T, c *Client) int {
			return countItems(t, c.Artifacts.AllVersions(t.Context(), ListArtifactVersionsRequest{ID: "a"}))
		}},
		{"environments", "/v1/environments", func(t *testing.T, c *Client) int {
			return countItems(t, c.Environments.All(t.Context(), ListEnvironmentsRequest{}))
		}},
		{"files", "/v1/files", func(t *testing.T, c *Client) int { return countItems(t, c.Files.All(t.Context(), ListFilesRequest{})) }},
		{"memory stores", "/v1/memory-stores", func(t *testing.T, c *Client) int {
			return countItems(t, c.MemoryStores.All(t.Context(), ListMemoryStoresRequest{}))
		}},
		{"memories", "/v1/memory-stores/m/memories", func(t *testing.T, c *Client) int {
			return countItems(t, c.MemoryStores.AllMemories(t.Context(), ListMemoriesRequest{ID: "m"}))
		}},
		{"memory versions", "/v1/memory-stores/m/memories/n/versions", func(t *testing.T, c *Client) int {
			return countItems(t, c.MemoryStores.AllMemoryVersions(t.Context(), ListMemoryVersionsRequest{ID: "m", MemoryID: "n"}))
		}},
		{"plugins", "/v1/plugins", func(t *testing.T, c *Client) int {
			return countItems(t, c.Plugins.All(t.Context(), ListPluginsRequest{}))
		}},
		{"plugin versions", "/v1/plugins/p/versions", func(t *testing.T, c *Client) int {
			return countItems(t, c.Plugins.AllVersions(t.Context(), ListPluginVersionsRequest{ID: "p"}))
		}},
		{"sessions", "/v1/sessions", func(t *testing.T, c *Client) int {
			return countItems(t, c.Sessions.All(t.Context(), ListSessionsRequest{}))
		}},
		{"session events", "/v1/sessions/s/events", func(t *testing.T, c *Client) int {
			return countItems(t, c.Sessions.AllEvents(t.Context(), ListSessionEventsRequest{ID: "s"}))
		}},
		{"session resources", "/v1/sessions/s/resources", func(t *testing.T, c *Client) int {
			return countItems(t, c.Sessions.AllResources(t.Context(), ListSessionResourcesRequest{ID: "s"}))
		}},
		{"threads", "/v1/sessions/s/threads", func(t *testing.T, c *Client) int {
			return countItems(t, c.Sessions.AllThreads(t.Context(), ListSessionThreadsRequest{ID: "s"}))
		}},
		{"thread events", "/v1/sessions/s/threads/t/events", func(t *testing.T, c *Client) int {
			return countItems(t, c.Sessions.AllThreadEvents(t.Context(), ListThreadEventsRequest{ID: "s", ThreadID: "t"}))
		}},
		{"skills", "/v1/skills", func(t *testing.T, c *Client) int {
			return countItems(t, c.Skills.All(t.Context(), ListSkillsRequest{}))
		}},
		{"skill versions", "/v1/skills/k/versions", func(t *testing.T, c *Client) int {
			return countItems(t, c.Skills.AllVersions(t.Context(), ListSkillVersionsRequest{ID: "k"}))
		}},
		{"triggers", "/v1/triggers", func(t *testing.T, c *Client) int {
			return countItems(t, c.Triggers.All(t.Context(), ListTriggersRequest{}))
		}},
		{"trigger runs", "/v1/triggers/r/runs", func(t *testing.T, c *Client) int {
			return countItems(t, c.Triggers.AllRuns(t.Context(), ListTriggerRunsRequest{ID: "r"}))
		}},
		{"vaults", "/v1/vaults", func(t *testing.T, c *Client) int {
			return countItems(t, c.Vaults.All(t.Context(), ListVaultsRequest{}))
		}},
		{"vault credentials", "/v1/vaults/v/credentials", func(t *testing.T, c *Client) int {
			return countItems(t, c.Vaults.AllCredentials(t.Context(), ListVaultCredentialsRequest{ID: "v"}))
		}},
		{"work items", "/v1/environments/e/work", func(t *testing.T, c *Client) int {
			return countItems(t, c.WorkItems.All(t.Context(), ListWorkRequest{EnvironmentID: "e"}))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != http.MethodGet || r.URL.Path != tc.path {
					t.Errorf("request=%s %s, want GET %s", r.Method, r.URL.Path, tc.path)
				}
				wantToken := ""
				if requests == 2 {
					wantToken = "next"
				}
				if token := r.URL.Query().Get("page_token"); token != wantToken {
					t.Errorf("page_token=%q want %q", token, wantToken)
				}
				w.Header().Set("Content-Type", "application/json")
				// Event unions require their discriminator; other models accept an ID.
				item := `{"id":"row"}`
				if strings.Contains(tc.name, "events") {
					item = eventsUserMessageE1
				}
				if tc.name == "session resources" {
					item = `{"type":"file","id":"resource","file_id":"file","path":"/upload/a.csv"}`
				}
				if requests == 1 {
					_, _ = io.WriteString(w, `{"items":[`+item+`],"next_page_token":"next"}`)
				} else {
					_, _ = io.WriteString(w, `{"items":[`+item+`]}`)
				}
			}))
			if count := tc.run(t, newTestClient(t, server)); count != 2 || requests != 2 {
				t.Fatalf("items=%d requests=%d, want two pages", count, requests)
			}
		})
	}
}

func countItems[T any](t *testing.T, all iter.Seq2[*T, error]) int {
	t.Helper()
	count := 0
	for item, err := range all {
		if err != nil {
			t.Fatal(err)
		}
		if item == nil {
			t.Fatal("iterator yielded nil item")
		}
		count++
	}
	return count
}

// syntacticDecodeErr is a real JSON syntax failure as produced by a response
// body decode.
func syntacticDecodeErr() error {
	return json.Unmarshal([]byte(`{"items": [`), new(any))
}

// semanticDecodeErr is a real JSON semantic failure (type mismatch).
func semanticDecodeErr() error {
	return json.Unmarshal([]byte(`"abc"`), new(int))
}

func TestIsProtocolError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"invalid call", &InvalidCallError{Reason: "empty id"}, true},
		{"wrapped invalid call", fmt.Errorf("poll: %w", &InvalidCallError{Reason: "empty id"}), true},
		{"request validation", ValidationErrors{{Field: "id", Message: "required"}}, true},
		{"stream null payload", &StreamDecodeError{Event: "agent.message", Err: errors.New("data must not be null")}, true},
		{"unexpected success contract", NewUnexpectedResponseError(200, nil, "media", nil, "unexpected content type", nil), true},
		{"2xx syntax decode failure", NewUnexpectedResponseError(200, nil, "decode", nil, "", syntacticDecodeErr()), true},
		{"2xx semantic decode failure", NewUnexpectedResponseError(200, nil, "decode", nil, "", semanticDecodeErr()), true},
		{"body cut short mid-read", NewUnexpectedResponseError(200, nil, "read", nil, "", io.ErrUnexpectedEOF), false},
		{"proxy error page", NewUnexpectedResponseError(502, nil, "decode", nil, "", syntacticDecodeErr()), false},
		{"api error", NewUnexpectedResponseError(500, nil, "status", nil, "", nil), false},
		{"network error", errors.New("dial timeout"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsProtocolError(c.err); got != c.want {
				t.Errorf("IsProtocolError(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

func TestIsTransientStreamError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"2xx decode failure is terminal", NewUnexpectedResponseError(200, nil, "decode", nil, "", syntacticDecodeErr()), false},
		{"2xx content type mismatch is terminal", NewUnexpectedResponseError(200, nil, "media", nil, "unexpected content type", nil), false},
		{"network error", errors.New("connection reset"), true},
		{"4xx interrupted error body", NewUnexpectedResponseError(404, nil, "read", nil, "", io.ErrUnexpectedEOF), false},
		{"5xx interrupted error body", NewUnexpectedResponseError(503, nil, "read", nil, "", io.ErrUnexpectedEOF), true},
		{"429 interrupted error body", NewUnexpectedResponseError(429, nil, "read", nil, "", io.ErrUnexpectedEOF), true},
		{"2xx interrupted body read", NewUnexpectedResponseError(200, nil, "read", nil, "", io.ErrUnexpectedEOF), true},
		{"429", NewUnexpectedResponseError(http.StatusTooManyRequests, nil, "status", nil, "", nil), true},
		{"500", NewUnexpectedResponseError(500, nil, "status", nil, "", nil), true},
		{"404", NewUnexpectedResponseError(404, nil, "status", nil, "", nil), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isTransientStreamError(c.err); got != c.want {
				t.Errorf("isTransientStreamError(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

func TestStreamHandshakeContractErrorDoesNotReconnect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{}`)
		}))
		client := newTestClient(t, server)
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		var result error
		for _, err := range client.Sessions.Events(ctx, "s1") {
			result = err
		}
		if !IsProtocolError(result) || calls.Load() != 1 {
			t.Fatalf("calls=%d error=%v", calls.Load(), result)
		}
	})
}

// TestStreamEventsBackfillDecodeErrorTerminal pins the livelock fix: an
// expired-cursor recovery whose history page is undecodable must surface the
// error and end the iteration after exactly one history request, not retry
// the same deterministic fetch forever.
func TestStreamEventsBackfillDecodeErrorTerminal(t *testing.T) {
	var historyHits atomic.Int32
	srv := httptest.NewServer(authWrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/sessions/s1/events/stream":
			switch r.URL.Query().Get("cursor") {
			case "stale-cursor":
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":{"type":"invalid_parameter_error","message":"the stream cursor expired","param":"cursor"}}`)
			case "now":
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, eventsFrame("c0", "control", `{"cursor":"c0","reason":"tail"}`))
				eventsFlush(w)
				<-r.Context().Done()
			default:
				w.WriteHeader(http.StatusBadRequest)
			}
		case "/v1/sessions/s1/events":
			historyHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"items": [`) // truncated: undecodable 2xx page
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	sess := &Session{ID: "s1"}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var gotErr error
	for _, err := range c.Sessions.Events(ctx, sess.ID, WithCursor("stale-cursor")) {
		if err == nil {
			continue
		}
		gotErr = err
	}
	if gotErr == nil {
		t.Fatal("expected the undecodable history page to surface as an error")
	}
	if !IsProtocolError(gotErr) {
		t.Fatalf("error = %v, want a protocol error", gotErr)
	}
	if got := historyHits.Load(); got != 1 {
		t.Fatalf("history requests = %d, want exactly 1 (no retry of a deterministic failure)", got)
	}
}

func TestHTTPErrorEnvelopeAndMetadata(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusNotFound, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", "7")
				w.Header().Set("Msh-Request-Id", "req-error")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"error":{"type":"not_found_error","message":"gone","param":"id","request_id":"req-body"}}`)
			}))
			client, err := New("test-key", WithHTTPClient(server.Client()), WithBaseURL(server.URL), WithMaxRetries(0))
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.Agents.Get(t.Context(), GetAgentRequest{ID: "missing"})
			wrapped := fmt.Errorf("fetch: %w", err)
			apiErr, ok := errors.AsType[*Error](wrapped)
			if !ok || apiErr.StatusCode() != status || apiErr.Body.Error.Message != "gone" {
				t.Fatalf("apiErr=%+v err=%v", apiErr, err)
			}
			response, ok := errors.AsType[HTTPError](wrapped)
			if !ok {
				t.Fatalf("error=%v", err)
			}
			detail, ok := ErrorDetails(wrapped)
			if !ok || detail.Message != "gone" || detail.Param == nil || *detail.Param != "id" {
				t.Fatalf("detail=%+v response=%+v", detail, response)
			}
			for _, want := range []string{"fetch: HTTP ", "not_found_error: gone", "(param: id)", "(request id: req-body)"} {
				if !strings.Contains(wrapped.Error(), want) {
					t.Fatalf("wrapped.Error() = %q, missing %q", wrapped.Error(), want)
				}
			}
			if result.Body != nil || result.StatusCode != status || result.Header.Get("Msh-Request-Id") != "req-error" || response.StatusCode() != status || response.Header().Get("Retry-After") != "7" ||
				response.Header().Get("Msh-Request-Id") != "req-error" {
				t.Fatalf("result=%+v response=%+v", result, response)
			}
		})
	}
}

func TestUndecodableHTTPFailuresKeepTheirStatus(t *testing.T) {
	for _, tc := range []struct {
		status    int
		transient bool
		outcome   streamOutcome
	}{
		{http.StatusNotFound, false, outTerminal},
		{http.StatusTooManyRequests, true, outTransient},
		{http.StatusBadGateway, true, outTransient},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			err := NewUnexpectedResponseError(tc.status, http.Header{"Retry-After": []string{"7"}}, "media", []byte("proxy response"), "unexpected content type", nil)
			if got := isTransientStreamError(err); got != tc.transient {
				t.Fatalf("transient=%v", got)
			}
			stream := eventStream{cfg: eventsConfig{resume: true}}
			if got := stream.classify(err); got != tc.outcome {
				t.Fatalf("outcome=%v want=%v", got, tc.outcome)
			}
			if tc.status == http.StatusTooManyRequests && (!IsRateLimited(err) || transientWait(0, err) != 7*time.Second) {
				t.Fatal("lost retry status or delay")
			}
		})
	}
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		failure := NewUnexpectedResponseError(http.StatusNotFound, nil, "media", nil, "unexpected content type", nil)
		_, err := poll(
			t.Context(),
			waitConfig{timeout: time.Minute, interval: time.Second},
			func(context.Context) (*Agent, error) { calls++; return nil, failure },
			func(*Agent) (bool, error) { return false, nil },
		)
		if !errors.Is(err, failure) || calls != 1 || !IsNotFound(err) {
			t.Fatalf("calls=%d err=%v", calls, err)
		}
	})
}

func TestStreamNullPayloadStopsRecovery(t *testing.T) {
	for _, scope := range []string{"session", "thread"} {
		t.Run(scope, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var calls atomic.Int32
				server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					calls.Add(1)
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "id: cursor-bad\nevent: agent.message\ndata: null\n\n")
				}))
				client := newTestClient(t, server)
				ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
				defer cancel()
				events := client.Sessions.Events(ctx, "s1")
				if scope == "thread" {
					events = client.Sessions.ThreadEvents(ctx, "s1", "t1")
				}
				var terminal error
				for _, err := range events {
					terminal = err
				}
				decode, ok := errors.AsType[*StreamDecodeError](terminal)
				if !ok || decode.Event != "agent.message" || !IsProtocolError(terminal) || calls.Load() != 1 {
					t.Fatalf("calls=%d terminal=%v; invalid event data must stop recovery", calls.Load(), terminal)
				}
			})
		})
	}
}

// Metadata remains available even when an error response has no typed body.
func TestHTTPErrorMetadataWithoutBody(t *testing.T) {
	header := http.Header{"Msh-Request-Id": []string{"req-error"}}
	err := fmt.Errorf("fetch: %w", NewUnexpectedResponseError(503, header, "media", nil, "unexpected content type", nil))
	status, gotHeader, ok := httpErrorMetadata(err)
	if !ok || status != 503 || gotHeader.Get("Msh-Request-Id") != "req-error" || !IsServerError(err) {
		t.Fatalf("metadata=%d %v %v, error=%v", status, gotHeader, ok, err)
	}
	if _, ok := ErrorDetails(err); ok {
		t.Fatal("unexpected typed error details without a decoded body")
	}
}

func TestStreamBackfillRetriesInterruptedBodyRead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var histories atomic.Int32
		server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path == "/v1/sessions/s1/events" {
				if histories.Add(1) == 1 {
					w.Header().Set("Content-Length", "1000")
					_, _ = io.WriteString(w, `{"items":[`)
					return
				}
				_, _ = fmt.Fprintf(w, `{"items":[%s],"stream_cursor":"tip"}`, eventsUserMessageE1)
				return
			}
			if r.URL.Query().Get("cursor") == "stale" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":{"type":"invalid_parameter_error","message":"stream cursor expired","param":"cursor"}}`)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "event: error\ndata: {\"error\":{\"type\":\"server_error\",\"message\":\"stop\"}}\n\n")
		}))
		client := newTestClient(t, server)
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
		if histories.Load() != 2 || fmt.Sprint(ids) != "[e1]" || !errors.As(terminal, &streamErr) {
			t.Fatalf("histories=%d ids=%v error=%v, want recovered history then stream error", histories.Load(), ids, terminal)
		}
	})
}
