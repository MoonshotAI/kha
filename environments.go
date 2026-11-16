package kha

import (
	"context"
	"fmt"
	"iter"
	"time"
)

// All lazily iterates all matching results.
func (s *EnvironmentsClient) All(ctx context.Context, req ListEnvironmentsRequest) iter.Seq2[*Environment, error] {
	return paginate(ctx, req.PageToken, func(ctx context.Context, token *string) ([]*Environment, *string, error) {
		request := req
		request.PageToken = token
		resp, err := s.List(ctx, request)
		if err != nil {
			return nil, nil, err
		}
		page := resp.Body
		return page.Items, page.NextPageToken, nil
	})
}

// Wait cadence and bounds of EnvironmentsClient.WaitForReady: the whole wait is a
// context deadline (default 10 minutes) returning ctx's error on expiry,
// and polls run on a fixed interval (default 2 seconds — first poll
// immediately, no jitter, no backoff; Retry-After may extend an interval).
const (
	defaultEnvironmentWaitTimeout  = 10 * time.Minute
	defaultEnvironmentWaitInterval = 2 * time.Second
)

// EnvironmentWaitOption customizes EnvironmentsClient.WaitForReady.
type EnvironmentWaitOption func(*environmentWaitConfig)

// environmentWaitConfig accumulates EnvironmentWaitOption values.
type environmentWaitConfig struct {
	timeout  time.Duration
	interval time.Duration
}

// WithEnvironmentTimeout bounds the whole wait (default 10 minutes),
// counted from the first poll. The timeout is a context deadline: on
// expiry the returned error is context.DeadlineExceeded, and cancelling
// the caller's ctx always ends the wait immediately. Non-positive values
// fall back to the default.
func WithEnvironmentTimeout(d time.Duration) EnvironmentWaitOption {
	return func(c *environmentWaitConfig) { c.timeout = d }
}

// WithEnvironmentInterval sets the cadence between polls (default 2
// seconds); the first poll always runs immediately. Non-positive values
// fall back to the default.
func WithEnvironmentInterval(d time.Duration) EnvironmentWaitOption {
	return func(c *environmentWaitConfig) { c.interval = d }
}

// EnvironmentWaitFailedError reports that EnvironmentsClient.WaitForReady watched
// the build reach a terminal failure state. It is a domain outcome, not an
// HTTP error: it never carries a status code.
type EnvironmentWaitFailedError struct {
	// ID identifies the failed Environment.
	ID string `json:"id"`
	// Status is the terminal build status observed (e.g. "failed"), an
	// open vocabulary kept verbatim.
	Status string `json:"status"`
}

// Error implements error.
func (e *EnvironmentWaitFailedError) Error() string {
	return fmt.Sprintf("kha: environment %s reached terminal status %q", e.ID, e.Status)
}

// WaitForReady polls until the latest Environment build is ready or fails.
func (s *EnvironmentsClient) WaitForReady(ctx context.Context, id string, opts ...EnvironmentWaitOption) (HTTPResult[*Environment], error) {
	if id == "" {
		return HTTPResult[*Environment]{}, &InvalidCallError{Reason: "environment id must not be empty"}
	}
	cfg := environmentWaitConfig{timeout: defaultEnvironmentWaitTimeout, interval: defaultEnvironmentWaitInterval}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.timeout <= 0 {
		cfg.timeout = defaultEnvironmentWaitTimeout
	}
	if cfg.interval <= 0 {
		cfg.interval = defaultEnvironmentWaitInterval
	}
	return poll(ctx, waitConfig(cfg), func(ctx context.Context) (HTTPResult[*Environment], error) {
		return s.Get(ctx, GetEnvironmentRequest{ID: id})
	}, func(result HTTPResult[*Environment]) (bool, error) {
		status := result.Body.BuildStatus
		if status == nil {
			return false, nil
		}
		switch *status {
		case EnvironmentBuildStatusReady:
			return true, nil
		case EnvironmentBuildStatusFailed:
			return true, &EnvironmentWaitFailedError{ID: id, Status: string(*status)}
		}
		return false, nil
	})
}
