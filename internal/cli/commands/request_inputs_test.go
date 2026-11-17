package commands

import (
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"

	json "encoding/json/v2"
)

func TestAgentBindingsAndComplexRequestFiles(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		args       []string
	}{
		{"agent_create", `{"name":"a","model":"m","system":"prompt","metadata":{"team":"test"},"tools":[{"type":"agent_toolset","name":"default_toolset_20260901","configs":[{"name":"shell","enabled":false}]}]}`, []string{"agents", "create"}},
		{"agent_model", `{"name":"a","model":{"id":"m","reasoning_effort":"high","provider":"p"}}`, []string{"agents", "create"}},
		{"agent_clear", `{"version":"7","plugins":[]}`, []string{"agents", "update", "agent_1"}},
		{"environment_create", `{"name":"e","config":{"type":"cloud","networking":{"type":"limited","allow_package_managers":true}}}`, []string{"environments", "create"}},
		{"environment_update", `{"metadata":{}}`, []string{"environments", "update", "env_1"}},
		{"session_create", `{"agent_id":"agent_1","environment_id":"env_1","resources":[{"type":"vault","vault_id":"vlt_1"}]}`, []string{"sessions", "create"}},
		{"session_update", `{"agent_overrides":{"system":"","tools":[]}}`, []string{"sessions", "update", "sess_1"}},
		{"session_title", `{"title":""}`, []string{"sessions", "update", "sess_1", "--title", ""}},
		{"session_resource", `{"type":"file","file_id":"file_1"}`, []string{"sessions", "resources", "add", "sess_1"}},
		{"memory_store_create", `{"name":"notes","description":"Shared notes"}`, []string{"memory-stores", "create"}},
		{"memory_store_update", `{"description":""}`, []string{"memory-stores", "update", "ms_1"}},
		{"memory_store_fields", `{"name":"notes","description":"Shared notes"}`, []string{"memory-stores", "create", "--name", "notes", "--description", "Shared notes"}},
		{"memory_store_clear", `{"description":""}`, []string{"memory-stores", "update", "ms_1", "--description", ""}},
		{"trigger_create", `{"name":"t","agent_id":"agent_1","environment_id":"env_1","policy":{"type":"cron","expression":"0 9 * * *","timezone":"UTC"},"session_mode":"reuse_session","initial_events":[{"type":"user.message","data":{"content":[{"type":"text","text":"task"}]}}]}`, []string{"triggers", "create"}},
		{"trigger_update", `{"agent_id":"agent_2","agent_version":"","environment_id":"env_2","notification":{}}`, []string{"triggers", "update", "trig_1"}},
		{"vault_create", `{"display_name":"v","metadata":{"team":"research"}}`, []string{"vaults", "create"}},
		{"vault_update", `{"metadata":{}}`, []string{"vaults", "update", "vlt_1"}},
		{"vault_display_name", `{"display_name":""}`, []string{"vaults", "update", "vlt_1", "--display-name", ""}},
		{"credential_create", `{"display_name":"cred","auth":{"type":"environment_variable","secret_name":"TOKEN","secret_value":"test-only","networking":{"type":"limited","allowed_hosts":["example.com"]}}}`, []string{"vaults", "credentials", "create", "vlt_1"}},
		{"credential_update", `{"auth":{"type":"static_bearer","mcp_server_url":"https://example.com","token":"test-only"}}`, []string{"vaults", "credentials", "update", "vlt_1", "cred_1"}},
		{"oauth_create", `{"mcp_server_url":"https://example.com","client":{"client_id":"client_1","scope":"read"},"egress_environment_id":"env_1"}`, []string{"vaults", "oauth", "create", "vlt_1"}},
		{"dream_policy", `{"agent_id":"agent_1","environment_id":"env_1","activity":{"enabled":true,"min_changed_sessions":5},"schedule":{"enabled":false}}`, []string{"memory-stores", "dream-policy", "ms_1"}},
		{"binding_create", `{"name":"a","model":"m","skills":[{"skill_id":"skl_1"},{"skill_id":"skl_2"}],"plugins":[{"plugin_id":"plug_1"},{"plugin_id":"plug_2"}]}`, []string{"agents", "create", "--name", "a", "--model", "m", "--skill", "skl_1", "--skill", "skl_2", "--plugin", "plug_1", "--plugin", "plug_2"}},
		{"binding_update", `{"version":"7","skills":[{"skill_id":"skl_1"}]}`, []string{"agents", "update", "agent_1", "--version", "7", "--skill", "skl_1"}},
		{"plugin_update", `{"plugins":[{"plugin_id":"plug_1"}]}`, []string{"agents", "update", "agent_1", "--plugin", "plug_1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateEnv(t)
			args := append([]string{}, tc.args...)
			if !strings.Contains(strings.Join(args, " "), "--") {
				args = append(args, "--file", "-")
			}
			synctest.Test(t, func(t *testing.T) {
				bodies := make(chan []byte, 1)
				srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
					}
					bodies <- body
					// Create endpoints are 201 per the spec; the SDK decodes
					// the model only on the declared status.
					if r.Method == http.MethodPost {
						w.WriteHeader(http.StatusCreated)
					}
					// SessionResource is a union the generic body cannot
					// decode into; answer with a concrete variant there.
					if strings.Contains(r.URL.Path, "/resources") {
						_, _ = io.WriteString(w, `{"type":"file","file_id":"file_1"}`)
						return
					}
					_, _ = io.WriteString(w, `{"id":"created","model":"m","version":"8","config":{"type":"cloud"},"activity":{"enabled":false},"schedule":{"enabled":false},"auth":{"type":"environment_variable","secret_name":"TOKEN"}}`)
				})
				args = append(args, "--base-url", srv.URL, "--api-key", "sk-test", "-o", "json")
				_, _, err := runCmd(t, tc.want, args...)
				if err != nil {
					t.Fatal(err)
				}
				var got, want any
				select {
				case body := <-bodies:
					if err := json.Unmarshal(body, &got); err != nil {
						t.Fatal(err)
					}
				default:
					t.Fatal("no request")
				}
				if err := json.Unmarshal([]byte(tc.want), &want); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("request=%v, want %v", got, want)
				}
			})
		})
	}
}

