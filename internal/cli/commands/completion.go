package commands

import (
	"context"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	kha "github.com/MoonshotAI/kha"
	"github.com/MoonshotAI/kha/internal/cli/config"
	"github.com/MoonshotAI/kha/internal/cli/output"
)

// completeIDs resolves identifiers only while Cobra is completing a command.
// It deliberately returns no diagnostics: completion output is a protocol
// consumed by the shell, not user-facing command output.
func completeIDs(fetch func(context.Context, *kha.Client) ([]string, error)) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return completeIDsAt(0, func(ctx context.Context, c *kha.Client, _ []string) ([]string, error) {
		return fetch(ctx, c)
	})
}

// completeIDsAt is completeIDs for the positional argument at index position,
// with the args already given passed to fetch (e.g. a parent resource ID).
func completeIDsAt(position int, fetch func(context.Context, *kha.Client, []string) ([]string, error)) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) != position {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		client, _, err := newClientQuiet(cmd)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		ids, err := fetch(cmd.Context(), client, args)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return ids, cobra.ShellCompDirectiveNoFileComp
	}
}

// mustRegisterFlagCompletion wires fn as the completion of cmd's flag name.
// RegisterFlagCompletionFunc only fails when the flag does not exist — a
// programming error at tree-build time, so it panics: every test and every
// process startup builds the tree and trips it immediately.
func mustRegisterFlagCompletion(cmd *cobra.Command, name string, fn func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective)) {
	if err := cmd.RegisterFlagCompletionFunc(name, fn); err != nil {
		panic(err)
	}
}

// completeFlagStatic completes a flag whose value is one of a fixed set.
func completeFlagStatic(values ...string) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return values, cobra.ShellCompDirectiveNoFileComp
	}
}

// completeFlagStaticCSV completes a comma-separated flag value: the part of
// toComplete before the last comma stays as the prefix, so "idle,run"
// completes to "idle,running".
func completeFlagStaticCSV(values ...string) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		prefix := ""
		if i := strings.LastIndex(toComplete, ","); i >= 0 {
			prefix = toComplete[:i+1]
		}
		out := make([]string, len(values))
		for i, v := range values {
			out[i] = prefix + v
		}
		return out, cobra.ShellCompDirectiveNoFileComp
	}
}

// completeFlagIDs is completeIDs for flag values, with the same
// silent-failure contract; args are the positionals given so far (a flag
// completion has no positional contract of its own — fetch checks what it
// needs, e.g. a parent resource ID at args[0]).
func completeFlagIDs(fetch func(context.Context, *kha.Client, []string) ([]string, error)) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		client, _, err := newClientQuiet(cmd)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		ids, err := fetch(cmd.Context(), client, args)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return ids, cobra.ShellCompDirectiveNoFileComp
	}
}

// profileCompletion completes --profile from the local config file — the one
// dynamic completion that must never touch the network.
func profileCompletion(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	cfg, err := config.Load()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	names := make([]string, 0, len(cfg.Profiles))
	for name := range cfg.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, cobra.ShellCompDirectiveNoFileComp
}

// completionCandidate builds one "id\tdescription" candidate. Both halves are
// server-provided, so escapes are stripped and line/tab breaks (which would
// corrupt the shell's completion protocol) become spaces.
func completionCandidate(id, description string) string {
	sanitize := func(s string) string {
		return output.StripEscapes(strings.Map(func(r rune) rune {
			if r == '\n' || r == '\r' || r == '\t' {
				return ' '
			}
			return r
		}, s))
	}
	id = sanitize(id)
	description = sanitize(description)
	if description == "" {
		return id
	}
	return id + "\t" + description
}

func newClientQuiet(cmd *cobra.Command) (*kha.Client, credentials, error) {
	// Completion uses the same connection selection as command execution.
	g := globalFlags{}
	if f := cmd.Root().PersistentFlags().Lookup("profile"); f != nil {
		g.profile = f.Value.String()
	}
	if f := cmd.Root().PersistentFlags().Lookup("api-key"); f != nil {
		g.apiKey = f.Value.String()
	}
	if f := cmd.Root().PersistentFlags().Lookup("base-url"); f != nil {
		g.baseURL = f.Value.String()
	}
	if f := cmd.Root().PersistentFlags().Lookup("api-version"); f != nil {
		g.apiVersion = f.Value.String()
	}
	return newClient(&g)
}

