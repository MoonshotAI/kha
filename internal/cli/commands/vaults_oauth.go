package commands

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	kha "github.com/MoonshotAI/kha"
	"github.com/MoonshotAI/kha/internal/cli/output"
)

var oauthFlowHeaders = []string{"OAUTH SESSION", "AUTHORIZE URL", "MCP SERVER"}

func oauthFlowRow(f *kha.CreateVaultOAuthSessionResponse) []string {
	return []string{f.OauthSessionID, f.AuthorizeURL, f.MCPServerURL}
}

var oauthSessionHeaders = []string{"STATUS", "CREDENTIAL", "ERROR"}

func oauthSessionRow(s *kha.VaultOAuthSession) []string {
	return []string{string(s.Status), output.Dash(valueOr(s.CredentialID, "")), output.Dash(valueOr(s.Error, ""))}
}

func newVaultsOAuthCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "oauth",
		Short: "Authorize MCP servers into a Vault over OAuth",
	}
	cmd.AddCommand(
		newVaultsOAuthCreateCmd(g),
		newVaultsOAuthWaitCmd(g),
		newVaultsOAuthGetCmd(g),
	)
	return cmd
}

func newVaultsOAuthCreateCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "create <vault-id>",
		Short:             "Start an OAuth flow for an MCP server and print the authorize URL",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: vaultIDCompletion,
		Example:           `  kha vaults oauth create vlt_xxx --mcp-url https://mcp.example`,
	}
	var mcpURL, displayName string
	cmd.Flags().StringVar(&mcpURL, "mcp-url", "", "MCP server URL to authorize")
	cmd.Flags().StringVar(&displayName, "display-name", "", "name for the credential created after authorization (default: the server URL)")
	var egressEnvironment string
	cmd.Flags().StringVar(&egressEnvironment, "egress-environment", "", "Environment proxy for OAuth discovery and refresh")
	mustRegisterFlagCompletion(cmd, "egress-environment", environmentIDCompletion)
	body := addBodyFlag(cmd, "egress-environment", "mcp-url", "display-name")
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		var req kha.CreateVaultOAuthSessionRequest
		if cmd.Flags().Changed("file") {
			if err := body.decode(cmd, &req); err != nil {
				return err
			}
		} else {
			if mcpURL == "" {
				return errUsage("vaults oauth create requires --mcp-url (or --file)")
			}
			if cmd.Flags().Changed("egress-environment") {
				req.EgressEnvironmentID = new(egressEnvironment)
			}
			req.MCPServerURL = mcpURL
			if cmd.Flags().Changed("display-name") {
				req.DisplayName = new(displayName)
			}
		}
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		flowResult, err := client.Vaults.CreateOAuthSession(cmd.Context(), kha.CreateVaultOAuthSessionHTTPRequest{ID: args[0], Body: &req})
		if err != nil {
			return err
		}
		flow := flowResult.Body

		note(cmd, "open the authorize URL in a browser, then run `kha vaults oauth wait %s %s`", args[0], flow.OauthSessionID)
		return printOne(cmd, g, oauthFlowHeaders, oauthFlowRow(flow), flow)
	})
	return cmd
}

func newVaultsOAuthWaitCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "wait <vault-id> <oauth-session-id>",
		Short: "Wait for the user to complete an OAuth authorization",
		Args:  cobra.ExactArgs(2),
		// The API has no list endpoint for OAuth sessions, so only the Vault
		// ID completes; the session ID position silently yields no candidates.
		ValidArgsFunction: vaultIDCompletion,
	}
	wf := addWaitFlags(cmd)
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		stop := waitProgress(cmd, "waiting for OAuth authorization of %s", args[1])
		response, err := client.Vaults.WaitOAuthSession(cmd.Context(), args[0], args[1], wf.options()...)
		stop()
		if err != nil {
			if _, ok := errors.AsType[*kha.WaitFailedError](err); ok {
				return fmt.Errorf("oauth authorization failed: %w", err)
			}
			return err
		}
		sess := response.Body
		if sess.CredentialID != nil {
			note(cmd, "authorized; credential %s created", *sess.CredentialID)
		} else {
			note(cmd, "authorized")
		}
		return printOne(cmd, g, oauthSessionHeaders, oauthSessionRow(sess), sess)
	})
	return cmd
}

func newVaultsOAuthGetCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "get <vault-id> <oauth-session-id>",
		Short: "Get the current status of an OAuth flow",
		Args:  cobra.ExactArgs(2),
		// Same as wait: no OAuth-session list endpoint, Vault ID completes only.
		ValidArgsFunction: vaultIDCompletion,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			client, _, err := newClient(g)
			if err != nil {
				return err
			}
			sessResult, err := client.Vaults.GetOAuthSession(cmd.Context(), kha.GetVaultOAuthSessionRequest{ID: args[0], SessionID: args[1]})
			if err != nil {
				return err
			}
			sess := sessResult.Body

			return printOne(cmd, g, oauthSessionHeaders, oauthSessionRow(sess), sess)
		}),
	}
}
