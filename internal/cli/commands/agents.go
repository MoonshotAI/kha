package commands

import (
	"github.com/MoonshotAI/kha"
	"github.com/spf13/cobra"

	"github.com/MoonshotAI/kha/internal/cli/output"
)

var agentHeaders = []string{"ID", "NAME", "VERSION", "MODEL", "CREATED"}

func agentRow(a *kha.Agent) []string {
	return []string{a.ID, a.Name, a.Version, modelRefString(a.Model), output.Time(a.CreatedAt)}
}

func newAgentsCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agents",
		Short: "Configure Agents and manage their immutable versions",
	}
	cmd.AddCommand(
		newAgentsListCmd(g),
		newAgentsGetCmd(g),
		newAgentsCreateCmd(g),
		newAgentsUpdateCmd(g),
		newAgentsEditCmd(g),
		newAgentsArchiveCmd(g),
		newAgentsVersionsCmd(g),
	)
	return cmd
}

func newAgentsListCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List Agents (newest first; archived Agents are never listed)",
		Args:  cobra.NoArgs,
	}
	lf := addListFlags(cmd)
	var order, since string
	var excludeMultiagent, officialOnly bool
	cmd.Flags().StringVar(&order, "order", "", "listing order: created_at or created_at_desc (default)")
	cmd.Flags().BoolVar(&excludeMultiagent, "exclude-multiagent", false, "only Agents without a multiagent configuration (subagent-picker mode)")
	cmd.Flags().BoolVar(&officialOnly, "official-only", false, "only official Agents provided by the platform")
	cmd.Flags().StringVar(&since, "since", "", "only Agents created within a relative duration (24h, 7d, 2w) or after a date (2026-11-01)")
	mustRegisterFlagCompletion(cmd, "order", completeFlagStatic("created_at", "created_at_desc"))
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		if err := lf.validate(); err != nil {
			return err
		}
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		params := kha.ListAgentsRequest{PageSize: lf.pageSize(), PageToken: lf.token()}
		if order != "" {
			params.Order = new(kha.AgentOrder(order))
		}
		if excludeMultiagent {
			params.ExcludeMultiagent = new(true)
		}
		if officialOnly {
			params.OfficialOnly = new(true)
		}
		if since != "" {
			t, err := parseSince(since)
			if err != nil {
				return err
			}
			params.CreatedAtGte = new(t)
		}
		if lf.all {
			return printAll(cmd, g, agentHeaders, client.Agents.All(cmd.Context(), params), agentRow)
		}
		itemsResult, err := client.Agents.List(cmd.Context(), params)
		if err != nil {
			return err
		}
		page := itemsResult.Body
		page.Items = nonNilItems(page.Items)
		page.NextPageToken = nonEmptyPageToken(page.NextPageToken)

		rows := make([][]string, 0, len(page.Items))
		for _, a := range page.Items {
			rows = append(rows, agentRow(a))
		}
		return printList(cmd, g, agentHeaders, rows, page)
	})
	return cmd
}

func newAgentsGetCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:               "get <id>",
		Short:             "Get one Agent at its latest version",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: agentIDCompletion,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			client, _, err := newClient(g)
			if err != nil {
				return err
			}
			agentResult, err := client.Agents.Get(cmd.Context(), kha.GetAgentRequest{ID: args[0]})
			if err != nil {
				return err
			}
			agent := agentResult.Body

			return printOne(cmd, g, agentHeaders, agentRow(agent), agent)
		}),
	}
}

func newAgentsCreateCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create an Agent and its first immutable version",
		Args:  cobra.NoArgs,
		Example: `  kha agents create --name researcher --model kimi-k3.1-pro --description "web research"
  kha agents create --name analyst --model kimi-k3.1-pro --system-file system.md
  kha agents create --name bot --model kimi-k3.1-pro --tool '{type: custom, name: search, description: web search, input_schema: {type: object}}' --skill sk_xxx@2
  cat agent.json | kha agents create --file -`,
	}
	var name, model, description string
	cmd.Flags().StringVarP(&name, "name", "n", "", "display name of the Agent")
	cmd.Flags().StringVarP(&model, "model", "m", "", "model id, e.g. kimi-k3.1-pro")
	cmd.Flags().StringVarP(&description, "description", "d", "", "description of the Agent")
	system := addTextFlags(cmd, "system", "system prompt")
	var tools []*kha.Tool
	var mcpServers []*kha.MCPServer
	addYAMLFlags(cmd, "one tool as a JSON or YAML object (repeatable)", struct {
		name   string
		target any
	}{"tool", &tools})
	addYAMLFlags(cmd, "one MCP server as a JSON or YAML object (repeatable)", struct {
		name   string
		target any
	}{"mcp-server", &mcpServers})
	var multiagent kha.MultiagentCoordinator
	cmd.Flags().Var(&yamlValue{flag: "multiagent", target: &multiagent}, "multiagent", "coordinator configuration as a JSON or YAML object, e.g. '{agents: [{type: agent, id: agent_xxx, version: \"1\"}]}'")
	metadata := addKVFlag(cmd, "metadata", "key=value metadata entry (repeatable; up to 16)")
	body := addBodyFlag(cmd, "name", "model", "description", "system", "system-file", "tool", "mcp-server", "multiagent", "metadata")
	bindings := addAgentBindingFlags(cmd)
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		var req kha.CreateAgentRequest
		if cmd.Flags().Changed("file") {
			if err := body.decode(cmd, &req); err != nil {
				return err
			}
		} else {
			if name == "" || model == "" {
				return errUsage("agents create requires --name and --model (or --file)")
			}
			req = kha.CreateAgentRequest{Name: name, Model: *kha.NewModelRefString(model)}
			skills, plugins, err := bindings.resolve()
			if err != nil {
				return err
			}
			req.Skills, req.Plugins = skills, plugins
			if cmd.Flags().Changed("description") {
				req.Description = new(description)
			}
			text, err := system.resolve(cmd)
			if err != nil {
				return err
			}
			req.System = text
			req.Tools, req.MCPServers = tools, mcpServers
			if cmd.Flags().Changed("multiagent") {
				req.Multiagent = &multiagent
			}
			req.Metadata = metadata.mapValue()
		}
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		agentResult, err := client.Agents.Create(cmd.Context(), kha.CreateAgentHTTPRequest{Body: &req})
		if err != nil {
			return err
		}
		agent := agentResult.Body

		note(cmd, "agent %s created (version %s)", agent.ID, agent.Version)
		return printOne(cmd, g, agentHeaders, agentRow(agent), agent)
	})
	return cmd
}

func newAgentsUpdateCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "update <id>",
		Short:             "Update an Agent, creating a new immutable version",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: agentIDCompletion,
		Example: `  kha agents update agent_xxx --version 3 --system-file system.md
  kha agents update agent_xxx --tool '{type: custom, name: search, description: web search, input_schema: {type: object}}'
  cat system.md | kha agents update agent_xxx --version 3 --system-file -`,
	}
	var name, model, description, agentVersion string
	cmd.Flags().StringVarP(&name, "name", "n", "", "new display name")
	cmd.Flags().StringVarP(&model, "model", "m", "", "new model id")
	cmd.Flags().StringVarP(&description, "description", "d", "", "new description (empty clears)")
	system := addTextFlags(cmd, "system", "new system prompt (empty clears)")
	cmd.Flags().StringVar(&agentVersion, "version", "", "current version for the optimistic-concurrency check")
	var tools []*kha.Tool
	var mcpServers []*kha.MCPServer
	addYAMLFlags(cmd, "one tool as a JSON or YAML object (repeatable)", struct {
		name   string
		target any
	}{"tool", &tools})
	addYAMLFlags(cmd, "one MCP server as a JSON or YAML object (repeatable)", struct {
		name   string
		target any
	}{"mcp-server", &mcpServers})
	var multiagent kha.MultiagentCoordinator
	cmd.Flags().Var(&yamlValue{flag: "multiagent", target: &multiagent}, "multiagent", "replacement coordinator configuration as a JSON or YAML object; '{agents: []}' clears it")
	metadata := addKVFlag(cmd, "metadata", "key=value metadata entry (repeatable; replaces the whole map)")
	body := addBodyFlag(cmd, "name", "model", "description", "system", "system-file", "version", "tool", "mcp-server", "multiagent", "metadata")
	bindings := addAgentBindingFlags(cmd)
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		var req kha.UpdateAgentRequest
		if cmd.Flags().Changed("file") {
			if err := body.decode(cmd, &req); err != nil {
				return err
			}
		} else {
			skills, plugins, err := bindings.resolve()
			if err != nil {
				return err
			}
			req.Skills, req.Plugins = skills, plugins
			changed := len(skills) > 0 || len(plugins) > 0
			if cmd.Flags().Changed("name") {
				req.Name = new(name)
				changed = true
			}
			if cmd.Flags().Changed("model") {
				req.Model = kha.NewModelRefString(model)
				changed = true
			}
			if cmd.Flags().Changed("description") {
				req.Description = new(description)
				changed = true
			}
			text, err := system.resolve(cmd)
			if err != nil {
				return err
			}
			if text != nil {
				req.System = text
				changed = true
			}
			if len(tools) > 0 {
				req.Tools = tools
				changed = true
			}
			if len(mcpServers) > 0 {
				req.MCPServers = mcpServers
				changed = true
			}
			if cmd.Flags().Changed("multiagent") {
				req.Multiagent = &multiagent
				changed = true
			}
			if m := metadata.mapValue(); m != nil {
				req.Metadata = m
				changed = true
			}
			if cmd.Flags().Changed("version") {
				req.Version = new(agentVersion)
			}
			if !changed {
				return errUsage("nothing to update: pass at least one field flag or --file")
			}
		}
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		agentResult, err := client.Agents.Update(cmd.Context(), kha.UpdateAgentHTTPRequest{ID: args[0], Body: &req})
		if err != nil {
			return err
		}
		agent := agentResult.Body

		note(cmd, "agent %s updated (new version %s)", agent.ID, agent.Version)
		return printOne(cmd, g, agentHeaders, agentRow(agent), agent)
	})
	return cmd
}

func newAgentsArchiveCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:               "archive <id>",
		Short:             "Archive an Agent (it stays readable but cannot run new Sessions)",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: agentIDCompletion,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			client, _, err := newClient(g)
			if err != nil {
				return err
			}
			if _, err := client.Agents.Archive(cmd.Context(), kha.ArchiveAgentRequest{ID: args[0]}); err != nil {
				return err
			}
			note(cmd, "agent %s archived", args[0])
			return nil
		}),
	}
}

func newAgentsVersionsCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "versions <id>",
		Short:             "List an Agent's immutable versions (newest first)",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: agentIDCompletion,
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
		params := kha.ListAgentVersionsRequest{ID: args[0], PageSize: lf.pageSize(), PageToken: lf.token()}
		headers := []string{"VERSION", "NAME", "MODEL", "CREATED"}
		versionRow := func(a *kha.Agent) []string {
			return []string{a.Version, a.Name, modelRefString(a.Model), output.Time(a.CreatedAt)}
		}
		if lf.all {
			return printAll(cmd, g, headers, client.Agents.AllVersions(cmd.Context(), params), versionRow)
		}
		itemsResult, err := client.Agents.ListVersions(cmd.Context(), params)
		if err != nil {
			return err
		}
		page := itemsResult.Body
		page.Items = nonNilItems(page.Items)
		page.NextPageToken = nonEmptyPageToken(page.NextPageToken)

		rows := make([][]string, 0, len(page.Items))
		for _, a := range page.Items {
			rows = append(rows, versionRow(a))
		}
		return printList(cmd, g, headers, rows, page)
	})
	return cmd
}
