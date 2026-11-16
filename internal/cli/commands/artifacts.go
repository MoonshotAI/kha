package commands

import (
	"path/filepath"

	"github.com/MoonshotAI/kha"

	"github.com/spf13/cobra"

	"github.com/MoonshotAI/kha/internal/cli/output"
)

var artifactHeaders = []string{"ID", "NAME", "VERSION", "SIZE", "DELIVERED"}

func artifactRow(a *kha.Artifact) []string {
	return []string{string(a.ID), a.Name, a.Version, output.Bytes(a.SizeBytes), output.Time(a.DeliveredAt)}
}

func newArtifactsCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "artifacts",
		Short: "Browse and download delivered Artifacts",
	}
	cmd.AddCommand(
		newArtifactsListCmd(g),
		newArtifactsGetCmd(g),
		newArtifactsDeleteCmd(g),
		newArtifactsDownloadCmd(g),
		newArtifactsVersionsCmd(g),
	)
	return cmd
}

func newArtifactsListCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List delivered Artifacts (latest version each, most recent first)",
		Args:  cobra.NoArgs,
	}
	lf := addListFlags(cmd)
	var sessionID string
	cmd.Flags().StringVar(&sessionID, "session", "", "restrict to Artifacts produced by this Session")
	mustRegisterFlagCompletion(cmd, "session", sessionIDCompletion)
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		if err := lf.validate(); err != nil {
			return err
		}
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		params := kha.ListArtifactsRequest{PageSize: lf.pageSize(), PageToken: lf.token()}
		if sessionID != "" {
			params.SessionID = new(sessionID)
		}
		if lf.all {
			return printAll(cmd, g, artifactHeaders, client.Artifacts.All(cmd.Context(), params),
				artifactRow)
		}
		artifactsResult, err := client.Artifacts.List(cmd.Context(), params)
		if err != nil {
			return err
		}
		page := artifactsResult.Body
		page.Items = nonNilItems(page.Items)
		page.NextPageToken = nonEmptyPageToken(page.NextPageToken)

		rows := make([][]string, 0, len(page.Items))
		for _, item := range page.Items {
			rows = append(rows, artifactRow(item))
		}
		return printList(cmd, g, artifactHeaders, rows, page)
	})
	return cmd
}

func newArtifactsGetCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "get <id>",
		Short:             "Get one Artifact's metadata",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: artifactIDCompletion,
	}
	var version string
	cmd.Flags().StringVar(&version, "version", "", "exact version to get (default: latest)")
	mustRegisterFlagCompletion(cmd, "version", artifactVersionFlagCompletion)
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		artifactResult, err := client.Artifacts.Get(cmd.Context(), kha.GetArtifactRequest{ID: args[0], Version: optionalString(version)})
		if err != nil {
			return err
		}
		artifact := artifactResult.Body

		return printOne(cmd, g, artifactHeaders, artifactRow(artifact), artifact)
	})
	return cmd
}

func newArtifactsDeleteCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:               "delete <id>",
		Short:             "Delete an Artifact",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: artifactIDCompletion,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			client, _, err := newClient(g)
			if err != nil {
				return err
			}
			if _, err := client.Artifacts.Delete(cmd.Context(), kha.DeleteArtifactRequest{ID: args[0]}); err != nil {
				return err
			}
			note(cmd, "artifact %s deleted", args[0])
			return nil
		}),
	}
}

func newArtifactsDownloadCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "download <id>",
		Short:             "Download an Artifact's content",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: artifactIDCompletion,
	}
	var outPath, version string
	var force bool
	// --out has no -o shorthand: the global -o is --output, and a local
	// shorthand would collide with it at flag-merge time.
	cmd.Flags().StringVar(&outPath, "out", "", "destination path (default: the Artifact's name in the current directory)")
	cmd.Flags().StringVar(&version, "version", "", "exact version to download (default: latest)")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite the destination file if it already exists")
	mustRegisterFlagCompletion(cmd, "version", artifactVersionFlagCompletion)
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		dest := outPath
		if dest == "" {
			// The default destination is the requested version's name;
			// --version may select a name different from the latest version.
			artifactResult, err := client.Artifacts.Get(cmd.Context(), kha.GetArtifactRequest{ID: args[0], Version: optionalString(version)})
			if err != nil {
				return err
			}
			artifact := artifactResult.Body

			// The server-provided name is untrusted: strip any directory
			// components so a hostile name cannot escape the current
			// directory.
			dest = filepath.Base(artifact.Name)
		}
		rcResult, err := client.Artifacts.GetContent(cmd.Context(), kha.GetArtifactContentRequest{ID: args[0], Version: optionalString(version)})
		if err != nil {
			return err
		}
		rc := rcResult.Body

		defer func() { _ = rc.Close() }()
		n, err := writeToFile(dest, rc, force)
		if err != nil {
			return err
		}
		note(cmd, "artifact %s downloaded to %s (%s)", args[0], dest, output.Bytes(n))
		return nil
	})
	return cmd
}

func newArtifactsVersionsCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "versions <id>",
		Short:             "List an Artifact's versions (newest first)",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: artifactIDCompletion,
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
		params := kha.ListArtifactVersionsRequest{ID: args[0], PageSize: lf.pageSize(), PageToken: lf.token()}
		if lf.all {
			return printAll(cmd, g, artifactHeaders, client.Artifacts.AllVersions(cmd.Context(), params),
				artifactRow)
		}
		artifactsResult, err := client.Artifacts.ListVersions(cmd.Context(), params)
		if err != nil {
			return err
		}
		page := artifactsResult.Body
		page.Items = nonNilItems(page.Items)
		page.NextPageToken = nonEmptyPageToken(page.NextPageToken)

		rows := make([][]string, 0, len(page.Items))
		for _, item := range page.Items {
			rows = append(rows, artifactRow(item))
		}
		return printList(cmd, g, artifactHeaders, rows, page)
	})
	return cmd
}
