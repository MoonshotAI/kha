package commands

import (
	"fmt"
	"io"
	"iter"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	json "encoding/json/v2"

	"github.com/schollz/progressbar/v3"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	kha "github.com/MoonshotAI/kha"
	"github.com/MoonshotAI/kha/internal/cli/output"
)

// usageError marks bad invocation input (missing required flags, invalid
// combinations) so Execute can exit 2 even when raised inside a RunE.
type usageError struct{ err error }

func (e *usageError) Error() string { return e.err.Error() }
func (e *usageError) Unwrap() error { return e.err }

// errUsage builds a usage error.
func errUsage(format string, args ...any) error {
	return &usageError{fmt.Errorf(format, args...)}
}

func wrapArgumentErrors(root *cobra.Command) {
	var walk func(*cobra.Command)
	walk = func(cmd *cobra.Command) {
		if args := cmd.Args; args != nil {
			cmd.Args = func(cmd *cobra.Command, values []string) error {
				if err := args(cmd, values); err != nil {
					details := fmt.Sprintf("%s\nhint: run '%s --help' for more information", err, cmd.CommandPath())
					return &usageError{fmt.Errorf("%s", details)}
				}
				return nil
			}
		}
		for _, child := range cmd.Commands() {
			walk(child)
		}
	}
	walk(root)
}

// run wraps a command body so its failures are recognized as runtime errors
// (exit 1) rather than usage errors (exit 2).
func run(fn func(cmd *cobra.Command, args []string) error) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		if err := fn(cmd, args); err != nil {
			format := "table"
			if f := cmd.Flags().Lookup("error-output"); f != nil {
				format = f.Value.String()
			}
			return &runtimeError{err: err, errorOutput: format}
		}
		return nil
	}
}

// listFlags are the paging flags shared by every list verb. Listing fetches
// a single page; -o json exposes the raw next_page_token for scripting.
// --all drains the whole listing through the SDK's All iterator instead.
type listFlags struct {
	limit     int
	pageToken string
	all       bool
}

// addListFlags registers --limit/--page-token/--all and applies them to any
// of the generated list params structs (they all share PageSize/PageToken
// fields).
func addListFlags(cmd *cobra.Command) *listFlags {
	f := &listFlags{}
	cmd.Flags().IntVar(&f.limit, "limit", 0, "maximum number of items to return (server default 50)")
	cmd.Flags().StringVar(&f.pageToken, "page-token", "", "continue from a previous listing's next_page_token")
	cmd.Flags().BoolVarP(&f.all, "all", "a", false, "fetch every page (cannot combine with --limit/--page-token)")
	return f
}

// validate enforces the --all / single-page mutual exclusion.
func (f *listFlags) validate() error {
	if f.all && (f.limit > 0 || f.pageToken != "") {
		return errUsage("--all cannot be combined with --limit or --page-token")
	}
	return nil
}

// pageSize projects --limit onto the generated params' PageSize
// field, absent when the flag was unset.
func (f *listFlags) pageSize() *int32 {
	if f.limit <= 0 {
		return nil
	}
	return new(int32(f.limit))
}

// token projects --page-token onto the generated params' PageToken
// field, absent when the flag was unset.
func (f *listFlags) token() *string {
	if f.pageToken == "" {
		return nil
	}
	return new(f.pageToken)
}

func printAll[T any](cmd *cobra.Command, g *globalFlags, headers []string, seq iter.Seq2[T, error], row func(T) []string) error {
	return output.PrintAll(cmd.OutOrStdout(), g.response, output.List[T]{
		Headers: headers,
		Items:   seq,
		Row:     row,
	})
}

func printOne(cmd *cobra.Command, g *globalFlags, headers []string, row []string, model any) error {
	return output.PrintResponse(cmd.OutOrStdout(), output.Response{
		Model:   model,
		Headers: headers,
		Rows:    [][]string{row},
	}, g.response)
}