func TestAgentBindingErrorsBeforeRequests(t *testing.T) {
	for _, args := range [][]string{
		{"agents", "update", "agent_1", "--file", "body.json", "--skill", "skl_1"},
		{"agents", "update", "agent_1", "--file", "body.json", "--plugin", "plug_1"},
		{"agents", "update", "agent_1", "--skill", ""},
		{"agents", "update", "agent_1", "--plugin", " "},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			isolateEnv(t)
			synctest.Test(t, func(t *testing.T) {
				var requests atomic.Int64
				srv := serve(t, func(w http.ResponseWriter, _ *http.Request) { requests.Add(1); http.Error(w, "unexpected", 400) })
				root := NewRootCommand()
				root.SetIn(failingInput{})
				root.SetOut(io.Discard)
				root.SetErr(io.Discard)
				root.SetArgs(append(args, "--base-url", srv.URL, "--api-key", "sk-test"))
				err := root.Execute()
				if exitCode(err) != 2 || requests.Load() != 0 {
					t.Fatalf("err=%v requests=%d", err, requests.Load())
				}
			})
		})
	}
}

func TestBodyFileInputErrorsBeforeRequests(t *testing.T) {
	for _, tc := range []struct {
		name   string
		args   []string
		fields []string
	}{
		{
			name:   "memory_store_create",
			args:   []string{"memory-stores", "create"},
			fields: []string{"name", "description"},
		},
		{
			name:   "memory_store_update",
			args:   []string{"memory-stores", "update", "ms_1"},
			fields: []string{"name", "description"},
		},
		{
			name:   "session_resource_add",
			args:   []string{"sessions", "resources", "add", "sess_1"},
			fields: []string{"file-id"},
		},
	} {
		for _, field := range append([]string{""}, tc.fields...) {
			for _, path := range []string{"", "-", "missing.json"} {
				if field == "" && path != "" {
					continue
				}
				t.Run(tc.name+"/"+field+"/"+path, func(t *testing.T) {
					isolateEnv(t)
					synctest.Test(t, func(t *testing.T) {
						var requests atomic.Int64
						srv := serve(t, func(w http.ResponseWriter, _ *http.Request) {
							requests.Add(1)
							http.Error(w, "unexpected request", http.StatusBadRequest)
						})
						args := append([]string{}, tc.args...)
						args = append(args, "--file="+path, "--base-url", srv.URL, "--api-key", "sk-test")
						want := "--file requires a file path"
						if field != "" {
							args = append(args, "--"+field, "value")
							want = "flags in the group"
						}
						root := NewRootCommand()
						root.SetIn(failingInput{})
						root.SetOut(io.Discard)
						root.SetErr(io.Discard)
						root.SetArgs(args)
						err := root.Execute()
						if exitCode(err) != 2 || requests.Load() != 0 {
							t.Fatalf("err=%v requests=%d; want usage error before I/O", err, requests.Load())
						}
						if !strings.Contains(err.Error(), want) {
							t.Fatalf("err=%v, want %q", err, want)
						}
					})
				})
			}
		}
	}
}

