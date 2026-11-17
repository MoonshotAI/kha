package kha

import (
	"io"
	mathrand "math/rand/v2"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// productTransport owns retry policy for clients constructed with New.
// Generated transport retries are disabled for those clients.
type productTransport struct {
	base       http.RoundTripper
	maxRetries int
	basePath   string
}

func (t *productTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	path := strings.TrimPrefix(req.URL.EscapedPath(), t.basePath)
	mediaType, _, _ := mime.ParseMediaType(req.Header.Get("Content-Type"))
	emptyBody := req.Body == nil || req.Body == http.NoBody
	protected := (emptyBody || mediaType == "application/json") &&
		req.Header.Get("Idempotency-Key") != "" &&
		protectedWrite(req.Method, path)
	replayable := emptyBody || req.GetBody != nil
	retries := 0
	if replayable && (protected || safeRead(req.Method, path)) {
		retries = t.maxRetries
	}
	current := req
	for attempt := 0; ; attempt++ {
		resp, err := t.base.RoundTrip(current)
		if attempt >= retries || !retryResponse(resp, err, protected, attempt) || req.Context().Err() != nil {
			return resp, err
		}
		delay := retryBackoff(attempt)
		if resp != nil {
			if after := retryAfter(resp.Header.Get("Retry-After")); after > 0 {
				delay = after
			}
			// Bound draining: a gateway's error stream must not delay cancellation.
			if resp.Body != nil {
				_, _ = io.CopyN(io.Discard, resp.Body, 64<<10)
				_ = resp.Body.Close()
			}
		}
		if !sleepCtx(req.Context(), delay) {
			return nil, req.Context().Err()
		}
		current = current.Clone(req.Context())
		if req.GetBody != nil {
			current.Body, err = req.GetBody()
			if err != nil {
				return nil, err
			}
		}
	}
}

func safeRead(method, path string) bool {
	if method != http.MethodGet && method != http.MethodHead {
		return false
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 4 || parts[0] != "v1" {
		return true
	}
	// Downloads are single requests; the event iterator owns SSE recovery.
	switch len(parts) {
	case 4:
		if (parts[1] == "files" || parts[1] == "artifacts") && parts[3] == "content" {
			return false
		}
	case 5:
		if parts[1] == "environments" && parts[3] == "work" && parts[4] == "poll" {
			return false
		}
		if parts[1] == "sessions" {
			if parts[3] == "events" && parts[4] == "stream" || parts[3] == "filesystem" && parts[4] == "raw" {
				return false
			}
		}
	case 6:
		if (parts[1] == "skills" || parts[1] == "plugins") && parts[3] == "versions" && parts[5] == "content" {
			return false
		}
	case 7:
		if parts[1] == "sessions" && parts[3] == "threads" && parts[5] == "events" && parts[6] == "stream" {
			return false
		}
	}
	return true
}

func protectedWrite(method, path string) bool {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if method == http.MethodPatch {
		return len(parts) == 3 && parts[0] == "v1" && parts[1] == "environments"
	}
	if method != http.MethodPost {
		return false
	}
	switch path {
	case "/v1/agents", "/v1/sessions", "/v1/environments", "/v1/vaults", "/v1/memory-stores", "/v1/triggers":
		return true
	}
	if len(parts) < 2 || parts[0] != "v1" {
		return false
	}
	switch {
	case len(parts) == 4 && parts[1] == "triggers" && parts[3] == "run":
		return true
	case len(parts) == 4 && parts[1] == "sessions" && (parts[3] == "events" || parts[3] == "resources"):
		return true
	case len(parts) == 4 && parts[1] == "vaults" && parts[3] == "credentials":
		return true
	case len(parts) == 5 && parts[1] == "vaults" && parts[3] == "credentials" && parts[4] == "batch":
		return true
	default:
		return false
	}
}

// retryResponse decides whether another attempt is warranted.
// Protected writes retry 409 only after the first attempt already failed:
// the server's idempotency middleware returns 409 while the original request
// is still in flight, then replays the stored response once it completes. A
// 409 on the first attempt is a real conflict (mismatched fingerprint or
// business rule) and must surface immediately.
func retryResponse(resp *http.Response, err error, protected bool, attempt int) bool {
	if err != nil {
		return true
	}
	if resp == nil {
		return false
	}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode >= 500:
		return true
	case protected && attempt > 0 && resp.StatusCode == http.StatusConflict:
		return true
	default:
		return false
	}
}

func retryAfter(value string) time.Duration {
	const limit = time.Minute
	value = strings.TrimSpace(value)
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds <= 0 {
			return 0
		}
		if seconds >= int64(limit/time.Second) {
			return limit
		}
		return time.Duration(seconds) * time.Second
	}
	if until, err := http.ParseTime(value); err == nil {
		return min(max(time.Until(until), 0), limit)
	}
	return 0
}

func retryBackoff(attempt int) time.Duration {
	wait := min(time.Second<<min(attempt, 5), 30*time.Second)
	return wait + time.Duration(mathrand.Float64()*float64(wait)/4) // #nosec G404 -- Retry jitter does not require cryptographic randomness.
}
