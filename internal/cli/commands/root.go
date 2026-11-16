// Package commands implements the kha CLI command tree:
// `kha <resource> <verb>` over the public SDK surface.
package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/MoonshotAI/kha"
	"github.com/MoonshotAI/kha/internal/cli/config"
	"github.com/MoonshotAI/kha/internal/cli/output"
)

// version is the CLI version printed by `kha version` and sent in the
// User-Agent. Development builds carry "-dev"; release builds inject the tag
// (vX.Y.Z) at build time:
//
//	go build -ldflags "-X github.com/MoonshotAI/kha/internal/cli/commands.version=vX.Y.Z"
var version = "0.1.0-dev"

// cliVersion returns the effective CLI version: the injected tag when set,
// else the module version stamped into the binary by `go install ...@vX.Y.Z`
// (which beats the development default), else the development default.
func cliVersion() string {
	if version != "0.1.0-dev" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return strings.TrimPrefix(info.Main.Version, "v")
	}
	return version
}

// defaultBaseURL mirrors the SDK default (kha.WithBaseURL); used for
// display only, the client itself applies the real default.
const defaultBaseURL = "https://api.moonshot.cn"

// CLI exit codes.
const (
	exitError       = 1   // runtime/API or validation failure
	exitUsage       = 2   // bad flags or arguments
	exitInterrupted = 130 // SIGINT (128 + 2)
)

// errInterrupted is mapped to a dedicated exit code by Execute.
var errInterrupted = errors.New("interrupted")

// runtimeError marks an error raised inside a RunE (as opposed to flag
// parsing or argument validation), so Execute can tell runtime failures
// (exit 1) from usage errors (exit 2).
type runtimeError struct {
	err         error
	errorOutput string
}

func (e *runtimeError) Error() string { return e.err.Error() }
func (e *runtimeError) Unwrap() error { return e.err }

// reportedError signals failure after a command has printed its findings.
// Execute sets the exit code without rendering a second error frame.
type reportedError struct{ err error }

func (e *reportedError) Error() string { return e.err.Error() }
func (e *reportedError) Unwrap() error { return e.err }

// globalFlags are the persistent flags available on every command.
type globalFlags struct {
	profile     string
	apiKey      string
	baseURL     string
	apiVersion  string
	response    output.ResponseOptions
	errorOutput string
}

