package commands

import (
	"strings"
	"testing"

	json "encoding/json/v2"

	"github.com/spf13/cobra"

	kha "github.com/MoonshotAI/kha"
)

// docsTestModel mirrors the stable `kha docs -o json` schema; a field
// rename or drop in docs.go fails to populate it here.
type docsTestModel struct {
	Name       string `json:"name"`
	Version    string `json:"version"`
	APIVersion string `json:"api_version"`
	Synopsis   string `json:"synopsis"`
	Protocol   struct {
		Input     []docsTestEntry `json:"input"`
		Output    []docsTestEntry `json:"output"`
		Auth      []docsTestEntry `json:"auth"`
		ExitCodes []struct {
			Code    int    `json:"code"`
			Meaning string `json:"meaning"`
		} `json:"exit_codes"`
		Completion []docsTestEntry `json:"completion"`
		Streaming  []docsTestEntry `json:"streaming"`
	} `json:"protocol"`
	GlobalFlags []docsTestFlag    `json:"global_flags"`
	Commands    []docsTestCommand `json:"commands"`
}

type docsTestEntry struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

type docsTestFlag struct {
	Name      string `json:"name"`
	Shorthand string `json:"shorthand"`
	Type      string `json:"type"`
	Default   string `json:"default"`
	Usage     string `json:"usage"`
}

type docsTestCommand struct {
	Path     string            `json:"path"`
	Use      string            `json:"use"`
	Short    string            `json:"short"`
	Long     string            `json:"long"`
	Example  string            `json:"example"`
	Flags    []docsTestFlag    `json:"flags"`
	Commands []docsTestCommand `json:"commands"`
}

func TestDocsFullMarkdownCoversProtocolAndTree(t *testing.T) {
	isolateEnv(t)
	stdout, stderr, err := runCmd(t, "", "docs")
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty (docs is local; no anonymous note)", stderr)
	}
	for _, want := range []string{
		"# kha — Command-line client for the Kimi Hosted Agents API",
		"## Global flags",
		"## Protocol",
		"### Input",
		"--file -",
		"Field content files",
		"stdin",
		"### Output",
		"### Authentication",
		"KIMI_API_KEY",
		"### Exit codes",
		"| 130 |",
		"### Completion",
		"kha completion <bash|zsh|fish|powershell>",
		"### Streaming",
		"agent.delta",
		"stream_cursor",
		"## Commands",
		"### `kha agents`",
		"### `kha sessions get`",
		"### `kha docs`",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("full docs markdown missing %q", want)
		}
	}
	if strings.Contains(stdout, "kha help") {
		t.Error("the cobra builtin help command leaked into the reference")
	}
}

func TestDocsTargetedLeafMarkdown(t *testing.T) {
	isolateEnv(t)
	stdout, _, err := runCmd(t, "", "docs", "sessions", "get")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"# `kha sessions get`",
		"Get one Session with its resource bindings",
		"Usage: `kha sessions get <id>`",
		"Only the global flags apply.",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("targeted docs missing %q:\n%s", want, stdout)
		}
	}
	for _, unwanted := range []string{"kha sessions list", "kha agents", "## Global flags", "## Protocol"} {
		if strings.Contains(stdout, unwanted) {
			t.Errorf("targeted docs must not contain %q:\n%s", unwanted, stdout)
		}
	}
}

func TestDocsTargetedParentMarkdown(t *testing.T) {
	isolateEnv(t)
	stdout, _, err := runCmd(t, "", "docs", "sessions")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# `kha sessions`", "## `kha sessions send`", "## `kha sessions resources list`"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("parent docs missing %q", want)
		}
	}
	if strings.Contains(stdout, "kha agents") {
		t.Error("parent docs leaked a sibling subtree")
	}
}

func TestDocsUnknownPathIsUsageError(t *testing.T) {
	isolateEnv(t)
	for _, args := range [][]string{{"sessions", "nope"}, {"help"}} {
		_, _, err := runCmd(t, "", append([]string{"docs"}, args...)...)
		if err == nil {
			t.Fatalf("docs %v: expected a usage error", args)
		}
		if !strings.Contains(err.Error(), "unknown command path") {
			t.Errorf("docs %v: error = %q, want unknown command path", args, err)
		}
		if code := exitCode(err); code != exitUsage {
			t.Errorf("docs %v: exitCode = %d, want %d", args, code, exitUsage)
		}
	}
}

