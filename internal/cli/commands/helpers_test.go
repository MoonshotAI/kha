package commands

import (
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"

	json "encoding/json/v2"

	"github.com/spf13/cobra"
)

// Exercise the public command tree and inspect the serialized requests,
// including omitted versus explicitly empty fields. The text fixture also
// verifies preservation of multibyte UTF-8 and exact whitespace.
func TestFieldFileInputs(t *testing.T) {
	commands := []struct {
		name, flag, field, method, path string
		args                            []string
	}{
		{"agent_create", "system", "system", "POST", "/v1/agents", []string{"agents", "create", "--name", "demo", "--model", "kimi-k2"}},
		{"agent_update", "system", "system", "PATCH", "/v1/agents/agent_1", []string{"agents", "update", "agent_1", "--version", "7"}},
		{"session_send", "text", "events", "POST", "/v1/sessions/sess_1/events", []string{"sessions", "send", "sess_1"}},
		{"trigger_run", "payload", "payload", "POST", "/v1/triggers/trig_1/run", []string{"triggers", "run", "trig_1", "--wait"}},
		{"memory_create", "content", "content", "POST", "/v1/memory-stores/ms_1/memories", []string{"memory-stores", "memories", "create", "ms_1", "--path", "notes.md"}},
		{"memory_update", "content", "content", "PATCH", "/v1/memory-stores/ms_1/memories/mem_1", []string{"memory-stores", "memories", "update", "ms_1", "mem_1", "--sha256", "current-hash"}},
	}
	for _, cmdCase := range commands {
		for _, source := range []string{"file", "stdin", "empty_file", "empty_stdin", "literal", "legacy", "omitted"} {
			if cmdCase.flag == "text" && strings.HasPrefix(source, "empty_") {
				continue // Empty Session messages are rejected before API access.
			}
			if source == "omitted" && (strings.HasSuffix(cmdCase.name, "update") || cmdCase.flag == "text") {
				continue
			}
			if source == "legacy" && cmdCase.flag != "content" {
				continue
			}
			t.Run(cmdCase.name+"/"+source, func(t *testing.T) {
				isolateEnv(t)
				text := "  分析 CSV\r\nReturn JSON.\n\n"
				if strings.HasPrefix(source, "empty_") || source == "omitted" {
					text = ""
				}
				if cmdCase.flag == "content" && (source == "file" || source == "stdin") {
					text += "\x00\xff" // Memory files also preserve non-UTF-8 bytes.
				}
				file := filepath.Join(t.TempDir(), "input with spaces.md")
				if err := os.WriteFile(file, []byte(text), 0600); err != nil {
					t.Fatal(err)
				}
				args := append([]string{}, cmdCase.args...)
				switch source {
				case "omitted":
				case "literal":
					if cmdCase.flag == "text" {
						args = append(args, text)
					} else {
						args = append(args, "--"+cmdCase.flag, text)
					}
				case "legacy":
					args = append(args, "--content-stdin")
				default:
					if strings.HasSuffix(source, "stdin") {
						file = "-"
					}
					args = append(args, "--"+cmdCase.flag+"-file", file)
				}
				synctest.Test(t, func(t *testing.T) {
					bodies := make(chan map[string]any, 1)
					srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
						if r.Method == "GET" && r.URL.Path == "/v1/sessions/sess_1" {
							_, _ = io.WriteString(w, `{"id":"sess_1","status":"idle"}`)
							return
						}
						if r.Method != cmdCase.method || r.URL.Path != cmdCase.path {
							t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
							http.Error(w, "unexpected request", http.StatusBadRequest)
							return
						}
						var body map[string]any
						raw, err := io.ReadAll(r.Body)
						if err != nil {
							t.Error(err)
						}
						if source == "omitted" && cmdCase.flag == "payload" {
							if len(raw) != 0 {
								t.Errorf("unexpected request body: %s", raw)
							}
						} else if err := json.Unmarshal(raw, &body); err != nil {
							t.Error(err)
						}
						bodies <- body
						// Create endpoints are 201 per the spec; the SDK
						// decodes the model only on the declared status.
						if cmdCase.name == "session_send" {
							w.WriteHeader(http.StatusAccepted)
						}
						if cmdCase.name == "trigger_run" || cmdCase.name == "agent_create" || cmdCase.name == "memory_create" {
							w.WriteHeader(http.StatusCreated)
						}
						_, _ = io.WriteString(w, `{"id":"created","event_id":"evt_1","version":"8","status":"completed","model":"kimi-k2"}`)
					})
					args = append(args, "--base-url", srv.URL, "--api-key", "sk-test", "-o", "json")
					_, stderr, err := runCmd(t, text, args...)
					if err != nil {
						t.Fatalf("command failed: %v\n%s", err, stderr)
					}
					var body map[string]any
					select {
					case body = <-bodies:
					default:
						t.Fatal("no mutation request received")
					}
					got, ok := body[cmdCase.field].(string)
					if cmdCase.field == "events" {
						data, err := json.Marshal(body)
						if err != nil {
							t.Fatal(err)
						}
						var sent struct {
							Events []struct {
								Data struct {
									Content []struct {
										Text string `json:"text"`
									} `json:"content"`
								} `json:"data"`
							} `json:"events"`
						}
						if err := json.Unmarshal(data, &sent); err != nil {
							t.Fatal(err)
						}
						if len(sent.Events) != 1 {
							t.Fatalf("unexpected message body: %s", data)
						}
						content := sent.Events[0].Data.Content
						if len(content) != 1 {
							t.Fatalf("unexpected content blocks: %s", data)
						}
						got, ok = content[0].Text, true
					}
					if cmdCase.field == "content" {
						raw, err := base64.StdEncoding.DecodeString(got)
						if err != nil {
							t.Fatal(err)
						}
						got = string(raw)
					}
					if ok != (source != "omitted") || got != text {
						t.Fatalf("field %s: present=%v, got %q, want %q", cmdCase.field, ok, got, text)
					}
					if cmdCase.name == "agent_update" && (body["version"] != "7" || len(body) != 2) {
						t.Fatalf("update changed other fields or lost concurrency version: %v", body)
					}
					if cmdCase.name == "memory_update" && body["content_sha256"] != "current-hash" {
						t.Fatalf("update lost concurrency hash: %v", body)
					}
				})
			})
		}
	}
}

