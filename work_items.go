package kha

import (
	"context"
	"iter"
)

// All lazily iterates all matching results.
func (s *WorkItemsClient) All(ctx context.Context, req ListWorkRequest) iter.Seq2[*WorkItem, error] {
	return paginate(ctx, req.PageToken, func(ctx context.Context, token *string) ([]*WorkItem, *string, error) {
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
