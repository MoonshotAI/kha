package kha

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

type testRoundTripper func(*http.Request) (*http.Response, error)

func (f testRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type trackedResponseBody struct {
	io.Reader
	closed *bool
}

func (b *trackedResponseBody) Close() error { *b.closed = true; return nil }

func TestProtectedWriteReplayAndRetryAfter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var bodies, keys []string
		var attempts []time.Time
		var discardedClosed bool
		transport := testRoundTripper(func(req *http.Request) (*http.Response, error) {
			data, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatal(err)
			}
			_ = req.Body.Close()
			bodies = append(bodies, string(data))
			keys = append(keys, req.Header.Get("Idempotency-Key"))
			attempts = append(attempts, time.Now())
			if len(attempts) == 1 {
				return nil, io.EOF
			}
			resp := &http.Response{StatusCode: http.StatusCreated, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(agentA1)), Request: req}
			if len(attempts) == 2 {
				resp.StatusCode = http.StatusTooManyRequests
				resp.Header.Set("Retry-After", "1")
				resp.Body = &trackedResponseBody{Reader: strings.NewReader(`{"error":{"type":"rate_limit_reached_error"}}`), closed: &discardedClosed}
			}
			return resp, nil
		})
		hc := &http.Client{Transport: transport}
		client, err := New("", WithAnonymous(), WithHTTPClient(hc))
		if err != nil {
			t.Fatal(err)
		}
		result, err := client.Agents.Create(t.Context(), CreateAgentHTTPRequest{IdempotencyKey: new("caller-key"), Body: &CreateAgentRequest{Name: "helper", Model: *NewModelRefString("kimi-k2")}})
		if err != nil {
			t.Fatal(err)
		}
		if result.StatusCode != http.StatusCreated || len(attempts) != 3 || !discardedClosed {
			t.Fatalf("result=%+v attempts=%d discardedClosed=%v", result, len(attempts), discardedClosed)
		}
		for i := range bodies {
			if keys[i] != "caller-key" || bodies[i] != bodies[0] {
				t.Fatalf("attempt %d changed key or body: keys=%v bodies=%v", i, keys, bodies)
			}
		}
		firstDelay := attempts[1].Sub(attempts[0])
		if firstDelay < time.Second || firstDelay > 1250*time.Millisecond || attempts[2].Sub(attempts[1]) != time.Second {
			t.Fatalf("attempt timing=%v", attempts)
		}
		if _, ok := hc.Transport.(testRoundTripper); !ok {
			t.Fatal("New mutated the caller's HTTP client")
		}
	})
}

