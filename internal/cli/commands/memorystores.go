package commands

import (
	"fmt"
	"time"

	"github.com/MoonshotAI/kha"

	"github.com/spf13/cobra"

	"github.com/MoonshotAI/kha/internal/cli/output"
)

var memoryStoreHeaders = []string{"ID", "NAME", "CREATED"}

func memoryStoreRow(m *kha.MemoryStore) []string {
	return []string{m.ID, m.Name, output.Time(m.CreatedAt)}
}

var dreamPolicyHeaders = []string{"MEMORY STORE", "AGENT", "ACTIVITY", "SCHEDULE", "TRIGGER", "UPDATED"}

func dreamPolicyRow(p *kha.DreamPolicy) []string {
	activity := "off"
	if p.Activity.Enabled {
		threshold := "-"
		if p.Activity.MinChangedSessions != nil {
			threshold = fmt.Sprint(*p.Activity.MinChangedSessions)
		}
		activity = fmt.Sprintf("on (>=%s sessions)", threshold)
	}
	schedule := "off"
	if p.Schedule.Enabled {
		schedule = fmt.Sprintf("on (%s %s)", valueOr(p.Schedule.LocalTime, "-"), valueOr(p.Schedule.Timezone, "-"))
	}
	return []string{p.MemoryStoreID, valueOr(p.AgentID, "-"), activity, schedule, valueOr(p.TriggerID, "-"), output.Time(valueOr(p.UpdatedAt, time.Time{}))}
}

func newMemoryStoresCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "memory-stores",
		Aliases: []string{"memorystores"},
		Short:   "Manage Memory Stores",
	}
	cmd.AddCommand(
		newMemoryStoresListCmd(g),
		newMemoryStoresGetCmd(g),
		newMemoryStoresCreateCmd(g),
		newMemoryStoresUpdateCmd(g),
		newMemoryStoresEditCmd(g),
		newMemoryStoresDeleteCmd(g),
		newMemoryStoresArchiveCmd(g),
		newMemoriesCmd(g),
		newMemoryStoresDreamPolicyCmd(g),
	)
	return cmd
}

// newMemoryStoresDreamPolicyCmd reads the policy by default; with --file it
// replaces the whole policy (the update is a full replacement: the body
// always carries both the activity and the schedule condition).
func newMemoryStoresDreamPolicyCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "dream-policy <memory-store-id>",
		Short:             "Read a Dream policy, or replace it with --file",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: memoryStoreIDCompletion,
		Example: `  kha memory-stores dream-policy mstr_xxx
  cat policy.json | kha memory-stores dream-policy mstr_xxx --file -
  # policy.json: {"activity": {"enabled": true, "min_changed_sessions": 3},
  #               "schedule": {"enabled": false}, "environment_id": "env_xxx"}`,
	}
	body := addBodyFlag(cmd)
	cmd.AddCommand(newMemoryStoresDreamPolicyEditCmd(g), newMemoryStoresDreamRunCmd(g), newMemoryStoresDreamRunsCmd(g))
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		if !cmd.Flags().Changed("file") {
			policyResult, err := client.MemoryStores.GetDreamPolicy(cmd.Context(), kha.GetDreamPolicyRequest{MemoryStoreID: args[0]})
			if err != nil {
				return err
			}
			policy := policyResult.Body

			return printOne(cmd, g, dreamPolicyHeaders, dreamPolicyRow(policy), policy)
		}
		var req kha.UpdateDreamPolicyRequest
		if err := body.decode(cmd, &req); err != nil {
			return err
		}
		policyResult, err := client.MemoryStores.UpdateDreamPolicy(cmd.Context(), kha.UpdateDreamPolicyHTTPRequest{MemoryStoreID: args[0], Body: &req})
		if err != nil {
			return err
		}
		policy := policyResult.Body

		note(cmd, "dream policy of %s replaced", args[0])
		return printOne(cmd, g, dreamPolicyHeaders, dreamPolicyRow(policy), policy)
	})
	return cmd
}