// structuredFlagCase drives one command with flag-built fields and asserts
// the exact request body, mirroring TestAgentBindingsAndComplexRequestFiles.
func structuredFlagCase(t *testing.T, want string, args ...string) {
	t.Helper()
	isolateEnv(t)
	synctest.Test(t, func(t *testing.T) {
		bodies := make(chan []byte, 1)
		srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
			}
			bodies <- body
			if strings.HasSuffix(r.URL.Path, "/events") {
				w.WriteHeader(http.StatusAccepted)
				_, _ = io.WriteString(w, `{"event_id":"evt_1"}`)
				return
			}
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusCreated)
			}
			_, _ = io.WriteString(w, `{"id":"created","model":"m","version":"8","config":{"type":"cloud"},"auth":{"type":"environment_variable","secret_name":"TOKEN"}}`)
		})
		args = append(args, "--base-url", srv.URL, "--api-key", "sk-test", "-o", "json")
		if _, _, err := runCmd(t, "", args...); err != nil {
			t.Fatal(err)
		}
		var got, wantV any
		select {
		case body := <-bodies:
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatal("no request")
		}
		if err := json.Unmarshal([]byte(want), &wantV); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, wantV) {
			t.Fatalf("request=%v, want %v", got, wantV)
		}
	})
}