func printList(cmd *cobra.Command, g *globalFlags, headers []string, rows [][]string, model any, itemsPath ...string) error {
	path := "items"
	if len(itemsPath) > 0 {
		path = itemsPath[0]
	}
	return output.PrintResponse(cmd.OutOrStdout(), output.Response{
		Model:     model,
		Headers:   headers,
		Rows:      rows,
		ItemsPath: path,
	}, g.response)
}

// note prints a progress/confirmation line to stderr (never stdout, which is
// reserved for data). The fully formatted line passes through
// output.StripEscapes: interpolated values can carry server- or
// agent-controlled text (file names, API error messages) and must not
// inject terminal control sequences into the user's tty; the format
// constants themselves never contain ESC.
func note(cmd *cobra.Command, format string, args ...any) {
	_, _ = fmt.Fprintln(cmd.ErrOrStderr(), output.StripEscapes(fmt.Sprintf(format, args...)))
}

// bodyFlags implement the create/update input contract: either field
// flags or --file carrying a JSON/YAML request body ("-" reads stdin).
type bodyFlags struct {
	file string
}

// addBodyFlag registers --file on a create/update command.
func addBodyFlag(cmd *cobra.Command, fieldFlags ...string) *bodyFlags {
	b := &bodyFlags{}
	cmd.Flags().StringVarP(&b.file, "file", "f", "", `read the request body as JSON or YAML from a file ("-" reads stdin); cannot combine with field flags`)
	for _, name := range fieldFlags {
		cmd.MarkFlagsMutuallyExclusive("file", name)
	}
	return b
}

// decode reads the JSON or YAML request body from the --file source
// ("-" = stdin) into out.
func (b *bodyFlags) decode(cmd *cobra.Command, out any) error {
	data, err := readInputFile(cmd, "file", b.file)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, out); err != nil {
		mapped, yamlErr := yamlToJSON(data)
		if yamlErr != nil {
			return fmt.Errorf("decoding %s request body: JSON: %w; YAML: %w", b.file, err, yamlErr)
		}
		if err := json.Unmarshal(mapped, out); err != nil {
			return fmt.Errorf("decoding %s as JSON/YAML request body: %w", b.file, err)
		}
	}
	return nil
}

