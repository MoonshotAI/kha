package commands

import (
	"os"
	"sort"

	"github.com/spf13/cobra"

	"github.com/MoonshotAI/kha/internal/cli/config"
	"github.com/MoonshotAI/kha/internal/cli/output"
)

// newProfileCmd groups the local profile inspection/switching commands.
// Unlike login/logout these never touch the keychain or the API: they read
// and write the config file only, and never print key material.
func newProfileCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "Inspect and switch connection profiles (local; never touches the API)",
	}
	cmd.AddCommand(newProfileListCmd(g), newProfileUseCmd(g))
	return cmd
}

func newProfileListCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List profiles and where each API key is stored (never the key itself)",
		Args:  cobra.NoArgs,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			active := selectedProfile(g, cfg)
			names := make([]string, 0, len(cfg.Profiles))
			for name := range cfg.Profiles {
				names = append(names, name)
			}
			sort.Strings(names)
			headers := []string{"NAME", "CURRENT", "BASE URL", "API VERSION", "KEY"}
			rows := make([][]string, 0, len(names))
			items := make([]map[string]string, 0, len(names))
			for _, name := range names {
				p := cfg.Profiles[name]
				current := "-"
				if name == active {
					current = "*"
				}
				base := output.Dash(p.BaseURL)
				version := output.Dash(p.APIVersion)
				key := keyStorage(p)
				rows = append(rows, []string{name, current, base, version, key})
				items = append(items, map[string]string{
					"name":        name,
					"current":     current,
					"base_url":    p.BaseURL,
					"api_version": p.APIVersion,
					"key":         key,
				})
			}
			return printList(cmd, g, headers, rows, map[string]any{"items": items})
		}),
	}
}

// keyStorage describes where a profile's API key lives without revealing
// any part of the key: keychain-backed, legacy plaintext in the config
// file, or none (anonymous access).
func keyStorage(p *config.Profile) string {
	switch {
	case p.APIKeyRef != "":
		return "keychain"
	case p.APIKey != "":
		return "plaintext (legacy)"
	default:
		return "none"
	}
}

func newProfileUseCmd(_ *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "use <name>",
		Short: "Switch the current profile for interactive use",
		Long: `Switch the current profile stored in the config file.

The switch applies to future interactive invocations. Scripts and CI should
select profiles explicitly with --profile or KHA_PROFILE instead of mutating
shared state; when KHA_PROFILE is set it keeps overriding this switch.`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: profileCompletion,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if cfg.GetProfile(name) == nil {
				return errUsage("profile %q does not exist: create it with kha login --profile %s", name, name)
			}
			cfg.Current = name
			if err := cfg.Save(); err != nil {
				return err
			}
			note(cmd, "current profile is now %q", name)
			if env := os.Getenv("KHA_PROFILE"); env != "" {
				note(cmd, "note: KHA_PROFILE=%q is set in this shell and overrides the current profile", env)
			}
			return nil
		}),
	}
}
