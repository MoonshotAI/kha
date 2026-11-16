package commands

import (
	"fmt"
	"strings"

	"github.com/MoonshotAI/kha"

	"github.com/spf13/cobra"

	"github.com/MoonshotAI/kha/internal/cli/output"
)

var credentialHeaders = []string{"ID", "DISPLAY NAME", "TYPE", "DETAIL", "CREATED"}

// vaultAuthTypeName renders the credential auth union's discriminator for table
// cells and error messages. Discriminators the pinned spec does not declare
// decode to a zero Type with the payload kept in Raw (open vocabulary); the
// raw discriminator is restored from Raw.
func vaultAuthTypeName(auth kha.VaultCredentialAuth) string {
	switch auth.Type {
	case kha.VaultCredentialAuthTypeStaticBearerCredential:
		return string(kha.StaticBearerCredentialTypeStaticBearer)
	case kha.VaultCredentialAuthTypeMCPOAuthCredential:
		return string(kha.MCPOAuthCredentialTypeMCPOauth)
	case kha.VaultCredentialAuthTypeEnvironmentVariableCredential:
		return string(kha.EnvironmentVariableCredentialTypeEnvironmentVariable)
	}
	return unionDiscriminator(auth.Raw)
}

// credentialRow renders one credential; DETAIL identifies the credential's
// target (MCP server URL or environment variable name). Secret values never
// appear: the API omits them from every response.
func credentialRow(c *kha.VaultCredential) []string {
	detail := "-"
	switch auth := c.Auth; {
	case auth.AsStaticBearerCredential() != nil:
		detail = auth.AsStaticBearerCredential().MCPServerURL
	case auth.AsMCPOAuthCredential() != nil:
		detail = auth.AsMCPOAuthCredential().MCPServerURL
	case auth.AsEnvironmentVariableCredential() != nil:
		detail = auth.AsEnvironmentVariableCredential().SecretName
	}
	return []string{c.ID, c.DisplayName, output.Dash(vaultAuthTypeName(c.Auth)), output.Dash(detail), output.Time(c.CreatedAt)}
}

func newVaultsCredentialsCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "credentials",
		Short: "Manage a Vault's credentials",
	}
	cmd.AddCommand(
		newVaultsCredentialsListCmd(g),
		newVaultsCredentialsGetCmd(g),
		newVaultsCredentialsCreateCmd(g),
		newVaultsCredentialsUpdateCmd(g),
		newVaultsCredentialsEditCmd(g),
		newVaultsCredentialsDeleteCmd(g),
		newVaultsCredentialsArchiveCmd(g),
		newVaultsCredentialsBatchCmd(g),
		newVaultsCredentialsCheckCmd(g),
	)
	return cmd
}

func newVaultsCredentialsListCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "list <vault-id>",
		Short:             "List a Vault's credentials (newest first, secrets omitted)",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: vaultIDCompletion,
	}
	lf := addListFlags(cmd)
	var includeArchived bool
	cmd.Flags().BoolVar(&includeArchived, "include-archived", false, "include archived credentials")
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		if err := lf.validate(); err != nil {
			return err
		}
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		params := kha.ListVaultCredentialsRequest{ID: args[0], PageSize: lf.pageSize(), PageToken: lf.token()}
		if includeArchived {
			params.IncludeArchived = new(true)
		}
		if lf.all {
			return printAll(cmd, g, credentialHeaders, client.Vaults.AllCredentials(cmd.Context(), params),
				credentialRow)
		}
		itemsResult, err := client.Vaults.ListCredentials(cmd.Context(), params)
		if err != nil {
			return err
		}
		page := itemsResult.Body
		page.Items = nonNilItems(page.Items)
		page.NextPageToken = nonEmptyPageToken(page.NextPageToken)

		rows := make([][]string, 0, len(page.Items))
		for _, item := range page.Items {
			rows = append(rows, credentialRow(item))
		}
		return printList(cmd, g, credentialHeaders, rows, page)
	})
	return cmd
}

func newVaultsCredentialsGetCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:               "get <vault-id> <credential-id>",
		Short:             "Get one credential (secret values omitted)",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: vaultCredentialIDCompletion,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			client, _, err := newClient(g)
			if err != nil {
				return err
			}
			credResult, err := client.Vaults.GetCredential(cmd.Context(), kha.GetVaultCredentialRequest{ID: args[0], CredentialID: args[1]})
			if err != nil {
				return err
			}
			cred := credResult.Body

			return printOne(cmd, g, credentialHeaders, credentialRow(cred), cred)
		}),
	}
}