func TestUploadDoesNotRetryOrAddIdempotencyKey(t *testing.T) {
	calls := 0
	client, err := New("", WithAnonymous(), WithHTTPClient(&http.Client{Transport: testRoundTripper(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Header.Get("Idempotency-Key") != "" {
			t.Error("upload unexpectedly received an idempotency key")
		}
		if err := req.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = req.MultipartForm.RemoveAll() }()
		file, header, err := req.FormFile("file")
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(file)
		_ = file.Close()
		if err != nil || header.Filename != "test.txt" || string(content) != "hello" || req.FormValue("purpose") != "file-extract" {
			t.Fatalf("filename=%s content=%q purpose=%q error=%v", header.Filename, content, req.FormValue("purpose"), err)
		}
		_ = req.Body.Close()
		return &http.Response{StatusCode: 503, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"type":"internal_error","message":"try later"}}`)), Request: req}, nil
	})}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Files.Upload(t.Context(), UploadFileRequest{Body: &UploadFileBody{File: File{Filename: "test.txt", Body: strings.NewReader("hello")}, Purpose: new(FilePurposeFileExtract)}})
	if err == nil || calls != 1 {
		t.Fatalf("calls=%d error=%v", calls, err)
	}
}

func TestProtectedWriteRoutes(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		want   bool
	}{
		{name: "environment update", method: http.MethodPatch, path: "/v1/environments/env_1", want: true},
		{name: "session events", method: http.MethodPost, path: "/v1/sessions/sesn_1/events", want: true},
		{name: "session resources", method: http.MethodPost, path: "/v1/sessions/sesn_1/resources", want: true},
		{name: "credential create", method: http.MethodPost, path: "/v1/vaults/vlt_1/credentials", want: true},
		{name: "credential batch", method: http.MethodPost, path: "/v1/vaults/vlt_1/credentials/batch", want: true},
		{name: "upload remains unprotected", method: http.MethodPost, path: "/v1/files", want: false},
		{name: "credential check", method: http.MethodPost, path: "/v1/vaults/credentials/check", want: false},
		{name: "session continue", method: http.MethodPost, path: "/v1/sessions/sesn_1/continue", want: false},
		{name: "credential archive", method: http.MethodPost, path: "/v1/vaults/vlt_1/credentials/vcrd_1/archive", want: false},
		{name: "events read", method: http.MethodGet, path: "/v1/sessions/sesn_1/events", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := protectedWrite(tt.method, tt.path); got != tt.want {
				t.Fatalf("protectedWrite(%s, %s) = %v, want %v", tt.method, tt.path, got, tt.want)
			}
		})
	}
}

func TestEnvironmentUpdateRetriesWithStableKey(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var keys, bodies []string
		server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			keys = append(keys, r.Header.Get("Idempotency-Key"))
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			bodies = append(bodies, string(body))
			w.Header().Set("Content-Type", "application/json")
			if len(keys) == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = io.WriteString(w, `{"error":{"type":"server_error","message":"retry later"}}`)
				return
			}
			_, _ = io.WriteString(w, `{"id":"env_1","name":"updated","config":{"type":"self_hosted"},"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`)
		}))
		client := newTestClient(t, server)
		result, err := client.Environments.Update(t.Context(), UpdateEnvironmentHTTPRequest{ID: "env_1", IdempotencyKey: new("environment-update-key"), Body: &UpdateEnvironmentRequest{Name: new("updated")}})
		if err != nil || result.Body == nil || result.Body.ID != "env_1" {
			t.Fatalf("result=%+v error=%v", result, err)
		}
		if len(keys) != 2 || keys[0] == "" || keys[0] != keys[1] || bodies[0] != bodies[1] {
			t.Fatalf("keys=%v bodies=%v", keys, bodies)
		}
	})
}

func TestTriggerRunRetriesWithStableKey(t *testing.T) {
	for _, tc := range []struct {
		name, id, path string
		body           *RunTriggerRequest
		wireBody       string
	}{
		{"no body", "trig_1", "/v1/triggers/trig_1/run", nil, ""},
		{"escaped ID", "trig/part", "/v1/triggers/trig%2Fpart/run", &RunTriggerRequest{}, "{}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var keys []string
				server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					keys = append(keys, r.Header.Get("Idempotency-Key"))
					body, err := io.ReadAll(r.Body)
					if err != nil || string(body) != tc.wireBody || r.URL.EscapedPath() != tc.path {
						t.Errorf("body=%q path=%q error=%v", body, r.URL.EscapedPath(), err)
					}
					w.Header().Set("Content-Type", "application/json")
					if len(keys) == 1 {
						w.WriteHeader(http.StatusServiceUnavailable)
						_, _ = io.WriteString(w, `{"error":{"type":"server_error","message":"retry later"}}`)
						return
					}
					w.WriteHeader(http.StatusCreated)
					_, _ = io.WriteString(w, triggerRunPending)
				}))
				client := newTestClient(t, server)
				result, err := client.Triggers.Run(t.Context(), RunTriggerHTTPRequest{ID: tc.id, IdempotencyKey: new("trigger-run-key"), Body: tc.body})
				if err != nil || result.StatusCode != http.StatusCreated || result.Body == nil || len(keys) != 2 || keys[0] == "" || keys[0] != keys[1] {
					t.Fatalf("result=%+v keys=%v error=%v", result, keys, err)
				}
			})
		})
	}
}

