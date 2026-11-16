package kha

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

const environmentE1Building = `{"id":"e1","name":"build-env","config":{"type":"cloud"},"build_status":"building","created_at":"2026-10-22T01:00:00Z","updated_at":"2026-10-22T01:00:00Z","scope":"project"}`
const environmentE1Ready = `{"id":"e1","name":"build-env","config":{"type":"cloud"},"build_status":"ready","created_at":"2026-10-22T01:00:00Z","updated_at":"2026-10-22T02:00:00Z","scope":"project"}`
const environmentE1Failed = `{"id":"e1","name":"build-env","config":{"type":"cloud"},"build_status":"failed","created_at":"2026-10-22T01:00:00Z","updated_at":"2026-10-22T02:00:00Z","scope":"project"}`

// TestEnvironmentWaitForReady polls through two building reads into the
// ready state returned in the HTTP result.
func TestEnvironmentWaitForReady(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(authWrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if hits.Add(1) < 3 {
			_, _ = io.WriteString(w, environmentE1Building)
			return
		}
		w.Header().Set("Msh-Request-Id", "req-env-ready")
		_, _ = io.WriteString(w, environmentE1Ready)
	})))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	envs := c.Environments

	ctx := context.Background()

	envResult, err := envs.Get(ctx, GetEnvironmentRequest{ID: "e1"})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	env := envResult.Body

	meta, err := c.Environments.WaitForReady(ctx, env.ID, WithEnvironmentInterval(time.Millisecond))
	if err != nil {
		t.Fatalf("WaitForReady: %v", err)
	}
	if got := hits.Load(); got != 3 {
		t.Fatalf("polls = %d, want 3 (initial Get plus two more reads)", got)
	}
	if meta.Body.BuildStatus == nil {
		t.Fatal("BuildStatus after wait is absent, want ready")
	}
	if *meta.Body.BuildStatus != EnvironmentBuildStatusReady {
		t.Fatalf("BuildStatus after wait = %q, want ready", *meta.Body.BuildStatus)
	}
	if meta.Header.Get("Msh-Request-Id") != "req-env-ready" {
		t.Fatalf("WaitForReady Meta = %+v, want the ready poll's header", meta)
	}
}

// TestEnvironmentWaitForReadyFailedBuild ends the wait with an
// *EnvironmentWaitFailedError while retaining the observed failed model.
func TestEnvironmentWaitForReadyFailedBuild(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(authWrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if hits.Add(1) == 1 {
			_, _ = io.WriteString(w, environmentE1Building)
			return
		}
		_, _ = io.WriteString(w, environmentE1Failed)
	})))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	envs := c.Environments

	ctx := context.Background()

	envResult, err := envs.Get(ctx, GetEnvironmentRequest{ID: "e1"})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	env := envResult.Body

	meta, err := c.Environments.WaitForReady(ctx, env.ID, WithEnvironmentInterval(time.Millisecond))
	if err == nil {
		t.Fatal("WaitForReady succeeded on a failed build")
	}
	if meta.Body == nil || pointerValue(meta.Body.BuildStatus) != EnvironmentBuildStatusFailed {
		t.Fatalf("result = %+v, want the failed build alongside its domain error", meta)
	}
	var wfe *EnvironmentWaitFailedError
	if !errors.As(err, &wfe) || wfe.ID != "e1" || wfe.Status != "failed" {
		t.Fatalf("error = %v, want EnvironmentWaitFailedError e1/failed", err)
	}
}

// TestEnvironmentWaitForReadyClientErrorAborts ends the wait on the first
// 4xx that is not 408 or 429.
func TestEnvironmentWaitForReadyClientErrorAborts(t *testing.T) {
	var pollHits atomic.Int32
	srv := httptest.NewServer(authWrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if pollHits.Load() == 0 && r.Method == http.MethodGet {
			pollHits.Add(1)
			_, _ = io.WriteString(w, environmentE1Building)
			return
		}
		pollHits.Add(1)
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"type":"not_found_error","message":"environment gone"}}`)
	})))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	envs := c.Environments

	ctx := context.Background()

	envResult, err := envs.Get(ctx, GetEnvironmentRequest{ID: "e1"})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	env := envResult.Body

	_, err = c.Environments.WaitForReady(ctx, env.ID, WithEnvironmentInterval(time.Millisecond))
	if !IsNotFound(err) {
		t.Fatalf("WaitForReady: %v, want the 404 APIError", err)
	}
	if got := pollHits.Load(); got != 2 {
		t.Fatalf("polls = %d, want 2 (initial Get plus one aborted poll)", got)
	}
}

// TestEnvironmentWaitForReadyProtocolErrorAborts fails fast when a poll
// returns 200 with a body the pinned contract cannot decode: the wait ends
// after exactly one request with the protocol error, not the deadline.
func TestEnvironmentWaitForReadyProtocolErrorAborts(t *testing.T) {
	var pollHits atomic.Int32
	srv := httptest.NewServer(authWrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pollHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"e1","build_status":`)
	})))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	env := &Environment{ID: "e1"}
	_, err := c.Environments.WaitForReady(context.Background(), env.ID, WithEnvironmentInterval(time.Millisecond), WithEnvironmentTimeout(time.Minute))
	if !IsProtocolError(err) {
		t.Fatalf("WaitForReady: %v, want a protocol error", err)
	}
	if got := pollHits.Load(); got != 1 {
		t.Fatalf("polls = %d, want 1 (deterministic decode failure must not be retried)", got)
	}
}

// TestEnvironmentWaitForReadyRetryAfterExtendsInterval stretches one poll
// interval to the server's Retry-After. The client is built without client
// retries so the 429 reaches the waiter directly.
func TestEnvironmentWaitForReadyRetryAfterExtendsInterval(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(authWrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if hits.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"type":"rate_limit_reached_error","message":"slow down"}}`)
			return
		}
		_, _ = io.WriteString(w, environmentE1Ready)
	})))
	t.Cleanup(srv.Close)

	c, err := New("good-key", WithBaseURL(srv.URL), WithMaxRetries(0))
	if err != nil {
		t.Fatal(err)
	}
	env := &Environment{ID: "e1"}

	start := time.Now()
	if _, err := c.Environments.WaitForReady(context.Background(), env.ID, WithEnvironmentInterval(time.Millisecond)); err != nil {
		t.Fatalf("WaitForReady: %v", err)
	}
	if elapsed := time.Since(start); elapsed < time.Second {
		t.Fatalf("wait ended after %v, want the Retry-After second to extend the 1ms interval", elapsed)
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("polls = %d, want 2 (429 then ready)", got)
	}
}

// TestEnvironmentWaitForReadyTimeout ends the wait with the context's
// deadline error while the build keeps reporting a non-terminal state.
func TestEnvironmentWaitForReadyTimeout(t *testing.T) {
	srv := httptest.NewServer(authWrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, environmentE1Building)
	})))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	env := &Environment{ID: "e1"}
	_, err := c.Environments.WaitForReady(context.Background(), env.ID,
		WithEnvironmentTimeout(30*time.Millisecond),
		WithEnvironmentInterval(time.Millisecond),
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitForReady: %v, want context.DeadlineExceeded", err)
	}
}
