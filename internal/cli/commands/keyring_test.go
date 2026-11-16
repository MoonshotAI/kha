package commands

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/zalando/go-keyring"

	"github.com/MoonshotAI/kha/internal/cli/config"
)

type fakeKeychain struct {
	entries                   map[string]string
	getErr, setErr, deleteErr error
	onSet                     func()
}

func (f *fakeKeychain) Get(service, account string) (string, error) {
	if f.getErr != nil {
		return "", f.getErr
	}
	key, ok := f.entries[service+"/"+account]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return key, nil
}
func (f *fakeKeychain) Set(service, account, key string) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.entries[service+"/"+account] = key
	if f.onSet != nil {
		f.onSet()
	}
	return nil
}
func (f *fakeKeychain) Delete(service, account string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	delete(f.entries, service+"/"+account)
	return nil
}
func stubKeychain(t *testing.T) *fakeKeychain {
	t.Helper()
	old := systemKeys
	fake := &fakeKeychain{entries: map[string]string{}}
	systemKeys = fake
	t.Cleanup(func() { systemKeys = old })
	return fake
}
func stubLoginPrompt(t *testing.T, key string) {
	t.Helper()
	old := promptLoginToken
	promptLoginToken = func(*cobra.Command, string) (string, error) { return key, nil }
	t.Cleanup(func() { promptLoginToken = old })
}

func TestLoginMigratesLegacyProfile(t *testing.T) {
	isolateEnv(t)
	cfg := &config.Config{}
	cfg.SetProfile("old", config.Profile{APIKey: "sk-old-plaintext", BaseURL: "https://private.example", APIVersion: "2099-01-01"})
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	stubLoginPrompt(t, "sk-new-secret")
	stdout, stderr, err := runCmd(t, "", "login", "--profile", "old")
	if err != nil {
		t.Fatal(err)
	}
	path, _ := config.Path()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"sk-old-plaintext", "sk-new-secret"} {
		if strings.Contains(string(data)+stdout+stderr, secret) {
			t.Fatal("plaintext key leaked")
		}
	}
	c, err := resolveConnection(&globalFlags{profile: "old"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if c.apiKey != "sk-new-secret" || c.baseURL != "https://private.example" || c.apiVersion != "2099-01-01" {
		t.Fatal("login did not preserve settings or use saved key")
	}
	if _, _, err := runCmd(t, "", "login", "--api-key", "sk-replacement", "--profile", "old"); err != nil {
		t.Fatal(err)
	}
	if len(systemKeys.(*fakeKeychain).entries) != 1 {
		t.Fatal("old keychain entry not removed")
	}
	if _, _, err := runCmd(t, "", "logout", "--profile", "old"); err != nil {
		t.Fatal(err)
	}
	if len(systemKeys.(*fakeKeychain).entries) != 0 {
		t.Fatal("logout left keychain entry")
	}
}

func TestLoginKeychainFailurePreservesProfile(t *testing.T) {
	for _, failure := range []string{"set", "get", "config"} {
		t.Run(failure, func(t *testing.T) {
			isolateEnv(t)
			fake := systemKeys.(*fakeKeychain)
			cfg := &config.Config{}
			cfg.SetProfile("old", config.Profile{APIKey: "sk-legacy"})
			if err := cfg.Save(); err != nil {
				t.Fatal(err)
			}
			path, _ := config.Path()
			before, _ := os.ReadFile(path)
			leak := errors.New("backend printed sk-super-secret")
			switch failure {
			case "set":
				fake.setErr = leak
			case "get":
				fake.getErr = leak
			case "config":
				// Fail config replacement after key storage, exercising rollback.
				fake.onSet = func() {
					if err := os.Rename(path, path+".backup"); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(path, 0o700); err != nil {
						t.Fatal(err)
					}
				}
			}
			out, stderr, err := runCmd(t, "sk-super-secret", "login", "--api-key-stdin", "--profile", "old")
			if err == nil {
				t.Fatal("expected storage failure")
			}
			if strings.Contains(out+stderr+err.Error(), "sk-super-secret") {
				t.Fatal("backend error leaked secret")
			}
			if failure == "config" {
				path += ".backup"
			}
			after, _ := os.ReadFile(path)
			if string(after) != string(before) {
				t.Fatal("failed login changed legacy profile")
			}
			if len(fake.entries) != 0 {
				t.Fatal("failed login left new credential")
			}
		})
	}
}

func TestKeychainOverrideAndNoAnonymousFallback(t *testing.T) {
	isolateEnv(t)
	if _, _, err := runCmd(t, "sk-saved", "login", "--api-key-stdin"); err != nil {
		t.Fatal(err)
	}
	fake := systemKeys.(*fakeKeychain)
	fake.getErr = errors.New("locked")
	if _, err := resolveConnection(&globalFlags{}, true); err == nil {
		t.Fatal("unavailable saved key downgraded to anonymous")
	}
	for _, mode := range []string{"flag", "env"} {
		t.Run(mode, func(t *testing.T) {
			g := &globalFlags{}
			if mode == "flag" {
				g.apiKey = "sk-override"
			} else {
				t.Setenv("KIMI_API_KEY", "sk-override")
			}
			c, err := resolveConnection(g, true)
			if err != nil || c.apiKey != "sk-override" {
				t.Fatal("explicit key should bypass locked keychain", err)
			}
		})
	}
}

func TestLoginRequiresTerminalWithoutExplicitKey(t *testing.T) {
	isolateEnv(t)
	for _, args := range [][]string{{"login"}, {"worker", "login", "env_1"}} {
		_, _, err := runCmd(t, "do-not-implicitly-read-pipe", args...)
		if exitCode(err) != 2 {
			t.Fatalf("args %v: want usage error, got %v", args, err)
		}
	}
}

// Login stores the base URL as flag > env > previous profile value > empty.
func TestLoginBaseURLPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name     string
		flag     string // --base-url value; empty means absent
		env      string // KHA_BASE_URL value; empty means unset
		previous string // previous profile value; empty means absent
		want     string
	}{
		{name: "env only", env: "https://env.example", want: "https://env.example"},
		{name: "flag beats env", flag: "https://flag.example", env: "https://env.example", want: "https://flag.example"},
		{name: "env beats previous profile", env: "https://env.example", previous: "https://old.example", want: "https://env.example"},
		{name: "previous profile without flag or env", previous: "https://old.example", want: "https://old.example"},
		{name: "empty without any source"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateEnv(t)
			if tc.previous != "" {
				cfg := &config.Config{}
				cfg.SetProfile("demo", config.Profile{APIKey: "sk-legacy", BaseURL: tc.previous})
				if err := cfg.Save(); err != nil {
					t.Fatal(err)
				}
			}
			if tc.env != "" {
				t.Setenv("KHA_BASE_URL", tc.env)
			}
			args := []string{"login", "--api-key", "sk-new", "--profile", "demo"}
			if tc.flag != "" {
				args = append(args, "--base-url", tc.flag)
			}
			if _, _, err := runCmd(t, "", args...); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.Load()
			if err != nil {
				t.Fatal(err)
			}
			prof := cfg.GetProfile("demo")
			if prof == nil {
				t.Fatal("login did not save the profile")
			}
			if prof.BaseURL != tc.want {
				t.Errorf("stored base_url = %q, want %q", prof.BaseURL, tc.want)
			}
		})
	}
}

