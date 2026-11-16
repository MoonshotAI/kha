package commands

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	kha "github.com/MoonshotAI/kha"
	"github.com/MoonshotAI/kha/internal/cli/output"
)

// docsModel is the stable JSON schema printed by `kha docs -o json`.
// Field order is the emitted key order; do not reorder or rename fields.
// The Markdown view renders from the same model, so both formats always
// tell the same story.
type docsModel struct {
	Name        string       `json:"name"`
	Version     string       `json:"version"`
	APIVersion  string       `json:"api_version"`
	Synopsis    string       `json:"synopsis"`
	Description string       `json:"description,omitempty"`
	Protocol    protocolDoc  `json:"protocol"`
	GlobalFlags []flagDoc    `json:"global_flags"`
	Commands    []commandDoc `json:"commands"`
}

// protocolDoc is the hand-written half of the reference: the global
// contracts that apply to every command, in fixed sections.
type protocolDoc struct {
	Input      []protocolEntry `json:"input"`
	Output     []protocolEntry `json:"output"`
	Auth       []protocolEntry `json:"auth"`
	ExitCodes  []exitCodeDoc   `json:"exit_codes"`
	Completion []protocolEntry `json:"completion"`
	Streaming  []protocolEntry `json:"streaming"`
}

type protocolEntry struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

type exitCodeDoc struct {
	Code    int    `json:"code"`
	Meaning string `json:"meaning"`
}

type commandDoc struct {
	Path    string `json:"path"`
	Use     string `json:"use"`
	Short   string `json:"short"`
	Long    string `json:"long,omitempty"`
	Example string `json:"example,omitempty"`
	// Flags holds the command's local flags only; the persistent flags live
	// once in GlobalFlags instead of repeating on every command.
	Flags    []flagDoc    `json:"flags"`
	Commands []commandDoc `json:"commands,omitempty"`
}

type flagDoc struct {
	Name      string `json:"name"`
	Shorthand string `json:"shorthand,omitempty"`
	Type      string `json:"type"`
	Default   string `json:"default"`
	Usage     string `json:"usage"`
}

// newDocsCmd prints the CLI reference from the in-memory command tree: no
// network, no credential resolution, no config reads. Its -o flag shadows
// the root's persistent -o deliberately — on docs it selects the
// documentation format (markdown or json); on every other command -o keeps
// selecting the API data format (table, json, or yaml).
func newDocsCmd() *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "docs [command-path...]",
		Short: "Print the CLI reference (local; never touches the API)",
		Long: `Print the CLI reference: the input/output contract, authentication,
exit codes, completion, the streaming protocol, and every command and flag
in the tree.

With no arguments the full reference prints. A command path narrows the
output to that command and its subcommands:

  kha docs sessions
  kha docs sessions get -o json

On this command, -o selects the documentation format (markdown or json);
on every other command it selects the API data format (table, json, yaml).

The reference is generated from the local command tree: docs never resolves
credentials, reads the config, or contacts the API.`,
		Args: cobra.ArbitraryArgs,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			switch format {
			case "markdown", "json":
			default:
				return errUsage("invalid --output %q for docs: want markdown or json", format)
			}
			target := cmd.Root()
			if len(args) > 0 {
				found, remaining, err := cmd.Root().Find(args)
				if err != nil || found == nil || len(remaining) != 0 || skipDocsCommand(found) {
					return errUsage("unknown command path %q: run 'kha docs' for the full reference", strings.Join(args, " "))
				}
				target = found
			}
			model := buildDocsModel(target, len(args) == 0)
			if format == "json" {
				return output.PrintJSON(cmd.OutOrStdout(), model)
			}
			_, err := fmt.Fprint(cmd.OutOrStdout(), renderDocsMarkdown(model, len(args) == 0))
			return err
		}),
	}
	cmd.Flags().StringVarP(&format, "output", "o", "markdown", "documentation format: markdown or json (shadows the global data-format -o on this command only)")
	mustRegisterFlagCompletion(cmd, "output", completeFlagStatic("markdown", "json"))
	return cmd
}

// buildDocsModel assembles the reference for target: the whole tree when
// full is set, otherwise just target and its subcommands. The envelope
// (protocol, global flags) is identical either way.
func buildDocsModel(target *cobra.Command, full bool) docsModel {
	root := target.Root()
	model := docsModel{
		Name:        root.Name(),
		Version:     cliVersion(),
		APIVersion:  kha.DefaultAPIVersion,
		Synopsis:    root.Short,
		Description: root.Long,
		Protocol:    cliProtocol(),
		GlobalFlags: flagDocs(root.PersistentFlags()),
	}
	if full {
		for _, c := range root.Commands() {
			if skipDocsCommand(c) {
				continue
			}
			model.Commands = append(model.Commands, commandDocOf(c))
		}
	} else {
		model.Commands = []commandDoc{commandDocOf(target)}
	}
	return model
}

