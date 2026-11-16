package kha

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Error is the typed HTTP response error returned for declared API failures.
type Error = ResponseError[*APIError]

// String formats the decoded Hosted Agents error payload for ResponseError.Error().
func (e *APIError) String() string {
	if e == nil {
		return ""
	}
	var b strings.Builder
	d := e.Error
	if d.Type != "" {
		b.WriteString(string(d.Type))
	}
	if d.Message != "" {
		if b.Len() > 0 {
			b.WriteString(": ")
		}
		b.WriteString(d.Message)
	}
	if d.Param != nil && *d.Param != "" {
		fmt.Fprintf(&b, " (param: %s)", *d.Param)
	}
	if d.RequestID != nil && *d.RequestID != "" {
		fmt.Fprintf(&b, " (request id: %s)", *d.RequestID)
	}
	return b.String()
}

// InvalidCallError marks a deterministic caller-side or contract violation:
// an empty required argument, or a response whose shape no longer matches
// the pinned spec. Retrying it cannot succeed, so waiters and pollers must
// fail fast on it instead of treating it as a transient network fault.
type InvalidCallError struct {
	Reason string
}

// Error implements error.
func (e *InvalidCallError) Error() string { return "kha: " + e.Reason }

// IsProtocolError reports whether err is a deterministic protocol violation
// that retrying cannot fix: an InvalidCallError raised by the SDK, or a 2xx
// response outside the pinned contract, invalid stream data, or generated
// request validation. A body cut short mid-read is a transport fault;
// non-2xx error pages are classified by their HTTP status. Waiters, pollers
// and stream recovery must fail fast on protocol errors.
func IsProtocolError(err error) bool {
	if _, ok := errors.AsType[*InvalidCallError](err); ok {
		return true
	}
	if _, ok := errors.AsType[ValidationErrors](err); ok {
		return true
	}
	if _, ok := errors.AsType[*StreamDecodeError](err); ok {
		return true
	}
	return isDeterministicDecodeError(err)
}

// isDeterministicDecodeError reports a successful HTTP response whose
// status, content type, or JSON conflicts with the pinned contract.
func isDeterministicDecodeError(err error) bool {
	var unexp *UnexpectedResponseError
	if !errors.As(err, &unexp) || unexp.StatusCode() < 200 || unexp.StatusCode() >= 300 {
		return false
	}
	return unexp.Kind != "read"
}

// ErrorDetails extracts the decoded Hosted Agents error envelope.
func ErrorDetails(err error) (*ErrorObject, bool) {
	if stream, ok := errors.AsType[*StreamError](err); ok {
		return &stream.Details, true
	}
	if apiErr, ok := errors.AsType[*Error](err); ok && apiErr.Body != nil && apiErr.Body.Error.Type != "" {
		return &apiErr.Body.Error, true
	}
	return nil, false
}

// StreamError is a terminal error event after the SSE response has started.
// It has no HTTP status; the successful handshake has already completed.
type StreamError struct{ Details ErrorObject }

func (e *StreamError) Error() string {
	return "kha: stream error: " + string(e.Details.Type) + ": " + e.Details.Message
}

// IsNotFound reports whether an operation returned HTTP 404.
func IsNotFound(err error) bool { return hasStatus(err, http.StatusNotFound) }

// IsUnauthorized reports whether credentials were rejected with HTTP 401.
func IsUnauthorized(err error) bool { return hasStatus(err, http.StatusUnauthorized) }

// IsForbidden reports whether an operation returned HTTP 403.
func IsForbidden(err error) bool { return hasStatus(err, http.StatusForbidden) }

// IsInvalidRequest reports whether an operation returned HTTP 400.
func IsInvalidRequest(err error) bool { return hasStatus(err, http.StatusBadRequest) }

// IsConflict reports whether an operation returned HTTP 409.
func IsConflict(err error) bool { return hasStatus(err, http.StatusConflict) }

// IsRateLimited reports whether an operation returned HTTP 429.
func IsRateLimited(err error) bool { return hasStatus(err, http.StatusTooManyRequests) }

// IsServerError reports whether an operation returned HTTP 5xx.
func IsServerError(err error) bool {
	status, _, ok := httpErrorMetadata(err)
	return ok && status >= 500 && status < 600
}

func hasStatus(err error, status int) bool {
	actual, _, ok := httpErrorMetadata(err)
	return ok && actual == status
}

// httpErrorMetadata recognizes both decoded API errors and UnexpectedResponseError via HTTPError.
func httpErrorMetadata(err error) (int, http.Header, bool) {
	if response, ok := errors.AsType[HTTPError](err); ok {
		return response.StatusCode(), response.Header(), true
	}
	return 0, nil, false
}

// isResponseReadError reports an interrupted successful response body.
// Non-2xx failures remain classified by status, even if their body was cut short.
func isResponseReadError(err error) bool {
	response, ok := errors.AsType[*UnexpectedResponseError](err)
	return ok && response.Kind == "read" && response.StatusCode() >= 200 && response.StatusCode() < 300
}
