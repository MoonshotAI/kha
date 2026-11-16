package worker

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"testing"

	jsonv2 "encoding/json/v2"

	kha "github.com/MoonshotAI/kha"
)

func TestRetryable(t *testing.T) {
	var page struct {
		Items []string `json:"items"`
	}
	decodeErr := jsonv2.Unmarshal([]byte(`{"items":"not-an-array"}`), &page)
	if decodeErr == nil {
		t.Fatal("fixture must produce a deterministic decode error")
	}
	protocolDecodeErr := kha.NewUnexpectedResponseError(http.StatusOK, nil, "decode", nil, "", decodeErr)

	for _, tt := range []struct {
		name string
		err  error
		want bool
	}{
		{name: "invalid call error", err: &kha.InvalidCallError{Reason: "session id is required"}},
		{name: "wrapped invalid call error", err: fmt.Errorf("heartbeat: %w", &kha.InvalidCallError{Reason: "session id is required"})},
		{name: "2xx contract decode error", err: protocolDecodeErr},
		{name: "interrupted success body", err: kha.NewUnexpectedResponseError(http.StatusOK, nil, "read", nil, "", io.ErrUnexpectedEOF), want: true},
		{name: "bad request", err: sdkHTTPError(http.StatusBadRequest, nil)},
		{name: "unauthorized is lease lost", err: sdkHTTPError(http.StatusUnauthorized, nil)},
		{name: "precondition failed is lease lost", err: sdkHTTPError(http.StatusPreconditionFailed, nil)},
		{name: "request timeout", err: sdkHTTPError(http.StatusRequestTimeout, nil), want: true},
		{name: "too many requests", err: sdkHTTPError(http.StatusTooManyRequests, nil), want: true},
		{name: "internal server error", err: sdkHTTPError(http.StatusInternalServerError, nil), want: true},
		{name: "bad gateway", err: sdkHTTPError(http.StatusBadGateway, nil), want: true},
		{name: "empty unauthorized body", err: kha.NewUnexpectedResponseError(http.StatusUnauthorized, nil, "status", nil, "", nil)},
		{name: "undecodable lease loss", err: kha.NewUnexpectedResponseError(http.StatusPreconditionFailed, nil, "status", nil, "", nil)},
		{name: "undecodable bad request", err: kha.NewUnexpectedResponseError(http.StatusBadRequest, nil, "status", nil, "", nil)},
		{name: "undecodable rate limit", err: kha.NewUnexpectedResponseError(http.StatusTooManyRequests, nil, "status", nil, "", nil), want: true},
		{name: "proxy failure", err: kha.NewUnexpectedResponseError(http.StatusBadGateway, nil, "status", nil, "", nil), want: true},
		{name: "network error", err: errors.New("dial tcp: connection refused"), want: true},
		{name: "wrapped network error", err: fmt.Errorf("poll: %w", errors.New("connection reset by peer")), want: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := retryable(tt.err); got != tt.want {
				t.Fatalf("retryable(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestUndecodableLeaseStatus(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusPreconditionFailed} {
		err := fmt.Errorf("request: %w", kha.NewUnexpectedResponseError(status, nil, "status", nil, "", nil))
		if !isLeaseLost(err) {
			t.Fatalf("status %d was not treated as lease loss", status)
		}
	}
	if !isNotDormant(kha.NewUnexpectedResponseError(http.StatusConflict, nil, "status", nil, "", nil)) {
		t.Fatal("undecodable conflict lost the settlement guard status")
	}
}