func TestRetryTimingBounds(t *testing.T) {
	for _, tc := range []struct {
		attempt int
		minimum time.Duration
	}{
		{0, time.Second},
		{1, 2 * time.Second},
		{4, 16 * time.Second},
		{5, 30 * time.Second},
		{100, 30 * time.Second},
	} {
		delay := retryBackoff(tc.attempt)
		if delay < tc.minimum || delay > tc.minimum+tc.minimum/4 {
			t.Fatalf("attempt %d: delay %s outside [%s, %s]", tc.attempt, delay, tc.minimum, tc.minimum+tc.minimum/4)
		}
	}
	synctest.Test(t, func(t *testing.T) {
		for value, want := range map[string]time.Duration{
			"5": 5 * time.Second, "300": time.Minute, "9223372036854775807": time.Minute,
			"-1": 0, "invalid": 0, time.Now().Add(5 * time.Minute).UTC().Format(http.TimeFormat): time.Minute,
			"  " + time.Now().Add(5*time.Second).UTC().Format(http.TimeFormat) + "  ": 5 * time.Second,
		} {
			if got := retryAfter(value); got != want {
				t.Errorf("Retry-After %q = %s, want %s", value, got, want)
			}
		}
	})
}

// Downloads and SSE handshakes retain the single-request policy of DoRaw.
// SSE recovery, when enabled, owns subsequent connection attempts.
func TestRawOperationsDoNotRetry(t *testing.T) {
	for _, route := range []struct {
		name string
		path func(string) string
		call func(context.Context, *Client, string) error
	}{
		{
			name: "file content",
			path: func(id string) string { return "/v1/files/" + id + "/content" },
			call: func(ctx context.Context, c *Client, id string) error {
				result, err := c.Files.GetContent(ctx, GetFileContentRequest{ID: id})
				if result.Body != nil {
					_ = result.Body.Close()
				}
				return err
			},
		},
		{
			name: "artifact content",
			path: func(id string) string { return "/v1/artifacts/" + id + "/content" },
			call: func(ctx context.Context, c *Client, id string) error {
				result, err := c.Artifacts.GetContent(ctx, GetArtifactContentRequest{ID: id})
				if result.Body != nil {
					_ = result.Body.Close()
				}
				return err
			},
		},
		{
			name: "skill content",
			path: func(id string) string { return "/v1/skills/" + id + "/versions/1/content" },
			call: func(ctx context.Context, c *Client, id string) error {
				result, err := c.Skills.GetVersionContent(ctx, GetSkillVersionContentRequest{ID: id, Version: "1"})
				if result.Body != nil {
					_ = result.Body.Close()
				}
				return err
			},
		},
		{
			name: "plugin content",
			path: func(id string) string { return "/v1/plugins/" + id + "/versions/1/content" },
			call: func(ctx context.Context, c *Client, id string) error {
				result, err := c.Plugins.GetVersionContent(ctx, GetPluginVersionContentRequest{ID: id, Version: "1"})
				if result.Body != nil {
					_ = result.Body.Close()
				}
				return err
			},
		},
		{
			name: "filesystem download",
			path: func(id string) string { return "/v1/sessions/" + id + "/filesystem/raw" },
			call: func(ctx context.Context, c *Client, id string) error {
				result, err := c.Sessions.DownloadFilesystemFile(ctx, DownloadSessionFilesystemFileRequest{ID: id, Path: "outputs/report.txt"})
				if result.Body != nil {
					_ = result.Body.Close()
				}
				return err
			},
		},
		{
			name: "session stream",
			path: func(id string) string { return "/v1/sessions/" + id + "/events/stream" },
			call: func(ctx context.Context, c *Client, id string) error {
				for _, err := range c.Sessions.Events(ctx, id, WithResume(false)) {
					return err
				}
				return nil
			},
		},
		{
			name: "thread stream",
			path: func(id string) string { return "/v1/sessions/" + id + "/threads/" + id + "/events/stream" },
			call: func(ctx context.Context, c *Client, id string) error {
				for _, err := range c.Sessions.ThreadEvents(ctx, id, id, WithResume(false)) {
					return err
				}
				return nil
			},
		},
	} {
		for _, location := range []struct {
			name, basePath, wireBase, id string
		}{
			{name: "default root", id: "resource_1"},
			{name: "escaped path", basePath: "/tenant/../gateway%2Fteam", wireBase: "/gateway%2Fteam", id: "resource/part"},
		} {
			t.Run(route.name+"/"+location.name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					calls := 0
					server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls++
						wantPath := location.wireBase + route.path(url.PathEscape(location.id))
						if r.Method != http.MethodGet || r.URL.EscapedPath() != wantPath {
							t.Errorf("request = %s %s, want GET %s", r.Method, r.URL.EscapedPath(), wantPath)
						}
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusServiceUnavailable)
						_, _ = io.WriteString(w, `{"error":{"type":"server_error","message":"unavailable"}}`)
					}))
					client, err := New("", WithAnonymous(), WithBaseURL("https://api.example.test"+location.basePath), WithHTTPClient(server.Client()))
					if err != nil {
						t.Fatal(err)
					}
					err = route.call(t.Context(), client, location.id)
					if err == nil || calls != 1 {
						t.Fatalf("calls = %d, error = %v; want one request and failure", calls, err)
					}
				})
			})
		}
	}
}

