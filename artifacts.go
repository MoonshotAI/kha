package kha

import (
	"context"
	"iter"
)

// All lazily iterates all matching results.
func (s *ArtifactsClient) All(ctx context.Context, req ListArtifactsRequest) iter.Seq2[*Artifact, error] {
	return paginate(ctx, req.PageToken, func(ctx context.Context, token *string) ([]*Artifact, *string, error) {
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
func (s *ArtifactsClient) AllVersions(ctx context.Context, req ListArtifactVersionsRequest) iter.Seq2[*Artifact, error] {
	return paginate(ctx, req.PageToken, func(ctx context.Context, token *string) ([]*Artifact, *string, error) {
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