// yamlToJSON converts a YAML document to JSON through the generic value
// tree, so struct decoding follows the json tags. Decoding YAML straight
// into the structs instead would match yaml.v3's lowercased Go field names
// and silently drop snake_case keys such as display_name or agent_id.
func yamlToJSON(data []byte) ([]byte, error) {
	var v any
	if err := yaml.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

// decodeValue decodes one JSON or YAML flag value into out through the same
// yamlToJSON path as --file bodies, so structured flag values accept relaxed
// YAML ({name: x}) and strict JSON interchangeably. Variant structs whose
// discriminator has a single sensible default get it filled in when absent,
// so `--mcp-server '{name: gh, url: ...}'` need not repeat `type: url`.
func decodeValue(flag, raw string, out any) error {
	mapped, err := yamlToJSON([]byte(raw))
	if err != nil {
		return errUsage("decoding --%s: %v", flag, err)
	}
	if err := json.Unmarshal(mapped, out); err != nil {
		return errUsage("decoding --%s: %v", flag, err)
	}
	defaultVariantFields(out)
	return nil
}

// defaultVariantFields fills single-default discriminator fields the value
// omitted, matching what the API itself assumes for those variants.
func defaultVariantFields(out any) {
	switch v := out.(type) {
	case *kha.MCPServer:
		if v.Type == "" {
			v.Type = kha.MCPServerTypeURL
		}
	}
}

// yamlValue is a single-value flag holding one JSON or YAML document
// (--multiagent '{agents: [...]}'); a second occurrence is an error.
type yamlValue struct {
	flag   string
	target any
	set    bool
}

func (y *yamlValue) Type() string { return "value" }

func (y *yamlValue) String() string { return "" }

func (y *yamlValue) Set(raw string) error {
	if y.set {
		return errUsage("--%s given twice; it holds a single value", y.flag)
	}
	y.set = true
	return decodeValue(y.flag, raw, y.target)
}

// yamlValues is a repeatable flag whose values are JSON or YAML documents
// ({...}); each occurrence appends one decoded element to a request array
// (ant-style --tool '{type: custom, ...}').
type yamlValues struct {
	flag   string
	target any
}

// addYAMLFlags registers one repeatable structured flag per (name, target)
// pair; target must be a pointer to a slice of pointers to structs.
func addYAMLFlags(cmd *cobra.Command, usage string, fields ...struct {
	name   string
	target any
}) []*yamlValues {
	out := make([]*yamlValues, 0, len(fields))
	for _, f := range fields {
		y := &yamlValues{flag: f.name, target: f.target}
		cmd.Flags().Var(y, f.name, usage)
		out = append(out, y)
	}
	return out
}

func (y *yamlValues) Type() string { return "value" }

func (y *yamlValues) String() string { return "" }

func (y *yamlValues) Set(raw string) error {
	rv := reflect.ValueOf(y.target).Elem()
	elem := reflect.New(rv.Type().Elem().Elem())
	if err := decodeValue(y.flag, raw, elem.Interface()); err != nil {
		return err
	}
	rv.Set(reflect.Append(rv, elem))
	return nil
}

// kvFlags is a repeatable key=value flag building a string map
// (--metadata team=infra --metadata env=prod). Keys must be unique.
type kvFlags struct {
	flag string
	out  map[string]string
}

// addKVFlags registers one repeatable key=value flag.
func addKVFlag(cmd *cobra.Command, name, usage string) *kvFlags {
	k := &kvFlags{flag: name}
	cmd.Flags().Var(k, name, usage)
	return k
}

func (k *kvFlags) Type() string { return "key=value" }

func (k *kvFlags) String() string { return "" }

func (k *kvFlags) Set(raw string) error {
	key, value, ok := strings.Cut(raw, "=")
	if !ok || key == "" {
		return errUsage("--%s wants key=value, got %q", k.flag, raw)
	}
	if _, dup := k.out[key]; dup {
		return errUsage("--%s key %q given twice", k.flag, key)
	}
	if k.out == nil {
		k.out = map[string]string{}
	}
	k.out[key] = value
	return nil
}

// map projects the collected pairs onto a generated metadata field.
func (k *kvFlags) mapValue() *kha.StringMetadata {
	if k.out == nil {
		return nil
	}
	m := kha.StringMetadata(k.out)
	return &m
}

// refListFlags parse repeatable id[@version] references (--skill sk_1@2).
type refListFlags struct {
	flag string
	ids  []string
}

func addRefListFlag(cmd *cobra.Command, name, usage string) *refListFlags {
	r := &refListFlags{flag: name}
	cmd.Flags().Var(r, name, usage)
	return r
}

func (r *refListFlags) Type() string { return "id[@version]" }

func (r *refListFlags) String() string { return strings.Join(r.ids, ",") }

func (r *refListFlags) Set(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return errUsage("--%s requires a nonempty ID; use --file to clear the list", r.flag)
	}
	r.ids = append(r.ids, raw)
	return nil
}

// split cuts one id[@version] value into its parts.
func splitRef(raw string) (id string, version *string) {
	if id, v, ok := strings.Cut(raw, "@"); ok && v != "" {
		return id, new(v)
	}
	return raw, nil
}

// strList is a repeatable string flag (--allow-host, --pip, ...) with
// whitespace-only values rejected at parse time.
type strList struct {
	flag string
	out  []string
}

func addStrListFlag(cmd *cobra.Command, name, usage string) *strList {
	s := &strList{flag: name}
	cmd.Flags().Var(s, name, usage)
	return s
}

func (s *strList) Type() string { return "string" }

func (s *strList) String() string { return strings.Join(s.out, ",") }

func (s *strList) Set(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return errUsage("--%s requires a nonempty value", s.flag)
	}
	s.out = append(s.out, raw)
	return nil
}

