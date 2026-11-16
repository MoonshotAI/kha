package kha

import (
	"context"
	"iter"
)

// All lazily iterates all matching results.
func (s *PluginsClient) All(ctx context.Context, req ListPluginsRequest) iter.Seq2[*Plugin, error] {
	return paginate(ctx, req.PageToken, func(ctx context.Context, token *string) ([]*Plugin, *string, error) {
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
func (s *PluginsClient) AllVersions(ctx context.Context, req ListPluginVersionsRequest) iter.Seq2[*PluginVersion, error] {
	return paginate(ctx, req.PageToken, func(ctx context.Context, token *string) ([]*PluginVersion, *string, error) {
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
