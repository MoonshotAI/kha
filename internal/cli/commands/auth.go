package commands

import (
	"crypto/rand"
	"errors"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zalando/go-keyring"

	kha "github.com/MoonshotAI/kha"
	"github.com/MoonshotAI/kha/internal/cli/config"
	"github.com/MoonshotAI/kha/internal/cli/output"
)

// newLoginCmd saves the key in the system keychain and connection settings
// in the profile. A hidden prompt is the default; --api-key-stdin supports
// piping the key without placing it in argv. No API call is made.
func newLoginCmd(g *globalFlags) *cobra.Command {
	var apiKeyStdin bool
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Save an API key in the system keychain for a profile",
		Long: `Save an API key in the system keychain and save the profile settings.
Base URL: --base-url > KHA_BASE_URL > previous profile value.
API version: --api-version > previous profile value. Verify with 'kha whoami'.

Without a key flag, paste the key at the hidden prompt. An empty key
(Enter at the prompt, or --api-key '') saves the profile without a
credential for anonymous access; --api-key-stdin requires a non-empty key.`,
		Example: `  kha login
  kha login --profile staging --base-url https://staging.internal
  kha login --api-key-stdin`,
		Args: cobra.NoArgs,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			if apiKeyStdin && g.apiKey != "" {
				return errUsage("--api-key and --api-key-stdin cannot be combined")
			}
			key := g.apiKey
			if apiKeyStdin {
				var err error
				key, err = readSecret(cmd)
				if err != nil {
					return err
				}
			}
			if key == "" && !apiKeyStdin && !cmd.Flags().Changed("api-key") {
				var err error
				key, err = promptLoginToken(cmd, "Paste the API key (input hidden; leave empty for anonymous access)")
				if err != nil {
					return err
				}
			}
			key = strings.TrimSpace(key)
			if key != "" && (len(key) > 1024 || strings.ContainsAny(key, "\r\n\x00")) {
				return errUsage("invalid API key length or characters")
			}
			name := g.profile
			if name == "" {
				name = "default"
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			previous := cfg.GetProfile(name)
			baseURL, apiVersion := g.baseURL, g.apiVersion
			if !cmd.Flags().Changed("base-url") {
				// Save the endpoint from the login precedence order.
				if env := os.Getenv("KHA_BASE_URL"); env != "" {
					baseURL = env
				} else if previous != nil {
					baseURL = previous.BaseURL
				}
			}
			if previous != nil && !cmd.Flags().Changed("api-version") {
				apiVersion = previous.APIVersion
			}
			prof := config.Profile{BaseURL: baseURL, APIVersion: apiVersion}
			if key != "" {
				// Store under a fresh reference so a failed config write
				// leaves the previous profile and key usable.
				ref := rand.Text()
				if err := systemKeys.Set(apiKeyService, ref, key); err != nil {
					return errors.New("could not save API key in system keychain; use KIMI_API_KEY for automation")
				}
				stored, err := savedAPIKey(ref)
				if err != nil || stored != key {
					_ = systemKeys.Delete(apiKeyService, ref)
					return errors.New("could not verify API key storage in system keychain; profile was not changed")
				}
				prof.APIKeyRef = ref
			}
			cfg.SetProfile(name, prof)
			if err := cfg.Save(); err != nil {
				if prof.APIKeyRef != "" {
					_ = systemKeys.Delete(apiKeyService, prof.APIKeyRef)
				}
				return err
			}
			if previous != nil && previous.APIKeyRef != "" {
				if err := systemKeys.Delete(apiKeyService, previous.APIKeyRef); err != nil && !errors.Is(err, keyring.ErrNotFound) {
					note(cmd, "profile updated, but the old keychain entry could not be removed")
				}
			}
			if key == "" {
				note(cmd, "profile %q saved without an API key; requests will be sent anonymously", name)
			} else {
				note(cmd, "API key saved in system keychain for profile %q", name)
			}
			if cfg.Current == name {
				note(cmd, "profile %q is now the current profile", name)
			}
			return nil
		}),
	}
	cmd.Flags().BoolVar(&apiKeyStdin, "api-key-stdin", false, "read the API key from stdin")
	return cmd
}

// newLogoutCmd removes one credential profile.
func newLogoutCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Remove a profile and its saved API key",
		Long: `Remove a profile from the config file and its API key from the system keychain.
This does not revoke the key on the server. The profile is chosen
by --profile > KHA_PROFILE > the current profile.`,
		Args: cobra.NoArgs,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			name := selectedProfile(g, cfg)
			if name == "" || cfg.GetProfile(name) == nil {
				return errUsage("no stored credentials: nothing to log out of")
			}
			if ref := cfg.GetProfile(name).APIKeyRef; ref != "" {
				if err := systemKeys.Delete(apiKeyService, ref); err != nil && !errors.Is(err, keyring.ErrNotFound) {
					return errors.New("could not remove API key from system keychain; profile was not changed")
				}
			}
			cfg.DeleteProfile(name)
			if err := cfg.Save(); err != nil {
				return err
			}
			note(cmd, "profile %q removed", name)
			return nil
		}),
	}
}

// newWhoamiCmd verifies the resolved credentials with a minimal API call
// (list agents, page_size 1) and reports the effective configuration.
func newWhoamiCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Verify credentials and show the effective configuration",
		Args:  cobra.NoArgs,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			client, creds, err := newClient(g)
			if err != nil {
				return err
			}
			response, err := client.Agents.List(cmd.Context(), kha.ListAgentsRequest{PageSize: new(int32(1))})
			if err != nil {
				return err
			}
			requestID := response.Header.Get("Msh-Request-Id")

			base := creds.baseURL
			if base == "" {
				base = defaultBaseURL
			}
			profile := creds.profile
			if profile == "" {
				profile = "-"
			}
			apiVersion := creds.apiVersion
			if apiVersion == "" {
				apiVersion = kha.DefaultAPIVersion
			}
			result := map[string]string{
				"profile":     profile,
				"base_url":    base,
				"api_version": apiVersion,
				"request_id":  requestID,
			}
			return printOne(cmd, g,
				[]string{"PROFILE", "BASE URL", "API VERSION", "REQUEST ID"},
				[]string{profile, base, apiVersion, output.Dash(requestID)},
				result)
		}),
	}
}

// selectedProfile applies the profile-selection half of the precedence chain
// (--profile > KHA_PROFILE > config current).
func selectedProfile(g *globalFlags, cfg *config.Config) string {
	if g.profile != "" {
		return g.profile
	}
	if env := os.Getenv("KHA_PROFILE"); env != "" {
		return env
	}
	return cfg.Current
}