func agentIDCompletion(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return completeIDs(func(ctx context.Context, c *kha.Client) ([]string, error) {
		agentsResult, err := c.Agents.List(ctx, kha.ListAgentsRequest{})
		if err != nil {
			return nil, err
		}
		agents := nonNilItems(agentsResult.Body.Items)

		ids := make([]string, len(agents))
		for i, a := range agents {
			ids[i] = completionCandidate(a.ID, a.Name+" v"+a.Version)
		}
		return ids, nil
	})(cmd, args, toComplete)
}

// agentVersionFlagCompletion completes --agent-version from the versions of
// the Agent currently named by the command's --agent flag.
func agentVersionFlagCompletion(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	f := cmd.Flags().Lookup("agent")
	if f == nil || f.Value.String() == "" {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	agentID := f.Value.String()
	return completeFlagIDs(func(ctx context.Context, c *kha.Client, _ []string) ([]string, error) {
		versionsResult, err := c.Agents.ListVersions(ctx, kha.ListAgentVersionsRequest{ID: agentID})
		if err != nil {
			return nil, err
		}
		versions := nonNilItems(versionsResult.Body.Items)

		ids := make([]string, len(versions))
		for i, a := range versions {
			ids[i] = completionCandidate(a.Version, a.Name+" ("+modelRefString(a.Model)+")")
		}
		return ids, nil
	})(cmd, args, toComplete)
}

func sessionIDCompletion(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return completeIDs(func(ctx context.Context, c *kha.Client) ([]string, error) {
		sessionsResult, err := c.Sessions.List(ctx, kha.ListSessionsRequest{})
		if err != nil {
			return nil, err
		}
		sessions := nonNilItems(sessionsResult.Body.Items)

		ids := make([]string, len(sessions))
		for i, s := range sessions {
			desc := string(s.Status)
			if title := s.Title; title != nil && *title != "" {
				desc = *title + " (" + desc + ")"
			}
			ids[i] = completionCandidate(s.ID, desc)
		}
		return ids, nil
	})(cmd, args, toComplete)
}

// sessionResourceIDCompletion completes the Session ID at the first positional
// and the resource binding IDs inside it at the second.
func sessionResourceIDCompletion(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return sessionIDCompletion(cmd, args, toComplete)
	}
	return completeIDsAt(1, func(ctx context.Context, c *kha.Client, args []string) ([]string, error) {
		listResult, err := c.Sessions.ListResources(ctx, kha.ListSessionResourcesRequest{ID: args[0]})
		if err != nil {
			return nil, err
		}
		list := listResult.Body

		items := nonNilItems(list.Items)
		ids := make([]string, 0, len(items))
		for _, r := range items {
			if r == nil {
				continue
			}
			if f := r.AsSessionResourceFile(); f != nil {
				if f.ID != nil {
					detail := "file " + f.FileID
					if f.Path != nil {
						detail += " -> " + *f.Path
					}
					ids = append(ids, completionCandidate(*f.ID, detail))
				}
			}
			if v := r.AsSessionResourceVault(); v != nil {
				if v.ID != nil {
					ids = append(ids, completionCandidate(*v.ID, "vault "+v.VaultID))
				}
			}
			if m := r.AsSessionResourceMemoryStore(); m != nil {
				if m.ID != nil {
					ids = append(ids, completionCandidate(*m.ID, "memory-store "+m.MemoryStoreID+" ("+string(m.Access)+")"))
				}
			}
			if repo := r.AsSessionResourceGitHubRepository(); repo != nil {
				if repo.ID != nil {
					detail := "repo " + repo.URL
					if repo.MountPath != nil {
						detail += " -> " + *repo.MountPath
					}
					ids = append(ids, completionCandidate(*repo.ID, detail))
				}
			}
		}
		return ids, nil
	})(cmd, args, toComplete)
}

// sessionThreadIDCompletion completes the Session ID at the first positional
// and the Thread IDs inside it at the second.
func sessionThreadIDCompletion(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return sessionIDCompletion(cmd, args, toComplete)
	}
	return completeIDsAt(1, func(ctx context.Context, c *kha.Client, args []string) ([]string, error) {
		return threadCandidates(ctx, c, args[0])
	})(cmd, args, toComplete)
}

