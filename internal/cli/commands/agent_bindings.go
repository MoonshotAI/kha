package commands

import (
	"github.com/MoonshotAI/kha"

	"github.com/spf13/cobra"
)

type agentBindingFlags struct {
	skills, plugins *refListFlags
}

func addAgentBindingFlags(cmd *cobra.Command) *agentBindingFlags {
	f := &agentBindingFlags{}
	f.skills = addRefListFlag(cmd, "skill", "Skill ID, optionally id@version (repeatable; replaces the entire Skill list; use --file to clear)")
	f.plugins = addRefListFlag(cmd, "plugin", "Plugin ID, optionally id@version (repeatable; replaces the entire Plugin list; use --file to clear)")
	cmd.MarkFlagsMutuallyExclusive("file", "skill")
	cmd.MarkFlagsMutuallyExclusive("file", "plugin")
	mustRegisterFlagCompletion(cmd, "skill", skillIDCompletion)
	mustRegisterFlagCompletion(cmd, "plugin", pluginIDCompletion)
	return f
}

func (f *agentBindingFlags) resolve() ([]*kha.SkillRef, []*kha.PluginRef, error) {
	var skills []*kha.SkillRef
	for _, raw := range f.skills.ids {
		id, version := splitRef(raw)
		skills = append(skills, &kha.SkillRef{SkillID: id, Version: version})
	}
	var plugins []*kha.PluginRef
	for _, raw := range f.plugins.ids {
		id, version := splitRef(raw)
		plugins = append(plugins, &kha.PluginRef{PluginID: id, Version: version})
	}
	return skills, plugins, nil
}