func TestFieldFileErrorsBeforeRequests(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"agent_create_sources", []string{"agents", "create", "--name", "a", "--model", "m", "--system", "", "--system-file", "-"}},
		{"agent_update_sources", []string{"agents", "update", "agent_1", "--system", "literal", "--system-file", "-"}},
		{"agent_create_body", []string{"agents", "create", "--file", "-", "--system-file", "-"}},
		{"agent_update_body", []string{"agents", "update", "agent_1", "--file", "-", "--system-file", "-"}},
		{"session_sources", []string{"sessions", "send", "sess_1", "", "--text-file", "-"}},
		{"session_missing_source", []string{"sessions", "send", "sess_1"}},
		{"trigger_sources", []string{"triggers", "run", "trig_1", "--payload", "", "--payload-file", "-"}},
		{"memory_sources", []string{"memory-stores", "memories", "create", "ms_1", "--path", "notes", "--content", "", "--content-file", "-"}},
		{"memory_stdin_sources", []string{"memory-stores", "memories", "update", "ms_1", "mem_1", "--content-stdin", "--content-file", "-"}},
		{"memory_create_body", []string{"memory-stores", "memories", "create", "ms_1", "--file", "-", "--content-file", "-"}},
		{"memory_update_body", []string{"memory-stores", "memories", "update", "ms_1", "mem_1", "--file", "-", "--content-file", "-"}},
		{"agent_empty_path", []string{"agents", "update", "agent_1", "--system-file="}},
		{"session_empty_path", []string{"sessions", "send", "sess_1", "--text-file="}},
		{"trigger_empty_path", []string{"triggers", "run", "trig_1", "--payload-file="}},
		{"memory_empty_path", []string{"memory-stores", "memories", "update", "ms_1", "mem_1", "--content-file="}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateEnv(t)
			synctest.Test(t, func(t *testing.T) {
				var requests atomic.Int64
				srv := serve(t, func(w http.ResponseWriter, _ *http.Request) {
					requests.Add(1)
					http.Error(w, "unexpected request", http.StatusBadRequest)
				})
				args := append(tc.args, "--base-url", srv.URL, "--api-key", "sk-test")
				_, _, err := runCmd(t, "should not be read", args...)
				if exitCode(err) != 2 || requests.Load() != 0 {
					t.Fatalf("err=%v, code=%d, requests=%d; want usage error before API access", err, exitCode(err), requests.Load())
				}
			})
		})
	}
}

