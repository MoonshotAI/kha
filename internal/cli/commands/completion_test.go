package commands

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestCompletionCandidate(t *testing.T) {
	for _, tc := range []struct{ id, description, want string }{
		{"agent_1", "name", "agent_1\tname"},
		{"agent_1", "", "agent_1"},
		{"id\nforged\r\t\x1b[31mred\x1b[0m", "name\n\r\t", "id forged  red\tname   "},
		{"id\nforged", "", "id forged"},
	} {
		if got := completionCandidate(tc.id, tc.description); got != tc.want {
			t.Errorf("completionCandidate(%q, %q) = %q, want %q", tc.id, tc.description, got, tc.want)
		}
	}
}

func TestDynamicCompletionSkipsNullItems(t *testing.T) {
	isolateEnv(t)
	srv := serve(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprint(w, `{"items":[null]}`) })
	writeTestProfile(t, srv.URL)
	for _, tc := range []struct {
		path string
		args []string
	}{
		{"agents get", nil}, {"sessions get", nil}, {"vaults get", nil}, {"triggers get", nil},
		{"environments get", nil}, {"plugins get", nil}, {"memory-stores get", nil},
		{"skills get", nil}, {"artifacts get", nil}, {"files get", nil},
		{"sessions threads get", []string{"sess_1"}}, {"sessions resources get", []string{"sess_1"}},
		{"vaults credentials get", []string{"vlt_1"}}, {"triggers runs get", []string{"trg_1"}},
		{"memory-stores memories get", []string{"ms_1"}},
		{"plugins content", []string{"plugin_1"}}, {"skills content", []string{"skill_1"}},
	} {
		t.Run(tc.path, func(t *testing.T) {
			root := NewRootCommand()
			cmd, _, err := root.Find(strings.Fields(tc.path))
			if err != nil {
				t.Fatal(err)
			}
			cmd.SetContext(t.Context())
			if cmd.ValidArgsFunction == nil {
				t.Fatalf("%s has no completion", tc.path)
			}
			got, directive := cmd.ValidArgsFunction(cmd, tc.args, "")
			if len(got) != 0 || directive != cobra.ShellCompDirectiveNoFileComp {
				t.Fatalf("candidates=%v directive=%v", got, directive)
			}
		})
	}
	for _, tc := range []struct {
		path, flag string
		args       []string
	}{
		{"artifacts get", "version", []string{"arti_1"}},
	} {
		t.Run(tc.path+"/"+tc.flag, func(t *testing.T) {
			cmd, fn := flagCompletion(t, tc.path, tc.flag)
			got, directive := fn(cmd, tc.args, "")
			if len(got) != 0 || directive != cobra.ShellCompDirectiveNoFileComp {
				t.Fatalf("candidates=%v directive=%v", got, directive)
			}
		})
	}
}
