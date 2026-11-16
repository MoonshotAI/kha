package commands

import (
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/MoonshotAI/kha"

	"github.com/spf13/cobra"

	"github.com/MoonshotAI/kha/internal/cli/output"
)

var pluginHeaders = []string{"ID", "NAME", "DISPLAY NAME", "VERSION", "SCOPE", "STATUS", "LATEST"}

func pluginRow(p *kha.Plugin) []string {
	return []string{p.ID, p.Name, p.DisplayName, p.Version, string(p.Scope), string(p.Status), p.LatestVersion}
}

var pluginVersionHeaders = []string{"VERSION", "NAME", "DESCRIPTION", "CREATED"}

func pluginVersionRow(v *kha.PluginVersion) []string {
	return []string{v.Version, v.Name, v.Description, output.Time(v.CreatedAt)}
}

var pluginIssueHeaders = []string{"SEVERITY", "CODE", "FIELD", "MESSAGE"}

func pluginIssueRows(r *kha.PluginValidationReport) [][]string {
	rows := make([][]string, 0, len(r.Issues))
	for _, issue := range r.Issues {
		rows = append(rows, []string{string(issue.Severity), issue.Code, output.Dash(valueOr(issue.Field, "")), issue.Message})
	}
	return rows
}

func newPluginsCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plugins",
		Short: "Browse the Plugin catalog and publish project-owned Plugins",
	}
	cmd.AddCommand(
		newPluginsListCmd(g),
		newPluginsGetCmd(g),
		newPluginsCreateCmd(g),
		newPluginsValidateCmd(g),
		newPluginsArchiveCmd(g),
		newPluginsVersionsCmd(g),
		newPluginsContentCmd(g),
	)
	return cmd
}

func newPluginsListCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List available Plugins",
		Args:  cobra.NoArgs,
	}
	lf := addListFlags(cmd)
	var query, scope, category string
	cmd.Flags().StringVarP(&query, "query", "q", "", "case-insensitive match over name, description, and keywords")
	cmd.Flags().StringVar(&scope, "scope", "", "ownership scope to list: official or project (default: both)")
	mustRegisterFlagCompletion(cmd, "scope", completeFlagStatic("official", "project"))
	cmd.Flags().StringVar(&category, "category", "", "filter by display category: finance, productivity, creative_3d, dev_tools, learning, featured")
	mustRegisterFlagCompletion(cmd, "category", completeFlagStatic("finance", "productivity", "creative_3d", "dev_tools", "learning", "featured"))
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		if err := lf.validate(); err != nil {
			return err
		}
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		params := kha.ListPluginsRequest{PageSize: lf.pageSize(), PageToken: lf.token()}
		if query != "" {
			params.Query = new(query)
		}
		if scope != "" {
			params.Scope = new(kha.ResourceScope(scope))
		}
		if category != "" {
			params.Category = new(kha.PluginCategory(category))
		}
		if lf.all {
			return printAll(cmd, g, pluginHeaders, client.Plugins.All(cmd.Context(), params),
				pluginRow)
		}
		itemsResult, err := client.Plugins.List(cmd.Context(), params)
		if err != nil {
			return err
		}
		page := itemsResult.Body
		page.Items = nonNilItems(page.Items)
		page.NextPageToken = nonEmptyPageToken(page.NextPageToken)

		rows := make([][]string, 0, len(page.Items))
		for _, p := range page.Items {
			rows = append(rows, pluginRow(p))
		}
		return printList(cmd, g, pluginHeaders, rows, page)
	})
	return cmd
}

func newPluginsGetCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:               "get <id>",
		Short:             "Get one Plugin",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: pluginIDCompletion,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			client, _, err := newClient(g)
			if err != nil {
				return err
			}
			pluginResult, err := client.Plugins.Get(cmd.Context(), kha.GetPluginRequest{ID: args[0]})
			if err != nil {
				return err
			}
			plugin := pluginResult.Body

			return printOne(cmd, g, pluginHeaders, pluginRow(plugin), plugin)
		}),
	}
}

// newPluginsCreateCmd uploads a .zip bundle; its kimi.plugin.json manifest
// defines the Plugin, so there are no field flags and no JSON body.
func newPluginsCreateCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "create <bundle.zip>",
		Short: "Publish a Plugin and its first version from a bundle upload (streamed from disk — large bundles are not loaded into memory)",
		Args:  cobra.ExactArgs(1),
		Example: `  kha plugins validate ./data-tools.zip
  kha plugins create ./data-tools.zip`,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			f, err := os.Open(args[0])
			if err != nil {
				return err
			}
			defer func() { _ = f.Close() }()
			client, _, err := newClient(g)
			if err != nil {
				return err
			}
			respResult, err := client.Plugins.Create(cmd.Context(), kha.CreatePluginRequest{Body: &kha.CreatePluginBody{File: kha.File{Filename: filepath.Base(args[0]), Body: f}}})
			if err != nil {
				return err
			}
			resp := respResult.Body

			noteReport(cmd, &resp.Report)
			note(cmd, "plugin %s published at version %s", resp.Plugin.ID, resp.Version.Version)
			return printOne(cmd, g, pluginHeaders, pluginRow(&resp.Plugin), resp)
		}),
	}
}