func TestStructuredFlagsBuildRequestBodies(t *testing.T) {
	t.Run("agent_relaxed_yaml_tool", func(t *testing.T) {
		structuredFlagCase(t,
			`{"name":"a","model":"m","tools":[{"type":"custom","name":"search","description":"web search","input_schema":{"type":"object"}}]}`,
			"agents", "create", "--name", "a", "--model", "m",
			"--tool", `{type: custom, name: search, description: web search, input_schema: {type: object}}`)
	})
	t.Run("agent_json_tool_and_repeatable", func(t *testing.T) {
		structuredFlagCase(t,
			`{"name":"a","model":"m","tools":[{"type":"custom","name":"t1","description":"d","input_schema":{"type":"object"}},{"type":"custom","name":"t2","description":"d","input_schema":{"type":"object"}}],"mcp_servers":[{"type":"url","name":"gh","url":"https://mcp.example","headers":{"X-Tenant":"acme"}}]}`,
			"agents", "create", "--name", "a", "--model", "m",
			"--tool", `{"type":"custom","name":"t1","description":"d","input_schema":{"type":"object"}}`,
			"--tool", `{type: custom, name: t2, description: d, input_schema: {type: object}}`,
			"--mcp-server", `{name: gh, url: https://mcp.example, headers: {X-Tenant: acme}}`)
	})
	t.Run("agent_metadata_and_multiagent", func(t *testing.T) {
		structuredFlagCase(t,
			`{"name":"a","model":"m","metadata":{"team":"infra","env":"prod"},"multiagent":{"agents":[{"type":"agent","id":"agent_2","version":"3"}]}}`,
			"agents", "create", "--name", "a", "--model", "m",
			"--metadata", "team=infra", "--metadata", "env=prod",
			"--multiagent", `{agents: [{type: agent, id: agent_2, version: "3"}]}`)
	})
	t.Run("agent_skill_version_pin", func(t *testing.T) {
		structuredFlagCase(t,
			`{"name":"a","model":"m","skills":[{"skill_id":"skl_1","version":"2"},{"skill_id":"skl_2"}]}`,
			"agents", "create", "--name", "a", "--model", "m", "--skill", "skl_1@2", "--skill", "skl_2")
	})
	t.Run("session_resource_and_override", func(t *testing.T) {
		structuredFlagCase(t,
			`{"agent_id":"agent_1","environment_id":"env_1","resources":[{"type":"vault","vault_id":"vlt_1"},{"type":"memory_store","memory_store_id":"mstr_1","access":"read_write"}],"agent_overrides":{"model":"kimi-k3.1-pro","system":"be terse"},"metadata":{"ticket":"PROJ-42"}}`,
			"sessions", "create", "--agent", "agent_1", "--environment", "env_1",
			"--resource", `{type: vault, vault_id: vlt_1}`,
			"--resource", `{type: memory_store, memory_store_id: mstr_1, access: read_write}`,
			"--override", `{model: kimi-k3.1-pro, system: be terse}`,
			"--metadata", "ticket=PROJ-42")
	})
	t.Run("session_update_override", func(t *testing.T) {
		structuredFlagCase(t,
			`{"agent_overrides":{"model":"kimi-k3.1-pro"}}`,
			"sessions", "update", "sess_1", "--override", `{model: kimi-k3.1-pro}`)
	})
	t.Run("session_send_event", func(t *testing.T) {
		structuredFlagCase(t,
			`{"events":[{"type":"user.message","data":{"content":[{"type":"text","text":"look at this"},{"type":"file","file_id":"file_9","mime_type":"text/csv","title":"data.csv","path":"upload/data.csv"}]}}]}`,
			"sessions", "send", "sess_1",
			"--event", `{type: user.message, data: {content: [{type: text, text: "look at this"}, {type: file, file_id: file_9, mime_type: text/csv, title: data.csv, path: upload/data.csv}]}}`)
	})
	t.Run("trigger_notification_and_initial_events", func(t *testing.T) {
		structuredFlagCase(t,
			`{"name":"t","agent_id":"agent_1","environment_id":"env_1","policy":{"type":"cron","expression":"0 9 * * *","timezone":"UTC"},"initial_events":[{"type":"user.message","data":{"content":[{"type":"text","text":"run it"}]}}],"notification":{"url":"https://example.invalid/hook","secret_vault":{"vault_id":"vlt_1"}}}`,
			"triggers", "create", "--name", "t", "--agent", "agent_1", "--environment", "env_1", "--cron", "0 9 * * *",
			"--initial-event", `{type: user.message, data: {content: [{type: text, text: "run it"}]}}`,
			"--notification", `{url: https://example.invalid/hook, secret_vault: {vault_id: vlt_1}}`)
	})
	t.Run("trigger_update_notification_detach", func(t *testing.T) {
		structuredFlagCase(t,
			`{"notification":{}}`,
			"triggers", "update", "trig_1", "--notification", `{}`)
	})
	t.Run("environment_cloud_config", func(t *testing.T) {
		structuredFlagCase(t,
			`{"name":"e","config":{"type":"cloud","networking":{"type":"limited","allowed_hosts":["example.com","api.internal"],"allow_package_managers":true},"packages":{"pip":["pandas==2.3.2"],"npm":["left-pad"]},"setup_script":"mkdir -p /workspace/output"},"metadata":{"team":"data"}}`,
			"environments", "create", "--name", "e",
			"--networking", "limited", "--allow-host", "example.com", "--allow-host", "api.internal", "--allow-package-managers",
			"--pip", "pandas==2.3.2", "--npm", "left-pad",
			"--setup-script", "mkdir -p /workspace/output",
			"--metadata", "team=data")
	})
	t.Run("environment_update_config", func(t *testing.T) {
		structuredFlagCase(t,
			`{"config":{"type":"cloud","networking":{"type":"unrestricted"}}}`,
			"environments", "update", "env_1", "--config", `{type: cloud, networking: {type: unrestricted}}`)
	})
	t.Run("credential_networking_and_injection", func(t *testing.T) {
		structuredFlagCase(t,
			`{"auth":{"type":"environment_variable","secret_name":"TOKEN","secret_value":"s3cret","networking":{"type":"limited","allowed_hosts":["api.example.com"]},"injection_location":{"header":true,"body":true}}}`,
			"vaults", "credentials", "create", "vlt_1", "--type", "environment_variable", "--name", "TOKEN", "--value", "s3cret",
			"--networking", "limited", "--allow-host", "api.example.com",
			"--inject-header", "--inject-body")
	})
}