// dreamTriggerID resolves the Trigger the Dream policy manages; each Dream is
// one run of it. A policy that was never configured has no Trigger yet.
func dreamTriggerID(cmd *cobra.Command, client *kha.Client, storeID string) (string, error) {
	policyResult, err := client.MemoryStores.GetDreamPolicy(cmd.Context(), kha.GetDreamPolicyRequest{MemoryStoreID: storeID})
	if err != nil {
		return "", err
	}
	if id := valueOr(policyResult.Body.TriggerID, ""); id != "" {
		return id, nil
	}
	return "", fmt.Errorf("memory store %s has no Dream policy yet; configure one with `kha memory-stores dream-policy %s --file -`", storeID, storeID)
}

func newMemoryStoresDreamRunCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run <memory-store-id>",
		Short: "Start a Dream now; --wait blocks until it ends",
		Long: `Start a Dream for a Memory Store now, without waiting for the policy's
activity or schedule condition. A Dream is one run of the Trigger the Dream
policy manages, so the output is a Trigger run. To stop a Dream, interrupt the
run's Session with kha sessions interrupt.`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: memoryStoreIDCompletion,
		Example: `  kha memory-stores dream-policy run mstr_xxx --wait
  kha memory-stores dream-policy runs mstr_xxx --has-error`,
	}
	var wait bool
	cmd.Flags().BoolVarP(&wait, "wait", "w", false, "wait for the Dream to reach a terminal status and print the outcome")
	wf := addWaitFlags(cmd)
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		triggerID, err := dreamTriggerID(cmd, client, args[0])
		if err != nil {
			return err
		}
		response, err := client.Triggers.Run(cmd.Context(), kha.RunTriggerHTTPRequest{ID: triggerID})
		if err != nil {
			return err
		}
		note(cmd, "dream started for %s (trigger %s, run %s)", args[0], triggerID, response.Body.ID)
		return finishTriggerRun(cmd, g, client, triggerID, response.Body, wait, wf)
	})
	return cmd
}

func newMemoryStoresDreamRunsCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "runs <memory-store-id>",
		Short:             "List a Memory Store's Dreams (newest first)",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: memoryStoreIDCompletion,
	}
	lf := addListFlags(cmd)
	var hasError bool
	cmd.Flags().BoolVar(&hasError, "has-error", false, "only Dreams with a failure")
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		if err := lf.validate(); err != nil {
			return err
		}
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		triggerID, err := dreamTriggerID(cmd, client, args[0])
		if err != nil {
			return err
		}
		return listTriggerRuns(cmd, g, client, triggerID, lf, hasError)
	})
	return cmd
}

func newMemoryStoresListCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List Memory Stores",
		Args:  cobra.NoArgs,
	}
	lf := addListFlags(cmd)
	var includeArchived bool
	cmd.Flags().BoolVar(&includeArchived, "include-archived", false, "include archived stores")
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		if err := lf.validate(); err != nil {
			return err
		}
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		params := kha.ListMemoryStoresRequest{PageSize: lf.pageSize(), PageToken: lf.token()}
		if includeArchived {
			params.IncludeArchived = new(true)
		}
		if lf.all {
			return printAll(cmd, g, memoryStoreHeaders, client.MemoryStores.All(cmd.Context(), params), memoryStoreRow)
		}
		itemsResult, err := client.MemoryStores.List(cmd.Context(), params)
		if err != nil {
			return err
		}
		page := itemsResult.Body
		page.Items = nonNilItems(page.Items)
		page.NextPageToken = nonEmptyPageToken(page.NextPageToken)

		rows := make([][]string, 0, len(page.Items))
		for _, m := range page.Items {
			rows = append(rows, memoryStoreRow(m))
		}
		return printList(cmd, g, memoryStoreHeaders, rows, page)
	})
	return cmd
}

func newMemoryStoresGetCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:               "get <id>",
		Short:             "Get one Memory Store",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: memoryStoreIDCompletion,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			client, _, err := newClient(g)
			if err != nil {
				return err
			}
			storeResult, err := client.MemoryStores.Get(cmd.Context(), kha.GetMemoryStoreRequest{ID: args[0]})
			if err != nil {
				return err
			}
			store := storeResult.Body

			return printOne(cmd, g, memoryStoreHeaders, memoryStoreRow(store), store)
		}),
	}
}

func newMemoryStoresCreateCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a Memory Store",
		Args:  cobra.NoArgs,
	}
	var name, description string
	cmd.Flags().StringVarP(&name, "name", "n", "", "store name (unique within the project; also the Session mount directory)")
	cmd.Flags().StringVarP(&description, "description", "d", "", "description shown to the Agent")
	body := addBodyFlag(cmd, "name", "description")
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		var req kha.CreateMemoryStoreRequest
		if cmd.Flags().Changed("file") {
			if err := body.decode(cmd, &req); err != nil {
				return err
			}
		} else {
			if name == "" {
				return errUsage("memory-stores create requires --name (or --file)")
			}
			req.Name = name
			if cmd.Flags().Changed("description") {
				req.Description = new(description)
			}
		}
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		storeResult, err := client.MemoryStores.Create(cmd.Context(), kha.CreateMemoryStoreHTTPRequest{Body: &req})
		if err != nil {
			return err
		}
		store := storeResult.Body

		note(cmd, "memory store %s created", store.ID)
		return printOne(cmd, g, memoryStoreHeaders, memoryStoreRow(store), store)
	})
	return cmd
}

func newMemoryStoresUpdateCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "update <id>",
		Short:             "Update a Memory Store",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: memoryStoreIDCompletion,
	}
	var name, description string
	cmd.Flags().StringVarP(&name, "name", "n", "", "new name")
	cmd.Flags().StringVarP(&description, "description", "d", "", "new description")
	body := addBodyFlag(cmd, "name", "description")
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		var req kha.UpdateMemoryStoreRequest
		if cmd.Flags().Changed("file") {
			if err := body.decode(cmd, &req); err != nil {
				return err
			}
		} else {
			changed := false
			if cmd.Flags().Changed("name") {
				req.Name = new(name)
				changed = true
			}
			if cmd.Flags().Changed("description") {
				req.Description = new(description)
				changed = true
			}
			if !changed {
				return errUsage("nothing to update: pass at least one field flag or --file")
			}
		}
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		storeResult, err := client.MemoryStores.Update(cmd.Context(), kha.UpdateMemoryStoreHTTPRequest{ID: args[0], Body: &req})
		if err != nil {
			return err
		}
		store := storeResult.Body

		note(cmd, "memory store %s updated", store.ID)
		return printOne(cmd, g, memoryStoreHeaders, memoryStoreRow(store), store)
	})
	return cmd
}

func newMemoryStoresDeleteCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:               "delete <id>",
		Short:             "Delete a Memory Store",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: memoryStoreIDCompletion,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			client, _, err := newClient(g)
			if err != nil {
				return err
			}
			if _, err := client.MemoryStores.Delete(cmd.Context(), kha.DeleteMemoryStoreRequest{ID: args[0]}); err != nil {
				return err
			}
			note(cmd, "memory store %s deleted", args[0])
			return nil
		}),
	}
}

func newMemoryStoresArchiveCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:               "archive <id>",
		Short:             "Archive a Memory Store",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: memoryStoreIDCompletion,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			client, _, err := newClient(g)
			if err != nil {
				return err
			}
			if _, err := client.MemoryStores.Archive(cmd.Context(), kha.ArchiveMemoryStoreRequest{ID: args[0]}); err != nil {
				return err
			}
			note(cmd, "memory store %s archived", args[0])
			return nil
		}),
	}
}