func TestReadInputFile(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, tc := range []struct {
		name, path string
		stdin      io.Reader
		want       string
		wantErr    error
	}{
		{"missing_file", "missing", nil, "", os.ErrNotExist},
		{"broken_stdin", "-", failingInput{}, "", errInputRead},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := NewRootCommand()
			cmd.SetIn(tc.stdin)
			got, err := readInputFile(cmd, "text-file", tc.path)
			if !errors.Is(err, tc.wantErr) || string(got) != tc.want {
				t.Fatalf("got %q, %v; want %q, %v", got, err, tc.want, tc.wantErr)
			}
		})
	}
}

var errInputRead = errors.New("input read failed")

type failingInput struct{}

func (failingInput) Read([]byte) (int, error) { return 0, errInputRead }

func TestFieldFileReadErrorsBeforeRequests(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"agent", []string{"agents", "update", "agent_1", "--system-file"}},
		{"session", []string{"sessions", "send", "sess_1", "--text-file"}},
		{"trigger", []string{"triggers", "run", "trig_1", "--payload-file"}},
		{"memory", []string{"memory-stores", "memories", "update", "ms_1", "mem_1", "--content-file"}},
	} {
		for _, source := range []string{"file", "stdin"} {
			t.Run(tc.name+"/"+source, func(t *testing.T) {
				isolateEnv(t)
				path := filepath.Join(t.TempDir(), "missing")
				wantErr := os.ErrNotExist
				if source == "stdin" {
					path, wantErr = "-", errInputRead
				}
				synctest.Test(t, func(t *testing.T) {
					var requests atomic.Int64
					srv := serve(t, func(w http.ResponseWriter, _ *http.Request) {
						requests.Add(1)
						http.Error(w, "unexpected request", http.StatusBadRequest)
					})
					root := NewRootCommand()
					root.SetIn(failingInput{})
					root.SetOut(io.Discard)
					root.SetErr(io.Discard)
					root.SetArgs(append(append([]string{}, tc.args...), path, "--base-url", srv.URL, "--api-key", "sk-test"))
					err := root.Execute()
					if !errors.Is(err, wantErr) || exitCode(err) != 1 || requests.Load() != 0 {
						t.Fatalf("err=%v, code=%d, requests=%d; want %v before API access", err, exitCode(err), requests.Load(), wantErr)
					}
				})
			})
		}
	}
}

func TestWaitProgressSilentWithoutTerminal(t *testing.T) {
	cmd := &cobra.Command{}
	var buf strings.Builder
	cmd.SetErr(&buf)
	stop := waitProgress(cmd, "waiting for %s", "thing")
	stop()
	stop() // stop is safe to call more than once
	if buf.Len() != 0 {
		t.Fatalf("a non-terminal stderr must stay silent, got %q", buf.String())
	}
}