func TestStructuredFlagErrorsBeforeRequests(t *testing.T) {
	for _, args := range [][]string{
		{"agents", "create", "--name", "a", "--model", "m", "--tool", "{not yaml: ["},
		{"agents", "create", "--name", "a", "--model", "m", "--tool", "{type: custom, name: 42, description: d, input_schema: {type: object}}"},
		{"agents", "create", "--name", "a", "--model", "m", "--metadata", "nokey"},
		{"agents", "create", "--name", "a", "--model", "m", "--metadata", "a=1", "--metadata", "a=2"},
		{"agents", "create", "--name", "a", "--model", "m", "--multiagent", "{agents: []}", "--multiagent", "{agents: []}"},
		{"agents", "create", "--name", "a", "--model", "m", "--file", "body.json", "--tool", "{type: custom, name: t, description: d, input_schema: {type: object}}"},
		{"agents", "create", "--name", "a", "--model", "m", "--skill", "skl_1", "--skill", ""},
		{"sessions", "create", "--agent", "agent_1", "--environment", "env_1", "--resource", "{type: 42, vault_id: vlt_1}"},
		{"environments", "create", "--name", "e", "--self-hosted", "--pip", "pandas"},
		{"environments", "create", "--name", "e", "--allow-host", " "},
		{"vaults", "credentials", "create", "vlt_1", "--type", "static_bearer", "--mcp-url", "https://mcp.example", "--value", "v", "--networking", "limited"},
		{"vaults", "credentials", "create", "vlt_1", "--type", "static_bearer", "--mcp-url", "https://mcp.example", "--value", "v", "--inject-header"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			isolateEnv(t)
			synctest.Test(t, func(t *testing.T) {
				var requests atomic.Int64
				srv := serve(t, func(w http.ResponseWriter, _ *http.Request) { requests.Add(1); http.Error(w, "unexpected", 400) })
				root := NewRootCommand()
				root.SetIn(failingInput{})
				root.SetOut(io.Discard)
				root.SetErr(io.Discard)
				root.SetArgs(append(args, "--base-url", srv.URL, "--api-key", "sk-test"))
				err := root.Execute()
				if err == nil || requests.Load() != 0 {
					t.Fatalf("err=%v requests=%d", err, requests.Load())
				}
			})
		})
	}
}

func TestResourceBindingShorthands(t *testing.T) {
	t.Run("session_create_all_shorthands", func(t *testing.T) {
		structuredFlagCase(t,
			`{"agent_id":"agent_9","agent_version":"3","environment_id":"env_1","resources":[{"type":"vault","vault_id":"vlt_1"},{"type":"vault","vault_id":"vlt_2"},{"type":"memory_store","memory_store_id":"mstr_1","access":"read_only"},{"type":"memory_store","memory_store_id":"mstr_2","access":"read_write"},{"type":"file","file_id":"file_1"},{"type":"github_repository","url":"https://github.com/org/repo","authorization_token":"ghp_x","checkout":{"type":"branch","name":"main"}}]}`,
			"sessions", "create", "--agent", "agent_9@3", "--environment", "env_1",
			"--vault", "vlt_1", "--vault", "vlt_2",
			"--memory-store", "mstr_1:read_only", "--memory-store", "mstr_2",
			"--file-id", "file_1",
			"--repo", "https://github.com/org/repo:main", "--repo-token", "ghp_x")
	})
	t.Run("trigger_create_shorthands", func(t *testing.T) {
		structuredFlagCase(t,
			`{"name":"t","agent_id":"agent_1","environment_id":"env_1","policy":{"type":"cron","expression":"0 9 * * *","timezone":"UTC"},"resources":[{"type":"vault","vault_id":"vlt_1"}]}`,
			"triggers", "create", "--name", "t", "--agent", "agent_1", "--environment", "env_1", "--cron", "0 9 * * *",
			"--vault", "vlt_1")
	})
	t.Run("session_create_mixed_shorthand_and_resource", func(t *testing.T) {
		structuredFlagCase(t,
			`{"agent_id":"agent_1","environment_id":"env_1","resources":[{"type":"vault","vault_id":"vlt_1"},{"type":"file","file_id":"file_9"}]}`,
			"sessions", "create", "--agent", "agent_1", "--environment", "env_1",
			"--vault", "vlt_1",
			"--resource", `{type: file, file_id: file_9}`)
	})
}