// threadCandidates lists one Session's Thread IDs with status descriptions.
func threadCandidates(ctx context.Context, c *kha.Client, sessionID string) ([]string, error) {
	threadsResult, err := c.Sessions.ListThreads(ctx, kha.ListSessionThreadsRequest{ID: sessionID})
	if err != nil {
		return nil, err
	}
	threads := nonNilItems(threadsResult.Body.Items)

	ids := make([]string, 0, len(threads))
	for _, t := range threads {
		desc := string(t.Status)
		if agent := t.Agent; agent != nil {
			desc = agent.Name + "@" + agent.Version + ", " + desc
		}
		if t.ParentThreadID == nil {
			desc = "coordinator, " + desc
		}
		ids = append(ids, completionCandidate(t.ID, desc))
	}
	return ids, nil
}

// sessionThreadFlagCompletion completes sessions attach --thread from the
// Threads of the Session named by the first positional.
func sessionThreadFlagCompletion(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return completeFlagIDs(func(ctx context.Context, c *kha.Client, args []string) ([]string, error) {
		if len(args) != 1 {
			return nil, nil
		}
		return threadCandidates(ctx, c, args[0])
	})(cmd, args, toComplete)
}

// sessionPendingEventIDCompletion completes the Session ID at the first
// positional and the queued input event IDs inside it at the second.
func sessionPendingEventIDCompletion(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return sessionIDCompletion(cmd, args, toComplete)
	}
	return completeIDsAt(1, func(ctx context.Context, c *kha.Client, args []string) ([]string, error) {
		listResult, err := c.Sessions.ListPendingEvents(ctx, kha.ListPendingEventsRequest{ID: args[0]})
		if err != nil {
			return nil, err
		}
		list := listResult.Body

		items := nonNilItems(list.Items)
		ids := make([]string, 0, len(items))
		for _, p := range items {
			if p == nil {
				continue
			}
			desc := string(p.Type)
			if summary := pendingCompletionSummary(p.Events); summary != "" {
				desc += ": " + summary
			}
			ids = append(ids, completionCandidate(p.EventID, desc))
		}
		return ids, nil
	})(cmd, args, toComplete)
}

// pendingCompletionSummary summarizes the user events carried by one queued
// input for a completion description. Queued inputs are always user.message
// shaped, so the summary is the message's one-line text preview; an input of
// any other (future) type falls back to its raw discriminator.
func pendingCompletionSummary(events []*kha.QueuedUserEventInput) string {
	summaries := make([]string, 0, len(events))
	for _, in := range events {
		if in != nil && in.Type == kha.UserMessageInputTypeUserMessage {
			summaries = append(summaries, "> "+completionOneLine(completionContentText(in.Data.Content)))
			continue
		}
		discriminator := ""
		if in != nil {
			discriminator = string(in.Type)
		}
		summaries = append(summaries, "[unknown event "+discriminator+"]")
	}
	return strings.Join(summaries, "; ")
}

// completionSummaryWidth caps one-line completion previews.
const completionSummaryWidth = 160

// completionContentText concatenates the text blocks of a content list;
// non-text blocks (thinking, tool use) render as a bracketed placeholder.
// Everything it returns has been through output.StripEscapes (server text is
// untrusted).
func completionContentText(blocks []*kha.ContentBlock) string {
	var b strings.Builder
	for _, block := range blocks {
		if block == nil {
			continue
		}
		if t := block.AsTextContentBlock(); t != nil {
			b.WriteString(output.StripEscapes(t.Text))
			continue
		}
		b.WriteString("[")
		b.WriteString(contentBlockDiscriminator(block.Type))
		b.WriteString("]")
	}
	return b.String()
}

// contentBlockDiscriminator maps a known non-text content-block variant back
// to its spec discriminator for the bracketed placeholder.
func contentBlockDiscriminator(t kha.ContentBlockType) string {
	switch t {
	case kha.ContentBlockTypeThinkingContentBlock:
		return "thinking"
	case kha.ContentBlockTypeToolUseContentBlock:
		return "tool_use"
	case kha.ContentBlockTypeToolResultContentBlock:
		return "tool_result"
	case kha.ContentBlockTypeFileToolResultContent:
		return "file"
	}
	return "unknown"
}