// parseSince parses a --since value into a lower time bound: a relative
// duration ("24h", "7d", "2w" — days and weeks beyond time.ParseDuration)
// or an absolute date ("2026-11-01", UTC midnight). The CLI offers this
// single flag instead of the API's eight RFC 3339 gt/gte/lt/lte parameters:
// listing use is almost always "recent first", and exact bounds remain
// reachable through the SDK.
func parseSince(raw string) (time.Time, error) {
	if days, ok := strings.CutSuffix(raw, "d"); ok {
		if n, err := strconv.Atoi(days); err == nil && n > 0 {
			return time.Now().Add(-time.Duration(n) * 24 * time.Hour), nil
		}
	}
	if weeks, ok := strings.CutSuffix(raw, "w"); ok {
		if n, err := strconv.Atoi(weeks); err == nil && n > 0 {
			return time.Now().Add(-time.Duration(n) * 7 * 24 * time.Hour), nil
		}
	}
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return time.Now().Add(-d), nil
	}
	if t, err := time.ParseInLocation("2006-01-02", raw, time.UTC); err == nil {
		return t, nil
	}
	return time.Time{}, errUsage("--since wants a relative duration (24h, 7d, 2w) or a date (2026-11-01), got %q", raw)
}

// wrapUnion packs one concrete variant struct into its generated union
// envelope through a JSON round-trip. Same-union packing uses the generated
// New<Union>(v) constructors; this exists only for the one cross-union
// wrap they cannot express — a QueuedUserEventInput into the SessionEvent
// envelope in sessions_pending.go (the input variants share the streamed
// envelope shape, so the round-trip dispatches them there).
func wrapUnion[U any, V any](v V) (U, error) {
	var u U
	data, err := json.Marshal(v)
	if err != nil {
		return u, fmt.Errorf("encoding payload: %w", err)
	}
	if err := json.Unmarshal(data, &u); err != nil {
		return u, fmt.Errorf("packing union: %w", err)
	}
	return u, nil
}

// modelRefString renders a ModelRef for a table cell.
func modelRefString(ref kha.ModelRef) string {
	if s, ok := ref.AsString(); ok {
		return s
	}
	if mc := ref.AsModelConfig(); mc != nil {
		return mc.ID
	}
	return "-"
}

// unionDiscriminator restores the raw "type" discriminator of a union
// variant the pinned spec does not declare (decoded to a zero Type with the
// payload kept in Raw, open vocabulary); it falls back to "unknown" when
// the payload carries no type either.
func unionDiscriminator(raw []byte) string {
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &probe); err == nil && probe.Type != "" {
		return probe.Type
	}
	return "unknown"
}

// nonEmptyPageToken omits an empty pagination token from CLI list output.
func nonEmptyPageToken(token *string) *string {
	if token == nil || *token == "" {
		return nil
	}
	return token
}

// waitFlags are the polling knobs shared by every waiting command
// (environments wait-ready, triggers runs wait, vaults oauth wait, and the
// --wait variants). They map onto the SDK's WaitOption vocabulary.
type waitFlags struct {
	timeout  time.Duration
	interval time.Duration
}

// addWaitFlags registers --timeout/--interval on a waiting command.
func addWaitFlags(cmd *cobra.Command) *waitFlags {
	w := &waitFlags{}
	cmd.Flags().DurationVarP(&w.timeout, "timeout", "t", 10*time.Minute, "maximum time to wait")
	cmd.Flags().DurationVar(&w.interval, "interval", 2*time.Second, "delay between status polls")
	return w
}

// options projects the flags onto the SDK wait options. Non-positive
// values fall back to the SDK defaults inside the waiter.
func (w *waitFlags) options() []kha.WaitOption {
	return []kha.WaitOption{kha.WithTimeout(w.timeout), kha.WithInterval(w.interval)}
}

// terminalFile returns w as a file when it is a character device (a real
// terminal), else nil: spinners and progress bars stay silent on pipes.
func terminalFile(w io.Writer) *os.File {
	f, ok := w.(*os.File)
	if !ok {
		return nil
	}
	if st, err := f.Stat(); err != nil || st.Mode()&os.ModeCharDevice == 0 {
		return nil
	}
	return f
}