// An empty key at login saves the profile without a credential (anonymous
// access) instead of failing, and clears any previously stored key.
func TestLoginEmptyKeySavesAnonymousProfile(t *testing.T) {
	isolateEnv(t)
	if _, _, err := runCmd(t, "", "login", "--api-key", "sk-secret", "--profile", "demo", "--base-url", "https://anon.example"); err != nil {
		t.Fatal(err)
	}
	_, stderr, err := runCmd(t, "", "login", "--api-key", "", "--profile", "demo")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "anonymously") {
		t.Fatalf("empty key login did not warn about anonymous access: %q", stderr)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	prof := cfg.GetProfile("demo")
	if prof == nil || prof.APIKeyRef != "" || prof.APIKey != "" {
		t.Fatal("anonymous login stored a credential")
	}
	if prof.BaseURL != "https://anon.example" {
		t.Fatal("anonymous login lost the previous base URL")
	}
	if len(systemKeys.(*fakeKeychain).entries) != 0 {
		t.Fatal("anonymous login left a keychain entry")
	}
	c, err := resolveConnection(&globalFlags{profile: "demo"}, true)
	if err != nil || c.apiKey != "" {
		t.Fatal("anonymous profile should resolve without a key", err)
	}
}

// Pressing Enter at the hidden prompt takes the same anonymous path.
func TestLoginEmptyPromptSavesAnonymousProfile(t *testing.T) {
	isolateEnv(t)
	stubLoginPrompt(t, "")
	_, stderr, err := runCmd(t, "", "login", "--profile", "demo")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "anonymously") {
		t.Fatalf("empty prompt did not warn about anonymous access: %q", stderr)
	}
	c, err := resolveConnection(&globalFlags{profile: "demo"}, true)
	if err != nil || c.apiKey != "" {
		t.Fatal("anonymous profile should resolve without a key", err)
	}
	if len(systemKeys.(*fakeKeychain).entries) != 0 {
		t.Fatal("anonymous login touched the keychain")
	}
}
