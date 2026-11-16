package commands

import (
	"github.com/MoonshotAI/kha"
	"github.com/spf13/cobra"

	"github.com/MoonshotAI/kha/internal/cli/output"
)

var vaultHeaders = []string{"ID", "DISPLAY NAME", "CREATED"}

func vaultRow(v *kha.Vault) []string {
	return []string{v.ID, v.DisplayName, output.Time(v.CreatedAt)}
}

func newVaultsCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "vaults",
		Short: "Manage Vaults that hold credentials for Session binding",
	}
	cmd.AddCommand(
		newVaultsListCmd(g),
		newVaultsGetCmd(g),
		newVaultsCreateCmd(g),
		newVaultsUpdateCmd(g),
		newVaultsEditCmd(g),
		newVaultsDeleteCmd(g),
		newVaultsArchiveCmd(g),
		newVaultsCredentialsCmd(g),
		newVaultsOAuthCmd(g),
	)
	return cmd
}

func newVaultsListCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List Vaults",
		Args:  cobra.NoArgs,
	}
	lf := addListFlags(cmd)
	var includeArchived bool
	cmd.Flags().BoolVar(&includeArchived, "include-archived", false, "include archived Vaults")
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		if err := lf.validate(); err != nil {
			return err
		}
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		params := kha.ListVaultsRequest{PageSize: lf.pageSize(), PageToken: lf.token()}
		if includeArchived {
			params.IncludeArchived = new(true)
		}
		if lf.all {
			return printAll(cmd, g, vaultHeaders, client.Vaults.All(cmd.Context(), params),
				vaultRow)
		}
		itemsResult, err := client.Vaults.List(cmd.Context(), params)
		if err != nil {
			return err
		}
		page := itemsResult.Body
		page.Items = nonNilItems(page.Items)
		page.NextPageToken = nonEmptyPageToken(page.NextPageToken)

		rows := make([][]string, 0, len(page.Items))
		for _, item := range page.Items {
			rows = append(rows, vaultRow(item))
		}
		return printList(cmd, g, vaultHeaders, rows, page)
	})
	return cmd
}

func newVaultsGetCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:               "get <id>",
		Short:             "Get one Vault",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: vaultIDCompletion,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			client, _, err := newClient(g)
			if err != nil {
				return err
			}
			vaultResult, err := client.Vaults.Get(cmd.Context(), kha.GetVaultRequest{ID: args[0]})
			if err != nil {
				return err
			}
			vault := vaultResult.Body

			return printOne(cmd, g, vaultHeaders, vaultRow(vault), vault)
		}),
	}
}

func newVaultsCreateCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a Vault",
		Args:  cobra.NoArgs,
	}
	var displayName string
	cmd.Flags().StringVar(&displayName, "display-name", "", "human-readable Vault name")
	body := addBodyFlag(cmd, "display-name")
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		var req kha.CreateVaultRequest
		if cmd.Flags().Changed("file") {
			if err := body.decode(cmd, &req); err != nil {
				return err
			}
		} else {
			if displayName == "" {
				return errUsage("vaults create requires --display-name (or --file)")
			}
			req.DisplayName = displayName
		}
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		vaultResult, err := client.Vaults.Create(cmd.Context(), kha.CreateVaultHTTPRequest{Body: &req})
		if err != nil {
			return err
		}
		vault := vaultResult.Body

		note(cmd, "vault %s created", vault.ID)
		return printOne(cmd, g, vaultHeaders, vaultRow(vault), vault)
	})
	return cmd
}

func newVaultsUpdateCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "update <id>",
		Short:             "Update a Vault",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: vaultIDCompletion,
	}
	var displayName string
	cmd.Flags().StringVar(&displayName, "display-name", "", "new display name")
	body := addBodyFlag(cmd, "display-name")
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		var req kha.UpdateVaultRequest
		if cmd.Flags().Changed("file") {
			if err := body.decode(cmd, &req); err != nil {
				return err
			}
		} else {
			if !cmd.Flags().Changed("display-name") {
				return errUsage("nothing to update: pass --display-name or --file")
			}
			req.DisplayName = new(displayName)
		}
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		vaultResult, err := client.Vaults.Update(cmd.Context(), kha.UpdateVaultHTTPRequest{ID: args[0], Body: &req})
		if err != nil {
			return err
		}
		vault := vaultResult.Body

		note(cmd, "vault %s updated", vault.ID)
		return printOne(cmd, g, vaultHeaders, vaultRow(vault), vault)
	})
	return cmd
}

func newVaultsDeleteCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:               "delete <id>",
		Short:             "Delete a Vault",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: vaultIDCompletion,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			client, _, err := newClient(g)
			if err != nil {
				return err
			}
			if _, err := client.Vaults.Delete(cmd.Context(), kha.DeleteVaultRequest{ID: args[0]}); err != nil {
				return err
			}
			note(cmd, "vault %s deleted", args[0])
			return nil
		}),
	}
}

func newVaultsArchiveCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:               "archive <id>",
		Short:             "Archive a Vault",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: vaultIDCompletion,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			client, _, err := newClient(g)
			if err != nil {
				return err
			}
			if _, err := client.Vaults.Archive(cmd.Context(), kha.ArchiveVaultRequest{ID: args[0]}); err != nil {
				return err
			}
			note(cmd, "vault %s archived", args[0])
			return nil
		}),
	}
}
