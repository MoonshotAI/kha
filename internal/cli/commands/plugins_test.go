package commands

import (
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"

	json "encoding/json/v2"

	"gopkg.in/yaml.v3"
)

const demoPluginJSON = `{"id":"plg_1","name":"data-tools","display_name":"Data tools","description":"Tools.",` +
	`"version":"1.0.0","created_at":"2026-09-13T10:00:00Z","updated_at":"2026-09-13T10:00:00Z",` +
	`"scope":"project","status":"active","latest_version":"1.0.0"}`

const demoPluginVersionJSON = `{"plugin_id":"plg_1","version":"1.0.0","name":"data-tools",` +
	`"description":"Tools.","created_at":"2026-09-13T10:00:00Z"}`

// writeBundle writes a stand-in bundle file and returns its path.
func writeBundle(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("PK\x03\x04 stand-in bundle"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// multipartFilename reads the uploaded part's filename, proving the bundle
// went out as a multipart file rather than a raw body.
func multipartFilename(t *testing.T, r *http.Request) (string, string) {
	t.Helper()
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("content type %q: %v", r.Header.Get("Content-Type"), err)
	}
	part, err := multipart.NewReader(r.Body, params["boundary"]).NextPart()
	if err != nil {
		t.Fatalf("reading the multipart body: %v", err)
	}
	body, err := io.ReadAll(part)
	if err != nil {
		t.Fatal(err)
	}
	return part.FileName(), string(body)
}

func TestPluginsListScope(t *testing.T) {
	isolateEnv(t)
	var gotQuery string
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/plugins" {
			gotQuery = r.URL.RawQuery
			_, _ = fmt.Fprintf(w, `{"items":[%s]}`, demoPluginJSON)
			return
		}
		http.NotFound(w, r)
	})

	stdout, _, err := runCmd(t, "", "plugins", "list", "--scope", "project",
		"--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotQuery, "scope=project") {
		t.Errorf("query = %q, want scope=project", gotQuery)
	}
	if strings.Contains(stdout, "SOURCE") {
		t.Errorf("obsolete SOURCE column: %s", stdout)
	}
	for _, want := range []string{"SCOPE", "STATUS", "LATEST", "plg_1", "project", "active"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout)
		}
	}
}

func TestPluginsCreateUploadsBundle(t *testing.T) {
	isolateEnv(t)
	var gotName, gotBody string
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/plugins" {
			gotName, gotBody = multipartFilename(t, r)
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `{"plugin":%s,"version":%s,"report":{"ok":true,"issues":[]}}`,
				demoPluginJSON, demoPluginVersionJSON)
			return
		}
		http.NotFound(w, r)
	})

	bundle := writeBundle(t, "data-tools.zip")
	stdout, stderr, err := runCmd(t, "", "plugins", "create", bundle, "-o", "json",
		"--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if gotName != "data-tools.zip" {
		t.Errorf("uploaded filename = %q, want data-tools.zip", gotName)
	}
	if !strings.HasPrefix(gotBody, "PK") {
		t.Errorf("uploaded body = %q, want the bundle bytes", gotBody)
	}
	for _, want := range []string{`"plugin"`, `"version"`, `"report"`, `"plg_1"`} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "DISPLAY NAME") {
		t.Errorf("json output contains table headers:\n%s", stdout)
	}
	if !strings.Contains(stderr, "published at version 1.0.0") {
		t.Errorf("stderr missing the publish note:\n%s", stderr)
	}
}

// TestPluginsValidateReportsFindings exercises the script-facing exit code
// and preserves the report across every output format, including warnings.
func TestPluginsValidateReportsFindings(t *testing.T) {
	for _, tc := range []struct {
		name     string
		ok       bool
		severity string
		wantCode int
	}{
		{"rejected", false, "error", 1},
		{"warnings", true, "warn", 0},
	} {
		for _, format := range []string{"table", "json", "yaml"} {
			t.Run(tc.name+"/"+format, func(t *testing.T) {
				isolateEnv(t)
				synctest.Test(t, func(t *testing.T) {
					srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
						if r.Method == http.MethodPost && r.URL.Path == "/v1/plugins/validate" {
							_, _ = fmt.Fprintf(w, `{"report":{"ok":%t,"issues":[{"code":"manifest.name.pattern","field":"name","severity":%q,"message":"The Plugin name must be kebab-case."}]}}`, tc.ok, tc.severity)
							return
						}
						http.NotFound(w, r)
					})
					bundle := writeBundle(t, "bundle.zip")
					stdout, stderr, err := runCmd(t, "", "plugins", "validate", bundle,
						"--base-url", srv.URL, "--api-key", "sk-test", "-o", format, "--error-output", format)
					printed := captureStderr(t, func() {
						if code := exitCode(err); code != tc.wantCode {
							t.Errorf("exit code = %d, want %d (err=%v)", code, tc.wantCode, err)
						}
					})
					if printed != "" {
						t.Errorf("unexpected second error frame: %q", printed)
					}
					for _, want := range []string{tc.severity, "manifest.name.pattern", "kebab-case"} {
						if !strings.Contains(stdout, want) {
							t.Errorf("stdout missing %q:\n%s", want, stdout)
						}
					}
					if format == "table" {
						if !strings.Contains(stdout, "SEVERITY") {
							t.Errorf("missing findings header: %s", stdout)
						}
					} else {
						var report struct {
							Report struct {
								Ok     bool  `json:"ok" yaml:"ok"`
								Issues []any `json:"issues" yaml:"issues"`
							} `json:"report" yaml:"report"`
						}
						if format == "yaml" {
							if err := yaml.Unmarshal([]byte(stdout), &report); err != nil {
								t.Fatal(err)
							}
						} else if err := json.Unmarshal([]byte(stdout), &report); err != nil {
							t.Fatal(err)
						}
						if report.Report.Ok != tc.ok || len(report.Report.Issues) != 1 {
							t.Errorf("validation report changed: %s", stdout)
						}
					}
					verdict := "cannot be published"
					if tc.ok {
						verdict = "can be published"
					}
					if !strings.Contains(stderr, verdict) {
						t.Errorf("stderr missing %q: %s", verdict, stderr)
					}
				})
			})
		}
	}
}

