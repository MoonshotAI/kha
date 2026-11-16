package creds

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoonshotAI/kha"
)

func TestNewClientEmptySecret(t *testing.T) {
	if _, err := NewClient("http://127.0.0.1", ""); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Errorf("NewClient: err = %v, want it to say the credential is empty", err)
	}
}

func TestNewClientSendsBearerSecret(t *testing.T) {
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"depth":1,"claimed":0,"workers":0}`)
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, "ewk-secret")
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.WorkItems.Stats(context.Background(), kha.GetWorkItemStatsRequest{EnvironmentID: "env_1"})
	if err != nil {
		t.Fatal(err)
	}
	stats := result.Body
	if gotAuth != "Bearer ewk-secret" {
		t.Errorf("Authorization = %q, want Bearer ewk-secret", gotAuth)
	}
	if gotPath != "/v1/environments/env_1/work/stats" {
		t.Errorf("path = %q, want the env work stats endpoint", gotPath)
	}
	if stats.Depth != 1 {
		t.Errorf("stats.Depth = %d, want 1", stats.Depth)
	}
}