func commandDocOf(cmd *cobra.Command) commandDoc {
	d := commandDoc{
		Path:    cmd.CommandPath(),
		Use:     cmd.Use,
		Short:   cmd.Short,
		Long:    cmd.Long,
		Example: cmd.Example,
		Flags:   flagDocs(cmd.LocalFlags()),
	}
	for _, c := range cmd.Commands() {
		if skipDocsCommand(c) {
			continue
		}
		d.Commands = append(d.Commands, commandDocOf(c))
	}
	return d
}

// skipDocsCommand filters cobra plumbing out of the reference: the builtin
// `help` command (added at execution time) and anything hidden.
func skipDocsCommand(cmd *cobra.Command) bool {
	return cmd.Hidden || cmd.Name() == "help"
}

func flagDocs(fs *pflag.FlagSet) []flagDoc {
	docs := []flagDoc{}
	fs.VisitAll(func(f *pflag.Flag) {
		// The builtin -h/--help flag is cobra plumbing added at execution
		// time, identical on every command: the reference leaves it out,
		// like the builtin help command.
		if f.Hidden || f.Name == "help" {
			return
		}
		d := flagDoc{Name: f.Name, Type: f.Value.Type(), Default: f.DefValue, Usage: f.Usage}
		if f.Shorthand != "" {
			d.Shorthand = f.Shorthand
		}
		docs = append(docs, d)
	})
	return docs
}

// renderDocsMarkdown renders the model: the full reference (global flags,
// protocol, every command) or one targeted command subtree.
func renderDocsMarkdown(m docsModel, full bool) string {
	var b strings.Builder
	if !full {
		for i, c := range flattenPreorder(m.Commands) {
			heading := "##"
			if i == 0 {
				heading = "#"
			}
			writeCommandSection(&b, c, heading)
		}
		return b.String()
	}
	_, _ = fmt.Fprintf(&b, "# %s — %s\n\n", m.Name, m.Synopsis)
	if m.Description != "" {
		_, _ = fmt.Fprintf(&b, "%s\n\n", strings.TrimSpace(m.Description))
	}
	b.WriteString("## Global flags\n\nEvery command accepts these flags.\n\n")
	markdownFlagTable(&b, m.GlobalFlags)
	b.WriteString("\n## Protocol\n\n")
	writeProtocolSection(&b, "Input", m.Protocol.Input)
	writeProtocolSection(&b, "Output", m.Protocol.Output)
	writeProtocolSection(&b, "Authentication", m.Protocol.Auth)
	b.WriteString("### Exit codes\n\n| Code | Meaning |\n| ---- | ------- |\n")
	for _, ec := range m.Protocol.ExitCodes {
		_, _ = fmt.Fprintf(&b, "| %d | %s |\n", ec.Code, mdCell(ec.Meaning))
	}
	b.WriteString("\n")
	writeProtocolSection(&b, "Completion", m.Protocol.Completion)
	writeProtocolSection(&b, "Streaming", m.Protocol.Streaming)
	b.WriteString("## Commands\n\n")
	for _, c := range flattenPreorder(m.Commands) {
		writeCommandSection(&b, c, "###")
	}
	return b.String()
}

// flattenPreorder lists command subtrees depth-first, parents before
// children, so the Markdown stays a flat list of sections — the full path
// in each heading already conveys the nesting.
func flattenPreorder(docs []commandDoc) []commandDoc {
	var out []commandDoc
	var walk func(ds []commandDoc)
	walk = func(ds []commandDoc) {
		for _, d := range ds {
			out = append(out, d)
			walk(d.Commands)
		}
	}
	walk(docs)
	return out
}

func writeCommandSection(b *strings.Builder, d commandDoc, heading string) {
	_, _ = fmt.Fprintf(b, "%s `%s`\n\n", heading, d.Path)
	if d.Short != "" {
		_, _ = fmt.Fprintf(b, "%s\n\n", d.Short)
	}
	if d.Long != "" {
		_, _ = fmt.Fprintf(b, "%s\n\n", strings.TrimSpace(d.Long))
	}
	_, _ = fmt.Fprintf(b, "Usage: `%s`\n\n", usageLine(d))
	if len(d.Flags) == 0 {
		b.WriteString("Only the global flags apply.\n\n")
	} else {
		b.WriteString("Flags:\n\n")
		markdownFlagTable(b, d.Flags)
		b.WriteString("\n")
	}
	if d.Example != "" {
		_, _ = fmt.Fprintf(b, "Examples:\n\n```\n%s\n```\n\n", strings.Trim(d.Example, "\n"))
	}
}

