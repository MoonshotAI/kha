package kha

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

const vaultOAuthFlowF1 = `{"oauth_session_id":"os1","authorize_url":"https://auth.example.com/authorize?x=1","mcp_server_url":"https://mcp.example.com"}`

// TestVaultOAuthWaitForAuthorized starts a flow through the service, then
// polls past transient 500s and a pending state to
// the authorized terminal state. The synctest bubble clock makes the
// protocol client's retry backoff and the wait's poll cadence instant.
func TestVaultOAuthWaitForAuthorized(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var polls atomic.Int32
		srv := httptest.NewTestServer(t, authWrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch {
			case r.Method == http.MethodPost && r.URL.Path == "/v1/vaults/v1/oauth-sessions":
				w.WriteHeader(http.StatusCreated)
				_, _ = io.WriteString(w, vaultOAuthFlowF1)
			case r.Method == http.MethodGet && r.URL.Path == "/v1/vaults/v1/oauth-sessions/os1":
				// Four 500s exhaust the protocol client's GET retry budget
				// (3 retries), so the waiter's own transient-error
				// tolerance drives the fifth fetch; then pending, then
				// authorized.
				switch n := polls.Add(1); {
				case n <= 4:
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = io.WriteString(w, `{"error":{"type":"api_error","message":"boom"}}`)
				case n == 5:
					_, _ = io.WriteString(w, `{"status":"pending"}`)
				default:
					_, _ = io.WriteString(w, `{"status":"authorized","credential_id":"c9"}`)
				}
			default:
				w.WriteHeader(http.StatusNotFound)
				_, _ = io.WriteString(w, `{"error":{"type":"not_found_error","message":"no such oauth session"}}`)
			}
		})))

		c, err := New("good-key", WithBaseURL(srv.URL), WithHTTPClient(srv.Client()), WithMaxRetries(3))
		if err != nil {
			t.Fatal(err)
		}
		vaults := c.Vaults

		ctx := t.Context()

		flowResult, err := vaults.CreateOAuthSession(ctx, CreateVaultOAuthSessionHTTPRequest{ID: "v1", Body: &CreateVaultOAuthSessionRequest{MCPServerURL: "https://mcp.example.com"}})
		if err != nil {
			t.Fatalf("CreateOAuthSession: %v", err)
		}
		flow := flowResult.Body

		if flow.OauthSessionID != "os1" || flow.AuthorizeURL != "https://auth.example.com/authorize?x=1" {
			t.Fatalf("flow = %q / %q", flow.OauthSessionID, flow.AuthorizeURL)
		}
		if flow.MCPServerURL != "https://mcp.example.com" {
			t.Fatalf("snapshot = %+v", flow)
		}

		sessResult, err := c.Vaults.WaitOAuthSession(ctx, "v1", flow.OauthSessionID, WithInterval(time.Second), WithTimeout(10*time.Minute))
		if err != nil {
			t.Fatalf("WaitForAuthorized: %v", err)
		}
		sess := sessResult.Body

		if got := polls.Load(); got != 6 {
			t.Fatalf("polls = %d, want 6 (4 exhausted retries, pending, authorized)", got)
		}
		if sess.CredentialID == nil || *sess.CredentialID != "c9" {
			t.Fatalf("session = %+v", sess)
		}
	})
}

// TestVaultOAuthWaitFailed drives the by-id waiter to a failed terminal
// state and decodes the WaitFailedError.
func TestVaultOAuthWaitFailed(t *testing.T) {
	srv := httptest.NewServer(authWrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/v1/vaults/v1/oauth-sessions/os2" {
			_, _ = io.WriteString(w, `{"status":"failed","error":"user denied access"}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"type":"not_found_error","message":"no such oauth session"}}`)
	})))
	t.Cleanup(srv.Close)

	vaults := newTestClient(t, srv).Vaults

	sessResult, err := vaults.WaitOAuthSession(context.Background(), "v1", "os2", WithInterval(time.Millisecond), WithTimeout(10*time.Second))
	sess := sessResult.Body
	if sess == nil || sess.Status != VaultOAuthSessionStatusFailed {
		t.Fatalf("session = %+v, want the failed session alongside the domain error", sess)
	}
	var waitErr *WaitFailedError
	if !errors.As(err, &waitErr) {
		t.Fatalf("err = %v (%T), want *WaitFailedError", err, err)
	}
	if waitErr.Resource != "vault oauth session" || waitErr.ID != "os2" || waitErr.Status != "failed" || waitErr.Reason != "user denied access" {
		t.Fatalf("WaitFailedError = %+v", waitErr)
	}
}

// TestVaultOAuthWait4xx ends the wait immediately on a 4xx poll response:
// no retry, the typed ResponseError surfaces.
func TestVaultOAuthWait4xx(t *testing.T) {
	var polls atomic.Int32
	srv := httptest.NewServer(authWrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		polls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"type":"not_found_error","message":"no such oauth session"}}`)
	})))
	t.Cleanup(srv.Close)

	flow := &CreateVaultOAuthSessionResponse{OauthSessionID: "os3"}
	c := newTestClient(t, srv)
	sessResult, err := c.Vaults.WaitOAuthSession(context.Background(), "v1", flow.OauthSessionID, WithInterval(time.Millisecond), WithTimeout(10*time.Second))
	sess := sessResult.Body
	if sess != nil {
		t.Fatalf("session = %+v, want nil on failure", sess)
	}
	if !IsNotFound(err) {
		t.Fatalf("err = %v, want not found", err)
	}
	if got := polls.Load(); got != 1 {
		t.Fatalf("polls = %d, want 1 (4xx ends the wait)", got)
	}
}