// completionOneLine collapses s to its first completionSummaryWidth runes on
// a single line.
func completionOneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	runes := []rune(s)
	if len(runes) > completionSummaryWidth {
		s = string(runes[:completionSummaryWidth]) + "..."
	}
	return s
}

// sessionFSPathCompletion completes the Session ID at the first positional and
// file paths (download targets — directories are not downloadable) at the
// second. When toComplete already names a directory, that directory is listed
// so nested paths complete too: the endpoint lists one directory at a time,
// non-recursively.
func sessionFSPathCompletion(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return sessionIDCompletion(cmd, args, toComplete)
	}
	return completeIDsAt(1, func(ctx context.Context, c *kha.Client, args []string) ([]string, error) {
		params := kha.ListSessionFilesystemRequest{ID: args[0]}
		if i := strings.LastIndex(toComplete, "/"); i > 0 {
			params.Prefix = new(toComplete[:i])
		}
		listResult, err := c.Sessions.ListFilesystem(ctx, params)
		if err != nil {
			return nil, err
		}
		list := listResult.Body

		items := nonNilItems(list.Items)
		paths := make([]string, 0, len(items))
		for _, n := range items {
			if n == nil || n.IsDir {
				continue
			}
			paths = append(paths, completionCandidate(n.Path, output.Bytes(n.SizeBytes)))
		}
		return paths, nil
	})(cmd, args, toComplete)
}

// sessionFSDirFlagCompletion completes sessions fs list --prefix with
// directory paths only (a prefix always names a directory), each with a
// trailing "/" so completion can descend into it. Same one-directory listing
// semantics as sessionFSPathCompletion.
func sessionFSDirFlagCompletion(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return completeFlagIDs(func(ctx context.Context, c *kha.Client, args []string) ([]string, error) {
		if len(args) != 1 {
			return nil, nil
		}
		params := kha.ListSessionFilesystemRequest{ID: args[0]}
		if i := strings.LastIndex(toComplete, "/"); i > 0 {
			params.Prefix = new(toComplete[:i])
		}
		listResult, err := c.Sessions.ListFilesystem(ctx, params)
		if err != nil {
			return nil, err
		}
		list := listResult.Body

		items := nonNilItems(list.Items)
		dirs := make([]string, 0, len(items))
		for _, n := range items {
			if n == nil || !n.IsDir {
				continue
			}
			dirs = append(dirs, completionCandidate(n.Path+"/", "directory"))
		}
		return dirs, nil
	})(cmd, args, toComplete)
}

func vaultIDCompletion(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return completeIDs(func(ctx context.Context, c *kha.Client) ([]string, error) {
		vaultsResult, err := c.Vaults.List(ctx, kha.ListVaultsRequest{})
		if err != nil {
			return nil, err
		}
		vaults := nonNilItems(vaultsResult.Body.Items)

		ids := make([]string, len(vaults))
		for i, v := range vaults {
			ids[i] = completionCandidate(v.ID, v.DisplayName)
		}
		return ids, nil
	})(cmd, args, toComplete)
}

// vaultCredentialIDCompletion completes the Vault ID at the first positional
// and the credential IDs inside it at the second.
func vaultCredentialIDCompletion(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return vaultIDCompletion(cmd, args, toComplete)
	}
	return completeIDsAt(1, func(ctx context.Context, c *kha.Client, args []string) ([]string, error) {
		credsResult, err := c.Vaults.ListCredentials(ctx, kha.ListVaultCredentialsRequest{ID: args[0]})
		if err != nil {
			return nil, err
		}
		creds := nonNilItems(credsResult.Body.Items)

		ids := make([]string, len(creds))
		for i, cred := range creds {
			ids[i] = completionCandidate(cred.ID, cred.DisplayName)
		}
		return ids, nil
	})(cmd, args, toComplete)
}