// usageLine renders the full usage line: the command path plus the
// positional arguments from Use ("get <id>" under "kha sessions" becomes
// "kha sessions get <id>").
func usageLine(d commandDoc) string {
	if i := strings.IndexByte(d.Use, ' '); i >= 0 {
		return d.Path + d.Use[i:]
	}
	return d.Path
}

func markdownFlagTable(b *strings.Builder, flags []flagDoc) {
	b.WriteString("| Flag | Type | Default | Description |\n")
	b.WriteString("| ---- | ---- | ------- | ----------- |\n")
	for _, f := range flags {
		name := "--" + f.Name
		if f.Shorthand != "" {
			name = "-" + f.Shorthand + ", " + name
		}
		_, _ = fmt.Fprintf(b, "| `%s` | %s | `%s` | %s |\n", name, f.Type, f.Default, mdCell(f.Usage))
	}
}

// mdCell makes a string safe for a Markdown table cell.
func mdCell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	return strings.ReplaceAll(s, "\n", " ")
}

func writeProtocolSection(b *strings.Builder, title string, entries []protocolEntry) {
	_, _ = fmt.Fprintf(b, "### %s\n\n", title)
	for _, e := range entries {
		_, _ = fmt.Fprintf(b, "- **%s**: %s\n", e.Title, e.Body)
	}
	b.WriteString("\n")
}

