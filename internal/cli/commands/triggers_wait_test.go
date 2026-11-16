package commands

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

// serveCommandTest routes the CLI's default HTTP client through the test
// server's transport, including its virtual network inside a synctest bubble.
// Callers must be sequential because the default transport is process-wide.
func serveCommandTest(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	server := httptest.NewTestServer(t, handler)
	previous := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = previous })
	return server
}

func TestTriggerWaitRequestTiming(t *testing.T) {
	for _, transient := range []bool{false, true} {
		t.Run(fmt.Sprintf("transient=%t", transient), func(t *testing.T) {
			isolateEnv(t)
			synctest.Test(t, func(t *testing.T) {
				var requests []time.Time
				server := serveCommandTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != http.MethodGet || r.URL.Path != "/v1/triggers/trig_1/runs/trun_1" {
						t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					}
					requests = append(requests, time.Now())
					w.Header().Set("Content-Type", "application/json")
					if transient && len(requests) <= 4 {
						// Exhaust the client's three retries so the waiter must recover.
						w.Header().Set("Retry-After", "7")
						w.WriteHeader(http.StatusServiceUnavailable)
						_, _ = fmt.Fprint(w, `{"error":{"type":"server_error","message":"try again"}}`)
						return
					}
					status := "completed"
					if !transient && len(requests) == 1 {
						status = "running"
					}
					_, _ = fmt.Fprintf(w, `{"id":"trun_1","trigger_id":"trig_1","status":%q,"created_at":"2026-08-01T10:00:00Z"}`, status)
				}))
				stdout, _, err := runCmd(t, "", "triggers", "runs", "wait", "trig_1", "trun_1", "--interval", "5s", "--timeout", "1m", "--base-url", server.URL, "--api-key", "sk-test")
				if err != nil {
					t.Fatal(err)
				}
				wantRequests, wantDelay := 2, 5*time.Second
				if transient {
					wantRequests, wantDelay = 5, 7*time.Second
				}
				if len(requests) != wantRequests {
					t.Fatalf("requests = %d, want %d", len(requests), wantRequests)
				}
				if delay := requests[len(requests)-1].Sub(requests[len(requests)-2]); delay != wantDelay {
					t.Errorf("last poll interval = %s, want %s", delay, wantDelay)
				}
				if !strings.Contains(stdout, "completed") {
					t.Errorf("missing terminal result: %s", stdout)
				}
			})
		})
	}
}

func TestTriggerWaitTimeoutCoversFirstRequest(t *testing.T) {
	isolateEnv(t)
	synctest.Test(t, func(t *testing.T) {
		requests := 0
		server := serveCommandTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests++
			// A response would eventually finish even if the wait timeout were
			// accidentally applied only after this first request.
			select {
			case <-r.Context().Done():
			case <-time.After(10 * time.Second):
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, `{"id":"trun_1","status":"completed"}`)
			}
		}))
		start := time.Now()
		_, _, err := runCmd(t, "", "triggers", "runs", "wait", "trig_1", "trun_1", "--timeout", "2s", "--base-url", server.URL, "--api-key", "sk-test")
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v, want deadline exceeded", err)
		}
		if elapsed := time.Since(start); elapsed != 2*time.Second {
			t.Errorf("elapsed = %s, want 2s", elapsed)
		}
		if requests != 1 {
			t.Errorf("requests = %d, want 1", requests)
		}
	})
}

func TestTriggerRunWaitSkipsPollingTerminalResponse(t *testing.T) {
	for _, status := range []string{"completed", "failed", "skipped"} {
		t.Run(status, func(t *testing.T) {
			isolateEnv(t)
			synctest.Test(t, func(t *testing.T) {
				var requests []string
				server := serveCommandTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests = append(requests, r.Method+" "+r.URL.Path)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusCreated)
					_, _ = fmt.Fprintf(w, `{"id":"trun_1","trigger_id":"trig_1","status":%q,"created_at":"2026-08-01T10:00:00Z"}`, status)
				}))
				stdout, _, err := runCmd(t, "", "triggers", "run", "trig_1", "--wait", "--base-url", server.URL, "--api-key", "sk-test")
				if err != nil {
					t.Fatal(err)
				}
				if got := strings.Join(requests, ", "); got != "POST /v1/triggers/trig_1/run" {
					t.Errorf("requests = %s, want only the run POST", got)
				}
				if !strings.Contains(stdout, status) {
					t.Errorf("missing terminal status: %s", stdout)
				}
			})
		})
	}
}
