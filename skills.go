package kha

import (
	"context"
	"iter"
)

// All lazily iterates all matching results.
func (s *SkillsClient) All(ctx context.Context, req ListSkillsRequest) iter.Seq2[*Skill, error] {
	return paginate(ctx, req.PageToken, func(ctx context.Context, token *string) ([]*Skill, *string, error) {
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
func (s *SkillsClient) AllVersions(ctx context.Context, req ListSkillVersionsRequest) iter.Seq2[*SkillVersion, error] {
	return paginate(ctx, req.PageToken, func(ctx context.Context, token *string) ([]*SkillVersion, *string, error) {
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