func TestResourceBindingErrorsBeforeRequests(t *testing.T) {
	for _, args := range [][]string{
		{"sessions", "create", "--agent", "agent_1@2", "--agent-version", "3", "--environment", "env_1"},
		{"sessions", "create", "--agent", "agent_1", "--environment", "env_1", "--memory-store", "mstr_1:bogus"},
		{"sessions", "create", "--agent", "agent_1", "--environment", "env_1", "--memory-store", "mstr_1:"},
		{"sessions", "create", "--agent", "agent_1", "--environment", "env_1", "--repo", "https://gitlab.com/org/repo"},
		{"sessions", "create", "--agent", "agent_1", "--environment", "env_1", "--repo-token", "ghp_x"},
		{"triggers", "create", "--name", "t", "--agent", "agent_1", "--environment", "env_1", "--cron", "0 9 * * *", "--repo", "git@github.com:org/repo.git"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			isolateEnv(t)
			synctest.Test(t, func(t *testing.T) {
				var requests atomic.Int64
				srv := serve(t, func(w http.ResponseWriter, _ *http.Request) { requests.Add(1); http.Error(w, "unexpected", 400) })
				root := NewRootCommand()
				root.SetIn(failingInput{})
				root.SetOut(io.Discard)
				root.SetErr(io.Discard)
				root.SetArgs(append(args, "--base-url", srv.URL, "--api-key", "sk-test"))
				err := root.Execute()
				if err == nil || requests.Load() != 0 {
					t.Fatalf("err=%v requests=%d", err, requests.Load())
				}
			})
		})
	}
}

func TestSinceFlag(t *testing.T) {
	isolateEnv(t)
	synctest.Test(t, func(t *testing.T) {
		queries := make(chan string, 2)
		srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
			queries <- r.URL.RawQuery
			_, _ = io.WriteString(w, `{"items":[]}`)
		})
		if _, _, err := runCmd(t, "", "agents", "list", "--since", "7d", "--base-url", srv.URL, "--api-key", "sk-test"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := runCmd(t, "", "sessions", "list", "--since", "2026-11-01", "--base-url", srv.URL, "--api-key", "sk-test"); err != nil {
			t.Fatal(err)
		}
		q1, q2 := <-queries, <-queries
		if !strings.Contains(q1, "created_at_gte=") {
			t.Fatalf("agents query = %s", q1)
		}
		if !strings.Contains(q2, "updated_at_gte=2026-11-01T00") {
			t.Fatalf("sessions query = %s", q2)
		}
	})
}

func TestSinceFlagRejectsGarbageBeforeRequests(t *testing.T) {
	isolateEnv(t)
	synctest.Test(t, func(t *testing.T) {
		var requests atomic.Int64
		srv := serve(t, func(w http.ResponseWriter, _ *http.Request) { requests.Add(1); http.Error(w, "unexpected", 400) })
		_, _, err := runCmd(t, "", "agents", "list", "--since", "last-tuesday", "--base-url", srv.URL, "--api-key", "sk-test")
		if err == nil || requests.Load() != 0 {
			t.Fatalf("err=%v requests=%d", err, requests.Load())
		}
	})
}