// A JSON endpoint named "content" is not a raw download. Keep its retries,
// as well as metadata reads and the history side of SSE recovery.
func TestJSONReadsRetainRetries(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(context.Context, *Client) error
	}{
		{
			name: "memory version content",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.MemoryStores.GetMemoryVersionContent(ctx, GetMemoryVersionContentRequest{ID: "store_1", MemoryID: "memory_1", VersionID: "1"})
				return err
			},
		},
		{
			name: "file metadata",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.Files.Get(ctx, GetFileRequest{ID: "file_1"})
				return err
			},
		},
		{
			name: "session event history",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.Sessions.ListEvents(ctx, ListSessionEventsRequest{ID: "session_1"})
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					calls++
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = io.WriteString(w, `{"error":{"type":"server_error","message":"unavailable"}}`)
				}))
				client := newTestClient(t, server)
				err := tc.call(t.Context(), client)
				if err == nil || calls != 4 {
					t.Fatalf("calls = %d, error = %v; want initial request plus three retries", calls, err)
				}
			})
		})
	}
}

func pointerValue[T any](p *T) T {
	if p != nil {
		return *p
	}
	var zero T
	return zero
}

func TestPollNeverRetriesWithDefaultClient(t *testing.T) {
	for _, tc := range []struct {
		name, basePath, id, path string
	}{
		{"plain ID", "", "env", "/v1/environments/env/work/poll"},
		{"escaped ID", "", "env/part", "/v1/environments/env%2Fpart/work/poll"},
		{"escaped base path", "/tenant%2Fone", "env", "/tenant%2Fone/v1/environments/env/work/poll"},
		{"escaped base and ID", "/tenant%2Fone", "env/part", "/tenant%2Fone/v1/environments/env%2Fpart/work/poll"},
		{"normalized base path", "/tenant/../gateway", "env", "/gateway/v1/environments/env/work/poll"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var calls atomic.Int32
				server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					if r.URL.EscapedPath() != tc.path {
						t.Errorf("request path = %q, want %q", r.URL.EscapedPath(), tc.path)
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = io.WriteString(w, `{"error":{"type":"server_error","message":"retry later"}}`)
				}))
				client, err := New("test-key", WithBaseURL("https://api.example.test"+tc.basePath), WithHTTPClient(server.Client()))
				if err != nil {
					t.Fatal(err)
				}
				_, err = client.WorkItems.Poll(t.Context(), PollWorkItemsRequest{EnvironmentID: tc.id, WorkerID: "worker"})
				if err == nil || calls.Load() != 1 {
					t.Fatalf("calls=%d error=%v", calls.Load(), err)
				}
			})
		})
	}
}

func TestCreateWithoutIdempotencyKeyDoesNotRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var keys []string
		server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			keys = append(keys, r.Header.Get("Idempotency-Key"))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":{"type":"internal_error","message":"retry later"}}`)
		}))
		t.Cleanup(server.Close)
		client := newTestClient(t, server)
		_, err := client.Agents.Create(t.Context(), CreateAgentHTTPRequest{Body: &CreateAgentRequest{Name: "helper", Model: *NewModelRefString("kimi-k2")}})
		if err == nil || len(keys) != 1 || keys[0] != "" {
			t.Fatalf("keys=%v error=%v; want one request without an idempotency key", keys, err)
		}
	})
}

