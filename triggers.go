package kha

import (
	"context"
	"iter"
)

// All lazily iterates all matching results.
func (s *TriggersClient) All(ctx context.Context, req ListTriggersRequest) iter.Seq2[*Trigger, error] {
	return paginate(ctx, req.PageToken, func(ctx context.Context, token *string) ([]*Trigger, *string, error) {
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

// AllRuns lazily iterates all matching results.
func (s *TriggersClient) AllRuns(ctx context.Context, req ListTriggerRunsRequest) iter.Seq2[*TriggerRun, error] {
	return paginate(ctx, req.PageToken, func(ctx context.Context, token *string) ([]*TriggerRun, *string, error) {
		request := req
		request.PageToken = token
		resp, err := s.ListRuns(ctx, request)
		if err != nil {
			return nil, nil, err
		}
		page := resp.Body
		return page.Items, page.NextPageToken, nil
	})
}

// terminalTriggerRunStatus reports whether s is a state the run never
// leaves. TriggerRunStatus is an open vocabulary: the three values known to
// this SDK are terminal, anything else keeps the wait polling.
func terminalTriggerRunStatus(s TriggerRunStatus) bool {
	switch s {
	case TriggerRunStatusCompleted,
		TriggerRunStatusFailed,
		TriggerRunStatusSkipped:
		return true
	}
	return false
}

// WaitForTerminal polls a Trigger run until completed, failed, or skipped.
// Every terminal state is returned as data; failed runs are not HTTP errors.
func (s *TriggersClient) WaitForTerminal(ctx context.Context, id, runID string, opts ...WaitOption) (HTTPResult[*TriggerRun], error) {
	if id == "" || runID == "" {
		return HTTPResult[*TriggerRun]{}, &InvalidCallError{Reason: "trigger id and run id must not be empty"}
	}
	return poll(ctx, newWaitConfig(opts), func(ctx context.Context) (HTTPResult[*TriggerRun], error) {
		return s.GetRun(ctx, GetTriggerRunRequest{ID: id, RunID: runID})
	}, func(result HTTPResult[*TriggerRun]) (bool, error) {
		return terminalTriggerRunStatus(result.Body.Status), nil
	})
}