// cliProtocol is the hand-written global contract. Keep it in sync with the
// helpers it describes: bodyFlags/textFlags/readInputFile/readSecret (input), edit.go's
// runEdit (editor edits), PrintResponse/PrintAll/StreamPrinter and note (output), resolveConnection
// (auth), exitCode (exit codes), completion.go, and the sessions streaming
// commands.
func cliProtocol() protocolDoc {
	return protocolDoc{
		Input: []protocolEntry{
			{"Field flags or --file", "On create and update commands that take a request body, pass either individual field flags or `--file` with a complete JSON/YAML document; the two styles cannot be combined. On `files upload`, `--file` instead selects the local file to upload."},
			{"stdin", "Pass `--file -` to read a JSON/YAML request body from stdin. Every field-specific file flag also accepts `-` to read that field's content from stdin."},
			{"Field content files", "Use `--system-file` on Agent create/update, `--text-file` on Session send, `--payload-file` on Trigger run, and `--content-file` (or `--content-stdin`) on Memory create/update. Content is read verbatim, preserving trailing newlines and empty files. Memory content accepts raw bytes and is base64-encoded automatically by the CLI."},
			{"Session text", "`sessions send` rejects empty or whitespace-only text from any input source before sending an API request (exit 2). To prompt a Session to continue, send an explicit message such as `Continue.`."},
			{"Configuration inputs", "Use scalar flags for common fields, or repeatable `--skill ID` and `--plugin ID` on Agents (each replaces the stored list on update). Use `--file` (JSON/YAML) for nested configuration such as versioned bindings, clearing arrays/maps, model parameters, tools, MCP servers, metadata, and Session overrides."},
			{"Input sources", "Text flags and positional text arguments are always treated as literal strings, never file paths. Provide at most one source per field (for example, either positional text or `--text-file` on `sessions send <id> [text]`). Flags for different fields can be combined freely. Invalid flag combinations exit 2 and file/stdin read errors exit 1 before any API call."},
			{"Secrets", "Pass secrets via stdin so they never appear in shell history or process listings: `kha login --api-key-stdin` and `kha vaults credentials create --value-stdin`. Trailing newlines (`\\r\\n` or `\\n`) are stripped."},
			{"Interactive editor", "`edit` opens mutable resource fields as YAML in `$KHA_EDITOR`, `$VISUAL`, `$EDITOR`, or `vi`. Saving changes applies the update; saving without changes or saving an empty file cancels. YAML syntax errors or 4xx API rejections reopen the buffer with the error prepended as comments."},
		},
		Output: []protocolEntry{
			{"stdout is data only", "Data commands write only data to stdout: a human-readable table by default, byte-exact API JSON with `-o json`, or YAML with `-o yaml`. Passing `-r` / `--raw-output` without `--transform` switches default `table` output to JSON. Progress notes, warnings, spinners, and errors always go to stderr."},
			{"Response transforms", "`--transform <path>` applies a GJSON path before rendering table, JSON, or YAML output. On list commands, field paths and multipaths apply per item, while collection queries (`#(...)`) apply to the item array; in watch mode, the path applies to each event. Unmatched paths leave the response or item unchanged."},
			{"Transformed tables", "When `--transform` is used with table output, scalar values render under a `VALUE` column, objects use their JSON keys as columns, arrays render one row per element, and watch mode prints one table per event."},
			{"Raw strings", "`-r` / `--raw-output` prints strings and string arrays without surrounding JSON quotes, one value per line (similar to `jq -r`). Non-string values keep the selected output format."},
			{"Structured errors", "`--error-output json` or `--error-output yaml` formats runtime errors on stderr as a structured object with an `error` field."},
			{"Offline reference", "`kha docs` prints Markdown by default or a machine-readable JSON schema with `-o json`."},
		},
		Auth: []protocolEntry{
			{"Explicit profile", "When `--profile <name>` is passed, data commands use that profile's saved settings and ignore `KIMI_API_KEY` and `KHA_BASE_URL` so ambient environment variables do not leak across profiles. Explicit `--api-key`, `--base-url`, and `--api-version` flags still take highest precedence."},
			{"Implicit profile", "Without `--profile`, the active profile comes from `KHA_PROFILE` or the config file's `current` entry. Credentials resolve as `--api-key` > `KIMI_API_KEY` > profile key; endpoints resolve as `--base-url` > `KHA_BASE_URL` > profile base URL > `https://api.moonshot.cn`."},
			{"Profiles and login", "`kha login` stores your API key in the system keychain and saves connection settings in the profile (`--base-url` > `KHA_BASE_URL` > previous value; `--api-version` > previous value). It reads the key from a hidden prompt or `--api-key-stdin` and migrates legacy plaintext profiles automatically. Entering an empty key at the prompt or `--api-key ''` saves an anonymous profile (`--api-key-stdin` requires a non-empty key). Run `kha whoami` to verify credentials and `kha logout` to remove a profile. Worker login manages separate per-environment work keys via `--token-file` or `--token-stdin`."},
			{"Anonymous mode", "If no credential is configured, commands still execute and send unauthenticated requests (the production API returns HTTP 401)."},
		},
		ExitCodes: []exitCodeDoc{
			{0, "success (`plugins validate` also exits 0 when valid, including with warnings)"},
			{1, "runtime, API, or validation failure (the command ran and failed; `plugins validate` still writes findings to stdout)"},
			{2, "usage error (invalid flags or arguments; no API request was sent)"},
			{130, "interrupted by SIGINT/SIGTERM (pressing Ctrl-C on `sessions attach` detaches locally while the remote turn keeps running)"},
		},
		Completion: []protocolEntry{
			{"Script", "`kha completion <bash|zsh|fish|powershell>` prints the shell completion script for your shell."},
			{"Dynamic candidates", "Positional resource IDs complete live against the API using your active credentials, showing each `id` with a human-readable description. Nested commands (`sessions resources get`, `sessions threads get`, `sessions pending retract`, `sessions fs download`) complete the Session ID first and then the nested resource ID or file path. Completion never prints errors to the terminal or falls back to local filenames on API failure."},
			{"Flag values", "Enum flags (`-o`, `--status`, `--order`, `--purpose`, etc.) complete their allowed values, including comma-separated lists such as `sessions list --status idle,running`. Resource flags (`--profile`, `--agent`, `--environment`, `--thread`, `--prefix`, etc.) complete from local config or the live API, while file flags (`--file`, `--out`, `--*-file`) use standard shell file completion."},
		},
		Streaming: []protocolEntry{
			{"Live stream", "`kha sessions attach <id>` streams live Session events as plain text: `agent.delta` chunks append in place, `agent.message` prints the authoritative message body without duplicating already-streamed text, model thinking collapses to a summary line, and tool calls print as concise one-line traces. Status transitions and errors are written to stderr."},
			{"Detaching vs interrupting", "Pressing Ctrl-C during `attach` or `events --watch` detaches the local terminal (exit 130) while the Session continues running in the cloud. Use `kha sessions interrupt <id>` to stop the active remote turn."},
			{"History", "`kha sessions events <id>` (and `sessions threads events`) pages persisted event history from oldest to newest. Each page includes `stream_cursor` (shown as `CURSOR` in table output) for resuming a live stream with `--watch --since`."},
			{"Watch mode", "`kha sessions events <id> --watch` (`-w`) prints persisted history and seamlessly transitions to the live SSE stream using the last page's `stream_cursor` (or `now` when history is empty). Output formatting stays consistent across both phases: a single table header without `--transform` (or per-event tables with `--transform`), NDJSON (one compact JSON event per line) with `-o json`, and `---`-separated documents with `-o yaml`. Duplicate events across the history/live boundary are filtered out by event ID. Combine with `--all` to drain all history pages first, or `--since <cursor|now>` to start from a specific cursor without paging history (`--order desc` cannot be used with `--watch`)."},
			{"Resilience", "Live streams (`attach` and `events --watch`) automatically handle heartbeats, reconnect with `Last-Event-ID`, deduplicate events by payload ID, and recover from expired cursors by re-anchoring at `now` and backfilling missed history."},
		},
	}
}
