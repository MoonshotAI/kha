// Package worker runs self-hosted Environment work items. It acknowledges work,
// maintains leases while consuming Session events and executing local tools, then
// settles or abandons work without overwriting a newer owner.
package worker

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	mrand "math/rand/v2"
	"net/http"
	"time"

	kha "github.com/MoonshotAI/kha"
)

// ErrLeaseLost reports a lost lease or invalid worker credentials (401/412).
// The worker must abandon the work without executing tools or overwriting a newer owner.
var ErrLeaseLost = errors.New("worker lease lost")

const (
	pollBackoffInitial = time.Second
	pollBackoffMax     = 30 * time.Second
	// worker run accepts no local lease parameter, so use the platform default
	// until the first Heartbeat response provides ttl_seconds.
	defaultLeaseTTL      = 300 * time.Second
	minHeartbeatInterval = 50 * time.Millisecond
	heartbeatQuickRetry  = 100 * time.Millisecond
	// After Stop settlement retries expire, heartbeats stop and the sweeper
	// reclaims the work.
	settleWindow  = 15 * time.Second
	eventPageSize = 200
	// Warn on this cap to expose an unconsumed event gap.
	maxEventBacktrackPages = 10
)

// NewWorkerID returns an identity that is not reused across process restarts.
func NewWorkerID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("wk-%d", time.Now().UnixNano())
	}
	return "wk-" + hex.EncodeToString(b[:])
}

func statusCode(err error) int {
	if apiErr, ok := errors.AsType[kha.HTTPError](err); ok {
		return apiErr.StatusCode()
	}
	return 0
}

// A 401/412 requires immediate abandonment to preserve lease fencing.
func isLeaseLost(err error) bool {
	code := statusCode(err)
	return code == http.StatusUnauthorized || code == http.StatusPreconditionFailed
}

// A 409 rejects idle settlement while the Session is busy; the lease remains valid.
func isNotDormant(err error) bool {
	return statusCode(err) == http.StatusConflict
}

// Network errors, 408, 429, and 5xx are retryable. Lease loss, other 4xx statuses,
// and deterministic protocol errors are terminal.
func retryable(err error) bool {
	if isLeaseLost(err) || kha.IsProtocolError(err) {
		return false
	}
	code := statusCode(err)
	if code == 0 || code >= 200 && code < 300 {
		return true // network-layer error or interrupted read of a successful response
	}
	return code >= 500 ||
		code == http.StatusRequestTimeout ||
		code == http.StatusTooManyRequests
}

func wait(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// retryWithWindow bounds exponential-backoff retries and preserves the last
// failure when the window expires. Caller cancellation or timeout takes precedence.
func retryWithWindow(ctx context.Context, window, baseDelay, maxDelay time.Duration, op func(context.Context) error) error {
	retryCtx, cancel := context.WithTimeout(ctx, window)
	defer cancel()
	var lastErr error
	for delay := baseDelay; ; delay = min(delay*2, maxDelay) {
		err := op(retryCtx)
		if err == nil || !retryable(err) {
			return err
		}
		// When the window cancels an in-flight request, retain the previous send
		// failure; if the first request hangs, record the timeout it returns.
		if lastErr == nil || !errors.Is(err, retryCtx.Err()) {
			lastErr = err
		}
		if err := wait(retryCtx, jitter(delay)); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return &windowExhaustedError{window: window, lastErr: lastErr}
		}
	}
}

type windowExhaustedError struct {
	window  time.Duration
	lastErr error
}

func (e *windowExhaustedError) Error() string {
	return fmt.Sprintf("window (%s) exhausted: %v", e.window, e.lastErr)
}

func (e *windowExhaustedError) Unwrap() []error {
	return []error{context.DeadlineExceeded, e.lastErr}
}

// jitter returns a random duration in [0.75d, 1.25d) to desynchronize retries
// across workers.
func jitter(d time.Duration) time.Duration {
	return d*3/4 + time.Duration(mrand.Int64N(int64(d/2)+1)) // #nosec G404 -- backoff jitter does not require cryptographic randomness
}

// Only sessions_token is part of the worker contract; other bundle members are
// reserved for platform extensions.
type secretBundle struct {
	SessionsToken string `json:"sessions_token"`
}

// Malformed or incomplete deliveries are dropped for reclaim and redelivery.
func decodeSessionsToken(secret *string) (string, error) {
	if secret == nil || *secret == "" {
		return "", errors.New("delivery carries no secret")
	}
	s := *secret
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return "", fmt.Errorf("decode secret bundle: %w", err)
	}
	var b secretBundle
	if err := json.Unmarshal(raw, &b); err != nil {
		return "", fmt.Errorf("parse secret bundle: %w", err)
	}
	if b.SessionsToken == "" {
		return "", errors.New("secret bundle carries no sessions_token")
	}
	return b.SessionsToken, nil
}
