package commands

import (
	"github.com/spf13/cobra"

	kha "github.com/MoonshotAI/kha"
)

// newVersionCmd prints the CLI version and the API version the SDK pins.
func newVersionCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the CLI and pinned API versions",
		Args:  cobra.NoArgs,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			return printOne(cmd, g,
				[]string{"CLI VERSION", "API VERSION"},
				[]string{cliVersion(), kha.DefaultAPIVersion},
				map[string]string{
					"version":     cliVersion(),
					"api_version": kha.DefaultAPIVersion,
				})
		}),
	}
}