// secretFlags are the secret-input flags shared by credential create/update.
// --value-stdin (reads stdin) is the recommended path: it keeps the secret
// out of the shell history and the process list.
type secretFlags struct {
	value      string
	valueStdin bool
}

func addSecretFlags(cmd *cobra.Command) *secretFlags {
	s := &secretFlags{}
	cmd.Flags().StringVar(&s.value, "value", "", "secret value (visible in the shell history and process list; prefer --value-stdin)")
	cmd.Flags().BoolVar(&s.valueStdin, "value-stdin", false, "read the secret value from stdin (recommended; keeps it out of the shell history)")
	return s
}

func newVaultsCredentialsCreateCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "create <vault-id>",
		Short:             "Store one credential in a Vault",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: vaultIDCompletion,
		Example: `  kha vaults credentials create vlt_xxx --type environment_variable --name GITHUB_TOKEN --value-stdin
  kha vaults credentials create vlt_xxx --type static_bearer --mcp-url https://mcp.example --value-stdin
  cat cred.json | kha vaults credentials create vlt_xxx --file -`,
	}
	var displayName, credType, mcpURL, secretName string
	cmd.Flags().StringVar(&displayName, "display-name", "", "human-readable credential name")
	cmd.Flags().StringVar(&credType, "type", "", "credential type: static_bearer or environment_variable (use --file for mcp_oauth)")
	cmd.Flags().StringVar(&mcpURL, "mcp-url", "", "MCP server URL (static_bearer)")
	cmd.Flags().StringVar(&secretName, "name", "", "environment variable name (environment_variable)")
	mustRegisterFlagCompletion(cmd, "type", completeFlagStatic("static_bearer", "environment_variable"))
	var credNetworking string
	cmd.Flags().StringVar(&credNetworking, "networking", "", "host scope for environment_variable substitution: limited or unrestricted")
	mustRegisterFlagCompletion(cmd, "networking", completeFlagStatic("limited", "unrestricted"))
	credAllowedHosts := addStrListFlag(cmd, "allow-host", "host allowed when --networking limited (repeatable)")
	var injectHeader, injectBody bool
	cmd.Flags().BoolVar(&injectHeader, "inject-header", false, "inject the environment_variable credential into the request header")
	cmd.Flags().BoolVar(&injectBody, "inject-body", false, "inject the environment_variable credential into the request body")
	secret := addSecretFlags(cmd)
	body := addBodyFlag(cmd, "display-name", "type", "mcp-url", "name", "networking", "allow-host", "inject-header", "inject-body", "value", "value-stdin")
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		var req kha.CreateVaultCredentialRequest
		if cmd.Flags().Changed("file") {
			if err := body.decode(cmd, &req); err != nil {
				return err
			}
		} else {
			value, err := resolveSecret(cmd, secret.value, secret.valueStdin, true)
			if err != nil {
				return err
			}
			if credType != "environment_variable" && (cmd.Flags().Changed("networking") || len(credAllowedHosts.out) > 0 || injectHeader || injectBody) {
				return errUsage("--networking/--allow-host/--inject-header/--inject-body only apply to --type environment_variable")
			}
			var auth kha.VaultCredentialAuth
			switch credType {
			case "static_bearer":
				if mcpURL == "" {
					return errUsage("--type static_bearer requires --mcp-url")
				}
				cred := &kha.StaticBearerCredential{Type: kha.StaticBearerCredentialTypeStaticBearer, MCPServerURL: mcpURL, Token: new(value)}
				auth = *kha.NewVaultCredentialAuthStaticBearerCredential(cred)
			case "environment_variable":
				if secretName == "" {
					return errUsage("--type environment_variable requires --name")
				}
				cred := &kha.EnvironmentVariableCredential{Type: kha.EnvironmentVariableCredentialTypeEnvironmentVariable, SecretName: secretName, SecretValue: new(value)}
				if cmd.Flags().Changed("networking") || len(credAllowedHosts.out) > 0 {
					net := &kha.CredentialNetworking{Type: kha.CredentialNetworkAccess(credNetworking)}
					if !cmd.Flags().Changed("networking") {
						net.Type = kha.CredentialNetworkAccessLimited
					}
					for _, h := range credAllowedHosts.out {
						net.AllowedHosts = append(net.AllowedHosts, kha.CredentialAllowedHost(h))
					}
					cred.Networking = net
				}
				if injectHeader || injectBody {
					cred.InjectionLocation = &kha.CredentialInjectionLocation{}
					if injectHeader {
						cred.InjectionLocation.Header = new(true)
					}
					if injectBody {
						cred.InjectionLocation.Body = new(true)
					}
				}
				auth = *kha.NewVaultCredentialAuthEnvironmentVariableCredential(cred)
			default:
				return errUsage(`credentials create requires --type static_bearer or environment_variable (or --file)`)
			}
			req.Auth = auth

			if cmd.Flags().Changed("display-name") {
				req.DisplayName = new(displayName)
			}
		}
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		credResult, err := client.Vaults.CreateCredential(cmd.Context(), kha.CreateVaultCredentialHTTPRequest{ID: args[0], Body: &req})
		if err != nil {
			return err
		}
		cred := credResult.Body

		note(cmd, "credential %s created in vault %s", cred.ID, args[0])
		return printOne(cmd, g, credentialHeaders, credentialRow(cred), cred)
	})
	return cmd
}