// newPluginsValidateCmd reports every finding of a bundle without publishing
// it. Findings remain ordinary output; an unpublishable bundle exits with 1.
func newPluginsValidateCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "validate <bundle.zip>",
		Short: "Check a Plugin bundle and report every finding without publishing it",
		Long:  "Check a Plugin bundle without publishing it. Print every finding and exit with code 1 when the bundle cannot be published; otherwise exit with code 0.",
		Args:  cobra.ExactArgs(1),
		RunE: run(func(cmd *cobra.Command, args []string) error {
			f, err := os.Open(args[0])
			if err != nil {
				return err
			}
			defer func() { _ = f.Close() }()
			client, _, err := newClient(g)
			if err != nil {
				return err
			}
			respResult, err := client.Plugins.Validate(cmd.Context(), kha.ValidatePluginRequest{Body: &kha.ValidatePluginBody{File: kha.File{Filename: filepath.Base(args[0]), Body: f}}})
			if err != nil {
				return err
			}
			resp := respResult.Body

			if resp.Report.Ok {
				note(cmd, "bundle %s can be published", filepath.Base(args[0]))
			} else {
				note(cmd, "bundle %s cannot be published", filepath.Base(args[0]))
			}
			if err := printList(cmd, g, pluginIssueHeaders, pluginIssueRows(&resp.Report), resp, "report.issues"); err != nil {
				return err
			}
			if !resp.Report.Ok {
				return &reportedError{err: errors.New("bundle cannot be published")}
			}
			return nil
		}),
	}
}

// noteReport surfaces a successful publish's warnings, which table output
// otherwise drops (it renders the Plugin, not the report).
func noteReport(cmd *cobra.Command, r *kha.PluginValidationReport) {
	for _, issue := range r.Issues {
		note(cmd, "%s: %s: %s", issue.Severity, issue.Code, issue.Message)
	}
}

func newPluginsArchiveCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:               "archive <id>",
		Short:             "Archive a project-owned Plugin (its name becomes reusable)",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: pluginIDCompletion,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			client, _, err := newClient(g)
			if err != nil {
				return err
			}
			if _, err := client.Plugins.Archive(cmd.Context(), kha.ArchivePluginRequest{ID: args[0]}); err != nil {
				return err
			}
			note(cmd, "plugin %s archived", args[0])
			return nil
		}),
	}
}

func newPluginsVersionsCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "versions <id>",
		Short:             "List a Plugin's published versions (newest semantic version first)",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: pluginIDCompletion,
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
		params := kha.ListPluginVersionsRequest{ID: args[0], PageSize: lf.pageSize(), PageToken: lf.token()}
		if lf.all {
			return printAll(cmd, g, pluginVersionHeaders, client.Plugins.AllVersions(cmd.Context(), params),
				pluginVersionRow)
		}
		itemsResult, err := client.Plugins.ListVersions(cmd.Context(), params)
		if err != nil {
			return err
		}
		page := itemsResult.Body
		page.Items = nonNilItems(page.Items)
		page.NextPageToken = nonEmptyPageToken(page.NextPageToken)

		rows := make([][]string, 0, len(page.Items))
		for _, v := range page.Items {
			rows = append(rows, pluginVersionRow(v))
		}
		return printList(cmd, g, pluginVersionHeaders, rows, page)
	})
	cmd.AddCommand(newPluginsVersionsCreateCmd(g))
	return cmd
}

func newPluginsVersionsCreateCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:               "create <id> <bundle.zip>",
		Short:             "Publish one more immutable version of a Plugin from a bundle upload",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: pluginBundleCompletion,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			f, err := os.Open(args[1])
			if err != nil {
				return err
			}
			defer func() { _ = f.Close() }()
			client, _, err := newClient(g)
			if err != nil {
				return err
			}
			respResult, err := client.Plugins.CreateVersion(cmd.Context(), kha.CreatePluginVersionRequest{ID: args[0], Body: &kha.CreatePluginVersionBody{File: kha.File{Filename: filepath.Base(args[1]), Body: f}}})
			if err != nil {
				return err
			}
			resp := respResult.Body

			noteReport(cmd, &resp.Report)
			note(cmd, "plugin %s published at version %s", args[0], resp.Version.Version)
			return printOne(cmd, g, pluginVersionHeaders, pluginVersionRow(&resp.Version), resp)
		}),
	}
}

// newPluginsContentCmd streams one version's bundle to stdout (default,
// pipe-safe) or into a file with --out.
func newPluginsContentCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "content <id> <version>",
		Short:             "Download a Plugin version's bundle (stdout or --out file)",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: pluginVersionCompletion,
		Example: `  kha plugins content plg_xxx 1.0.0 --out bundle.zip
  kha plugins content plg_xxx 1.0.0 | unzip -l /dev/stdin`,
	}
	var outPath string
	// --out has no -o shorthand: the global -o is --output, and a local
	// shorthand would collide with it at flag-merge time.
	cmd.Flags().StringVar(&outPath, "out", "", "destination file (default: the bundle bytes go to stdout)")
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		rcResult, err := client.Plugins.GetVersionContent(cmd.Context(), kha.GetPluginVersionContentRequest{ID: args[0], Version: args[1]})
		if err != nil {
			return err
		}
		rc := rcResult.Body

		defer func() { _ = rc.Close() }()
		if outPath == "" {
			_, err := io.Copy(cmd.OutOrStdout(), rc)
			return err
		}
		// --out is an explicitly user-chosen path: overwrite it (curl -o
		// semantics); the --force guard exists for server-chosen names.
		n, err := writeToFile(outPath, rc, true)
		if err != nil {
			return err
		}
		note(cmd, "plugin %s version %s downloaded to %s (%s)", args[0], args[1], outPath, output.Bytes(n))
		return nil
	})
	return cmd
}