// NewRootCommand builds the full command tree. Tests drive it with
// SetArgs/SetOut/SetErr; main goes through Execute.
func NewRootCommand() *cobra.Command {
	g := &globalFlags{}
	root := &cobra.Command{
		Use:   "kha",
		Short: "Command-line client for the Kimi Hosted Agents API",
		Long: `Command-line client for the Kimi Hosted Agents API.

Every resource command follows the same pattern: kha <resource> <verb> [flags].
Commands print human-readable tables by default; use -o json or -o yaml for
structured output, --transform <gjson-path> to extract or reshape fields, and
-r (--raw-output) to print strings without quotes. Data is written only to
stdout, while progress notes, warnings, and errors go to stderr.

Credentials resolve as --api-key > KIMI_API_KEY > profile, and endpoints as
--base-url > KHA_BASE_URL > profile > https://api.moonshot.cn. The active
profile comes from --profile, KHA_PROFILE, or the config's "current" entry
(passing --profile explicitly ignores environment variables). Run 'kha login'
to save credentials in your system keychain, 'kha whoami' to verify your
connection, and 'kha docs' to browse the offline CLI reference.`,
		SilenceErrors: true,
		SilenceUsage:  true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			switch g.response.Format {
			case "table", "json", "yaml", "jsonl":
			default:
				return fmt.Errorf("invalid --output %q: want table, json, yaml, or jsonl", g.response.Format)
			}
			switch g.errorOutput {
			case "table", "json", "yaml", "jsonl":
			default:
				return fmt.Errorf("invalid --error-output %q: want table, json, or yaml", g.errorOutput)
			}
			maybeNotifyUpdate(cmd)
			return nil
		},
	}
	pf := root.PersistentFlags()
	pf.StringVar(&g.profile, "profile", "", "connection profile to use (ignores environment key and endpoint; explicit flags still override)")
	pf.StringVar(&g.apiKey, "api-key", "", "API key (overrides KIMI_API_KEY and the selected profile)")
	pf.StringVar(&g.baseURL, "base-url", "", "API base URL (debugging; overrides KHA_BASE_URL and the selected profile's base_url)")
	pf.StringVar(&g.apiVersion, "api-version", "", "kimi-api-version header to send (debugging; overrides the selected profile's api_version)")
	pf.StringVarP(&g.response.Format, "output", "o", "table", "output format: table, json, yaml, or jsonl (one compact JSON value per line)")
	pf.StringVar(&g.errorOutput, "error-output", "table", "error output format on stderr: table, json, or yaml")
	pf.StringVar(&g.response.Transform, "transform", "", "Filter or reshape the response with a GJSON path")
	pf.BoolVarP(&g.response.RawOutput, "raw-output", "r", false, "Print string results without surrounding quotes, like jq -r")
	mustRegisterFlagCompletion(root, "output", completeFlagStatic("table", "json", "yaml", "jsonl"))
	mustRegisterFlagCompletion(root, "error-output", completeFlagStatic("table", "json", "yaml"))
	mustRegisterFlagCompletion(root, "profile", profileCompletion)

	root.AddCommand(newLoginCmd(g), newLogoutCmd(g), newWhoamiCmd(g), newProfileCmd(g), newVersionCmd(g), newDocsCmd(), newInstallSkillCmd(), newUpgradeCmd())
	root.AddCommand(
		newAgentsCmd(g),
		newSessionsCmd(g),
		newVaultsCmd(g),
		newMemoryStoresCmd(g),
		newTriggersCmd(g),
		newSkillsCmd(g),
		newEnvironmentsCmd(g),
		newFilesCmd(g),
		newArtifactsCmd(g),
		newPluginsCmd(g),
		newWorkerCmd(g),
		newTunnelCmd(),
	)
	wrapArgumentErrors(root)
	return root
}

// Execute builds the tree, wires Ctrl-C/SIGTERM cancellation into every
// command's context, and maps the outcome onto the CLI exit codes.
func Execute() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	root := NewRootCommand()
	root.SetIn(os.Stdin)
	root.SetOut(os.Stdout)
	root.SetErr(os.Stderr)
	return exitCode(root.ExecuteContext(ctx))
}

// exitCode maps a command error to the process exit code, printing unreported
// runtime errors to stderr. The printed text passes
// through output.StripEscapes: an APIError's message is server-controlled
// and must not inject terminal control sequences into the user's tty.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	switch {
	case errors.Is(err, errInterrupted), errors.Is(err, context.Canceled):
		return exitInterrupted
	}
	if ue, ok := errors.AsType[*usageError](err); ok {
		fmt.Fprintf(os.Stderr, "error: %s\n", output.StripEscapes(ue.err.Error()))
		return exitUsage
	}
	if _, ok := errors.AsType[*reportedError](err); ok {
		return exitError
	}
	if rt, ok := errors.AsType[*runtimeError](err); ok {
		renderError(os.Stderr, rt.err, rt.errorOutput)
		return exitError
	}
	// Anything else comes from cobra itself (flag parsing, argument
	// validation): a usage error.
	fmt.Fprintf(os.Stderr, "error: %s\n", output.StripEscapes(err.Error()))
	return exitUsage
}

func renderError(w io.Writer, err error, format string) {
	message := output.StripEscapes(apiErrorMessage(err))
	if format == "json" {
		_ = output.PrintJSON(w, map[string]string{"error": message})
		return
	}
	if format == "yaml" {
		_ = output.PrintYAML(w, map[string]string{"error": message})
		return
	}
	_, _ = fmt.Fprintf(w, "error: %s\n", message)
}

