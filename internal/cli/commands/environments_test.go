package commands

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
)

func TestCreateSelfHostedEnvironment(t *testing.T) {
	for _, input := range []string{"flag", "file"} {
		t.Run(input, func(t *testing.T) {
			isolateEnv(t)
			synctest.Test(t, func(t *testing.T) {
				var body string
				srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodPost {
						raw, _ := io.ReadAll(r.Body)
						body = string(raw)
						w.WriteHeader(http.StatusCreated)
					}
					_, _ = fmt.Fprint(w, `{"id":"env_1","name":"runner","config":{"type":"self_hosted"},"build_status":"ready","created_at":"2026-10-12T00:00:00Z"}`)
				})
				args := []string{"environments", "create", "--wait", "--base-url", srv.URL}
				stdin := ""
				if input == "flag" {
					args = append(args, "--name", "runner", "--self-hosted")
				} else {
					args = append(args, "--file", "-")
					stdin = `{"name":"runner","config":{"type":"self_hosted"}}`
				}
				out, stderr, err := runCmd(t, stdin, args...)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(body, `"type":"self_hosted"`) || !strings.Contains(out, "env_1") {
					t.Fatal("wrong create request or response", body, out)
				}
				if strings.Contains(stderr, "build started") {
					t.Fatal("self-hosted environment advertised a build")
				}
			})
		})
	}
}

func TestSelfHostedFileConflict(t *testing.T) {
	isolateEnv(t)
	_, _, err := runCmd(t, `{}`, "environments", "create", "--self-hosted", "--file", "-")
	if exitCode(err) != 2 {
		t.Fatal("expected mutually exclusive input error", err)
	}
}

// ID-based operations must not fetch an Environment before their target request.
func TestEnvironmentOperationsRequestSequence(t *testing.T) {
	for _, tc := range []struct {
		command string
		request string
	}{
		{"delete", "DELETE /v1/environments/env_1"},
		{"archive", "POST /v1/environments/env_1/archive"},
		{"wait-ready", "GET /v1/environments/env_1"},
	} {
		t.Run(tc.command, func(t *testing.T) {
			isolateEnv(t)
			var requests []string
			srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
				request := r.Method + " " + r.URL.Path
				requests = append(requests, request)
				if request != tc.request {
					http.NotFound(w, r)
					return
				}
				if tc.command == "wait-ready" {
					_, _ = fmt.Fprint(w, `{"id":"env_1","name":"demo","build_status":"ready","created_at":"2026-08-01T10:00:00Z"}`)
				} else {
					w.WriteHeader(http.StatusNoContent)
				}
			})
			_, _, err := runCmd(t, "", "environments", tc.command, "env_1", "--base-url", srv.URL, "--api-key", "sk-test")
			if err != nil {
				t.Fatal(err)
			}
			if len(requests) != 1 || requests[0] != tc.request {
				t.Errorf("requests = %v; want exactly [%s]", requests, tc.request)
			}
		})
	}
}
