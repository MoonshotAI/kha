package kha

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

const sessionS1 = `{"id":"s1","agent_id":"a1","environment_id":"e1","status":"running","created_at":"2026-10-22T01:00:00Z","updated_at":"2026-10-22T02:00:00Z"}`

// authWrap mirrors the public contract's authentication envelope.
func authWrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good-key" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"type":"authentication_error","message":"missing or invalid credential","request_id":"r-auth"}}`)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func newTestClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	httpClient := srv.Client()
	c, err := New("good-key", WithBaseURL(srv.URL), WithHTTPClient(httpClient), WithMaxRetries(3))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestCreateGetSessionRoundTrip exercises client construction (bearer auth,
// version header), the idempotent retry on Create, and model reads.
func TestCreateGetSessionRoundTrip(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(authWrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Kimi-Api-Version"); got != DefaultAPIVersion {
			t.Errorf("Kimi-Api-Version = %q, want the pinned default", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sessions":
			if hits.Add(1) == 1 {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = io.WriteString(w, `{"error":{"type":"rate_limit_reached_error","message":"slow down"}}`)
				return
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, sessionS1)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/s1":
			w.Header().Set("Msh-Request-Id", "req-get-1")
			_, _ = io.WriteString(w, sessionS1)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"type":"not_found_error","message":"no such session"}}`)
		}
	})))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	sessResult, err := c.Sessions.Create(context.Background(), CreateSessionHTTPRequest{IdempotencyKey: new("session-create-key"), Body: &CreateSessionRequest{AgentID: "a1", EnvironmentID: "e1"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	sess := sessResult.Body

	if got := hits.Load(); got != 2 {
		t.Fatalf("create hits = %d, want 2 (429 retry with caller idempotency key)", got)
	}
	if sess.ID != "s1" || sess.Status != "running" {
		t.Fatalf("model = %s/%s", sess.ID, sess.Status)
	}

	gotResult, err := c.Sessions.Get(context.Background(), GetSessionRequest{ID: "s1"})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got := gotResult.Body

	if got.AgentID != "a1" {
		t.Fatalf("snapshot agent = %q", got.AgentID)
	}
	if gotResult.Header.Get("Msh-Request-Id") != "req-get-1" {
		t.Fatalf("Msh-Request-Id header = %q, want the middleware header via Header", gotResult.Header.Get("Msh-Request-Id"))
	}
	if sessResult.StatusCode != http.StatusCreated {
		t.Fatal("Create returned an unexpected HTTP status")
	}
	if _, err := c.Sessions.Get(context.Background(), GetSessionRequest{ID: got.ID}); err != nil {
		t.Fatalf("Get again: %v", err)
	}

	// The 401 envelope decodes into *ResponseError[*UnauthorizedErrorResponse] with predicates.
	srv2 := httptest.NewServer(authWrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, sessionS1)
	})))
	t.Cleanup(srv2.Close)
	anon, err := New("", WithBaseURL(srv2.URL), WithAnonymous())
	if err != nil {
		t.Fatal(err)
	}
	_, getErr := anon.Sessions.Get(context.Background(), GetSessionRequest{ID: "s1"})
	if !IsUnauthorized(getErr) {
		t.Fatalf("Get without credential: %v, want unauthorized", getErr)
	}
	var ae *Error
	details, hasDetails := ErrorDetails(getErr)
	if !errors.As(getErr, &ae) || !hasDetails || details.Type != "authentication_error" || pointerValue(details.RequestID) != "r-auth" {
		t.Fatalf("decoded = %+v", ae)
	}
}

// TestAllPagination drives the product All iterator through the service
// layer across two pages.
func TestAllPagination(t *testing.T) {
	srv := httptest.NewServer(authWrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page_token") == "t2" {
			_, _ = io.WriteString(w, `{"items":[]}`)
			return
		}
		_, _ = io.WriteString(w, `{"items":[`+sessionS1+`],"next_page_token":"t2"}`)
	})))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	var ids []string
	for sess, err := range c.Sessions.All(context.Background(), ListSessionsRequest{}) {
		if err != nil {
			t.Fatalf("All: %v", err)
		}
		ids = append(ids, sess.ID)
	}
	if len(ids) != 1 || ids[0] != "s1" {
		t.Fatalf("ids = %v", ids)
	}
}

// TestCreateEvents posts user events through the generated Sessions client.
func TestCreateEvents(t *testing.T) {
	var gotBody atomic.Value
	srv := httptest.NewServer(authWrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/events") {
			b, _ := io.ReadAll(r.Body)
			gotBody.Store(string(b))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	sess := &Session{ID: "s1"}
	if _, err := c.Sessions.CreateEvents(context.Background(), CreateSessionEventsRequest{ID: sess.ID, Body: &SendEventsRequest{Events: []*UserEventInput{NewUserEventInputUserMessageInput(&UserMessageInput{})}}}); err != nil {
		t.Fatalf("CreateEvents: %v", err)
	}
	if body, _ := gotBody.Load().(string); !strings.Contains(body, `"events"`) {
		t.Fatalf("posted body = %q", body)
	}
}

func TestNewAppliesProductDefaultsAndGeneratedOptions(t *testing.T) {
	t.Setenv(apiKeyEnv, "env-key")
	for _, tc := range []struct {
		name                                 string
		key                                  string
		opts                                 []ClientOption
		wantAuth, wantVersion, wantUserAgent string
	}{
		{name: "environment fallback", wantAuth: "Bearer env-key", wantVersion: DefaultAPIVersion, wantUserAgent: defaultUserAgent()},
		{name: "explicit key", key: "explicit-key", wantAuth: "Bearer explicit-key", wantVersion: DefaultAPIVersion, wantUserAgent: defaultUserAgent()},
		{name: "anonymous", key: "explicit-key", opts: []ClientOption{WithAnonymous()}, wantVersion: DefaultAPIVersion, wantUserAgent: defaultUserAgent()},
		{name: "overrides", opts: []ClientOption{WithAPIVersion("future-version"), WithUserAgent("my-client"), WithBearerToken("override-key")}, wantAuth: "Bearer override-key", wantVersion: "future-version", wantUserAgent: "my-client"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := testRoundTripper(func(r *http.Request) (*http.Response, error) {
				if r.Header.Get("Authorization") != tc.wantAuth || r.Header.Get("Kimi-Api-Version") != tc.wantVersion || r.Header.Get("User-Agent") != tc.wantUserAgent || r.Header.Get("X-Custom") != "custom-value" {
					t.Errorf("unexpected headers: %v", r.Header)
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(sessionS1)), Request: r}, nil
			})
			hc := &http.Client{Transport: transport}
			opts := append([]ClientOption{WithHTTPClient(hc), WithHeader("X-Custom", "custom-value")}, tc.opts...)
			client, err := New(tc.key, opts...)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.Sessions.Get(t.Context(), GetSessionRequest{ID: "s1"}); err != nil {
				t.Fatal(err)
			}
			if _, ok := hc.Transport.(testRoundTripper); !ok {
				t.Fatal("New changed the caller's HTTP transport")
			}
		})
	}
}

const (
	agentA1   = `{"id":"a1","name":"helper","model":"kimi-k3.1-pro","version":"1","created_at":"2026-10-22T01:00:00Z","updated_at":"2026-10-22T01:00:00Z","scope":"project"}`
	agentA1V2 = `{"id":"a1","name":"helper-v2","model":"kimi-k3.1-pro","version":"2","created_at":"2026-10-22T01:00:00Z","updated_at":"2026-10-22T02:00:00Z","scope":"project"}`
)

// TestAgentsListVersions reads the Agent's immutable versions, newest first,
// through the paged call and the lazy iterator.
func TestAgentsListVersions(t *testing.T) {
	srv := httptest.NewServer(authWrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/v1/agents/a1/versions" {
			_, _ = io.WriteString(w, `{"items":[`+agentA1V2+`,`+agentA1+`]}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	agents := c.Agents

	ctx := context.Background()

	pageResult, err := agents.ListVersions(ctx, ListAgentVersionsRequest{ID: "a1"})
	if err != nil {
		t.Fatalf("ListVersions: %v", err)
	}
	page := pageResult.Body.Items
	next := pointerValue(pageResult.Body.NextPageToken)

	if len(page) != 2 || page[0].Version != "2" || page[1].Version != "1" || next != "" {
		t.Fatalf("page = %v items, next = %q", len(page), next)
	}

	var versions []string
	for a, err := range agents.AllVersions(ctx, ListAgentVersionsRequest{ID: "a1"}) {
		if err != nil {
			t.Fatalf("AllVersions: %v", err)
		}
		versions = append(versions, a.Version)
	}
	if len(versions) != 2 || versions[0] != "2" {
		t.Fatalf("versions = %v", versions)
	}
}

// workItemW1 is the poll delivery shape: the only response carrying secret.
const workItemW1 = `{"id":"w1","session_id":"s1","environment_id":"e1","status":"queued","generation":3,"worker_id":"wk1","lease_ttl_ms":30000,"created_at":"2026-10-22T01:00:00Z","claimed_at":"2026-10-22T01:00:01Z","secret":"c3d0LXRva2Vu"}`

// TestPollClaimEmptyQueueAndNoRetry exercises the PollClient contract: a
// retryable failure is not retried (one delivery-side-effect call, one HTTP
// request), a 200 claims the item with its lease credential, and a 204 is a
// nil-item, nil-error empty queue.
func TestPollClaimEmptyQueueAndNoRetry(t *testing.T) {
	var hits atomic.Int32
	var step atomic.Int32
	srv := httptest.NewServer(authWrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/v1/environments/e1/work/poll" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		q := r.URL.Query()
		if q.Get("worker_id") != "wk1" || q.Get("block_ms") != "250" || q.Get("lease_ttl_ms") != "30000" || q.Get("reclaim_older_than_ms") != "1000" {
			t.Errorf("poll query = %v", q)
		}
		w.Header().Set("Content-Type", "application/json")
		switch step.Load() {
		case 0:
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"type":"rate_limit_reached_error","message":"slow down"}}`)
		case 1:
			w.Header().Set("Msh-Request-Id", "req-poll-1")
			_, _ = io.WriteString(w, workItemW1)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})))
	t.Cleanup(srv.Close)

	if _, err := NewPollClient(srv.URL, ""); err == nil {
		t.Fatal("NewPollClient with an empty secret: want an error, no KIMI_API_KEY fallback")
	}
	c, err := NewPollClient(srv.URL, "good-key")
	if err != nil {
		t.Fatal(err)
	}
	work := c.WorkItems

	req := PollWorkItemsRequest{
		EnvironmentID: "e1", WorkerID: "wk1",
		BlockMs:            new(int32(250)),
		LeaseTTLMs:         new(int32(30000)),
		ReclaimOlderThanMs: new(int32(1000)),
	}

	// A 429 is retryable for plain GETs; the poll client must not retry it.
	_, pollErr := work.Poll(context.Background(), req)
	if !IsRateLimited(pollErr) {
		t.Fatalf("Poll on 429: %v, want rate limited", pollErr)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("poll hits = %d, want 1 (delivery side effect, no blind retry)", got)
	}

	step.Store(1)
	itemResult, err := work.Poll(context.Background(), req)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	item := itemResult.Body

	if item == nil || item.ID != "w1" || item.Generation != 3 {
		t.Fatalf("claimed = %+v", item)
	}
	if item.Secret == nil {
		t.Fatal("secret is absent")
	}
	if *item.Secret != "c3d0LXRva2Vu" {
		t.Fatalf("secret = %q, want c3d0LXRva2Vu", *item.Secret)
	}
	if itemResult.Header.Get("Msh-Request-Id") != "req-poll-1" {
		t.Fatalf("Msh-Request-Id header = %q", itemResult.Header.Get("Msh-Request-Id"))
	}

	step.Store(2)
	itemResult, err = work.Poll(context.Background(), req)
	if err != nil {
		t.Fatalf("Poll on an empty queue: %v", err)
	}

	if itemResult.Body != nil {
		t.Fatalf("empty queue response = %T, want NoContent", itemResult.Body)
	}
	if itemResult.StatusCode != http.StatusNoContent {
		t.Fatal("empty queue returned an unexpected HTTP status")
	}
}

func TestProductRequestAndResponseMapping(t *testing.T) {
	cases := []struct {
		name, method, path, body, requestFragment string
		status                                    int
		call                                      func(*testing.T, *Client)
	}{
		{"get", "GET", "/v1/agents/a", `{"id":"a"}`, "", 200, func(t *testing.T, c *Client) {
			r, e := c.Agents.Get(t.Context(), GetAgentRequest{ID: "a"})
			checkHTTPMetadata(t, r.StatusCode, r.Header, e, 200)
			if r.Body.ID != "a" {
				t.Fatal(r.Body)
			}
		}},
		{"create", "POST", "/v1/agents", `{"id":"a"}`, `"name":"helper"`, 201, func(t *testing.T, c *Client) {
			r, e := c.Agents.Create(t.Context(), CreateAgentHTTPRequest{Body: &CreateAgentRequest{Name: "helper", Model: *NewModelRefString("kimi-k3.1-pro")}})
			checkHTTPMetadata(t, r.StatusCode, r.Header, e, 201)
			if r.Body.ID != "a" {
				t.Fatal(r.Body)
			}
		}},
		{"update", "PATCH", "/v1/agents/a", `{"id":"a","name":"updated"}`, `"name":"updated"`, 200, func(t *testing.T, c *Client) {
			r, e := c.Agents.Update(t.Context(), UpdateAgentHTTPRequest{ID: "a", Body: &UpdateAgentRequest{Name: new("updated")}})
			checkHTTPMetadata(t, r.StatusCode, r.Header, e, 200)
			if r.Body.Name != "updated" {
				t.Fatal(r.Body)
			}
		}},
		{"archive", "POST", "/v1/agents/a/archive", "", "", 204, func(t *testing.T, c *Client) {
			r, e := c.Agents.Archive(t.Context(), ArchiveAgentRequest{ID: "a"})
			checkHTTPMetadata(t, r.StatusCode, r.Header, e, 204)
		}},
		{"delete", "DELETE", "/v1/environments/e", "", "", 204, func(t *testing.T, c *Client) {
			r, e := c.Environments.Delete(t.Context(), DeleteEnvironmentRequest{ID: "e"})
			checkHTTPMetadata(t, r.StatusCode, r.Header, e, 204)
		}},
		{"thread IDs", "GET", "/v1/sessions/s/threads/t", `{"id":"t","session_id":"s"}`, "", 200, func(t *testing.T, c *Client) {
			r, e := c.Sessions.GetThread(t.Context(), GetSessionThreadRequest{ID: "s", ThreadID: "t"})
			checkHTTPMetadata(t, r.StatusCode, r.Header, e, 200)
			if r.Body.ID != "t" || r.Body.SessionID != "s" {
				t.Fatal(r.Body)
			}
		}},
		{"credential IDs", "GET", "/v1/vaults/v/credentials/c", `{"id":"c","vault_id":"v","auth":{"type":"static_bearer","mcp_server_url":"https://example.com"}}`, "", 200, func(t *testing.T, c *Client) {
			r, e := c.Vaults.GetCredential(t.Context(), GetVaultCredentialRequest{ID: "v", CredentialID: "c"})
			checkHTTPMetadata(t, r.StatusCode, r.Header, e, 200)
			if r.Body.ID != "c" || r.Body.VaultID != "v" {
				t.Fatal(r.Body)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != tc.method || r.URL.Path != tc.path {
					t.Errorf("request=%s %s, want %s %s", r.Method, r.URL.Path, tc.method, tc.path)
				}
				data, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				if tc.requestFragment != "" && !strings.Contains(string(data), tc.requestFragment) {
					t.Errorf("body=%s, missing %s", data, tc.requestFragment)
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Msh-Request-Id", "request-1")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			tc.call(t, newTestClient(t, server))
			if calls != 1 {
				t.Fatalf("calls=%d, want one operation", calls)
			}
		})
	}
}

func checkHTTPMetadata(t *testing.T, status int, header http.Header, err error, want int) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	if status != want || header.Get("Msh-Request-Id") != "request-1" {
		t.Fatalf("status=%d header=%v", status, header)
	}
}

func TestDownloadBodyOwnership(t *testing.T) {
	closed := false
	client, err := New("", WithAnonymous(), WithHTTPClient(&http.Client{Transport: testRoundTripper(func(req *http.Request) (*http.Response, error) {
		if req.Method != "GET" || req.URL.Path != "/v1/files/f/content" {
			t.Errorf("request=%s %s", req.Method, req.URL.Path)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/octet-stream"}, "Content-Disposition": []string{`attachment; filename="file.bin"`}, "Msh-Request-Id": []string{"req-download"}}, Body: &trackedResponseBody{Reader: strings.NewReader("download"), closed: &closed}, Request: req}, nil
	})}))
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Files.GetContent(t.Context(), GetFileContentRequest{ID: "f"})
	if err != nil {
		t.Fatal(err)
	}
	if closed {
		t.Fatal("download closed before handoff")
	}
	if result.StatusCode != http.StatusOK || result.Header.Get("Msh-Request-Id") != "req-download" || result.Header.Get("Content-Disposition") != `attachment; filename="file.bin"` {
		t.Fatalf("download metadata=%+v", result)
	}
	body, err := io.ReadAll(result.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "download" {
		t.Fatalf("body=%q", body)
	}
	if err := result.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if !closed {
		t.Fatal("caller Close did not close the response body")
	}
}

func TestPatchFieldPresenceOnWire(t *testing.T) {
	for _, tc := range []struct {
		name string
		body *UpdateAgentRequest
		want string
	}{
		{"omitted collections", &UpdateAgentRequest{Name: new("renamed")}, `{"name":"renamed"}`},
		{"empty collections", &UpdateAgentRequest{Tools: []*Tool{}, Metadata: new(StringMetadata{})}, `{"tools":[],"metadata":{}}`},
		{"empty scalar", &UpdateAgentRequest{Description: new("")}, `{"description":""}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodPatch || r.URL.Path != "/v1/agents/agent_1" {
					t.Errorf("request = %s %s", r.Method, r.URL.Path)
				}
				data, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				if strings.TrimSpace(string(data)) != tc.want {
					t.Errorf("body = %s, want %s", data, tc.want)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"agent_1"}`)
			}))
			result, err := newTestClient(t, server).Agents.Update(t.Context(), UpdateAgentHTTPRequest{ID: "agent_1", Body: tc.body})
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || result.Body.ID != "agent_1" {
				t.Fatalf("calls = %d, result = %+v", calls, result)
			}
		})
	}
}

func TestWaitersRejectEmptyIDsImmediately(t *testing.T) {
	for _, tc := range []struct {
		name string
		wait func(*testing.T, *Client) error
	}{
		{"environment", func(t *testing.T, c *Client) error {
			_, err := c.Environments.WaitForReady(t.Context(), "")
			return err
		}},
		{"trigger", func(t *testing.T, c *Client) error {
			_, err := c.Triggers.WaitForTerminal(t.Context(), "", "run")
			return err
		}},
		{"trigger run", func(t *testing.T, c *Client) error {
			_, err := c.Triggers.WaitForTerminal(t.Context(), "trigger", "")
			return err
		}},
		{"vault", func(t *testing.T, c *Client) error {
			_, err := c.Vaults.WaitOAuthSession(t.Context(), "", "session")
			return err
		}},
		{"OAuth session", func(t *testing.T, c *Client) error {
			_, err := c.Vaults.WaitOAuthSession(t.Context(), "vault", "")
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				client, err := New("", WithAnonymous(), WithMaxRetries(0), WithHTTPClient(&http.Client{
					Transport: testRoundTripper(func(*http.Request) (*http.Response, error) {
						calls++
						return nil, errors.New("unexpected request")
					}),
				}))
				if err != nil {
					t.Fatal(err)
				}
				start := time.Now()
				err = tc.wait(t, client)
				if !IsProtocolError(err) || calls != 0 || time.Since(start) != 0 {
					t.Fatalf("error=%v requests=%d elapsed=%s, want immediate protocol error", err, calls, time.Since(start))
				}
			})
		})
	}
}

func TestEventStreamsRejectEmptyIDsImmediately(t *testing.T) {
	for _, tc := range []struct {
		name      string
		sessionID string
		threadID  string
		thread    bool
	}{
		{name: "session"},
		{name: "thread session", threadID: "thread", thread: true},
		{name: "thread", sessionID: "session", thread: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				client, err := New("", WithAnonymous(), WithHTTPClient(&http.Client{
					Transport: testRoundTripper(func(*http.Request) (*http.Response, error) {
						calls++
						return nil, errors.New("unexpected request")
					}),
				}))
				if err != nil {
					t.Fatal(err)
				}
				start := time.Now()
				stream := client.Sessions.Events(t.Context(), tc.sessionID)
				if tc.thread {
					stream = client.Sessions.ThreadEvents(t.Context(), tc.sessionID, tc.threadID)
				}
				var terminal error
				var results int
				for _, err := range stream {
					terminal = err
					results++
				}
				if !IsProtocolError(terminal) || calls != 0 || results != 1 || time.Since(start) != 0 {
					t.Fatalf("error=%v requests=%d results=%d elapsed=%s, want immediate protocol error", terminal, calls, results, time.Since(start))
				}
			})
		})
	}
}

func TestLoadSecretFileFromFile(t *testing.T) {
	dir := t.TempDir()

	path := filepath.Join(dir, "key")
	if err := os.WriteFile(path, []byte("  ewk-abc\n\t"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSecretFile(nil, path)
	if err != nil {
		t.Fatal(err)
	}
	if got != "ewk-abc" {
		t.Errorf("LoadSecretFile = %q, want %q", got, "ewk-abc")
	}

	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, []byte("  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSecretFile(nil, empty); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Errorf("empty file: err = %v, want it to say the file is empty", err)
	}

	missing := filepath.Join(dir, "missing")
	if _, err := LoadSecretFile(nil, missing); err == nil || !strings.Contains(err.Error(), missing) {
		t.Errorf("missing file: err = %v, want it to name the path", err)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

func TestLoadSecretFileFromStdin(t *testing.T) {
	got, err := LoadSecretFile(strings.NewReader("swt-xyz\n"), "-")
	if err != nil {
		t.Fatal(err)
	}
	if got != "swt-xyz" {
		t.Errorf("LoadSecretFile = %q, want %q", got, "swt-xyz")
	}

	// Redact "-" as "stdin" in read errors.
	_, err = LoadSecretFile(failingReader{}, "-")
	if err == nil {
		t.Fatal("expected a read error from a failing reader")
	}
	if !strings.Contains(err.Error(), "stdin") || strings.Contains(err.Error(), `"-"`) {
		t.Errorf("err = %v, want the redacted stdin name", err)
	}
}

func TestNewPollClientDisablesRetries(t *testing.T) {
	// Poll has delivery side effects, so even a retryable 500 must produce one request.
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, `{"error":{"type":"api_error","message":"boom","request_id":"req_1"}}`)
	}))
	defer srv.Close()

	client, err := NewPollClient(srv.URL, "ewk-secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.WorkItems.Stats(context.Background(), GetWorkItemStatsRequest{EnvironmentID: "env_1"}); err == nil {
		t.Fatal("expected the 500 to surface")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("server saw %d requests, want exactly 1 (poll client must not retry)", got)
	}
}