// apiErrorMessage appends the response header's request ID when the error body
// did not already include one via APIError.String().
func apiErrorMessage(err error) string {
	message := err.Error()
	response, ok := errors.AsType[kha.HTTPError](err)
	if !ok {
		return message
	}
	if detail, ok := kha.ErrorDetails(err); ok && detail.RequestID != nil && *detail.RequestID != "" {
		return message
	}
	requestID := response.Header().Get("Msh-Request-Id")
	if requestID == "" {
		requestID = response.Header().Get("X-Request-ID")
	}
	if requestID != "" {
		return fmt.Sprintf("%s (request id: %s)", message, requestID)
	}
	return message
}

// credentials is the resolved identity for one invocation.
type credentials struct {
	apiKey     string
	baseURL    string
	apiVersion string
	profile    string // selected profile name ("" when none resolved)
}

// resolveConnection uses explicit flags over profile values. An explicit
// --profile ignores environment key and endpoint overrides; otherwise those
// overrides precede the profile selected by KHA_PROFILE or config current.
// A named profile must exist; an empty resolved key permits anonymous access.
// Machine-token commands set loadKey=false: they need the profile endpoint,
// not its API key.
func resolveConnection(g *globalFlags, loadKey bool) (credentials, error) {
	cfg, err := config.Load()
	if err != nil {
		return credentials{}, err
	}
	name := g.profile
	explicit := name != ""
	if name == "" {
		name = os.Getenv("KHA_PROFILE")
		explicit = name != ""
	}
	if name == "" {
		name = cfg.Current
	}
	var prof *config.Profile
	if name != "" {
		prof = cfg.GetProfile(name)
		if prof == nil && explicit {
			return credentials{}, errUsage("profile %q does not exist (create it with `kha login --profile %s`)", name, name)
		}
	}
	creds := credentials{apiKey: g.apiKey, baseURL: g.baseURL, apiVersion: g.apiVersion, profile: name}
	if creds.apiKey == "" && g.profile == "" {
		creds.apiKey = os.Getenv("KIMI_API_KEY")
	}
	if loadKey && creds.apiKey == "" && prof != nil {
		if prof.APIKeyRef != "" {
			creds.apiKey, err = savedAPIKey(prof.APIKeyRef)
			if err != nil {
				return credentials{}, err
			}
		} else {
			creds.apiKey = prof.APIKey
		}
	}
	if creds.baseURL == "" && g.profile == "" {
		creds.baseURL = os.Getenv("KHA_BASE_URL")
	}
	if creds.baseURL == "" && prof != nil {
		creds.baseURL = prof.BaseURL
	}
	if creds.apiVersion == "" && prof != nil {
		creds.apiVersion = prof.APIVersion
	}
	return creds, nil
}

// newClient resolves credentials and builds the SDK client. Without any
// credential it still builds the client (anonymous mode): the request goes
// out without an Authorization header and the API's own 401 envelope is the
// signal when a deployment requires authentication. extra carries
// caller-specific SDK options (e.g. work poll's WithMaxRetries(0)).
func newClient(g *globalFlags, extra ...kha.ClientOption) (*kha.Client, credentials, error) {
	creds, err := resolveConnection(g, true)
	if err != nil {
		return nil, credentials{}, err
	}
	opts := append([]kha.ClientOption{kha.WithUserAgent("kha-cli/" + cliVersion())}, extra...)
	if creds.apiKey == "" {
		opts = append(opts, kha.WithAnonymous())
	}
	if creds.baseURL != "" {
		opts = append(opts, kha.WithBaseURL(creds.baseURL))
	}
	if creds.apiVersion != "" {
		opts = append(opts, kha.WithAPIVersion(creds.apiVersion))
	}
	client, err := kha.New(creds.apiKey, opts...)
	if err != nil {
		return nil, credentials{}, err
	}
	return client, creds, nil
}