func triggerIDCompletion(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return completeIDs(func(ctx context.Context, c *kha.Client) ([]string, error) {
		triggersResult, err := c.Triggers.List(ctx, kha.ListTriggersRequest{})
		if err != nil {
			return nil, err
		}
		triggers := nonNilItems(triggersResult.Body.Items)

		ids := make([]string, len(triggers))
		for i, t := range triggers {
			ids[i] = completionCandidate(t.ID, t.Name+" ("+string(t.Status)+")")
		}
		return ids, nil
	})(cmd, args, toComplete)
}

// triggerRunIDCompletion completes the Trigger ID at the first positional and
// the run IDs of that Trigger at the second.
func triggerRunIDCompletion(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return triggerIDCompletion(cmd, args, toComplete)
	}
	return completeIDsAt(1, func(ctx context.Context, c *kha.Client, args []string) ([]string, error) {
		runsResult, err := c.Triggers.ListRuns(ctx, kha.ListTriggerRunsRequest{ID: args[0]})
		if err != nil {
			return nil, err
		}
		runs := nonNilItems(runsResult.Body.Items)

		ids := make([]string, 0, len(runs))
		for _, r := range runs {
			ids = append(ids, completionCandidate(r.ID, string(r.Status)+" "+output.Time(r.CreatedAt)))
		}
		return ids, nil
	})(cmd, args, toComplete)
}

func environmentIDCompletion(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return completeIDs(func(ctx context.Context, c *kha.Client) ([]string, error) {
		envsResult, err := c.Environments.List(ctx, kha.ListEnvironmentsRequest{})
		if err != nil {
			return nil, err
		}
		envs := nonNilItems(envsResult.Body.Items)

		ids := make([]string, len(envs))
		for i, e := range envs {
			ids[i] = completionCandidate(e.ID, e.Name)
		}
		return ids, nil
	})(cmd, args, toComplete)
}

func pluginIDCompletion(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return completeIDs(func(ctx context.Context, c *kha.Client) ([]string, error) {
		pluginsResult, err := c.Plugins.List(ctx, kha.ListPluginsRequest{})
		if err != nil {
			return nil, err
		}
		plugins := nonNilItems(pluginsResult.Body.Items)

		ids := make([]string, len(plugins))
		for i, p := range plugins {
			ids[i] = completionCandidate(p.ID, p.DisplayName)
		}
		return ids, nil
	})(cmd, args, toComplete)
}

// pluginVersionCompletion completes the Plugin ID at the first positional and
// the published semantic versions of that Plugin at the second.
func pluginVersionCompletion(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return pluginIDCompletion(cmd, args, toComplete)
	}
	return completeIDsAt(1, func(ctx context.Context, c *kha.Client, args []string) ([]string, error) {
		versionsResult, err := c.Plugins.ListVersions(ctx, kha.ListPluginVersionsRequest{ID: args[0]})
		if err != nil {
			return nil, err
		}
		versions := nonNilItems(versionsResult.Body.Items)

		ids := make([]string, len(versions))
		for i, v := range versions {
			ids[i] = completionCandidate(v.Version, v.Name)
		}
		return ids, nil
	})(cmd, args, toComplete)
}

// pluginBundleCompletion completes the Plugin ID at the first positional and
// leaves the second to the shell's own file completion — the bundle is a
// local path, not a server-side identifier.
func pluginBundleCompletion(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return pluginIDCompletion(cmd, args, toComplete)
	}
	return nil, cobra.ShellCompDirectiveDefault
}

func memoryStoreIDCompletion(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return completeIDs(func(ctx context.Context, c *kha.Client) ([]string, error) {
		storesResult, err := c.MemoryStores.List(ctx, kha.ListMemoryStoresRequest{})
		if err != nil {
			return nil, err
		}
		stores := nonNilItems(storesResult.Body.Items)

		ids := make([]string, len(stores))
		for i, s := range stores {
			ids[i] = completionCandidate(s.ID, s.Name)
		}
		return ids, nil
	})(cmd, args, toComplete)
}

// memoryIDCompletion completes the Memory Store ID at the first positional
// and the memory file IDs inside it at the second.
func memoryIDCompletion(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return memoryStoreIDCompletion(cmd, args, toComplete)
	}
	return completeIDsAt(1, func(ctx context.Context, c *kha.Client, args []string) ([]string, error) {
		memoriesResult, err := c.MemoryStores.ListMemories(ctx, kha.ListMemoriesRequest{ID: args[0]})
		if err != nil {
			return nil, err
		}
		memories := nonNilItems(memoriesResult.Body.Items)

		ids := make([]string, len(memories))
		for i, m := range memories {
			ids[i] = completionCandidate(m.ID, m.Path)
		}
		return ids, nil
	})(cmd, args, toComplete)
}

