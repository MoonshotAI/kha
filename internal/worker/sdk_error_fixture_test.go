package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"

	kha "github.com/MoonshotAI/kha"
)

type errorFixtureTransport func(*http.Request) (*http.Response, error)

func (f errorFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// sdkHTTPError obtains a real generated error without opening a network connection.
func sdkHTTPError(status int, body *kha.APIError) error {
	if body == nil {
		body = &kha.APIError{Error: kha.ErrorObject{Type: "test_error", Message: "fixture failure"}}
	}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	client, err := kha.NewClient("http://kha.test", kha.WithMaxRetries(0), kha.WithHTTPClient(&http.Client{
		Transport: errorFixtureTransport(func(req *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(bytes.NewReader(data)), Request: req}, nil
		}),
	}))
	if err != nil {
		return err
	}
	_, err = client.Agents.Get(context.Background(), kha.GetAgentRequest{ID: "fixture"})
	return err
}
