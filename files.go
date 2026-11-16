package kha

import (
	"context"
	"iter"
)

// All lazily iterates all matching results.
func (s *FilesClient) All(ctx context.Context, req ListFilesRequest) iter.Seq2[*FileMetadata, error] {
	return paginate(ctx, req.PageToken, func(ctx context.Context, token *string) ([]*FileMetadata, *string, error) {
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
