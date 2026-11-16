// Package creds loads worker machine credentials and constructs SDK clients.
// Environment work keys (ewk-) access only their Environment's work queue;
// Session work tokens (swt-) access their Session's lifecycle and data endpoints.
// Secrets stay in process memory or caller-provided files and are sent as Bearer
// tokens, never written to logs, errors, or KHA profiles.
package creds

import (
	"errors"

	kha "github.com/MoonshotAI/kha"
)

// NewClient constructs an SDK client from an explicit worker machine credential;
// it never falls back to KIMI_API_KEY.
func NewClient(baseURL, secret string, opts ...kha.ClientOption) (*kha.Client, error) {
	if secret == "" {
		return nil, errors.New("worker credential is empty")
	}
	if baseURL != "" {
		opts = append([]kha.ClientOption{kha.WithBaseURL(baseURL)}, opts...)
	}
	return kha.New(secret, opts...)
}