const inFlightConflictBody = `{"error":{"type":"conflict_error","message":"A request with the same Idempotency-Key is still in flight; retry shortly."}}`

// After a transient failure, Hex may still hold the in-flight marker for the
// same Idempotency-Key. Retry attempts must keep backing off on that 409 until
// the stored response is ready to replay.
func TestProtectedWriteRetriesInFlightConflict(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var attempts []time.Time
		var keys []string
		server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			attempts = append(attempts, time.Now())
			keys = append(keys, r.Header.Get("Idempotency-Key"))
			w.Header().Set("Content-Type", "application/json")
			switch len(attempts) {
			case 1:
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = io.WriteString(w, `{"error":{"type":"server_error","message":"retry later"}}`)
			case 2:
				w.WriteHeader(http.StatusConflict)
				_, _ = io.WriteString(w, inFlightConflictBody)
			default:
				w.WriteHeader(http.StatusCreated)
				_, _ = io.WriteString(w, agentA1)
			}
		}))
		client := newTestClient(t, server)
		result, err := client.Agents.Create(t.Context(), CreateAgentHTTPRequest{IdempotencyKey: new("in-flight-key"), Body: &CreateAgentRequest{Name: "helper", Model: *NewModelRefString("kimi-k2")}})
		if err != nil || result.StatusCode != http.StatusCreated || len(attempts) != 3 {
			t.Fatalf("result=%+v attempts=%d error=%v", result, len(attempts), err)
		}
		if keys[0] != "in-flight-key" || keys[0] != keys[1] || keys[1] != keys[2] {
			t.Fatalf("keys=%v", keys)
		}
		firstDelay := attempts[1].Sub(attempts[0])
		secondDelay := attempts[2].Sub(attempts[1])
		if firstDelay < time.Second || firstDelay > 1250*time.Millisecond || secondDelay < 2*time.Second || secondDelay > 2500*time.Millisecond {
			t.Fatalf("attempt timing=%v", attempts)
		}
	})
}

// A 409 on the first attempt is a real conflict; do not burn the retry budget.
func TestProtectedWriteDoesNotRetryFirstAttemptConflict(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"error":{"type":"conflict_error","message":"The Idempotency-Key was already used with a different request target, API version, or body."}}`)
		}))
		client := newTestClient(t, server)
		result, err := client.Agents.Create(t.Context(), CreateAgentHTTPRequest{IdempotencyKey: new("conflict-key"), Body: &CreateAgentRequest{Name: "helper", Model: *NewModelRefString("kimi-k2")}})
		if !IsConflict(err) || result.StatusCode != http.StatusConflict || calls != 1 {
			t.Fatalf("result=%+v calls=%d error=%v", result, calls, err)
		}
	})
}

// Safe reads never treat 409 as retryable, even after a prior transient failure.
func TestSafeReadDoesNotRetryConflict(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls++
			w.Header().Set("Content-Type", "application/json")
			if calls == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = io.WriteString(w, `{"error":{"type":"server_error","message":"retry later"}}`)
				return
			}
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"error":{"type":"conflict_error","message":"version mismatch"}}`)
		}))
		client := newTestClient(t, server)
		result, err := client.Agents.Get(t.Context(), GetAgentRequest{ID: "a1"})
		if !IsConflict(err) || result.StatusCode != http.StatusConflict || calls != 2 {
			t.Fatalf("result=%+v calls=%d error=%v", result, calls, err)
		}
	})
}

func TestProductOperationErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		call   func(*testing.T, *Client) (int, http.Header, error)
	}{
		{"bad request", http.StatusBadRequest, func(t *testing.T, c *Client) (int, http.Header, error) {
			r, err := c.Agents.Create(t.Context(), CreateAgentHTTPRequest{Body: &CreateAgentRequest{Name: "helper", Model: *NewModelRefString("kimi-k2")}})
			return r.StatusCode, r.Header, err
		}},
		{"not found", http.StatusNotFound, func(t *testing.T, c *Client) (int, http.Header, error) {
			r, err := c.Agents.Get(t.Context(), GetAgentRequest{ID: "a"})
			return r.StatusCode, r.Header, err
		}},
		{"conflict", http.StatusConflict, func(t *testing.T, c *Client) (int, http.Header, error) {
			r, err := c.Agents.Create(t.Context(), CreateAgentHTTPRequest{Body: &CreateAgentRequest{Name: "helper", Model: *NewModelRefString("kimi-k2")}})
			return r.StatusCode, r.Header, err
		}},
		{"OAuth conflict", http.StatusConflict, func(t *testing.T, c *Client) (int, http.Header, error) {
			r, err := c.Vaults.CreateOAuthSession(t.Context(), CreateVaultOAuthSessionHTTPRequest{ID: "v", Body: &CreateVaultOAuthSessionRequest{MCPServerURL: "https://example.com/mcp"}})
			return r.StatusCode, r.Header, err
		}},
		{"forbidden empty response", http.StatusForbidden, func(t *testing.T, c *Client) (int, http.Header, error) {
			r, err := c.Files.Delete(t.Context(), DeleteFileRequest{ID: "f"})
			return r.StatusCode, r.Header, err
		}},
		{"forbidden download", http.StatusForbidden, func(t *testing.T, c *Client) (int, http.Header, error) {
			r, err := c.Files.GetContent(t.Context(), GetFileContentRequest{ID: "f"})
			if r.Body != nil {
				_ = r.Body.Close()
				t.Error("error response leaked a download body")
			}
			return r.StatusCode, r.Header, err
		}},
		{"precondition failed", http.StatusPreconditionFailed, func(t *testing.T, c *Client) (int, http.Header, error) {
			r, err := c.WorkItems.Ack(t.Context(), AckWorkItemHTTPRequest{EnvironmentID: "e", ID: "w", Body: &AckWorkItemRequest{WorkerID: "worker", Generation: 1}})
			return r.StatusCode, r.Header, err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Msh-Request-Id", "req-specific-error")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","message":"operation failed","param":"id"}}`)
			}))
			status, header, err := tc.call(t, newTestClient(t, server))
			detail, ok := ErrorDetails(err)
			if !ok || detail.Message != "operation failed" || detail.Param == nil || *detail.Param != "id" {
				t.Fatalf("error=%v detail=%+v", err, detail)
			}
			if status != tc.status || header.Get("Msh-Request-Id") != "req-specific-error" {
				t.Fatalf("status=%d header=%v", status, header)
			}
		})
	}
}

func TestProductRejectsUnexpectedSuccess(t *testing.T) {
	server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Msh-Request-Id", "req-unexpected")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"error":{"type":"server_error","message":"unexpected success"}}`)
	}))
	result, err := newTestClient(t, server).Agents.Get(t.Context(), GetAgentRequest{ID: "a1"})
	if !IsProtocolError(err) || result.Body != nil || result.StatusCode != http.StatusCreated || result.Header.Get("Msh-Request-Id") != "req-unexpected" {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}

func TestProductErrorPreservesTypedCause(t *testing.T) {
	server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Msh-Request-Id", "req-typed")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"type":"not_found_error","message":"gone"}}`)
	}))
	result, err := newTestClient(t, server).Agents.Get(t.Context(), GetAgentRequest{ID: "a1"})
	wrapped := fmt.Errorf("get agent: %w", err)
	protocol, ok := errors.AsType[*Error](wrapped)
	if !ok || protocol.Body == nil || protocol.Body.Error.Message != "gone" {
		t.Fatalf("typed cause missing: %v", wrapped)
	}
	meta, ok := errors.AsType[HTTPError](wrapped)
	if !ok || meta.StatusCode() != http.StatusNotFound || meta.Header().Get("Msh-Request-Id") != "req-typed" || result.StatusCode != meta.StatusCode() {
		t.Fatalf("result=%+v error=%v", result, wrapped)
	}
	detail, ok := ErrorDetails(wrapped)
	if !ok || detail.Message != "gone" || !IsNotFound(wrapped) {
		t.Fatalf("product details missing: %v", wrapped)
	}
}
