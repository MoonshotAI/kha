package kha

import (
	"context"
	"iter"
)

// All lazily iterates all matching results.
func (s *VaultsClient) All(ctx context.Context, req ListVaultsRequest) iter.Seq2[*Vault, error] {
	return paginate(ctx, req.PageToken, func(ctx context.Context, token *string) ([]*Vault, *string, error) {
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

// AllCredentials lazily iterates all matching results.
func (s *VaultsClient) AllCredentials(ctx context.Context, req ListVaultCredentialsRequest) iter.Seq2[*VaultCredential, error] {
	return paginate(ctx, req.PageToken, func(ctx context.Context, token *string) ([]*VaultCredential, *string, error) {
		request := req
		request.PageToken = token
		resp, err := s.ListCredentials(ctx, request)
		if err != nil {
			return nil, nil, err
		}
		page := resp.Body
		return page.Items, page.NextPageToken, nil
	})
}

// WaitOAuthSession polls until the session is authorized or failed. A failed session is returned alongside its WaitFailedError.
func (s *VaultsClient) WaitOAuthSession(ctx context.Context, id string, sessionID string, opts ...WaitOption) (HTTPResult[*VaultOAuthSession], error) {
	if id == "" || sessionID == "" {
		return HTTPResult[*VaultOAuthSession]{}, &InvalidCallError{Reason: "vault id and OAuth session id must not be empty"}
	}
	return poll(ctx, newWaitConfig(opts), func(ctx context.Context) (HTTPResult[*VaultOAuthSession], error) {
		return s.GetOAuthSession(ctx, GetVaultOAuthSessionRequest{ID: id, SessionID: sessionID})
	}, func(result HTTPResult[*VaultOAuthSession]) (bool, error) {
		sess := result.Body
		switch sess.Status {
		case VaultOAuthSessionStatusAuthorized:
			return true, nil
		case VaultOAuthSessionStatusFailed:
			err := &WaitFailedError{Resource: "vault oauth session", ID: sessionID, Status: string(sess.Status)}
			if sess.Error != nil {
				err.Reason = *sess.Error
			}
			return true, err
		}
		return false, nil
	})
}
