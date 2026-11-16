package commands

import (
	"fmt"
	"net/http"
	"testing"
	"testing/synctest"

	"github.com/MoonshotAI/kha/internal/cli/config"
)

func TestConnectionSelection(t *testing.T) {
	for _, tc := range []struct {
		name, profileKey, wantTarget, wantKey string
		args                                  []string
	}{
		{"explicit_profile", "sk-profile", "profile", "sk-profile", []string{"--profile", "staging"}},
		{"explicit_key", "sk-profile", "profile", "sk-flag", []string{"--profile", "staging", "--api-key", "sk-flag"}},
		{"explicit_endpoint", "sk-profile", "override", "sk-profile", []string{"--profile", "staging", "--base-url"}},
		{"anonymous_profile", "", "profile", "", []string{"--profile", "staging"}},
		{"environment_overrides_implicit_profile", "sk-profile", "environment", "sk-env", nil},
		{"completion_profile", "sk-profile", "profile", "sk-profile", []string{"--profile", "staging", "__complete", "agents", "get", ""}},
		{"completion_anonymous_profile", "", "profile", "", []string{"--profile", "staging", "__complete", "agents", "get", ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateEnv(t)
			synctest.Test(t, func(t *testing.T) {
				var target, auth string
				newEndpoint := func(name string) string {
					return serve(t, func(w http.ResponseWriter, r *http.Request) {
						target, auth = name, r.Header.Get("Authorization")
						_, _ = fmt.Fprint(w, `{"items":[]}`)
					}).URL
				}
				profileURL := newEndpoint("profile")
				envURL := newEndpoint("environment")
				overrideURL := newEndpoint("override")
				cfg := &config.Config{}
				cfg.SetProfile("prod", config.Profile{APIKey: "sk-prod", BaseURL: envURL})
				cfg.SetProfile("staging", config.Profile{APIKey: tc.profileKey, BaseURL: profileURL})
				if err := cfg.Save(); err != nil {
					t.Fatal(err)
				}
				t.Setenv("KIMI_API_KEY", "sk-env")
				t.Setenv("KHA_BASE_URL", envURL)
				t.Setenv("KHA_PROFILE", "prod")
				args := append([]string{}, tc.args...)
				if len(args) > 0 && args[len(args)-1] == "--base-url" {
					args = append(args, overrideURL)
				}
				if tc.name != "completion_profile" && tc.name != "completion_anonymous_profile" {
					args = append(args, "whoami", "-o", "json")
				}
				if _, _, err := runCmd(t, "", args...); err != nil {
					t.Fatal(err)
				}
				wantAuth := ""
				if tc.wantKey != "" {
					wantAuth = "Bearer " + tc.wantKey
				}
				if target != tc.wantTarget || auth != wantAuth {
					t.Fatalf("target=%q auth=%q; want %q %q", target, auth, tc.wantTarget, wantAuth)
				}
			})
		})
	}
}

func TestExplicitProfileDefaultsIgnoreEnvironment(t *testing.T) {
	isolateEnv(t)
	cfg := &config.Config{}
	cfg.SetProfile("empty", config.Profile{})
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KIMI_API_KEY", "sk-env")
	t.Setenv("KHA_BASE_URL", "https://other.example.com")
	creds, err := resolveConnection(&globalFlags{profile: "empty"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if creds.apiKey != "" || creds.baseURL != "" {
		t.Fatalf("empty profile inherited environment values: %+v", creds)
	}
}
