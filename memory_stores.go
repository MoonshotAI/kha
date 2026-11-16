package kha

import (
	"context"
	"iter"
)

// All lazily iterates all matching results.
func (s *MemoryStoresClient) All(ctx context.Context, req ListMemoryStoresRequest) iter.Seq2[*MemoryStore, error] {
	return paginate(ctx, req.PageToken, func(ctx context.Context, token *string) ([]*MemoryStore, *string, error) {
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

// AllMemories lazily iterates all matching results.
func (s *MemoryStoresClient) AllMemories(ctx context.Context, req ListMemoriesRequest) iter.Seq2[*Memory, error] {
	return paginate(ctx, req.PageToken, func(ctx context.Context, token *string) ([]*Memory, *string, error) {
		request := req
		request.PageToken = token
		resp, err := s.ListMemories(ctx, request)
		if err != nil {
			return nil, nil, err
		}
		page := resp.Body
		return page.Items, page.NextPageToken, nil
	})
}

// AllMemoryVersions lazily iterates all matching results.
func (s *MemoryStoresClient) AllMemoryVersions(ctx context.Context, req ListMemoryVersionsRequest) iter.Seq2[*MemoryVersion, error] {
	return paginate(ctx, req.PageToken, func(ctx context.Context, token *string) ([]*MemoryVersion, *string, error) {
		request := req
		request.PageToken = token
		resp, err := s.ListMemoryVersions(ctx, request)
		if err != nil {
			return nil, nil, err
		}
		page := resp.Body
		return page.Items, page.NextPageToken, nil
	})
}