// memoryVersionIDCompletion completes the Memory Store and Memory IDs at the
// first two positionals and the version IDs of that Memory at the third.
func memoryVersionIDCompletion(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) < 2 {
		return memoryIDCompletion(cmd, args, toComplete)
	}
	return completeIDsAt(2, func(ctx context.Context, c *kha.Client, args []string) ([]string, error) {
		versionsResult, err := c.MemoryStores.ListMemoryVersions(ctx, kha.ListMemoryVersionsRequest{ID: args[0], MemoryID: args[1]})
		if err != nil {
			return nil, err
		}
		versions := nonNilItems(versionsResult.Body.Items)

		ids := make([]string, len(versions))
		for i, v := range versions {
			ids[i] = completionCandidate(v.ID, string(v.Operation)+" "+v.Path)
		}
		return ids, nil
	})(cmd, args, toComplete)
}

func skillIDCompletion(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return completeIDs(func(ctx context.Context, c *kha.Client) ([]string, error) {
		skillsResult, err := c.Skills.List(ctx, kha.ListSkillsRequest{})
		if err != nil {
			return nil, err
		}
		skills := nonNilItems(skillsResult.Body.Items)

		ids := make([]string, len(skills))
		for i, s := range skills {
			ids[i] = completionCandidate(s.ID, s.Name)
		}
		return ids, nil
	})(cmd, args, toComplete)
}

// skillContentCompletion completes the Skill ID at the first positional and
// the version identifiers of that Skill at the second.
func skillContentCompletion(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return skillIDCompletion(cmd, args, toComplete)
	}
	return completeIDsAt(1, func(ctx context.Context, c *kha.Client, args []string) ([]string, error) {
		versionsResult, err := c.Skills.ListVersions(ctx, kha.ListSkillVersionsRequest{ID: args[0]})
		if err != nil {
			return nil, err
		}
		versions := nonNilItems(versionsResult.Body.Items)

		ids := make([]string, len(versions))
		for i, v := range versions {
			ids[i] = completionCandidate(v.Version, v.Name)
		}
		return ids, nil
	})(cmd, args, toComplete)
}

func artifactIDCompletion(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return completeIDs(func(ctx context.Context, c *kha.Client) ([]string, error) {
		artifactsResult, err := c.Artifacts.List(ctx, kha.ListArtifactsRequest{})
		if err != nil {
			return nil, err
		}
		artifacts := nonNilItems(artifactsResult.Body.Items)

		ids := make([]string, len(artifacts))
		for i, a := range artifacts {
			ids[i] = completionCandidate(a.ID, a.Name+" (v"+a.Version+")")
		}
		return ids, nil
	})(cmd, args, toComplete)
}

// artifactVersionFlagCompletion completes artifacts get/download --version
// from the versions of the Artifact named by the first positional.
func artifactVersionFlagCompletion(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return completeFlagIDs(func(ctx context.Context, c *kha.Client, args []string) ([]string, error) {
		if len(args) != 1 {
			return nil, nil
		}
		versionsResult, err := c.Artifacts.ListVersions(ctx, kha.ListArtifactVersionsRequest{ID: args[0]})
		if err != nil {
			return nil, err
		}
		versions := nonNilItems(versionsResult.Body.Items)

		ids := make([]string, len(versions))
		for i, a := range versions {
			ids[i] = completionCandidate(a.Version, a.Name)
		}
		return ids, nil
	})(cmd, args, toComplete)
}

func fileIDCompletion(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return completeIDs(func(ctx context.Context, c *kha.Client) ([]string, error) {
		filesResult, err := c.Files.List(ctx, kha.ListFilesRequest{})
		if err != nil {
			return nil, err
		}
		files := nonNilItems(filesResult.Body.Items)

		ids := make([]string, len(files))
		for i, f := range files {
			ids[i] = completionCandidate(f.ID, f.Filename)
		}
		return ids, nil
	})(cmd, args, toComplete)
}
