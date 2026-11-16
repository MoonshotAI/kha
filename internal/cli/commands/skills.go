package commands

import (
	"io"
	"os"
	"path/filepath"

	"github.com/MoonshotAI/kha"

	"github.com/spf13/cobra"

	"github.com/MoonshotAI/kha/internal/cli/output"
)

var skillHeaders = []string{"ID", "NAME", "LATEST", "STATUS", "CREATED"}

func skillRow(s *kha.Skill) []string {
	return []string{s.ID, s.Name, s.LatestVersion, string(s.Status), output.Time(s.CreatedAt)}
}

func newSkillsCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skills",
		Short: "Manage Skills and their immutable versions",
	}
	cmd.AddCommand(
		newSkillsListCmd(g),
		newSkillsGetCmd(g),
		newSkillsCreateCmd(g),
		newSkillsDeleteCmd(g),
		newSkillsArchiveCmd(g),
		newSkillsVersionsCmd(g),
		newSkillsContentCmd(g),
	)
	return cmd
}

func newSkillsListCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List Skills (project Skills and enabled official Skills)",
		Args:  cobra.NoArgs,
	}
	lf := addListFlags(cmd)
	var category string
	cmd.Flags().StringVar(&category, "category", "", "filter by display category: featured, office_productivity, business_finance, content_creation, academic_research, marketing_growth, engineering")
	mustRegisterFlagCompletion(cmd, "category", completeFlagStatic("featured", "office_productivity", "business_finance", "content_creation", "academic_research", "marketing_growth", "engineering"))
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		if err := lf.validate(); err != nil {
			return err
		}
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		params := kha.ListSkillsRequest{PageSize: lf.pageSize(), PageToken: lf.token()}
		if category != "" {
			params.Category = new(kha.SkillCategory(category))
		}
		if lf.all {
			return printAll(cmd, g, skillHeaders, client.Skills.All(cmd.Context(), params),
				skillRow)
		}
		itemsResult, err := client.Skills.List(cmd.Context(), params)
		if err != nil {
			return err
		}
		page := itemsResult.Body
		page.Items = nonNilItems(page.Items)
		page.NextPageToken = nonEmptyPageToken(page.NextPageToken)

		rows := make([][]string, 0, len(page.Items))
		for _, s := range page.Items {
			rows = append(rows, skillRow(s))
		}
		return printList(cmd, g, skillHeaders, rows, page)
	})
	return cmd
}

func newSkillsGetCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:               "get <id>",
		Short:             "Get one Skill",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: skillIDCompletion,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			client, _, err := newClient(g)
			if err != nil {
				return err
			}
			skillResult, err := client.Skills.Get(cmd.Context(), kha.GetSkillRequest{ID: args[0]})
			if err != nil {
				return err
			}
			skill := skillResult.Body

			return printOne(cmd, g, skillHeaders, skillRow(skill), skill)
		}),
	}
}

// newSkillsCreateCmd uploads a .zip/.skill bundle; the package defines the
// Skill, so there are no field flags and no JSON body.
func newSkillsCreateCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "create <bundle.zip|bundle.skill>",
		Short: "Create a Skill from a bundle upload (streamed from disk — large bundles are not loaded into memory)",
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
			respResult, err := client.Skills.Create(cmd.Context(), kha.CreateSkillRequest{Body: &kha.CreateSkillBody{File: kha.File{Filename: filepath.Base(args[0]), Body: f}}})
			if err != nil {
				return err
			}
			resp := respResult.Body

			note(cmd, "skill bundle %s uploaded", filepath.Base(args[0]))
			return printOne(cmd, g, skillHeaders, skillRow(&resp.Skill), resp)
		}),
	}
}

func newSkillsDeleteCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:               "delete <id>",
		Short:             "Delete a Skill",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: skillIDCompletion,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			client, _, err := newClient(g)
			if err != nil {
				return err
			}
			if _, err := client.Skills.Delete(cmd.Context(), kha.DeleteSkillRequest{ID: args[0]}); err != nil {
				return err
			}
			note(cmd, "skill %s deleted", args[0])
			return nil
		}),
	}
}

func newSkillsArchiveCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:               "archive <id>",
		Short:             "Archive a Skill (its name becomes reusable)",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: skillIDCompletion,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			client, _, err := newClient(g)
			if err != nil {
				return err
			}
			if _, err := client.Skills.Archive(cmd.Context(), kha.ArchiveSkillRequest{ID: args[0]}); err != nil {
				return err
			}
			note(cmd, "skill %s archived", args[0])
			return nil
		}),
	}
}

var skillVersionHeaders = []string{"VERSION", "NAME", "DESCRIPTION", "CREATED"}

func skillVersionRow(v *kha.SkillVersion) []string {
	return []string{v.Version, v.Name, v.Description, output.Time(v.CreatedAt)}
}

func newSkillsVersionsCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "versions <id>",
		Short:             "List a Skill's immutable versions (newest first)",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: skillIDCompletion,
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
		params := kha.ListSkillVersionsRequest{ID: args[0], PageSize: lf.pageSize(), PageToken: lf.token()}
		if lf.all {
			return printAll(cmd, g, skillVersionHeaders, client.Skills.AllVersions(cmd.Context(), params),
				skillVersionRow)
		}
		itemsResult, err := client.Skills.ListVersions(cmd.Context(), params)
		if err != nil {
			return err
		}
		page := itemsResult.Body
		page.Items = nonNilItems(page.Items)
		page.NextPageToken = nonEmptyPageToken(page.NextPageToken)

		rows := make([][]string, 0, len(page.Items))
		for _, v := range page.Items {
			rows = append(rows, skillVersionRow(v))
		}
		return printList(cmd, g, skillVersionHeaders, rows, page)
	})
	return cmd
}

// newSkillsContentCmd streams one version's bundle to stdout (default,
// pipe-safe) or into a file with --out.
func newSkillsContentCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "content <id> <version>",
		Short:             "Download a Skill version's bundle (stdout or --out file)",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: skillContentCompletion,
		Example: `  kha skills content skill_xxx 3 --out bundle.zip
  kha skills content skill_xxx 3 | tar -tvf -`,
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
		rcResult, err := client.Skills.GetVersionContent(cmd.Context(), kha.GetSkillVersionContentRequest{ID: args[0], Version: args[1]})
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
		note(cmd, "skill %s version %s downloaded to %s (%s)", args[0], args[1], outPath, output.Bytes(n))
		return nil
	})
	return cmd
}
