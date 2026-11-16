package kha

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const triggerRunPending = `{"id":"run_1","trigger_id":"trig_1","status":"pending","created_at":"2026-10-22T03:00:00Z"}`

const (
	triggerRunCompleted = `{"id":"run_1","trigger_id":"trig_1","status":"completed","session_id":"s1","created_at":"2026-10-22T03:00:00Z","finished_at":"2026-10-22T03:05:00Z"}`
	triggerRunFailed    = `{"id":"run_1","trigger_id":"trig_1","status":"failed","session_id":"s1","error":{"type":"fire_failed","message":"boom"},"created_at":"2026-10-22T03:00:00Z","finished_at":"2026-10-22T03:04:00Z"}`
)

// TestTriggerRunWaitForTerminal fires a run and waits: a transient 5xx poll
// is retried inside the wait, which returns the completed run.
func TestTriggerRunWaitForTerminal(t *testing.T) {
	var runBody atomic.Value
	var polls atomic.Int32
	srv := httptest.NewServer(authWrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/triggers/trig_1/run":
			b, _ := io.ReadAll(r.Body)
			runBody.Store(string(b))
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, triggerRunPending)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/triggers/trig_1/runs/run_1":
			if polls.Add(1) == 1 {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(w, `{"error":{"type":"internal_error","message":"flaky"}}`)
				return
			}
			_, _ = io.WriteString(w, triggerRunCompleted)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"type":"not_found_error","message":"no such run"}}`)
		}
	})))
	t.Cleanup(srv.Close)

	// No client-level retries: the 5xx surfaces to the waiter, which must treat
	// it as transient.
	c, err := New("good-key", WithBaseURL(srv.URL), WithMaxRetries(0))
	if err != nil {
		t.Fatal(err)
	}
	svc := c.Triggers

	ctx := context.Background()

	runResult, err := svc.Run(ctx, RunTriggerHTTPRequest{ID: "trig_1", Body: &RunTriggerRequest{Payload: new("hello")}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	run := runResult.Body

	if run.Status != TriggerRunStatusPending {
		t.Fatalf("status = %s", run.Status)
	}
	if body, _ := runBody.Load().(string); !strings.Contains(body, `"payload":"hello"`) {
		t.Fatalf("posted body = %q", body)
	}

	doneResult, err := c.Triggers.WaitForTerminal(ctx, run.TriggerID, run.ID, WithInterval(time.Millisecond))
	if err != nil {
		t.Fatalf("WaitForTerminal: %v", err)
	}
	done := doneResult.Body

	if done.ID != run.ID {
		t.Fatal("WaitForTerminal returned another run")
	}
	if done.Status != TriggerRunStatusCompleted {
		t.Fatalf("final status = %s", done.Status)
	}
	if done.SessionID == nil {
		t.Fatal("session id is absent, want s1")
	}
	if *done.SessionID != "s1" {
		t.Fatalf("session id = %q, want s1", *done.SessionID)
	}
	if got := polls.Load(); got != 2 {
		t.Fatalf("polls = %d, want 2 (transient 5xx retried by the waiter)", got)
	}
}

// TestTriggerRunWaitAlreadyTerminal reads once when the run is already terminal.
func TestTriggerRunWaitAlreadyTerminal(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(authWrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, triggerRunCompleted)
	})))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	run := &TriggerRun{ID: "run_1", TriggerID: "trig_1"}
	doneResult, err := c.Triggers.WaitForTerminal(context.Background(), run.TriggerID, run.ID)
	if err != nil {
		t.Fatalf("WaitForTerminal: %v", err)
	}
	done := doneResult.Body

	if done.ID != run.ID || done.Status != TriggerRunStatusCompleted {
		t.Fatalf("done = %v/%s", done, done.Status)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("hits = %d, want one read of terminal state", got)
	}
}

// TestTriggerRunWaitFailedIsSuccess ends the wait successfully on a failed
// run: the failure is data on the snapshot, not an error.
func TestTriggerRunWaitFailedIsSuccess(t *testing.T) {
	srv := httptest.NewServer(authWrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, triggerRunFailed)
	})))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	run := &TriggerRun{ID: "run_1", TriggerID: "trig_1"}
	doneResult, err := c.Triggers.WaitForTerminal(context.Background(), run.TriggerID, run.ID, WithInterval(time.Millisecond))
	if err != nil {
		t.Fatalf("WaitForTerminal: %v", err)
	}
	done := doneResult.Body

	if done.Status != TriggerRunStatusFailed {
		t.Fatalf("status = %s", done.Status)
	}
	runErr := done.Error
	if runErr == nil || runErr.Type != "fire_failed" || runErr.Message != "boom" {
		t.Fatalf("error = %+v, want fire_failed: boom", runErr)
	}
}

// TestTriggerRunWait4xxStops ends the wait immediately with the *ResponseError[*NotFoundErrorResponse]
// when a poll fails 4xx.
func TestTriggerRunWait4xxStops(t *testing.T) {
	srv := httptest.NewServer(authWrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"type":"not_found_error","message":"run gone"}}`)
	})))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	run := &TriggerRun{ID: "run_1", TriggerID: "trig_1"}
	doneResult, err := c.Triggers.WaitForTerminal(context.Background(), run.TriggerID, run.ID, WithInterval(time.Millisecond))
	done := doneResult.Body
	if err == nil || done != nil {
		t.Fatalf("done = %v, err = %v", done, err)
	}
	if !IsNotFound(err) {
		t.Fatalf("err = %v, want not found", err)
	}
	if _, ok := errors.AsType[*Error](err); !ok {
		t.Fatalf("err = %v, want an *Error", err)
	}
}
