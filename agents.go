package kha

import (
	"context"
	"iter"
)

// All lazily iterates all matching results.
func (s *AgentsClient) All(ctx context.Context, req ListAgentsRequest) iter.Seq2[*Agent, error] {
	return paginate(ctx, req.PageToken, func(ctx context.Context, token *string) ([]*Agent, *string, error) {
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

// AllVersions lazily iterates all matching results.
func (s *AgentsClient) AllVersions(ctx context.Context, req ListAgentVersionsRequest) iter.Seq2[*Agent, error] {
	return paginate(ctx, req.PageToken, func(ctx context.Context, token *string) ([]*Agent, *string, error) {
		request := req
		request.PageToken = token
		resp, err := s.ListVersions(ctx, request)
		if err != nil {
			return nil, nil, err
		}
		page := resp.Body
		return page.Items, page.NextPageToken, nil
	})
}
