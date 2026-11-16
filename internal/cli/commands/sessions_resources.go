package commands

import (
	"github.com/MoonshotAI/kha"
	"github.com/spf13/cobra"
)

var sessionResourceHeaders = []string{"ID", "TYPE", "DETAIL"}

// sessionResourceRow renders one resource binding; the DETAIL column carries
// the bound object's identity (file id, vault id, or memory store id).
func sessionResourceRow(r *kha.SessionResource) []string {
	id, kind, detail := "-", unionDiscriminator(r.Raw), "-"
	switch {
	case r.AsSessionResourceFile() != nil:
		f := r.AsSessionResourceFile()
		id = valueOr(f.ID, "-")
		kind = string(f.Type)
		detail = f.FileID
		if f.Path != nil {
			detail += " -> " + *f.Path
		}
	case r.AsSessionResourceVault() != nil:
		v := r.AsSessionResourceVault()
		id = valueOr(v.ID, "-")
		kind = string(v.Type)
		detail = v.VaultID
	case r.AsSessionResourceMemoryStore() != nil:
		m := r.AsSessionResourceMemoryStore()
		id = valueOr(m.ID, "-")
		kind = string(m.Type)
		detail = m.MemoryStoreID + " (" + string(m.Access) + ")"
	case r.AsSessionResourceGitHubRepository() != nil:
		repo := r.AsSessionResourceGitHubRepository()
		id = valueOr(repo.ID, "-")
		kind = string(repo.Type)
		detail = repo.URL
		if repo.MountPath != nil {
			detail += " -> " + *repo.MountPath
		}
	}
	return []string{id, kind, detail}
}

func newSessionsResourcesCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "resources",
		Short: "Manage a Session's resource bindings",
	}
	cmd.AddCommand(
		newSessionsResourcesListCmd(g),
		newSessionsResourcesAddCmd(g),
		newSessionsResourcesGetCmd(g),
		newSessionsResourcesDeleteCmd(g),
	)
	return cmd
}

func newSessionsResourcesListCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "list <session-id>",
		Short:             "List the resources bound to a Session",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: sessionIDCompletion,
	}
	lf := addListFlags(cmd)
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		if err := lf.validate(); err != nil {
			return err
		}
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		params := kha.ListSessionResourcesRequest{ID: args[0], PageSize: lf.pageSize(), PageToken: lf.token()}
		if lf.all {
			return printAll(cmd, g, sessionResourceHeaders, client.Sessions.AllResources(cmd.Context(), params), sessionResourceRow)
		}
		listResult, err := client.Sessions.ListResources(cmd.Context(), params)
		if err != nil {
			return err
		}
		list := listResult.Body
		list.Items = nonNilItems(list.Items)

		rows := make([][]string, 0, len(list.Items))
		for i := range list.Items {
			rows = append(rows, sessionResourceRow(list.Items[i]))
		}
		return printList(cmd, g, sessionResourceHeaders, rows, list)
	})
	return cmd
}

// newSessionsResourcesAddCmd binds an uploaded file to a Session. Only files
// can be added after creation; vault and memory-store bindings are fixed at
// Session creation.
func newSessionsResourcesAddCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "add <session-id>",
		Short:             "Bind an uploaded file to a Session",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: sessionIDCompletion,
		Example:           `  kha sessions resources add sess_xxx --file-id file_yyy`,
	}
	var fileID string
	cmd.Flags().StringVar(&fileID, "file-id", "", "id of the uploaded file to bind (file_...)")
	mustRegisterFlagCompletion(cmd, "file-id", fileIDCompletion)
	body := addBodyFlag(cmd, "file-id")
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		var req kha.AddSessionResourceRequest
		if cmd.Flags().Changed("file") {
			if err := body.decode(cmd, &req); err != nil {
				return err
			}
		} else {
			if fileID == "" {
				return errUsage("sessions resources add requires --file-id (or --file)")
			}
			req = kha.AddSessionResourceRequest{Type: kha.AddSessionResourceRequestTypeFile, FileID: fileID}
		}
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		resResult, err := client.Sessions.AddResource(cmd.Context(), kha.AddSessionResourceHTTPRequest{ID: args[0], Body: &req})
		if err != nil {
			return err
		}
		res := resResult.Body

		note(cmd, "resource bound to session %s", args[0])
		return printOne(cmd, g, sessionResourceHeaders, sessionResourceRow(res), res)
	})
	return cmd
}

func newSessionsResourcesGetCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:               "get <session-id> <resource-id>",
		Short:             "Get one resource binding of a Session",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: sessionResourceIDCompletion,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			client, _, err := newClient(g)
			if err != nil {
				return err
			}
			resResult, err := client.Sessions.GetResource(cmd.Context(), kha.GetSessionResourceRequest{ID: args[0], ResourceID: args[1]})
			if err != nil {
				return err
			}
			res := resResult.Body

			return printOne(cmd, g, sessionResourceHeaders, sessionResourceRow(res), res)
		}),
	}
}

func newSessionsResourcesDeleteCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:               "delete <session-id> <resource-id>",
		Short:             "Remove a resource binding from a Session",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: sessionResourceIDCompletion,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			client, _, err := newClient(g)
			if err != nil {
				return err
			}
			if _, err := client.Sessions.DeleteResource(cmd.Context(), kha.DeleteSessionResourceRequest{ID: args[0], ResourceID: args[1]}); err != nil {
				return err
			}
			note(cmd, "resource %s removed from session %s", args[1], args[0])
			return nil
		}),
	}
}