// waitProgress runs a stderr spinner (frame + label + elapsed, redrawn by
// the bar's own render ticker) until the returned stop function is called;
// stop erases the line and is safe to call more than once. It is silent —
// and spawns nothing — when stderr is not a terminal, so piped output stays
// clean (stdout carries data only either way).
func waitProgress(cmd *cobra.Command, format string, args ...any) (stop func()) {
	label := strings.ReplaceAll(output.StripEscapes(fmt.Sprintf(format, args...)), "\n", " ")
	w := cmd.ErrOrStderr()
	if terminalFile(w) == nil {
		return func() {}
	}
	bar := progressbar.NewOptions64(-1, // indeterminate: spinner mode
		progressbar.OptionSetDescription(label),
		progressbar.OptionSetWriter(w),
		// Spinner 14 is the same braille cycle the previous hand-rolled
		// spinner used.
		progressbar.OptionSpinnerType(14),
	)
	_ = bar.RenderBlank() // marks the bar started, so the ticker draws
	return func() {
		_ = bar.Finish() // stops the render ticker
		_ = bar.Clear()  // erase the spinner line
	}
}

// textFlags select a literal field value or a file, preserving empty input
// and trailing newlines. A nil result means neither flag was supplied.
type textFlags struct {
	name  string
	value string
	file  string
}

func addTextFlags(cmd *cobra.Command, name, usage string) *textFlags {
	f := &textFlags{name: name}
	cmd.Flags().StringVar(&f.value, name, "", usage+" (literal text)")
	cmd.Flags().StringVar(&f.file, name+"-file", "", "read "+name+` from a file ("-" reads stdin); cannot combine with --`+name)
	cmd.MarkFlagsMutuallyExclusive(name, name+"-file")
	return f
}

func (f *textFlags) resolve(cmd *cobra.Command) (*string, error) {
	if cmd.Flags().Changed(f.name + "-file") {
		data, err := readInputFile(cmd, f.name+"-file", f.file)
		if err != nil {
			return nil, err
		}
		return new(string(data)), nil
	}
	if cmd.Flags().Changed(f.name) {
		return new(f.value), nil
	}
	return nil, nil
}

// readInputFile reads a file flag as raw bytes; "-" reads stdin.
func readInputFile(cmd *cobra.Command, flag, path string) ([]byte, error) {
	if path == "" {
		return nil, errUsage("--%s requires a file path or - for stdin", flag)
	}
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(cmd.InOrStdin())
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, fmt.Errorf("reading --%s %s: %w", flag, path, err)
	}
	return data, nil
}

// readSecret reads a secret value from the command's stdin (--value-stdin),
// so the secret never appears in the shell history or the process list. All
// trailing \r and \n bytes are stripped (echo adds one line terminator; a
// secret never ends with them).
func readSecret(cmd *cobra.Command) (string, error) {
	data, err := io.ReadAll(cmd.InOrStdin())
	if err != nil {
		return "", fmt.Errorf("reading the secret from stdin: %w", err)
	}
	s := string(data)
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	if s == "" {
		return "", errUsage("empty secret on stdin")
	}
	return s, nil
}

// resolveSecret enforces the --value / --value-stdin mutual exclusion and
// returns the chosen secret. required reports whether a secret must be
// present at all.
func resolveSecret(cmd *cobra.Command, value string, valueStdin bool, required bool) (string, error) {
	if valueStdin && cmd.Flags().Changed("value") {
		return "", errUsage("--value and --value-stdin cannot be combined")
	}
	if valueStdin {
		return readSecret(cmd)
	}
	if cmd.Flags().Changed("value") {
		return value, nil
	}
	if required {
		return "", errUsage("a secret is required: pass --value-stdin (reads stdin, keeps it out of the shell history) or --value")
	}
	return "", nil
}

// nonNilItems keeps incomplete list entries out of CLI rendering and completion.
func nonNilItems[T any](items []*T) []*T {
	return slices.DeleteFunc(items, func(item *T) bool { return item == nil })
}