func TestProfileListAndUse(t *testing.T) {
	isolateEnv(t)
	// Seed two profiles through the login flow so keychain refs are real.
	if _, _, err := runCmd(t, "sk-alpha\n", "login", "--api-key-stdin", "--profile", "alpha", "--base-url", "https://alpha.example"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCmd(t, "sk-beta\n", "login", "--api-key-stdin", "--profile", "beta"); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := runCmd(t, "", "profile", "list")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout, "sk-alpha") || strings.Contains(stdout, "sk-beta") {
		t.Fatalf("profile list leaked key material:\n%s", stdout)
	}
	for _, want := range []string{"alpha", "beta", "keychain", "https://alpha.example"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("profile list missing %q:\n%s", want, stdout)
		}
	}

	_, stderr, err := runCmd(t, "", "profile", "use", "beta")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, `"beta"`) {
		t.Fatalf("use stderr = %s", stderr)
	}
	stdout, _, err = runCmd(t, "", "profile", "list")
	if err != nil {
		t.Fatal(err)
	}
	// beta must now be the current profile (the "*" row).
	for line := range strings.Lines(stdout) {
		if strings.HasPrefix(line, "beta") && !strings.Contains(line, "*") {
			t.Fatalf("beta not marked current:\n%s", stdout)
		}
	}

	// KHA_PROFILE in the shell overrides the switch and must be called out.
	t.Setenv("KHA_PROFILE", "alpha")
	_, stderr, err = runCmd(t, "", "profile", "use", "beta")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "KHA_PROFILE") {
		t.Fatalf("missing KHA_PROFILE override note:\n%s", stderr)
	}

	// A profile that does not exist cannot be selected.
	if _, _, err := runCmd(t, "", "profile", "use", "ghost"); err == nil {
		t.Fatal("profile use ghost should fail")
	}
}

func TestJSONLListOutput(t *testing.T) {
	isolateEnv(t)
	synctest.Test(t, func(t *testing.T) {
		srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("page_token") == "" {
				_, _ = io.WriteString(w, `{"items":[{"id":"agent_1","name":"a","model":"kimi-k3.1-pro","version":"1","created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-01T10:00:00Z","scope":"project"}],"next_page_token":"tok"}`)
				return
			}
			_, _ = io.WriteString(w, `{"items":[{"id":"agent_2","name":"b","model":"kimi-k3.1-pro","version":"1","created_at":"2026-08-02T10:00:00Z","updated_at":"2026-08-02T10:00:00Z","scope":"project"}]}`)
		})
		stdout, _, err := runCmd(t, "", "agents", "list", "--all", "-o", "jsonl", "--base-url", srv.URL, "--api-key", "sk-test")
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(stdout), "\n")
		if len(lines) != 2 {
			t.Fatalf("jsonl lines = %d:\n%s", len(lines), stdout)
		}
		for _, line := range lines {
			var m map[string]any
			if err := json.Unmarshal([]byte(line), &m); err != nil {
				t.Fatalf("line %q not compact JSON: %v", line, err)
			}
		}
		if !strings.Contains(lines[0], `"agent_1"`) || !strings.Contains(lines[1], `"agent_2"`) {
			t.Fatalf("paging order wrong:\n%s", stdout)
		}

		// --transform and -r apply per item.
		stdout, _, err = runCmd(t, "", "agents", "list", "--all", "-o", "jsonl", "--transform", "id", "-r", "--base-url", srv.URL, "--api-key", "sk-test")
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(stdout) != "agent_1\nagent_2" {
			t.Fatalf("transformed jsonl = %q", stdout)
		}
	})
}

func TestJSONLSingleItemOutput(t *testing.T) {
	isolateEnv(t)
	synctest.Test(t, func(t *testing.T) {
		srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `{"id":"sess_1","agent_id":"agent_1","environment_id":"env_1","status":"idle","created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-01T10:00:00Z"}`)
		})
		stdout, _, err := runCmd(t, "", "sessions", "get", "sess_1", "-o", "jsonl", "--base-url", srv.URL, "--api-key", "sk-test")
		if err != nil {
			t.Fatal(err)
		}
		if lines := strings.Count(strings.TrimSpace(stdout), "\n"); lines != 0 {
			t.Fatalf("single item jsonl should be one line:\n%s", stdout)
		}
		if !strings.HasPrefix(stdout, `{"id":"sess_1"`) {
			t.Fatalf("not compact JSON: %q", stdout)
		}
	})
}