// newVaultsCredentialsUpdateCmd renames a credential or rotates its secret.
// A flag-driven rotation reads the credential first to learn its auth type,
// then sends a same-type auth carrying only the new secret (the API merges
// by field; type and identifying fields cannot change).
func newVaultsCredentialsUpdateCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "update <vault-id> <credential-id>",
		Short:             "Rename a credential or rotate its secret",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: vaultCredentialIDCompletion,
	}
	var displayName string
	cmd.Flags().StringVar(&displayName, "display-name", "", "new display name")
	secret := addSecretFlags(cmd)
	body := addBodyFlag(cmd, "display-name", "value", "value-stdin")
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		secretGiven := secret.valueStdin || cmd.Flags().Changed("value")
		if !cmd.Flags().Changed("file") && !cmd.Flags().Changed("display-name") && !secretGiven {
			return errUsage("nothing to update: pass --display-name, a secret flag, or --file")
		}
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		var req kha.UpdateVaultCredentialRequest
		if cmd.Flags().Changed("file") {
			if err := body.decode(cmd, &req); err != nil {
				return err
			}
		} else {
			if cmd.Flags().Changed("display-name") {
				req.DisplayName = new(displayName)
			}
			if secretGiven {
				value, err := resolveSecret(cmd, secret.value, secret.valueStdin, false)
				if err != nil {
					return err
				}
				currentResult, err := client.Vaults.GetCredential(cmd.Context(), kha.GetVaultCredentialRequest{ID: args[0], CredentialID: args[1]})
				if err != nil {
					return err
				}
				current := currentResult.Body

				auth, err := rotateAuth(current, value)
				if err != nil {
					return err
				}
				req.Auth = auth
			}
		}
		updatedResult, err := client.Vaults.UpdateCredential(cmd.Context(), kha.UpdateVaultCredentialHTTPRequest{ID: args[0], CredentialID: args[1], Body: &req})
		if err != nil {
			return err
		}
		updated := updatedResult.Body

		note(cmd, "credential %s updated", updated.ID)
		return printOne(cmd, g, credentialHeaders, credentialRow(updated), updated)
	})
	return cmd
}

// rotateAuth builds a same-type auth update carrying only the new secret.
func rotateAuth(current *kha.VaultCredential, value string) (*kha.VaultCredentialAuth, error) {
	var auth kha.VaultCredentialAuth
	switch a := current.Auth; {
	case a.AsStaticBearerCredential() != nil:
		cred := &kha.StaticBearerCredential{Type: kha.StaticBearerCredentialTypeStaticBearer, Token: new(value)}
		auth = *kha.NewVaultCredentialAuthStaticBearerCredential(cred)
	case a.AsEnvironmentVariableCredential() != nil:
		cred := &kha.EnvironmentVariableCredential{Type: kha.EnvironmentVariableCredentialTypeEnvironmentVariable, SecretValue: new(value)}
		auth = *kha.NewVaultCredentialAuthEnvironmentVariableCredential(cred)
	case a.AsMCPOAuthCredential() != nil:
		cred := &kha.MCPOAuthCredential{Type: kha.MCPOAuthCredentialTypeMCPOauth, AccessToken: new(value)}
		auth = *kha.NewVaultCredentialAuthMCPOAuthCredential(cred)
	default:
		return nil, fmt.Errorf("credential %s has an auth type this CLI does not know (%q): use --file", current.ID, vaultAuthTypeName(current.Auth))
	}
	return &auth, nil
}

func newVaultsCredentialsDeleteCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:               "delete <vault-id> <credential-id>",
		Short:             "Delete a credential",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: vaultCredentialIDCompletion,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			client, _, err := newClient(g)
			if err != nil {
				return err
			}
			if _, err := client.Vaults.DeleteCredential(cmd.Context(), kha.DeleteVaultCredentialRequest{ID: args[0], CredentialID: args[1]}); err != nil {
				return err
			}
			note(cmd, "credential %s deleted", args[1])
			return nil
		}),
	}
}

func newVaultsCredentialsArchiveCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:               "archive <vault-id> <credential-id>",
		Short:             "Archive a credential (its secret is purged)",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: vaultCredentialIDCompletion,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			client, _, err := newClient(g)
			if err != nil {
				return err
			}
			if _, err := client.Vaults.ArchiveCredential(cmd.Context(), kha.ArchiveVaultCredentialRequest{ID: args[0], CredentialID: args[1]}); err != nil {
				return err
			}
			note(cmd, "credential %s archived", args[1])
			return nil
		}),
	}
}

func newVaultsCredentialsBatchCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "batch <vault-id>",
		Short:             "Store several credentials in one atomic request (JSON body)",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: vaultIDCompletion,
		Example: `  cat creds.json | kha vaults credentials batch vlt_xxx --file -
  # creds.json: {"items": [{"display_name": "...", "auth": {...}}, ...]}`,
	}
	body := addBodyFlag(cmd)
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		if body.file == "" {
			return errUsage("credentials batch requires --file carrying the request body as JSON")
		}
		var req kha.BatchCreateVaultCredentialsRequest
		if err := body.decode(cmd, &req); err != nil {
			return err
		}
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		respResult, err := client.Vaults.BatchCreateCredentials(cmd.Context(), kha.BatchCreateVaultCredentialsHTTPRequest{ID: args[0], Body: &req})
		if err != nil {
			return err
		}
		resp := respResult.Body

		resp.Items = nonNilItems(resp.Items)
		note(cmd, "%d credential(s) created in vault %s", len(resp.Items), args[0])
		rows := make([][]string, 0, len(resp.Items))
		for i := range resp.Items {
			rows = append(rows, credentialRow(resp.Items[i]))
		}
		return printList(cmd, g, credentialHeaders, rows, resp)
	})
	return cmd
}

// newVaultsCredentialsCheckCmd checks which Vaults already hold an active
// credential for each requested MCP server URL. The check endpoint is
// project-wide; --vault restricts the search (repeatable).
func newVaultsCredentialsCheckCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Check credential coverage for MCP server URLs (JSON body)",
		Args:  cobra.NoArgs,
		Example: `  echo '{"mcp_server_urls": ["https://mcp.example"]}' | kha vaults credentials check --file -
  echo '{"mcp_server_urls": ["https://mcp.example"]}' | kha vaults credentials check --vault vlt_xxx --file -`,
	}
	var vaultIDs []string
	cmd.Flags().StringSliceVar(&vaultIDs, "vault", nil, "restrict the search to this Vault (repeatable)")
	mustRegisterFlagCompletion(cmd, "vault", vaultIDCompletion)
	body := addBodyFlag(cmd)
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		if body.file == "" {
			return errUsage("credentials check requires --file carrying the request body as JSON")
		}
		var req kha.CheckVaultCredentialsRequest
		if err := body.decode(cmd, &req); err != nil {
			return err
		}
		req.VaultIds = append(req.VaultIds, vaultIDs...)
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		respResult, err := client.Vaults.CheckCredentials(cmd.Context(), kha.CheckVaultCredentialsHTTPRequest{Body: &req})
		if err != nil {
			return err
		}
		resp := respResult.Body

		headers := []string{"MCP SERVER URL", "MATCHING VAULTS"}
		rows := make([][]string, 0, len(resp.Coverage))
		for _, cov := range resp.Coverage {
			matches := strings.Join(cov.MatchingVaultIds, ", ")
			rows = append(rows, []string{cov.MCPServerURL, output.Dash(matches)})
		}
		return printList(cmd, g, headers, rows, resp, "coverage")
	})
	return cmd
}
