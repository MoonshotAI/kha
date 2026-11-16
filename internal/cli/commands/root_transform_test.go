package commands

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestAgentsCreateTransformRawOutput(t *testing.T) {
	isolateEnv(t)
	f := &fakeAPI{t: t}
	srv := serve(t, f.handler().ServeHTTP)

	stdout, _, err := runCmd(t, "", "agents", "create", "--name", "created", "--model", "kimi-k2",
		"--transform", "id", "-r", "--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "agent_9\n" {
		t.Errorf("stdout = %q, want a raw created id", stdout)
	}
}

func TestAgentsListTransformID(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "table", want: "VALUE\nagent_1\nagent_2\n"},
		{name: "raw strings", args: []string{"-r"}, want: "agent_1\nagent_2\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			isolateEnv(t)
			srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = fmt.Fprint(w, `{"items":[{"id":"agent_1","name":"first","model":"kimi-k3","version":"1","created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-01T10:00:00Z","scope":"project"},{"id":"agent_2","name":"second","model":"kimi-k3","version":"2","created_at":"2026-08-02T10:00:00Z","updated_at":"2026-08-02T10:00:00Z","scope":"project"}],"next_page_token":"tok2"}`)
			})
			args := []string{"agents", "list", "--transform", "id"}
			args = append(args, tc.args...)
			args = append(args, "--base-url", srv.URL, "--api-key", "sk-test")

			stdout, _, err := runCmd(t, "", args...)
			if err != nil {
				t.Fatal(err)
			}
			if stdout != tc.want {
				t.Errorf("stdout = %q, want %q", stdout, tc.want)
			}
		})
	}
}

func TestAgentsListTransformMultipath(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "default table", want: "id       name\nagent_1  demo\n"},
		{name: "explicit table", args: []string{"-o", "table"}, want: "id       name\nagent_1  demo\n"},
		{name: "json", args: []string{"-o", "json"}, want: "[\n  {\n    \"id\": \"agent_1\",\n    \"name\": \"demo\"\n  }\n]\n"},
		{name: "yaml", args: []string{"-o", "yaml"}, want: "- id: agent_1\n  name: demo\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			isolateEnv(t)
			f := &fakeAPI{t: t}
			srv := serve(t, f.handler().ServeHTTP)
			args := []string{"agents", "list", "--transform", "{id,name}"}
			args = append(args, tc.args...)
			args = append(args, "--base-url", srv.URL, "--api-key", "sk-test")

			stdout, _, err := runCmd(t, "", args...)
			if err != nil {
				t.Fatal(err)
			}
			if stdout != tc.want {
				t.Errorf("stdout = %q, want %q", stdout, tc.want)
			}
		})
	}
}

func TestRawOutputSelectsStructuredOutput(t *testing.T) {
	isolateEnv(t)
	f := &fakeAPI{t: t}
	srv := serve(t, f.handler().ServeHTTP)

	stdout, _, err := runCmd(t, "", "agents", "list", "-r", "--base-url", srv.URL, "--api-key", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, `"items"`) || strings.Contains(stdout, "NAME") {
		t.Errorf("-r without a transform must select JSON instead of a table:\n%s", stdout)
	}
}

func TestFilesListAllTransformsAggregatedArray(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "table", want: "VALUE\nfile_1\nfile_2\n"},
		{name: "json", args: []string{"-o", "json"}, want: "[\n  \"file_1\",\n  \"file_2\"\n]\n"},
		{name: "yaml", args: []string{"-o", "yaml"}, want: "- file_1\n- file_2\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			isolateEnv(t)
			srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/files" {
					http.NotFound(w, r)
					return
				}
				if r.URL.Query().Get("page_token") == "tok2" {
					_, _ = fmt.Fprint(w, `{"items":[{"id":"file_2","filename":"b.csv","bytes":7,"created_at":"2026-08-02T10:00:00Z"}]}`)
					return
				}
				_, _ = fmt.Fprint(w, `{"items":[{"id":"file_1","filename":"a.csv","bytes":5,"created_at":"2026-08-01T10:00:00Z"}],"next_page_token":"tok2"}`)
			})
			args := []string{"files", "list", "--all", "--transform", "id"}
			args = append(args, tc.args...)
			args = append(args, "--base-url", srv.URL, "--api-key", "sk-test")

			stdout, _, err := runCmd(t, "", args...)
			if err != nil {
				t.Fatal(err)
			}
			if stdout != tc.want {
				t.Errorf("stdout = %q, want the transformed array from both pages %q", stdout, tc.want)
			}
		})
	}
}

func TestPrintAllRawOutputPreservesPartialStream(t *testing.T) {
	tests := []struct {
		name      string
		rawOutput bool
		transform string
		want      string
	}{
		{name: "raw output streams", rawOutput: true, want: "[\n  1"},
		{name: "transform buffers", transform: "@this", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := NewRootCommand()
			var out strings.Builder
			cmd.SetOut(&out)
			g := &globalFlags{}
			g.response.Format = "table"
			g.response.Transform = tc.transform
			g.response.RawOutput = tc.rawOutput
			seq := func(yield func(int, error) bool) {
				if !yield(1, nil) {
					return
				}
				yield(0, errInputRead)
			}

			err := printAll(cmd, g, nil, seq, nil)
			if err == nil {
				t.Fatal("expected the iterator error")
			}
			if got := out.String(); got != tc.want {
				t.Errorf("partial output = %q, want %q", got, tc.want)
			}
		})
	}
}
