package commands

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode"

	"github.com/spf13/cobra"
	"github.com/zalando/go-keyring"

	"github.com/MoonshotAI/kha"
)

// tokenFlags selects one explicit machine credential source. It never falls
// back after a failed read, and never accepts a token in argv.
type tokenFlags struct {
	file  string
	stdin bool
}

func (f *tokenFlags) register(cmd *cobra.Command, kind string) {
	cmd.Flags().StringVar(&f.file, "token-file", "", "read "+kind+" from a file (\"-\" reads stdin)")
	cmd.Flags().BoolVar(&f.stdin, "token-stdin", false, "read "+kind+" from stdin")
	cmd.MarkFlagsMutuallyExclusive("token-file", "token-stdin")
}

func (f *tokenFlags) explicit(cmd *cobra.Command) bool {
	return cmd.Flags().Changed("token-file") || cmd.Flags().Changed("token-stdin")
}

func (f *tokenFlags) path(cmd *cobra.Command) (string, error) {
	if cmd.Flags().Changed("token-stdin") {
		if !f.stdin {
			return "", errUsage("--token-stdin must be true when specified")
		}
		return "-", nil
	}
	if f.file == "" {
		return "", errUsage("provide --token-file or --token-stdin")
	}
	return f.file, nil
}

func (f *tokenFlags) load(cmd *cobra.Command, prefixes ...string) (string, error) {
	path, err := f.path(cmd)
	if err != nil {
		return "", err
	}
	token, err := kha.LoadSecretFile(cmd.InOrStdin(), path)
	if err != nil {
		return "", errUsage("%s", err)
	}
	if err := validateWorkerToken(token, prefixes...); err != nil {
		return "", err
	}
	return token, nil
}

func validateWorkerToken(token string, prefixes ...string) error {
	for _, prefix := range prefixes {
		if strings.HasPrefix(token, prefix) && len(token) > len(prefix) && len(token) <= 1024 &&
			!strings.ContainsFunc(token, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
			return nil
		}
	}
	return errUsage("expected a %s token; token contents are not shown", strings.Join(prefixes, " or "))
}

const workerKeyService = "kha.worker"

// Hash the endpoint and environment together, keeping keychain identifiers
// short even for long base paths. Paths and ports remain part of the scope.
func workerKeyAccount(baseURL, environmentID string) (string, error) {
	if environmentID == "" {
		return "", errUsage("environment ID must not be empty")
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errUsage("worker base URL must be an http(s) URL without user info, query or fragment")
	}
	u.Host = strings.ToLower(u.Host)
	endpoint := strings.TrimRight(u.String(), "/")
	return fmt.Sprintf("%x", sha256.Sum256([]byte(endpoint+"\x00"+environmentID))), nil
}

func loadWorkerKey(baseURL, environmentID string) (string, error) {
	account, err := workerKeyAccount(baseURL, environmentID)
	if err != nil {
		return "", err
	}
	token, err := systemKeys.Get(workerKeyService, account)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", errUsage("no work key saved for this endpoint and environment; run 'kha worker login %s' with the same --base-url, or provide --token-file / --token-stdin", environmentID)
	}
	if err != nil {
		// Backend errors may contain sensitive data; do not print them.
		return "", errors.New("system keychain unavailable or locked; use --token-file or --token-stdin")
	}
	if err := validateWorkerToken(token, "ewk-"); err != nil {
		return "", err
	}
	return token, nil
}

func newWorkerLoginCmd(g *globalFlags) *cobra.Command {
	var tokenInput tokenFlags
	cmd := &cobra.Command{
		Use:   "login <environment-id>",
		Short: "Save an environment work key in the system keychain",
		Long: `Save an existing work key from the console. Use a hidden terminal prompt,
or --token-stdin / --token-file for noninteractive login.
The key is stored in the system keychain, scoped to the API endpoint and
environment, without contacting the API. No secret is written to a profile.
On machines without a keychain, use worker poll --token-file or --token-stdin.`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: environmentIDCompletion,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			account, err := workerKeyAccount(workerBaseURL(g), args[0])
			if err != nil {
				return err
			}
			var token string
			if tokenInput.explicit(cmd) {
				token, err = tokenInput.load(cmd, "ewk-")
			} else {
				token, err = promptLoginToken(cmd, "Paste the environment work key (input hidden)")
			}
			if err != nil {
				return err
			}
			if err := validateWorkerToken(token, "ewk-"); err != nil {
				return err
			}
			if err := systemKeys.Set(workerKeyService, account, token); err != nil {
				return errors.New("could not save work key in system keychain; use --token-file or --token-stdin")
			}
			// Some OS helpers report success before storage errors surface.
			stored, err := systemKeys.Get(workerKeyService, account)
			if err != nil || stored != token {
				return errors.New("could not verify work key storage in system keychain; use --token-file or --token-stdin")
			}
			note(cmd, "work key saved for environment %s at %s", args[0], workerBaseURL(g))
			return nil
		}),
	}
	tokenInput.register(cmd, "the environment work key (ewk-)")
	return cmd
}

func newWorkerLogoutCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:               "logout <environment-id>",
		Short:             "Remove this endpoint and environment's work key from the system keychain",
		Long:              "Remove the locally saved key only. To revoke the work key on the server, use the console.",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: environmentIDCompletion,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			account, err := workerKeyAccount(workerBaseURL(g), args[0])
			if err != nil {
				return err
			}
			if err := systemKeys.Delete(workerKeyService, account); err != nil && !errors.Is(err, keyring.ErrNotFound) {
				return errors.New("could not remove work key from system keychain")
			}
			note(cmd, "local work key removed for environment %s at %s (server key not revoked)", args[0], workerBaseURL(g))
			return nil
		}),
	}
}
