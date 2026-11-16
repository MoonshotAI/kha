package commands

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
)

func TestListCommandsNormalizePages(t *testing.T) {
	for _, resource := range []string{"agents", "sessions", "environments", "artifacts", "files", "plugins", "skills", "triggers", "vaults", "memory-stores"} {
		for _, tc := range []struct {
			name  string
			body  string
			token string
		}{
			{"null-item", `{"items":[null]}`, ""},
			{"empty-items", `{"items":[]}`, ""},
			{"omitted-items", `{}`, ""},
			{"empty-token", `{"items":[],"next_page_token":""}`, ""},
			{"next-token", `{"items":[],"next_page_token":"next_1"}`, "next_1"},
		} {
			t.Run(resource+"/"+tc.name, func(t *testing.T) {
				isolateEnv(t)
				srv := serve(t, func(w http.ResponseWriter, _ *http.Request) {
					_, _ = fmt.Fprint(w, tc.body)
				})
				stdout, _, err := runCmd(t, "", resource, "list", "-o", "json", "--base-url", srv.URL, "--api-key", "sk-test")
				if err != nil {
					t.Fatal(err)
				}
				var page map[string]jsontext.Value
				if err := json.Unmarshal([]byte(stdout), &page); err != nil {
					t.Fatal(err)
				}
				if string(page["items"]) != "[]" {
					t.Errorf("items = %s; want []", page["items"])
				}
				if tc.token == "" {
					if token, ok := page["next_page_token"]; ok {
						t.Errorf("empty next_page_token retained: %s", token)
					}
				} else if string(page["next_page_token"]) != `"`+tc.token+`"` {
					t.Errorf("next_page_token = %s; want %q", page["next_page_token"], tc.token)
				}
			})
		}
	}
}

func TestEventListsFilterNullItemsAcrossFormats(t *testing.T) {
	for _, command := range [][]string{{"sessions", "events", "sess_1"}, {"sessions", "threads", "events", "sess_1", "thr_1"}} {
		for _, format := range []string{"json", "yaml", "table"} {
			for _, transform := range []string{"", "items.#"} {
				t.Run(strings.Join(command, "/")+"/"+format+"/"+transform, func(t *testing.T) {
					isolateEnv(t)
					synctest.Test(t, func(t *testing.T) {
						server := serveCommandTest(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
							w.Header().Set("Content-Type", "application/json")
							_, _ = fmt.Fprintf(w, `{"items":[null,%s,null],"stream_cursor":"cur_1"}`, statusEventTestJSON("evt_kept", "idle", "end_turn"))
						}))
						args := append(slices.Clone(command), "-o", format, "--base-url", server.URL, "--api-key", "sk-test")
						if transform != "" {
							args = append(args, "--transform", transform, "--raw-output")
						}
						stdout, _, err := runCmd(t, "", args...)
						if err != nil {
							t.Fatal(err)
						}
						if transform != "" {
							want := "1"
							if format == "table" {
								want = "VALUE 1"
							}
							if got := strings.Join(strings.Fields(stdout), " "); got != want {
								t.Errorf("transformed item count = %q, want %q", got, want)
							}
						} else if strings.Contains(stdout, "null") || !strings.Contains(stdout, "evt_kept") {
							t.Errorf("null item retained or valid event lost: %s", stdout)
						}
					})
				})
			}
		}
	}
}