func TestDocsJSONEnvelopeAndGlobalFlagDedup(t *testing.T) {
	isolateEnv(t)
	stdout, _, err := runCmd(t, "", "docs", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var m docsTestModel
	if err := json.Unmarshal([]byte(stdout), &m); err != nil {
		t.Fatalf("docs -o json does not parse: %v\n%s", err, stdout)
	}
	if m.Name != "kha" {
		t.Errorf("name = %q", m.Name)
	}
	if m.Version != version {
		t.Errorf("version = %q, want %q", m.Version, version)
	}
	if m.APIVersion != kha.DefaultAPIVersion {
		t.Errorf("api_version = %q, want %q", m.APIVersion, kha.DefaultAPIVersion)
	}
	if m.Synopsis != "Command-line client for the Kimi Hosted Agents API" {
		t.Errorf("synopsis = %q", m.Synopsis)
	}
	if len(m.Protocol.ExitCodes) != 4 {
		t.Fatalf("exit_codes = %v", m.Protocol.ExitCodes)
	}
	for i, code := range []int{0, 1, 2, 130} {
		if m.Protocol.ExitCodes[i].Code != code {
			t.Errorf("exit_codes[%d] = %d, want %d", i, m.Protocol.ExitCodes[i].Code, code)
		}
	}
	for _, section := range [][]docsTestEntry{m.Protocol.Input, m.Protocol.Output, m.Protocol.Auth, m.Protocol.Completion, m.Protocol.Streaming} {
		if len(section) == 0 {
			t.Error("protocol section is empty")
		}
	}
	wantGlobals := map[string]string{
		"api-key": "", "api-version": "", "base-url": "", "error-output": "", "output": "o", "profile": "",
		"raw-output": "r", "transform": "",
	}
	if len(m.GlobalFlags) != len(wantGlobals) {
		t.Errorf("global_flags = %v", m.GlobalFlags)
	}
	for _, f := range m.GlobalFlags {
		shorthand, ok := wantGlobals[f.Name]
		if !ok {
			t.Errorf("unexpected global flag %q", f.Name)
			continue
		}
		if f.Shorthand != shorthand {
			t.Errorf("global flag %q shorthand = %q, want %q", f.Name, f.Shorthand, shorthand)
		}
		delete(wantGlobals, f.Name)
	}
	for name := range wantGlobals {
		t.Errorf("global flag %q missing", name)
	}
	// Dedup: the persistent --api-key is documented exactly once, in
	// global_flags — never repeated per command.
	if n := strings.Count(stdout, `"api-key"`); n != 1 {
		t.Errorf("%q appears %d times in the JSON, want exactly 1 (global flags are deduplicated)", `"api-key"`, n)
	}
	// Local flags only: no command re-lists a persistent flag. The one
	// deliberate exception is docs' own -o shadow.
	globals := map[string]bool{"api-key": true, "api-version": true, "base-url": true, "error-output": true, "output": true, "profile": true, "raw-output": true, "transform": true}
	var walk func(cs []docsTestCommand)
	walk = func(cs []docsTestCommand) {
		for _, c := range cs {
			for _, f := range c.Flags {
				if globals[f.Name] && (c.Path != "kha docs" || f.Name != "output") {
					t.Errorf("command %s re-lists persistent flag --%s", c.Path, f.Name)
				}
			}
			walk(c.Commands)
		}
	}
	walk(m.Commands)
	// docs documents itself, including its shadowing -o.
	var docsCmd *docsTestCommand
	for i := range m.Commands {
		if m.Commands[i].Path == "kha docs" {
			docsCmd = &m.Commands[i]
		}
	}
	if docsCmd == nil {
		t.Fatal("kha docs missing from its own reference")
	}
	if len(docsCmd.Flags) != 1 || docsCmd.Flags[0].Name != "output" || docsCmd.Flags[0].Default != "markdown" {
		t.Errorf("docs flags = %+v, want the local markdown/json -o", docsCmd.Flags)
	}
}

func TestDocsJSONTargetedLeaf(t *testing.T) {
	isolateEnv(t)
	stdout, _, err := runCmd(t, "", "docs", "sessions", "get", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var m docsTestModel
	if err := json.Unmarshal([]byte(stdout), &m); err != nil {
		t.Fatalf("docs sessions get -o json does not parse: %v", err)
	}
	if len(m.Commands) != 1 {
		t.Fatalf("commands = %d, want exactly the targeted leaf", len(m.Commands))
	}
	c := m.Commands[0]
	if c.Path != "kha sessions get" {
		t.Errorf("path = %q", c.Path)
	}
	if c.Use != "get <id>" {
		t.Errorf("use = %q", c.Use)
	}
	if c.Short != "Get one Session with its resource bindings" {
		t.Errorf("short = %q", c.Short)
	}
	if len(c.Flags) != 0 {
		t.Errorf("flags = %+v, want none (only global flags apply)", c.Flags)
	}
	if !strings.Contains(stdout, `"flags": []`) {
		t.Error("an empty local flag set must render as [], not null or omitted")
	}
	if len(c.Commands) != 0 {
		t.Errorf("a leaf must have no subcommands, got %v", c.Commands)
	}
}

func TestDocsRendersEveryLeaf(t *testing.T) {
	isolateEnv(t)
	root := NewRootCommand()
	var leaves []string
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		var visible []*cobra.Command
		for _, ch := range c.Commands() {
			if !skipDocsCommand(ch) {
				visible = append(visible, ch)
			}
		}
		if len(visible) == 0 && c != root {
			leaves = append(leaves, c.CommandPath())
		}
		for _, ch := range visible {
			walk(ch)
		}
	}
	walk(root)
	if len(leaves) == 0 {
		t.Fatal("no leaf commands found")
	}
	for _, path := range leaves {
		args := append([]string{"docs"}, strings.Split(strings.TrimPrefix(path, "kha "), " ")...)
		stdout, _, err := runCmd(t, "", append(args, "-o", "json")...)
		if err != nil {
			t.Errorf("docs %s -o json: %v", path, err)
			continue
		}
		if !strings.Contains(stdout, `"path": "`+path+`"`) {
			t.Errorf("docs %s -o json: output missing the command's own path", path)
		}
		stdout, _, err = runCmd(t, "", args...)
		if err != nil {
			t.Errorf("docs %s: %v", path, err)
			continue
		}
		if !strings.Contains(stdout, "# `"+path+"`") {
			t.Errorf("docs %s: markdown missing the command heading", path)
		}
	}
	t.Logf("rendered %d leaf commands in both formats", len(leaves))
}

func TestDocsOutputFlagShadowSemantics(t *testing.T) {
	isolateEnv(t)
	// docs' own -o accepts only the documentation formats.
	for _, bad := range []string{"table", "yaml"} {
		_, _, err := runCmd(t, "", "docs", "-o", bad)
		if err == nil || !strings.Contains(err.Error(), "want markdown or json") {
			t.Errorf("docs -o %s: error = %v, want a markdown-or-json usage error", bad, err)
			continue
		}
		if code := exitCode(err); code != exitUsage {
			t.Errorf("docs -o %s: exitCode = %d, want %d", bad, code, exitUsage)
		}
	}
	// The shadowing binds even when -o precedes the subcommand.
	stdout, _, err := runCmd(t, "", "-o", "json", "docs")
	if err != nil {
		t.Fatalf("-o json docs: %v", err)
	}
	var m docsTestModel
	if err := json.Unmarshal([]byte(stdout), &m); err != nil {
		t.Fatalf("-o json docs did not print JSON: %v", err)
	}
	// Everywhere else, -o keeps its data-format meaning.
	stdout, _, err = runCmd(t, "", "version", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, `"api_version"`) {
		t.Errorf("version -o json no longer prints the data model:\n%s", stdout)
	}
}

func TestDocsNeverResolvesCredentials(t *testing.T) {
	isolateEnv(t)
	// An explicit but nonexistent profile fails any credential resolution;
	// docs must not even try.
	t.Setenv("KHA_PROFILE", "does-not-exist")
	stdout, stderr, err := runCmd(t, "", "docs")
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty: docs reads no config and prints no anonymous note", stderr)
	}
	if !strings.Contains(stdout, "# kha") {
		t.Error("docs did not render the reference")
	}
}