func TestPluginsVersionsCreateAndList(t *testing.T) {
	isolateEnv(t)
	var gotName string
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/plugins/plg_1/versions":
			gotName, _ = multipartFilename(t, r)
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `{"plugin":%s,"version":%s,"report":{"ok":true,"issues":[]}}`,
				demoPluginJSON, demoPluginVersionJSON)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/plugins/plg_1/versions":
			if r.URL.Query().Get("page_token") == "tok2" {
				_, _ = fmt.Fprint(w, `{"items":[{"plugin_id":"plg_1","version":"0.9.0","name":"data-tools","description":"Older.","created_at":"2026-09-12T10:00:00Z"}]}`)
				return
			}
			_, _ = fmt.Fprintf(w, `{"items":[%s],"next_page_token":"tok2"}`, demoPluginVersionJSON)
		default:
			http.NotFound(w, r)
		}
	})

	bundle := writeBundle(t, "data-tools-1.1.0.zip")
	if _, _, err := runCmd(t, "", "plugins", "versions", "create", "plg_1", bundle,
		"--base-url", srv.URL, "--api-key", "sk-test"); err != nil {
		t.Fatal(err)
	}
	if gotName != "data-tools-1.1.0.zip" {
		t.Errorf("uploaded filename = %q, want data-tools-1.1.0.zip", gotName)
	}

	stdout, _, err := runCmd(t, "", "plugins", "versions", "plg_1", "--all",
		"--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "1.0.0") || !strings.Contains(stdout, "0.9.0") {
		t.Errorf("--all did not drain both pages:\n%s", stdout)
	}
}

func TestPluginsContentAndArchive(t *testing.T) {
	isolateEnv(t)
	archived := false
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/plugins/plg_1/versions/1.0.0/content":
			w.Header().Set("Content-Type", "application/zip")
			_, _ = w.Write([]byte("PK\x03\x04 bundle bytes"))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/plugins/plg_1/archive":
			archived = true
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})

	out := filepath.Join(t.TempDir(), "downloaded.zip")
	if _, _, err := runCmd(t, "", "plugins", "content", "plg_1", "1.0.0", "--out", out,
		"--base-url", srv.URL, "--api-key", "sk-test"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "PK\x03\x04 bundle bytes" {
		t.Errorf("downloaded bytes = %q", data)
	}

	stdout, _, err := runCmd(t, "", "plugins", "content", "plg_1", "1.0.0",
		"--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "PK\x03\x04 bundle bytes" {
		t.Errorf("stdout = %q, want the bundle bytes", stdout)
	}

	if _, _, err := runCmd(t, "", "plugins", "archive", "plg_1",
		"--base-url", srv.URL, "--api-key", "sk-test"); err != nil {
		t.Fatal(err)
	}
	if !archived {
		t.Error("archive did not reach the endpoint")
	}
}

// TestBaseURLPrecedence locks the resolution order --base-url >
// KHA_BASE_URL > the selected profile's base_url.
func TestBaseURLPrecedence(t *testing.T) {
	hit := func(t *testing.T) (string, *string) {
		t.Helper()
		var path string
		srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
			path = r.URL.Path
			_, _ = fmt.Fprint(w, `{"items":[]}`)
		})
		return srv.URL, &path
	}

	t.Run("flag beats env and profile", func(t *testing.T) {
		isolateEnv(t)
		wantURL, wantHit := hit(t)
		envURL, envHit := hit(t)
		profileURL, profileHit := hit(t)
		writeTestProfile(t, profileURL)
		t.Setenv("KHA_BASE_URL", envURL)

		if _, _, err := runCmd(t, "", "plugins", "list", "--base-url", wantURL); err != nil {
			t.Fatal(err)
		}
		if *wantHit != "/v1/plugins" {
			t.Errorf("--base-url was not used (env %q, profile %q)", *envHit, *profileHit)
		}
	})

	t.Run("env beats profile", func(t *testing.T) {
		isolateEnv(t)
		envURL, envHit := hit(t)
		profileURL, profileHit := hit(t)
		writeTestProfile(t, profileURL)
		t.Setenv("KHA_BASE_URL", envURL)

		if _, _, err := runCmd(t, "", "plugins", "list"); err != nil {
			t.Fatal(err)
		}
		if *envHit != "/v1/plugins" {
			t.Errorf("KHA_BASE_URL was not used (profile %q)", *profileHit)
		}
	})

	t.Run("profile is the fallback", func(t *testing.T) {
		isolateEnv(t)
		profileURL, profileHit := hit(t)
		writeTestProfile(t, profileURL)

		if _, _, err := runCmd(t, "", "plugins", "list"); err != nil {
			t.Fatal(err)
		}
		if *profileHit != "/v1/plugins" {
			t.Error("the profile's base_url was not used")
		}
	})
}
